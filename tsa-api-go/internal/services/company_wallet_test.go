package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// newTestCompanyWallet is the company wallet on the treasury test rig's "base"
// lane, offering USDC (6 dp) with usdcUnits in stock.
//
// Its Treasury carries CompanyWalletCaps() — production's caps, not the
// conversion treasury's test caps — so a test that sends above $200 sees
// exactly what production would (review C2).
func newTestCompanyWallet(t *testing.T, usdcUnits int64) (*CompanyWallet, *fakeSponsorNative, *fakeRelaySigner) {
	t.Helper()
	rig, _ := newSponsorRig("base", "USDC")
	tr := NewTreasury(map[string]*TreasuryLane{"base": {SponsorLane: rig.lane, ChainID: 8453}}, CompanyWalletCaps(),
		func() time.Time { return sponsorTestNow })
	native, signer := rig.native, rig.signer
	native.tokenBalances = map[string]*big.Int{strings.ToLower(sponsorTestUSDC): big.NewInt(usdcUnits)}
	w := NewCompanyWallet(CompanyWalletConfig{Treasury: tr, Decimals: map[string]map[string]int{"base": {"USDC": 6}}})
	if w == nil {
		t.Fatal("no company wallet")
	}
	return w, native, signer
}

func TestCompanyWallet_OffersOnlyTokensWithKnownDecimals(t *testing.T) {
	w, _, _ := newTestCompanyWallet(t, 1)
	if got := w.Chains(); len(got) != 1 || got[0] != "base" {
		t.Fatalf("chains = %v", got)
	}
	if !w.Offers("BASE", "usdc") || w.Offers("base", "USDT") || w.Offers("polygon", "USDC") {
		t.Fatal("offers")
	}
	if w.Address() != sponsorTestSigner {
		t.Fatalf("address = %s", w.Address())
	}
	if NewCompanyWallet(CompanyWalletConfig{Treasury: w.Treasury()}) != nil {
		t.Fatal("a wallet with no readable token must be nil")
	}
}

// Review C2: the company wallet's lane never refuses on value — the $200
// maximum lives in pricing, so a waived order can be sent.
func TestCompanyWalletCaps_AreOff(t *testing.T) {
	w, _, _ := newTestCompanyWallet(t, 1)
	if err := w.Treasury().CheckCaps("base", 1_000_000, 1_000_000); err != nil {
		t.Fatalf("caps refused: %v", err)
	}
	if c := CompanyWalletCaps(); c.MaxInFlightUSDPerOrder != 0 || c.MaxInFlightUSDPerChain != 0 {
		t.Fatalf("caps = %+v", c)
	}
}

// Review I1: a chain whose RPC fails the receipt probe keeps its lane (open
// orders still deliver) but is not quotable until a re-probe passes.
func TestCompanyWallet_ReceiptProbeGatesQuotesNotDelivery(t *testing.T) {
	w0, _, _ := newTestCompanyWallet(t, 1)
	rpc := &fakeRawRPC{blocks: map[string]map[string]any{"latest": {"number": "0x10", "transactions": []string{"0xaa"}}}, receipt: "null"}
	w := NewCompanyWallet(CompanyWalletConfig{
		Treasury: w0.Treasury(), Decimals: map[string]map[string]int{"base": {"USDC": 6}},
		ReceiptRPCs: map[string]RawRPCCaller{"base": rpc}, ReceiptsOK: map[string]bool{"base": false},
	})
	if w.ReceiptsReadable("base") || len(w.QuotableChains()) != 0 {
		t.Fatalf("quotable = %v, want none while receipts are unreadable", w.QuotableChains())
	}
	if w.Lane("base") == nil || len(w.Chains()) != 1 || !w.Offers("base", "USDC") {
		t.Fatal("the lane must stay for delivery")
	}
	logs := captureLog(t)
	w.ReprobeReceipts(context.Background())
	if w.ReceiptsReadable("base") {
		t.Fatal("still unreadable, reopened anyway")
	}
	rpc.receipt = `{"status":"0x1"}`
	w.ReprobeReceipts(context.Background())
	if !w.ReceiptsReadable("base") || len(w.QuotableChains()) != 1 {
		t.Fatal("a passing re-probe must reopen the chain")
	}
	if !strings.Contains(logs.String(), "ramp_company_wallet_receipts_restored chain=base") {
		t.Fatalf("log = %q", logs.String())
	}
	const alert = "ALERT ramp_company_wallet_receipts_unreadable=1 chain=base"
	alertsBefore := strings.Count(logs.String(), alert)
	rpc.receipt = "null"
	w.ReprobeReceipts(context.Background())
	if w.ReceiptsReadable("base") || strings.Count(logs.String(), alert) != alertsBefore+1 {
		t.Fatalf("a failing re-probe must close it again and alert once: %q", logs.String())
	}
	// No ReceiptsOK map: every chain is readable (tests, and a wallet built
	// before any probe ran).
	if !w0.ReceiptsReadable("base") {
		t.Fatal("default must be readable")
	}
}

func TestCompanyWalletTokenDecimals_DropsAnUnreadableToken(t *testing.T) {
	tr, lane, native, _ := newTestTreasuryWithFakes(t)
	_ = tr
	native.decimalsByToken = map[string]int{strings.ToLower(sponsorTestBscUSDT): 18}
	got, dropped := CompanyWalletTokenDecimals(lane)
	if got["USDC"] != 6 || got["USDT"] != 18 || len(dropped) != 0 {
		t.Fatalf("decimals = %v dropped = %v", got, dropped)
	}
	native.decimalsErr = errors.New("rpc down")
	got, dropped = CompanyWalletTokenDecimals(lane)
	if len(got) != 0 || len(dropped) != 2 {
		t.Fatalf("decimals = %v dropped = %v, want both dropped", got, dropped)
	}
}

// D52: the public path reads through a 30s cache; open/claim/send read fresh.
func TestCompanyWalletBalance_CachedForPublicPathsFreshForMoney(t *testing.T) {
	w, native, _ := newTestCompanyWallet(t, 50_000_000)
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return clock }
	ctx := context.Background()
	if b, err := w.Balance(ctx, "base", "USDC", false); err != nil || b.Int64() != 50_000_000 {
		t.Fatalf("first read: %v %v", b, err)
	}
	native.tokenBalances[strings.ToLower(sponsorTestUSDC)] = big.NewInt(10_000_000) // an operator withdrew
	if b, _ := w.Balance(ctx, "base", "USDC", false); b.Int64() != 50_000_000 {
		t.Fatalf("cached read = %v, want the 30s reading", b)
	}
	if b, _ := w.Balance(ctx, "base", "USDC", true); b.Int64() != 10_000_000 {
		t.Fatalf("fresh read = %v, want the chain's", b)
	}
	clock = clock.Add(31 * time.Second)
	native.tokenBalanceErr = errors.New("rpc 403")
	if _, err := w.Balance(ctx, "base", "USDC", false); !errors.Is(err, ErrTreasuryStockUnreadable) {
		t.Fatalf("failed read: %v", err)
	}
	native.tokenBalanceErr = nil
	clock = clock.Add(2 * time.Second)
	if _, err := w.Balance(ctx, "base", "USDC", false); err == nil {
		t.Fatal("a failure must be remembered for 5s and read as closed")
	}
	clock = clock.Add(4 * time.Second)
	if b, err := w.Balance(ctx, "base", "USDC", false); err != nil || b.Int64() != 10_000_000 {
		t.Fatalf("after the failure window: %v %v", b, err)
	}
}

// D54: 6-dp amounts scale exactly to a token's own decimals, both ways.
func TestCompanyWallet_ScalesExactly(t *testing.T) {
	tr, _, native, _ := newTestTreasuryWithFakes(t)
	native.tokenBalances = map[string]*big.Int{strings.ToLower(sponsorTestBscUSDT): bigFromDecimalString("5000000000000000000")}
	w := NewCompanyWallet(CompanyWalletConfig{Treasury: tr, Decimals: map[string]map[string]int{"base": {"USDC": 2, "USDT": 18}}})
	if b, err := w.Balance(context.Background(), "base", "USDT", true); err != nil || b.Int64() != 5_000_000 {
		t.Fatalf("18-dp balance = %v %v, want 5.000000 in 6-dp units", b, err)
	}
	if u, err := w.ToTokenUnits("base", "USDT", big.NewInt(1_500_000)); err != nil || u.String() != "1500000000000000000" {
		t.Fatalf("to 18 dp = %v %v", u, err)
	}
	if u, err := w.ToTokenUnits("base", "USDC", big.NewInt(1_230_000)); err != nil || u.Int64() != 123 {
		t.Fatalf("to 2 dp = %v %v", u, err)
	}
	if _, err := w.ToTokenUnits("base", "USDC", big.NewInt(1_234_567)); err == nil {
		t.Fatal("a non-divisible amount must be refused, never rounded")
	}
}

func bigFromDecimalString(s string) *big.Int {
	n, _ := new(big.Int).SetString(s, 10)
	return n
}

type fakeRawRPC struct {
	blocks     map[string]map[string]any
	receipt    string
	blockErr   error
	receiptErr error
}

func (f *fakeRawRPC) CallContext(_ context.Context, result any, method string, args ...any) error {
	switch method {
	case "eth_getBlockByNumber":
		if f.blockErr != nil {
			return f.blockErr
		}
		raw, _ := json.Marshal(f.blocks[args[0].(string)])
		return json.Unmarshal(raw, result)
	case "eth_getTransactionReceipt":
		if f.receiptErr != nil {
			return f.receiptErr
		}
		return json.Unmarshal([]byte(f.receipt), result)
	}
	return errors.New("unexpected " + method)
}

// 2.3 #8: the 2026-10-01 incident — block reads worked, every receipt read
// was a 403. Only a receipt read catches it.
func TestProbeReceipts(t *testing.T) {
	ctx := context.Background()
	withTx := map[string]map[string]any{"latest": {"number": "0x10", "transactions": []string{"0xaa", "0xbb"}}}
	if err := ProbeReceipts(ctx, &fakeRawRPC{blocks: withTx, receipt: `{"status":"0x1"}`}); err != nil {
		t.Fatalf("healthy: %v", err)
	}
	if err := ProbeReceipts(ctx, &fakeRawRPC{blocks: withTx, receiptErr: errors.New("403 Forbidden")}); !errors.Is(err, ErrReceiptsUnreadable) {
		t.Fatalf("403: %v", err)
	}
	if err := ProbeReceipts(ctx, &fakeRawRPC{blocks: withTx, receipt: `null`}); !errors.Is(err, ErrReceiptsUnreadable) {
		t.Fatalf("null receipt for a mined tx: %v", err)
	}
	empty := map[string]map[string]any{
		"latest": {"number": "0x10", "transactions": []string{}},
		"0xf":    {"number": "0xf", "transactions": []string{"0xcc"}},
	}
	if err := ProbeReceipts(ctx, &fakeRawRPC{blocks: empty, receipt: `{"status":"0x1"}`}); err != nil {
		t.Fatalf("walks back past an empty block: %v", err)
	}
	if err := ProbeReceipts(ctx, &fakeRawRPC{blockErr: errors.New("dial tcp")}); !errors.Is(err, ErrReceiptsUnreadable) {
		t.Fatalf("no block: %v", err)
	}
}
