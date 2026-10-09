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

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// submitted is a live refund of amount whose exact transfer the owner signed
// and the server recorded (and broadcast once).
func (r *refundRig) submitted(t *testing.T, ref, amount string) (*models.PayRefund, *types.Transaction) {
	t.Helper()
	req := r.requested(t, ref, amount)
	raw, tx := signRefundTx(t, r.exactRefundTx(req))
	got, err := r.s.Submit(context.Background(), r.m, req.ID, r.ownerWallet, PayRefundSubmit{ChainID: 56, TokenSymbol: "USDT", SignedTx: raw})
	if err != nil {
		t.Fatal(err)
	}
	return got, tx
}

// chainShows makes the verifier read a mined transfer of credited from the
// owner, confirmations deep.
func (r *refundRig) chainShows(credited *big.Int, confirmations uint64) {
	r.ver.set(&StablePayment{Credited: credited, Sender: r.ownerWallet, BlockTime: payTestNow, Confirmations: confirmations}, nil, true)
}

// chainShowsReverted makes the verifier read a reverted receipt,
// confirmations deep.
func (r *refundRig) chainShowsReverted(confirmations uint64) {
	r.ver.set(&StablePayment{Credited: new(big.Int), Confirmations: confirmations}, fmt.Errorf("%w: 0xabc", ErrTransferReverted), true)
}

func (r *refundRig) status(t *testing.T, id uuid.UUID) *models.PayRefund {
	t.Helper()
	got, err := r.s.Get(context.Background(), r.m.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPayRefundConfirm_FullCreditDeepEnoughConfirmsWithWebhookAndNotices(t *testing.T) {
	r := newRefundRig(t)
	ref, tx := r.submitted(t, "CONF-1", "12.50")
	r.chainShows(bscAmount(12_500_000), 5)
	if err := r.s.ConfirmDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := r.status(t, ref.ID)
	if got.Status != models.PayRefundConfirmed || got.CreditedMicroUSD != 12_500_000 || got.ConfirmedAt == nil {
		t.Fatalf("refund %+v", got)
	}
	hooks := r.webhooks(t, models.PayEventRefunded)
	if len(hooks) != 1 {
		t.Fatalf("hooks %d", len(hooks))
	}
	refund := hooks[0]["refund"].(map[string]any)
	data := hooks[0]["data"].(map[string]any)
	if refund["id"] != ref.ID.String() || refund["amount"] != "12.50" || refund["token"] != "USDT" || refund["chainId"] != float64(56) ||
		refund["txHash"] != tx.Hash().Hex() || refund["confirmedAt"] == nil || data["refundStatus"] != "partial" || data["refundedAmount"] != "12.50" {
		t.Fatalf("body %+v", hooks[0])
	}
	received, confirmed := r.alertsOf(events.PayRefundReceived), r.alertsOf(events.PayRefundConfirmed)
	if len(received) != 1 || received[0].UserID != r.payer || received[0].Message != "You received a 12.50 USDT refund from Sky Air" {
		t.Fatalf("payer notice %+v", received)
	}
	if len(confirmed) != 1 || confirmed[0].Message != "Refund to Ada confirmed" {
		t.Fatalf("owner notice %+v", confirmed)
	}
}

// The confirmer proves the transfer of the refund's own token contract, from
// the owner's wallet to the payer's — the Transfer log must be emitted by
// token_address.
func TestPayRefundConfirm_ProvesTheRefundsOwnTokenAndWallets(t *testing.T) {
	r := newRefundRig(t)
	ref, tx := r.submitted(t, "CONF-18", "12.50")
	r.chainShows(bscAmount(12_500_000), 9)
	r.s.ConfirmDue(context.Background())
	calls := r.ver.verified
	want := [5]string{"bsc", tx.Hash().Hex(), refundBSCUSDT, r.ownerWallet, ref.ToAddress}
	if len(calls) == 0 || calls[len(calls)-1] != want {
		t.Fatalf("verified %v, want %v", calls, want)
	}
}

// Refunds pass no NotBefore: the server decoded and recorded the transaction
// itself, so when it was mined proves nothing about whether it is this refund.
func TestPayRefundConfirm_TheBlockTimeIsNotHeldAgainstIt(t *testing.T) {
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-19", "12.50")
	r.ver.set(&StablePayment{Credited: bscAmount(12_500_000), Sender: r.ownerWallet, BlockTime: payTestNow.Add(-48 * time.Hour), Confirmations: 9}, nil, true)
	r.s.ConfirmDue(context.Background())
	if got := r.status(t, ref.ID); got.Status != models.PayRefundConfirmed {
		t.Fatalf("%+v", got)
	}
}

func TestPayRefundConfirm_NotDeepEnoughWaits(t *testing.T) {
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-2", "12.50")
	r.chainShows(bscAmount(12_500_000), 4)
	if err := r.s.ConfirmDue(context.Background()); err != nil || r.status(t, ref.ID).Status != models.PayRefundSubmitted {
		t.Fatalf("err %v", err)
	}
}

func TestPayRefundConfirm_OverCreditStillConfirms(t *testing.T) {
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-3", "12.50")
	logged := captureLog(t)
	r.chainShows(new(big.Int).Add(bscAmount(12_500_000), big.NewInt(1)), 9)
	r.s.ConfirmDue(context.Background())
	if got := r.status(t, ref.ID); got.Status != models.PayRefundConfirmed || got.CreditedMicroUSD != 12_500_000 {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(logged.String(), "ALERT pay_refund_over_credited=1") {
		t.Fatalf("log %q", logged.String())
	}
}

// Credited, but less than the refund: money moved, so never failed — a person looks.
func TestPayRefundConfirm_PartialCreditNeedsReview(t *testing.T) {
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-4", "12.50")
	logged := captureLog(t)
	r.chainShows(bscAmount(12_000_000), 9)
	r.s.ConfirmDue(context.Background())
	got := r.status(t, ref.ID)
	if got.Status != models.PayRefundNeedsReview || got.CreditedMicroUSD != 12_000_000 || got.FailureReason != "Credited 12.00 of 12.50" {
		t.Fatalf("%+v", got)
	}
	if len(r.webhooks(t, models.PayEventRefunded)) != 0 || len(r.alertsOf(events.PayRefundNeedsReview)) != 1 {
		t.Fatal("needs_review must alert the owner and send no refunded webhook")
	}
	if !strings.Contains(logged.String(), "ALERT pay_refund_needs_review=1") {
		t.Fatalf("log %q", logged.String())
	}
}

func TestPayRefundConfirm_RevertedFailsAndFreesTheCap(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-5", "34")
	r.chainShowsReverted(5)
	r.s.ConfirmDue(ctx)
	got := r.status(t, ref.ID)
	if got.Status != models.PayRefundFailed || got.FailureReason != "The transaction failed on-chain" || len(r.alertsOf(events.PayRefundFailed)) != 1 {
		t.Fatalf("%+v", got)
	}
	if _, err := r.s.Request(ctx, r.m, models.PayModeLive, got.PaymentID, PayRefundRequest{Amount: "34", Via: models.PayRefundViaApp}); err != nil {
		t.Fatalf("the failed amount is not free again: %v", err)
	}
}

// A revert a reorg could still undo, or one whose depth can't be read, is not
// yet proof the customer wasn't paid.
func TestPayRefundConfirm_AShallowOrUndatedRevertWaits(t *testing.T) {
	for name, read := range map[string]func(r *refundRig){
		"shallow": func(r *refundRig) { r.chainShowsReverted(4) },
		"undated": func(r *refundRig) { r.ver.set(nil, fmt.Errorf("%w: 0xabc", ErrTransferReverted), true) },
	} {
		r := newRefundRig(t)
		ref, _ := r.submitted(t, "CONF-20", "1")
		read(r)
		if err := r.s.ConfirmDue(context.Background()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := r.status(t, ref.ID); got.Status != models.PayRefundSubmitted || len(r.alertsOf(events.PayRefundFailed)) != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestPayRefundConfirm_DeepAndNothingMovedFails(t *testing.T) {
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-6", "1")
	r.ver.set(&StablePayment{Credited: new(big.Int), Sender: r.ownerWallet, BlockTime: payTestNow, Confirmations: 9},
		fmt.Errorf("%w: tx", ErrTransferNoMatch), true)
	r.s.ConfirmDue(context.Background())
	if got := r.status(t, ref.ID); got.Status != models.PayRefundFailed || got.FailureReason != "The transaction didn't pay the customer" {
		t.Fatalf("%+v", got)
	}
}

func TestPayRefundConfirm_ShallowAndNothingMovedWaits(t *testing.T) {
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-21", "1")
	r.ver.set(&StablePayment{Credited: new(big.Int), Sender: r.ownerWallet, BlockTime: payTestNow, Confirmations: 4},
		fmt.Errorf("%w: tx", ErrTransferNoMatch), true)
	r.s.ConfirmDue(context.Background())
	if got := r.status(t, ref.ID); got.Status != models.PayRefundSubmitted {
		t.Fatalf("%+v", got)
	}
}

// P2: failed only when proven unpaid. Any other refuting reading — a transfer
// that credited someone else, a transaction another wallet signed — is odd
// enough that a person looks; its amount stays held.
func TestPayRefundConfirm_AnyOtherRefutedReadingNeedsReview(t *testing.T) {
	ctx := context.Background()
	for name, read := range map[string]func(r *refundRig){
		"wrong recipient": func(r *refundRig) { r.ver.set(nil, fmt.Errorf("%w: 0xabc", ErrTransferWrongRecipient), true) },
		"wrong sender": func(r *refundRig) {
			r.ver.set(&StablePayment{Credited: bscAmount(1_000_000), Sender: refundFundingWallet, BlockTime: payTestNow, Confirmations: 9}, nil, true)
		},
	} {
		r := newRefundRig(t)
		ref, _ := r.submitted(t, "CONF-22", "1")
		logged := captureLog(t)
		read(r)
		r.s.ConfirmDue(ctx)
		got := r.status(t, ref.ID)
		if got.Status != models.PayRefundNeedsReview || got.FailureReason != payRefundUnexpectedReason || got.CreditedMicroUSD != 0 {
			t.Errorf("%s: %+v", name, got)
		}
		review := r.alertsOf(events.PayRefundNeedsReview)
		if len(review) != 1 || strings.Contains(review[0].Message, "replaced") || len(r.alertsOf(events.PayRefundFailed)) != 0 {
			t.Errorf("%s: notices %+v", name, r.alerts())
		}
		if !strings.Contains(logged.String(), "ALERT pay_refund_unexpected_reading=1") {
			t.Errorf("%s: log %q", name, logged.String())
		}
		if _, err := r.s.Request(ctx, r.m, models.PayModeLive, got.PaymentID, PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp}); !errors.Is(err, ErrPayRefundOpen) {
			t.Errorf("%s: the cap was released: %v", name, err)
		}
	}
}

// The node has never seen it: send the recorded transaction again.
func TestPayRefundConfirm_UnknownTransactionIsBroadcastAgain(t *testing.T) {
	r := newRefundRig(t)
	ref, tx := r.submitted(t, "CONF-7", "1")
	r.ver.set(nil, errors.New("verify: not found"), false)
	r.net.nonce = 0
	if err := r.s.ConfirmDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	sent := r.net.broadcasts()
	if r.status(t, ref.ID).Status != models.PayRefundSubmitted || len(sent) != 2 || sent[1].Hash() != tx.Hash() {
		t.Fatalf("broadcasts %d", len(sent))
	}
}

// Another transaction took its nonce, deep enough, and the chain still doesn't
// know ours. Ours can never mine — but the one that replaced it may have paid
// the customer (a speed-up from another wallet app), or the node pruned ours.
// Never failed: parked for a person, its amount still held (review I4).
func TestPayRefundConfirm_ReplacedByAnotherTransactionNeedsReview(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-8", "1")
	logged := captureLog(t)
	r.ver.set(nil, errors.New("verify: not found"), false)
	r.net.nonce = 1
	r.s.ConfirmDue(ctx)
	got := r.status(t, ref.ID)
	if got.Status != models.PayRefundNeedsReview || got.FailureReason != payRefundReplacedReason || got.CreditedMicroUSD != 0 {
		t.Fatalf("%+v", got)
	}
	if len(r.net.broadcasts()) != 1 {
		t.Fatal("a replaced transaction was sent again")
	}
	if depths := r.net.nonceDepths; depths[len(depths)-1] != 5 {
		t.Fatalf("the nonce was read %d deep, want MinConf (5)", depths[len(depths)-1])
	}
	review := r.alertsOf(events.PayRefundNeedsReview)
	if len(review) != 1 || !strings.Contains(review[0].Message, "replaced by another transaction") || len(r.alertsOf(events.PayRefundFailed)) != 0 {
		t.Fatalf("notices %+v", r.alerts())
	}
	if !strings.Contains(logged.String(), "ALERT pay_refund_replaced=1") {
		t.Fatalf("log %q", logged.String())
	}
	if _, err := r.s.Request(ctx, r.m, models.PayModeLive, got.PaymentID, PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp}); !errors.Is(err, ErrPayRefundOpen) {
		t.Fatalf("a replaced refund must keep the payment's refund open: %v", err)
	}
}

// Review focus 2: a lagging node said "unknown", but asked again the chain has
// it — it is ours that mined. Never failed.
func TestPayRefundConfirm_NonceMovedButOurTransactionIsKnownOnRecheckWaits(t *testing.T) {
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-9", "1")
	r.ver.set(nil, errors.New("verify: not found"), false)
	r.ver.knownAnswers = []bool{false, true}
	r.net.nonce = 1
	r.s.ConfirmDue(context.Background())
	if got := r.status(t, ref.ID); got.Status != models.PayRefundSubmitted {
		t.Fatalf("%+v", got)
	}
}

// A chain it can't read is a worker failure (the streak alert), never a
// verdict — and what the RPC said never reaches the log or the error unredacted.
func TestPayRefundConfirm_AnUnreadableChainIsAnErrorAndChangesNothing(t *testing.T) {
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-10", "1")
	logged := captureLog(t)
	r.ver.set(nil, errors.New("verify: dial tcp: i/o timeout"), false)
	r.ver.knownErr = errors.New(`Post "https://bsc.example-rpc.io/v2/SECRETKEY": dial tcp: i/o timeout`)
	err := r.s.ConfirmDue(context.Background())
	if err == nil {
		t.Fatal("an unreadable chain must fail the tick")
	}
	if strings.Contains(err.Error(), "SECRETKEY") || strings.Contains(logged.String(), "SECRETKEY") {
		t.Fatalf("err %q log %q", err, logged.String())
	}
	if got := r.status(t, ref.ID); got.Status != models.PayRefundSubmitted {
		t.Fatalf("%+v", got)
	}
}

// Still unproven an hour after it was sent: ALERTed once, never failed.
func TestPayRefundConfirm_UnconfirmedAfterAnHourAlertsOnce(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	old, _ := r.submitted(t, "CONF-23", "1")
	fresh, _ := r.submitted(t, "CONF-24", "2") // another amount: another transaction
	earlier := payTestNow.Add(-payRefundUnconfirmedAfter - time.Minute)
	r.db.Model(&models.PayRefund{}).Where("id = ?", old.ID).Update("submitted_at", &earlier)
	logged := captureLog(t)
	r.s.ConfirmDue(ctx)
	r.s.ConfirmDue(ctx)
	if n := strings.Count(logged.String(), "ALERT pay_refund_unconfirmed=1"); n != 1 || !strings.Contains(logged.String(), old.ID.String()) {
		t.Fatalf("%d alerts: %q", n, logged.String())
	}
	if r.status(t, old.ID).Status != models.PayRefundSubmitted || r.status(t, fresh.ID).Status != models.PayRefundSubmitted {
		t.Fatal("an unconfirmed refund moved")
	}
}

// D25: two confirmers on one refund — one claim, one webhook, one set of
// notices. On SQLite and on PostgreSQL, where the confirmers really run in
// parallel (skips without DATABASE_URL).
func TestPayRefundConfirm_TwoConfirmersClaimItOnce(t *testing.T) {
	testTwoConfirmersClaimOnce(t, newRefundRig(t))
}

func TestPayRefundConfirm_TwoConfirmersClaimItOnceOnPostgres(t *testing.T) {
	testTwoConfirmersClaimOnce(t, newRefundPostgresRig(t))
}

func testTwoConfirmersClaimOnce(t *testing.T, r *refundRig) {
	t.Helper()
	ref, _ := r.submitted(t, "CONF-11", "12.50")
	r.chainShows(bscAmount(12_500_000), 9)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.s.ConfirmDue(context.Background()) }()
	}
	wg.Wait()
	if r.status(t, ref.ID).Status != models.PayRefundConfirmed || len(r.webhooks(t, models.PayEventRefunded)) != 1 ||
		len(r.alertsOf(events.PayRefundReceived)) != 1 || len(r.alertsOf(events.PayRefundConfirmed)) != 1 {
		t.Fatalf("confirmed more than once: %d webhooks, %d payer, %d owner notices", len(r.webhooks(t, models.PayEventRefunded)),
			len(r.alertsOf(events.PayRefundReceived)), len(r.alertsOf(events.PayRefundConfirmed)))
	}
}

// Review C3: once a refund is in needs_review, money moved (or may have). A
// later reading that says otherwise — reverted, deep and nothing moved, or
// replaced — never fails it, so its amount is never released for a second
// refund that could pay twice. Support is ALERTed of the contradiction.
func TestPayRefundReevaluate_AContradictedReviewStaysInReview(t *testing.T) {
	ctx := context.Background()
	for name, read := range map[string]func(r *refundRig){
		"reverted": func(r *refundRig) { r.chainShowsReverted(9) },
		"deep and nothing moved": func(r *refundRig) {
			r.ver.set(&StablePayment{Credited: new(big.Int), Sender: r.ownerWallet, BlockTime: payTestNow, Confirmations: 9},
				fmt.Errorf("%w: tx", ErrTransferNoMatch), true)
		},
		"replaced":        func(r *refundRig) { r.ver.set(nil, errors.New("verify: not found"), false); r.net.nonce = 1 },
		"wrong recipient": func(r *refundRig) { r.ver.set(nil, fmt.Errorf("%w: 0xabc", ErrTransferWrongRecipient), true) },
	} {
		r := newRefundRig(t)
		ref, _ := r.submitted(t, "CONF-15", "12.50")
		r.chainShows(bscAmount(12_000_000), 9)
		r.s.ConfirmDue(ctx)
		if r.status(t, ref.ID).Status != models.PayRefundNeedsReview {
			t.Fatalf("%s: setup: needs_review", name)
		}
		logged := captureLog(t)
		read(r)
		got, err := r.s.Reevaluate(ctx, ref.ID)
		if err != nil || got.Status != models.PayRefundNeedsReview || got.CreditedMicroUSD != 12_000_000 || got.FailureReason != "Credited 12.00 of 12.50" {
			t.Errorf("%s: %+v %v", name, got, err)
		}
		if len(r.alertsOf(events.PayRefundFailed)) != 0 || len(r.alertsOf(events.PayRefundNeedsReview)) != 1 {
			t.Errorf("%s: the owner was told again: %+v", name, r.alerts())
		}
		if !strings.Contains(logged.String(), "ALERT pay_refund_review_contradicted=1") {
			t.Errorf("%s: log %q", name, logged.String())
		}
		if _, err := r.s.Request(ctx, r.m, models.PayModeLive, got.PaymentID, PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp}); !errors.Is(err, ErrPayRefundOpen) {
			t.Errorf("%s: the cap was released: %v", name, err)
		}
	}
}

// Review I5: refunds stuck on a chain the confirmer can't read rotate to the
// back; they never hide a newer refund on a healthy chain.
func TestPayRefundConfirm_RefundsOnAnUnreadableChainDoNotStarveTheRest(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	earlier := payTestNow.Add(-2 * time.Hour)
	for i := 0; i < payRefundConfirmBatch; i++ {
		hash := fmt.Sprintf("0x%064x", i+1)
		stuck := &models.PayRefund{PaymentID: uuid.New(), MerchantID: r.m.ID, Mode: models.PayModeLive, AmountMicroUSD: 1_000_000,
			CapMicroUSD: 33_000_000, ToAddress: refundPayerWallet, FromAddress: r.ownerWallet, ChainID: 1, TokenSymbol: "USDT",
			TokenAddress: refundBSCUSDT, TxHash: &hash, RawTx: "0x00", Status: models.PayRefundSubmitted,
			RequestedVia: models.PayRefundViaApp, CreatedAt: earlier, SubmittedAt: &earlier}
		if err := r.db.Create(stuck).Error; err != nil {
			t.Fatal(err)
		}
	}
	ref, _ := r.submitted(t, "CONF-16", "12.50") // the newest; chain 1 has no client
	r.chainShows(bscAmount(12_500_000), 9)
	if err := r.s.ConfirmDue(ctx); err == nil {
		t.Fatal("tick 1: the unreadable chain must fail the tick")
	}
	if r.status(t, ref.ID).Status != models.PayRefundSubmitted {
		t.Fatal("tick 1 should only have reached the 50 oldest")
	}
	r.s.ConfirmDue(ctx)
	if got := r.status(t, ref.ID); got.Status != models.PayRefundConfirmed {
		t.Fatalf("tick 2: the newest refund was starved: %+v", got)
	}
}

// Review I6: a rebroadcast the network refuses records why; one that goes
// through clears it. The refund stays submitted throughout.
func TestPayRefundConfirm_ARebroadcastKeepsTheLastErrorUntilOneGoesThrough(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	r.net.broadcastErr = errors.New("insufficient funds for gas * price + value")
	ref, _ := r.submitted(t, "CONF-17", "1")
	r.ver.set(nil, errors.New("verify: not found"), false)
	r.net.nonce = 0
	if err := r.s.ConfirmDue(ctx); err != nil {
		t.Fatal(err)
	}
	got := r.status(t, ref.ID)
	if got.Status != models.PayRefundSubmitted || got.LastBroadcastError != "Not enough gas in your wallet" || len(r.net.broadcasts()) != 2 {
		t.Fatalf("refused: %+v (%d sends)", got, len(r.net.broadcasts()))
	}
	r.net.broadcastErr = nil // the owner topped up gas
	r.s.ConfirmDue(ctx)
	got = r.status(t, ref.ID)
	if got.Status != models.PayRefundSubmitted || got.LastBroadcastError != "" || got.LastBroadcastAt == nil || len(r.net.broadcasts()) != 3 {
		t.Fatalf("accepted: %+v", got)
	}
}

func TestPayRefundReevaluate_RunsTheCheckNowOnOpenRefundsOnly(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	ref, _ := r.submitted(t, "CONF-12", "12.50")
	r.chainShows(bscAmount(12_000_000), 9)
	r.s.ConfirmDue(ctx)
	if r.status(t, ref.ID).Status != models.PayRefundNeedsReview {
		t.Fatal("setup: needs_review")
	}
	// Support finds the full credit (say the node had mis-read it): re-check closes it.
	r.chainShows(bscAmount(12_500_000), 9)
	got, err := r.s.Reevaluate(ctx, ref.ID)
	if err != nil || got.Status != models.PayRefundConfirmed || got.FailureReason != "" || len(r.webhooks(t, models.PayEventRefunded)) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	open := r.requested(t, "CONF-13", "1")
	if _, err := r.s.Reevaluate(ctx, open.ID); !errors.Is(err, ErrPayRefundState) {
		t.Fatalf("requested: %v", err)
	}
	if _, err := r.s.Reevaluate(ctx, uuid.New()); !errors.Is(err, ErrPayRefundNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestPayRefundSubmit_TriesTheConfirmationOnce(t *testing.T) {
	r := newRefundRig(t)
	r.chainShows(bscAmount(12_500_000), 9)
	ref, _ := r.submitted(t, "CONF-14", "12.50")
	if ref.Status != models.PayRefundConfirmed {
		t.Fatalf("%+v", ref)
	}
}

// D25 / C3 under a race: a confirmer holding a stale copy of the refund (read
// as submitted, since confirmed by another) can neither fail it nor park it,
// and tells nobody anything.
func TestPayRefundConfirm_AStaleReadingCannotMoveASettledRefund(t *testing.T) {
	ctx := context.Background()
	for name, move := range map[string]func(r *refundRig, stale *models.PayRefund) error{
		"failed": func(r *refundRig, stale *models.PayRefund) error {
			return r.s.markFailed(ctx, stale, payRefundRevertedReason)
		},
		"needs_review": func(r *refundRig, stale *models.PayRefund) error {
			return r.s.markNeedsReview(ctx, stale, 1, "Credited 0.000001 of 12.50", "pay_refund_needs_review")
		},
	} {
		r := newRefundRig(t)
		stale, _ := r.submitted(t, "CONF-25", "12.50")
		r.chainShows(bscAmount(12_500_000), 9)
		r.s.ConfirmDue(ctx)
		before := len(r.alerts())
		if err := move(r, stale); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := r.status(t, stale.ID); got.Status != models.PayRefundConfirmed || got.CreditedMicroUSD != 12_500_000 || got.FailureReason != "" {
			t.Errorf("%s: a stale reading moved a confirmed refund: %+v", name, got)
		}
		if len(r.alerts()) != before {
			t.Errorf("%s: the owner was told: %+v", name, r.alerts()[before:])
		}
	}
}
