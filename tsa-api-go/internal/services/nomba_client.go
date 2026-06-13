package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// NombaClient wraps the Nomba REST API for the Nigerian bank directory
// (GET /v1/transfers/banks) and account name enquiry
// (POST /v1/transfers/bank/lookup). The same OAuth credentials authorize
// the Phase-2 payout endpoints, so this client doubles as the seam where
// payouts will later land.
//
// Token lifecycle: tokens are issued via /v1/auth/token/issue and cached
// in memory until ~30s before their declared expiry. A single-flight
// mutex prevents thundering-herd refresh.
//
// Bank list and name-enquiry results are cached in memory — banks for
// 24h (they "rarely change" per Nomba), name-enquiry for 5 minutes
// (handles user retyping the same account, but stays fresh enough that
// edited account numbers don't return stale names).
type NombaClient struct {
	baseURL      string
	clientID     string
	clientSecret string
	accountID    string
	httpClient   *http.Client

	tokenMu    sync.Mutex
	token      string
	tokenExpAt time.Time

	banksMu  sync.Mutex
	banks    []NombaBank
	banksAt  time.Time

	lookupMu sync.Mutex
	lookups  map[string]lookupEntry
}

type NombaBank struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type lookupEntry struct {
	at     time.Time
	result NombaLookupResult
}

type NombaLookupResult struct {
	AccountNumber string `json:"accountNumber"`
	AccountName   string `json:"accountName"`
}

const (
	nombaBanksTTL   = 24 * time.Hour
	nombaLookupTTL  = 5 * time.Minute
	nombaHTTPTimout = 15 * time.Second
)

// ErrNombaDisabled is returned when callers try to use a client whose
// credentials weren't configured at startup.
var ErrNombaDisabled = errors.New("nomba not configured")

// ErrNombaAccountNotFound is returned by Lookup when Nomba reports the
// account number does not resolve at the supplied bank.
var ErrNombaAccountNotFound = errors.New("account not found")

// NewNombaClient returns nil if any required credential is missing —
// callers must check `c != nil` (or wrap with IsEnabled) before use so
// dev environments without Nomba creds keep booting.
func NewNombaClient(baseURL, clientID, clientSecret, accountID string) *NombaClient {
	if clientID == "" || clientSecret == "" || accountID == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = "https://sandbox.nomba.com"
	}
	return &NombaClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
		accountID:    accountID,
		httpClient:   &http.Client{Timeout: nombaHTTPTimout},
		lookups:      make(map[string]lookupEntry),
	}
}

// IsEnabled is safe to call on a nil receiver.
func (c *NombaClient) IsEnabled() bool { return c != nil }

// ─── Token management ──────────────────────────────────────────────────────

type tokenIssueResp struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	Data        struct {
		AccessToken string `json:"access_token"`
		ExpiresAt   string `json:"expiresAt"`
		ExpiresIn   int    `json:"expires_in"`
	} `json:"data"`
}

func (c *NombaClient) ensureToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExpAt.Add(-30*time.Second)) {
		return c.token, nil
	}
	body := map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/auth/token/issue", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accountId", c.accountID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("nomba token request: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("nomba token: HTTP %d: %s", resp.StatusCode, string(data))
	}
	var parsed tokenIssueResp
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("nomba token decode: %w", err)
	}
	if parsed.Data.AccessToken == "" {
		return "", fmt.Errorf("nomba token: empty access_token (code=%s)", parsed.Code)
	}
	c.token = parsed.Data.AccessToken
	c.tokenExpAt = computeTokenExpiry(parsed.Data.ExpiresAt, parsed.Data.ExpiresIn)
	return c.token, nil
}

func computeTokenExpiry(expiresAt string, expiresIn int) time.Time {
	if expiresAt != "" {
		if t, err := time.Parse(time.RFC3339, expiresAt); err == nil {
			return t
		}
	}
	if expiresIn > 0 {
		return time.Now().Add(time.Duration(expiresIn) * time.Second)
	}
	// Conservative default — re-auth in an hour even if Nomba's response
	// shape is unfamiliar.
	return time.Now().Add(1 * time.Hour)
}

// authedRequest builds a request with the bearer + accountId headers and
// retries once on 401 by forcing a token refresh.
func (c *NombaClient) authedRequest(ctx context.Context, method, path string, body []byte) (*http.Response, []byte, error) {
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
		req.Header.Set("accountId", c.accountID)
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

// ─── Bank list ─────────────────────────────────────────────────────────────

type banksResp struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	Data        struct {
		Results []NombaBank `json:"results"`
	} `json:"data"`
}

// ListBanks returns the Nomba-supported Nigerian banks. Cached for 24h
// in memory. Callers receive a copy of the slice so they can sort/filter
// without affecting the cache.
func (c *NombaClient) ListBanks(ctx context.Context) ([]NombaBank, error) {
	if !c.IsEnabled() {
		return nil, ErrNombaDisabled
	}
	c.banksMu.Lock()
	if len(c.banks) > 0 && time.Since(c.banksAt) < nombaBanksTTL {
		out := make([]NombaBank, len(c.banks))
		copy(out, c.banks)
		c.banksMu.Unlock()
		return out, nil
	}
	c.banksMu.Unlock()

	resp, raw, err := c.authedRequest(ctx, http.MethodGet, "/v1/transfers/banks", nil)
	if err != nil {
		return nil, fmt.Errorf("nomba banks: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("nomba banks: HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var parsed banksResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("nomba banks decode: %w", err)
	}
	banks := make([]NombaBank, 0, len(parsed.Data.Results))
	for _, b := range parsed.Data.Results {
		if b.Code == "" || b.Name == "" {
			continue
		}
		banks = append(banks, b)
	}
	c.banksMu.Lock()
	c.banks = banks
	c.banksAt = time.Now()
	c.banksMu.Unlock()

	out := make([]NombaBank, len(banks))
	copy(out, banks)
	return out, nil
}

// ─── Account lookup ────────────────────────────────────────────────────────

type lookupResp struct {
	Code        string            `json:"code"`
	Description string            `json:"description"`
	Data        NombaLookupResult `json:"data"`
}

// Lookup resolves a 10-digit Nigerian NUBAN against a bank code, returning
// the account holder name as Nomba/NIBSS reports it. Results are cached
// for 5 minutes by (bankCode, accountNumber) so repeated form keystrokes
// don't burn rate-limit budget.
func (c *NombaClient) Lookup(ctx context.Context, bankCode, accountNumber string) (NombaLookupResult, error) {
	if !c.IsEnabled() {
		return NombaLookupResult{}, ErrNombaDisabled
	}
	bankCode = strings.TrimSpace(bankCode)
	accountNumber = strings.TrimSpace(accountNumber)
	if bankCode == "" || len(accountNumber) != 10 {
		return NombaLookupResult{}, fmt.Errorf("invalid bank code or account number")
	}
	key := bankCode + ":" + accountNumber
	c.lookupMu.Lock()
	if entry, ok := c.lookups[key]; ok && time.Since(entry.at) < nombaLookupTTL {
		c.lookupMu.Unlock()
		return entry.result, nil
	}
	c.lookupMu.Unlock()

	body, _ := json.Marshal(map[string]string{
		"bankCode":      bankCode,
		"accountNumber": accountNumber,
	})
	resp, raw, err := c.authedRequest(ctx, http.MethodPost, "/v1/transfers/bank/lookup", body)
	if err != nil {
		return NombaLookupResult{}, fmt.Errorf("nomba lookup: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return NombaLookupResult{}, ErrNombaAccountNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return NombaLookupResult{}, fmt.Errorf("nomba lookup: HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var parsed lookupResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return NombaLookupResult{}, fmt.Errorf("nomba lookup decode: %w", err)
	}
	if parsed.Data.AccountName == "" {
		return NombaLookupResult{}, ErrNombaAccountNotFound
	}
	c.lookupMu.Lock()
	c.lookups[key] = lookupEntry{at: time.Now(), result: parsed.Data}
	c.lookupMu.Unlock()
	return parsed.Data, nil
}
