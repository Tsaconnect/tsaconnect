package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PaycrestClient wraps the Paycrest v2 Sender API — a decentralised routing
// protocol for stablecoin⇄fiat in emerging markets. It is the candidate NGN
// rail alongside Flutterwave: an order is matched to a liquidity PROVIDER who
// supplies the other side, so Paycrest itself never custodies funds.
//
// Two directions, one endpoint (POST /sender/orders); source.type and
// destination.type pick the direction:
//
//	onramp  — user pays NGN to the provider's bank account, provider delivers
//	          stablecoin to the recipient wallet
//	offramp — we send stablecoin to the provider's receive address, provider
//	          pays NGN to the recipient bank account
//
// Auth is a static API key in the `API-Key` header (NOT Bearer, despite the
// "Missing Authorization header" text on a 401 — that message is misleading;
// API-Key is what the API actually accepts).
//
// LIVE-MEASURED CONSTRAINTS (2026-09-03/04, see docs/plans runbook). These are
// NOT in Paycrest's documentation and each one has bitten us:
//
//   - Rates are PER-PROVIDER and move daily. The same pair quoted +0.24% one
//     day and +0.92% the next when the cheaper provider left the book. Never
//     cache a rate across orders; quote per order.
//   - Capacity is per (token, network) and providers drop out without notice.
//     USDT-on-Base had a $2,000 ceiling one day and NO provider the next.
//     Always pre-flight with Rates() for the ACTUAL amount before offering a
//     venue to a user.
//   - Small orders price worse: providers tier their books, so a $1 order can
//     pay 1.1% where a $360 order pays 0.25%.
//   - validUntil has been a consistent 30 minutes, not the 5 the docs imply.
//   - A deposit that reaches the provider's bank but is not matched leaves the
//     order at amountPaid=0 with NO refund path: there is no refund/cancel/
//     dispute endpoint (all 404), and the troubleshooting docs do not cover it.
//     Recovery is a manual email to support@paycrest.io. Any flow built on this
//     MUST have an operator path for stranded funds.
type PaycrestClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

const (
	paycrestDefaultBase = "https://api.paycrest.io"
	paycrestHTTPTimeout = 30 * time.Second

	// maxPaycrestResponseBytes caps any single response we will buffer.
	maxPaycrestResponseBytes = 8 << 20

	// PaycrestMinOrderUSD is the protocol floor. Below this no provider quotes.
	PaycrestMinOrderUSD = 0.5
)

// ErrPaycrestDisabled is returned when callers use a client whose API key was
// not configured at startup (dormant-by-default, mirroring FlutterwaveClient).
var ErrPaycrestDisabled = errors.New("paycrest not configured")

// ErrPaycrestNoProvider is Paycrest's authoritative "nobody will fill this" —
// returned as a 404 from the rate endpoint when no provider covers the
// requested (token, network, amount). It is a normal, expected condition, not
// a transport failure: the caller should offer a different venue or amount
// rather than retry. Crucially the API fails loudly here instead of quietly
// quoting a worse price.
var ErrPaycrestNoProvider = errors.New("paycrest: no provider available for this amount")

// ErrPaycrestOrderNotFound is a 404 on an order id.
var ErrPaycrestOrderNotFound = errors.New("paycrest: order not found")

// NewPaycrestClient returns nil when no API key is configured so dev and test
// environments keep booting; callers gate on IsEnabled().
func NewPaycrestClient(baseURL, apiKey string) *PaycrestClient {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = paycrestDefaultBase
	}
	return &PaycrestClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: &http.Client{Timeout: paycrestHTTPTimeout},
	}
}

// IsEnabled is safe to call on a nil receiver.
func (c *PaycrestClient) IsEnabled() bool { return c != nil }

// ─── Request plumbing ──────────────────────────────────────────────────────

// paycrestEnvelope is the uniform response wrapper: {status, message, data}.
// status is "success" or "error"; errors carry a nested {field, message}.
type paycrestEnvelope struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e paycrestEnvelope) failed() bool {
	return strings.EqualFold(strings.TrimSpace(e.Status), "error")
}

// validationDetail pulls the {field, message} that a 400 puts inside data, so
// "Failed to validate payload" becomes something an operator can act on.
func (e paycrestEnvelope) validationDetail() string {
	var v struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(e.Data, &v); err == nil && v.Message != "" {
		if v.Field != "" {
			return fmt.Sprintf("%s: %s", v.Field, v.Message)
		}
		return v.Message
	}
	return ""
}

// PaycrestRequestError is a request Paycrest answered with a non-2xx status.
// Its text is the one every caller has always shown.
type PaycrestRequestError struct {
	Op     string
	Status int
	Msg    string
}

func (e *PaycrestRequestError) Error() string {
	return fmt.Sprintf("paycrest %s: HTTP %d: %s", e.Op, e.Status, e.Msg)
}

// paycrestRejected reports a request Paycrest refused outright (4xx): it saw
// the request and created nothing, so sending a corrected one cannot duplicate
// an order. A 5xx or a lost answer is NOT this — an order may exist.
func paycrestRejected(err error) bool {
	var e *PaycrestRequestError
	return errors.As(err, &e) && e.Status >= 400 && e.Status < 500
}

func paycrestError(op string, status int, body []byte) error {
	var env paycrestEnvelope
	_ = json.Unmarshal(body, &env)
	msg := strings.TrimSpace(env.Message)
	if d := env.validationDetail(); d != "" {
		msg = msg + " (" + d + ")"
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
	}
	return &PaycrestRequestError{Op: op, Status: status, Msg: msg}
}

// do issues a request. authed=false is used for the public rate, markets and
// institution endpoints, which are readable without a key — useful because it
// lets quoting work even in an environment with no credentials.
func (c *PaycrestClient) do(ctx context.Context, method, path string, body []byte, authed bool) (*http.Response, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// X-Trace-Id mirrors the Flutterwave client: one id per call so a support
	// ticket can name the exact request.
	req.Header.Set("X-Trace-Id", uuid.NewString())
	if authed {
		if !c.IsEnabled() {
			return nil, nil, ErrPaycrestDisabled
		}
		req.Header.Set("API-Key", c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	// Bounded: /v2/markets returns the protocol-wide book and is now fetched on
	// a user-facing path, so a misbehaving or hostile upstream must not be able
	// to stream us out of memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPaycrestResponseBytes))
	if err != nil {
		return nil, nil, err
	}
	return resp, raw, nil
}

// ─── Rates ─────────────────────────────────────────────────────────────────

// PaycrestQuote is one side of the book. Rate is fiat-per-token as a decimal
// string — kept as a string deliberately so no float rounding enters a money
// path before the caller decides its own precision.
type PaycrestQuote struct {
	Rate                 string   `json:"rate"`
	ProviderIDs          []string `json:"providerIds"`
	OrderType            string   `json:"orderType"`
	RefundTimeoutMinutes int      `json:"refundTimeoutMinutes"`
}

// PaycrestRates carries both directions. Either side may be absent when no
// provider covers that direction at the requested size — an empty Buy is
// exactly how "onramp unavailable right now" presents.
type PaycrestRates struct {
	Buy  *PaycrestQuote `json:"buy"`
	Sell *PaycrestQuote `json:"sell"`
}

// Rates quotes a (network, token, amount) pair. amount is in TOKEN units, not
// fiat — callers holding a naira figure must convert with a reference rate
// first (see MarketRate).
//
// A 404 means no provider will fill this size: returned as ErrPaycrestNoProvider
// so callers can distinguish "unavailable" from "request failed".
func (c *PaycrestClient) Rates(ctx context.Context, network, token string, amount float64, fiat string) (*PaycrestRates, error) {
	if network == "" || token == "" || fiat == "" {
		return nil, errors.New("paycrest rates: network, token and fiat are required")
	}
	if amount <= 0 {
		return nil, errors.New("paycrest rates: amount must be positive")
	}
	// Plain decimal, never scientific notation — %g would render a large
	// amount as "1e+06" and the path segment would not parse.
	amt := strconv.FormatFloat(amount, 'f', -1, 64)
	path := fmt.Sprintf("/v2/rates/%s/%s/%s/%s",
		url.PathEscape(network), url.PathEscape(strings.ToUpper(token)),
		url.PathEscape(amt), url.PathEscape(strings.ToUpper(fiat)))

	resp, raw, err := c.do(ctx, http.MethodGet, path, nil, false)
	if err != nil {
		return nil, fmt.Errorf("paycrest rates: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w (%s %s %s on %s)", ErrPaycrestNoProvider, amt, token, fiat, network)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, paycrestError("rates", resp.StatusCode, raw)
	}
	var env paycrestEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("paycrest rates decode: %w", err)
	}
	var out PaycrestRates
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return nil, fmt.Errorf("paycrest rates decode data: %w", err)
	}
	return &out, nil
}

// PaycrestCurrency is one supported fiat with the protocol's own market
// reference rate — the mid we compare provider quotes against to compute a
// spread, and what converts a naira amount into token units for Rates().
type PaycrestCurrency struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	Decimals       int    `json:"decimals"`
	Symbol         string `json:"symbol"`
	MarketBuyRate  string `json:"marketBuyRate"`
	MarketSellRate string `json:"marketSellRate"`
	ShortName      string `json:"shortName"`
}

// Currencies lists supported fiats and their market reference rates. Public.
func (c *PaycrestClient) Currencies(ctx context.Context) ([]PaycrestCurrency, error) {
	resp, raw, err := c.do(ctx, http.MethodGet, "/v1/currencies", nil, false)
	if err != nil {
		return nil, fmt.Errorf("paycrest currencies: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, paycrestError("currencies", resp.StatusCode, raw)
	}
	var env paycrestEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("paycrest currencies decode: %w", err)
	}
	var out []PaycrestCurrency
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return nil, fmt.Errorf("paycrest currencies decode data: %w", err)
	}
	return out, nil
}

// MarketRate returns the protocol's reference rate for one fiat. side is "buy"
// or "sell". Used to size a naira amount into token units before quoting, and
// to express a provider quote as a spread.
func (c *PaycrestClient) MarketRate(ctx context.Context, fiat, side string) (float64, error) {
	cur, err := c.Currencies(ctx)
	if err != nil {
		return 0, err
	}
	fiat = strings.ToUpper(strings.TrimSpace(fiat))
	for _, x := range cur {
		if !strings.EqualFold(x.Code, fiat) {
			continue
		}
		raw := x.MarketBuyRate
		if strings.EqualFold(side, "sell") {
			raw = x.MarketSellRate
		}
		v, ok := parsePositiveDecimal(raw)
		if !ok {
			return 0, fmt.Errorf("paycrest market rate: unusable %s rate %q", fiat, raw)
		}
		return v, nil
	}
	return 0, fmt.Errorf("paycrest market rate: %s not supported", fiat)
}

// ─── Institutions ──────────────────────────────────────────────────────────

// PaycrestInstitution is a payout destination. Code is Paycrest's own
// SWIFT-style identifier (GTBINGLA, OPAYNGPC) — NOT the NIBSS numeric code the
// app and BankDirectory use, so a translation layer is mandatory.
type PaycrestInstitution struct {
	Name string `json:"name"`
	Code string `json:"code"`
	Type string `json:"type"`
}

// Institutions lists payout destinations for a fiat currency. Public. NGN
// returned 171 entries when last measured.
func (c *PaycrestClient) Institutions(ctx context.Context, fiat string) ([]PaycrestInstitution, error) {
	fiat = strings.ToUpper(strings.TrimSpace(fiat))
	if fiat == "" {
		return nil, errors.New("paycrest institutions: currency required")
	}
	resp, raw, err := c.do(ctx, http.MethodGet, "/v1/institutions/"+url.PathEscape(fiat), nil, false)
	if err != nil {
		return nil, fmt.Errorf("paycrest institutions: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, paycrestError("institutions", resp.StatusCode, raw)
	}
	var env paycrestEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("paycrest institutions decode: %w", err)
	}
	var out []PaycrestInstitution
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return nil, fmt.Errorf("paycrest institutions decode data: %w", err)
	}
	return out, nil
}

// ─── Orders ────────────────────────────────────────────────────────────────

// PaycrestBankAccount identifies a fiat account on either leg: the onramp
// refund destination, or the offramp payout recipient.
type PaycrestBankAccount struct {
	Institution       string `json:"institution"`
	AccountIdentifier string `json:"accountIdentifier"`
	AccountName       string `json:"accountName"`
	Memo              string `json:"memo,omitempty"`
}

// PaycrestOnrampRequest buys stablecoin with fiat.
//
// RefundAccount is REQUIRED — omitting it fails validation with "Refund
// institution not supported". Paycrest cannot know who will pay at creation
// time, so this is whatever account WE nominate, which is the lever that lets
// failures route back to us rather than to the payer.
type PaycrestOnrampRequest struct {
	// AmountNGN is the fiat the user pays, before our sender fee.
	AmountNGN string
	FiatCode  string // "NGN"
	Token     string // "USDC"
	Network   string // Paycrest naming: "base", "bnb-smart-chain", "arbitrum-one"
	// RecipientAddress is the wallet the stablecoin is delivered to.
	RecipientAddress string
	RefundAccount    PaycrestBankAccount
	// Reference is OUR idempotency + attribution key, echoed on the order and
	// on every webhook.
	Reference string
	// SenderFeePercent is our margin, settled on-chain to SenderFeeAddress.
	// Empty falls back to the dashboard profile default.
	SenderFeePercent string
	SenderFeeAddress string
}

// PaycrestOfframpRequest sells stablecoin for fiat.
type PaycrestOfframpRequest struct {
	// AmountToken is the stablecoin amount being sold.
	AmountToken string
	Token       string
	Network     string
	FiatCode    string
	// Recipient is the bank account the provider pays out to.
	Recipient PaycrestBankAccount
	// RefundAddress receives the stablecoin back if the order fails.
	RefundAddress    string
	Reference        string
	SenderFeePercent string
	SenderFeeAddress string
}

// PaycrestProviderAccount is where the payer sends fiat on an onramp.
//
// AccountName is PROVIDER-DEPENDENT and cannot be relied on: observed both as
// "5ran6/EMMANUEL ADEMU" (carrying the payer's name) and "LuminetITSoluti
// Checkout" (a generic merchant name). Never promise the user a particular
// name — show them whatever comes back here.
type PaycrestProviderAccount struct {
	Institution       string `json:"institution"`
	AccountIdentifier string `json:"accountIdentifier"`
	AccountName       string `json:"accountName"`
	AmountToTransfer  string `json:"amountToTransfer"`
	Currency          string `json:"currency"`
	ValidUntil        string `json:"validUntil"`
	// Offramp orders return a crypto address instead of a bank account.
	ReceiveAddress string `json:"receiveAddress"`
	Network        string `json:"network"`
}

// PaycrestOrder is the create/fetch response.
//
// AmountPaid is the field that matters on a failure: a deposit the protocol
// never matched leaves this at "0", and there is no API to recover from that
// state. Treat AmountPaid=="0" on an expired order as an operator alarm, not
// a normal outcome.
type PaycrestOrder struct {
	ID               string                   `json:"id"`
	Status           string                   `json:"status"`
	Direction        string                   `json:"direction"`
	OrderType        string                   `json:"orderType"`
	Currency         string                   `json:"currency"`
	Amount           string                   `json:"amount"`
	AmountInUSD      string                   `json:"amountInUsd"`
	AmountPaid       string                   `json:"amountPaid"`
	AmountReturned   string                   `json:"amountReturned"`
	PercentSettled   string                   `json:"percentSettled"`
	Rate             string                   `json:"rate"`
	SenderFee        string                   `json:"senderFee"`
	SenderFeePercent string                   `json:"senderFeePercent"` // what Paycrest applied; the dashboard default when the request omits it
	TransactionFee   string                   `json:"transactionFee"`
	Reference        string                   `json:"reference"`
	CreatedAt        string                   `json:"createdAt"`
	UpdatedAt        string                   `json:"updatedAt"`
	ProviderAccount  *PaycrestProviderAccount `json:"providerAccount"`
	Source           json.RawMessage          `json:"source"`
	Destination      json.RawMessage          `json:"destination"`
	TransactionLogs  []PaycrestTxLog          `json:"transactionLogs"`
}

// PaycrestTxLog is one entry in the order's audit trail. An order carrying
// only "order_initiated" after expiry is the unmatched-deposit signature.
type PaycrestTxLog struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	TxHash    string `json:"tx_hash"`
	GatewayID string `json:"gateway_id"`
	CreatedAt string `json:"created_at"`
}

// Order status values observed live. Terminal states end polling.
const (
	PaycrestStatusPending   = "pending"
	PaycrestStatusSettling  = "settling"
	PaycrestStatusSettled   = "settled"
	PaycrestStatusExpired   = "expired"
	PaycrestStatusRefunded  = "refunded"
	PaycrestStatusCancelled = "cancelled"
)

// PaycrestTerminal reports whether a status can still change.
func PaycrestTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case PaycrestStatusSettled, PaycrestStatusExpired,
		PaycrestStatusRefunded, PaycrestStatusCancelled:
		return true
	}
	return false
}

// CreateOnrampOrder reserves a provider quote and returns the bank account the
// payer must transfer to. No money moves until that transfer happens, so this
// call is safe to make speculatively — but the quote holds only until
// ProviderAccount.ValidUntil (30 minutes when last measured).
func (c *PaycrestClient) CreateOnrampOrder(ctx context.Context, req PaycrestOnrampRequest) (*PaycrestOrder, error) {
	if !c.IsEnabled() {
		return nil, ErrPaycrestDisabled
	}
	switch {
	case strings.TrimSpace(req.AmountNGN) == "":
		return nil, errors.New("paycrest onramp: amount required")
	case strings.TrimSpace(req.RecipientAddress) == "":
		return nil, errors.New("paycrest onramp: recipient address required")
	case strings.TrimSpace(req.Reference) == "":
		return nil, errors.New("paycrest onramp: reference required")
	case req.RefundAccount.Institution == "" || req.RefundAccount.AccountIdentifier == "":
		// Enforced here rather than discovered as a 400: the API rejects a
		// missing refund account with the opaque "Refund institution not
		// supported".
		return nil, errors.New("paycrest onramp: refund account is required")
	}
	fiat := req.FiatCode
	if fiat == "" {
		fiat = "NGN"
	}
	payload := map[string]any{
		"amount":   req.AmountNGN,
		"amountIn": "fiat",
		"source": map[string]any{
			"type":          "fiat",
			"currency":      strings.ToUpper(fiat),
			"refundAccount": req.RefundAccount,
		},
		"destination": map[string]any{
			"type":     "crypto",
			"currency": strings.ToUpper(req.Token),
			"recipient": map[string]any{
				"address": req.RecipientAddress,
				"network": req.Network,
			},
		},
		"reference": req.Reference,
	}
	applyPaycrestSenderFee(payload, req.SenderFeePercent, req.SenderFeeAddress)
	return c.postOrder(ctx, "onramp", payload)
}

// CreateOfframpOrder returns the address to send stablecoin to. The provider
// pays the recipient bank account once the deposit confirms on-chain.
func (c *PaycrestClient) CreateOfframpOrder(ctx context.Context, req PaycrestOfframpRequest) (*PaycrestOrder, error) {
	if !c.IsEnabled() {
		return nil, ErrPaycrestDisabled
	}
	switch {
	case strings.TrimSpace(req.AmountToken) == "":
		return nil, errors.New("paycrest offramp: amount required")
	case strings.TrimSpace(req.Reference) == "":
		return nil, errors.New("paycrest offramp: reference required")
	case req.Recipient.Institution == "" || req.Recipient.AccountIdentifier == "":
		return nil, errors.New("paycrest offramp: recipient bank account required")
	case strings.TrimSpace(req.RefundAddress) == "":
		// Without this a failed order has nowhere to return the stablecoin.
		return nil, errors.New("paycrest offramp: refund address required")
	}
	fiat := req.FiatCode
	if fiat == "" {
		fiat = "NGN"
	}
	payload := map[string]any{
		"amount": req.AmountToken,
		"source": map[string]any{
			"type":          "crypto",
			"currency":      strings.ToUpper(req.Token),
			"network":       req.Network,
			"refundAddress": req.RefundAddress,
		},
		"destination": map[string]any{
			"type":      "fiat",
			"currency":  strings.ToUpper(fiat),
			"recipient": req.Recipient,
		},
		"reference": req.Reference,
	}
	applyPaycrestSenderFee(payload, req.SenderFeePercent, req.SenderFeeAddress)
	return c.postOrder(ctx, "offramp", payload)
}

// applyPaycrestSenderFee attaches our margin. Both fields are omitted when the
// percent is empty so the dashboard profile default applies — that default is
// what produced the 0.5% observed on live orders.
func applyPaycrestSenderFee(payload map[string]any, percent, address string) {
	percent = strings.TrimSpace(percent)
	if percent == "" {
		return
	}
	payload["senderFeePercent"] = percent
	if a := strings.TrimSpace(address); a != "" {
		payload["senderFeeAddress"] = a
	}
}

func (c *PaycrestClient) postOrder(ctx context.Context, op string, payload map[string]any) (*PaycrestOrder, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("paycrest %s marshal: %w", op, err)
	}
	resp, body, err := c.do(ctx, http.MethodPost, "/v2/sender/orders", raw, true)
	if err != nil {
		return nil, fmt.Errorf("paycrest %s: %w", op, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, paycrestError(op, resp.StatusCode, body)
	}
	var env paycrestEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("paycrest %s decode: %w", op, err)
	}
	if env.failed() {
		return nil, fmt.Errorf("paycrest %s failed: %s", op, env.Message)
	}
	var out PaycrestOrder
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return nil, fmt.Errorf("paycrest %s decode data: %w", op, err)
	}
	if out.ID == "" {
		return nil, fmt.Errorf("paycrest %s: response carried no order id", op)
	}
	return &out, nil
}

// GetOrder fetches one order. Used both to poll (there is no other way to
// observe progress without a public webhook endpoint) and to independently
// verify a webhook before acting on it.
func (c *PaycrestClient) GetOrder(ctx context.Context, id string) (*PaycrestOrder, error) {
	if !c.IsEnabled() {
		return nil, ErrPaycrestDisabled
	}
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("paycrest get-order: id required")
	}
	resp, raw, err := c.do(ctx, http.MethodGet, "/v2/sender/orders/"+url.PathEscape(id), nil, true)
	if err != nil {
		return nil, fmt.Errorf("paycrest get-order: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrPaycrestOrderNotFound, id)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, paycrestError("get-order", resp.StatusCode, raw)
	}
	var env paycrestEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("paycrest get-order decode: %w", err)
	}
	var out PaycrestOrder
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return nil, fmt.Errorf("paycrest get-order decode data: %w", err)
	}
	return &out, nil
}

// UnmatchedDeposit reports the failure signature we hit live: an order that
// expired having never registered the money that reached the provider's bank.
// There is no API-level recovery — callers should alarm and open a manual
// recovery task rather than retry.
func (o *PaycrestOrder) UnmatchedDeposit() bool {
	if o == nil {
		return false
	}
	if !strings.EqualFold(o.Status, PaycrestStatusExpired) {
		return false
	}
	paid := strings.TrimSpace(o.AmountPaid)
	return paid == "" || paid == "0"
}

// ─── Markets ───────────────────────────────────────────────────────────────

// PaycrestBookEntry is one provider's standing offer for a (side, token,
// network, fiat), with the order-size band it will fill.
type PaycrestBookEntry struct {
	ProviderID     string `json:"providerId"`
	Side           string `json:"side"` // "buy" (onramp) | "sell" (offramp)
	Token          string `json:"token"`
	Fiat           string `json:"fiat"`
	Network        string `json:"network"`
	Rate           string `json:"rate"`
	RateType       string `json:"rateType"`
	Min            string `json:"min"`
	Max            string `json:"max"`
	BalanceUsd     string `json:"balanceUsd"`
	SuccessPercent string `json:"successPercent"`
}

// PaycrestMarkets is the protocol-wide order book plus headline aggregates.
type PaycrestMarkets struct {
	AsOf string              `json:"asOf"`
	Book []PaycrestBookEntry `json:"book"`
}

// Markets returns the whole order book in ONE call.
//
// This exists because /v2/rates is both slow and concurrency-throttled:
// measured from production, a single rates call takes 1.8-5.4s, and ten fired
// concurrently take 28-35s EACH. /v2/markets returns the entire book — every
// provider, token, network and order-size band — in about 0.2s.
//
// So the venue list is derived from here, and /v2/rates is spent only on the
// one venue the user actually picked, at order time, where an authoritative
// price matters. Public: no key required.
func (c *PaycrestClient) Markets(ctx context.Context) (*PaycrestMarkets, error) {
	resp, raw, err := c.do(ctx, http.MethodGet, "/v2/markets", nil, false)
	if err != nil {
		return nil, fmt.Errorf("paycrest markets: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, paycrestError("markets", resp.StatusCode, raw)
	}
	var env paycrestEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("paycrest markets decode: %w", err)
	}
	var out PaycrestMarkets
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return nil, fmt.Errorf("paycrest markets decode data: %w", err)
	}
	return &out, nil
}
