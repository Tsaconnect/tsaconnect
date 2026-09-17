package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// ClubkonnectClient talks to ClubKonnect (Nellobyte Systems), the prepaid
// utility-bill vendor: airtime, data, electricity, cable TV, betting and exam
// e-PINs, vended from a company Naira float held in THEIR wallet.
//
// API quirks this client absorbs so nothing above it has to know:
//   - Everything is HTTP GET with query-string params; auth is UserID+APIKey in
//     the query string. The key is a credential in a URL, so every error path
//     runs through redact() before the text can reach a log.
//   - JSON bodies are served as text/html, and some error paths (Cancel) return
//     plain text. Decoding goes by content, never by Content-Type.
//   - `status` strings lie: ORDER_COMPLETED includes 201 "Network Unresponsive",
//     which is NOT success — ClubKonnect keeps retrying it for up to an hour.
//     Success is statusCode == 200 and nothing else (decideVend).
//   - Public catalogs need no credentials but have real data defects, so they
//     are curated here (Showmax dropped, discount strings ignored) rather than
//     forwarded raw to the app.
var (
	ErrClubkonnectDisabled = errors.New("bill vendor not configured")
	// ErrBillFloatLow means OUR ClubKonnect wallet ran dry (codes 412/415/416/
	// 417) — a platform incident, never a user error. Callers must alert, not
	// blame the customer.
	ErrBillFloatLow = errors.New("bill vendor float exhausted")
	// ErrBillCustomerNotFound: a verify lookup found no account holder. A
	// sentinel so handlers can pass this one through to users while treating
	// every other vendor error as internal.
	ErrBillCustomerNotFound = errors.New("customer not found")
)

// vendTimeout covers ClubKonnect's up-to-60s network wait before it parks an
// order as 201; catalogTimeout is for the cheap public reads.
const (
	ckVendTimeout    = 75 * time.Second
	ckReadTimeout    = 20 * time.Second
	ckCatalogCacheTTL = time.Hour
)

// BillVendResult is one vend/query response, normalized.
type BillVendResult struct {
	OrderID    string
	StatusCode int    // numeric statuscode; 0 when the body carried none
	Status     string // ClubKonnect group string (ORDER_RECEIVED, ORDER_COMPLETED…)
	Remark     string
	// Delivery is the customer-facing artifact of a successful vend when the
	// response carries one: a prepaid-meter token, an exam e-PIN.
	Delivery string
}

// VendDecision is the only status vocabulary BillService reasons in.
type VendDecision int

const (
	VendPending VendDecision = iota // non-terminal: keep polling
	VendSuccess                     // statusCode 200, and only 200
	VendFailed                      // terminal: safe to reverse the payment
)

// decideVend maps a ClubKonnect statuscode to a decision, plus whether the
// failure was our float running dry. Codes from the public dispute-resolution
// table. Unknown codes are PENDING on purpose: a wrong "failed" refunds a bill
// that may still deliver (double-spend), while a wrong "pending" merely waits
// for the reconciler — the reconciler escalates orders that never resolve.
func decideVend(statusCode int) (VendDecision, bool) {
	switch statusCode {
	case 200:
		return VendSuccess, false
	case 412, 415, 416, 417:
		return VendFailed, true // insufficient wallet/store balance — our float
	}
	switch {
	case statusCode >= 400 && statusCode < 500:
		return VendFailed, false // validation/credential errors — terminal
	case statusCode >= 500 && statusCode < 600:
		return VendFailed, false // ORDER_CANCELLED group — terminal
	default:
		// 100/199 received, 201/299 completed-but-unconfirmed, 300/399 processed,
		// 600s on-hold, and anything undocumented.
		return VendPending, false
	}
}

type ClubkonnectClient struct {
	baseURL       string
	userID        string
	apiKey        string
	dataplanField string
	httpc         *http.Client

	mu    sync.Mutex
	cache map[string]ckCacheEntry
}

type ckCacheEntry struct {
	body    []byte
	fetched time.Time
}

func NewClubkonnectClient(cfg *config.Config) *ClubkonnectClient {
	return &ClubkonnectClient{
		baseURL:       strings.TrimRight(cfg.ClubkonnectBaseURL, "/"),
		userID:        cfg.ClubkonnectUserID,
		apiKey:        cfg.ClubkonnectAPIKey,
		dataplanField: cfg.ClubkonnectDataplanField,
		httpc:         &http.Client{Timeout: ckVendTimeout},
		cache:         map[string]ckCacheEntry{},
	}
}

func (c *ClubkonnectClient) IsEnabled() bool {
	return c != nil && c.userID != "" && c.apiKey != ""
}

// redact strips credential material from any string that might travel to a
// log or an error response: whole query strings first (transport errors embed
// the full request URL), then the API key and UserID (both halves of the
// credential pair) raw and URL-encoded.
func (c *ClubkonnectClient) redact(s string) string {
	s = RedactURLQueries(s)
	for _, secret := range []string{c.apiKey, c.userID} {
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
		if esc := url.QueryEscape(secret); esc != secret {
			s = strings.ReplaceAll(s, esc, "[REDACTED]")
		}
	}
	return s
}

func (c *ClubkonnectClient) errf(format string, args ...any) error {
	return errors.New(c.redact(fmt.Sprintf(format, args...)))
}

// get performs one GET. authed adds the credential params. The returned body is
// trimmed (catalog endpoints lead with whitespace) but otherwise raw.
func (c *ClubkonnectClient) get(ctx context.Context, path string, params url.Values, authed bool) ([]byte, error) {
	if authed && !c.IsEnabled() {
		return nil, ErrClubkonnectDisabled
	}
	if params == nil {
		params = url.Values{}
	}
	if authed {
		params.Set("UserID", c.userID)
		params.Set("APIKey", c.apiKey)
	}
	u := c.baseURL + "/" + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, c.errf("clubkonnect request: %v", err)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, c.errf("clubkonnect: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, c.errf("clubkonnect read: %v", err)
	}
	body = []byte(strings.TrimSpace(string(body)))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.errf("clubkonnect HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	return body, nil
}

// snippet bounds how much of a body lands in an error message.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ─── vend + query ────────────────────────────────────────────────────────────

// parseVend decodes a vend/query body. ClubKonnect returns flat JSON objects
// with lowercase keys and string values ({"orderid":"...","statuscode":"100",
// "status":"ORDER_RECEIVED"}); non-JSON bodies become an error so callers fall
// back to the reconciler rather than guessing an outcome.
func (c *ClubkonnectClient) parseVend(body []byte) (BillVendResult, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return BillVendResult{}, c.errf("clubkonnect: non-JSON response: %s", snippet(body))
	}
	res := BillVendResult{}
	for k, v := range m {
		s := strings.TrimSpace(anyToString(v))
		switch strings.ToLower(k) {
		case "orderid", "order_id":
			res.OrderID = s
		case "statuscode", "status_code":
			res.StatusCode, _ = strconv.Atoi(s)
		case "status", "orderstatus":
			if res.Status == "" {
				res.Status = s
			}
		case "remark", "description":
			if res.Remark == "" {
				res.Remark = s
			}
		case "pin", "pins", "metertoken", "token", "purchasedtoken", "epin":
			if res.Delivery == "" && s != "" {
				res.Delivery = s
			}
		}
	}
	if res.StatusCode == 0 && res.Status == "" && res.OrderID == "" {
		return BillVendResult{}, c.errf("clubkonnect: unrecognized response: %s", snippet(body))
	}
	return res, nil
}

func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// JSON numbers; statuscodes and amounts are integral in practice.
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func (c *ClubkonnectClient) vend(ctx context.Context, path string, params url.Values) (BillVendResult, error) {
	body, err := c.get(ctx, path, params, true)
	if err != nil {
		return BillVendResult{}, err
	}
	return c.parseVend(body)
}

// VendAirtime tops up airtime. network is the catalog ID ("01" MTN, "02" Glo,
// "03" 9mobile, "04" Airtel).
func (c *ClubkonnectClient) VendAirtime(ctx context.Context, network, phone string, amountNGN int64, requestID string) (BillVendResult, error) {
	return c.vend(ctx, "APIAirtimeV1.asp", url.Values{
		"MobileNetwork": {network},
		"Amount":        {strconv.FormatInt(amountNGN, 10)},
		"MobileNumber":  {phone},
		"RequestID":     {requestID},
	})
}

// VendData buys a bundle. plan is a catalog BillPlan; which of its fields goes
// in DataPlan is CLUBKONNECT_DATAPLAN_FIELD (gated docs; see config).
func (c *ClubkonnectClient) VendData(ctx context.Context, network string, plan BillPlan, phone, requestID string) (BillVendResult, error) {
	dataPlan := plan.Code
	if strings.EqualFold(c.dataplanField, "id") {
		dataPlan = plan.ProductID
	}
	return c.vend(ctx, "APIDatabundleV1.asp", url.Values{
		"MobileNetwork": {network},
		"DataPlan":      {dataPlan},
		"MobileNumber":  {phone},
		"RequestID":     {requestID},
	})
}

// VendElectricity pays a meter. meterType is "01" prepaid / "02" postpaid
// (catalog PRODUCT_ID).
func (c *ClubkonnectClient) VendElectricity(ctx context.Context, disco, meterNo, meterType string, amountNGN int64, requestID string) (BillVendResult, error) {
	return c.vend(ctx, "APIElectricityV1.asp", url.Values{
		"ElectricCompany": {disco},
		"MeterNo":         {meterNo},
		"MeterType":       {meterType},
		"Amount":          {strconv.FormatInt(amountNGN, 10)},
		"RequestID":       {requestID},
	})
}

// VendCableTV subscribes a smartcard to a package. Per the public docs the
// package id rides in a param literally named MeterType (their naming, not
// ours); Package is sent too in case the gated spec differs.
func (c *ClubkonnectClient) VendCableTV(ctx context.Context, cableTV, packageCode, smartCardNo, requestID string) (BillVendResult, error) {
	return c.vend(ctx, "APICableTVV1.asp", url.Values{
		"CableTV":     {cableTV},
		"MeterType":   {packageCode},
		"Package":     {packageCode},
		"SmartCardNo": {smartCardNo},
		"RequestID":   {requestID},
	})
}

// VendBetting funds a betting account. Param names follow the community
// wrapper (the official per-endpoint page is login-gated) — verified by the
// live smoke test.
func (c *ClubkonnectClient) VendBetting(ctx context.Context, company, customerID string, amountNGN int64, requestID string) (BillVendResult, error) {
	return c.vend(ctx, "APIBettingV1.asp", url.Values{
		"BettingCompany": {company},
		"CustomerID":     {customerID},
		"Amount":         {strconv.FormatInt(amountNGN, 10)},
		"RequestID":      {requestID},
	})
}

// VendEpin buys a WAEC/JAMB exam PIN. provider is "waec" or "jamb"; examType is
// the catalog PRODUCT_CODE (e.g. "waecdirect", "utme"). The PIN arrives in the
// response body → BillVendResult.Delivery.
func (c *ClubkonnectClient) VendEpin(ctx context.Context, provider, examType, phone, requestID string) (BillVendResult, error) {
	path := "APIWAECV1.asp"
	if strings.EqualFold(provider, "jamb") {
		path = "APIJAMBV1.asp"
	}
	return c.vend(ctx, path, url.Values{
		"ExamType":  {examType},
		"PhoneNo":   {phone},
		"RequestID": {requestID},
	})
}

// QueryOrder fetches the current state of a vend by our RequestID — the
// reconciler's source of truth (callbacks are unauthenticated and untrusted).
func (c *ClubkonnectClient) QueryOrder(ctx context.Context, requestID string) (BillVendResult, error) {
	ctx, cancel := context.WithTimeout(ctx, ckReadTimeout)
	defer cancel()
	return c.vend(ctx, "APIQueryV1.asp", url.Values{"RequestID": {requestID}})
}

// CancelOrder asks ClubKonnect to cancel/refund an order (their wallet, not the
// user's). Plain-text responses are normal on this endpoint; failure to cancel
// is reported as an error and left to manual dispute.
func (c *ClubkonnectClient) CancelOrder(ctx context.Context, orderID string) error {
	ctx, cancel := context.WithTimeout(ctx, ckReadTimeout)
	defer cancel()
	body, err := c.get(ctx, "APICancelV1.asp", url.Values{"OrderID": {orderID}}, true)
	if err != nil {
		return err
	}
	if res, perr := c.parseVend(body); perr == nil {
		if d, _ := decideVend(res.StatusCode); d == VendFailed || res.StatusCode == 521 {
			return nil // terminal — cancelled, or already terminal (521 = no-op)
		}
		return c.errf("clubkonnect cancel: order not terminal (code %d)", res.StatusCode)
	}
	// Plain text, e.g. "refund failed, contact customer care".
	return c.errf("clubkonnect cancel: %s", snippet(body))
}

// WalletBalance reads our prepaid Naira float (whole ₦, fractions truncated).
func (c *ClubkonnectClient) WalletBalance(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, ckReadTimeout)
	defer cancel()
	body, err := c.get(ctx, "APIWalletBalanceV1.asp", nil, true)
	if err != nil {
		return 0, err
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return 0, c.errf("clubkonnect balance: non-JSON response: %s", snippet(body))
	}
	for k, v := range m {
		if strings.Contains(strings.ToLower(k), "balance") {
			s := strings.ReplaceAll(anyToString(v), ",", "")
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return 0, c.errf("clubkonnect balance: bad value %q", s)
			}
			return int64(f), nil
		}
	}
	return 0, c.errf("clubkonnect balance: no balance field: %s", snippet(body))
}

// ─── verify (customer lookup) ────────────────────────────────────────────────

// VerifyElectricity resolves the account holder on a meter. Empty name or an
// INVALID marker → not found.
func (c *ClubkonnectClient) VerifyElectricity(ctx context.Context, disco, meterNo string) (string, error) {
	return c.verify(ctx, "APIVerifyElectricityV1.asp", url.Values{
		"ElectricCompany": {disco},
		"MeterNo":         {meterNo},
	})
}

// VerifyCableTV resolves the account holder on a smartcard. Note the literal
// "V1.0" in the path — it is not a typo here, it is theirs.
func (c *ClubkonnectClient) VerifyCableTV(ctx context.Context, cableTV, smartCardNo string) (string, error) {
	return c.verify(ctx, "APIVerifyCableTVV1.0.asp", url.Values{
		"CableTV":     {cableTV},
		"SmartCardNo": {smartCardNo},
	})
}

func (c *ClubkonnectClient) verify(ctx context.Context, path string, params url.Values) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, ckReadTimeout)
	defer cancel()
	body, err := c.get(ctx, path, params, true)
	if err != nil {
		return "", err
	}
	name := extractCustomerName(body)
	if name == "" || strings.Contains(strings.ToUpper(name), "INVALID") {
		return "", ErrBillCustomerNotFound
	}
	return name, nil
}

// extractCustomerName digs a human name out of whatever shape the verify
// endpoint returns ({"customer_name":"…"}, {"content":{"Customer_Name":"…"}},
// or a bare string).
func extractCustomerName(body []byte) string {
	var s string
	if err := json.Unmarshal(body, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	return findNameKey(m, 0)
}

func findNameKey(m map[string]any, depth int) string {
	if depth > 3 {
		return ""
	}
	// Deterministic key order so nested/multiple candidates resolve stably.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "name") {
			if s := strings.TrimSpace(anyToString(m[k])); s != "" {
				return s
			}
		}
	}
	for _, k := range keys {
		if child, ok := m[k].(map[string]any); ok {
			if s := findNameKey(child, depth+1); s != "" {
				return s
			}
		}
	}
	return ""
}

// ─── catalogs (public, cached, curated) ──────────────────────────────────────

// BillProvider is one vendable operator within a category, with the amount
// bounds ClubKonnect enforces (0 = unbounded, user-priced categories only).
type BillProvider struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	MinAmount int64  `json:"minAmount,omitempty"`
	MaxAmount int64  `json:"maxAmount,omitempty"`
}

// BillPlan is a fixed-price product: a data bundle, a cable package, an exam
// PIN. CostNGN is what ClubKonnect charges US; AmountNGN is what the USER pays
// (face value; for data, cost+markup — priced by BillService, not here).
type BillPlan struct {
	Code      string `json:"code"`
	ProductID string `json:"-"` // data only: PRODUCT_ID, the alternate vend key
	Name      string `json:"name"`
	AmountNGN int64  `json:"amount"`
	CostNGN   int64  `json:"-"`
}

// cachedCatalog fetches a public catalog with a 1h in-memory cache. A stale
// entry is served if the refresh fails — bills should not flap on a slow
// catalog endpoint.
func (c *ClubkonnectClient) cachedCatalog(ctx context.Context, path string) ([]byte, error) {
	c.mu.Lock()
	entry, ok := c.cache[path]
	c.mu.Unlock()
	if ok && time.Since(entry.fetched) < ckCatalogCacheTTL {
		return entry.body, nil
	}
	ctx, cancel := context.WithTimeout(ctx, ckReadTimeout)
	defer cancel()
	body, err := c.get(ctx, path, nil, false)
	if err != nil {
		if ok {
			return entry.body, nil // stale beats broken
		}
		return nil, err
	}
	c.mu.Lock()
	c.cache[path] = ckCacheEntry{body: body, fetched: time.Now()}
	c.mu.Unlock()
	return body, nil
}

// ckNetworkNames normalizes ClubKonnect's MOBILE_NETWORK keys ("m_9mobile") to
// display names keyed by catalog ID.
var ckNetworkNames = map[string]string{
	"mtn": "MTN", "glo": "Glo", "m_9mobile": "9mobile", "airtel": "Airtel",
}

// AirtimeNetworks lists telcos with their airtime bounds.
func (c *ClubkonnectClient) AirtimeNetworks(ctx context.Context) ([]BillProvider, error) {
	body, err := c.cachedCatalog(ctx, "APIAirtimeDiscountV1.asp")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		MobileNetwork map[string][]struct {
			ID        string `json:"ID"`
			MinAmount int64  `json:"MINAMOUNT"`
			MaxAmount int64  `json:"MAXAMOUNT"`
		} `json:"MOBILE_NETWORK"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, c.errf("airtime catalog decode: %v", err)
	}
	var out []BillProvider
	for key, entries := range parsed.MobileNetwork {
		name := ckNetworkNames[strings.ToLower(key)]
		if name == "" {
			name = key
		}
		for _, e := range entries {
			out = append(out, BillProvider{Code: e.ID, Name: name, MinAmount: e.MinAmount, MaxAmount: e.MaxAmount})
		}
	}
	sortProviders(out)
	return out, nil
}

// DataNetworks lists telcos that have data plans (same codes as airtime).
func (c *ClubkonnectClient) DataNetworks(ctx context.Context) ([]BillProvider, error) {
	byNetwork, err := c.dataPlansByNetwork(ctx)
	if err != nil {
		return nil, err
	}
	var out []BillProvider
	for code, np := range byNetwork {
		out = append(out, BillProvider{Code: code, Name: np.name})
	}
	sortProviders(out)
	return out, nil
}

// DataPlans lists bundles for one telco (by catalog ID code). AmountNGN here is
// ClubKonnect's COST; retail pricing is BillService's job.
func (c *ClubkonnectClient) DataPlans(ctx context.Context, network string) ([]BillPlan, error) {
	byNetwork, err := c.dataPlansByNetwork(ctx)
	if err != nil {
		return nil, err
	}
	np, ok := byNetwork[network]
	if !ok {
		return nil, fmt.Errorf("unknown network %q", network)
	}
	return np.plans, nil
}

type ckNetworkPlans struct {
	name  string
	plans []BillPlan
}

func (c *ClubkonnectClient) dataPlansByNetwork(ctx context.Context) (map[string]ckNetworkPlans, error) {
	body, err := c.cachedCatalog(ctx, "APIDatabundlePlansV1.asp")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		MobileNetwork map[string][]struct {
			ID      string `json:"ID"`
			Product []struct {
				Code   string `json:"PRODUCT_CODE"`
				ID     string `json:"PRODUCT_ID"`
				Name   string `json:"PRODUCT_NAME"`
				Amount string `json:"PRODUCT_AMOUNT"`
			} `json:"PRODUCT"`
		} `json:"MOBILE_NETWORK"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, c.errf("data catalog decode: %v", err)
	}
	out := map[string]ckNetworkPlans{}
	for key, entries := range parsed.MobileNetwork {
		name := ckNetworkNames[strings.ToLower(key)]
		if name == "" {
			name = key
		}
		for _, e := range entries {
			np := ckNetworkPlans{name: name}
			for _, p := range e.Product {
				cost := parseNGN(p.Amount)
				if cost <= 0 {
					continue
				}
				np.plans = append(np.plans, BillPlan{
					Code: p.Code, ProductID: p.ID, Name: p.Name, CostNGN: cost,
				})
			}
			out[e.ID] = np
		}
	}
	return out, nil
}

// Discos lists electricity distributors. Meter type is chosen separately
// (prepaid "01" / postpaid "02" — uniform across discos in the catalog).
func (c *ClubkonnectClient) Discos(ctx context.Context) ([]BillProvider, error) {
	body, err := c.cachedCatalog(ctx, "APIElectricityDiscosV1.asp")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		ElectricCompany map[string][]struct {
			ID      string `json:"ID"`
			Name    string `json:"NAME"`
			Product []struct {
				MinAmount int64 `json:"MINAMOUNT"`
				MaxAmount int64 `json:"MAXAMOUNT"`
			} `json:"PRODUCT"`
		} `json:"ELECTRIC_COMPANY"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, c.errf("disco catalog decode: %v", err)
	}
	var out []BillProvider
	for _, entries := range parsed.ElectricCompany {
		for _, e := range entries {
			p := BillProvider{Code: e.ID, Name: e.Name}
			if len(e.Product) > 0 {
				p.MinAmount = e.Product[0].MinAmount
				p.MaxAmount = e.Product[0].MaxAmount
			}
			out = append(out, p)
		}
	}
	sortProviders(out)
	return out, nil
}

// CableProviders lists cable operators (dstv, gotv, startimes). Showmax is
// dropped: its ClubKonnect catalog is a defective copy of Startimes'.
func (c *ClubkonnectClient) CableProviders(ctx context.Context) ([]BillProvider, error) {
	byProvider, err := c.cablePackagesByProvider(ctx)
	if err != nil {
		return nil, err
	}
	var out []BillProvider
	for code, name := range byProvider.names {
		out = append(out, BillProvider{Code: code, Name: name})
	}
	sortProviders(out)
	return out, nil
}

// CablePackages lists packages for one operator; AmountNGN is the face price
// the user pays.
func (c *ClubkonnectClient) CablePackages(ctx context.Context, provider string) ([]BillPlan, error) {
	byProvider, err := c.cablePackagesByProvider(ctx)
	if err != nil {
		return nil, err
	}
	plans, ok := byProvider.plans[provider]
	if !ok {
		return nil, fmt.Errorf("unknown cable provider %q", provider)
	}
	return plans, nil
}

type ckCableCatalog struct {
	names map[string]string
	plans map[string][]BillPlan
}

func (c *ClubkonnectClient) cablePackagesByProvider(ctx context.Context) (ckCableCatalog, error) {
	out := ckCableCatalog{names: map[string]string{}, plans: map[string][]BillPlan{}}
	body, err := c.cachedCatalog(ctx, "APICableTVPackagesV2.asp")
	if err != nil {
		return out, err
	}
	var parsed struct {
		TVID map[string][]struct {
			ID      string `json:"ID"`
			Product []struct {
				PackageID   string `json:"PACKAGE_ID"`
				PackageName string `json:"PACKAGE_NAME"`
				Amount      string `json:"PACKAGE_AMOUNT"`
				Cost        string `json:"PRODUCT_DISCOUNT_AMOUNT"` // absolute cost for cable
			} `json:"PRODUCT"`
		} `json:"TV_ID"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return out, c.errf("cable catalog decode: %v", err)
	}
	for displayName, entries := range parsed.TVID {
		if strings.EqualFold(displayName, "showmax") {
			continue
		}
		for _, e := range entries {
			out.names[e.ID] = displayName
			for _, p := range e.Product {
				face := parseNGN(p.Amount)
				if face <= 0 {
					continue
				}
				out.plans[e.ID] = append(out.plans[e.ID], BillPlan{
					Code: p.PackageID, Name: p.PackageName, AmountNGN: face, CostNGN: parseNGN(p.Cost),
				})
			}
		}
	}
	return out, nil
}

// BettingCompanies lists fundable betting operators.
func (c *ClubkonnectClient) BettingCompanies(ctx context.Context) ([]BillProvider, error) {
	body, err := c.cachedCatalog(ctx, "APIBettingCompaniesV2.asp")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		BettingCompany []struct {
			Code      string `json:"PRODUCT_CODE"`
			Name      string `json:"PRODUCT_NAME"`
			MinAmount int64  `json:"MINAMOUNT"`
			MaxAmount int64  `json:"MAXAMOUNT"`
		} `json:"BETTING_COMPANY"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, c.errf("betting catalog decode: %v", err)
	}
	var out []BillProvider
	for _, e := range parsed.BettingCompany {
		out = append(out, BillProvider{Code: e.Code, Name: e.Name, MinAmount: e.MinAmount, MaxAmount: e.MaxAmount})
	}
	sortProviders(out)
	return out, nil
}

// EpinPlans lists exam-PIN products for "waec" or "jamb". Face-priced; JAMB's
// catalog is empty at the time of writing, which simply yields no products.
func (c *ClubkonnectClient) EpinPlans(ctx context.Context, provider string) ([]BillPlan, error) {
	path := "APIWAECPackagesV2.asp"
	if strings.EqualFold(provider, "jamb") {
		path = "APIJAMBPackagesV2.asp"
	}
	body, err := c.cachedCatalog(ctx, path)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		ExamType []struct {
			Code   string `json:"PRODUCT_CODE"`
			Desc   string `json:"PRODUCT_DESCRIPTION"`
			Amount string `json:"PRODUCT_AMOUNT"`
		} `json:"EXAM_TYPE"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, c.errf("epin catalog decode: %v", err)
	}
	var out []BillPlan
	for _, e := range parsed.ExamType {
		face := parseNGN(e.Amount)
		if face <= 0 {
			continue
		}
		out = append(out, BillPlan{Code: e.Code, Name: e.Desc, AmountNGN: face})
	}
	return out, nil
}

// parseNGN reads ClubKonnect's decimal-string Naira amounts ("4400.00",
// "307") as whole ₦, truncating kobo fractions.
func parseNGN(s string) int64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

func sortProviders(list []BillProvider) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].Code < list[j].Code
	})
}
