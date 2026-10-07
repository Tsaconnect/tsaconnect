package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

type recordingSaleVerifier struct {
	credited              *big.Int
	err                   error
	calls                 int
	token, from, to, hash string
}

func (v *recordingSaleVerifier) VerifyTransfer(context.Context, string, string, string) (*big.Int, error) {
	return nil, errors.New("unused")
}
func (v *recordingSaleVerifier) VerifyTransferFrom(_ context.Context, hash, token, from, to string) (*big.Int, error) {
	v.calls++
	v.hash, v.token, v.from, v.to = hash, token, from, to
	return v.credited, v.err
}

type fakeTxProbe struct {
	known           bool
	pending, latest uint64
	head            uint64 // BlockNumber
	receiptBlock    uint64 // the sale's receipt block
	receiptFailed   bool   // the receipt's status: reverted rather than success
	// The nonce is block-aware: latest from block latestSince on, earlier
	// before it (latestSince 0: latest at every block).
	latestSince, earlier uint64
	// matches are Transfer(wallet → her) logs; background is the token's other
	// Transfer traffic, which the scan's sanity query must see. Both are
	// returned only for the block range asked.
	matches, background []types.Log
	logsErr             error
	scans               int // filtered (3-topic) queries
	// blockTime is the receipt block's time; zero reads as now, so a test
	// that does not care about it sees a transfer mined just now.
	blockTime time.Time
	// gen is the RPC endpoint generation. switchOnScan / switchOnReceipt fail
	// the client over to another endpoint during a filtered scan / a receipt
	// read: the reads either side of it may come from different nodes.
	gen                           uint64
	switchOnScan, switchOnReceipt bool
}

func (p *fakeTxProbe) EndpointGeneration() uint64 { return p.gen }

func (p *fakeTxProbe) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	if p.known {
		return &types.Transaction{}, true, nil
	}
	return nil, false, ethereum.NotFound
}
func (p *fakeTxProbe) PendingNonceAt(context.Context, common.Address) (uint64, error) {
	return p.pending, nil
}
func (p *fakeTxProbe) NonceAt(_ context.Context, _ common.Address, block *big.Int) (uint64, error) {
	if block != nil && block.Uint64() < p.latestSince {
		return p.earlier, nil
	}
	return p.latest, nil
}
func (p *fakeTxProbe) BlockNumber(context.Context) (uint64, error) { return p.head, nil }
func (p *fakeTxProbe) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	if p.switchOnReceipt {
		p.gen++
	}
	status := types.ReceiptStatusSuccessful
	if p.receiptFailed {
		status = types.ReceiptStatusFailed
	}
	return &types.Receipt{Status: status, BlockNumber: new(big.Int).SetUint64(p.receiptBlock)}, nil
}
func (p *fakeTxProbe) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	at := p.blockTime
	if at.IsZero() {
		at = time.Now()
	}
	return &types.Header{Number: n, Time: uint64(at.Unix())}, nil
}
func (p *fakeTxProbe) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	if p.logsErr != nil {
		return nil, p.logsErr
	}
	src := p.background
	if len(q.Topics) == 3 {
		p.scans++
		src = p.matches
		if p.switchOnScan {
			p.gen++
		}
	}
	var out []types.Log
	for _, lg := range src {
		if lg.BlockNumber >= q.FromBlock.Uint64() && lg.BlockNumber <= q.ToBlock.Uint64() {
			out = append(out, lg)
		}
	}
	return out, nil
}

type deliverRig struct {
	s        *ITService
	intent   *models.ITIntent
	native   *fakeSponsorNative
	signer   *fakeRelaySigner
	verifier *recordingSaleVerifier
	probe    *fakeTxProbe
}

// newDeliverRig opens and pays (exactly, on time) the company wallet's worked
// example: 7.27815 USDC owed to testRoutedEVM, 50 USDC in stock. The wallet's
// lane has production's caps (none); the next send uses nonce 7 at head block
// 100, and a receipt lands at block 90 (11 deep).
func newDeliverRig(t *testing.T) *deliverRig {
	t.Helper()
	ctx := context.Background()
	s, userID := newRoutedBuyService(t)
	w0, native, signer := newTestCompanyWallet(t, 50_000_000)
	native.transferNonce = 7
	r := &deliverRig{s: s, native: native, signer: signer,
		verifier: &recordingSaleVerifier{err: errors.New("verify: not found")},
		probe:    &fakeTxProbe{head: 100, receiptBlock: 90, background: []types.Log{{BlockNumber: 100}}}}
	s.SetCompanyWallet(NewCompanyWallet(CompanyWalletConfig{
		Treasury: w0.Treasury(), Decimals: map[string]map[string]int{"base": {"USDC": 6}},
		Verifiers: map[string]ERC20SourcedTransferVerifier{"base": r.verifier},
		Probes:    map[string]TreasuryTxProbe{"base": r.probe},
	}))
	s.SetTreasuryMarketRate(&fixedTreasuryRate{rate: treasuryMarket})
	if err := s.db.AutoMigrate(&models.RampLeg{}, &models.ITIntentAuditLog{}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(models.RampLegsOneLiveSaleIndexSQL).Error; err != nil {
		t.Fatalf("index: %v", err)
	}
	intent, _, err := s.OpenTreasuryBuy(ctx, userID, treasuryTestQuote(t, s), "base", testRoutedEVM)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.settleStableBuyFromCharge(ctx, userID, "chg_deliver_t", "1000000"); err != nil {
		t.Fatalf("pay: %v", err)
	}
	r.intent = intent
	return r
}

func (r *deliverRig) legs(t *testing.T) []models.RampLeg {
	t.Helper()
	var legs []models.RampLeg
	r.s.db.Where("order_id = ? AND kind = ?", r.intent.ID, models.RampLegKindSale).Order("attempt ASC").Find(&legs)
	return legs
}

func TestSendTreasuryBuy_SendsOnceAndSettlesOnlyOnAProofFromTheWallet(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	legs := r.legs(t)
	if len(legs) != 1 || legs[0].TxHash != "0xsponsored" || legs[0].Status != models.RampLegStatusPending ||
		legs[0].Amount != "7278150" || !strings.EqualFold(legs[0].ToAddr, testRoutedEVM) ||
		legs[0].Nonce == nil || *legs[0].Nonce != 7 || legs[0].FromBlock != 100 {
		t.Fatalf("legs = %+v", legs)
	}
	if _, _, to, amt := r.native.transferCall(); to != testRoutedEVM || amt.Int64() != 7_278_150 {
		t.Fatalf("transfer to %s of %v", to, amt)
	}
	// Not readable yet: wait, never settle.
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("finalize (not yet): %v", err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want delivering until the receipt proves it", got.Status)
	}
	r.verifier.credited, r.verifier.err = big.NewInt(7_278_150), nil
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if r.verifier.from != sponsorTestSigner || r.verifier.to != testRoutedEVM || r.verifier.token != sponsorTestUSDC {
		t.Fatalf("verified %s → %s in %s, want wallet → her address in USDC", r.verifier.from, r.verifier.to, r.verifier.token)
	}
	got := reloadIntent(t, r.s, r.intent.ID)
	if got.Status != models.ITStatusSettled || got.TxHash != "0xsponsored" {
		t.Fatalf("status=%s tx=%s", got.Status, got.TxHash)
	}
	if legs := r.legs(t); legs[0].Status != models.RampLegStatusConfirmed {
		t.Fatalf("leg = %+v", legs[0])
	}
}

// Review Focus 1: a second tick observes; it never sends.
func TestSendTreasuryBuy_SecondTickNeverBroadcastsAgain(t *testing.T) {
	r := newDeliverRig(t)
	for i := 0; i < 3; i++ {
		if err := r.s.SendTreasuryBuy(context.Background(), r.intent.ID); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if r.signer.calls() != 1 || len(r.legs(t)) != 1 {
		t.Fatalf("broadcasts = %d legs = %d, want exactly one", r.signer.calls(), len(r.legs(t)))
	}
}

// Review Focus 1: a broadcast whose outcome is unknown and whose hash we never
// learned is never sent again by the machine — it stalls and alerts.
func TestSendTreasuryBuy_LegWithoutAHashIsNeverResent(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	r.signer.err = errors.New("rpc timeout after send")
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("uncertain send must not fail the tick: %v", err)
	}
	r.signer.err = nil
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if r.signer.calls() != 1 {
		t.Fatalf("broadcasts = %d, want 1", r.signer.calls())
	}
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("inside the stall window: %v", err)
	}
	r.s.db.Model(&models.RampLeg{}).Where("order_id = ?", r.intent.ID).Update("created_at", time.Now().Add(-31*time.Minute))
	logs := captureLog(t)
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); !errors.Is(err, ErrTreasurySendStalled) {
		t.Fatalf("past the stall: %v", err)
	}
	if !strings.Contains(logs.String(), "ALERT ramp_treasury_send_stalled intent="+r.intent.ID.String()) || r.signer.calls() != 1 {
		t.Fatalf("log=%q broadcasts=%d", logs.String(), r.signer.calls())
	}
}

// §5.4: a send that failed before it was signed (no nonce recorded) frees the
// leg; the next tick sends attempt 2 once a trusted scan shows nothing moved.
func TestSendTreasuryBuy_NotBroadcastBeforeSigningRetriesAsANewAttempt(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	r.native.transferErr = errors.New("failed to get nonce: rpc down")
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err == nil {
		t.Fatal("want the failure reported")
	}
	if legs := r.legs(t); len(legs) != 1 || legs[0].Nonce != nil || !strings.HasPrefix(legs[0].Error, "not broadcast:") {
		t.Fatalf("legs = %+v, want a not-broadcast leg with no nonce", legs)
	}
	r.native.transferErr = nil
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	legs := r.legs(t)
	if len(legs) != 2 || legs[0].Status != models.RampLegStatusFailed || legs[1].Attempt != 2 || legs[1].TxHash != "0xsponsored" {
		t.Fatalf("legs = %+v", legs)
	}
}

// Fix round 2, item 4: a node refused a SIGNED send ("not broadcast" with its
// nonce recorded). Another node may still hold it, so it is treated like a
// dropped send: no attempt 2 until that nonce is consumed 5 blocks deep and a
// trusted scan shows nothing reached her.
func TestSendTreasuryBuy_NotBroadcastAfterSigningWaitsForTheNonceToBeConsumed(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	r.signer.err = fmt.Errorf("%w: insufficient funds for gas", ErrTxNotBroadcast)
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err == nil {
		t.Fatal("want the refusal reported")
	}
	if legs := r.legs(t); len(legs) != 1 || legs[0].Nonce == nil || *legs[0].Nonce != 7 {
		t.Fatalf("legs = %+v, want the signed nonce 7 recorded", legs)
	}
	r.signer.err = nil
	r.probe.pending, r.probe.latest, r.probe.head = 7, 7, 4500
	r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4099}, {BlockNumber: 4500}}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); !errors.Is(err, ErrITBadState) || !strings.Contains(err.Error(), "can still land") {
		t.Fatalf("nonce 7 unused: %v, want refused", err)
	}
	r.probe.pending, r.probe.latest = 8, 8 // consumed (by a cancel), 5+ deep
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if legs := r.legs(t); len(legs) != 2 || legs[1].Attempt != 2 || r.signer.calls() != 2 {
		t.Fatalf("legs = %+v broadcasts = %d", legs, r.signer.calls())
	}
}

// §5.4: a reverted receipt moved nothing; the next tick sends attempt 2.
func TestFinalizeTreasuryBuy_RevertedFreesTheLegForAFreshAttempt(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	r.verifier.credited, r.verifier.err = nil, fmt.Errorf("%w: 0xsponsored", ErrTransferReverted)
	r.probe.receiptFailed = true
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if legs := r.legs(t); len(legs) != 2 || legs[0].Status != models.RampLegStatusFailed || r.signer.calls() != 2 {
		t.Fatalf("legs = %+v broadcasts = %d", legs, r.signer.calls())
	}
}

func TestFinalizeTreasuryBuy_AShortCreditNeverSettles(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	_ = r.s.SendTreasuryBuy(ctx, r.intent.ID)
	r.verifier.credited, r.verifier.err = big.NewInt(7_000_000), nil
	// Review minor: a short credit never improves — it is a stall, alerted at once.
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); !errors.Is(err, ErrTreasurySendStalled) {
		t.Fatalf("a credit below the amount must not settle, and must alert: %v", err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s", got.Status)
	}
}

// §5.4: stock short at send waits, logs, never sends part.
func TestSendTreasuryBuy_StockShortWaitsAndNeverSendsPart(t *testing.T) {
	r := newDeliverRig(t)
	setStock(r.native, 1_000_000)
	logs := captureLog(t)
	if err := r.s.SendTreasuryBuy(context.Background(), r.intent.ID); !errors.Is(err, ErrTreasuryStockShort) {
		t.Fatalf("err = %v", err)
	}
	if len(r.legs(t)) != 0 || r.signer.calls() != 0 || !strings.Contains(logs.String(), "ramp_treasury_stock_short intent=") {
		t.Fatalf("legs=%d broadcasts=%d log=%q", len(r.legs(t)), r.signer.calls(), logs.String())
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want delivering (waiting for a refill)", got.Status)
	}
}

func TestSendTreasuryBuy_KillSwitchStopsSends(t *testing.T) {
	r := newDeliverRig(t)
	r.s.CompanyWallet().Treasury().SetKillSwitch(true)
	if err := r.s.SendTreasuryBuy(context.Background(), r.intent.ID); !errors.Is(err, ErrTreasuryKilled) {
		t.Fatalf("err = %v", err)
	}
	if r.signer.calls() != 0 {
		t.Fatal("sent through the kill switch")
	}
}

// Review Focus 1 / S3: the database refuses a second live sale for one order.
func TestRampLegs_OneLiveSalePerOrderIndex(t *testing.T) {
	r := newDeliverRig(t)
	leg := func(attempt int, status string) error {
		return r.s.db.Create(&models.RampLeg{OrderID: r.intent.ID, Kind: models.RampLegKindSale, Attempt: attempt,
			ChainKey: "base", Token: "USDC", FromAddr: "0xw", ToAddr: "0xd", Amount: "1", Status: status}).Error
	}
	if err := leg(1, models.RampLegStatusFailed); err != nil {
		t.Fatal(err)
	}
	if err := leg(2, models.RampLegStatusPending); err != nil {
		t.Fatal(err)
	}
	if err := leg(3, models.RampLegStatusPending); err == nil {
		t.Fatal("a second live sale for the same order was accepted")
	}
}

// §5.4: an operator re-send only after the dropped-transaction check.
func TestResendTreasuryBuy_OnlyOnProofNothingMoved(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	admin := uuid.New()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil { // nonce 7, from block 100
		t.Fatal(err)
	}
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "dropped"); !errors.Is(err, ErrTreasuryResendRefused) {
		t.Fatalf("inside the stall window: %v", err)
	}
	r.s.db.Model(&models.RampLeg{}).Where("order_id = ?", r.intent.ID).Update("created_at", time.Now().Add(-31*time.Minute))
	r.probe.known = true
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "dropped"); !errors.Is(err, ErrTreasuryResendRefused) {
		t.Fatalf("node still knows the hash: %v", err)
	}
	r.probe.known, r.probe.pending, r.probe.latest = false, 9, 7
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "dropped"); !errors.Is(err, ErrTreasuryResendRefused) {
		t.Fatalf("a pending tx: %v", err)
	}
	// Controller ruling: nonce 7 unused with nothing pending is NOT proof — a
	// signed send our node forgot can still win nonce 7 after a re-send went
	// out on nonce 8. Refused until a same-nonce cancel consumes it.
	r.probe.pending = 7
	_, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "basescan: unknown hash")
	if !errors.Is(err, ErrTreasuryResendRefused) || !errors.Is(err, ErrTreasuryResendNeedsCancel) ||
		!strings.Contains(err.Error(), "nonce 7") {
		t.Fatalf("unused nonce: %v, want refused until a cancel consumes nonce 7", err)
	}
	leg := r.legs(t)[0]
	ev, err := r.s.resendEvidence(ctx, r.intent, &leg)
	if err != nil || ev.Allowed || !ev.NeedsCancel || ev.NonceConsumed ||
		!strings.Contains(ev.Reason, "nonce 7 is still unused") || !strings.Contains(ev.Reason, "same-nonce zero-value cancel") {
		t.Fatalf("evidence = %+v %v, want the cancel instruction shown", ev, err)
	}
	// The cancel consumed nonce 7, and a trusted scan to the head finds
	// nothing to her: now nothing can land.
	r.probe.pending, r.probe.latest, r.probe.head = 8, 8, 4500
	r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4099}, {BlockNumber: 4500}}
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, ""); !errors.Is(err, ErrITOperatorNoteRequired) {
		t.Fatalf("no note: %v", err)
	}
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "cancel 0xc4 consumed nonce 7"); err != nil {
		t.Fatalf("resend: %v", err)
	}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	legs := r.legs(t)
	if len(legs) != 2 || legs[0].Status != models.RampLegStatusFailed || !strings.Contains(legs[0].Error, "dropped") || r.signer.calls() != 2 {
		t.Fatalf("legs = %+v broadcasts = %d", legs, r.signer.calls())
	}
	var n int64
	r.s.db.Model(&models.ITIntentAuditLog{}).Where("intent_id = ? AND action = ?", r.intent.ID, models.ITAuditStableBuyResent).Count(&n)
	if n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
}

// Review C1: the broadcast was mined but its hash was never learned. Its nonce
// was used, the node knows no hash and the mempool is empty (pending ==
// latest): only the chain can tell, and a transfer to her refuses the re-send.
func TestResendTreasuryBuy_MinedButHashLostIsRefused(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	admin := uuid.New()
	r.signer.err = errors.New("rpc timeout after send") // it went out; we never learn the hash
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	leg := r.legs(t)[0]
	if leg.TxHash != "" || leg.Nonce == nil || *leg.Nonce != 7 || leg.FromBlock != 100 {
		t.Fatalf("leg = %+v, want the nonce and start block recorded before the broadcast", leg)
	}
	r.s.db.Model(&models.RampLeg{}).Where("id = ?", leg.ID).Update("created_at", time.Now().Add(-31*time.Minute))
	r.probe.pending, r.probe.latest, r.probe.head = 8, 8, 4500 // nonce 7 consumed, nothing pending
	// Chunks 100–2099, 2100–4099, 4100–4500: the token's traffic is visible at each chunk's end.
	visible := []types.Log{{BlockNumber: 2099}, {BlockNumber: 4099}, {BlockNumber: 4500}}
	r.probe.background = visible
	r.probe.matches = []types.Log{{BlockNumber: 3000, TxHash: common.HexToHash("0xbeef")}}
	_, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "looks dropped")
	if !errors.Is(err, ErrTreasuryResendRefused) || !strings.Contains(err.Error(), "a transfer to her was mined") {
		t.Fatalf("err = %v, want refused on the mined transfer", err)
	}
	if r.probe.scans != 3 {
		t.Fatalf("filtered scans = %d, want 3 chunks of 2000 over blocks 100–4500", r.probe.scans)
	}
	// A blind RPC — empty answers, no error — refuses too.
	r.probe.matches, r.probe.background = nil, nil
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "looks dropped"); !errors.Is(err, ErrTreasuryResendRefused) ||
		!strings.Contains(err.Error(), "could not be trusted") {
		t.Fatalf("blind RPC: %v", err)
	}
	// Review N2: one blind chunk in the middle is enough to refuse — the
	// other chunks' visibility says nothing about it.
	r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4500}}
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "looks dropped"); !errors.Is(err, ErrTreasuryResendRefused) ||
		!strings.Contains(err.Error(), "could not be trusted") || !strings.Contains(err.Error(), "blocks 4095–4099") {
		t.Fatalf("blind middle chunk: %v", err)
	}
	// Only a clean scan with every chunk visible allows it.
	r.probe.background = visible
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "basescan: nothing to her since block 100"); err != nil {
		t.Fatalf("clean scan: %v", err)
	}
	if r.signer.calls() != 1 {
		t.Fatalf("broadcasts = %d: the re-send only frees the leg; the next tick sends", r.signer.calls())
	}
}

// Review N1: a refund after a dropped send stands on the same proof as the
// re-send did — nonce consumed at the head and a trusted scan with nothing to
// her — re-checked at refund time, because a re-send never makes the original
// signed transaction go away.
func TestMarkStableBuyRefunded_RefusedWhileADroppedSendCanStillLand(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	admin := uuid.New()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil { // nonce 7, from block 100
		t.Fatal(err)
	}
	r.s.db.Model(&models.RampLeg{}).Where("order_id = ?", r.intent.ID).Update("created_at", time.Now().Add(-31*time.Minute))
	visible := []types.Log{{BlockNumber: 2099}, {BlockNumber: 4099}, {BlockNumber: 4500}}
	r.probe.pending, r.probe.latest, r.probe.head, r.probe.background = 8, 8, 4500, visible
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "cancel consumed nonce 7"); err != nil {
		t.Fatalf("resend: %v", err)
	}
	r.s.CompanyWallet().Treasury().SetKillSwitch(true) // the next attempt cannot go out
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); !errors.Is(err, ErrTreasuryKilled) {
		t.Fatalf("send: %v", err)
	}
	refund := func() error {
		_, err := r.s.MarkStableBuyRefunded(ctx, admin, r.intent.ID, "customer asked for her naira back")
		return err
	}
	// A node that reads nonce 7 as unused: the original can still land.
	r.probe.pending, r.probe.latest = 7, 7
	if err := refund(); !errors.Is(err, ErrITBadState) || !strings.Contains(err.Error(), "can still land") {
		t.Fatalf("unused nonce: %v, want refused", err)
	}
	r.probe.pending, r.probe.latest = 8, 8
	// Consumed, but a transfer to her was mined: refused.
	r.probe.matches = []types.Log{{BlockNumber: 3000, TxHash: common.HexToHash("0xbeef")}}
	if err := refund(); !errors.Is(err, ErrITBadState) || !strings.Contains(err.Error(), "a transfer to her was mined") {
		t.Fatalf("transfer found: %v, want refused", err)
	}
	// Consumed, nothing to her, but one chunk blind: refused.
	r.probe.matches = nil
	r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4500}}
	if err := refund(); !errors.Is(err, ErrITBadState) || !strings.Contains(err.Error(), "could not be trusted") {
		t.Fatalf("blind chunk: %v, want refused", err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s", got.Status)
	}
	// Consumed and a trusted scan to the head shows nothing reached her.
	r.probe.background = visible
	if err := refund(); err != nil {
		t.Fatalf("refund after the nonce was consumed: %v", err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusRefunded {
		t.Fatalf("status = %s", got.Status)
	}
}

// Security scan A: a revert frees the leg only RAMP_TREASURY_CONFIRMATIONS (5)
// blocks deep — a shallow revert can be reorged into a success. Until then the
// leg is live: no re-send, no refund, no new attempt.
func TestFinalizeTreasuryBuy_ARevertFreesTheLegOnlyAtTheConfirmationDepth(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	admin := uuid.New()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	r.verifier.credited, r.verifier.err = nil, fmt.Errorf("%w: 0xsponsored", ErrTransferReverted)
	r.probe.receiptFailed = true
	r.probe.receiptBlock, r.probe.head = 200, 203 // 4 deep
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if legs := r.legs(t); legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("a 4-deep revert freed the leg: %+v", legs[0])
	}
	if _, err := r.s.MarkStableBuyRefunded(ctx, admin, r.intent.ID, "customer asked"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("refund on a shallow revert: %v", err)
	}
	r.s.db.Model(&models.RampLeg{}).Where("order_id = ?", r.intent.ID).Update("created_at", time.Now().Add(-31*time.Minute))
	r.probe.known = true // mined: the node knows it
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "reverted"); !errors.Is(err, ErrTreasuryResendRefused) {
		t.Fatalf("re-send on a shallow revert: %v", err)
	}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil || r.signer.calls() != 1 {
		t.Fatalf("a new attempt on a shallow revert: err=%v broadcasts=%d", err, r.signer.calls())
	}
	r.probe.head = 204 // 5 deep
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if legs := r.legs(t); len(legs) != 2 || legs[0].Status != models.RampLegStatusFailed || r.signer.calls() != 2 {
		t.Fatalf("legs = %+v broadcasts = %d, want attempt 2 once the revert is 5 deep", legs, r.signer.calls())
	}
}

// Security scan B: every path that could pay her a second time or return her
// naira — the automatic send, an operator re-send, a refund, deliver-anyway —
// goes through one gate (secondPaymentGate), so each earlier-sale state gets
// the same answer on every path.
func TestSecondPaymentGate_EveryEntryPointAgrees(t *testing.T) {
	type state struct {
		name   string
		legs   []models.RampLeg // earlier sales of the order (attempts from 1)
		setup  func(r *deliverRig)
		allow  bool
		noLive bool // the state is itself a live sale: no separate leg for a re-send to replace
	}
	nonce := func(n uint64) *uint64 { return &n }
	failed := func(errText, hash string, n *uint64) models.RampLeg {
		return models.RampLeg{Status: models.RampLegStatusFailed, Error: errText, TxHash: hash, Nonce: n}
	}
	reverted := func(r *deliverRig) {
		r.verifier.credited, r.verifier.err = nil, fmt.Errorf("%w: 0xrev", ErrTransferReverted)
		r.probe.receiptFailed = true
	}
	states := []state{
		{name: "no earlier sale", allow: true},
		{name: "not broadcast before signing (no nonce), clean trusted scan",
			legs: []models.RampLeg{failed("not broadcast: failed to get nonce", "", nil)}, allow: true},
		{name: "not broadcast before signing (no nonce), blind chunk",
			legs:  []models.RampLeg{failed("not broadcast: failed to get nonce", "", nil)},
			setup: func(r *deliverRig) { r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4500}} }},
		{name: "not broadcast after signing, nonce consumed 5+ deep, clean trusted scan",
			legs: []models.RampLeg{failed("not broadcast: insufficient funds for gas", "", nonce(5))}, allow: true},
		{name: "not broadcast after signing, nonce unused",
			legs: []models.RampLeg{failed("not broadcast: insufficient funds for gas", "", nonce(9))}},
		{name: "not broadcast after signing, nonce consumed only at the tip",
			legs:  []models.RampLeg{failed("not broadcast: insufficient funds for gas", "", nonce(5))},
			setup: func(r *deliverRig) { r.probe.latestSince, r.probe.earlier = 4500, 5 }},
		{name: "reverted, 5+ deep", legs: []models.RampLeg{failed("reverted: verify: transaction reverted", "0xrev", nonce(5))},
			setup: reverted, allow: true},
		{name: "reverted, shallow", legs: []models.RampLeg{failed("reverted: verify: transaction reverted", "0xrev", nonce(5))},
			setup: func(r *deliverRig) { reverted(r); r.probe.receiptBlock = 4498 }},
		{name: "reverted, now reads as paid", legs: []models.RampLeg{failed("reverted: verify: transaction reverted", "0xrev", nonce(5))},
			setup: func(r *deliverRig) { r.verifier.credited, r.verifier.err = big.NewInt(7_278_150), nil }},
		{name: "reverted per the verifier, but the receipt reads success",
			legs:  []models.RampLeg{failed("reverted: verify: transaction reverted", "0xrev", nonce(5))},
			setup: func(r *deliverRig) { reverted(r); r.probe.receiptFailed = false }},
		{name: "dropped, nonce unused", legs: []models.RampLeg{failed("dropped: operator re-send", "", nonce(9))}},
		{name: "dropped, nonce consumed, clean trusted scan", legs: []models.RampLeg{failed("dropped: operator re-send", "", nonce(5))}, allow: true},
		{name: "dropped, nonce consumed exactly 5 deep, clean trusted scan",
			legs:  []models.RampLeg{failed("dropped: operator re-send", "", nonce(5))},
			setup: func(r *deliverRig) { r.probe.latestSince, r.probe.earlier = 4496, 5 }, allow: true},
		{name: "dropped, nonce consumed only at the tip",
			legs:  []models.RampLeg{failed("dropped: operator re-send", "", nonce(5))},
			setup: func(r *deliverRig) { r.probe.latestSince, r.probe.earlier = 4500, 5 }},
		{name: "dropped, nonce consumed 4 deep",
			legs:  []models.RampLeg{failed("dropped: operator re-send", "", nonce(5))},
			setup: func(r *deliverRig) { r.probe.latestSince, r.probe.earlier = 4497, 5 }},
		{name: "dropped, nonce consumed, transfer to her found", legs: []models.RampLeg{failed("dropped: operator re-send", "", nonce(5))},
			setup: func(r *deliverRig) {
				r.probe.matches = []types.Log{{BlockNumber: 3000, TxHash: common.HexToHash("0xbeef")}}
			}},
		{name: "dropped, nonce consumed, blind chunk", legs: []models.RampLeg{failed("dropped: operator re-send", "", nonce(5))},
			setup: func(r *deliverRig) { r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4500}} }},
		// A lagging fallback can answer "no logs" for blocks it has not seen:
		// a failover anywhere inside the evidence decides nothing this tick.
		{name: "dropped, nonce consumed, clean scan, but the RPC failed over mid-scan",
			legs:  []models.RampLeg{failed("dropped: operator re-send", "", nonce(5))},
			setup: func(r *deliverRig) { r.probe.switchOnScan = true }},
		{name: "not broadcast before signing (no nonce), clean scan, but the RPC failed over mid-scan",
			legs:  []models.RampLeg{failed("not broadcast: failed to get nonce", "", nil)},
			setup: func(r *deliverRig) { r.probe.switchOnScan = true }},
		{name: "reverted, 5+ deep, but the RPC failed over mid-check",
			legs:  []models.RampLeg{failed("reverted: verify: transaction reverted", "0xrev", nonce(5))},
			setup: func(r *deliverRig) { reverted(r); r.probe.switchOnReceipt = true }},
		{name: "failed for an unknown reason", legs: []models.RampLeg{failed("something odd", "", nonce(5))}},
		{name: "a live sale", legs: []models.RampLeg{{Status: models.RampLegStatusPending, TxHash: "0xlive", Nonce: nonce(5)}}, noLive: true},
	}
	type entry struct {
		name   string
		status string // the order's status before the call
		live   bool   // a stalled live sale for a re-send to replace
		call   func(r *deliverRig) bool
	}
	ctx := context.Background()
	entries := []entry{
		{name: "automatic send", status: models.ITStatusDelivering, call: func(r *deliverRig) bool {
			err := r.s.SendTreasuryBuy(ctx, r.intent.ID)
			return err == nil && r.signer.calls() == 1
		}},
		{name: "operator re-send", status: models.ITStatusDelivering, live: true, call: func(r *deliverRig) bool {
			_, err := r.s.ResendTreasuryBuy(ctx, uuid.New(), r.intent.ID, "checked on basescan")
			return err == nil
		}},
		{name: "refund", status: models.ITStatusDelivering, call: func(r *deliverRig) bool {
			_, err := r.s.MarkStableBuyRefunded(ctx, uuid.New(), r.intent.ID, "returned by bank transfer")
			return err == nil
		}},
		{name: "deliver anyway", status: models.ITStatusBuyAwaitingRate, call: func(r *deliverRig) bool {
			_, err := r.s.DeliverParkedTreasuryBuy(ctx, uuid.New(), r.intent.ID, "owner approved")
			return err == nil
		}},
	}
	for _, st := range states {
		for _, en := range entries {
			if en.live && st.noLive {
				continue // the one-live-sale index forbids a second live leg
			}
			t.Run(st.name+"/"+en.name, func(t *testing.T) {
				r := newDeliverRig(t)
				// A chain where nonce 8 is next and nothing is pending, the
				// token's traffic visible at every chunk's end, receipts 90.
				r.probe.pending, r.probe.latest, r.probe.head = 8, 8, 4500
				r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4099}, {BlockNumber: 4500}}
				if en.status == models.ITStatusBuyAwaitingRate {
					parkTreasury(t, r.s, r.intent, "1000000")
				} else {
					r.s.db.Model(&models.ITIntent{}).Where("id = ?", r.intent.ID).Update("status", en.status)
				}
				legs := append([]models.RampLeg(nil), st.legs...)
				if en.live { // nonce 7 consumed, from block 100: provable on its own
					legs = append(legs, models.RampLeg{Status: models.RampLegStatusPending, Nonce: nonce(7),
						CreatedAt: time.Now().Add(-31 * time.Minute)})
				}
				for i := range legs {
					l := legs[i]
					l.OrderID, l.Kind, l.Attempt = r.intent.ID, models.RampLegKindSale, i+1
					l.ChainKey, l.Token, l.FromAddr, l.ToAddr, l.Amount = "base", "USDC", sponsorTestSigner, testRoutedEVM, "7278150"
					l.FromBlock = 100
					if l.CreatedAt.IsZero() {
						l.CreatedAt = time.Now().Add(-time.Hour)
					}
					if err := r.s.db.Create(&l).Error; err != nil {
						t.Fatalf("leg %d: %v", i+1, err)
					}
				}
				if st.setup != nil {
					st.setup(r)
				}
				if got := en.call(r); got != st.allow {
					t.Fatalf("proceeded = %v, want %v (the same answer on every path)", got, st.allow)
				}
			})
		}
	}
}

// Review minor: a proven sale settles only RAMP_TREASURY_CONFIRMATIONS (5) blocks deep.
func TestFinalizeTreasuryBuy_SettlesOnlyAtTheConfirmationDepth(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	r.verifier.credited, r.verifier.err = big.NewInt(7_278_150), nil
	r.probe.receiptBlock, r.probe.head = 200, 203 // 4 deep
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("4 confirmations: status = %s", got.Status)
	}
	r.probe.head = 204 // 5 deep
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("5 confirmations: status = %s", got.Status)
	}
}

// A failover between the verdict and the depth read means the two answers
// may come from different nodes: neither a settle nor a revert is decided on
// them. The next tick, on one endpoint, decides.
func TestFinalizeTreasuryBuy_AnRPCFailoverMidCheckDecidesNothing(t *testing.T) {
	for _, reverted := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverted=%v", reverted), func(t *testing.T) {
			r := newDeliverRig(t)
			ctx := context.Background()
			if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
				t.Fatal(err)
			}
			r.verifier.credited, r.verifier.err = big.NewInt(7_278_150), nil
			if reverted {
				r.verifier.credited, r.verifier.err = nil, fmt.Errorf("%w: 0xsponsored", ErrTransferReverted)
				r.probe.receiptFailed = true
			}
			r.probe.receiptBlock, r.probe.head = 200, 260
			r.probe.switchOnReceipt = true
			if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
				t.Fatal(err)
			}
			if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering {
				t.Fatalf("status = %s after a mid-check failover, want still delivering", got.Status)
			}
			if legs := r.legs(t); legs[0].Status != models.RampLegStatusPending {
				t.Fatalf("leg = %s after a mid-check failover, want still pending", legs[0].Status)
			}
			r.probe.switchOnReceipt = false
			if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
				t.Fatal(err)
			}
			want := models.RampLegStatusConfirmed
			if reverted {
				want = models.RampLegStatusFailed
			}
			if legs := r.legs(t); legs[0].Status != want {
				t.Fatalf("leg = %s on a steady endpoint, want %s", legs[0].Status, want)
			}
		})
	}
}

// The re-send evidence names the failover, so the operator knows to retry.
func TestResendEvidence_AnRPCFailoverMidScanRefuses(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil { // nonce 7, from block 100
		t.Fatal(err)
	}
	r.s.db.Model(&models.RampLeg{}).Where("order_id = ?", r.intent.ID).Update("created_at", time.Now().Add(-31*time.Minute))
	r.probe.pending, r.probe.latest, r.probe.head = 8, 8, 4500
	r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4099}, {BlockNumber: 4500}}
	r.probe.switchOnScan = true
	leg := r.legs(t)[0]
	ev, err := r.s.resendEvidence(ctx, r.intent, &leg)
	if err != nil || ev.Allowed || ev.ScanTrusted || !strings.Contains(ev.Reason, "RPC endpoint changed") {
		t.Fatalf("evidence = %+v %v, want refused: the endpoint changed mid-scan", ev, err)
	}
	if _, err := r.s.ResendTreasuryBuy(ctx, uuid.New(), r.intent.ID, "checked"); !errors.Is(err, ErrTreasuryResendRefused) {
		t.Fatalf("re-send across a failover: %v", err)
	}
	r.probe.switchOnScan = false
	if ev, err := r.s.resendEvidence(ctx, r.intent, &leg); err != nil || !ev.Allowed {
		t.Fatalf("evidence on a steady endpoint = %+v %v, want allowed", ev, err)
	}
}

// Review C2: "deliver anyway" above $200 is really sent — the lane has
// production's caps (none), so CheckCaps never refuses the waived amount.
func TestSendTreasuryBuy_DeliverAnywayAboveTheMaximumActuallySends(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	parkTreasury(t, r.s, r.intent, "30000000") // ₦300,000 → 218.34448 USDC live
	setStock(r.native, 300_000_000)
	if _, err := r.s.DeliverParkedTreasuryBuy(ctx, uuid.New(), r.intent.ID, "owner approved the overpayment"); err != nil {
		t.Fatalf("deliver anyway: %v", err)
	}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, _, to, amt := r.native.transferCall(); to != testRoutedEVM || amt == nil || amt.Int64() != 218_344_480 || r.signer.calls() != 1 {
		t.Fatalf("sent %v to %s in %d broadcasts, want 218.34448 USDC once", amt, to, r.signer.calls())
	}
}

// Review C2: an order stuck in delivering with no send in flight can be
// marked refunded — and is then never sent.
func TestMarkStableBuyRefunded_StuckDeliveryWithNoLiveSend(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	r.s.CompanyWallet().Treasury().SetKillSwitch(true)
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); !errors.Is(err, ErrTreasuryKilled) {
		t.Fatalf("send: %v", err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s", got.Status)
	}
	got, err := r.s.MarkStableBuyRefunded(ctx, uuid.New(), r.intent.ID, "returned by bank transfer, ref 991")
	if err != nil || got.Status != models.ITStatusRefunded {
		t.Fatalf("refund: %+v %v", got, err)
	}
	r.s.CompanyWallet().Treasury().SetKillSwitch(false)
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); !errors.Is(err, ErrITBadState) || r.signer.calls() != 0 {
		t.Fatalf("a refunded order was sent: err=%v broadcasts=%d", err, r.signer.calls())
	}
}

func TestMarkStableBuyRefunded_RefusedOnceASendIsLive(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.MarkStableBuyRefunded(ctx, uuid.New(), r.intent.ID, "customer asked"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want refused while a send is live", err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s", got.Status)
	}
}

// Review C1: the boot gate's check.
func TestRampLegsOneLiveSaleIndexPresent(t *testing.T) {
	r := newDeliverRig(t) // builds the index
	if ok, err := RampLegsOneLiveSaleIndexPresent(r.s.db); err != nil || !ok {
		t.Fatalf("with the index: ok=%v err=%v", ok, err)
	}
	s, _ := newRoutedBuyService(t)
	if err := s.db.AutoMigrate(&models.RampLeg{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := RampLegsOneLiveSaleIndexPresent(s.db); err != nil || ok {
		t.Fatalf("without the index: ok=%v err=%v", ok, err)
	}
}

func TestPendingTreasuryBuys(t *testing.T) {
	r := newDeliverRig(t)
	received, delivering, err := r.s.PendingTreasuryBuys(context.Background())
	if err != nil || len(received) != 1 || received[0] != r.intent.ID || len(delivering) != 0 {
		t.Fatalf("received=%v delivering=%v err=%v", received, delivering, err)
	}
}

// Fix round 2, item 1: the cancel-then-retry runbook lands its cancel at the
// tip. Consumed only there, a shallow reorg can hand nonce 7 back to the
// original sale while attempt 2 pays on nonce 8 — so the nonce must be
// consumed RAMP_TREASURY_CONFIRMATIONS (5) blocks deep, read at H−4.
func TestResendTreasuryBuy_RefusedUntilTheNonceIsConsumedDeep(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	admin := uuid.New()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil { // nonce 7, from block 100
		t.Fatal(err)
	}
	r.s.db.Model(&models.RampLeg{}).Where("order_id = ?", r.intent.ID).Update("created_at", time.Now().Add(-31*time.Minute))
	r.probe.pending, r.probe.latest, r.probe.head = 8, 8, 4500
	r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4099}, {BlockNumber: 4500}}
	leg := r.legs(t)[0]
	for _, since := range []uint64{4500, 4497} { // consumed at the tip, then 4 deep
		r.probe.latestSince, r.probe.earlier = since, 7
		ev, err := r.s.resendEvidence(ctx, r.intent, &leg)
		if err != nil || ev.Allowed || ev.NonceConsumed || ev.DeepBlock != 4496 || ev.DeepNonce != 7 ||
			!strings.Contains(ev.Reason, "not yet 5 blocks deep") {
			t.Fatalf("consumed from block %d: %+v %v, want refused until 5 deep", since, ev, err)
		}
		if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "cancel sent"); !errors.Is(err, ErrTreasuryResendRefused) {
			t.Fatalf("consumed from block %d: %v", since, err)
		}
	}
	// A chain shorter than the depth cannot prove it at all.
	r.probe.latestSince, r.probe.head = 0, 3
	r.probe.background = []types.Log{{BlockNumber: 3}}
	if ev, err := r.s.resendEvidence(ctx, r.intent, &leg); err != nil || ev.Allowed {
		t.Fatalf("head 3: %+v %v, want refused", ev, err)
	}
	r.probe.head = 4500
	r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4099}, {BlockNumber: 4500}}
	r.probe.latestSince, r.probe.earlier = 4496, 7 // exactly 5 deep
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "cancel 0xc4 is 5 deep"); err != nil {
		t.Fatalf("consumed 5 deep: %v", err)
	}
}

// Fix round 2, item 2: the verifier's verdict and the receipt read for the
// depth must agree on the outcome. A proof against a failed receipt never
// settles; a "reverted" against a successful receipt never frees the leg.
func TestFinalizeTreasuryBuy_TheReceiptStatusMustMatchTheVerdict(t *testing.T) {
	ctx := context.Background()
	t.Run("proven, but the receipt reads failed", func(t *testing.T) {
		r := newDeliverRig(t)
		if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
			t.Fatal(err)
		}
		r.verifier.credited, r.verifier.err = big.NewInt(7_278_150), nil
		r.probe.receiptFailed = true
		if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
			t.Fatal(err)
		}
		if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering {
			t.Fatalf("status = %s, want never settled on a failed receipt", got.Status)
		}
	})
	t.Run("reverted, but the receipt reads success", func(t *testing.T) {
		r := newDeliverRig(t)
		if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
			t.Fatal(err)
		}
		r.verifier.credited, r.verifier.err = nil, fmt.Errorf("%w: 0xsponsored", ErrTransferReverted)
		if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
			t.Fatal(err)
		}
		if legs := r.legs(t); len(legs) != 1 || legs[0].Status != models.RampLegStatusPending {
			t.Fatalf("legs = %+v, want the leg still live", legs)
		}
		if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil || r.signer.calls() != 1 {
			t.Fatalf("a second send: err=%v broadcasts=%d", err, r.signer.calls())
		}
	})
}

// Fix round 2, item 3: a settle never lands over a leg an operator failed
// meanwhile (a re-send between Finalize's read and its write).
func TestSettleTreasuryBuy_RefusedOverAFailedLeg(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	leg := r.legs(t)[0]
	intent := reloadIntent(t, r.s, r.intent.ID)
	if err := r.s.treasuryLegs(r.s.db).Fail(ctx, leg.ID, "dropped: operator re-send"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.settleTreasuryBuy(ctx, &intent, &leg, "7278150"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("settle over a failed leg: %v, want refused", err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering || got.TxHash != "" {
		t.Fatalf("status=%s tx=%s, want untouched", got.Status, got.TxHash)
	}
	// An already-confirmed leg (a crash between the two writes) still settles.
	r2 := newDeliverRig(t)
	if err := r2.s.SendTreasuryBuy(ctx, r2.intent.ID); err != nil {
		t.Fatal(err)
	}
	leg2 := r2.legs(t)[0]
	intent2 := reloadIntent(t, r2.s, r2.intent.ID)
	if err := r2.s.treasuryLegs(r2.s.db).Confirm(ctx, leg2.ID, "7278150", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r2.s.settleTreasuryBuy(ctx, &intent2, &leg2, "7278150"); err != nil {
		t.Fatalf("settle over a confirmed leg: %v", err)
	}
	if got := reloadIntent(t, r2.s, r2.intent.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("status = %s", got.Status)
	}
}

// newDeliverPostgres is a Postgres-backed company wallet with one USDC order
// in `delivering` and no sale yet: the state every concurrent tick races from.
func newDeliverPostgres(t *testing.T) (*ITService, *models.ITIntent, *fakeRelaySigner) {
	t.Helper()
	// paycrest_order_records: a refund closes a converted buy's record in the
	// same transaction (production always has the table).
	db := testutil.TempPostgres(t, "it_treasury_deliver_", &models.ITIntent{}, &models.RampLeg{}, &models.ITIntentAuditLog{},
		&models.PaycrestOrderRecord{})
	if err := db.Exec(models.RampLegsOneLiveSaleIndexSQL).Error; err != nil {
		t.Fatalf("index: %v", err)
	}
	s := NewITService(db, nil, stubFx{ngn: 1500})
	w0, _, signer := newTestCompanyWallet(t, 50_000_000)
	s.SetCompanyWallet(NewCompanyWallet(CompanyWalletConfig{
		Treasury: w0.Treasury(), Decimals: map[string]map[string]int{"base": {"USDC": 6}},
		Verifiers: map[string]ERC20SourcedTransferVerifier{"base": &recordingSaleVerifier{err: errors.New("verify: not found")}},
		Probes:    map[string]TreasuryTxProbe{"base": &fakeTxProbe{head: 100}},
	}))
	intent := newUSDCOrder()
	intent.Status = models.ITStatusDelivering
	if err := db.Create(intent).Error; err != nil {
		t.Fatal(err)
	}
	return s, intent, signer
}

// raceTicks runs fns at once and returns their errors.
func raceTicks(fns ...func() error) []error {
	start := make(chan struct{})
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func(i int, fn func() error) {
			defer wg.Done()
			<-start
			errs[i] = fn()
		}(i, fn)
	}
	close(start)
	wg.Wait()
	return errs
}

// Review Focus 1 / S3 on the real database: ticks racing for one order's
// first sale broadcast once — the row lock and the partial unique index, not
// a read-then-insert check, decide it.
func TestSendTreasuryBuy_ConcurrentTicksBroadcastOnce_Postgres(t *testing.T) {
	s, intent, signer := newDeliverPostgres(t)
	send := func() error { return s.SendTreasuryBuy(context.Background(), intent.ID) }
	for i, err := range raceTicks(send, send, send, send, send) {
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	var legs []models.RampLeg
	s.db.Where("order_id = ? AND kind = ?", intent.ID, models.RampLegKindSale).Find(&legs)
	if signer.calls() != 1 || len(legs) != 1 || legs[0].Nonce == nil || legs[0].TxHash != "0xsponsored" {
		t.Fatalf("broadcasts = %d legs = %+v, want exactly one recorded sale", signer.calls(), legs)
	}
	// A second live sale is refused by the database itself, at any attempt.
	err := s.db.Create(&models.RampLeg{OrderID: intent.ID, Kind: models.RampLegKindSale, Attempt: 9,
		ChainKey: "base", Token: "USDC", FromAddr: "0xw", ToAddr: "0xd", Amount: "1", Status: models.RampLegStatusPending}).Error
	if err == nil {
		t.Fatal("a second live sale was accepted")
	}
	// Reserve resolves that conflict to the live leg rather than an error.
	got, err := (&GormRampLegStore{DB: s.db}).Reserve(context.Background(), &models.RampLeg{OrderID: intent.ID,
		Kind: models.RampLegKindSale, Attempt: 9, ChainKey: "base", Token: "USDC", FromAddr: "0xw", ToAddr: "0xd", Amount: "1"})
	if err != nil || got == nil || got.ID != legs[0].ID {
		t.Fatalf("reserve = %+v, %v, want the live leg back", got, err)
	}
}

// Review C2 on the real database: a refund racing a send — exactly one wins.
// Either the order is refunded and nothing was broadcast, or it was sent and
// the refund was refused.
func TestMarkStableBuyRefunded_RacingASendNeverBoth_Postgres(t *testing.T) {
	for i := 0; i < 10; i++ {
		s, intent, signer := newDeliverPostgres(t)
		ctx := context.Background()
		errs := raceTicks(
			func() error { return s.SendTreasuryBuy(ctx, intent.ID) },
			func() error {
				_, err := s.MarkStableBuyRefunded(ctx, uuid.New(), intent.ID, "returned by bank transfer")
				return err
			})
		got := reloadIntent(t, s, intent.ID)
		switch {
		case errs[1] == nil:
			if got.Status != models.ITStatusRefunded || signer.calls() != 0 || !errors.Is(errs[0], ErrITBadState) {
				t.Fatalf("refund won but status=%s broadcasts=%d send=%v", got.Status, signer.calls(), errs[0])
			}
		case errors.Is(errs[1], ErrITBadState):
			if errs[0] != nil || got.Status != models.ITStatusDelivering || signer.calls() != 1 {
				t.Fatalf("send won but status=%s broadcasts=%d send=%v", got.Status, signer.calls(), errs[0])
			}
		default:
			t.Fatalf("refund: %v", errs[1])
		}
	}
}

// Review C1: the boot gate sees only this schema's ramp_legs — another
// schema's index of the same name in the same database never stands in.
func TestRampLegsOneLiveSaleIndexPresent_Postgres(t *testing.T) {
	s, _, _ := newDeliverPostgres(t)
	if ok, err := RampLegsOneLiveSaleIndexPresent(s.db); err != nil || !ok {
		t.Fatalf("with the index: ok=%v err=%v", ok, err)
	}
	bare := testutil.TempPostgres(t, "it_treasury_noindex_", &models.RampLeg{})
	if ok, err := RampLegsOneLiveSaleIndexPresent(bare); err != nil || ok {
		t.Fatalf("without the index: ok=%v err=%v", ok, err)
	}
}

// Final review F2: two company-wallet orders to one address. Order B's mined
// sale — its hash recorded on B's own leg — shows up in order A's scan. It is
// B's payment, not A's, so it must not refuse A's re-send or refund forever. A
// transfer whose hash no other order records still refuses A: it may be A's
// own lost-hash send.
func TestSecondPaymentGate_AnotherOrdersRecordedSaleIsNotHers(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	admin := uuid.New()
	r.signer.err = errors.New("rpc timeout after send") // A's send went out; its hash is lost
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	r.signer.err = nil
	r.s.db.Model(&models.RampLeg{}).Where("order_id = ?", r.intent.ID).Update("created_at", time.Now().Add(-31*time.Minute))
	r.probe.pending, r.probe.latest, r.probe.head = 8, 8, 4500 // A's nonce 7 consumed 5+ deep
	r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4099}, {BlockNumber: 4500}}

	orderB := seedTreasuryIntent(t, r.s, r.intent.UserID, models.ITStatusSettled)
	bHash := common.HexToHash("0xb0b")
	if err := r.s.db.Create(&models.RampLeg{OrderID: orderB.ID, Kind: models.RampLegKindSale, Attempt: 1,
		ChainKey: "base", Token: "USDC", FromAddr: sponsorTestSigner, ToAddr: testRoutedEVM, Amount: "7278150",
		Status: models.RampLegStatusConfirmed, TxHash: "0x" + strings.ToUpper(bHash.Hex()[2:]), FromBlock: 2500,
		CreatedAt: time.Now().Add(-time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	unrecorded := common.HexToHash("0xbeef")
	both := []types.Log{{BlockNumber: 3000, TxHash: bHash}, {BlockNumber: 3100, TxHash: unrecorded}}
	onlyB := []types.Log{{BlockNumber: 3000, TxHash: bHash}}

	r.probe.matches = both
	_, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "A's nonce consumed by a cancel")
	if !errors.Is(err, ErrTreasuryResendRefused) || !strings.Contains(err.Error(), unrecorded.Hex()) ||
		strings.Contains(err.Error(), bHash.Hex()) {
		t.Fatalf("an unrecorded transfer to her: %v, want refused on it alone", err)
	}
	r.probe.matches = onlyB
	if _, err := r.s.ResendTreasuryBuy(ctx, admin, r.intent.ID, "B's transfer is B's; A's nonce consumed by a cancel"); err != nil {
		t.Fatalf("re-send with only B's recorded sale to her: %v", err)
	}

	refund := func() error {
		_, err := r.s.MarkStableBuyRefunded(ctx, admin, r.intent.ID, "returned by bank transfer")
		return err
	}
	r.probe.matches = both
	if err := refund(); !errors.Is(err, ErrITBadState) || !strings.Contains(err.Error(), "a transfer to her was mined") {
		t.Fatalf("refund with an unrecorded transfer to her: %v, want refused", err)
	}
	r.probe.matches = onlyB
	if err := refund(); err != nil {
		t.Fatalf("refund with only B's recorded sale to her: %v", err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusRefunded {
		t.Fatalf("status = %s", got.Status)
	}
	if r.signer.calls() != 1 {
		t.Fatalf("broadcasts = %d", r.signer.calls())
	}
}

// Final review F4: a send-time backstop. Whatever path priced an order above
// the per-order maximum, it is never sent unless an operator's waiver is
// recorded on it; the refusal is not "stock short", so the watcher reports it
// on the send-blocked channel.
func TestSendTreasuryBuy_AboveTheMaximumWithoutAWaiverIsNeverSent(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	setStock(r.native, 300_000_000)
	r.s.db.Model(&models.ITIntent{}).Where("id = ?", r.intent.ID).Update("asset_amount", "216988129") // $216.99 > $200
	logs := captureLog(t)
	err := r.s.SendTreasuryBuy(ctx, r.intent.ID)
	if !errors.Is(err, ErrStableBuyAboveMaximum) || errors.Is(err, ErrTreasuryStockShort) {
		t.Fatalf("err = %v, want refused above the maximum (and not as stock short)", err)
	}
	if r.signer.calls() != 0 || len(r.legs(t)) != 0 {
		t.Fatalf("broadcasts = %d legs = %d, want nothing sent or reserved", r.signer.calls(), len(r.legs(t)))
	}
	if !strings.Contains(logs.String(), "ALERT ramp_treasury_send_above_maximum intent="+r.intent.ID.String()) {
		t.Fatalf("log = %q", logs.String())
	}
	r.s.db.Model(&models.ITIntent{}).Where("id = ?", r.intent.ID).Update("max_waived", true)
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("waived: %v", err)
	}
	if _, _, _, amt := r.native.transferCall(); r.signer.calls() != 1 || amt == nil || amt.Int64() != 216_988_129 {
		t.Fatalf("sent %v in %d broadcasts, want the waived amount once", amt, r.signer.calls())
	}
}
