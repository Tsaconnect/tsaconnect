package services

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/holiman/uint256"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// transferCalldata is transfer(to, amount), built by hand rather than with the
// production helper, so the test checks it.
func transferCalldata(to string, amount *big.Int) []byte {
	return append(append(common.FromHex("0xa9059cbb"), common.LeftPadBytes(common.HexToAddress(to).Bytes(), 32)...),
		common.LeftPadBytes(amount.Bytes(), 32)...)
}

// bscAmount is micro-USD in 18-decimal BSC USDT base units.
func bscAmount(micro int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(micro), big.NewInt(1_000_000_000_000))
}

// refundTx is a transaction to sign. txType defaults to dynamic-fee.
type refundTx struct {
	key     *ecdsa.PrivateKey
	chainID int64
	token   string
	data    []byte
	value   *big.Int
	nonce   uint64
	txType  uint8
}

func signRefundTx(t *testing.T, spec refundTx) (string, *types.Transaction) {
	t.Helper()
	to := common.HexToAddress(spec.token)
	value := spec.value
	if value == nil {
		value = new(big.Int)
	}
	chainID := big.NewInt(spec.chainID)
	u256 := func(b *big.Int) *uint256.Int { return uint256.MustFromBig(b) }
	var inner types.TxData
	switch spec.txType {
	case types.LegacyTxType:
		inner = &types.LegacyTx{Nonce: spec.nonce, To: &to, Value: value, Gas: 80_000, GasPrice: big.NewInt(2), Data: spec.data}
	case types.AccessListTxType:
		inner = &types.AccessListTx{ChainID: chainID, Nonce: spec.nonce, To: &to, Value: value, Gas: 80_000, GasPrice: big.NewInt(2), Data: spec.data}
	case types.BlobTxType:
		inner = &types.BlobTx{ChainID: u256(chainID), Nonce: spec.nonce, To: to, Value: u256(value), Gas: 80_000,
			GasFeeCap: uint256.NewInt(2), GasTipCap: uint256.NewInt(1), Data: spec.data, BlobFeeCap: uint256.NewInt(1),
			BlobHashes: []common.Hash{{0x01}}}
	case types.SetCodeTxType:
		inner = &types.SetCodeTx{ChainID: u256(chainID), Nonce: spec.nonce, To: to, Value: u256(value), Gas: 80_000,
			GasFeeCap: uint256.NewInt(2), GasTipCap: uint256.NewInt(1), Data: spec.data,
			AuthList: []types.SetCodeAuthorization{{ChainID: *u256(chainID), Address: common.HexToAddress("0x4444444444444444444444444444444444444444")}}}
	default:
		inner = &types.DynamicFeeTx{ChainID: chainID, Nonce: spec.nonce, To: &to, Value: value,
			Gas: 80_000, GasFeeCap: big.NewInt(2), GasTipCap: big.NewInt(1), Data: spec.data}
	}
	tx, err := types.SignTx(types.NewTx(inner), types.LatestSignerForChainID(chainID), spec.key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return hexutil.Encode(raw), tx
}

// exactRefundTx is the BSC USDT transfer that pays ref exactly, from the owner.
func (r *refundRig) exactRefundTx(ref *models.PayRefund) refundTx {
	return refundTx{key: r.ownerKey, chainID: 56, token: refundBSCUSDT, data: transferCalldata(ref.ToAddress, bscAmount(ref.AmountMicroUSD)),
		txType: types.DynamicFeeTxType}
}

// requested is a live refund of amount, asked for in the app, on a fresh
// direct payment (cap $34) — its destination is the funding wallet.
func (r *refundRig) requested(t *testing.T, ref, amount string) *models.PayRefund {
	t.Helper()
	p := r.paidDirect(t, ref, "34", "")
	out, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, PayRefundRequest{Amount: amount, Via: models.PayRefundViaApp})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertNothingSent fails unless ref is still requested with no hash, and the
// network was never handed a transaction.
func (r *refundRig) assertNothingSent(t *testing.T, ref *models.PayRefund) {
	t.Helper()
	got, err := r.s.Get(context.Background(), r.m.ID, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.PayRefundRequested || got.TxHash != nil || got.RawTx != "" || len(r.net.broadcasts()) != 0 {
		t.Fatalf("a refused transaction was recorded or sent: %+v, %d broadcasts", got, len(r.net.broadcasts()))
	}
}

func TestPayRefundDetail_OptionsAreEveryListedStableWithTheDepositFirst(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "DET-1", "12.50")
	d, err := r.s.Detail(context.Background(), r.m, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []PayRefundOption{
		{ChainID: 56, ChainKey: "bsc", ChainName: "BNB Smart Chain", TokenSymbol: "USDT", TokenAddress: refundBSCUSDT, Decimals: 18,
			AmountBaseUnits: "12500000000000000000", AmountDisplay: "12.5", IsDefault: true},
		{ChainID: 8453, ChainKey: "base", ChainName: "Base", TokenSymbol: "USDC", TokenAddress: refundBaseUSDC, Decimals: 6,
			AmountBaseUnits: "12500000", AmountDisplay: "12.5"},
	}
	if len(d.Options) != 2 || d.Options[0] != want[0] || d.Options[1] != want[1] {
		t.Fatalf("options %+v", d.Options)
	}
	if d.FromAddress != r.ownerWallet || d.PayerFirstName != "Ada" || d.Payment.MerchantName != "Sky Air" ||
		!strings.HasPrefix(d.Payment.Code, "TSA-") || d.Refund.ID != ref.ID || d.Payment.CustomerTotal == 0 {
		t.Fatalf("detail %+v", d)
	}
}

// The default follows the payer's own deposit even when it is not the first
// chain listed: a converted payment's deposit was Base USDC.
func TestPayRefundDetail_TheDefaultIsWhatThePayerPaidIn(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidByOrder(t, "DET-4", "base", "USDC", "34", "")
	ref, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp})
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.s.Detail(context.Background(), r.m, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Options) != 2 || d.Options[0].ChainID != 8453 || !d.Options[0].IsDefault || d.Options[1].IsDefault {
		t.Fatalf("options %+v", d.Options)
	}
}

// A deposit on a chain refunds can't use (depositChainID 0) still gets one
// default: the first option.
func TestPayRefundDetail_AnUnknownDepositDefaultsToTheFirstOption(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidByOrder(t, "DET-5", "polygon", "USDT", "34", "")
	ref, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp})
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.s.Detail(context.Background(), r.m, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Options) != 2 || d.Options[0].ChainID != 56 || !d.Options[0].IsDefault || d.Options[1].IsDefault {
		t.Fatalf("options %+v", d.Options)
	}
}

func TestPayRefundDetail_NoFromAddressUntilTheOwnerProvesTheWallet(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "DET-2", "1")
	r.db.Where("user_id = ?", r.m.UserID).Delete(&models.WalletLinkChallenge{})
	if d, err := r.s.Detail(context.Background(), r.m, ref.ID); err != nil || d.FromAddress != "" {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestPayRefundDetail_AClosedRefundOffersNoOptions(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "DET-3", "1")
	if _, err := r.s.Cancel(context.Background(), r.m.ID, ref.ID); err != nil {
		t.Fatal(err)
	}
	if d, err := r.s.Detail(context.Background(), r.m, ref.ID); err != nil || d.Options == nil || len(d.Options) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestPayRefundDetail_OnlyTheMerchantsOwn(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "DET-6", "1")
	other := *r.m
	other.ID = uuid.New()
	if _, err := r.s.Detail(context.Background(), &other, ref.ID); !errors.Is(err, ErrPayRefundNotFound) {
		t.Fatalf("%v", err)
	}
}

// Unreadable decimals drop that option and are logged without the RPC's URL.
func TestPayRefundDetail_UnreadableDecimalsAreLoggedRedacted(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "DET-7", "1")
	logged := captureLog(t)
	r.net.decimalsErr = errors.New(`Post "https://bsc.example-rpc.io/v2/SECRETKEY": dial tcp: i/o timeout`)
	d, err := r.s.Detail(context.Background(), r.m, ref.ID)
	if err != nil || len(d.Options) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
	if strings.Contains(logged.String(), "SECRETKEY") || !strings.Contains(logged.String(), "decimals") {
		t.Fatalf("log %q", logged.String())
	}
}

// D23: the hash is recorded before the money can move, then broadcast.
func TestPayRefundSubmit_RecordsTheHashThenBroadcasts(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-1", "12.50")
	raw, tx := signRefundTx(t, r.exactRefundTx(ref))
	got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "usdt", SignedTx: raw})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.PayRefundSubmitted || got.TxHash == nil || *got.TxHash != tx.Hash().Hex() || got.ChainID != 56 ||
		got.TokenSymbol != "USDT" || got.TokenAddress != refundBSCUSDT || got.FromAddress != r.ownerWallet || got.RawTx != raw || got.SubmittedAt == nil {
		t.Fatalf("refund %+v", got)
	}
	if got.LastBroadcastError != "" || got.LastBroadcastAt == nil {
		t.Fatalf("broadcast record %q %v", got.LastBroadcastError, got.LastBroadcastAt)
	}
	if sent := r.net.broadcasts(); len(sent) != 1 || sent[0].Hash() != tx.Hash() {
		t.Fatalf("broadcasts %d", len(sent))
	}
}

// The hash is on the row before the network sees the transaction: a crash
// mid-broadcast still leaves the refund bound to its own transfer.
func TestPayRefundSubmit_TheRowIsWrittenBeforeTheBroadcast(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-9", "1")
	raw, tx := signRefundTx(t, r.exactRefundTx(ref))
	var atBroadcast *models.PayRefund
	r.net.onBroadcast = func() { atBroadcast, _ = r.s.Get(context.Background(), r.m.ID, ref.ID) }
	if _, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw}); err != nil {
		t.Fatal(err)
	}
	if atBroadcast == nil || atBroadcast.Status != models.PayRefundSubmitted || atBroadcast.TxHash == nil ||
		*atBroadcast.TxHash != tx.Hash().Hex() || atBroadcast.RawTx != raw {
		t.Fatalf("at broadcast the row was %+v", atBroadcast)
	}
}

// Only a transaction the owner's proven wallet signed, sending exactly this
// refund's amount of the chosen listed stablecoin to its destination, is taken.
func TestPayRefundSubmit_RefusesATransactionThatIsNotExactlyThisRefund(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-2", "12.50")
	stranger, _ := crypto.GenerateKey()
	exact := r.exactRefundTx(ref)
	with := func(change func(*refundTx)) string {
		spec := exact
		change(&spec)
		raw, _ := signRefundTx(t, spec)
		return raw
	}
	exactRaw := with(func(*refundTx) {})
	notExactly := "it doesn't send exactly 12.5 USDT to the customer"
	for name, c := range map[string]struct {
		in  PayRefundSubmit
		why string
	}{
		"unsupported chain":        {PayRefundSubmit{ChainID: 97, TokenSymbol: "USDT", SignedTx: with(func(s *refundTx) { s.chainID = 97 })}, "refunds can't be sent on that network"},
		"signed for another chain": {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: with(func(s *refundTx) { s.chainID = 8453 })}, "it was signed for another network"},
		"token not listed there":   {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDC", SignedTx: exactRaw}, "USDC isn't available on BNB Smart Chain"},
		"not a stablecoin":         {PayRefundSubmit{ChainID: 56, TokenSymbol: "BNB", SignedTx: exactRaw}, "refunds are sent in USDC or USDT"},
		"another token contract":   {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: with(func(s *refundTx) { s.token = refundBaseUSDC })}, "it doesn't send USDT"},
		"another recipient": {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: with(func(s *refundTx) {
			s.data = transferCalldata("0x3333333333333333333333333333333333333333", bscAmount(12_500_000))
		})}, notExactly},
		"another amount":             {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: with(func(s *refundTx) { s.data = transferCalldata(ref.ToAddress, bscAmount(12_499_999)) })}, notExactly},
		"extra calldata":             {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: with(func(s *refundTx) { s.data = append(append([]byte{}, s.data...), 0x00) })}, notExactly},
		"also sends the native coin": {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: with(func(s *refundTx) { s.value = big.NewInt(1) })}, "it also sends the network's own coin"},
		"a blob transaction":         {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: with(func(s *refundTx) { s.txType = types.BlobTxType })}, "it isn't a plain token transfer"},
		"a set-code transaction":     {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: with(func(s *refundTx) { s.txType = types.SetCodeTxType })}, "it isn't a plain token transfer"},
		"signed by another wallet":   {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: with(func(s *refundTx) { s.key = stranger })}, "not your TSA wallet " + r.ownerWallet + " — switch to that wallet"},
		"not a transaction":          {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: "0x1234"}, "it isn't a signed transaction"},
		"not hex":                    {PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: "hello"}, "it isn't a signed transaction"},
	} {
		_, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, c.in)
		var txErr *PayRefundTxError
		if !errors.As(err, &txErr) || !strings.Contains(txErr.Why, c.why) {
			t.Errorf("%s: %v, want a refusal saying %q", name, err, c.why)
		}
	}
	r.assertNothingSent(t, ref)
}

// Legacy (EIP-155) and access-list transfers are as good as dynamic-fee ones.
func TestPayRefundSubmit_TakesLegacyAndAccessListTransfers(t *testing.T) {
	for _, kind := range []uint8{types.LegacyTxType, types.AccessListTxType} {
		r := newRefundRig(t)
		ref := r.requested(t, "SUB-10", "1")
		spec := r.exactRefundTx(ref)
		spec.txType = kind
		raw, tx := signRefundTx(t, spec)
		got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
		if err != nil || got.Status != models.PayRefundSubmitted || *got.TxHash != tx.Hash().Hex() {
			t.Fatalf("type %d: %+v %v", kind, got, err)
		}
	}
}

// P9: a transaction whose nonce the owner's wallet has already used can never
// be mined. It is refused before anything is written, so the owner can sign
// again — rather than parking the refund in review.
func TestPayRefundSubmit_RefusesANonceTheWalletAlreadyUsed(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-11", "1")
	spec := r.exactRefundTx(ref)
	spec.nonce = 7
	raw, _ := signRefundTx(t, spec)
	in := PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw}
	r.net.nonce = 8
	if _, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, in); !errors.Is(err, ErrPayRefundNonceUsed) {
		t.Fatalf("%v", err)
	}
	r.assertNothingSent(t, ref)
	if PayRefundRefusal(ErrPayRefundNonceUsed) == "" {
		t.Fatal("the nonce refusal has no owner-facing words")
	}
	r.net.nonce = 7 // the next free nonce is the transaction's own
	if got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, in); err != nil || got.Status != models.PayRefundSubmitted {
		t.Fatalf("%+v %v", got, err)
	}
}

// A check that can't be made is not a check passed: nothing is written, and
// what the RPC said is logged redacted, never returned.
func TestPayRefundSubmit_AnUnreadableNonceIsUnavailable(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-12", "1")
	logged := captureLog(t)
	r.net.nonceErr = errors.New(`Post "https://bsc.example-rpc.io/v2/SECRETKEY": dial tcp: i/o timeout`)
	raw, _ := signRefundTx(t, r.exactRefundTx(ref))
	_, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
	if !errors.Is(err, ErrPayRefundUnavailable) || strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(logged.String(), "SECRETKEY") {
		t.Fatalf("log %q", logged.String())
	}
	r.assertNothingSent(t, ref)
}

func TestPayRefundSubmit_UnreadableDecimalsAreUnavailable(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-13", "1")
	raw, _ := signRefundTx(t, r.exactRefundTx(ref))
	r.net.decimalsErr = errors.New(`Post "https://bsc.example-rpc.io/v2/SECRETKEY": dial tcp: i/o timeout`)
	_, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
	if !errors.Is(err, ErrPayRefundUnavailable) || strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("%v", err)
	}
	r.assertNothingSent(t, ref)
}

// The wallet the handler vouches for must be the owner's proven one now.
func TestPayRefundSubmit_OnlyFromTheOwnersProvenWallet(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-14", "1")
	raw, _ := signRefundTx(t, r.exactRefundTx(ref))
	in := PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw}
	var txErr *PayRefundTxError
	if _, err := r.s.Submit(context.Background(), r.m, ref.ID, "0x5555555555555555555555555555555555555555", in); !errors.As(err, &txErr) {
		t.Fatalf("another wallet: %v", err)
	}
	r.db.Where("user_id = ?", r.m.UserID).Delete(&models.WalletLinkChallenge{})
	if _, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, in); !errors.As(err, &txErr) {
		t.Fatalf("unproven: %v", err)
	}
	r.assertNothingSent(t, ref)
}

func TestPayRefundSubmit_OnlyTheMerchantsOwn(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-15", "1")
	raw, _ := signRefundTx(t, r.exactRefundTx(ref))
	other := *r.m
	other.ID = uuid.New()
	if _, err := r.s.Submit(context.Background(), &other, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw}); !errors.Is(err, ErrPayRefundNotFound) {
		t.Fatalf("%v", err)
	}
	r.assertNothingSent(t, ref)
}

// A refused broadcast leaves the refund submitted (the confirmer sends it
// again) and says why, in words the owner can act on (review I6). The raw
// error is logged only redacted (P4).
func TestPayRefundSubmit_ABroadcastErrorLeavesItSubmitted(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-3", "1")
	logged := captureLog(t)
	r.net.broadcastErr = errors.New(`insufficient funds for gas * price + value: balance 0, tx cost 160000 (https://bsc.example-rpc.io/v2/SECRETKEY)`)
	raw, _ := signRefundTx(t, r.exactRefundTx(ref))
	got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
	if err != nil || got.Status != models.PayRefundSubmitted {
		t.Fatalf("%+v %v", got, err)
	}
	if got.LastBroadcastError != "Not enough gas in your wallet" || got.LastBroadcastAt == nil {
		t.Fatalf("broadcast error %q at %v", got.LastBroadcastError, got.LastBroadcastAt)
	}
	if v := r.s.View(got); v.LastBroadcastError != "Not enough gas in your wallet" || v.LastBroadcastAt == nil {
		t.Fatalf("view %+v", v)
	}
	if strings.Contains(logged.String(), "SECRETKEY") || !strings.Contains(logged.String(), got.ID.String()) {
		t.Fatalf("log %q", logged.String())
	}
}

// Submit's own broadcast was refused and the chain doesn't know the transfer:
// its first check leaves sending it again to the confirmer instead of
// re-sending at once and overwriting the refusal it just recorded.
func TestPayRefundSubmit_ARefusedBroadcastIsNotSentAgainInTheSameRequest(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-18", "1")
	r.net.broadcastErr = errors.New("replacement transaction underpriced")
	r.ver.set(nil, errors.New("verify: not found"), false)
	raw, _ := signRefundTx(t, r.exactRefundTx(ref))
	got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
	if err != nil || got.Status != models.PayRefundSubmitted || got.LastBroadcastError != "The network fee was too low" {
		t.Fatalf("%+v %v", got, err)
	}
	if n := len(r.net.broadcasts()); n != 1 {
		t.Fatalf("sent %d times in one submit, want 1", n)
	}
	// The confirmer still sends it again.
	r.net.broadcastErr = nil
	if err := r.s.ConfirmDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(r.net.broadcasts()); n != 2 {
		t.Fatalf("the confirmer sent it %d times in all, want 2", n)
	}
}

// A node that already has the transaction took it: no error is recorded.
func TestPayRefundSubmit_AlreadyKnownIsASend(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-16", "1")
	r.net.broadcastErr = errors.New("already known")
	raw, _ := signRefundTx(t, r.exactRefundTx(ref))
	got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
	if err != nil || got.Status != models.PayRefundSubmitted || got.LastBroadcastError != "" || got.LastBroadcastAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
}

// A later send that goes through clears the earlier refusal.
func TestPayRefundBroadcast_ASuccessClearsTheLastError(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-17", "1")
	r.net.broadcastErr = errors.New("replacement transaction underpriced")
	raw, tx := signRefundTx(t, r.exactRefundTx(ref))
	got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
	if err != nil || got.LastBroadcastError != "The network fee was too low" {
		t.Fatalf("%+v %v", got, err)
	}
	r.net.broadcastErr = nil
	r.s.broadcast(context.Background(), "bsc", got, tx)
	again, _ := r.s.Get(context.Background(), r.m.ID, ref.ID)
	if again.LastBroadcastError != "" || got.LastBroadcastError != "" || len(r.net.broadcasts()) != 2 {
		t.Fatalf("%+v", again)
	}
}

// What the network said is reworded before it is stored or shown: an RPC error
// can carry the provider's URL (and its key).
func TestPayRefundBroadcastReason_IsOwnerSafe(t *testing.T) {
	for raw, want := range map[string]string{
		"insufficient funds for gas * price + value":                            "Not enough gas in your wallet",
		"InsufficientFunds, Balance is 0 less than sending value + gas 160000":  "Not enough gas in your wallet",
		"replacement transaction underpriced":                                   "The network fee was too low",
		"transaction underpriced: tip needed 1, tip permitted 0":                "The network fee was too low",
		"max fee per gas less than block base fee":                              "The network fee was too low",
		"nonce too low: next nonce 8, tx nonce 7":                               "Another transaction from your wallet already used its place",
		`Post "https://bsc.example-rpc.io/v2/SECRETKEY": dial tcp: i/o timeout`: "The network didn't take it",
	} {
		got := payRefundBroadcastReason(errors.New(raw))
		if got != want || strings.Contains(got, "http") {
			t.Errorf("%q → %q, want %q", raw, got, want)
		}
	}
}

func TestPayRefundSubmit_OnlyFromRequested(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-4", "1")
	raw, _ := signRefundTx(t, r.exactRefundTx(ref))
	in := PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw}
	if _, err := r.s.Submit(ctx, r.m, ref.ID, r.ownerWallet, in); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Submit(ctx, r.m, ref.ID, r.ownerWallet, in); !errors.Is(err, ErrPayRefundState) {
		t.Fatalf("twice: %v", err)
	}
	cancelled := r.requested(t, "SUB-5", "1")
	r.s.Cancel(ctx, r.m.ID, cancelled.ID)
	raw2, _ := signRefundTx(t, r.exactRefundTx(cancelled))
	if _, err := r.s.Submit(ctx, r.m, cancelled.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw2}); !errors.Is(err, ErrPayRefundState) {
		t.Fatalf("cancelled: %v", err)
	}
	if len(r.net.broadcasts()) != 1 {
		t.Fatalf("broadcasts %d", len(r.net.broadcasts()))
	}
}

// The guarded write is the last word: a refund that left requested after it
// was read (cancelled in between) is not submitted, and nothing is sent.
func TestPayRefundSubmit_TheWriteIsGuardedOnRequested(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-18", "1")
	raw, _ := signRefundTx(t, r.exactRefundTx(ref))
	r.net.onTokenDecimals = func() {
		r.db.Model(&models.PayRefund{}).Where("id = ?", ref.ID).Update("status", models.PayRefundCancelled)
	}
	if _, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw}); !errors.Is(err, ErrPayRefundState) {
		t.Fatalf("%v", err)
	}
	got, _ := r.s.Get(context.Background(), r.m.ID, ref.ID)
	if got.Status != models.PayRefundCancelled || got.TxHash != nil || len(r.net.broadcasts()) != 0 {
		t.Fatalf("%+v", got)
	}
}

// One on-chain transfer can never settle two refunds.
func TestPayRefundSubmit_ATransactionCannotServeTwoRefunds(t *testing.T) {
	testRefundTxCannotServeTwo(t, newRefundRig(t))
}

func TestPayRefundSubmit_ATransactionCannotServeTwoRefundsOnPostgres(t *testing.T) {
	testRefundTxCannotServeTwo(t, newRefundPostgresRig(t))
}

func testRefundTxCannotServeTwo(t *testing.T, r *refundRig) {
	t.Helper()
	ctx := context.Background()
	a := r.requested(t, "SUB-6", "12.50")
	b := r.requested(t, "SUB-7", "12.50") // same destination and amount: identical calldata
	raw, _ := signRefundTx(t, r.exactRefundTx(a))
	in := PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw}
	if _, err := r.s.Submit(ctx, r.m, a.ID, r.ownerWallet, in); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Submit(ctx, r.m, b.ID, r.ownerWallet, in); !errors.Is(err, ErrPayRefundTxUsed) {
		t.Fatalf("%v", err)
	}
	if got, _ := r.s.Get(ctx, r.m.ID, b.ID); got.Status != models.PayRefundRequested || got.TxHash != nil {
		t.Fatalf("b: %+v", got)
	}
	if len(r.net.broadcasts()) != 1 {
		t.Fatalf("broadcasts %d", len(r.net.broadcasts()))
	}
}

func TestPayRefundSubmit_TestRefundsAreNeverSubmitted(t *testing.T) {
	r := newRefundRig(t)
	p := r.paid(t, models.PayModeTest, "SUB-8", map[string]any{"customer_total_kobo": int64(1_500_000)})
	ref, err := r.s.Request(context.Background(), r.m, models.PayModeTest, p.ID, PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: "0x00"}); !errors.Is(err, ErrPayRefundState) {
		t.Fatalf("%v", err)
	}
}

// A test refund moves no money: even one left in requested is never sent.
func TestPayRefundSubmit_ATestRowIsNeverSentEvenWhenRequested(t *testing.T) {
	r := newRefundRig(t)
	p := r.paid(t, models.PayModeTest, "SUB-19", map[string]any{"customer_total_kobo": int64(1_500_000)})
	ref := r.insertRefund(t, p, models.PayRefundRequested, 1_000_000)
	raw, _ := signRefundTx(t, r.exactRefundTx(ref))
	if _, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw}); !errors.Is(err, ErrPayRefundState) {
		t.Fatalf("%v", err)
	}
	r.assertNothingSent(t, ref)
}

// A nonce far past the wallet's next one leaves a gap that only other
// transactions can fill: it would sit unmined. Refused before anything is
// written, in words the owner can act on.
func TestPayRefundSubmit_RefusesANonceFarAheadOfTheWallet(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-20", "1")
	r.net.nonce = 3
	spec := r.exactRefundTx(ref)
	spec.nonce = 3 + payRefundNonceAhead + 1
	raw, _ := signRefundTx(t, spec)
	if _, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw}); !errors.Is(err, ErrPayRefundNonceFar) {
		t.Fatalf("%v", err)
	}
	r.assertNothingSent(t, ref)
	if PayRefundRefusal(ErrPayRefundNonceFar) == "" {
		t.Fatal("the far-nonce refusal has no owner-facing words")
	}
	spec.nonce = 3 + payRefundNonceAhead // the furthest still taken
	raw, _ = signRefundTx(t, spec)
	if got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw}); err != nil || got.Status != models.PayRefundSubmitted {
		t.Fatalf("%+v %v", got, err)
	}
}

// The owner's app may hang up mid-broadcast: the send and its record still
// finish, so the row says how the send went.
func TestPayRefundBroadcast_OutlivesACancelledRequest(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-21", "1")
	raw, tx := signRefundTx(t, r.exactRefundTx(ref))
	got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.net.broadcastErr = errors.New("insufficient funds for gas * price + value")
	r.s.broadcast(ctx, "bsc", got, tx)
	if errs := r.net.sentCtxErrs; errs[len(errs)-1] != nil {
		t.Fatalf("the broadcast ran on a cancelled context: %v", errs[len(errs)-1])
	}
	again, _ := r.s.Get(context.Background(), r.m.ID, ref.ID)
	if again.LastBroadcastError != "Not enough gas in your wallet" {
		t.Fatalf("the send was not recorded: %+v", again)
	}
}

// A refund that settled while it was being sent again keeps its record clean:
// the broadcast record only ever writes a submitted row.
func TestPayRefundBroadcast_RecordsOnlyOnASubmittedRefund(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "SUB-22", "1")
	raw, tx := signRefundTx(t, r.exactRefundTx(ref))
	got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
	if err != nil {
		t.Fatal(err)
	}
	r.net.broadcastErr = errors.New("insufficient funds for gas * price + value")
	r.net.onBroadcast = func() {
		r.db.Model(&models.PayRefund{}).Where("id = ?", ref.ID).Update("status", models.PayRefundConfirmed)
	}
	r.s.broadcast(context.Background(), "bsc", got, tx)
	again, _ := r.s.Get(context.Background(), r.m.ID, ref.ID)
	if again.LastBroadcastError != "" {
		t.Fatalf("a confirmed refund was given a broadcast error: %+v", again)
	}
}

// "nonce too low" for our own transaction usually means it is already mined:
// a send the chain knows is a send that went through, never "another
// transaction used its place".
func TestPayRefundBroadcast_NonceTooLowForOurOwnMinedTransactionIsASend(t *testing.T) {
	for _, tc := range []struct {
		name  string
		known bool
		err   error
		want  string
	}{
		{"ours is known", true, nil, ""},
		{"ours is unknown", false, nil, "Another transaction from your wallet already used its place"},
		{"unreadable", false, errors.New("dial tcp: i/o timeout"), "The network didn't take it"},
	} {
		r := newRefundRig(t)
		ref := r.requested(t, "SUB-23", "1")
		raw, tx := signRefundTx(t, r.exactRefundTx(ref))
		got, err := r.s.Submit(context.Background(), r.m, ref.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
		if err != nil {
			t.Fatal(err)
		}
		r.net.broadcastErr = errors.New("nonce too low: next nonce 1, tx nonce 0")
		r.ver.set(nil, errors.New("verify: not found"), tc.known)
		r.ver.knownErr = tc.err
		r.s.broadcast(context.Background(), "bsc", got, tx)
		again, _ := r.s.Get(context.Background(), r.m.ID, ref.ID)
		if again.LastBroadcastError != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, again.LastBroadcastError, tc.want)
		}
	}
}

func TestPayRefundBroadcast_KnownTransactionIsASend(t *testing.T) {
	for _, msg := range []string{"already known", "AlreadyKnown", "known transaction: 0xabc", "rpc: Known transaction: 0xabc"} {
		if !alreadyKnownTx(errors.New(msg)) {
			t.Errorf("%q is not read as already known", msg)
		}
	}
	for _, msg := range []string{"unknown account", "unknown transaction", "nonce too low"} {
		if alreadyKnownTx(errors.New(msg)) {
			t.Errorf("%q read as already known", msg)
		}
	}
}

// Why a refund's sizing could not be read is logged (redacted), not dropped.
func TestPayRefundDetail_AnUnreadableSourceIsLogged(t *testing.T) {
	r := newRefundRig(t)
	ref := r.requested(t, "DET-8", "1")
	if err := r.db.Where("pay_payment_id = ?", ref.PaymentID).Delete(&models.PaycrestOrderRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Model(&models.PayPayment{}).Where("id = ?", ref.PaymentID).Update("paycrest_order_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	logged := captureLog(t)
	d, err := r.s.Detail(context.Background(), r.m, ref.ID)
	if err != nil || len(d.Options) == 0 || !d.Options[0].IsDefault {
		t.Fatalf("%+v %v", d, err)
	}
	if !strings.Contains(logged.String(), ref.ID.String()) || !strings.Contains(logged.String(), "source") {
		t.Fatalf("log %q", logged.String())
	}
}
