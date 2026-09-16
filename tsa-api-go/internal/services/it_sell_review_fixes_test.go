package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ─── C1: reverse and payout must never both happen ─────────────────────────

// Scenario A: the admin reverse broadcasts and the reverse mines, but its
// receipt wait times out. The row must not be left sendable.
func TestC1_ReverseVerifyTimeoutThenReconcilerAndRetrySendNothing(t *testing.T) {
	for _, attempts := range []int{0, 1} {
		f := newSellFixture(t)
		f.s.SetFWAbsenceTrusted(true)
		in := f.seedSell(t, func(in *models.ITIntent) {
			in.PayoutAttempts = attempts
			if attempts > 0 {
				in.FailureReason = "auto-payout unknown: timeout"
			}
		})
		f.age(t, in.ID, time.Hour)
		f.chain.verifyErr["PremiumDebitReversed"] = errors.New("get receipt: timeout")

		if _, err := f.s.AdminReverseSell(context.Background(), in.ID); err == nil {
			t.Fatalf("attempts=%d: want the verify failure surfaced", attempts)
		}
		f.age(t, in.ID, time.Hour)
		f.reconcile(t)
		_, _ = f.s.RetrySellPayout(context.Background(), in.ID)

		got := f.reload(t, in.ID)
		if len(f.gw.calls) != 0 {
			t.Fatalf("attempts=%d: %d payout(s) sent after a reverse that mined — the user is paid twice", attempts, len(f.gw.calls))
		}
		if got.Status != models.ITStatusNeedsReview {
			t.Fatalf("attempts=%d: status = %s, want needs_review (debit resolved on-chain, outcome unrecorded)", attempts, got.Status)
		}
		if f.chain.reverseCalls != 1 {
			t.Fatalf("attempts=%d: reverse broadcast %d times, want once", attempts, f.chain.reverseCalls)
		}
	}
}

// The reverse is broadcast but not yet mined when verification gives up: the
// row stays claimed for the reverse and is never sent.
func TestC1_UnminedReverseKeepsRowClaimedForReverse(t *testing.T) {
	f := newSellFixture(t)
	f.s.SetFWAbsenceTrusted(true)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
	f.age(t, in.ID, time.Hour)
	f.chain.holdResolves = true
	f.chain.verifyErr["PremiumDebitReversed"] = errors.New("get receipt: timeout")

	_, _ = f.s.AdminReverseSell(context.Background(), in.ID)
	got := f.reload(t, in.ID)
	if got.Status != models.ITStatusDebited || !got.PayoutRejected {
		t.Fatalf("status=%s rejected=%v, want debited and claimed for the reverse", got.Status, got.PayoutRejected)
	}
	f.age(t, in.ID, time.Hour)
	f.reconcile(t)
	if _, err := f.s.RetrySellPayout(context.Background(), in.ID); err == nil {
		t.Fatal("retry-payout must refuse a row claimed for reversal")
	}
	if len(f.gw.calls) != 0 {
		t.Fatalf("%d payout(s) sent for a row claimed for reversal", len(f.gw.calls))
	}
}

// Scenario B, send first: an admin reverse arriving while a payout request is
// in flight must lose.
func TestC1_ReverseLosesToInFlightSend(t *testing.T) {
	f := newSellFixture(t)
	f.s.SetFWAbsenceTrusted(true)
	in := f.seedSell(t, nil)
	var adminErr, directErr error
	f.gw.onCall = func() {
		_, adminErr = f.s.AdminReverseSell(context.Background(), in.ID)
		// The internal reverse path (webhook, reconciler) has no admin
		// pre-checks: the row claim alone must stop it.
		_, directErr = f.s.ReverseSell(context.Background(), in.ID, "")
	}

	if err := f.s.AutoPayout(context.Background(), in); err != nil {
		t.Fatalf("AutoPayout: %v", err)
	}
	if adminErr == nil || directErr == nil || f.chain.reverseCalls != 0 {
		t.Fatalf("adminErr=%v directErr=%v reverse=%d, want every reverse refused while the send is in flight", adminErr, directErr, f.chain.reverseCalls)
	}
	if got := f.reload(t, in.ID); got.PayoutID != "trf_ok" || got.Status != models.ITStatusDebited {
		t.Fatalf("payout=%q status=%s", got.PayoutID, got.Status)
	}
}

// Scenario B, reverse first: a send holding a stale copy of the row must lose
// to a reverse that has already claimed it.
func TestC1_StaleSendLosesToClaimedReverse(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, nil)
	stale := *in
	f.chain.reverse = notBroadcast(errors.New("rpc down")) // claim taken, reverse not sent

	if _, err := f.s.AdminReverseSell(context.Background(), in.ID); err == nil {
		t.Fatal("want the reverse broadcast error")
	}
	if err := f.s.AutoPayout(context.Background(), &stale); err == nil {
		t.Fatal("a send on a row claimed for reversal must fail")
	}
	if len(f.gw.calls) != 0 {
		t.Fatalf("%d payout(s) sent after the reverse claimed the row", len(f.gw.calls))
	}
}

func TestC1_PayoutRequiresPendingDebitOnChain(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, nil)
	r := f.chain.records[itRef("IT:debit:", in.ID)]
	r.State = DebitStateResolved
	f.chain.records[itRef("IT:debit:", in.ID)] = r

	if err := f.s.AutoPayout(context.Background(), in); err == nil || errors.Is(err, ErrITSellPayoutRejected) {
		t.Fatalf("err = %v, want a non-rejection refusal", err)
	}
	if len(f.gw.calls) != 0 {
		t.Fatal("no payout may be sent for a debit that is no longer pending")
	}
	if got := f.reload(t, in.ID); got.Status != models.ITStatusNeedsReview {
		t.Fatalf("status = %s, want needs_review", got.Status)
	}

	f.chain.readErr = errors.New("rpc down")
	in2 := f.seedSell(t, nil)
	if err := f.s.AutoPayout(context.Background(), in2); err == nil || len(f.gw.calls) != 0 {
		t.Fatalf("err=%v calls=%d, want no send when the debit cannot be read", err, len(f.gw.calls))
	}
}

// A reverse that reverts because the debit is already resolved must not be
// retried forever.
func TestC1_RevertingReverseStopsRetrying(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutRejected = true })
	ref := itRef("IT:debit:", in.ID)
	r := f.chain.records[ref]
	r.State = DebitStateResolved
	f.chain.records[ref] = r
	f.chain.reverse = notBroadcast(errors.New("gas estimation failed: execution reverted: Not pending"))
	f.age(t, in.ID, time.Hour)

	f.reconcile(t)
	f.age(t, in.ID, time.Hour)
	f.reconcile(t)
	if f.chain.reverseCalls > 1 {
		t.Fatalf("reverse attempted %d times on a resolved debit", f.chain.reverseCalls)
	}
	if got := f.reload(t, in.ID); got.Status != models.ITStatusNeedsReview {
		t.Fatalf("status = %s, want needs_review", got.Status)
	}
}

func TestC1_SettleVerifyFailureOnResolvedDebitGoesToReview(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutID = "trf_s"; in.PayoutAttempts = 1 })
	f.transfers.byID["trf_s"] = FWTransfer{ID: "trf_s", Status: "SUCCESSFUL"}
	f.chain.verifyErr["PremiumDebitSettled"] = errors.New("receipt timeout")
	f.age(t, in.ID, time.Hour)

	f.reconcile(t)
	if got := f.reload(t, in.ID); got.Status != models.ITStatusNeedsReview {
		t.Fatalf("status = %s, want needs_review", got.Status)
	}
}

// ─── C2: absence at Flutterwave is only trusted behind the flag ─────────────

func TestC2_AbsenceUntrustedAlertsInsteadOfResending(t *testing.T) {
	f := newSellFixture(t) // flag off by default
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1; in.FailureReason = "auto-payout unknown: timeout" })
	f.age(t, in.ID, 10*time.Minute)

	f.reconcile(t)
	if len(f.gw.calls) != 0 || f.chain.reverseCalls != 0 {
		t.Fatalf("calls=%d reverse=%d, want alert-only while absence is untrusted", len(f.gw.calls), f.chain.reverseCalls)
	}
	if _, err := f.s.RetrySellPayout(context.Background(), in.ID); !errors.Is(err, ErrITSellPayoutUnverified) || len(f.gw.calls) != 0 {
		t.Fatalf("retry err=%v calls=%d, want unverified", err, len(f.gw.calls))
	}
}

func TestC2_ProviderConfirmedOutcomesWorkWithoutTheFlag(t *testing.T) {
	f := newSellFixture(t)
	found := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
	f.transfers.byRef[itPayoutRef(found.ID)] = FWTransfer{ID: "trf_found", Status: "SUCCESSFUL"}
	fresh := f.seedSell(t, nil) // provably never sent
	f.age(t, found.ID, 10*time.Minute)
	f.age(t, fresh.ID, 10*time.Minute)

	f.reconcile(t)
	if got := f.reload(t, found.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("found transfer: status = %s, want settled", got.Status)
	}
	if len(f.gw.calls) != 1 || f.gw.calls[0].Reference != itPayoutRef(fresh.ID) {
		t.Fatalf("calls = %+v, want the never-sent payout sent", f.gw.calls)
	}
}

func TestC2_LookupIsCentredOnTheLastAttempt(t *testing.T) {
	f := newSellFixture(t)
	f.s.SetFWAbsenceTrusted(true)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
	f.age(t, in.ID, 3*time.Hour)
	attempt := time.Now().Add(-2 * time.Hour)
	f.s.db.Exec(`UPDATE it_intents SET last_payout_attempt_at = ? WHERE id = ?`, attempt, in.ID)

	f.reconcile(t)
	if d := f.transfers.lastAround.Sub(attempt); d > time.Second || d < -time.Second {
		t.Fatalf("lookup centred on %v, want the last attempt %v", f.transfers.lastAround, attempt)
	}
}

func TestAutoPayout_RecordsLastAttemptTime(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, nil)
	if err := f.s.AutoPayout(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t, in.ID); got.LastPayoutAttemptAt == nil || time.Since(*got.LastPayoutAttemptAt) > time.Minute {
		t.Fatalf("last_payout_attempt_at = %v", got.LastPayoutAttemptAt)
	}
}

// ─── C3: a refusal on a retry is confirmed before reversing ────────────────

func TestC3_RetryRefusalReversesOnlyAfterConfirmedAbsence(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1; in.PayoutRefusedOnRetry = true })
	f.age(t, in.ID, time.Hour)

	f.reconcile(t) // absence untrusted
	if f.chain.reverseCalls != 0 || len(f.gw.calls) != 0 {
		t.Fatalf("reverse=%d calls=%d, want alert-only without trusted absence", f.chain.reverseCalls, len(f.gw.calls))
	}

	f.transfers.byRef[itPayoutRef(in.ID)] = FWTransfer{ID: "trf_first", Status: "SUCCESSFUL"}
	f.reconcile(t)
	if got := f.reload(t, in.ID); got.Status != models.ITStatusSettled || f.chain.reverseCalls != 0 {
		t.Fatalf("status=%s reverse=%d — the refused retry's first transfer succeeded; must settle, not reverse", got.Status, f.chain.reverseCalls)
	}

	g := newSellFixture(t)
	g.s.SetFWAbsenceTrusted(true)
	in2 := g.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1; in.PayoutRefusedOnRetry = true })
	g.age(t, in2.ID, time.Hour)
	g.reconcile(t)
	if got := g.reload(t, in2.ID); got.Status != models.ITStatusReversed || len(g.gw.calls) != 0 {
		t.Fatalf("status=%s calls=%d, want reversed (confirmed absent) and never re-sent", got.Status, len(g.gw.calls))
	}
}

// ─── I1: alert-only rows must not starve the queue ──────────────────────────

func TestI1_AlertOnlyRowsDoNotBlockNewSells(t *testing.T) {
	f := newSellFixture(t) // absence untrusted: attempted rows are alert-only
	for i := 0; i < 55; i++ {
		in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1; in.FailureReason = "auto-payout unknown: timeout" })
		f.age(t, in.ID, 2*time.Hour)
	}
	fresh := f.seedSell(t, nil)
	f.age(t, fresh.ID, 10*time.Minute)

	for tick := 0; tick < 2 && len(f.gw.calls) == 0; tick++ {
		f.reconcile(t)
	}
	if len(f.gw.calls) != 1 || f.gw.calls[0].Reference != itPayoutRef(fresh.ID) {
		t.Fatalf("calls = %d, want the new sell paid out within two ticks", len(f.gw.calls))
	}
}

// ─── I2: no FW bypass for a payout that was ever sent ───────────────────────

func TestI2_NoGatewayStillNeedsProviderEvidenceOnceSent(t *testing.T) {
	f := newSellFixture(t)
	f.s.payout = nil
	f.s.payoutTransfers = nil
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
	f.age(t, in.ID, time.Hour)
	if _, err := f.s.AdminReverseSell(context.Background(), in.ID); !errors.Is(err, ErrITSellPayoutUnverified) {
		t.Fatalf("reverse err = %v, want unverified", err)
	}
	if _, err := f.s.AdminSettleSell(context.Background(), in.ID); !errors.Is(err, ErrITSellPayoutUnverified) {
		t.Fatalf("settle err = %v, want unverified", err)
	}
	if f.chain.reverseCalls+f.chain.settleCalls != 0 {
		t.Fatal("no chain op without evidence")
	}
}

// ─── I3: stablecoin payouts ─────────────────────────────────────────────────

func stableSell(in *models.ITIntent) {
	in.Asset = models.ITAssetUSDC
}

func TestI3_StableRefusalGoesBackToWatcher(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, stableSell)
	f.gw.err = rejected("HTTP 422: Insufficient balance")

	for i := 0; i < 2; i++ {
		_ = f.s.PayoutStableSell(context.Background(), in.ID)
	}
	if got := f.reload(t, in.ID); got.PayoutRejected || len(f.gw.calls) != 2 {
		t.Fatalf("rejected=%v calls=%d, want the watcher to keep retrying", got.PayoutRejected, len(f.gw.calls))
	}
}

func TestI3_StableWebhookParksOnlyOnConfirmedFailure(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { stableSell(in); in.PayoutID = "trf_p"; in.PayoutAttempts = 1 })
	f.transfers.byID["trf_p"] = FWTransfer{ID: "trf_p", Reference: itPayoutRef(in.ID), Status: "PENDING"}

	_ = f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_p", "failed")
	if got := f.reload(t, in.ID); got.PayoutRejected {
		t.Fatal("a FAILED webhook the provider does not confirm must not park the sell")
	}
}

type countingFeeSink struct{ n int }

func (c *countingFeeSink) EnqueueFeeSettled(FeeEvent) { c.n++ }

func TestI3_StableSettleBooksFeeOnce(t *testing.T) {
	f := newSellFixture(t)
	sink := &countingFeeSink{}
	f.s.fees = sink
	in := f.seedSell(t, func(in *models.ITIntent) { stableSell(in); in.PayoutID = "trf_s"; in.PlatformKeptKobo = "5000" })
	stale := *in
	stale2 := *in

	if _, err := f.s.resolveStableSellPayout(context.Background(), &stale, true); err != nil {
		t.Fatal(err)
	}
	_, _ = f.s.resolveStableSellPayout(context.Background(), &stale2, true)
	if sink.n != 1 {
		t.Fatalf("fee booked %d times, want once", sink.n)
	}
}

func TestI3_RepayStableSell(t *testing.T) {
	parked := func(f *sellFixture) *models.ITIntent {
		return f.seedSell(t, func(in *models.ITIntent) {
			stableSell(in)
			in.PayoutID = "trf_f"
			in.PayoutAttempts = 1
			in.PayoutRejected = true
			in.FailureReason = "payout FAILED at provider"
		})
	}
	t.Run("confirmed failure repays with a fresh reference", func(t *testing.T) {
		f := newSellFixture(t)
		in := parked(f)
		f.transfers.byID["trf_f"] = FWTransfer{ID: "trf_f", Status: "FAILED"}
		f.gw.payoutID = "trf_new"
		got, err := f.s.RepayStableSell(context.Background(), in.ID)
		if err != nil {
			t.Fatalf("repay: %v", err)
		}
		if len(f.gw.calls) != 1 {
			t.Fatalf("calls = %d", len(f.gw.calls))
		}
		ref := f.gw.calls[0].Reference
		if ref == itPayoutRef(in.ID) || len(ref) > 42 || !strings.HasPrefix(ref, ITPayoutRefPrefix) {
			t.Fatalf("reference %q must be fresh, prefixed and ≤42 chars", ref)
		}
		if got.PayoutID != "trf_new" || got.PayoutRejected || got.PayoutRefGen != 1 {
			t.Fatalf("row = payout %q rejected %v gen %d", got.PayoutID, got.PayoutRejected, got.PayoutRefGen)
		}
		// A late webhook for the superseded transfer must not touch the new one.
		f.transfers.byID["trf_f"] = FWTransfer{ID: "trf_f", Reference: itPayoutRef(in.ID), Status: "FAILED"}
		_ = f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_f", "failed")
		if got := f.reload(t, in.ID); got.PayoutRejected || got.PayoutID != "trf_new" {
			t.Fatalf("old-generation webhook changed the row: %+v", got)
		}
		// And the new generation's webhook resolves it.
		f.transfers.byID["trf_new"] = FWTransfer{ID: "trf_new", Reference: ref, Status: "SUCCESSFUL"}
		if err := f.s.ResolvePayoutFromWebhook(context.Background(), ref, "trf_new", "success"); err != nil {
			t.Fatalf("new-generation webhook: %v", err)
		}
		if got := f.reload(t, in.ID); got.Status != models.ITStatusSettled {
			t.Fatalf("status = %s, want settled", got.Status)
		}
	})
	t.Run("unconfirmed failure is refused", func(t *testing.T) {
		f := newSellFixture(t)
		in := parked(f)
		f.transfers.byID["trf_f"] = FWTransfer{ID: "trf_f", Status: "PENDING"}
		if _, err := f.s.RepayStableSell(context.Background(), in.ID); err == nil || len(f.gw.calls) != 0 {
			t.Fatalf("err=%v calls=%d", err, len(f.gw.calls))
		}
	})
	t.Run("not parked is refused", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.seedSell(t, func(in *models.ITIntent) { stableSell(in); in.PayoutID = "trf_x" })
		if _, err := f.s.RepayStableSell(context.Background(), in.ID); err == nil || len(f.gw.calls) != 0 {
			t.Fatalf("err=%v calls=%d", err, len(f.gw.calls))
		}
	})
	t.Run("MCGP sell is refused", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutID = "trf_f"; in.PayoutRejected = true })
		f.transfers.byID["trf_f"] = FWTransfer{ID: "trf_f", Status: "FAILED"}
		if _, err := f.s.RepayStableSell(context.Background(), in.ID); err == nil {
			t.Fatal("repay is for stablecoin sells only")
		}
	})
}

// ─── I5: webhooks are read back before money moves ─────────────────────────

func TestI5_WebhookStatusMustMatchProvider(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutID = "trf_1"; in.PayoutAttempts = 1 })
	f.transfers.byID["trf_1"] = FWTransfer{ID: "trf_1", Reference: itPayoutRef(in.ID), Status: "PENDING"}

	_ = f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_1", "success")
	_ = f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_1", "failed")
	if f.chain.settleCalls+f.chain.reverseCalls != 0 {
		t.Fatal("a webhook the provider does not confirm must move nothing")
	}
	f.transfers.byID["trf_1"] = FWTransfer{ID: "trf_1", Reference: itPayoutRef(in.ID), Status: "SUCCESSFUL"}
	if err := f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_1", "success"); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t, in.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("status = %s, want settled once confirmed", got.Status)
	}
}

func TestI5_WebhookPayoutIDMismatchIsIgnored(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutID = "trf_real"; in.PayoutAttempts = 1 })
	f.transfers.byID["trf_other"] = FWTransfer{ID: "trf_other", Reference: itPayoutRef(in.ID), Status: "FAILED"}

	_ = f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_other", "failed")
	if f.chain.reverseCalls != 0 || f.reload(t, in.ID).Status != models.ITStatusDebited {
		t.Fatal("a webhook for a different transfer id must not reverse")
	}
}

// The webhook can arrive before the send has recorded its payout id.
func TestI5_WebhookBeforePayoutIDRecordedAdoptsConfirmedTransfer(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
	f.transfers.byID["trf_9"] = FWTransfer{ID: "trf_9", Reference: itPayoutRef(in.ID), Status: "FAILED"}

	if err := f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_9", "failed"); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	if got := f.reload(t, in.ID); got.PayoutID != "trf_9" || got.Status != models.ITStatusReversed {
		t.Fatalf("payout=%q status=%s, want the confirmed transfer adopted and reversed", got.PayoutID, got.Status)
	}
}

// ─── I6: late debits beyond the scan window still alert ────────────────────

func TestI6_DebitLandingLongAfterFailureIsReported(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.Status = models.ITStatusFailed })
	f.chain.records[itRef("IT:debit:", in.ID)] = DebitRecord{User: common.HexToAddress(sellWallet), Amount: new(big.Int).Mul(big.NewInt(100), oneE18), State: DebitStatePending}
	f.age(t, in.ID, 72*time.Hour)

	n, err := f.s.AlertLateDebitsBeyondWindow(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, want one alert", n, err)
	}
	if f.chain.reverseCalls != 0 {
		t.Fatal("beyond the window the scan only alerts")
	}
}

// ─── I7: NULL columns on old rows ───────────────────────────────────────────

func TestI7_NullPayoutIDStillMatches(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, nil)
	if err := f.s.db.Exec(`UPDATE it_intents SET payout_id = NULL WHERE id = ?`, in.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.age(t, in.ID, 10*time.Minute)
	f.reconcile(t)
	if len(f.gw.calls) != 1 {
		t.Fatalf("calls = %d, want the NULL-payout_id row paid out", len(f.gw.calls))
	}
}

var _ = uuid.Nil
