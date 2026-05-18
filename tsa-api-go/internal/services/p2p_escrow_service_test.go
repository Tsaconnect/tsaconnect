package services

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
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

func TestPrepareCreateAd_EmitsExpectedSelectorAndArgs(t *testing.T) {
	svc := makeTestSvc(t)
	adID := HashID(uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"))
	token := "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
	amount := big.NewInt(1_000_000) // 1 USDT
	seller := "0x000000000000000000000000000000000000beef"

	raw, err := svc.PrepareCreateAd(adID, seller, token, amount)
	if err != nil {
		t.Fatalf("PrepareCreateAd: %v", err)
	}
	tx := decodeUnsignedTx(t, raw)

	// Function selector for createAd(bytes32,address,uint256) — first 4 bytes
	// of keccak256("createAd(bytes32,address,uint256)").
	wantSelector := selectorFor(t, "createAd(bytes32,address,uint256)")
	gotData := mustDecodeHex(t, tx.Data)
	if !bytesEqual(gotData[:4], wantSelector) {
		t.Fatalf("createAd selector mismatch: want %x got %x", wantSelector, gotData[:4])
	}
	// adId is the next 32 bytes after the selector.
	if !bytesEqual(gotData[4:36], adID[:]) {
		t.Fatalf("createAd adId not encoded as first arg")
	}
}

func TestPrepareOpenTrade_EncodesAllThreeArgs(t *testing.T) {
	svc := makeTestSvc(t)
	tradeID := HashID(uuid.New())
	adID := HashID(uuid.New())
	amount := big.NewInt(500_000_000) // 500 USDT
	buyer := "0x000000000000000000000000000000000000d00d"

	raw, err := svc.PrepareOpenTrade(tradeID, adID, buyer, amount)
	if err != nil {
		t.Fatalf("PrepareOpenTrade: %v", err)
	}
	tx := decodeUnsignedTx(t, raw)
	data := mustDecodeHex(t, tx.Data)

	wantSelector := selectorFor(t, "openTrade(bytes32,bytes32,uint256)")
	if !bytesEqual(data[:4], wantSelector) {
		t.Fatalf("openTrade selector mismatch")
	}
	if !bytesEqual(data[4:36], tradeID[:]) {
		t.Fatalf("openTrade tradeId not in slot 0")
	}
	if !bytesEqual(data[36:68], adID[:]) {
		t.Fatalf("openTrade adId not in slot 1")
	}
	// Amount in slot 2 should ABI-encode to 32 bytes big-endian.
	want := make([]byte, 32)
	amount.FillBytes(want)
	if !bytesEqual(data[68:100], want) {
		t.Fatalf("openTrade amount not in slot 2")
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
	_, err := svc.PrepareCreateAd(HashID(uuid.New()), "0xa", "0xb", big.NewInt(1))
	if err == nil {
		t.Fatalf("expected error when P2PEscrowAddress empty")
	}
}

// TestBuildUnsignedTx_NonDevEnvFailsClosed pins the positive-allowlist policy
// added by M1: any env string other than the literal "development" must fail
// closed when the RPC client is nil. Catches the common typo `"dev"` and the
// case where Env is empty / unset.
func TestBuildUnsignedTx_NonDevEnvFailsClosed(t *testing.T) {
	for _, env := range []string{"", "dev", "staging", "production", "Development"} {
		cfg := &config.Config{
			Env:              env,
			P2PEscrowAddress: "0xb5B073BF578fE24311ED50e540D9d632f1679261",
		}
		svc := NewP2PEscrowService(nil, cfg) // nil client
		_, err := svc.PrepareCreateAd(
			HashID(uuid.New()),
			"0x000000000000000000000000000000000000beef",
			"0x000000000000000000000000000000000000feed",
			big.NewInt(1),
		)
		if err == nil {
			t.Errorf("env=%q: expected fail-closed when client is nil", env)
		}
	}
}

func TestReadAd_RequiresClient(t *testing.T) {
	svc := makeTestSvc(t)
	_, err := svc.ReadAd(HashID(uuid.New()))
	if err == nil {
		t.Fatalf("expected error: ReadAd without RPC client")
	}
}

func TestABIEventsHaveExpectedTopicIDs(t *testing.T) {
	// Sanity check the events are parseable and their topic IDs match the
	// canonical keccak256(signature). If a signature drifts, on-chain events
	// won't decode — this test catches it at unit-test time.
	cases := map[string]string{
		"AdCreated":     "AdCreated(bytes32,address,address,uint256)",
		"TradeOpened":   "TradeOpened(bytes32,bytes32,address,uint256)",
		"TradePaid":     "TradePaid(bytes32)",
		"TradeReleased": "TradeReleased(bytes32,address,uint256,uint256)",
		"TradeCancelled": "TradeCancelled(bytes32,uint256)",
		"TradeDisputed":  "TradeDisputed(bytes32,address)",
		"TradeResolved":  "TradeResolved(bytes32,bool,uint256,uint256)",
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

// TestPrepare_SingleArgFns_AllSelectorsCorrect covers the six Prepare* methods
// that pass only a tradeId. A typo in the ABI string ("releas") or a
// function-name rename in Solidity would slip past compilation; this test
// pins the wire format.
func TestPrepare_SingleArgFns_AllSelectorsCorrect(t *testing.T) {
	svc := makeTestSvc(t)
	tradeID := HashID(uuid.New())
	caller := "0x000000000000000000000000000000000000face"

	cases := []struct {
		name    string
		sig     string
		prepare func() ([]byte, error)
	}{
		{"closeAd", "closeAd(bytes32)", func() ([]byte, error) { return svc.PrepareCloseAd(tradeID, caller) }},
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

// TestParseAdCreatedFromReceipt_HappyPath ensures the receipt parser decodes
// indexed (adId, seller, token) and non-indexed (amount) correctly. Built
// from a synthetic types.Receipt so no chain is required.
func TestParseAdCreatedFromReceipt_HappyPath(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	adID := HashID(uuid.New())
	seller := common.HexToAddress("0x000000000000000000000000000000000000beef")
	token := common.HexToAddress("0x000000000000000000000000000000000000feed")
	amount := big.NewInt(1_500_000_000) // 1500 USDT

	logEntry := mustBuildAdCreatedLog(t, contractAddr, adID, seller, token, amount)
	receipt := &types.Receipt{
		Status: 1,
		Logs:   []*types.Log{logEntry},
	}

	ev, err := parseAdCreatedFromReceipt(receipt, contractAddr, adID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.AdID != adID {
		t.Errorf("adId mismatch")
	}
	if ev.Seller != seller {
		t.Errorf("seller mismatch: want %s got %s", seller.Hex(), ev.Seller.Hex())
	}
	if ev.Token != token {
		t.Errorf("token mismatch")
	}
	if ev.Amount.Cmp(amount) != 0 {
		t.Errorf("amount mismatch: want %s got %s", amount, ev.Amount)
	}
}

func TestParseAdCreatedFromReceipt_IgnoresOtherContracts(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	imposter := common.HexToAddress("0x1111111111111111111111111111111111111111")
	adID := HashID(uuid.New())

	logEntry := mustBuildAdCreatedLog(t, imposter, adID,
		common.HexToAddress("0xaaaa"), common.HexToAddress("0xbbbb"), big.NewInt(1))
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}

	if _, err := parseAdCreatedFromReceipt(receipt, contractAddr, adID); err == nil {
		t.Fatalf("expected error: log from foreign contract must be ignored")
	}
}

func TestParseAdCreatedFromReceipt_IgnoresWrongAdId(t *testing.T) {
	contractAddr := common.HexToAddress("0xb5B073BF578fE24311ED50e540D9d632f1679261")
	mine := HashID(uuid.New())
	other := HashID(uuid.New())

	logEntry := mustBuildAdCreatedLog(t, contractAddr, other,
		common.HexToAddress("0xa"), common.HexToAddress("0xb"), big.NewInt(1))
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}

	if _, err := parseAdCreatedFromReceipt(receipt, contractAddr, mine); err == nil {
		t.Fatalf("expected error: AdCreated with different adId must not match")
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
	adID := HashID(uuid.New())
	buyer := common.HexToAddress("0x000000000000000000000000000000000000d00d")
	amount := big.NewInt(500_000_000)

	logEntry := mustBuildTradeOpenedLog(t, contractAddr, tradeID, adID, buyer, amount)
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{logEntry}}

	ev, err := parseTradeOpenedFromReceipt(receipt, contractAddr, tradeID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.AdID != adID || ev.Buyer != buyer || ev.Amount.Cmp(amount) != 0 {
		t.Errorf("decoded fields mismatch: %+v", ev)
	}
}

// TestGetAd_OutputUnpackOrder pins the (seller, token, remaining, reserved,
// createdAt, open) field order. A wrong-order ABI string would unpack
// `remaining` into `reserved` and silently corrupt cap-tracking — pack a
// known tuple through the same ABI definition, then unpack it, and assert
// each field round-trips. This catches field-order drift without a chain.
func TestGetAd_OutputUnpackOrder(t *testing.T) {
	wantSeller := common.HexToAddress("0x000000000000000000000000000000000000beef")
	wantToken := common.HexToAddress("0x000000000000000000000000000000000000feed")
	wantRemaining := big.NewInt(700)
	wantReserved := big.NewInt(300)
	wantCreatedAt := big.NewInt(1715000000)
	wantOpen := true

	method, ok := parsedP2PEscrowABI.Methods["getAd"]
	if !ok {
		t.Fatalf("getAd not in parsed ABI")
	}
	packed, err := method.Outputs.Pack(wantSeller, wantToken, wantRemaining, wantReserved, wantCreatedAt, wantOpen)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	vals, err := method.Outputs.Unpack(packed)
	if err != nil || len(vals) != 6 {
		t.Fatalf("unpack: %v vals=%d", err, len(vals))
	}
	// Mirror P2PEscrowService.ReadAd field assignments exactly.
	if vals[0].(common.Address) != wantSeller {
		t.Errorf("slot 0 not seller")
	}
	if vals[1].(common.Address) != wantToken {
		t.Errorf("slot 1 not token")
	}
	if vals[2].(*big.Int).Cmp(wantRemaining) != 0 {
		t.Errorf("slot 2 not remaining (would swap with reserved)")
	}
	if vals[3].(*big.Int).Cmp(wantReserved) != 0 {
		t.Errorf("slot 3 not reserved")
	}
	if vals[4].(*big.Int).Cmp(wantCreatedAt) != 0 {
		t.Errorf("slot 4 not createdAt")
	}
	if vals[5].(bool) != wantOpen {
		t.Errorf("slot 5 not open")
	}
}

// ─── synthetic-log builders ─────────────────────────────────────────────────

func mustBuildAdCreatedLog(t *testing.T, contractAddr common.Address, adID [32]byte,
	seller, token common.Address, amount *big.Int) *types.Log {
	t.Helper()
	ev := parsedP2PEscrowABI.Events["AdCreated"]
	data, err := ev.Inputs.NonIndexed().Pack(amount)
	if err != nil {
		t.Fatalf("pack non-indexed: %v", err)
	}
	return &types.Log{
		Address: contractAddr,
		Topics: []common.Hash{
			ev.ID,
			adID,
			common.BytesToHash(seller.Bytes()),
			common.BytesToHash(token.Bytes()),
		},
		Data: data,
	}
}

func mustBuildTradeOpenedLog(t *testing.T, contractAddr common.Address,
	tradeID, adID [32]byte, buyer common.Address, amount *big.Int) *types.Log {
	t.Helper()
	ev := parsedP2PEscrowABI.Events["TradeOpened"]
	data, err := ev.Inputs.NonIndexed().Pack(amount)
	if err != nil {
		t.Fatalf("pack non-indexed: %v", err)
	}
	return &types.Log{
		Address: contractAddr,
		Topics: []common.Hash{
			ev.ID,
			tradeID,
			adID,
			common.BytesToHash(buyer.Bytes()),
		},
		Data: data,
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
