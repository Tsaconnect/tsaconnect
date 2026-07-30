// HTTP-level tests for the MCGP pay/relay endpoints.
//
// These exist because of the interface seam (mcgpSwapPort / mcgpRelayPort): the
// concrete swap service reaches a chain on construction, which is why this
// handler shipped with no tests at all. In particular this file finally covers
// PreparePay end to end — the idempotency guarantee that T0b could only assert
// at the pure-function level.

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ── doubles ──────────────────────────────────────────────────────────────

type fakeSwapPort struct {
	prepareSpendCalls int
	lastRef           [32]byte
}

func (f *fakeSwapPort) Allowance(string, string) (*big.Int, error) { return big.NewInt(0), nil }
func (f *fakeSwapPort) LegacyEntitlement(string) (*big.Int, error) { return big.NewInt(0), nil }
func (f *fakeSwapPort) LegacyRemaining(string) (*big.Int, error)   { return big.NewInt(0), nil }
func (f *fakeSwapPort) PremiumBalance(string) (*big.Int, error)    { return big.NewInt(0), nil }
func (f *fakeSwapPort) PrepareApprove(string, string, *big.Int) ([]byte, error) {
	return preparedTx(), nil
}
func (f *fakeSwapPort) PrepareBuyPremium(string, *big.Int, *big.Int) ([]byte, error) {
	return preparedTx(), nil
}
func (f *fakeSwapPort) PrepareSellLegacy(string, *big.Int, *big.Int) ([]byte, error) {
	return preparedTx(), nil
}
func (f *fakeSwapPort) PrepareSellPremium(string, *big.Int, *big.Int) ([]byte, error) {
	return preparedTx(), nil
}
func (f *fakeSwapPort) PrepareSpend(_, _ string, _ *big.Int, ref [32]byte) ([]byte, error) {
	f.prepareSpendCalls++
	f.lastRef = ref
	return preparedTx(), nil
}
func (f *fakeSwapPort) PrepareWithdrawPremium(string, *big.Int) ([]byte, error) {
	return preparedTx(), nil
}
func (f *fakeSwapPort) Quote(uint8, *big.Int) (*big.Int, error) { return big.NewInt(20000), nil }
func (f *fakeSwapPort) VerifyAmountEvent(string, string, common.Address) (*services.AmountEvent, error) {
	return nil, nil
}
func (f *fakeSwapPort) VerifyPremiumSpent(string, common.Address, common.Address, [32]byte) (*services.PremiumSpentEvent, error) {
	return nil, nil
}

func preparedTx() []byte {
	b, _ := json.Marshal(map[string]any{"to": "0xabc", "value": "0", "data": "aabbcc"})
	return b
}

type fakeRelayPort struct {
	enabled     bool
	relayCalls  int
	relayErr    error
	scaffoldErr error
	lastWallet  string
}

func (f *fakeRelayPort) IsEnabled() bool { return f.enabled }
func (f *fakeRelayPort) Relay(_ context.Context, wallet string, _ services.ForwardRequest) (string, error) {
	f.relayCalls++
	f.lastWallet = wallet
	if f.relayErr != nil {
		return "", f.relayErr
	}
	return "0xrelayed", nil
}
func (f *fakeRelayPort) Scaffold(_ context.Context, _, to string, data []byte, gas uint64) (services.ForwardRequestScaffold, error) {
	if f.scaffoldErr != nil {
		return services.ForwardRequestScaffold{}, f.scaffoldErr
	}
	return services.ForwardRequestScaffold{
		Domain:   services.EIP712Domain{Name: "TSAConnectForwarder", Version: "1", ChainID: "146", VerifyingContract: "0xfwd"},
		To:       to,
		Data:     "0x" + common.Bytes2Hex(data),
		Gas:      gas,
		Nonce:    "0",
		Deadline: 1234567890,
		Value:    "0",
	}, nil
}

// ── rig ──────────────────────────────────────────────────────────────────

type relayRig struct {
	router *gin.Engine
	swap   *fakeSwapPort
	relay  *fakeRelayPort
	payer  *models.User
}

func newRelayRig(t *testing.T, relayEnabled bool) *relayRig {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY, username TEXT, name TEXT, wallet_address TEXT
	)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}

	payer := &models.User{
		ID:            uuid.New(),
		Username:      "payer",
		Name:          "Payer",
		WalletAddress: "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE",
	}
	payee := &models.User{
		ID:            uuid.New(),
		Username:      "payee",
		Name:          "Payee",
		WalletAddress: "0x09124a9E3c1B5AcF4207E33828195D00846E5EA8",
	}
	for _, u := range []*models.User{payer, payee} {
		if err := db.Exec(`INSERT INTO users (id, username, name, wallet_address) VALUES (?,?,?,?)`,
			u.ID.String(), u.Username, u.Name, u.WalletAddress).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}

	swap := &fakeSwapPort{}
	relay := &fakeRelayPort{enabled: relayEnabled}
	h := &MCGPSwapHandler{db: db, swap: swap}
	h.WithRelayer(relay, "0x1111111111111111111111111111111111111111")

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", payer); c.Next() })
	r.POST("/pay/prepare", h.PreparePay)
	r.GET("/relay/status", h.RelayStatus)
	r.POST("/relay", h.SubmitRelay)

	return &relayRig{router: r, swap: swap, relay: relay, payer: payer}
}

func (rr *relayRig) post(t *testing.T, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	rr.router.ServeHTTP(w, req)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func dataOf(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	d, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("response has no data object: %v", resp)
	}
	return d
}

// ── tests ────────────────────────────────────────────────────────────────

// The guarantee T0b could only prove at the function level: one intent, one ref,
// even across separate HTTP calls. Without it a relayed spend and a self-paid
// retry are two different refs, and both land.
func TestMCGPSwapHandler_PreparePay_SameIdempotencyKeyReturnsSameRef(t *testing.T) {
	rr := newRelayRig(t, false)
	body := map[string]any{"username": "payee", "mcgpAmount": "1000"}
	hdr := map[string]string{"Idempotency-Key": "intent-42"}

	code1, resp1 := rr.post(t, "/pay/prepare", body, hdr)
	code2, resp2 := rr.post(t, "/pay/prepare", body, hdr)

	if code1 != http.StatusOK || code2 != http.StatusOK {
		t.Fatalf("expected 200s, got %d and %d", code1, code2)
	}
	ref1 := dataOf(t, resp1)["ref"]
	ref2 := dataOf(t, resp2)["ref"]
	if ref1 != ref2 {
		t.Fatalf("same intent must derive one ref: %v vs %v", ref1, ref2)
	}
}

func TestMCGPSwapHandler_PreparePay_DistinctKeysGiveDistinctRefs(t *testing.T) {
	rr := newRelayRig(t, false)
	body := map[string]any{"username": "payee", "mcgpAmount": "1000"}

	_, resp1 := rr.post(t, "/pay/prepare", body, map[string]string{"Idempotency-Key": "one"})
	_, resp2 := rr.post(t, "/pay/prepare", body, map[string]string{"Idempotency-Key": "two"})

	if dataOf(t, resp1)["ref"] == dataOf(t, resp2)["ref"] {
		t.Fatal("distinct intents must not share a ref")
	}
}

func TestMCGPSwapHandler_PreparePay_WithoutAKeyStillMintsAFreshRef(t *testing.T) {
	rr := newRelayRig(t, false)
	body := map[string]any{"username": "payee", "mcgpAmount": "1000"}

	_, resp1 := rr.post(t, "/pay/prepare", body, nil)
	_, resp2 := rr.post(t, "/pay/prepare", body, nil)

	// Older app builds send no key; they keep the previous random-ref behaviour.
	if dataOf(t, resp1)["ref"] == dataOf(t, resp2)["ref"] {
		t.Fatal("without an idempotency key each prepare should mint a fresh ref")
	}
}

// The app must be able to rebuild the calldata locally and byte-compare it
// before signing, which it cannot do if it never learns the payee's address.
func TestMCGPSwapHandler_PreparePay_ReturnsResolvedRecipientAddress(t *testing.T) {
	rr := newRelayRig(t, false)
	_, resp := rr.post(t, "/pay/prepare", map[string]any{"username": "payee", "mcgpAmount": "1000"}, nil)

	got := dataOf(t, resp)["recipientAddress"]
	if got != "0x09124a9E3c1B5AcF4207E33828195D00846E5EA8" {
		t.Fatalf("expected the resolved payee address, got %v", got)
	}
}

func TestMCGPSwapHandler_PreparePay_OmitsForwardRequestWhenRelayerDisabled(t *testing.T) {
	rr := newRelayRig(t, false)
	_, resp := rr.post(t, "/pay/prepare", map[string]any{"username": "payee", "mcgpAmount": "1000"}, nil)

	if _, present := dataOf(t, resp)["forwardRequest"]; present {
		t.Fatal("a disabled relayer must not offer a forward request")
	}
}

func TestMCGPSwapHandler_PreparePay_IncludesForwardRequestWhenRelayerEnabled(t *testing.T) {
	rr := newRelayRig(t, true)
	_, resp := rr.post(t, "/pay/prepare", map[string]any{"username": "payee", "mcgpAmount": "1000"}, nil)

	fr, present := dataOf(t, resp)["forwardRequest"].(map[string]any)
	if !present {
		t.Fatalf("expected a forwardRequest scaffold, got %v", dataOf(t, resp))
	}
	domain, ok := fr["domain"].(map[string]any)
	if !ok || domain["verifyingContract"] != "0xfwd" {
		t.Fatalf("scaffold must carry the forwarder's own domain, got %v", fr["domain"])
	}
	if fr["to"] != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("forward request must target the swap contract, got %v", fr["to"])
	}
}

// A scaffold failure must degrade to self-pay, not fail the prepare: the user
// can still pay their own gas.
func TestMCGPSwapHandler_PreparePay_ScaffoldFailureStillReturnsSelfPayTx(t *testing.T) {
	rr := newRelayRig(t, true)
	rr.relay.scaffoldErr = context.DeadlineExceeded

	code, resp := rr.post(t, "/pay/prepare", map[string]any{"username": "payee", "mcgpAmount": "1000"}, nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	d := dataOf(t, resp)
	if _, present := d["forwardRequest"]; present {
		t.Fatal("a failed scaffold must not be returned")
	}
	if d["tx"] == nil {
		t.Fatal("the self-pay tx must still be present")
	}
}

func TestMCGPSwapHandler_RelayStatus_ReportsWhetherRelayingIsAvailable(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		rr := newRelayRig(t, enabled)
		req := httptest.NewRequest(http.MethodGet, "/relay/status", nil)
		w := httptest.NewRecorder()
		rr.router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status must always answer 200, got %d", w.Code)
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if got := dataOf(t, out)["relayerEnabled"]; got != enabled {
			t.Fatalf("expected relayerEnabled=%v, got %v", enabled, got)
		}
	}
}

// Disabled must be a 200 carrying a flag, not an error: the app has to be able
// to tell "relaying is off" apart from "the request failed". Falling back on an
// ambiguous outcome is the double-spend.
func TestMCGPSwapHandler_SubmitRelay_DisabledIsASuccessfulFlagNotAnError(t *testing.T) {
	rr := newRelayRig(t, false)
	code, resp := rr.post(t, "/relay", map[string]any{"from": rr.payer.WalletAddress}, nil)

	if code != http.StatusOK {
		t.Fatalf("expected 200 with a flag, got %d", code)
	}
	if dataOf(t, resp)["relayerEnabled"] != false {
		t.Fatalf("expected relayerEnabled=false, got %v", resp)
	}
	if rr.relay.relayCalls != 0 {
		t.Fatal("a disabled relayer must not be invoked")
	}
}

func TestMCGPSwapHandler_SubmitRelay_PassesTheAuthenticatedWallet(t *testing.T) {
	rr := newRelayRig(t, true)
	code, resp := rr.post(t, "/relay", map[string]any{
		"from": rr.payer.WalletAddress, "to": "0x1111111111111111111111111111111111111111",
		"value": "0", "gas": 200000, "deadline": 1234567890,
		"data": "0xaabbcc", "signature": "0xdeadbeef",
	}, nil)

	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", code, resp)
	}
	// The service binds `from` against this, so the handler must pass the SESSION
	// wallet rather than anything from the request body.
	if rr.relay.lastWallet != rr.payer.WalletAddress {
		t.Fatalf("expected the authenticated wallet, got %q", rr.relay.lastWallet)
	}
	if dataOf(t, resp)["txHash"] != "0xrelayed" {
		t.Fatalf("expected the relayed tx hash, got %v", dataOf(t, resp))
	}
}

func TestMCGPSwapHandler_SubmitRelay_MapsErrorsToStatusCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"rate limited", services.ErrRateLimited, http.StatusTooManyRequests},
		{"sender mismatch", services.ErrSenderMismatch, http.StatusForbidden},
		{"underfunded is retryable", services.ErrRelayerUnderfunded, http.StatusServiceUnavailable},
		{"simulation unavailable is retryable", services.ErrSimulationUnavailable, http.StatusServiceUnavailable},
		{"daily cap is retryable", services.ErrDailyCapExceeded, http.StatusServiceUnavailable},
		{"would revert is terminal", services.ErrSimulationReverted, http.StatusBadRequest},
		{"selector not allowed is terminal", services.ErrSelectorNotAllowed, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := newRelayRig(t, true)
			rr.relay.relayErr = tc.err
			code, _ := rr.post(t, "/relay", map[string]any{
				"from": rr.payer.WalletAddress, "to": "0x1111111111111111111111111111111111111111",
				"value": "0", "gas": 200000, "deadline": 1234567890,
				"data": "0xaabbcc", "signature": "0xdeadbeef",
			}, nil)
			if code != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, code)
			}
		})
	}
}

func TestMCGPSwapHandler_SubmitRelay_RejectsMalformedHex(t *testing.T) {
	rr := newRelayRig(t, true)
	for _, body := range []map[string]any{
		{"from": rr.payer.WalletAddress, "data": "zzzz", "signature": "0xdead"},
		{"from": rr.payer.WalletAddress, "data": "0xaabb", "signature": "nothex"},
	} {
		code, _ := rr.post(t, "/relay", body, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("expected 400 for malformed hex, got %d", code)
		}
	}
	if rr.relay.relayCalls != 0 {
		t.Fatal("malformed requests must not reach the relayer")
	}
}
