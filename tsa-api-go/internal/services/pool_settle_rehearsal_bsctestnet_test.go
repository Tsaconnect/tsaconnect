//go:build bsctestnet

package services

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Plan C2 Task 14: the settle path on chain 97 with throwaway keys — the real
// release key, the real treasury lane (delivery) and real receipts; the LP
// payout gateway is a recording stub (no provider sandbox payouts). It runs
// against BSC testnet or an anvil fork of it (chain id 97 either way; C1's
// guards refuse anything else).

const rehearsalPaymentWindow = 3600 * time.Second // the pool escrow's window (CheckPoolPaymentWindow)

type c2Rehearsal struct {
	*c1Rehearsal
	gw     *fakeNombaGateway
	fwd    *P2PSettlementForwarder
	sender *ChainReleaseSigner
}

func newC2Rehearsal(t *testing.T) *c2Rehearsal {
	t.Helper()
	r := &c2Rehearsal{c1Rehearsal: newC1Rehearsal(t)}
	r.sender = NewChainReleaseSigner(nil, mustKey(t, os.Getenv("LIQ_C1_RELEASE_PK")), map[int64]ReleaseBroadcaster{97: r.client})
	verifier := NewLogTransferVerifier(r.client.Client)
	sale := NewCompanyWallet(CompanyWalletConfig{Treasury: r.desk.c.Treasury,
		Decimals:  map[string]map[string]int{"bsc": {"USDT": 18}},
		Verifiers: map[string]ERC20SourcedTransferVerifier{"bsc": verifier},
		Probes:    map[string]TreasuryTxProbe{"bsc": r.client.Probe()}, ConversionTreasury: r.desk.c.Treasury.Address()})
	if sale == nil {
		t.Fatal("no sale wallet on the treasury's bsc lane")
	}
	r.desk.c.Sender, r.desk.c.Verifier, r.desk.c.Sale, r.desk.c.LogChunk = r.sender, verifier, sale, 2000
	r.desk.proveWindow = 20 * time.Second // P16: rehearsal-only (laptop RTT); production stays 6 s
	r.trades.SetPoolSlices(r.it, r.sender)
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	r.gw = &fakeNombaGateway{payoutID: "trf_reh"}
	r.fwd = NewP2PSettlementForwarder(r.db, NewPMService(r.db), r.gw, &stubPayoutBalances{ngn: 1_000_000}, "TSA Connect")
	src := NewP2PTradeClaimSource(r.db, NewPMService(r.db), r.trades, r.sender)
	src.SetForwarder(r.fwd)
	r.it.SetPoolForwards(src)
	return r
}

func mustKey(t *testing.T, hexKey string) *ecdsa.PrivateKey {
	t.Helper()
	k, err := crypto.HexToECDSA(strings.TrimPrefix(hexKey, "0x"))
	if err != nil {
		t.Fatal("bad key") // never the key itself
	}
	return k
}

// rehearsalDest is her wallet on the pool chain: a fixed address no key holds.
func rehearsalDest() string {
	return common.BytesToAddress(crypto.Keccak256([]byte("liq-c2-rehearsal-buyer"))[12:]).Hex()
}

// openRehearsalOrder funds every LP to at least 6 free USDT, then opens a
// pool order for naira inside the P16 rehearsal budget (30 s, started after
// the funding so it times the open alone).
func (r *c2Rehearsal) openRehearsalOrder(t *testing.T, naira string) *models.ITIntent {
	t.Helper()
	for _, lp := range r.lps {
		r.fund(t, context.Background(), lp)
		r.capFree(t, context.Background(), lp, usdt18(6))
	}
	user := seedRehearsalBuyer(t, r.db)
	p, err := r.it.QuotePoolBuy(context.Background(), "USDT", naira, "NGN", poolMid, CurrentPoolSettings())
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	r.warmRPC(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // P16
	defer cancel()
	start := time.Now()
	intent, pay, err := r.it.OpenPoolBuy(ctx, user, p, rehearsalDest())
	if err != nil || pay == nil {
		t.Fatalf("open = %v (after %s)", err, time.Since(start))
	}
	t.Logf("OPEN latency %s (rehearsal budget 30 s; prod cap is PoolOpenCap): intent %s units=%s", time.Since(start), intent.ID, intent.AssetAmount)
	for _, sl := range r.liveSlices(t, intent.ID) {
		t.Logf("  slice %s gen=%d units=%s trade=%s nonce=%d open tx=%s %s", sl.ID, sl.Generation, sl.TokenAmount, sl.OnChainTradeID,
			derefNonce(sl.OpenNonce), sl.OpenTxHash, r.txInfo(sl.OpenTxHash))
	}
	return intent
}

// capFree withdraws an LP's free stock above limit, so every order splits the
// same way however earlier phases left the pool (6 + 6 + rest at 6 each).
func (r *c2Rehearsal) capFree(t *testing.T, ctx context.Context, lp c1LP, limit *big.Int) {
	t.Helper()
	bal, err := r.trades.GetPoolBalance(ctx, lp.addr, "USDT", 97)
	if err != nil {
		t.Fatalf("pool balance: %v", err)
	}
	if bal.Available.Cmp(limit) <= 0 {
		return
	}
	amount := new(big.Int).Sub(bal.Available, limit).String()
	raw, err := r.trades.PrepareWithdraw(ctx, lp.pm.UserID, lp.addr, "USDT", amount, 97)
	h := r.send(t, ctx, lp.pk, "withdraw excess", raw, err)
	if _, err := r.trades.ConfirmWithdraw(ctx, lp.pm.UserID, lp.addr, "USDT", amount, h, 97); err != nil {
		t.Fatalf("confirm withdraw: %v", err)
	}
}

func derefNonce(n *int64) int64 {
	if n == nil {
		return -1
	}
	return *n
}

func (r *c2Rehearsal) pay(t *testing.T, intent *models.ITIntent) {
	t.Helper()
	ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "reh_"+intent.ID.String()[:8], intent.AmountMinor, time.Now(), false, PayInCentral)
	if !ok || err != nil {
		t.Fatalf("pay: %v %v", ok, err)
	}
}

func (r *c2Rehearsal) liveSlices(t *testing.T, id uuid.UUID) []models.P2PTrade {
	t.Helper()
	var out []models.P2PTrade
	r.db.Where("it_intent_id = ? AND dropped_at IS NULL", id).Order("generation, created_at").Find(&out)
	return out
}

func (r *c2Rehearsal) allSlices(t *testing.T, id uuid.UUID) []models.P2PTrade {
	t.Helper()
	var out []models.P2PTrade
	r.db.Where("it_intent_id = ?", id).Order("generation, created_at").Find(&out)
	return out
}

// txInfo is a mined tx's block, status and gas, for the report.
func (r *c2Rehearsal) txInfo(hash string) string {
	if hash == "" {
		return "(no tx)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rc, err := r.client.Client.TransactionReceipt(ctx, common.HexToHash(hash))
	if err != nil {
		return "receipt: " + RedactAlertText(err.Error())
	}
	return "block=" + rc.BlockNumber.String() + " status=" + big.NewInt(int64(rc.Status)).String() + " gas=" + big.NewInt(int64(rc.GasUsed)).String()
}

// advanceUntil drives the watcher step (and the reconciler, as the 30 s job
// does in production) until the intent reaches want or the deadline passes.
func (r *c2Rehearsal) advanceUntil(t *testing.T, id uuid.UUID, want string, within time.Duration) models.ITIntent {
	t.Helper()
	ctx := context.Background()
	start := time.Now()
	deadline := start.Add(within)
	var got models.ITIntent
	for time.Now().Before(deadline) {
		r.reconcilePass(t, ctx)
		if err := r.it.AdvancePoolBuy(ctx, id); err != nil {
			t.Logf("advance: %s", RedactAlertText(err.Error()))
		}
		r.db.First(&got, "id = ?", id)
		if got.Status == want {
			t.Logf("intent %s reached %s after %s", id, want, time.Since(start).Round(time.Second))
			return got
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("intent %v stuck at %s (%s), want %s", id, got.Status, got.FailureReason, want)
	return got
}

// forwardOf is a slice's forward row, if any.
func (r *c2Rehearsal) forwardOf(sliceID uuid.UUID) *models.P2PSettlementForward {
	var f models.P2PSettlementForward
	if r.db.Where("trade_id = ?", sliceID).First(&f).Error != nil {
		return nil
	}
	return &f
}

// callsFor counts the gateway calls carrying ref.
func (r *c2Rehearsal) callsFor(ref string) int {
	n := 0
	for _, c := range r.gw.calls {
		if c.Reference == ref {
			n++
		}
	}
	return n
}

// logSettled records every slice's release and the delivery for the report,
// and checks each kept slice is Released on-chain.
func (r *c2Rehearsal) logSettled(t *testing.T, intent models.ITIntent) {
	t.Helper()
	for _, sl := range r.allSlices(t, intent.ID) {
		on, err := r.desk.c.Escrow.ReadTrade(HashID(sl.ID))
		t.Logf("  slice %s gen=%d status=%s dropped=%v units=%s release tx=%s %s on-chain=%v",
			sl.ID, sl.Generation, sl.Status, sl.DroppedAt != nil, sl.TokenAmount, sl.ReleaseTxHash, r.txInfo(sl.ReleaseTxHash), on.Status)
		if err != nil {
			t.Fatalf("read slice %s: %v", sl.ID, err)
		}
		if sl.Status == models.P2PTradeStatusReleased && on.Status != OnChainP2PStatusReleased {
			t.Fatalf("slice %s released in the DB but %v on-chain", sl.ID, on.Status)
		}
		if f := r.forwardOf(sl.ID); f != nil {
			t.Logf("    forward %s status=%s amount=%s ref=%s", f.ID, f.Status, f.Amount, f.Reference)
		}
	}
	t.Logf("  delivery tx=%s %s", intent.TxHash, r.txInfo(intent.TxHash))
	var legs []models.RampLeg
	if err := r.db.Where("order_id = ?", intent.ID).Find(&legs).Error; err != nil {
		t.Fatalf("read ramp legs: %v", err)
	}
	for _, l := range legs {
		t.Logf("  ramp leg %s kind=%s status=%s tx=%s", l.ID, l.Kind, l.Status, l.TxHash)
	}
}

// Phase 1: a forced split, paid exactly, released, proven 5 deep, each LP
// forwarded once, her USDT delivered from the treasury lane 5 deep.
func TestPoolSettleRehearsal_SplitSettles_BSCTestnet(t *testing.T) {
	r := newC2Rehearsal(t)
	intent := r.openRehearsalOrder(t, "20000") // 14.483918 USDT at mid 1348: 6 + 6 + 2.483918
	slices := r.liveSlices(t, intent.ID)
	if len(slices) != 3 {
		t.Fatalf("%d slices, want the forced 3-slice split", len(slices))
	}
	r.pay(t, intent)
	r.advanceUntil(t, intent.ID, models.ITStatusDelivering, 2*time.Minute)
	got := r.advanceUntil(t, intent.ID, models.ITStatusSettled, 8*time.Minute)
	r.logSettled(t, got)
	for _, sl := range slices {
		f := r.forwardOf(sl.ID)
		if f == nil || f.Status != models.P2PForwardSubmitted || r.callsFor(f.Reference) != 1 {
			t.Fatalf("slice %s forward = %+v calls=%d, want one submitted forward", sl.ID, f, r.callsFor(refOf(f)))
		}
	}
	if len(r.gw.calls) != len(slices) {
		t.Fatalf("transfers=%d, want one per slice (%d)", len(r.gw.calls), len(slices))
	}
	t.Log("phase 1 PASS")
}

func refOf(f *models.P2PSettlementForward) string {
	if f == nil {
		return ""
	}
	return f.Reference
}

// F12: one LP's forward is refused by the provider (failed, never retried),
// another times out (pending, started_at set) and the re-drive sends it again
// under the same reference. The order settles regardless.
func TestPoolSettleRehearsal_ForwardFailure_BSCTestnet(t *testing.T) {
	r := newC2Rehearsal(t)
	n := 0
	r.gw.onCall = func() {
		n++
		switch n {
		case 1:
			r.gw.err = &ProviderRejectedError{Err: errors.New("invalid account")}
		case 2:
			r.gw.err = errors.New("timeout")
		default:
			r.gw.err = nil
		}
	}
	intent := r.openRehearsalOrder(t, "20000")
	slices := r.liveSlices(t, intent.ID)
	if len(slices) < 2 {
		t.Fatalf("%d slices, want at least two forwards", len(slices))
	}
	r.pay(t, intent)
	got := r.advanceUntil(t, intent.ID, models.ITStatusSettled, 8*time.Minute)
	r.logSettled(t, got)
	if len(r.gw.calls) < 2 {
		t.Fatalf("gateway calls = %d, want at least 2", len(r.gw.calls))
	}
	refRejected, refTimeout := r.gw.calls[0].Reference, r.gw.calls[1].Reference
	var rejected, timedOut models.P2PSettlementForward
	r.db.Where("reference = ?", refRejected).First(&rejected)
	r.db.Where("reference = ?", refTimeout).First(&timedOut)
	if rejected.Status != models.P2PForwardFailed {
		t.Fatalf("rejected forward = %s, want failed", rejected.Status)
	}
	if timedOut.Status != models.P2PForwardPending || timedOut.StartedAt == nil {
		t.Fatalf("timed-out forward = %s started_at=%v, want pending with started_at", timedOut.Status, timedOut.StartedAt)
	}
	// The re-drive waits p2pForwardRedriveAfter (2 min): age the row past it.
	if err := r.db.Exec(`UPDATE p2p_settlement_forwards SET created_at = created_at - interval '3 minutes' WHERE id = ?`, timedOut.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.fwd.RedrivePending(context.Background()); err != nil {
		t.Fatalf("redrive: %v", err)
	}
	r.db.First(&timedOut, "id = ?", timedOut.ID)
	r.db.First(&rejected, "id = ?", rejected.ID)
	if timedOut.Status != models.P2PForwardSubmitted || r.callsFor(refTimeout) != 2 {
		t.Fatalf("after redrive: %s calls=%d, want submitted under the same reference twice", timedOut.Status, r.callsFor(refTimeout))
	}
	if rejected.Status != models.P2PForwardFailed || r.callsFor(refRejected) != 1 {
		t.Fatalf("rejected forward re-sent: %s calls=%d", rejected.Status, r.callsFor(refRejected))
	}
	t.Logf("F12 PASS: rejected %s failed once; timed-out %s re-driven under the same reference", refRejected, refTimeout)
}

func partialIDFile() string {
	if f := os.Getenv("LIQ_C2_PARTIAL_ID_FILE"); f != "" {
		return f
	}
	return "/Users/ojay/Projects/tsaconnect/.liq-sdd/c2/partial.id"
}

// F6 setup: a split order, paid; the first slice released by the release key
// directly, the rest left Pending until the payment window passes.
func TestPoolSettleRehearsal_PartialReleaseSetup_BSCTestnet(t *testing.T) {
	r := newC2Rehearsal(t)
	intent := r.openRehearsalOrder(t, "10000") // ~7.24 USDT: two slices at 6 free each
	slices := r.liveSlices(t, intent.ID)
	if len(slices) < 2 {
		t.Fatalf("%d slices, want a split", len(slices))
	}
	r.pay(t, intent)
	rel, err := r.trades.AutoRelease(context.Background(), slices[0].ID, r.sender)
	if err != nil {
		t.Fatalf("release slice 1: %v", err)
	}
	if _, err := r.client.WaitForReceipt(rel.ReleaseTxHash, 90*time.Second); err != nil {
		t.Fatalf("release receipt: %v", err)
	}
	t.Logf("slice 1 %s released tx=%s %s", slices[0].ID, rel.ReleaseTxHash, r.txInfo(rel.ReleaseTxHash))
	var due time.Time
	for _, sl := range slices[1:] {
		on, err := r.desk.c.Escrow.ReadTrade(HashID(sl.ID))
		if err != nil || on.Status != OnChainP2PStatusPending {
			t.Fatalf("slice %s on-chain = %+v %v, want Pending", sl.ID, on, err)
		}
		if d := time.Unix(on.CreatedAt.Int64(), 0).Add(rehearsalPaymentWindow); d.After(due) {
			due = d
		}
	}
	if err := os.WriteFile(partialIDFile(), []byte(intent.ID.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("F6 setup PASS: intent %s; cancel due (chain time) %s", intent.ID, due.UTC().Format(time.RFC3339))
}

// F6: the unreleased slices are cancelled by their own LPs (cancelByTimeout is
// permissionless), the watcher parks the order pool_slices_short, the refund
// is refused while it is owed, re-delivery opens the owed units only and the
// order settles; each kept slice's LP is forwarded once, a cancelled slice never.
func TestPoolSettleRehearsal_PartialReleaseRedelivers_BSCTestnet(t *testing.T) {
	r := newC2Rehearsal(t)
	ctx := context.Background()
	raw, err := os.ReadFile(partialIDFile())
	if err != nil {
		t.Fatalf("no setup intent: %v", err)
	}
	id := uuid.MustParse(strings.TrimSpace(string(raw)))
	slices := r.liveSlices(t, id)
	head, err := r.client.Client.HeaderByNumber(ctx, nil)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	chainNow := time.Unix(int64(head.Time), 0)
	lpByPM := map[uuid.UUID]c1LP{}
	for _, lp := range r.lps {
		lpByPM[lp.pm.ID] = lp
	}
	// The setup released one slice (it carries the hash); slices of one open
	// share created_at, so never rely on their order.
	var releasedID uuid.UUID
	for _, sl := range slices {
		if sl.ReleaseTxHash != "" {
			releasedID = sl.ID
		}
	}
	if releasedID == uuid.Nil {
		t.Fatal("setup released no slice")
	}
	cancelled := map[uuid.UUID]bool{}
	for _, sl := range slices {
		if sl.ID == releasedID {
			continue
		}
		on, err := r.desk.c.Escrow.ReadTrade(HashID(sl.ID))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if on.Status != OnChainP2PStatusPending {
			continue
		}
		if due := time.Unix(on.CreatedAt.Int64(), 0).Add(rehearsalPaymentWindow); chainNow.Before(due) {
			t.Fatalf("slice %s not due until %s (chain now %s)", sl.ID, due.UTC().Format(time.RFC3339), chainNow.UTC().Format(time.RFC3339))
		}
		var ad models.P2PAd
		r.db.First(&ad, "id = ?", sl.AdID)
		lp, ok := lpByPM[*ad.PMID]
		if !ok {
			t.Fatalf("slice %s LP is not a rehearsal LP", sl.ID)
		}
		raw, err := r.desk.c.Escrow.PrepareCancelByTimeout(HashID(sl.ID), lp.addr)
		h := r.send(t, ctx, lp.pk, "cancelByTimeout (LP's own key)", raw, err)
		t.Logf("  slice %s cancel tx=%s %s", sl.ID, h, r.txInfo(h))
		cancelled[sl.ID] = true
	}
	if len(cancelled) == 0 {
		t.Fatal("no Pending slice left to cancel")
	}
	parked := r.advanceUntil(t, id, models.ITStatusBuyAwaitingRate, 3*time.Minute)
	if !strings.HasPrefix(parked.FailureReason, models.ITFailurePoolSlicesShort) {
		t.Fatalf("parked with %q, want %s", parked.FailureReason, models.ITFailurePoolSlicesShort)
	}
	if _, err := r.it.MarkStableBuyRefunded(ctx, uuid.New(), id, "rehearsal: must be refused"); err == nil {
		t.Fatal("refund of a part-released order was accepted")
	}
	// The released slice must be proven 5 deep before re-delivery counts it.
	var red *models.ITIntent
	deadline := time.Now().Add(3 * time.Minute)
	for {
		r.reconcilePass(t, ctx)
		rctx, cancel := context.WithTimeout(ctx, 60*time.Second) // P16: re-delivery opens and proves like an open
		start := time.Now()
		red, err = r.it.RedeliverPoolBuy(rctx, uuid.New(), id, "rehearsal re-delivery after a lost slice")
		cancel()
		if err == nil {
			t.Logf("RE-DELIVER latency %s: status %s", time.Since(start), red.Status)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("redeliver: %v", err)
		}
		t.Logf("redeliver: %s (retrying)", RedactAlertText(err.Error()))
		time.Sleep(5 * time.Second)
	}
	for _, sl := range r.allSlices(t, id) {
		if sl.Generation > 1 {
			t.Logf("  gen-2 slice %s units=%s trade=%s open tx=%s %s", sl.ID, sl.TokenAmount, sl.OnChainTradeID, sl.OpenTxHash, r.txInfo(sl.OpenTxHash))
		}
	}
	got := r.advanceUntil(t, id, models.ITStatusSettled, 8*time.Minute)
	r.logSettled(t, got)
	gen2 := 0
	for _, sl := range r.allSlices(t, id) {
		f := r.forwardOf(sl.ID)
		switch {
		case cancelled[sl.ID]:
			if f != nil {
				t.Fatalf("cancelled slice %s was forwarded", sl.ID)
			}
		case sl.ID == releasedID || (sl.Generation > 1 && sl.DroppedAt == nil):
			if sl.Generation > 1 {
				gen2++
			}
			if f == nil || f.Status != models.P2PForwardSubmitted || r.callsFor(f.Reference) != 1 {
				t.Fatalf("slice %s forward = %+v, want one submitted forward", sl.ID, f)
			}
		}
	}
	if gen2 == 0 {
		t.Fatal("no generation-2 slice")
	}
	t.Log("F6 PASS")
}
