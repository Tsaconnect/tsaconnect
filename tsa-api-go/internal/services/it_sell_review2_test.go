package services

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// logsDuring collects everything written to the standard logger while fn runs.
func logsDuring(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	fn()
	return buf.String()
}

// dropDebit removes the on-chain debit record, standing in for an RPC node
// that has not caught up with the block the debit is in.
func (f *sellFixture) dropDebit(in *models.ITIntent) {
	delete(f.chain.records, itRef("IT:debit:", in.ID))
}

// ─── N1: a lagging node must not park a healthy sell ────────────────────────

// REGRESSION: a node one block behind returned "no debit", which parked a
// perfectly good sell in needs_review with no way out.
func TestN1_LaggingNodeDoesNotParkTheSell(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, nil)
	f.dropDebit(in)

	if err := f.s.AutoPayout(context.Background(), in); err == nil {
		t.Fatal("want the payout held back while the debit is not visible")
	}
	got := f.reload(t, in.ID)
	if got.Status != models.ITStatusDebited {
		t.Fatalf("status = %s, want the sell left debited for a later tick", got.Status)
	}
	if len(f.gw.calls) != 0 {
		t.Fatal("no payout may be sent while the debit is not visible")
	}

	// The node catches up: the next reconciler tick completes the sell.
	f.chain.records[itRef("IT:debit:", in.ID)] = DebitRecord{
		User: common.HexToAddress(sellWallet), Amount: new(big.Int).Mul(big.NewInt(100), oneE18), State: DebitStatePending,
	}
	f.age(t, in.ID, 10*time.Minute)
	f.reconcile(t)
	if got := f.reload(t, in.ID); got.PayoutID != "trf_ok" {
		t.Fatalf("payout = %q, want the sell paid out once the debit is visible", got.PayoutID)
	}
}

func TestN1_ChainReadErrorDoesNotPark(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, nil)
	f.chain.readErr = errors.New("rpc down")

	if err := f.s.AutoPayout(context.Background(), in); err == nil {
		t.Fatal("want an error")
	}
	if got := f.reload(t, in.ID); got.Status != models.ITStatusDebited {
		t.Fatalf("status = %s, want debited", got.Status)
	}
}

func TestN1_ParksOnResolvedOrMismatchOnly(t *testing.T) {
	t.Run("resolved", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.seedSell(t, nil)
		ref := itRef("IT:debit:", in.ID)
		r := f.chain.records[ref]
		r.State = DebitStateResolved
		f.chain.records[ref] = r
		_ = f.s.AutoPayout(context.Background(), in)
		if got := f.reload(t, in.ID); got.Status != models.ITStatusNeedsReview {
			t.Fatalf("status = %s, want needs_review", got.Status)
		}
	})
	t.Run("wallet mismatch", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.seedSell(t, nil)
		f.chain.records[itRef("IT:debit:", in.ID)] = DebitRecord{
			User: common.HexToAddress("0x00000000000000000000000000000000000000CD"), Amount: big.NewInt(1), State: DebitStatePending,
		}
		_ = f.s.AutoPayout(context.Background(), in)
		if got := f.reload(t, in.ID); got.Status != models.ITStatusNeedsReview {
			t.Fatalf("status = %s, want needs_review", got.Status)
		}
	})
}

// A debit that stays invisible is not silently retried forever.
func TestN1_PersistentlyInvisibleDebitAlerts(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, nil)
	f.dropDebit(in)
	f.age(t, in.ID, time.Hour)
	in = f.reload(t, in.ID) // as the reconciler would read it

	out := logsDuring(t, func() { _ = f.s.AutoPayout(context.Background(), in) })
	if !strings.Contains(out, "ALERT") {
		t.Fatalf("log = %q, want an ALERT once the debit has been invisible past the abandon window", out)
	}
	if got := f.reload(t, in.ID); got.Status != models.ITStatusDebited {
		t.Fatalf("status = %s, want debited (alert only)", got.Status)
	}
}

// ─── N1(b): the needs_review exit door ──────────────────────────────────────

func TestN1_ReevaluateSell(t *testing.T) {
	park := func(f *sellFixture, reason string) *models.ITIntent {
		in := f.seedSell(t, func(in *models.ITIntent) {
			in.Status = models.ITStatusNeedsReview
			in.FailureReason = reason
			in.PayoutAttempts = 1
		})
		return in
	}
	resolve := func(f *sellFixture, in *models.ITIntent) {
		ref := itRef("IT:debit:", in.ID)
		f.chain.records[ref] = DebitRecord{User: common.HexToAddress(sellWallet), Amount: new(big.Int).Mul(big.NewInt(100), oneE18), State: DebitStateResolved}
	}

	t.Run("debit still pending returns the sell to debited", func(t *testing.T) {
		f := newSellFixture(t)
		in := park(f, "payout blocked: on-chain debit state 0 …")
		f.chain.records[itRef("IT:debit:", in.ID)] = DebitRecord{User: common.HexToAddress(sellWallet), Amount: new(big.Int).Mul(big.NewInt(100), oneE18), State: DebitStatePending}
		got, err := f.s.ReevaluateSell(context.Background(), in.ID)
		if err != nil || got.Status != models.ITStatusDebited {
			t.Fatalf("status=%v err=%v, want debited", got, err)
		}
	})
	t.Run("a settle that did not verify settles", func(t *testing.T) {
		f := newSellFixture(t)
		in := park(f, reviewReasonSettle+": tx 0xabc: receipt timeout")
		in.PayoutID = "trf_s"
		f.s.db.Model(&models.ITIntent{}).Where("id = ?", in.ID).Update("payout_id", "trf_s")
		f.transfers.byID["trf_s"] = FWTransfer{ID: "trf_s", Status: "SUCCESSFUL"}
		resolve(f, in)
		got, err := f.s.ReevaluateSell(context.Background(), in.ID)
		if err != nil || got.Status != models.ITStatusSettled {
			t.Fatalf("status=%v err=%v, want settled", got, err)
		}
		if f.chain.settleCalls != 0 {
			t.Fatal("the debit is already resolved on-chain; nothing may be broadcast")
		}
	})
	t.Run("a reverse that did not verify reverses", func(t *testing.T) {
		f := newSellFixture(t)
		in := park(f, reviewReasonReverse+": tx 0xabc: receipt timeout")
		resolve(f, in)
		got, err := f.s.ReevaluateSell(context.Background(), in.ID)
		if err != nil || got.Status != models.ITStatusReversed {
			t.Fatalf("status=%v err=%v, want reversed", got, err)
		}
	})
	t.Run("provider evidence decides when the attempted op is unknown", func(t *testing.T) {
		f := newSellFixture(t)
		in := park(f, "payout blocked: on-chain debit state 2 …")
		f.s.db.Model(&models.ITIntent{}).Where("id = ?", in.ID).Update("payout_id", "trf_x")
		f.transfers.byID["trf_x"] = FWTransfer{ID: "trf_x", Status: "SUCCESSFUL"}
		resolve(f, in)
		got, err := f.s.ReevaluateSell(context.Background(), in.ID)
		if err != nil || got.Status != models.ITStatusSettled {
			t.Fatalf("status=%v err=%v, want settled from the successful transfer", got, err)
		}
	})
	t.Run("contradictory evidence stays parked", func(t *testing.T) {
		f := newSellFixture(t)
		in := park(f, reviewReasonReverse+": tx 0xabc: receipt timeout")
		f.s.db.Model(&models.ITIntent{}).Where("id = ?", in.ID).Update("payout_id", "trf_ok2")
		f.transfers.byID["trf_ok2"] = FWTransfer{ID: "trf_ok2", Status: "SUCCESSFUL"}
		resolve(f, in)
		out := logsDuring(t, func() {
			if _, err := f.s.ReevaluateSell(context.Background(), in.ID); err == nil {
				t.Fatal("a reverse against a successful payout must not be confirmed")
			}
		})
		if !strings.Contains(out, "ALERT") {
			t.Fatalf("log = %q, want an ALERT", out)
		}
		if got := f.reload(t, in.ID); got.Status != models.ITStatusNeedsReview {
			t.Fatalf("status = %s, want it left parked", got.Status)
		}
	})
	t.Run("ambiguous chain state is refused", func(t *testing.T) {
		f := newSellFixture(t)
		in := park(f, reviewReasonSettle+": x")
		f.dropDebit(in) // state None: node lag or never landed
		if _, err := f.s.ReevaluateSell(context.Background(), in.ID); err == nil {
			t.Fatal("want a refusal while the chain cannot say")
		}
		f.chain.readErr = errors.New("rpc down")
		if _, err := f.s.ReevaluateSell(context.Background(), in.ID); !errors.Is(err, ErrITSellPayoutUnverified) {
			t.Fatalf("err = %v, want unverified", err)
		}
		if got := f.reload(t, in.ID); got.Status != models.ITStatusNeedsReview {
			t.Fatalf("status = %s", got.Status)
		}
	})
}

func TestN1_StuckListShowsNeedsReview(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.Status = models.ITStatusNeedsReview })
	f.age(t, in.ID, time.Hour)
	rows, err := f.s.ListStuckMCGPSells(context.Background(), time.Minute)
	if err != nil || len(rows) != 1 || rows[0].Status != models.ITStatusNeedsReview {
		t.Fatalf("rows=%+v err=%v, want the parked sell listed", rows, err)
	}
}

// ─── N2: a stale caller cannot mark a sell rejected ─────────────────────────

func TestN2_StaleRejectionCannotReverseAWinningSend(t *testing.T) {
	f := newSellFixture(t)
	allowQuoteCurrency(t, "KES")
	in := f.seedSell(t, func(in *models.ITIntent) { in.DestinationCurrency = "KES" }) // no quoter: preflight fails
	stale := *in
	// A concurrent caller won the attempt CAS and is sending right now.
	f.s.db.Exec(`UPDATE it_intents SET payout_attempts = 1, last_payout_attempt_at = ? WHERE id = ?`, time.Now(), in.ID)

	err := f.s.AutoPayout(context.Background(), &stale)
	if err == nil || errors.Is(err, ErrITSellPayoutRejected) {
		t.Fatalf("err = %v, want a plain failure — a stale caller must not declare the payout rejected", err)
	}
	if got := f.reload(t, in.ID); got.PayoutRejected {
		t.Fatal("stale caller marked the sell rejected; the in-flight send would be reversed under it")
	}
	if f.chain.reverseCalls != 0 {
		t.Fatal("no reverse may follow a stale rejection")
	}
}

func TestN2_ClaimReverseRevalidatesAnExistingClaim(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutRejected = true })
	// The row moved on since it was claimed: a payout id was recorded.
	f.s.db.Exec(`UPDATE it_intents SET payout_id = 'trf_live' WHERE id = ?`, in.ID)

	stale := *in // still remembers payout_id == ""
	if err := f.s.claimReverse(context.Background(), &stale); err == nil {
		t.Fatal("an already-claimed row that has since changed must not pass the claim")
	}
	if f.chain.reverseCalls != 0 {
		t.Fatalf("reverse=%d, want nothing broadcast", f.chain.reverseCalls)
	}
	// The claim is re-asserted, not assumed: a caller reading the row as it now
	// stands still claims it (the evidence checks above ReverseSell decide
	// whether reversing is right at all).
	fresh := f.reload(t, in.ID)
	if err := f.s.claimReverse(context.Background(), fresh); err != nil {
		t.Fatalf("a current read must still claim: %v", err)
	}
}

// ─── N3: a permanently refused stablecoin payout has an exit ────────────────

func TestN3_RepayAfterRefusedAttempts(t *testing.T) {
	seed := func(f *sellFixture) *models.ITIntent {
		return f.seedSell(t, func(in *models.ITIntent) {
			in.Asset = models.ITAssetUSDC
			in.PayoutAttempts = itPayoutMaxAttempts
			in.FailureReason = itPayoutReasonPrefix + " refused: HTTP 422 Insufficient balance"
		})
	}
	t.Run("absent at the provider and absence trusted", func(t *testing.T) {
		f := newSellFixture(t)
		f.s.SetFWAbsenceTrusted(true)
		in := seed(f)
		f.age(t, in.ID, time.Hour)
		f.gw.err = nil
		f.gw.payoutID = "trf_fresh"
		got, err := f.s.RepayStableSell(context.Background(), in.ID)
		if err != nil {
			t.Fatalf("repay: %v", err)
		}
		if got.PayoutRefGen != 1 || got.PayoutID != "trf_fresh" || len(f.gw.calls) != 1 {
			t.Fatalf("row=%+v calls=%d", got, len(f.gw.calls))
		}
		if ref := f.gw.calls[0].Reference; ref == itPayoutRef(in.ID) || len(ref) > 42 {
			t.Fatalf("reference %q must be fresh and within the limit", ref)
		}
	})
	t.Run("absence not trusted is refused", func(t *testing.T) {
		f := newSellFixture(t)
		in := seed(f)
		f.age(t, in.ID, time.Hour)
		if _, err := f.s.RepayStableSell(context.Background(), in.ID); !errors.Is(err, ErrITSellPayoutUnverified) || len(f.gw.calls) != 0 {
			t.Fatalf("err=%v calls=%d", err, len(f.gw.calls))
		}
	})
	t.Run("a transfer that exists is adopted, not repaid", func(t *testing.T) {
		f := newSellFixture(t)
		f.s.SetFWAbsenceTrusted(true)
		in := seed(f)
		f.age(t, in.ID, time.Hour)
		f.transfers.byRef[itPayoutRef(in.ID)] = FWTransfer{ID: "trf_exists", Status: "PENDING"}
		if _, err := f.s.RepayStableSell(context.Background(), in.ID); err == nil || len(f.gw.calls) != 0 {
			t.Fatalf("err=%v calls=%d, want a refusal: a transfer for this reference exists", err, len(f.gw.calls))
		}
		if got := f.reload(t, in.ID); got.PayoutID != "trf_exists" {
			t.Fatalf("payout id = %q, want the existing transfer adopted", got.PayoutID)
		}
	})
}

func TestN3_WatcherAlertsAndThrottlesRefusedStablePayouts(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) {
		in.Asset = models.ITAssetUSDT
		in.PayoutAttempts = itPayoutMaxAttempts
		in.FailureReason = itPayoutReasonPrefix + " refused: HTTP 422"
	})
	f.s.db.Exec(`UPDATE it_intents SET last_payout_attempt_at = ? WHERE id = ?`, time.Now(), in.ID)
	f.gw.err = rejected("HTTP 422")

	out := logsDuring(t, func() { _ = f.s.PayoutStableSell(context.Background(), in.ID) })
	if !strings.Contains(out, "ALERT") {
		t.Fatalf("log = %q, want an ALERT after repeated refusals", out)
	}
	if len(f.gw.calls) != 0 {
		t.Fatalf("calls = %d, want the retry throttled after a recent refusal", len(f.gw.calls))
	}
}

// ─── N4: an unconfirmable webhook must be re-driven ─────────────────────────

func TestN4_NonFinalProviderStatusIsRetryable(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) {
		in.Asset = models.ITAssetUSDC
		in.PayoutID = "trf_lag"
		in.PayoutAttempts = 1
	})
	f.transfers.byID["trf_lag"] = FWTransfer{ID: "trf_lag", Reference: itPayoutRef(in.ID), Status: "PENDING"}

	err := f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_lag", "success")
	if err == nil || errors.Is(err, ErrNonSettlingEvent) {
		t.Fatalf("err = %v, want a retryable error so the webhook is re-driven", err)
	}
	// Once the provider catches up the redelivery settles it.
	f.transfers.byID["trf_lag"] = FWTransfer{ID: "trf_lag", Reference: itPayoutRef(in.ID), Status: "SUCCESSFUL"}
	if err := f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_lag", "success"); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if got := f.reload(t, in.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("status = %s, want settled", got.Status)
	}
}

func TestN4_ContradictionStaysTerminal(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutID = "trf_c"; in.PayoutAttempts = 1 })
	f.transfers.byID["trf_c"] = FWTransfer{ID: "trf_c", Reference: itPayoutRef(in.ID), Status: "FAILED"}

	if err := f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_c", "success"); !errors.Is(err, ErrNonSettlingEvent) {
		t.Fatalf("err = %v, want a terminal acknowledgement for a contradiction", err)
	}
	if f.chain.settleCalls != 0 {
		t.Fatal("nothing may settle")
	}
}

// ─── N5: lookup and decode failures are alerts, not log lines ───────────────

func TestN5_LookupFailuresAlert(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1; in.FailureReason = "auto-payout unknown: timeout" })
	f.age(t, in.ID, 10*time.Minute)
	f.s.SetFWAbsenceTrusted(true)
	f.transfers.findErr = errors.New("flutterwave list-transfers: HTTP 503")

	if out := logsDuring(t, func() { f.reconcile(t) }); !strings.Contains(out, "ALERT") {
		t.Fatalf("log = %q, want the failed lookup alerted", out)
	}
}

func TestN5_UnrecognisedProviderShapeAlertsLoudly(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1; in.FailureReason = "auto-payout unknown: timeout" })
	f.age(t, in.ID, 10*time.Minute)
	f.transfers.findErr = ErrFWTransferShape

	out := logsDuring(t, func() { f.reconcile(t) })
	if !strings.Contains(out, "it_fw_transfer_shape") {
		t.Fatalf("log = %q, want the response-shape failure called out by name", out)
	}
}

// The client must mark a shape failure as such, since that is what the
// absence flag is waiting on.
func TestN5_ClientMarksShapeErrors(t *testing.T) {
	c, _ := fwTransfersServer(t, func(q url.Values) any {
		_ = q
		return map[string]any{"status": "success", "data": map[string]any{"cursor": map[string]any{}, "transfers": []any{}}}
	})
	_, _, err := c.FindTransferByReference(context.Background(), "IT-SELL-abc", fwAround)
	if !errors.Is(err, ErrFWTransferShape) {
		t.Fatalf("err = %v, want ErrFWTransferShape", err)
	}
}

// ─── minors ─────────────────────────────────────────────────────────────────

// The 10-minute in-flight rule must be evaluated by the database, not against
// one process's clock.
func TestMinor_ReverseAgeRuleIsEvaluatedInTheDatabase(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
	f.s.db.Exec(`UPDATE it_intents SET last_payout_attempt_at = ? WHERE id = ?`, time.Now().Add(-time.Minute), in.ID)
	fresh := f.reload(t, in.ID)
	if err := f.s.claimReverse(context.Background(), fresh); err == nil {
		t.Fatal("a payout attempted a minute ago may still be in flight; the claim must fail")
	}
	f.s.db.Exec(`UPDATE it_intents SET last_payout_attempt_at = ? WHERE id = ?`, time.Now().Add(-time.Hour), in.ID)
	fresh = f.reload(t, in.ID)
	if err := f.s.claimReverse(context.Background(), fresh); err != nil {
		t.Fatalf("an hour-old attempt must be claimable: %v", err)
	}
}

// The age rule must use the DATABASE's clock. This writes the timestamp with
// the database's own `now` (UTC in SQLite) and claims from a process whose
// local time is ahead of it — the shape of real clock skew between an app
// server and its database. Where the two agree (a UTC test host) both
// comparisons give the same answer and this only re-asserts the rule.
func TestMinor_ReverseAgeRuleSurvivesClockSkew(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
	if err := f.s.db.Exec(`UPDATE it_intents SET last_payout_attempt_at = datetime('now', '-5 minutes') WHERE id = ?`, in.ID).Error; err != nil {
		t.Fatal(err)
	}
	fresh := f.reload(t, in.ID)
	if err := f.s.claimReverse(context.Background(), fresh); err == nil {
		t.Fatal("a payout attempted 5 database-minutes ago may still be in flight; the claim must fail")
	}
}

func TestMinor_AlertLimiterEvictsOldEntries(t *testing.T) {
	lim := &alertLimiter{last: map[string]time.Time{}}
	for i := 0; i < alertLimiterMaxKeys+10; i++ {
		lim.allow(uuid.NewString(), time.Nanosecond)
	}
	if n := lim.size(); n > alertLimiterMaxKeys {
		t.Fatalf("limiter holds %d keys, want it bounded at %d", n, alertLimiterMaxKeys)
	}
}

func TestMinor_DailyAuditIsNotRepeatedAfterARestart(t *testing.T) {
	f := newSellFixture(t)
	if err := f.s.db.AutoMigrate(&models.JobRunMarker{}); err != nil {
		t.Fatal(err)
	}
	if !claimDailyRun(context.Background(), f.s.db, "test-audit", 24*time.Hour) {
		t.Fatal("first run should be allowed")
	}
	// A restart loses any in-memory marker; the persisted one must still hold.
	if claimDailyRun(context.Background(), f.s.db, "test-audit", 24*time.Hour) {
		t.Fatal("second run within the window must be skipped")
	}
}

func TestMinor_WebhookForAParkedSellIsAcknowledged(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) {
		in.Status = models.ITStatusNeedsReview
		in.PayoutAttempts = 1
	})
	f.transfers.byID["trf_p"] = FWTransfer{ID: "trf_p", Reference: itPayoutRef(in.ID), Status: "SUCCESSFUL"}

	out := logsDuring(t, func() {
		err := f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_p", "success")
		if !errors.Is(err, ErrNonSettlingEvent) {
			t.Fatalf("err = %v, want a terminal acknowledgement", err)
		}
	})
	if !strings.Contains(out, "ALERT") {
		t.Fatalf("log = %q, want an ALERT", out)
	}
	if got := f.reload(t, in.ID); got.PayoutID != "" {
		t.Fatalf("payout id = %q, want nothing adopted onto a parked sell", got.PayoutID)
	}
}

func TestMinor_SupersededGenerationSuccessParksTheSell(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) {
		in.Asset = models.ITAssetUSDC
		in.PayoutRefGen = 1
		in.PayoutID = "trf_new"
		in.PayoutAttempts = 1
	})
	// The superseded generation's transfer reports success: the user may have
	// been paid twice.
	err := f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "trf_old", "success")
	if !errors.Is(err, ErrNonSettlingEvent) {
		t.Fatalf("err = %v, want a terminal acknowledgement", err)
	}
	if got := f.reload(t, in.ID); got.Status != models.ITStatusNeedsReview {
		t.Fatalf("status = %s, want needs_review", got.Status)
	}
}
