package services

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// makeTestSvc returns a P2PEscrowService with no chain client (env=dev) so
// Prepare* methods take the fallback path that emits partial-tx JSON. That
// lets us verify call data without standing up an RPC mock.
func makeTestSvc(t *testing.T) *P2PEscrowService {
	t.Helper()
	cfg := &config.Config{
		Env:              "development",
		P2PEscrowAddress: "0xb5B073BF578fE24311ED50e540D9d632f1679261",
	}
	return NewP2PEscrowService(nil, cfg)
}

func TestHashID_DeterministicAndUnique(t *testing.T) {
	id1 := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	id2 := uuid.MustParse("11111111-2222-3333-4444-555555555556")

	if HashID(id1) != HashID(id1) {
		t.Fatalf("HashID not deterministic")
	}
	if HashID(id1) == HashID(id2) {
		t.Fatalf("HashID collision on differing UUIDs")
	}

	want := crypto.Keccak256Hash(id1[:])
	if HashID(id1) != want {
		t.Fatalf("HashID != keccak256(id.bytes)")
	}
}

func TestHashIDHex_FormatAndLength(t *testing.T) {
	id := uuid.New()
	h := HashIDHex(id)
	if len(h) != 66 {
		t.Fatalf("HashIDHex length = %d, want 66 (0x + 64 hex)", len(h))
	}
	if h[:2] != "0x" {
		t.Fatalf("HashIDHex must start with 0x, got %q", h[:2])
	}
}

// Legacy Prepare* methods are retained on the adapter for source-level
// compatibility while the trade service is migrated (Phase 2B). They must
// fail loudly so any remaining call site is caught at runtime.
func TestPrepareCreateAd_NowReturnsMigrationError(t *testing.T) {
	svc := makeTestSvc(t)
	_, err := svc.PrepareCreateAd(HashID(uuid.New()), "0xa", "0xb", big.NewInt(1))
	if !errors.Is(err, ErrPhase2Migration) {
		t.Fatalf("expected ErrPhase2Migration, got %v", err)
	}
}

func TestPrepareCloseAd_NowReturnsMigrationError(t *testing.T) {
	svc := makeTestSvc(t)
	_, err := svc.PrepareCloseAd(HashID(uuid.New()), "0xa")
	if !errors.Is(err, ErrPhase2Migration) {
		t.Fatalf("expected ErrPhase2Migration, got %v", err)
	}
}

func TestPrepareOpenTrade_LegacySignature_NowReturnsMigrationError(t *testing.T) {
	svc := makeTestSvc(t)
	_, err := svc.PrepareOpenTrade(HashID(uuid.New()), HashID(uuid.New()), "0xd00d", big.NewInt(1))
	if !errors.Is(err, ErrPhase2Migration) {
		t.Fatalf("expected ErrPhase2Migration, got %v", err)
	}
}

// PrepareOpenTradeWithVoucher is the pool-model replacement. Verify it packs
// the (tradeId, voucher tuple, sig, amount) selector correctly.
func TestPrepareOpenTradeWithVoucher_PacksCorrectSelector(t *testing.T) {
	svc := makeTestSvc(t)
	tradeID := HashID(uuid.New())
	voucher := AdVoucher{
		Seller:      common.HexToAddress("0x000000000000000000000000000000000000beef"),
		Token:       common.HexToAddress("0x000000000000000000000000000000000000feed"),
		MaxAmount:   big.NewInt(1000),
		MinPerTrade: big.NewInt(0),
		MaxPerTrade: big.NewInt(0),
		Deadline:    big.NewInt(1900000000),
		Nonce:       HashID(uuid.New()),
	}
	sig := make([]byte, 65) // junk sig — on-chain ECDSA.recover will reject; selector is what we're testing
	amount := big.NewInt(500)
	buyer := "0x000000000000000000000000000000000000d00d"

	raw, err := svc.PrepareOpenTradeWithVoucher(tradeID, voucher, sig, buyer, amount)
	if err != nil {
		t.Fatalf("PrepareOpenTradeWithVoucher: %v", err)
	}
	data := mustDecodeHex(t, decodeUnsignedTx(t, raw).Data)
	wantSelector := selectorFor(t, "openTrade(bytes32,(address,address,uint256,uint256,uint256,uint256,bytes32),bytes,uint256)")
	if !bytesEqual(data[:4], wantSelector) {
		t.Fatalf("openTrade(voucher) selector mismatch: want %x got %x", wantSelector, data[:4])
	}
	// tradeId is the next 32 bytes after the selector.
	if !bytesEqual(data[4:36], tradeID[:]) {
		t.Fatalf("tradeId not in first arg slot")
	}
}

// PrepareOpenTradeWithDeposit (v2 buy-ad / JIT path): pin the selector + that
// the tradeId lands in slot 0 and the merchant address is the 4th arg.
func TestPrepareOpenTradeWithDeposit_PacksCorrectSelector(t *testing.T) {
	svc := makeTestSvc(t)
	tradeID := HashID(uuid.New())
	token := "0x000000000000000000000000000000000000feed"
	merchant := "0x000000000000000000000000000000000000d00d"
	from := "0x000000000000000000000000000000000000beef"
	amount := big.NewInt(500_000)

	raw, err := svc.PrepareOpenTradeWithDeposit(tradeID, token, amount, merchant, from)
	if err != nil {
		t.Fatalf("PrepareOpenTradeWithDeposit: %v", err)
	}
	data := mustDecodeHex(t, decodeUnsignedTx(t, raw).Data)
	want := selectorFor(t, "openTradeWithDeposit(bytes32,address,uint256,address)")
	if !bytesEqual(data[:4], want) {
		t.Fatalf("openTradeWithDeposit selector mismatch: want %x got %x", want, data[:4])
	}
	if !bytesEqual(data[4:36], tradeID[:]) {
		t.Fatalf("tradeId not in first arg slot")
	}
	// merchant is the 4th arg (slot index 3) — address right-aligned in 32 bytes.
	merchantSlot := data[4+3*32 : 4+4*32]
	if !bytesEqual(merchantSlot[12:], common.HexToAddress(merchant).Bytes()) {
		t.Fatalf("merchant address not in 4th arg slot: %x", merchantSlot)
	}
}

// PrepareDeposit / PrepareWithdraw: verify selector for the new pool flow.
func TestPrepareDeposit_PacksCorrectSelector(t *testing.T) {
	svc := makeTestSvc(t)
	raw, err := svc.PrepareDeposit("0xbeef", "0xfeed", big.NewInt(1_000_000))
	if err != nil {
		t.Fatalf("PrepareDeposit: %v", err)
	}
	data := mustDecodeHex(t, decodeUnsignedTx(t, raw).Data)
	if !bytesEqual(data[:4], selectorFor(t, "deposit(address,uint256)")) {
		t.Fatalf("deposit selector mismatch")
	}
}

func TestPrepareWithdraw_PacksCorrectSelector(t *testing.T) {
	svc := makeTestSvc(t)
	raw, err := svc.PrepareWithdraw("0xbeef", "0xfeed", big.NewInt(1_000_000))
	if err != nil {
		t.Fatalf("PrepareWithdraw: %v", err)
	}
	data := mustDecodeHex(t, decodeUnsignedTx(t, raw).Data)
	if !bytesEqual(data[:4], selectorFor(t, "withdraw(address,uint256)")) {
		t.Fatalf("withdraw selector mismatch")
	}
}

func TestPrepareAdminResolve_EncodesBoolArg(t *testing.T) {
	svc := makeTestSvc(t)
	tradeID := HashID(uuid.New())

	for _, refund := range []bool{true, false} {
		raw, err := svc.PrepareAdminResolve(tradeID, refund, "0x000000000000000000000000000000000000aaaa")
		if err != nil {
			t.Fatalf("refund=%v: %v", refund, err)
		}
		data := mustDecodeHex(t, decodeUnsignedTx(t, raw).Data)
		// Bool is encoded in the last byte of its 32-byte slot.
		got := data[len(data)-1]
		want := byte(0)
		if refund {
			want = 1
		}
		if got != want {
			t.Fatalf("refund=%v: encoded bool byte want=%d got=%d", refund, want, got)
		}
	}
}

func TestPrepare_RequiresContractAddress(t *testing.T) {
	cfg := &config.Config{Env: "development", P2PEscrowAddress: ""}
	svc := NewP2PEscrowService(nil, cfg)
	// PrepareMarkPaid is the simplest still-functional Prepare* — exercise
	// the contract-address guard via it.
	_, err := svc.PrepareMarkPaid(HashID(uuid.New()), "0xbeef")
	if err == nil {
		t.Fatalf("expected error when P2PEscrowAddress empty")
	}
}

// TestBuildUnsignedTx_NonDevEnvFailsClosed pins the positive-allowlist policy:
// any env string other than the literal "development" must fail closed when
// the RPC client is nil. Catches the common typo `"dev"` and the case where
// Env is empty / unset.
func TestBuildUnsignedTx_NonDevEnvFailsClosed(t *testing.T) {
	for _, env := range []string{"", "dev", "staging", "production", "Development"} {
		cfg := &config.Config{
			Env:              env,
			P2PEscrowAddress: "0xb5B073BF578fE24311ED50e540D9d632f1679261",
		}
		svc := NewP2PEscrowService(nil, cfg) // nil client
		_, err := svc.PrepareMarkPaid(
			HashID(uuid.New()),
			"0x000000000000000000000000000000000000beef",
		)
		if err == nil {
			t.Errorf("env=%q: expected fail-closed when client is nil", env)
		}
	}
}

// ReadBalance / ReadVoucherFilled / ReadVoucherCancelled / ReadTrade /
// HashVoucher all need a live RPC client; verify the requireConfigured guard.
func TestReadBalance_RequiresClient(t *testing.T) {
	svc := makeTestSvc(t)
	_, err := svc.ReadBalance("0xbeef", "0xfeed")
	if err == nil {
		t.Fatalf("expected error: ReadBalance without RPC client")
	}
}

func TestABIEventsHaveExpectedTopicIDs(t *testing.T) {
	// Sanity check the events are parseable and their topic IDs match the
	// canonical keccak256(signature). If a signature drifts, on-chain events
	// won't decode — this test catches it at unit-test time.
	cases := map[string]string{
		"Deposited":        "Deposited(address,address,uint256)",
		"Withdrawn":        "Withdrawn(address,address,uint256)",
		"VoucherCancelled": "VoucherCancelled(bytes32,address)",
		"TradeOpened":      "TradeOpened(bytes32,bytes32,address,address,address,uint256)",
		"TradeOpenedWithDeposit": "TradeOpenedWithDeposit(bytes32,address,address,address,uint256)",
		"TradePaid":        "TradePaid(bytes32)",
		"TradeReleased":    "TradeReleased(bytes32,address,uint256,uint256)",
		"TradeAutoReleased": "TradeAutoReleased(bytes32,address,uint256,uint256)",
		"TradeCancelled":   "TradeCancelled(bytes32,uint256,bool)",
		"TradeDisputed":    "TradeDisputed(bytes32,address)",
		"TradeResolved":    "TradeResolved(bytes32,bool,uint256,uint256)",
	}
	for name, sig := range cases {
		ev, ok := parsedP2PEscrowABI.Events[name]
		if !ok {
			t.Fatalf("event %q missing from parsed ABI", name)
			continue
		}
		want := crypto.Keccak256Hash([]byte(sig))
		if ev.ID != want {
			t.Fatalf("event %q topic ID mismatch: want %s got %s", name, want, ev.ID)
		}
	}
}

// ─── helpers ───────────────────────────────────────────────────────────────

func decodeUnsignedTx(t *testing.T, raw []byte) blockchain.UnsignedTx {
	t.Helper()
	var tx blockchain.UnsignedTx
	if err := json.Unmarshal(raw, &tx); err != nil {
		t.Fatalf("decode unsigned tx: %v\nraw=%s", err, string(raw))
	}
	return tx
}

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	// Tolerate either "0x..." or bare hex.
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s = s[2:]
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex %q: %v", s, err)
	}
	return b
}

func selectorFor(t *testing.T, sig string) []byte {
	t.Helper()
	h := crypto.Keccak256([]byte(sig))
	return h[:4]
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ─── Gate-2 review additions ────────────────────────────────────────────────

// TestPrepare_SingleArgFns_AllSelectorsCorrect covers the Prepare* methods
// that pass only a tradeId. A typo in the ABI string ("releas") or a
// function-name rename in Solidity would slip past compilation; this test
// pins the wire format. Note: closeAd was removed in the pool-model upgrade
// — its replacement is cancelVoucher (covered separately).
func TestPrepare_SingleArgFns_AllSelectorsCorrect(t *testing.T) {
	svc := makeTestSvc(t)
	tradeID := HashID(uuid.New())
	caller := "0x000000000000000000000000000000000000face"

	cases := []struct {
		name    string
		sig     string
		prepare func() ([]byte, error)
	}{
		{"markPaid", "markPaid(bytes32)", func() ([]byte, error) { return svc.PrepareMarkPaid(tradeID, caller) }},
		{"release", "release(bytes32)", func() ([]byte, error) { return svc.PrepareRelease(tradeID, caller) }},
		{"adminRelease", "adminRelease(bytes32)", func() ([]byte, error) { return svc.PrepareAdminRelease(tradeID, caller) }},
		{"cancelByTimeout", "cancelByTimeout(bytes32)", func() ([]byte, error) { return svc.PrepareCancelByTimeout(tradeID, caller) }},
		{"raiseDispute", "raiseDispute(bytes32)", func() ([]byte, error) { return svc.PrepareRaiseDispute(tradeID, caller) }},
	}
	for _, c := range cases {
		raw, err := c.prepare()
		if err != nil {
			t.Errorf("%s prepare: %v", c.name, err)
			continue
		}
		data := mustDecodeHex(t, decodeUnsignedTx(t, raw).Data)
		want := selectorFor(t, c.sig)
		if !bytesEqual(data[:4], want) {
			t.Errorf("%s selector: want %x got %x", c.name, want, data[:4])
		}
		// tradeId is the next 32 bytes; confirm it landed in slot 0.
		if !bytesEqual(data[4:36], tradeID[:]) {
			t.Errorf("%s did not encode tradeId in slot 0", c.name)
		}
	}
}

// TestOnChainP2PTradeStatus_MatchesContractEnum pins the numeric values to
// the contract's Status enum order. The contract source is the authority;
// values are hand-curated here so an audit must touch both files in sync.
func TestOnChainP2PTradeStatus_MatchesContractEnum(t *testing.T) {
	// From smart-contracts/contracts/P2PEscrow.sol:
	//   enum Status { None, Pending, Paid, Released, Cancelled, Disputed, Resolved }
	cases := []struct {
		name string
		want OnChainP2PTradeStatus
		val  uint8
	}{
		{"None", OnChainP2PStatusNone, 0},
		{"Pending", OnChainP2PStatusPending, 1},
		{"Paid", OnChainP2PStatusPaid, 2},
		{"Released", OnChainP2PStatusReleased, 3},
		{"Cancelled", OnChainP2PStatusCancelled, 4},
		{"Disputed", OnChainP2PStatusDisputed, 5},
		{"Resolved", OnChainP2PStatusResolved, 6},
	}
	for _, c := range cases {
		if uint8(c.want) != c.val {
			t.Errorf("Status %s: const = %d, contract enum position = %d (drift)",
				c.name, uint8(c.want), c.val)
		}
	}
}

// TestP2PTradeFunding_Constants pins the Funding discriminator values to the
// contract's `enum Funding { PooledFromMaker, DepositedByTaker }` order. The
// contract source is the authority; a drift here would route buy-ad refunds
// down the pool-credit branch (double-credit) or vice-versa.
func TestP2PTradeFunding_Constants(t *testing.T) {
	// From smart-contracts/contracts/P2PEscrowV2.sol:
	//   enum Funding { PooledFromMaker, DepositedByTaker }
	cases := []struct {
		name string
		got  uint8
		want uint8
	}{
		{"PooledFromMaker", models.P2PTradeFundingPooled, 0},
		{"DepositedByTaker", models.P2PTradeFundingDepositedByTaker, 1},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("Funding %s: const = %d, contract enum position = %d (drift)",
				c.name, c.got, c.want)
		}
	}
}

func TestParseDepositedFromReceipt_HappyPath(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	seller := common.HexToAddress("0x000000000000000000000000000000000000beef")
	token := common.HexToAddress("0x000000000000000000000000000000000000feed")
	amount := big.NewInt(2_000_000)

	logEntry := mustBuildSimpleSellerTokenAmountLog(t, "Deposited", contractAddr, seller, token, amount)
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}
	ev, err := parseDepositedFromReceipt(receipt, contractAddr, seller, token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Seller != seller || ev.Token != token || ev.Amount.Cmp(amount) != 0 {
		t.Errorf("decoded fields mismatch: %+v", ev)
	}
}

func TestParseDepositedFromReceipt_IgnoresOtherContracts(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	imposter := common.HexToAddress("0x1111111111111111111111111111111111111111")
	seller := common.HexToAddress("0xbeef")
	token := common.HexToAddress("0xfeed")
	logEntry := mustBuildSimpleSellerTokenAmountLog(t, "Deposited", imposter, seller, token, big.NewInt(1))
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}
	if _, err := parseDepositedFromReceipt(receipt, contractAddr, seller, token); err == nil {
		t.Fatalf("expected error: log from foreign contract must be ignored")
	}
}

func TestParseVoucherCancelledFromReceipt_HappyPath(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	voucherHash := HashID(uuid.New())
	seller := common.HexToAddress("0xbeef")
	ev := parsedP2PEscrowABI.Events["VoucherCancelled"]
	logEntry := &types.Log{
		Address: contractAddr,
		Topics: []common.Hash{
			ev.ID,
			voucherHash,
			common.BytesToHash(seller.Bytes()),
		},
	}
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}
	got, err := parseVoucherCancelledFromReceipt(receipt, contractAddr, voucherHash)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.VoucherHash != voucherHash || got.Seller != seller {
		t.Errorf("decoded fields mismatch: %+v", got)
	}
}

func TestParseTradeReleasedFromReceipt_DecodesPaymentSplit(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	tradeID := HashID(uuid.New())
	buyer := common.HexToAddress("0x000000000000000000000000000000000000d00d")
	amountToBuyer := big.NewInt(199_500_000) // 199.5 USDT
	feeToSystem := big.NewInt(500_000)       // 0.5 USDT

	logEntry := mustBuildTradeReleasedLog(t, contractAddr, tradeID, buyer, amountToBuyer, feeToSystem)
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}

	ev, err := parseTradeReleasedFromReceipt(receipt, contractAddr, tradeID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Buyer != buyer {
		t.Errorf("buyer mismatch")
	}
	if ev.AmountToBuyer.Cmp(amountToBuyer) != 0 {
		t.Errorf("amountToBuyer: want %s got %s", amountToBuyer, ev.AmountToBuyer)
	}
	if ev.FeeToSystem.Cmp(feeToSystem) != 0 {
		t.Errorf("feeToSystem: want %s got %s", feeToSystem, ev.FeeToSystem)
	}
}

func TestParseTradeOpenedFromReceipt_HappyPath(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	tradeID := HashID(uuid.New())
	voucherHash := HashID(uuid.New())
	seller := common.HexToAddress("0x000000000000000000000000000000000000beef")
	token := common.HexToAddress("0x000000000000000000000000000000000000feed")
	buyer := common.HexToAddress("0x000000000000000000000000000000000000d00d")
	amount := big.NewInt(500_000_000)

	logEntry := mustBuildTradeOpenedLog(t, contractAddr, tradeID, voucherHash, buyer, seller, token, amount)
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}

	ev, err := parseTradeOpenedFromReceipt(receipt, contractAddr, tradeID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.VoucherHash != voucherHash || ev.Buyer != buyer ||
		ev.Seller != seller || ev.Token != token || ev.Amount.Cmp(amount) != 0 {
		t.Errorf("decoded fields mismatch: %+v", ev)
	}
}

// TestParseTradeOpenedWithDepositFromReceipt_HappyPath pins the v2 buy-ad /
// JIT-funded open event decode: 3 indexed topics (tradeId, seller=user,
// buyer=merchant) + 2 non-indexed (token, amount).
func TestParseTradeOpenedWithDepositFromReceipt_HappyPath(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	tradeID := HashID(uuid.New())
	seller := common.HexToAddress("0x000000000000000000000000000000000000beef") // depositing user
	buyer := common.HexToAddress("0x000000000000000000000000000000000000d00d")  // merchant
	token := common.HexToAddress("0x000000000000000000000000000000000000feed")
	amount := big.NewInt(250_000_000)

	logEntry := mustBuildTradeOpenedWithDepositLog(t, contractAddr, tradeID, seller, buyer, token, amount)
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}

	ev, err := parseTradeOpenedWithDepositFromReceipt(receipt, contractAddr, tradeID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Seller != seller || ev.Buyer != buyer || ev.Token != token || ev.Amount.Cmp(amount) != 0 {
		t.Errorf("decoded fields mismatch: %+v", ev)
	}
}

// TestParseTradeCancelledFromReceipt_DecodesAmountAndPoolFlag pins the v2
// TradeCancelled layout: tradeId indexed + (amount, returnedToPool) non-indexed.
func TestParseTradeCancelledFromReceipt_DecodesAmountAndPoolFlag(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	tradeID := HashID(uuid.New())
	amount := big.NewInt(100_000_000)

	for _, returnedToPool := range []bool{true, false} {
		logEntry := mustBuildTradeCancelledLog(t, contractAddr, tradeID, amount, returnedToPool)
		receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}
		ev, err := parseTradeCancelledFromReceipt(receipt, contractAddr, tradeID)
		if err != nil {
			t.Fatalf("returnedToPool=%v: parse: %v", returnedToPool, err)
		}
		if ev.Amount.Cmp(amount) != 0 {
			t.Errorf("returnedToPool=%v: amount = %s, want %s", returnedToPool, ev.Amount, amount)
		}
		if ev.ReturnedToPool != returnedToPool {
			t.Errorf("returnedToPool = %v, want %v", ev.ReturnedToPool, returnedToPool)
		}
	}
}

// TestParseTradeReleasedFromReceipt_AcceptsAutoReleased: a release that
// settled via the keeper-driven auto-release path emits TradeAutoReleased, not
// TradeReleased. The recognizer must still verify it (same field layout).
func TestParseTradeReleasedFromReceipt_AcceptsAutoReleased(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	tradeID := HashID(uuid.New())
	buyer := common.HexToAddress("0x000000000000000000000000000000000000d00d")
	amountToBuyer := big.NewInt(99)
	feeToSystem := big.NewInt(1)

	ev := parsedP2PEscrowABI.Events["TradeAutoReleased"]
	data, err := ev.Inputs.NonIndexed().Pack(amountToBuyer, feeToSystem)
	if err != nil {
		t.Fatalf("pack non-indexed: %v", err)
	}
	logEntry := &types.Log{
		Address: contractAddr,
		Topics: []common.Hash{
			ev.ID,
			tradeID,
			common.BytesToHash(buyer.Bytes()),
		},
		Data: data,
	}
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}
	got, err := parseTradeReleasedFromReceipt(receipt, contractAddr, tradeID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Buyer != buyer || got.AmountToBuyer.Cmp(amountToBuyer) != 0 || got.FeeToSystem.Cmp(feeToSystem) != 0 {
		t.Errorf("decoded auto-release fields mismatch: %+v", got)
	}
}

// TestReadTrade_Decodes10FieldLayout guards the v2 getTrade field reorder. It
// ABI-packs a 10-field output and confirms each field lands in the right slot
// of OnChainP2PTrade — in particular voucherHash at index 9 (was 6 in v1),
// funding at 6, feeBps(uint16) at 7, autoReleaseAt at 8.
func TestReadTrade_Decodes10FieldLayout(t *testing.T) {
	method, ok := parsedP2PEscrowABI.Methods["getTrade"]
	if !ok {
		t.Fatalf("getTrade not in parsed ABI")
	}
	seller := common.HexToAddress("0x000000000000000000000000000000000000beef")
	buyer := common.HexToAddress("0x000000000000000000000000000000000000d00d")
	token := common.HexToAddress("0x000000000000000000000000000000000000feed")
	amount := big.NewInt(123_456)
	createdAt := big.NewInt(1_700_000_000)
	status := uint8(OnChainP2PStatusPaid)
	funding := uint8(1) // DepositedByTaker
	feeBps := uint16(50)
	autoReleaseAt := big.NewInt(1_700_000_900)
	voucherHash := HashID(uuid.New())

	packed, err := method.Outputs.Pack(seller, buyer, token, amount, createdAt, status, funding, feeBps, autoReleaseAt, voucherHash)
	if err != nil {
		t.Fatalf("pack outputs: %v", err)
	}
	vals, err := method.Outputs.Unpack(packed)
	if err != nil || len(vals) != 10 {
		t.Fatalf("unpack: %v len=%d", err, len(vals))
	}
	// Decode in the same order ReadTrade does and assert field placement.
	tr := &OnChainP2PTrade{
		Seller:        vals[0].(common.Address),
		Buyer:         vals[1].(common.Address),
		Token:         vals[2].(common.Address),
		Amount:        vals[3].(*big.Int),
		CreatedAt:     vals[4].(*big.Int),
		Status:        OnChainP2PTradeStatus(vals[5].(uint8)),
		Funding:       vals[6].(uint8),
		FeeBps:        vals[7].(uint16),
		AutoReleaseAt: vals[8].(*big.Int),
		VoucherHash:   vals[9].([32]byte),
	}
	if tr.Seller != seller || tr.Buyer != buyer || tr.Token != token {
		t.Errorf("address fields mis-decoded: %+v", tr)
	}
	if tr.Amount.Cmp(amount) != 0 || tr.CreatedAt.Cmp(createdAt) != 0 {
		t.Errorf("amount/createdAt mis-decoded: %+v", tr)
	}
	if uint8(tr.Status) != status {
		t.Errorf("status = %d, want %d", tr.Status, status)
	}
	if tr.Funding != funding {
		t.Errorf("funding = %d, want %d (index 6)", tr.Funding, funding)
	}
	if tr.FeeBps != feeBps {
		t.Errorf("feeBps = %d, want %d (index 7, uint16)", tr.FeeBps, feeBps)
	}
	if tr.AutoReleaseAt.Cmp(autoReleaseAt) != 0 {
		t.Errorf("autoReleaseAt = %s, want %s (index 8)", tr.AutoReleaseAt, autoReleaseAt)
	}
	if tr.VoucherHash != voucherHash {
		t.Errorf("voucherHash mis-decoded — index drift (want slot 9): got %x", tr.VoucherHash)
	}
}

// TestTradesGetter_DecodesV3SnapshotLayout guards the V3 public trades(bytes32)
// getter decode used by ReadTradePlatformFeeBps. The struct getter returns the
// fields in DECLARATION order — seller, buyer, token, amount, createdAt,
// autoReleaseAt, status, funding, feeBps, platformFeeBpsSnapshot,
// gasFeeBpsSnapshot, voucherHash — which DIFFERS from getTrade's order. A wrong
// index would mis-read the platform snapshot (index 9). 12 fields total.
func TestTradesGetter_DecodesV3SnapshotLayout(t *testing.T) {
	method, ok := parsedP2PEscrowABI.Methods["trades"]
	if !ok {
		t.Fatalf("trades not in parsed ABI")
	}
	seller := common.HexToAddress("0x000000000000000000000000000000000000beef")
	buyer := common.HexToAddress("0x000000000000000000000000000000000000d00d")
	token := common.HexToAddress("0x000000000000000000000000000000000000feed")
	amount := big.NewInt(123_456)         // uint96
	createdAt := big.NewInt(1_700_000_000) // uint40
	autoReleaseAt := big.NewInt(1_700_000_900)
	status := uint8(OnChainP2PStatusReleased)
	funding := uint8(1)
	feeBps := uint16(0)               // V3 sets feeBps=0 for new trades
	platformBps := uint16(100)        // 1% platform snapshot — what we read
	gasBps := uint16(50)              // 0.5% gas snapshot
	voucherHash := HashID(uuid.New())

	packed, err := method.Outputs.Pack(seller, buyer, token, amount, createdAt, autoReleaseAt, status, funding, feeBps, platformBps, gasBps, voucherHash)
	if err != nil {
		t.Fatalf("pack trades outputs: %v", err)
	}
	vals, err := method.Outputs.Unpack(packed)
	if err != nil || len(vals) != 12 {
		t.Fatalf("unpack trades: %v len=%d", err, len(vals))
	}
	// Index 9 is the platform snapshot — the field ReadTradePlatformFeeBps reads.
	got, ok := vals[9].(uint16)
	if !ok {
		t.Fatalf("platform snapshot not uint16: %T", vals[9])
	}
	if got != platformBps {
		t.Errorf("platformFeeBpsSnapshot = %d, want %d (index 9)", got, platformBps)
	}
	// Sanity: index 8 is the (now-zero) legacy feeBps, index 10 the gas snapshot.
	if vals[8].(uint16) != feeBps {
		t.Errorf("feeBps = %d, want %d (index 8)", vals[8].(uint16), feeBps)
	}
	if vals[10].(uint16) != gasBps {
		t.Errorf("gasFeeBpsSnapshot = %d, want %d (index 10)", vals[10].(uint16), gasBps)
	}
}

// TestBalanceOf_OutputUnpackOrder pins the (available, locked) tuple ordering
// of the new view. A wrong-order ABI string would swap them and silently
// invert the seller's withdrawable amount.
func TestBalanceOf_OutputUnpackOrder(t *testing.T) {
	wantAvailable := big.NewInt(700)
	wantLocked := big.NewInt(300)

	method, ok := parsedP2PEscrowABI.Methods["balanceOf"]
	if !ok {
		t.Fatalf("balanceOf not in parsed ABI")
	}
	packed, err := method.Outputs.Pack(wantAvailable, wantLocked)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	vals, err := method.Outputs.Unpack(packed)
	if err != nil || len(vals) != 2 {
		t.Fatalf("unpack: %v vals=%d", err, len(vals))
	}
	if vals[0].(*big.Int).Cmp(wantAvailable) != 0 {
		t.Errorf("slot 0 not available")
	}
	if vals[1].(*big.Int).Cmp(wantLocked) != 0 {
		t.Errorf("slot 1 not locked")
	}
}

// ─── synthetic-log builders ─────────────────────────────────────────────────

// mustBuildSimpleSellerTokenAmountLog builds a Deposited / Withdrawn log
// (same shape: two indexed addresses + non-indexed amount).
func mustBuildSimpleSellerTokenAmountLog(t *testing.T, eventName string, contractAddr,
	seller, token common.Address, amount *big.Int) *types.Log {
	t.Helper()
	ev := parsedP2PEscrowABI.Events[eventName]
	data, err := ev.Inputs.NonIndexed().Pack(amount)
	if err != nil {
		t.Fatalf("pack non-indexed: %v", err)
	}
	return &types.Log{
		Address: contractAddr,
		Topics: []common.Hash{
			ev.ID,
			common.BytesToHash(seller.Bytes()),
			common.BytesToHash(token.Bytes()),
		},
		Data: data,
	}
}

func mustBuildTradeOpenedLog(t *testing.T, contractAddr common.Address,
	tradeID, voucherHash [32]byte, buyer, seller, token common.Address, amount *big.Int) *types.Log {
	t.Helper()
	ev := parsedP2PEscrowABI.Events["TradeOpened"]
	// Non-indexed: seller, token, amount (in declared order).
	data, err := ev.Inputs.NonIndexed().Pack(seller, token, amount)
	if err != nil {
		t.Fatalf("pack non-indexed: %v", err)
	}
	return &types.Log{
		Address: contractAddr,
		Topics: []common.Hash{
			ev.ID,
			tradeID,
			voucherHash,
			common.BytesToHash(buyer.Bytes()),
		},
		Data: data,
	}
}

func mustBuildTradeOpenedWithDepositLog(t *testing.T, contractAddr common.Address,
	tradeID [32]byte, seller, buyer, token common.Address, amount *big.Int) *types.Log {
	t.Helper()
	ev := parsedP2PEscrowABI.Events["TradeOpenedWithDeposit"]
	// Non-indexed: token, amount (in declared order).
	data, err := ev.Inputs.NonIndexed().Pack(token, amount)
	if err != nil {
		t.Fatalf("pack non-indexed: %v", err)
	}
	return &types.Log{
		Address: contractAddr,
		Topics: []common.Hash{
			ev.ID,
			tradeID,
			common.BytesToHash(seller.Bytes()),
			common.BytesToHash(buyer.Bytes()),
		},
		Data: data,
	}
}

func mustBuildTradeCancelledLog(t *testing.T, contractAddr common.Address,
	tradeID [32]byte, amount *big.Int, returnedToPool bool) *types.Log {
	t.Helper()
	ev := parsedP2PEscrowABI.Events["TradeCancelled"]
	data, err := ev.Inputs.NonIndexed().Pack(amount, returnedToPool)
	if err != nil {
		t.Fatalf("pack non-indexed: %v", err)
	}
	return &types.Log{
		Address: contractAddr,
		Topics:  []common.Hash{ev.ID, tradeID},
		Data:    data,
	}
}

func mustBuildTradeReleasedLog(t *testing.T, contractAddr common.Address,
	tradeID [32]byte, buyer common.Address, amountToBuyer, feeToSystem *big.Int) *types.Log {
	t.Helper()
	ev := parsedP2PEscrowABI.Events["TradeReleased"]
	data, err := ev.Inputs.NonIndexed().Pack(amountToBuyer, feeToSystem)
	if err != nil {
		t.Fatalf("pack non-indexed: %v", err)
	}
	return &types.Log{
		Address: contractAddr,
		Topics: []common.Hash{
			ev.ID,
			tradeID,
			common.BytesToHash(buyer.Bytes()),
		},
		Data: data,
	}
}

func TestParseTradePaidFromReceipt(t *testing.T) {
	contract := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	id := HashID(uuid.New())
	lg := &types.Log{Address: contract, Topics: []common.Hash{parsedP2PEscrowABI.Events["TradePaid"].ID, common.BytesToHash(id[:])}}
	ev, err := parseTradePaidFromReceipt(&types.Receipt{Status: 1, Logs: []*types.Log{lg}}, contract, id)
	if err != nil || ev.TradeID != id {
		t.Fatalf("ev=%+v err=%v", ev, err)
	}
	other := HashID(uuid.New())
	if _, err := parseTradePaidFromReceipt(&types.Receipt{Status: 1, Logs: []*types.Log{lg}}, contract, other); err == nil {
		t.Fatal("another trade's TradePaid must not verify")
	}
	foreign := *lg
	foreign.Address = common.HexToAddress("0x0000000000000000000000000000000000000001")
	if _, err := parseTradePaidFromReceipt(&types.Receipt{Status: 1, Logs: []*types.Log{&foreign}}, contract, id); err == nil {
		t.Fatal("a log from another contract must not verify")
	}
}

func TestVerifyTradePaid_RequiresClient(t *testing.T) {
	if _, err := makeTestSvc(t).VerifyTradePaid("0xabc", HashID(uuid.New())); err == nil {
		t.Fatal("want error without a chain client")
	}
}
