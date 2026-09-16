package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// Throwaway probe. NOT a regression test — it answers one question and should be
// deleted once the answer is recorded.
//
// THE QUESTION: the stablecoin minimum (DefaultStableMinSellMajor = "10") is OUR
// assumption, read from Flutterwave's documentation during Phase A and never
// verified against the API. Nothing in this codebase has ever asked the provider
// what its actual floor is. We reject trades on a guess.
//
// Phase A could not settle it: GET /fees returned 501 for every payment_method in
// sandbox, and its response shape (currency/fee/vat) carries no minimum anyway.
// This re-asks against whatever base URL the environment points at, and — more
// usefully — walks POST /transfers/rates DOWN through small amounts to find where
// the provider itself starts refusing.
//
// Read-only by construction. /fees and /transfers/rates are both quote endpoints:
// nothing is created, converted, or transferred. It never prints a credential.
func TestProbe_FlutterwaveLimits(t *testing.T) {
	if os.Getenv("FW_PROBE") == "" {
		t.Skip("set FW_PROBE=1 to run the live Flutterwave limits probe")
	}
	_ = godotenv.Load(filepath.Join("..", "..", ".env"))

	clientID := strings.TrimSpace(os.Getenv("FLUTTERWAVE_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("FLUTTERWAVE_CLIENT_SECRET"))
	if clientID == "" || clientSecret == "" {
		t.Skip("no Flutterwave credentials in the environment — skipping")
	}
	baseURL := strings.TrimSpace(os.Getenv("FLUTTERWAVE_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://developersandbox-api.flutterwave.com"
	}

	// Which environment answered matters more than the answer: a 501 from sandbox
	// repeats what Phase A already knows and settles nothing.
	t.Logf("BASE URL: %s", baseURL)
	if strings.Contains(baseURL, "sandbox") {
		t.Logf("WARNING: this is SANDBOX. A 501 here tells us nothing new — "+
			"Phase A already recorded that. Point FLUTTERWAVE_BASE_URL at production "+
			"to actually answer the question. (%s)", baseURL)
	}

	// idpURL empty -> the client's own default; the token endpoint is separate
	// from the API base and is not what this probe is varying.
	c := NewFlutterwaveClient(baseURL, strings.TrimSpace(os.Getenv("FLUTTERWAVE_IDP_URL")), clientID, clientSecret)
	if c == nil {
		t.Skip("Flutterwave client not constructible from this environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. Does /fees answer at all, and does anything in it imply a floor? ──
	t.Log("── GET /fees ──")
	for _, pm := range []string{"bank_transfer", "crypto", "wallet"} {
		fees, err := c.GetFees(ctx, 1000, "NGN", pm, "", "NG")
		if err != nil {
			t.Logf("  fees payment_method=%-14s ERROR: %v", pm, err)
			continue
		}
		for _, f := range fees {
			t.Logf("  fees payment_method=%-14s currency=%s fee=%v vat=%v", pm, f.Currency, f.Fee, f.VAT)
		}
	}

	// ── 2. Walk the rate endpoint down until the provider refuses. ──
	// This is the part that can actually answer the question: if Flutterwave
	// enforces a floor, a small enough destination amount should be rejected,
	// and the error text usually names the limit.
	t.Log("── POST /transfers/rates, descending (NGN -> USDC) ──")
	for _, dest := range []float64{100, 50, 20, 10, 5, 2, 1, 0.5, 0.1} {
		rate, err := c.QuoteRate(ctx, "NGN", "USDC", dest)
		if err != nil {
			t.Logf("  dest=%-6v REFUSED: %v", dest, err)
			continue
		}
		t.Logf("  dest=%-6v ok  src=%v %s  dst=%v %s",
			dest,
			rate.Source.Amount.Float64(), rate.Source.Currency,
			rate.Destination.Amount.Float64(), rate.Destination.Currency)
	}

	// ── 3. Same walk on the sell direction, which uses a different pair. ──
	t.Log("── POST /transfers/rates, descending (USDC -> NGN) ──")
	for _, dest := range []float64{100000, 10000, 5000, 1000, 500, 100} {
		rate, err := c.QuoteRate(ctx, "USDC", "NGN", dest)
		if err != nil {
			t.Logf("  dest=%-8v REFUSED: %v", dest, err)
			continue
		}
		t.Logf("  dest=%-8v ok  src=%v %s  dst=%v %s",
			dest,
			rate.Source.Amount.Float64(), rate.Source.Currency,
			rate.Destination.Amount.Float64(), rate.Destination.Currency)
	}

	t.Log("── done. Record the findings, then delete this file. ──")
}

// Throwaway probe #2. Finds the TSA account details users would transfer INTO,
// when the dashboard is unavailable.
//
// GET only — every path below is a read. Nothing is created, and no credential
// is printed. Bodies are truncated: a wallet or account response can carry
// identifiers we have no reason to spill into a terminal.
//
// The client has no "describe my own account" call today, which is the point:
// this establishes whether one exists before anybody writes it.
func TestProbe_FlutterwaveOwnAccount(t *testing.T) {
	if os.Getenv("FW_PROBE") == "" {
		t.Skip("set FW_PROBE=1 to run the live Flutterwave account probe")
	}
	_ = godotenv.Load(filepath.Join("..", "..", ".env"))

	clientID := strings.TrimSpace(os.Getenv("FLUTTERWAVE_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("FLUTTERWAVE_CLIENT_SECRET"))
	if clientID == "" || clientSecret == "" {
		t.Skip("no Flutterwave credentials in the environment — skipping")
	}
	baseURL := strings.TrimSpace(os.Getenv("FLUTTERWAVE_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://developersandbox-api.flutterwave.com"
	}
	t.Logf("BASE URL: %s", baseURL)

	c := NewFlutterwaveClient(baseURL, strings.TrimSpace(os.Getenv("FLUTTERWAVE_IDP_URL")), clientID, clientSecret)
	if c == nil {
		t.Skip("Flutterwave client not constructible from this environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Candidates, most likely first. A 404 is a useful answer: it rules the path
	// out. A 200 tells us what we can build on.
	paths := []string{
		"/virtual-accounts",     // may list the accounts already provisioned
		"/wallets",              // sibling of the known /wallets/balances
		"/wallets/balances",     // known-good; proves auth is working
		"/merchants/me",
		"/me",
		"/profile",
		"/accounts",
		"/settlements",          // documented; also shows the settlement cycle
	}

	for _, p := range paths {
		resp, body, err := c.authedRequest(ctx, "GET", p, nil, "")
		if err != nil {
			t.Logf("GET %-22s TRANSPORT ERROR: %v", p, err)
			continue
		}
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 600 {
			snippet = snippet[:600] + "…(truncated)"
		}
		t.Logf("GET %-22s %d\n      %s", p, resp.StatusCode, snippet)
	}

	t.Log("── done. Record what answered, then delete this file. ──")
}
