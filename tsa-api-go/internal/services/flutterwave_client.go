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
	var env fwEnvelope
	_ = json.Unmarshal(body, &env)
	msg := env.Message
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
// nested {first,last} (sandbox-verified 2026-07-19). The sandbox did NOT
// enforce BVN/NIN at customer creation; production NGN VAs may still require
// it — the identification shape stays best-effort until a live-mode check.
type FWCustomerRequest struct {
	Email         string  `json:"email"`
	Name          *FWName `json:"name,omitempty"`
	Phone         string  `json:"phone,omitempty"`
	IDType        string  `json:"id_type,omitempty"`   // "BVN" | "NIN" (live-mode TBC)
	IDNumber      string  `json:"id_number,omitempty"` // the BVN/NIN digits
	MetaReference string  `json:"-"`
}

// FWName is the nested name object used by customers (and transfer
// recipients accept the same shape).
type FWName struct {
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return FWTransfer{}, fwError("direct-transfer", resp.StatusCode, body)
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
		return FWTransfer{}, fmt.Errorf("flutterwave direct-transfer failed: %s", env.Message)
	}
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

// ─── Fees + rates ──────────────────────────────────────────────────────────

// FWFee is a provider fee quote for a prospective transfer/collection.
type FWFee struct {
	Currency string  `json:"currency"`
	Fee      float64 `json:"fee"`
	VAT      float64 `json:"vat"`
}

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
type FWRate struct {
	Rate        float64 `json:"rate"`
	Source      FWMoney `json:"source"`
	Destination FWMoney `json:"destination"`
}

type FWMoney struct {
	Currency string  `json:"currency"`
	Amount   float64 `json:"amount"`
}

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
