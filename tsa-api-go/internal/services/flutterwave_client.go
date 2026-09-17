package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// FlutterwaveClient wraps the Flutterwave v4 ("F4B") REST API. It replaces
// Nomba as the fiat gateway (Nomba declined production API access, 2026-07):
// bank directory, account name enquiry, virtual-account collections, payouts
// via the direct-transfer orchestrator, own-balance currency conversion
// (fiat ⇄ stablecoin), fee/rate quotes, and balance reads.
//
// v4 auth is OAuth2 client_credentials against Flutterwave's Keycloak IdP;
// tokens live ~600s and are cached until 60s before expiry behind a
// single-flight mutex. Every API call carries X-Trace-Id; mutating calls
// carry X-Idempotency-Key derived from the caller's reference so provider-side
// retries are safe.
//
// The production base URL is mid-migration on Flutterwave's side — it MUST
// come from config at go-live, never be hardcoded here.
type FlutterwaveClient struct {
	baseURL      string
	idpURL       string
	clientID     string
	clientSecret string
	httpClient   *http.Client

	tokenMu    sync.Mutex
	token      string
	tokenExpAt time.Time

	banksMu sync.Mutex
	banks   map[string]fwBanksEntry // key: ISO-2 country

	lookupMu sync.Mutex
	lookups  map[string]fwLookupEntry // key: currency:code:number
}

type fwBanksEntry struct {
	at    time.Time
	banks []FWBank
}

type fwLookupEntry struct {
	at   time.Time
	name string
}

// FWBank mirrors the app-facing bank shape ({code,name}); ID is Flutterwave's
// internal numeric id, kept for future branch lookups.
type FWBank struct {
	ID   json.Number `json:"id"`
	Code string      `json:"code"`
	Name string      `json:"name"`
}

const (
	fwBanksTTL    = 24 * time.Hour
	fwLookupTTL   = 5 * time.Minute
	fwHTTPTimeout = 20 * time.Second
	// fwTokenSkew refreshes the OAuth token this long before its declared
	// expiry (tokens last ~600s; the docs advise refreshing ~1min early).
	fwTokenSkew = 60 * time.Second

	fwDefaultSandboxBase = "https://developersandbox-api.flutterwave.com"
	fwDefaultIdpURL      = "https://idp.flutterwave.com"
)

// ErrFlutterwaveDisabled is returned when callers use a client whose
// credentials weren't configured at startup (dormant-by-default pattern).
var ErrFlutterwaveDisabled = errors.New("flutterwave not configured")

// ErrFWAccountNotFound is returned by ResolveAccount when Flutterwave reports
// the account number does not resolve at the supplied bank.
var ErrFWAccountNotFound = errors.New("account not found")

// ErrFWChargeNotFound is returned by GetCharge on a 404 — Flutterwave stating
// the charge does not exist on this tenant. Distinct from a transport error:
// this is the provider's authoritative answer, not a failure to reach it.
// Callers use it to stop retrying a lookup that can never succeed.
var ErrFWChargeNotFound = errors.New("charge not found")

// NewFlutterwaveClient returns nil if any required credential is missing —
// callers must check IsEnabled() so dev environments keep booting without
// Flutterwave creds.
func NewFlutterwaveClient(baseURL, idpURL, clientID, clientSecret string) *FlutterwaveClient {
	if clientID == "" || clientSecret == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = fwDefaultSandboxBase
	}
	if idpURL == "" {
		idpURL = fwDefaultIdpURL
	}
	return &FlutterwaveClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		idpURL:       strings.TrimRight(idpURL, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: fwHTTPTimeout},
		banks:        make(map[string]fwBanksEntry),
		lookups:      make(map[string]fwLookupEntry),
	}
}

// IsEnabled is safe to call on a nil receiver.
func (c *FlutterwaveClient) IsEnabled() bool { return c != nil }

// ─── Token management (OAuth2 client_credentials via Keycloak) ─────────────

type fwTokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func (c *FlutterwaveClient) ensureToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExpAt.Add(-fwTokenSkew)) {
		return c.token, nil
	}
	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"grant_type":    {"client_credentials"},
	}
	endpoint := c.idpURL + "/realms/flutterwave/protocol/openid-connect/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("flutterwave token request: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("flutterwave token: HTTP %d: %s", resp.StatusCode, string(data))
	}
	var parsed fwTokenResp
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("flutterwave token decode: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", errors.New("flutterwave token: empty access_token")
	}
	c.token = parsed.AccessToken
	exp := parsed.ExpiresIn
	if exp <= 0 {
		exp = 600
	}
	c.tokenExpAt = time.Now().Add(time.Duration(exp) * time.Second)
	return c.token, nil
}

// ─── Request plumbing ──────────────────────────────────────────────────────

// fwEnvelope is the tolerant v4 response envelope. v4 payloads carry a status
// string plus data; unknown extra fields are ignored. Success is keyed on the
// HTTP status code first and the status field second — v4 (unlike Nomba) uses
// real HTTP codes, but the belt-and-braces check catches "200 with failed
// body" drift while the API is young.
type fwEnvelope struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e fwEnvelope) failed() bool {
	s := strings.ToLower(e.Status)
	return s == "failed" || s == "error"
}

// fwError extracts the most useful message from an error body.
func fwError(op string, status int, body []byte) error {
	var env struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Error   *struct {
			Type             string `json:"type"`
			Code             string `json:"code"`
			Message          string `json:"message"`
			ValidationErrors []struct {
				Field   string `json:"field"`
				Message string `json:"message"`
			} `json:"validation_errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	msg := env.Message
	if env.Error != nil {
		if env.Error.Message != "" {
			msg = env.Error.Message
		}
		for _, ve := range env.Error.ValidationErrors {
			if ve.Message == "" {
				continue
			}
			if ve.Field != "" {
				msg += "; " + ve.Field + ": " + ve.Message
			} else {
				msg += "; " + ve.Message
			}
		}
		if env.Error.Type != "" && msg != "" {
			msg = env.Error.Type + ": " + msg
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	return fmt.Errorf("flutterwave %s: HTTP %d: %s", op, status, msg)
}

// authedRequest performs a v4 API call with bearer auth, X-Trace-Id, and an
// optional idempotency key; it retries once on 401 with a forced token
// refresh (mirrors the Nomba client's behavior).
func (c *FlutterwaveClient) authedRequest(ctx context.Context, method, path string, body []byte, idempotencyKey string) (*http.Response, []byte, error) {
	do := func() (*http.Response, []byte, error) {
		token, err := c.ensureToken(ctx)
		if err != nil {
			return nil, nil, err
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Trace-Id", uuid.NewString())
		if idempotencyKey != "" {
			req.Header.Set("X-Idempotency-Key", fwIdempotencyKey(idempotencyKey))
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, nil, err
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, raw, nil
	}

	resp, raw, err := do()
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.tokenMu.Lock()
		c.token = ""
		c.tokenMu.Unlock()
		resp, raw, err = do()
		if err != nil {
			return nil, nil, err
		}
	}
	return resp, raw, nil
}

// fwIdempotencyKey pads/truncates a caller reference into Flutterwave's
// required 12–255 char window without losing uniqueness for typical refs.
func fwIdempotencyKey(ref string) string {
	if len(ref) >= 12 {
		if len(ref) > 255 {
			return ref[:255]
		}
		return ref
	}
	return (ref + "-tsaconnect-pad")[:12]
}

// ─── Banks + name enquiry ──────────────────────────────────────────────────

// ListBanks returns Flutterwave's supported banks for an ISO-2 country
// (default NG), cached 24h per country. Callers get a copy.
func (c *FlutterwaveClient) ListBanks(ctx context.Context, country string) ([]FWBank, error) {
	if !c.IsEnabled() {
		return nil, ErrFlutterwaveDisabled
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		country = "NG"
	}
	c.banksMu.Lock()
	if entry, ok := c.banks[country]; ok && time.Since(entry.at) < fwBanksTTL {
		out := make([]FWBank, len(entry.banks))
		copy(out, entry.banks)
		c.banksMu.Unlock()
		return out, nil
	}
	c.banksMu.Unlock()

	resp, raw, err := c.authedRequest(ctx, http.MethodGet, "/banks?country="+url.QueryEscape(country), nil, "")
	if err != nil {
		return nil, fmt.Errorf("flutterwave banks: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fwError("banks", resp.StatusCode, raw)
	}
	var env fwEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("flutterwave banks decode: %w", err)
	}
	var banks []FWBank
	if err := json.Unmarshal(env.Data, &banks); err != nil {
		return nil, fmt.Errorf("flutterwave banks decode data: %w", err)
	}
	filtered := banks[:0]
	for _, b := range banks {
		if b.Code == "" || b.Name == "" {
			continue
		}
		filtered = append(filtered, b)
	}
	c.banksMu.Lock()
	c.banks[country] = fwBanksEntry{at: time.Now(), banks: filtered}
	c.banksMu.Unlock()

	out := make([]FWBank, len(filtered))
	copy(out, filtered)
	return out, nil
}

type fwResolveResp struct {
	AccountName   string `json:"account_name"`
	AccountNumber string `json:"account_number"`
}

// ResolveAccount performs name enquiry via POST /banks/account-resolve.
// Flutterwave supports NGN, GBP, and USD (NG operations) only. Results are
// cached 5 minutes by (currency, bankCode, accountNumber).
func (c *FlutterwaveClient) ResolveAccount(ctx context.Context, currency, bankCode, accountNumber string) (string, error) {
	if !c.IsEnabled() {
		return "", ErrFlutterwaveDisabled
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "NGN"
	}
	bankCode = strings.TrimSpace(bankCode)
	accountNumber = strings.TrimSpace(accountNumber)
	if bankCode == "" || accountNumber == "" {
		return "", fmt.Errorf("invalid bank code or account number")
	}
	key := currency + ":" + bankCode + ":" + accountNumber
	c.lookupMu.Lock()
	if entry, ok := c.lookups[key]; ok && time.Since(entry.at) < fwLookupTTL {
		c.lookupMu.Unlock()
		return entry.name, nil
	}
	c.lookupMu.Unlock()

	body, _ := json.Marshal(map[string]any{
		"currency": currency,
		"account": map[string]string{
			"code":   bankCode,
			"number": accountNumber,
		},
	})
	resp, raw, err := c.authedRequest(ctx, http.MethodPost, "/banks/account-resolve", body, "")
	if err != nil {
		return "", fmt.Errorf("flutterwave resolve: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrFWAccountNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fwError("resolve", resp.StatusCode, raw)
	}
	var env fwEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("flutterwave resolve decode: %w", err)
	}
	var parsed fwResolveResp
	if err := json.Unmarshal(env.Data, &parsed); err != nil {
		return "", fmt.Errorf("flutterwave resolve decode data: %w", err)
	}
	if env.failed() || parsed.AccountName == "" {
		return "", ErrFWAccountNotFound
	}
	now := time.Now()
	c.lookupMu.Lock()
	// Sweep expired entries before inserting so the cache stays bounded to the
	// distinct accounts resolved within one TTL window — entries are otherwise
	// only TTL-ignored on read, never evicted, which leaks for the process life.
	for k, e := range c.lookups {
		if now.Sub(e.at) >= fwLookupTTL {
			delete(c.lookups, k)
		}
	}
	c.lookups[key] = fwLookupEntry{at: now, name: parsed.AccountName}
	c.lookupMu.Unlock()
	return parsed.AccountName, nil
}

// ─── Customers + virtual accounts (collections) ────────────────────────────

// FWCustomerRequest creates the customer a virtual account hangs off. Name is
// nested {first,last} (sandbox-verified 2026-07-19). Phone is nested
// {country_code,number} — a bare string is rejected as REQUEST_NOT_VALID.
// BVN/NIN are NOT customer fields; they go on CreateVirtualAccount (PM path).
type FWCustomerRequest struct {
	Email         string   `json:"email"`
	Name          *FWName  `json:"name,omitempty"`
	Phone         *FWPhone `json:"phone,omitempty"`
	MetaReference string   `json:"-"`
}

// FWName is the nested name object used by customers (and transfer
// recipients accept the same shape).
type FWName struct {
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
}

// FWPhone is the nested phone object Flutterwave v4 expects on /customers.
type FWPhone struct {
	CountryCode string `json:"country_code"`
	Number      string `json:"number"`
}

// ParseFWPhone normalizes a user-entered phone into the FW v4 shape. Returns
// nil when the input can't be parsed confidently — callers should omit phone
// rather than send a malformed value (FW returns empty validation_errors).
func ParseFWPhone(raw string) *FWPhone {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Keep digits only (+, spaces, dashes dropped).
	digits := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] >= '0' && raw[i] <= '9' {
			digits = append(digits, raw[i])
		}
	}
	s := string(digits)
	if s == "" {
		return nil
	}
	// Nigeria is the only corridor for Spec 1 / PM today.
	switch {
	case strings.HasPrefix(s, "234") && len(s) >= 13:
		return &FWPhone{CountryCode: "234", Number: strings.TrimPrefix(s, "234")}
	case strings.HasPrefix(s, "0") && len(s) == 11:
		return &FWPhone{CountryCode: "234", Number: s[1:]}
	case len(s) == 10 && (s[0] == '7' || s[0] == '8' || s[0] == '9'):
		return &FWPhone{CountryCode: "234", Number: s}
	default:
		return nil
	}
}

type fwCustomerResp struct {
	ID string `json:"id"`
}

// CreateCustomer provisions a v4 customer and returns its id.
func (c *FlutterwaveClient) CreateCustomer(ctx context.Context, req FWCustomerRequest) (string, error) {
	if !c.IsEnabled() {
		return "", ErrFlutterwaveDisabled
	}
	if req.Email == "" {
		return "", errors.New("flutterwave create-customer: email required")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("flutterwave create-customer marshal: %w", err)
	}
	idem := req.MetaReference
	if idem == "" {
		idem = "cust-" + req.Email
	}
	resp, body, err := c.authedRequest(ctx, http.MethodPost, "/customers", raw, idem)
	if err != nil {
		return "", fmt.Errorf("flutterwave create-customer: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fwError("create-customer", resp.StatusCode, body)
	}
	var env fwEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("flutterwave create-customer decode: %w", err)
	}
	var parsed fwCustomerResp
	if err := json.Unmarshal(env.Data, &parsed); err != nil {
		return "", fmt.Errorf("flutterwave create-customer decode data: %w", err)
	}
	if env.failed() || parsed.ID == "" {
		return "", fmt.Errorf("flutterwave create-customer failed: %s", env.Message)
	}
	return parsed.ID, nil
}

// FWVirtualAccountRequest provisions a collection virtual account.
// AccountType "static" (permanent, Amount 0) or "dynamic" (one-time,
// amount-locked, Expiry seconds). Currencies: NGN, GHS, EGP, KES, MAD, ZAR.
type FWVirtualAccountRequest struct {
	CustomerID  string  `json:"customer_id"`
	Reference   string  `json:"reference"`
	Currency    string  `json:"currency"`
	Amount      float64 `json:"amount"`
	AccountType string  `json:"account_type"`
	Expiry      int     `json:"expiry,omitempty"`
	Narration   string  `json:"narration,omitempty"`

	// BVN/NIN identify the account holder. Flutterwave live mode REQUIRES one or
	// the other for NGN static accounts (sandbox does not). These carry personal
	// identity data: they transit in-memory to Flutterwave only and are NEVER
	// persisted or logged — do not add this struct to any log line.
	BVN string `json:"bvn,omitempty"`
	NIN string `json:"nin,omitempty"`
}

// FWVirtualAccount is the provisioned pay-into account (field names
// sandbox-verified: the bank is account_bank_name, expiry is
// account_expiration_datetime).
type FWVirtualAccount struct {
	ID            string `json:"id"`
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
	BankName      string `json:"account_bank_name"`
	Reference     string `json:"reference"`
	Status        string `json:"status"`
	ExpiresAt     string `json:"account_expiration_datetime"`
}

// CreateVirtualAccount provisions a virtual account for fiat collections.
// The Reference is the attribution key the charge.completed webhook carries
// back — it must be unique and deterministic per (user, purpose).
func (c *FlutterwaveClient) CreateVirtualAccount(ctx context.Context, req FWVirtualAccountRequest) (FWVirtualAccount, error) {
	if !c.IsEnabled() {
		return FWVirtualAccount{}, ErrFlutterwaveDisabled
	}
	if req.CustomerID == "" || req.Reference == "" {
		return FWVirtualAccount{}, errors.New("flutterwave create-va: customer_id and reference required")
	}
	if req.Currency == "" {
		req.Currency = "NGN"
	}
	if req.AccountType == "" {
		req.AccountType = "static"
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return FWVirtualAccount{}, fmt.Errorf("flutterwave create-va marshal: %w", err)
	}
	resp, body, err := c.authedRequest(ctx, http.MethodPost, "/virtual-accounts", raw, req.Reference)
	if err != nil {
		return FWVirtualAccount{}, fmt.Errorf("flutterwave create-va: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return FWVirtualAccount{}, fwError("create-va", resp.StatusCode, body)
	}
	var env fwEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return FWVirtualAccount{}, fmt.Errorf("flutterwave create-va decode: %w", err)
	}
	var parsed FWVirtualAccount
	if err := json.Unmarshal(env.Data, &parsed); err != nil {
		return FWVirtualAccount{}, fmt.Errorf("flutterwave create-va decode data: %w", err)
	}
	if env.failed() || parsed.AccountNumber == "" {
		return FWVirtualAccount{}, fmt.Errorf("flutterwave create-va failed: %s", env.Message)
	}
	return parsed, nil
}

// ─── Direct transfers (payouts, conversions, crypto-out) ───────────────────

// FWTransferRecipient is the union recipient for POST /direct-transfers.
// Exactly one branch is set per transfer type:
//   - type "bank":         Bank{Code, AccountNumber}
//   - type "mobile_money":  MobileMoney{Network, Msisdn, Country}
//   - type "wallet":        Wallet{Provider:"flutterwave", Identifier} —
//     identifier = OUR OWN merchant id for balance-to-balance conversion
//   - type "crypto":        Crypto{Network, Address} (stablecoin payout)
type FWTransferRecipient struct {
	Bank        *FWRecipientBank        `json:"bank,omitempty"`
	MobileMoney *FWRecipientMobileMoney `json:"mobile_money,omitempty"`
	Wallet      *FWRecipientWallet      `json:"wallet,omitempty"`
	Crypto      *FWRecipientCrypto      `json:"crypto,omitempty"`
	Name        *FWRecipientName        `json:"name,omitempty"`
}

type FWRecipientBank struct {
	Code          string `json:"code"`
	AccountNumber string `json:"account_number"`
}

type FWRecipientMobileMoney struct {
	Network string `json:"network"`
	Msisdn  string `json:"msisdn"`
	Country string `json:"country,omitempty"`
}

type FWRecipientWallet struct {
	Provider   string `json:"provider"`
	Identifier string `json:"identifier"`
}

type FWRecipientCrypto struct {
	Network string `json:"network"`
	Address string `json:"address"`
}

type FWRecipientName struct {
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
	Full  string `json:"full,omitempty"`
}

// FWDirectTransferRequest is the one-shot transfer orchestrator body.
type FWDirectTransferRequest struct {
	Action             string               `json:"action"` // "instant"
	Type               string               `json:"type"`   // bank | mobile_money | wallet | crypto
	Reference          string               `json:"reference"`
	Narration          string               `json:"narration,omitempty"`
	CallbackURL        string               `json:"callback_url,omitempty"`
	PaymentInstruction FWPaymentInstruction `json:"payment_instruction"`
}

type FWPaymentInstruction struct {
	SourceCurrency      string              `json:"source_currency"`
	DestinationCurrency string              `json:"destination_currency"`
	Amount              FWAmount            `json:"amount"`
	Recipient           FWTransferRecipient `json:"recipient"`
}

type FWAmount struct {
	Value     float64 `json:"value"`
	AppliesTo string  `json:"applies_to"` // "source_currency" | "destination_currency"
}

// FWTransfer is the created/queried transfer.
type FWTransfer struct {
	ID        string          `json:"id"`
	Reference string          `json:"reference"`
	Status    string          `json:"status"` // NEW | PENDING | SUCCESSFUL | FAILED
	Fee       json.RawMessage `json:"fee,omitempty"`
	// CacheHit is set on a DirectTransfer result Flutterwave served from its
	// idempotency cache (X-Idempotency-Cache-Hit) — the answer to the FIRST
	// request that used this reference, not a new transfer.
	CacheHit bool `json:"-"`
}

// ProviderRejectedError means the provider answered a money-moving request
// and refused it, so that request moved no money. It is deliberately narrow:
// a timeout, a 5xx, a 409 or a 429 is NOT one, because none of those say
// whether a transfer exists.
//
// On its own it only describes THIS request. If an earlier request with the
// same reference may have reached the provider, a refusal of the retry can be
// "that reference is already taken" — i.e. the first attempt went through.
// Replayed is what closes that gap: it is set when the provider served the
// answer from its idempotency cache, which makes the refusal the outcome of the
// first request as well.
type ProviderRejectedError struct {
	Err      error
	Replayed bool
}

func (e *ProviderRejectedError) Error() string { return e.Err.Error() }
func (e *ProviderRejectedError) Unwrap() error { return e.Err }

// fwTransferRefusedStatus lists the HTTP statuses on which Flutterwave has
// refused to create a transfer: malformed, unauthenticated, forbidden, unknown
// route, unprocessable (the validation and insufficient-balance class).
func fwTransferRefusedStatus(code int) bool {
	switch code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired,
		http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// DirectTransfer initiates a transfer via the v4 orchestrator. The caller's
// Reference doubles as the idempotency key, so a network-level retry can
// never double-pay.
func (c *FlutterwaveClient) DirectTransfer(ctx context.Context, req FWDirectTransferRequest) (FWTransfer, error) {
	if !c.IsEnabled() {
		return FWTransfer{}, ErrFlutterwaveDisabled
	}
	if req.Reference == "" {
		return FWTransfer{}, errors.New("flutterwave direct-transfer: reference required")
	}
	if req.Action == "" {
		req.Action = "instant"
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return FWTransfer{}, fmt.Errorf("flutterwave direct-transfer marshal: %w", err)
	}
	resp, body, err := c.authedRequest(ctx, http.MethodPost, "/direct-transfers", raw, req.Reference)
	if err != nil {
		return FWTransfer{}, fmt.Errorf("flutterwave direct-transfer: %w", err)
	}
	cacheHit := strings.EqualFold(strings.TrimSpace(resp.Header.Get("X-Idempotency-Cache-Hit")), "true")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		herr := fwError("direct-transfer", resp.StatusCode, body)
		if fwTransferRefusedStatus(resp.StatusCode) {
			return FWTransfer{}, &ProviderRejectedError{Err: herr, Replayed: cacheHit}
		}
		return FWTransfer{}, herr
	}
	var env fwEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return FWTransfer{}, fmt.Errorf("flutterwave direct-transfer decode: %w", err)
	}
	var parsed FWTransfer
	if err := json.Unmarshal(env.Data, &parsed); err != nil {
		return FWTransfer{}, fmt.Errorf("flutterwave direct-transfer decode data: %w", err)
	}
	if env.failed() {
		return FWTransfer{}, &ProviderRejectedError{
			Err:      fmt.Errorf("flutterwave direct-transfer failed: %s", env.Message),
			Replayed: cacheHit,
		}
	}
	parsed.CacheHit = cacheHit
	if parsed.ID == "" {
		return FWTransfer{}, errors.New("flutterwave direct-transfer: empty transfer id in response")
	}
	return parsed, nil
}

// GetTransfer fetches a transfer's current state (fallback when the
// transfer.disburse webhook is delayed).
func (c *FlutterwaveClient) GetTransfer(ctx context.Context, id string) (FWTransfer, error) {
	if !c.IsEnabled() {
		return FWTransfer{}, ErrFlutterwaveDisabled
	}
	if id == "" {
		return FWTransfer{}, errors.New("flutterwave get-transfer: id required")
	}
	resp, body, err := c.authedRequest(ctx, http.MethodGet, "/transfers/"+url.PathEscape(id), nil, "")
	if err != nil {
		return FWTransfer{}, fmt.Errorf("flutterwave get-transfer: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return FWTransfer{}, fwError("get-transfer", resp.StatusCode, body)
	}
	var env fwEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return FWTransfer{}, fmt.Errorf("flutterwave get-transfer decode: %w", err)
	}
	var parsed FWTransfer
	if err := json.Unmarshal(env.Data, &parsed); err != nil {
		return FWTransfer{}, fmt.Errorf("flutterwave get-transfer decode data: %w", err)
	}
	return parsed, nil
}

// ErrFWTransferShape marks a /transfers response this code does not
// positively recognise. It is the signal that the live listing differs from
// the schema FindTransferByReference was built against — the thing
// IT_SELL_FW_ABSENCE_TRUSTED is waiting on — so callers alert on it by name.
var ErrFWTransferShape = errors.New("flutterwave list-transfers: unrecognised response shape")

// fwTransferListMaxPages bounds a reference scan: 20 pages of 50 is a thousand
// transfers, far more than two days of payouts. Hitting it is an error, not
// "not found".
const fwTransferListMaxPages = 20

// fwTransferSearchSpan is how far either side of the attempt time a reference
// is searched for.
const fwTransferSearchSpan = 24 * time.Hour

// FindTransferByReference looks for the transfer carrying reference among
// transfers created within a day either side of around.
//
// v4 has no reference filter, so GET /transfers is paged by cursor over a
// date-only window (the convention FW listing endpoints use; `to` is pushed a
// day out so the window's last day is included) and rows are filtered here by
// created_datetime.
//
// found=false with a nil error is a claim that the transfer does not exist, and
// callers act on it, so it is only returned after a complete scan of a response
// shape this code positively recognises. Everything else is an error: a missing
// cursor, has_more_items, transfer list, reference or created_datetime; a
// cursor that does not advance; the page cap; or more than one transfer on the
// reference (references are meant to be unique, so two means something is
// wrong and neither may be picked).
//
// The shape was built from Flutterwave's published v4 schema and has not been
// confirmed against the live API; see IT_SELL_FW_ABSENCE_TRUSTED.
func (c *FlutterwaveClient) FindTransferByReference(ctx context.Context, reference string, around time.Time) (FWTransfer, bool, error) {
	if !c.IsEnabled() {
		return FWTransfer{}, false, ErrFlutterwaveDisabled
	}
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return FWTransfer{}, false, errors.New("flutterwave list-transfers: reference required")
	}
	lo, hi := around.Add(-fwTransferSearchSpan).UTC(), around.Add(fwTransferSearchSpan).UTC()
	var match *FWTransfer
	cursor := ""
	for page := 0; page < fwTransferListMaxPages; page++ {
		q := url.Values{}
		q.Set("from", lo.Format("2006-01-02"))
		if upper := fwUpperBoundDate(hi, time.Now()); upper != "" {
			q.Set("to", upper)
		}
		q.Set("size", "50")
		if cursor != "" {
			q.Set("next", cursor)
		}
		resp, body, err := c.authedRequest(ctx, http.MethodGet, "/transfers?"+q.Encode(), nil, "")
		if err != nil {
			return FWTransfer{}, false, fmt.Errorf("flutterwave list-transfers: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return FWTransfer{}, false, fwError("list-transfers", resp.StatusCode, body)
		}
		rows, hasMore, next, err := decodeFWTransferPage(body)
		if err != nil {
			return FWTransfer{}, false, err
		}
		for i := range rows {
			tr := rows[i]
			if tr.Reference != reference || tr.createdAt.Before(lo) || tr.createdAt.After(hi) {
				continue
			}
			if match != nil {
				return FWTransfer{}, false, fmt.Errorf("flutterwave list-transfers: reference %s carried by transfers %s and %s", reference, match.ID, tr.ID)
			}
			found := tr.FWTransfer
			match = &found
		}
		if !hasMore {
			if match == nil {
				return FWTransfer{}, false, nil
			}
			return *match, true, nil
		}
		if next == "" || next == cursor {
			return FWTransfer{}, false, fmt.Errorf("flutterwave list-transfers: cursor did not advance after page %d; scan incomplete", page+1)
		}
		cursor = next
	}
	return FWTransfer{}, false, fmt.Errorf("flutterwave list-transfers: more than %d pages in window; scan incomplete", fwTransferListMaxPages)
}

type fwListedTransfer struct {
	FWTransfer
	createdAt time.Time
}

// decodeFWTransferPage decodes one /transfers page, rejecting any shape that
// is not exactly the documented one.
func decodeFWTransferPage(body []byte) ([]fwListedTransfer, bool, string, error) {
	var env fwEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, false, "", fmt.Errorf("flutterwave list-transfers decode: %w", err)
	}
	var data struct {
		Cursor *struct {
			Next         string `json:"next"`
			HasMoreItems *bool  `json:"has_more_items"`
		} `json:"cursor"`
		Transfers *[]struct {
			ID              string  `json:"id"`
			Reference       *string `json:"reference"`
			Status          string  `json:"status"`
			CreatedDatetime *string `json:"created_datetime"`
		} `json:"transfers"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, false, "", fmt.Errorf("%w: data: %v", ErrFWTransferShape, err)
	}
	if data.Cursor == nil || data.Cursor.HasMoreItems == nil {
		return nil, false, "", fmt.Errorf("%w: no cursor.has_more_items; cannot tell whether the scan is complete", ErrFWTransferShape)
	}
	if data.Transfers == nil {
		return nil, false, "", fmt.Errorf("%w: no transfers list", ErrFWTransferShape)
	}
	out := make([]fwListedTransfer, 0, len(*data.Transfers))
	for _, t := range *data.Transfers {
		if t.Reference == nil || t.CreatedDatetime == nil {
			return nil, false, "", fmt.Errorf("%w: transfer %q lacks reference or created_datetime", ErrFWTransferShape, t.ID)
		}
		at, err := time.Parse(time.RFC3339, *t.CreatedDatetime)
		if err != nil {
			return nil, false, "", fmt.Errorf("%w: transfer %q created_datetime %q: %v", ErrFWTransferShape, t.ID, *t.CreatedDatetime, err)
		}
		out = append(out, fwListedTransfer{
			FWTransfer: FWTransfer{ID: t.ID, Reference: *t.Reference, Status: t.Status},
			createdAt:  at,
		})
	}
	return out, *data.Cursor.HasMoreItems, data.Cursor.Next, nil
}

// ─── Charges (collection verification) ─────────────────────────────────────

// FWCharge is the authoritative charge record fetched from GET /charges/{id}.
// It is the source of truth used to verify a charge.completed webhook before
// crediting money — the webhook body alone is not trusted (it can be stale,
// replayed, or wrong even when signature-valid).
type FWCharge struct {
	ID        string      `json:"id"`
	Reference string      `json:"reference"`
	Amount    json.Number `json:"amount"`
	Currency  string      `json:"currency"`
	Status    string      `json:"status"`
}

// GetCharge fetches a single charge by its Flutterwave id (the charge.completed
// webhook's data.id, e.g. "chg_..."). Used to independently confirm status,
// amount, currency, and reference before settling.
func (c *FlutterwaveClient) GetCharge(ctx context.Context, id string) (FWCharge, error) {
	if !c.IsEnabled() {
		return FWCharge{}, ErrFlutterwaveDisabled
	}
	if id == "" {
		return FWCharge{}, errors.New("flutterwave get-charge: id required")
	}
	resp, body, err := c.authedRequest(ctx, http.MethodGet, "/charges/"+url.PathEscape(id), nil, "")
	if err != nil {
		return FWCharge{}, fmt.Errorf("flutterwave get-charge: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		// Authoritative "this charge does not exist" — retrying cannot change it.
		return FWCharge{}, fmt.Errorf("%w: %v", ErrFWChargeNotFound, fwError("get-charge", resp.StatusCode, body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return FWCharge{}, fwError("get-charge", resp.StatusCode, body)
	}
	var env fwEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return FWCharge{}, fmt.Errorf("flutterwave get-charge decode: %w", err)
	}
	var parsed FWCharge
	if err := json.Unmarshal(env.Data, &parsed); err != nil {
		return FWCharge{}, fmt.Errorf("flutterwave get-charge decode data: %w", err)
	}
	return parsed, nil
}

// ─── Fees + rates ──────────────────────────────────────────────────────────

// FWFee is a provider fee quote for a prospective transfer/collection.
type FWFee struct {
	Currency string      `json:"currency"`
	Fee      FWFeeAmount `json:"fee"`
	VAT      FWFeeAmount `json:"vat"`
}

// FWFeeAmount is a fee/VAT amount from Flutterwave's v4 GET /fees endpoint.
//
// PRODUCTION OBSERVED (2026-08-31): GET /fees?payment_method=bank_transfer
// returned "fee" as a JSON ARRAY where the field was previously typed
// float64, failing the whole response with "cannot unmarshal array into Go
// struct field FWFee.fee of type float64" — silently, since GetFees' only
// caller (fee_quote_service.go QuoteProviderFee) treats any error as "no live
// quote" and falls back to the config percentage. This repo's Flutterwave
// credentials are sandbox-only and returned 403/500 against production for
// every endpoint tried (see fwfee-fix-report.md), so the array's ELEMENT
// SHAPE could not be observed directly and is INFERRED here, not observed:
// it assumes the array holds per-tier numeric fee components (bare numbers
// or numeric strings, mirroring the scalar shape) and sums them into a
// single total. If production's array elements turn out to be objects
// instead, this decoder still does the safe thing — it ERRORs rather than
// silently returning zero, which the next paragraph explains why that
// matters.
//
// Accepts, like FWQuoteAmount: a number, a numeric string, or null/""
// (-> 0). Additionally accepts an array of numbers/numeric strings, summed.
// Any element that isn't itself a number/numeric string/array is a decode
// ERROR, not a silent zero — a zero provider fee is a plausible-looking
// number that would under-charge on a live payout, the same reasoning
// FWQuoteAmount applies to a bad FX amount.
type FWFeeAmount float64

func (a *FWFeeAmount) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*a = 0
		return nil
	}
	if b[0] == '[' {
		var elems []json.RawMessage
		if err := json.Unmarshal(b, &elems); err != nil {
			return fmt.Errorf("FWFeeAmount: invalid array %s: %w", b, err)
		}
		var sum float64
		for _, e := range elems {
			var v FWFeeAmount
			if err := v.UnmarshalJSON(e); err != nil {
				return fmt.Errorf("FWFeeAmount: invalid array element %s: %w", e, err)
			}
			sum += float64(v)
		}
		*a = FWFeeAmount(sum)
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*a = 0
			return nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("FWFeeAmount: invalid numeric string %q: %w", s, err)
		}
		*a = FWFeeAmount(f)
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("FWFeeAmount: invalid value %s: %w", b, err)
	}
	*a = FWFeeAmount(f)
	return nil
}

// Float64 returns the amount as a plain float64 for arithmetic call sites.
func (a FWFeeAmount) Float64() float64 { return float64(a) }

// GetFees quotes Flutterwave's own fee for a payment. paymentMethod follows
// the v4 vocabulary (e.g. "bank_transfer", "mobile_money"); network/country
// refine mobile-money quotes.
func (c *FlutterwaveClient) GetFees(ctx context.Context, amount float64, currency, paymentMethod, network, country string) ([]FWFee, error) {
	if !c.IsEnabled() {
		return nil, ErrFlutterwaveDisabled
	}
	q := url.Values{}
	// Plain decimal, never scientific notation: %g renders ≥₦1,000,000 as
	// "1e+06", which the fee endpoint would mis-parse or reject.
	q.Set("amount", strconv.FormatFloat(amount, 'f', -1, 64))
	q.Set("currency", strings.ToUpper(currency))
	if paymentMethod != "" {
		q.Set("payment_method", paymentMethod)
	}
	if network != "" {
		q.Set("network", network)
	}
	if country != "" {
		q.Set("country", country)
	}
	resp, body, err := c.authedRequest(ctx, http.MethodGet, "/fees?"+q.Encode(), nil, "")
	if err != nil {
		return nil, fmt.Errorf("flutterwave fees: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fwError("fees", resp.StatusCode, body)
	}
	var env fwEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("flutterwave fees decode: %w", err)
	}
	// data may be a single object or a list depending on method — normalize.
	var many []FWFee
	if err := json.Unmarshal(env.Data, &many); err == nil {
		return many, nil
	}
	var one FWFee
	if err := json.Unmarshal(env.Data, &one); err != nil {
		return nil, fmt.Errorf("flutterwave fees decode data: %w", err)
	}
	return []FWFee{one}, nil
}

// FWRate is an FX quote between two currencies (incl. stablecoins).
//
// The vendor's top-level "rate" field is deliberately not modeled: nothing
// in this codebase reads it — DeriveFiatPerUnit/DeriveBuyFiatPerUnit (see
// stable_spread.go) compute the rate from Source/Destination instead, since
// FW's own docs never define the field's unit direction and the example is
// self-contradictory. Keeping an unused field around only to fight its wire
// type (see FWQuoteAmount) isn't worth it.
type FWRate struct {
	Source      FWMoney `json:"source"`
	Destination FWMoney `json:"destination"`
}

type FWMoney struct {
	Currency string        `json:"currency"`
	Amount   FWQuoteAmount `json:"amount"`
}

// FWQuoteAmount is a monetary amount from Flutterwave's v4 API. Observed in
// production on 2026-08-31: POST /transfers/rates returns "amount" (and
// "rate") as a JSON STRING, while the sandbox returns a JSON number for the
// same field — decoding straight into float64 fails the whole response with
// "cannot unmarshal string into Go struct field". FWQuoteAmount accepts either
// wire shape so a provider inconsistency on one field doesn't take down
// every quote.
//
// null and "" both decode to zero — a missing optional amount must not fail
// the whole response — but a non-numeric string (e.g. "abc") is a genuine
// decode error, not a silent zero: DeriveFiatPerUnit/DeriveBuyFiatPerUnit
// reject a non-positive amount via ErrBadFxProbe, and that guard only works
// if a bad value can't arrive disguised as a plausible zero.
type FWQuoteAmount float64

func (a *FWQuoteAmount) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*a = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*a = 0
			return nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("FWQuoteAmount: invalid numeric string %q: %w", s, err)
		}
		*a = FWQuoteAmount(f)
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*a = FWQuoteAmount(f)
	return nil
}

// Float64 returns the amount as a plain float64 for arithmetic call sites.
func (a FWQuoteAmount) Float64() float64 { return float64(a) }

// QuoteRate previews the FX applied to a cross-currency transfer via
// POST /transfers/rates.
func (c *FlutterwaveClient) QuoteRate(ctx context.Context, sourceCurrency, destCurrency string, destAmount float64) (FWRate, error) {
	if !c.IsEnabled() {
		return FWRate{}, ErrFlutterwaveDisabled
	}
	body, _ := json.Marshal(map[string]any{
		"source":      map[string]any{"currency": strings.ToUpper(sourceCurrency)},
		"destination": map[string]any{"currency": strings.ToUpper(destCurrency), "amount": destAmount},
	})
	resp, raw, err := c.authedRequest(ctx, http.MethodPost, "/transfers/rates", body, "")
	if err != nil {
		return FWRate{}, fmt.Errorf("flutterwave rates: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return FWRate{}, fwError("rates", resp.StatusCode, raw)
	}
	var env fwEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return FWRate{}, fmt.Errorf("flutterwave rates decode: %w", err)
	}
	var parsed FWRate
	if err := json.Unmarshal(env.Data, &parsed); err != nil {
		return FWRate{}, fmt.Errorf("flutterwave rates decode data: %w", err)
	}
	return parsed, nil
}

// ─── Balances ──────────────────────────────────────────────────────────────

// FWBalance is one currency balance on the merchant wallet
// (sandbox-verified shape: {currency, available_balance}).
type FWBalance struct {
	Currency         string  `json:"currency"`
	AvailableBalance float64 `json:"available_balance"`
}

// FetchBalances reads the merchant's wallet balances (all currencies) via
// GET /wallets/balances (path sandbox-verified 2026-07-19).
func (c *FlutterwaveClient) FetchBalances(ctx context.Context) ([]FWBalance, error) {
	if !c.IsEnabled() {
		return nil, ErrFlutterwaveDisabled
	}
	resp, body, err := c.authedRequest(ctx, http.MethodGet, "/wallets/balances", nil, "")
	if err != nil {
		return nil, fmt.Errorf("flutterwave balances: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fwError("balances", resp.StatusCode, body)
	}
	var env fwEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("flutterwave balances decode: %w", err)
	}
	var parsed []FWBalance
	if err := json.Unmarshal(env.Data, &parsed); err != nil {
		return nil, fmt.Errorf("flutterwave balances decode data: %w", err)
	}
	return parsed, nil
}

// ConvertBalance moves value between two of OUR OWN currency balances
// (e.g. USDT → NGN before an NGN payout). It is a wallet-type direct
// transfer whose recipient is our own merchant id.
//
// NOTE: the fiat→stablecoin direction is documented; stablecoin→fiat is
// dashboard-proven but undocumented — the sandbox E2E gate must confirm it
// before the sell flow depends on this call.
func (c *FlutterwaveClient) ConvertBalance(ctx context.Context, merchantID, sourceCurrency, destCurrency string, amount FWAmount, reference string) (FWTransfer, error) {
	if merchantID == "" {
		return FWTransfer{}, errors.New("flutterwave convert: merchant id required")
	}
	return c.DirectTransfer(ctx, FWDirectTransferRequest{
		Action:    "instant",
		Type:      "wallet",
		Reference: reference,
		PaymentInstruction: FWPaymentInstruction{
			SourceCurrency:      strings.ToUpper(sourceCurrency),
			DestinationCurrency: strings.ToUpper(destCurrency),
			Amount:              amount,
			Recipient: FWTransferRecipient{
				Wallet: &FWRecipientWallet{Provider: "flutterwave", Identifier: merchantID},
			},
		},
	})
}

// ─── Wallet statement ──────────────────────────────────────────────────────

// fwStatementDateLayout is the window format sent to /wallets/statement. The
// parameter names and layout are UNVERIFIED — Flutterwave documents neither —
// but date-only from/to is its convention on every other listing endpoint.
const fwStatementDateLayout = "2006-01-02"

// fwUpperBoundDate renders the `to` of a date-range query, or "" when it must
// be left off.
//
// Flutterwave's date bounds look inclusive-of-day, so the day AFTER the window
// ends is what includes the end's own day — but it also refuses any `to` it
// reads as later than today, with HTTP 400 REQUEST_NOT_VALID "'to' cannot be
// in the future". For a window ending today those two rules cannot both be
// satisfied: the day after today is tomorrow. So a window that reaches the
// present sends no `to` at all and lets the provider read up to now, and only
// a window that has already closed carries the +1 day.
//
// Sending today's date instead would be the worse trade: if the bound really
// is exclusive, every credit that arrived today is invisible until tomorrow.
func fwUpperBoundDate(end, now time.Time) string {
	next := end.UTC().AddDate(0, 0, 1)
	if next.UTC().Truncate(24 * time.Hour).After(now.UTC().Truncate(24 * time.Hour)) {
		return ""
	}
	return next.Format(fwStatementDateLayout)
}

const (
	// fwStatementProviderMaxPageSize is the most rows Flutterwave will serve
	// per /wallets/statement page. Asking for more is not clamped — the whole
	// request fails with HTTP 400 REQUEST_NOT_VALID "must be less than or
	// equal to 50", so every poll tick errors and no credit is ever ingested.
	fwStatementProviderMaxPageSize = 50
	// fwStatementPageSize is how many rows one request asks for. Also the
	// signal that a page is the last one: a short page ends the walk. Pinned
	// to the provider's maximum — see above.
	fwStatementPageSize = fwStatementProviderMaxPageSize
	// fwStatementMaxPages bounds the walk so a provider that keeps handing
	// back full pages cannot spin the poll forever. Reaching it means the
	// window was NOT fully read, which is why it is logged rather than
	// absorbed. 100 pages of 50 keeps the 5,000-row ceiling the walk had
	// when it asked for (unservable) pages of 100.
	fwStatementMaxPages = 100
)

// FWStatementTime is a statement timestamp.
//
// It is a bespoke type rather than a plain time.Time because the layout is
// unverified, and a strict decode would fail the WHOLE page on one oddly
// formatted row. A dropped page is a dropped credit — the single outcome the
// inbound pipeline exists to prevent. An unparseable value therefore decodes
// to the zero time and the ingestor falls back to its own clock.
type FWStatementTime struct{ time.Time }

func (t *FWStatementTime) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		t.Time = time.Time{}
		return nil
	}
	var s string
	if b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		// Epoch seconds/millis are a plausible alternative shape and cost
		// nothing to accept.
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			t.Time = time.Time{}
			return nil
		}
		if n > 1e12 {
			t.Time = time.UnixMilli(n).UTC()
		} else {
			t.Time = time.Unix(n, 0).UTC()
		}
		return nil
	}
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		fwStatementDateLayout,
	} {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed.UTC()
			return nil
		}
	}
	t.Time = time.Time{}
	return nil
}

// FWStatementBalance is the running wallet balance either side of one
// statement row. A row that actually moves the wallet leaves before != after;
// a row that is only a memo of value which never landed leaves them equal.
type FWStatementBalance struct {
	Before FWQuoteAmount `json:"before"`
	After  FWQuoteAmount `json:"after"`
}

// FWStatementEntry is one row of the merchant wallet statement.
//
// The shape is production-measured (2026-09-02), not documented — Flutterwave
// publishes no schema for this endpoint. Two measured facts drive the whole
// central-account attribution design:
//
//   - Reference on a `funding` row is the NIBSS NIP session id (30 digits),
//     unique per credit. It is the ONLY viable dedupe key: funding rows carry
//     no account number and no `transfer` sub-object (transfer and fee rows
//     do carry one).
//   - Remarks is free text — "Received Money from GTBANK|ADEMU EMMANUEL O." —
//     bank left of the pipe, account name right. Unstable by construction, so
//     nothing downstream may depend on it parsing.
//
// Unknown fields are ignored rather than rejected so a provider that starts
// returning more does not break the poll.
type FWStatementEntry struct {
	// Direction is "credit" | "debit".
	Direction string `json:"direction"`
	// Type is one of transfer | fee | reversal | settlement | funding | bill |
	// chargeback | card. Only `funding` is customer money arriving.
	Type      string             `json:"type"`
	Amount    FWQuoteAmount      `json:"amount"`
	Balance   FWStatementBalance `json:"balance"`
	Reference string             `json:"reference"`
	Remarks   string             `json:"remarks"`
	// Currency is absent on some measured rows; callers fall back to the
	// currency they asked for.
	Currency        string          `json:"currency"`
	TransactionDate FWStatementTime `json:"transaction_date"`
}

// FetchWalletStatement reads the merchant wallet's transaction statement via
// GET /wallets/statement (production-measured 2026-09-02, undocumented).
//
// This is the ONLY detection channel for money paid into the central account:
// wallet funding fires no webhook at all, so a credit nobody polled for is a
// credit nobody ever sees. from/to bound the window and are omitted when zero.
//
// The window is walked page by page. One request returns one page, and a busy
// day does not fit in one: everything past it is money that was never seen,
// and the reconciler — which proves coverage by finding a row older than the
// window start — has to decline every tick instead of catching it, so the
// ingest path and its safety net fail at the same threshold.
//
// The paging parameter names are as unverified as everything else here, so the
// walk never trusts them: it stops on a short page, stops when a page comes
// back identical to the one before it (the shape of a provider ignoring `page`
// entirely, which would otherwise multiply the window rather than extend it),
// and stops at a hard bound with a warning rather than spinning.
//
// The end bound is pushed a day past `to` because the parameters are date-only
// and we do not know whether the provider reads them as inclusive. Read as
// exclusive, "to = today" hides every credit that arrived today. Over-fetching
// is free — ingestion is idempotent on the NIP session id and the reconciler
// filters by timestamp — while under-fetching loses money.
func (c *FlutterwaveClient) FetchWalletStatement(ctx context.Context, currency string, from, to time.Time) ([]FWStatementEntry, error) {
	if !c.IsEnabled() {
		return nil, ErrFlutterwaveDisabled
	}
	pageQuery := func(page int) url.Values {
		q := url.Values{}
		if cur := strings.ToUpper(strings.TrimSpace(currency)); cur != "" {
			q.Set("currency", cur)
		}
		if !from.IsZero() {
			q.Set("from", from.UTC().Format(fwStatementDateLayout))
		}
		if !to.IsZero() {
			if upper := fwUpperBoundDate(to, time.Now()); upper != "" {
				q.Set("to", upper)
			}
		}
		q.Set("page", strconv.Itoa(page))
		q.Set("size", strconv.Itoa(fwStatementPageSize))
		return q
	}

	var all, prev []FWStatementEntry
	cursor := ""
	for page := 1; page <= fwStatementMaxPages; page++ {
		// A failed page is not returned as a short window: partial results
		// would read downstream as "the rest of the window is empty", and the
		// row order the provider uses is unverified, so there is no telling
		// which part is missing. The poll retries in seconds.
		q := pageQuery(page)
		if cursor != "" {
			q.Set("next", cursor)
		}
		rows, hasMore, sawCursor, next, err := c.fetchStatementPage(ctx, q)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return all, nil
		}
		if page > 1 && sameStatementPage(prev, rows) {
			return all, nil
		}
		all = append(all, rows...)
		// The provider's own cursor is the authoritative end of the window
		// when it sends one; the short-page rule is the fallback for the
		// payload shape that carries no cursor.
		if sawCursor {
			if !hasMore {
				return all, nil
			}
		} else if len(rows) < fwStatementPageSize {
			return all, nil
		}
		cursor = next
		prev = rows
	}
	log.Printf("[flutterwave] WARNING statement page bound reached: %d pages of %d rows read and the provider still has more; the window is truncated and older credits may be unseen",
		fwStatementMaxPages, fwStatementPageSize)
	return all, nil
}

// fwMoney reads a money field that the provider sends either as a bare number
// or as {value, currency}. The statement's `amount` is the object form; older
// measured payloads and other endpoints use the number.
type fwMoney struct {
	Value    FWQuoteAmount
	Currency string
}

func (m *fwMoney) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '{' {
		var o struct {
			Value    FWQuoteAmount `json:"value"`
			Currency string        `json:"currency"`
		}
		if err := json.Unmarshal(b, &o); err != nil {
			return err
		}
		m.Value, m.Currency = o.Value, o.Currency
		return nil
	}
	return m.Value.UnmarshalJSON(b)
}

// fwStatementWireRow is the statement row as sent, which is not the shape this
// client was written against. Measured in production 2026-09-17:
//
//	transaction_direction / transaction_type, not direction / type
//	amount as {value, currency}, not a bare number
//
// The legacy names stay as fallbacks so either payload reads. This matters
// more than a decode error would suggest: an unknown field name leaves
// Direction and Type empty, and a row with no type is silently not funding —
// ingestion would report success and credit nobody.
type fwStatementWireRow struct {
	TransactionDirection string `json:"transaction_direction"`
	Direction            string `json:"direction"`
	TransactionType      string `json:"transaction_type"`
	Type                 string `json:"type"`
	Amount               fwMoney
	Balance              FWStatementBalance `json:"balance"`
	Reference            string             `json:"reference"`
	Remarks              string             `json:"remarks"`
	Currency             string             `json:"currency"`
	TransactionDate      FWStatementTime    `json:"transaction_date"`
}

func (r fwStatementWireRow) entry() FWStatementEntry {
	e := FWStatementEntry{
		Direction:       firstNonEmpty(r.TransactionDirection, r.Direction),
		Type:            firstNonEmpty(r.TransactionType, r.Type),
		Amount:          r.Amount.Value,
		Balance:         r.Balance,
		Reference:       r.Reference,
		Remarks:         r.Remarks,
		Currency:        firstNonEmpty(r.Currency, r.Amount.Currency),
		TransactionDate: r.TransactionDate,
	}
	return e
}

// decodeStatementPage reads one page in either shape the provider has served:
// `data` as {cursor, transactions} — the live shape — or as a bare array.
// hasMore comes from the provider's own cursor when it sends one; without a
// cursor the caller falls back to its short-page rule.
func decodeStatementPage(body []byte) (rows []FWStatementEntry, hasMore, sawCursor bool, next string, err error) {
	var env fwEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, false, false, "", fmt.Errorf("flutterwave statement decode: %w", err)
	}
	data := bytes.TrimSpace(env.Data)
	var wire []fwStatementWireRow

	switch {
	case len(data) == 0 || string(data) == "null":
		return nil, false, false, "", nil
	case data[0] == '[':
		if err := json.Unmarshal(data, &wire); err != nil {
			return nil, false, false, "", fmt.Errorf("flutterwave statement decode data: %w", err)
		}
	default:
		var page struct {
			Transactions []fwStatementWireRow `json:"transactions"`
			Cursor       *struct {
				HasMoreItems *bool  `json:"has_more_items"`
				Next         string `json:"next"`
			} `json:"cursor"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, false, false, "", fmt.Errorf("flutterwave statement decode data: %w", err)
		}
		wire = page.Transactions
		if page.Cursor != nil {
			sawCursor = true
			next = strings.TrimSpace(page.Cursor.Next)
			hasMore = page.Cursor.HasMoreItems != nil && *page.Cursor.HasMoreItems
		}
	}

	rows = make([]FWStatementEntry, 0, len(wire))
	for _, w := range wire {
		rows = append(rows, w.entry())
	}
	return rows, hasMore, sawCursor, next, nil
}

func (c *FlutterwaveClient) fetchStatementPage(ctx context.Context, q url.Values) ([]FWStatementEntry, bool, bool, string, error) {
	resp, body, err := c.authedRequest(ctx, http.MethodGet, "/wallets/statement?"+q.Encode(), nil, "")
	if err != nil {
		return nil, false, false, "", fmt.Errorf("flutterwave statement: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, false, "", fwError("statement", resp.StatusCode, body)
	}
	return decodeStatementPage(body)
}

// sameStatementPage reports whether two consecutive pages are the same rows —
// the only evidence we can get that the provider is ignoring `page`. Rows are
// compared rather than deduped because two genuinely identical rows do occur,
// and dropping one would understate the provider's side and raise a phantom
// surplus at the reconciler.
func sameStatementPage(a, b []FWStatementEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		switch {
		case a[i].Reference != b[i].Reference,
			a[i].Amount != b[i].Amount,
			a[i].Remarks != b[i].Remarks,
			a[i].Balance.After != b[i].Balance.After,
			!a[i].TransactionDate.Equal(b[i].TransactionDate.Time):
			return false
		}
	}
	return true
}
