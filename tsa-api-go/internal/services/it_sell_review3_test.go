package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── B1: a parked stablecoin sell needs an exit too ─────────────────────────

// parkedStable is a stablecoin sell parked in needs_review — the state the
// superseded-generation guard leaves behind, which only ever happens to
// stablecoin sells (generations exist only via RepayStableSell).
func (f *sellFixture) parkedStable(t *testing.T, mutate func(*models.ITIntent)) *models.ITIntent {
	t.Helper()
	return f.seedSell(t, func(in *models.ITIntent) {
		in.Asset = models.ITAssetUSDC
		in.Status = models.ITStatusNeedsReview
		in.PayoutAttempts = 1
		in.PayoutRefGen = 1
		in.PayoutID = "trf_gen1"
		in.PlatformKeptKobo = "5000"
		if mutate != nil {
			mutate(in)
		}
	})
}

func TestB1_ReevaluateStableSell(t *testing.T) {
	t.Run("provider-confirmed success settles once", func(t *testing.T) {
		f := newSellFixture(t)
		sink := &countingFeeSink{}
		f.s.fees = sink
		in := f.parkedStable(t, nil)
		f.transfers.byID["trf_gen1"] = FWTransfer{ID: "trf_gen1", Reference: itPayoutRefFor(in), Status: "SUCCESSFUL"}

		got, err := f.s.ReevaluateSell(context.Background(), in.ID)
		if err != nil || got.Status != models.ITStatusSettled {
			t.Fatalf("status=%v err=%v, want settled", got, err)
		}
		if f.chain.settleCalls != 0 || f.chain.reverseCalls != 0 {
			t.Fatal("a brokered sell has no swap debit; nothing may be broadcast")
		}
		if _, err := f.s.ReevaluateSell(context.Background(), in.ID); err == nil {
			t.Fatal("a settled sell has nothing to reevaluate")
		}
		if sink.n != 1 {
			t.Fatalf("fee booked %d times, want once", sink.n)
		}
	})

	t.Run("provider-confirmed failure returns it to the operator repay path", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.parkedStable(t, nil)
		f.transfers.byID["trf_gen1"] = FWTransfer{ID: "trf_gen1", Reference: itPayoutRefFor(in), Status: "FAILED"}

		got, err := f.s.ReevaluateSell(context.Background(), in.ID)
		if err != nil {
			t.Fatalf("reevaluate: %v", err)
		}
		if got.Status != models.ITStatusDebited || !got.PayoutRejected {
			t.Fatalf("row = status %s rejected %v, want the parked-for-repay state", got.Status, got.PayoutRejected)
		}
		// And the repay path accepts it from there.
		f.transfers.byID["trf_gen1"] = FWTransfer{ID: "trf_gen1", Status: "FAILED"}
		if _, err := f.s.RepayStableSell(context.Background(), in.ID); err != nil {
			t.Fatalf("repay after reevaluate: %v", err)
		}
	})

	t.Run("two successful generations are never settled silently", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.parkedStable(t, nil)
		f.transfers.byID["trf_gen1"] = FWTransfer{ID: "trf_gen1", Reference: itPayoutRefFor(in), Status: "SUCCESSFUL"}
		// The superseded generation also paid.
		f.transfers.byRef[itPayoutRef(in.ID)] = FWTransfer{ID: "trf_gen0", Reference: itPayoutRef(in.ID), Status: "SUCCESSFUL"}

		out := logsDuring(t, func() {
			if _, err := f.s.ReevaluateSell(context.Background(), in.ID); err == nil {
				t.Fatal("a suspected double payment must not be closed automatically")
			}
		})
		if !strings.Contains(out, "trf_gen0") || !strings.Contains(out, "trf_gen1") {
			t.Fatalf("log = %q, want an ALERT naming both transfers", out)
		}
		if got := f.reload(t, in.ID); got.Status != models.ITStatusNeedsReview {
			t.Fatalf("status = %s, want it left parked for human accounting", got.Status)
		}
	})

	t.Run("non-terminal, absent and unreadable evidence all refuse", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(*sellFixture, *models.ITIntent)
		}{
			{"still pending", func(f *sellFixture, in *models.ITIntent) {
				f.transfers.byID["trf_gen1"] = FWTransfer{ID: "trf_gen1", Reference: itPayoutRefFor(in), Status: "PENDING"}
			}},
			{"provider unreachable", func(f *sellFixture, _ *models.ITIntent) {
				f.transfers.getErr = errors.New("HTTP 503")
			}},
			{"nothing recorded and nothing found", func(f *sellFixture, in *models.ITIntent) {
				f.s.db.Exec(`UPDATE it_intents SET payout_id = '' WHERE id = ?`, in.ID)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newSellFixture(t)
				in := f.parkedStable(t, nil)
				tc.setup(f, in)
				if _, err := f.s.ReevaluateSell(context.Background(), in.ID); err == nil {
					t.Fatal("want a refusal")
				}
				if got := f.reload(t, in.ID); got.Status != models.ITStatusNeedsReview {
					t.Fatalf("status = %s, want it left parked", got.Status)
				}
			})
		}
	})
}

// ─── B2: a webhook the provider never finalises must age out ────────────────

func seedFailedFWPayout(t *testing.T, db *gorm.DB, eventID, ref, transferID string, receivedAt time.Time) *models.WebhookEvent {
	t.Helper()
	body := []byte(`{
		"webhook_id": "` + eventID + `",
		"type": "transfer.disburse",
		"data": {"id": "` + transferID + `", "reference": "` + ref + `", "status": "SUCCESSFUL"}
	}`)
	row := &models.WebhookEvent{
		ID: uuid.New(), Provider: ProviderFlutterwave, EventID: eventID,
		Payload: body, Signature: "sig", Status: "failed", ReceivedAt: receivedAt,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed failed FW payout webhook: %v", err)
	}
	return row
}

// fakeNotFinalHooks stands in for the IT service: every payout webhook comes
// back "the provider has not finalised this yet".
type fakeNotFinalHooks struct{ fakeITHooks }

func (f *fakeNotFinalHooks) ResolvePayoutFromWebhook(_ context.Context, ref, payoutID, _ string) error {
	return fmt.Errorf("%w: transfer %s for %s is PENDING", ErrITPayoutNotFinal, payoutID, ref)
}

func TestB2_NonFinalPayoutWebhookAgesOut(t *testing.T) {
	cases := []struct {
		name       string
		receivedAt time.Time
		wantStatus string
	}{
		{"inside the grace keeps retrying", time.Now().Add(-itPayoutWebhookGrace + time.Hour), "failed"},
		{"past the grace goes terminal", time.Now().Add(-itPayoutWebhookGrace - time.Hour), "rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTradeDB(t)
			createPaymentMerchantsTable(t, db)
			createWebhookEventsTable(t, db)
			svc, esc, _ := newService(t, db)
			auto, _ := newPMAutomationService(t, db, esc, svc, nameMap(nil))
			auto.SetITHooks(&fakeNotFinalHooks{})

			row := seedFailedFWPayout(t, db, "fw-payout-"+tc.wantStatus, ITPayoutRefPrefix+"abc", "trf_lag", tc.receivedAt)
			out := logsDuring(t, func() {
				if _, err := auto.RetryFailedWebhooks(context.Background(), 5*time.Minute); err != nil {
					t.Fatalf("RetryFailedWebhooks: %v", err)
				}
			})

			var after models.WebhookEvent
			db.First(&after, "id = ?", row.ID)
			if after.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", after.Status, tc.wantStatus)
			}
			if tc.wantStatus == "rejected" {
				if after.ProcessedAt != nil {
					t.Error("an aged-out payout webhook settled nothing; it must not read as processed")
				}
				if !strings.Contains(out, "trf_lag") || !strings.Contains(out, ITPayoutRefPrefix+"abc") {
					t.Fatalf("log = %q, want an ALERT naming the reference and transfer", out)
				}
			}
		})
	}
}

// ─── B4: database clocks, and the transient admin status ────────────────────

func TestB4_InvisibleDebitAlertUsesTheDatabaseClock(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, nil)
	f.dropDebit(in)
	// Aged by the DATABASE's clock; the in-memory copy still looks fresh.
	if err := f.s.db.Exec(`UPDATE it_intents SET updated_at = datetime('now', '-90 minutes') WHERE id = ?`, in.ID).Error; err != nil {
		t.Fatal(err)
	}

	out := logsDuring(t, func() { _ = f.s.AutoPayout(context.Background(), in) })
	if !strings.Contains(out, "it_sell_debit_not_visible") {
		t.Fatalf("log = %q, want the alert decided from the database's clock", out)
	}
}

// dbNowExpr must be the DATABASE's now on every dialect we run. The
// integration test below cannot tell the two apart on SQLite (julianday()
// honours the stored offset, so an app-clock stamp converts to the same
// instant); this pins the SQL that is actually sent.
func TestB4_DatabaseNowExpressionPerDialect(t *testing.T) {
	cases := map[string]string{
		"sqlite":   "datetime('now')",
		"postgres": "NOW()",
	}
	for dialect, want := range cases {
		var db *gorm.DB
		switch dialect {
		case "sqlite":
			db = &gorm.DB{Config: &gorm.Config{Dialector: sqlite.Open(":memory:")}}
		default:
			db = &gorm.DB{Config: &gorm.Config{Dialector: postgres.Open("")}}
		}
		if got := dbNowExpr(db).SQL; got != want {
			t.Errorf("%s: dbNowExpr = %q, want %q", dialect, got, want)
		}
	}
}

func TestB4_LastPayoutAttemptIsStampedByTheDatabase(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, nil)
	if err := f.s.AutoPayout(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	var dbSkew float64
	if err := f.s.db.Raw(`SELECT (julianday('now') - julianday(last_payout_attempt_at)) * 86400.0 FROM it_intents WHERE id = ?`, in.ID).
		Scan(&dbSkew).Error; err != nil {
		t.Fatal(err)
	}
	// Weak by construction on SQLite (see TestB4_DatabaseNowExpressionPerDialect):
	// it pins that the column is written and sane, not which clock wrote it.
	if dbSkew > 120 || dbSkew < -120 {
		t.Fatalf("last_payout_attempt_at is %.0fs from the database's now — it must be stamped by the database", dbSkew)
	}
}
