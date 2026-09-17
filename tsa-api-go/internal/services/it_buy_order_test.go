package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// One open MCGP buy per user, a user-facing cancel for it, and the settle race
// between the two.
//
// Superseding the prior order on a new one made the first order vanish from
// the hub. Under central-account attribution that is worse than confusing: the
// payer's transfer for the first order's amount now matches no open claim and
// holds for an operator with the money already gone.

// raceBeforeNextITIntentUpdate runs sql on the SAME connection immediately
// before the next UPDATE against it_intents, exactly once. It stands in for a
// concurrent writer committing between a read and a guarded write — the only
// way a CAS clause is observable in a single-threaded test.
func raceBeforeNextITIntentUpdate(t *testing.T, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	fired := false
	name := "test:race:" + uuid.NewString()
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "it_intents" {
			return
		}
		fired = true
		if _, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context, sql, args...); err != nil {
			t.Errorf("race write: %v", err)
		}
	}); err != nil {
		t.Fatalf("register race callback: %v", err)
	}
}

// openMCGPBuyIndexDDLForTest is uq_it_intents_open_mcgp_buy as
// config/database.go builds it. config cannot be imported here without a cycle,
// so the text is repeated; TestAutoMigrateAddsTheOpenMCGPBuyIndex pins the
// production copy. SQLite supports partial indexes, so the race is real here.
const openMCGPBuyIndexDDLForTest = `CREATE UNIQUE INDEX IF NOT EXISTS uq_it_intents_open_mcgp_buy
	ON it_intents (user_id)
	WHERE direction = 'buy' AND status = 'pending_payment'
	  AND (asset IS NULL OR asset = '' OR asset = 'MCGP')
	  AND payment_ref LIKE 'IT-%'`

func reloadIntent(t *testing.T, s *ITService, id uuid.UUID) models.ITIntent {
	t.Helper()
	var got models.ITIntent
	if err := s.db.First(&got, "id = ?", id).Error; err != nil {
		t.Fatalf("reload intent %s: %v", id, err)
	}
	return got
}

// ─── one open order ────────────────────────────────────────────────────────

func TestOpenBuyOrder_RefusesWhileAnOrderIsOpenAndNamesIt(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	first, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatalf("open first: %v", err)
	}

	_, err = s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if !errors.Is(err, ErrITBuyOrderOpen) {
		t.Fatalf("second OpenBuyOrder = %v, want ErrITBuyOrderOpen", err)
	}
	var open *ITBuyOrderOpenError
	if !errors.As(err, &open) || open.Intent == nil {
		t.Fatalf("refusal carries no order to route to: %#v", err)
	}
	if open.Intent.ID != first.ID || open.Intent.NgnKobo != first.NgnKobo {
		t.Errorf("refusal names %s/%s, want the open order %s/%s",
			open.Intent.ID, open.Intent.NgnKobo, first.ID, first.NgnKobo)
	}

	if got := reloadIntent(t, s, first.ID); got.Status != models.ITStatusPendingPayment {
		t.Errorf("first order status = %q — a new order must never close the one the user may be paying", got.Status)
	}
	if rows := buyIntents(t, s, user.ID); len(rows) != 1 {
		t.Errorf("%d buy intents exist, want 1", len(rows))
	}
}

// Asking again for the SAME amount is the user coming back to the order they
// already have, not a second order. Answer with that order. This is also the
// only way a build that predates the open-order screen can get back to the
// pay-into details for it: that build shows the 409 message and nothing else.
func TestOpenBuyOrder_SameAmountReturnsTheOpenOrder(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	first, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	// 99,960 kobo snaps to the same whole naira the first order was minted at.
	again, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "99960")
	if err != nil {
		t.Fatalf("same-amount re-open = %v, want the open order back", err)
	}
	if again == nil || again.ID != first.ID {
		t.Fatalf("re-open returned %v, want the open order %s", again, first.ID)
	}
	if rows := buyIntents(t, s, user.ID); len(rows) != 1 {
		t.Errorf("%d buy intents exist, want 1", len(rows))
	}
}

// An order minted before whole-naira snapping carries kobo. It is offered to
// the matcher, and shown on the pay-into card, at its rounded naira — so that
// is the amount that counts as "the same order" too.
func TestOpenBuyOrder_SameAmountMatchesAPreSnapOrderByWholeNaira(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	first, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", first.ID).
		Update("ngn_kobo", "99960").Error; err != nil {
		t.Fatal(err)
	}
	again, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil || again == nil || again.ID != first.ID {
		t.Fatalf("re-open = %v, %v; want the pre-snap order %s back", again, err, first.ID)
	}
}

// Another user's open order is not this user's business.
func TestOpenBuyOrder_OtherUsersOpenOrderDoesNotBlock(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()
	other := seedVerifiedUser(t, s.db)

	if _, err := s.OpenBuyOrder(ctx, other, "0xOTHER", "100000"); err != nil {
		t.Fatalf("other user's order: %v", err)
	}
	if _, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000"); err != nil {
		t.Fatalf("blocked by another user's open order: %v", err)
	}
}

// An order past the claim window can no longer be paid into, so it must not
// wedge the user out of buying until the hourly sweep gets to it.
func TestOpenBuyOrder_ExpiresTheUsersStaleOrderInsteadOfRefusing(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	stale, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatalf("open stale: %v", err)
	}
	ageOrder(t, s, stale.ID, buyOrderClaimWindow+time.Hour)

	fresh, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if err != nil {
		t.Fatalf("refused behind an expired order: %v", err)
	}
	if fresh.Status != models.ITStatusPendingPayment {
		t.Errorf("fresh status = %q", fresh.Status)
	}
	if got := reloadIntent(t, s, stale.ID); got.Status != models.ITStatusCancelled {
		t.Errorf("stale order status = %q, want cancelled", got.Status)
	}
}

// The count-then-create is not atomic. A double tap that slips past the count
// trips the partial unique index instead, and must read as the same refusal —
// not a raw driver error rendered as a 400.
func TestOpenBuyOrder_UniqueIndexRaceReadsAsOrderOpen(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()
	if err := s.db.Exec(openMCGPBuyIndexDDLForTest).Error; err != nil {
		t.Fatalf("index ddl: %v", err)
	}
	// No wrapping transaction around the Create, so the winner's insert below
	// commits on its own instead of rolling back with the loser's.
	s.db = s.db.Session(&gorm.Session{SkipDefaultTransaction: true})

	winner := uuid.New()
	fired := false
	if err := s.db.Callback().Create().Before("gorm:create").Register("test:race:double-tap", func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "it_intents" {
			return
		}
		fired = true
		// The other tap's insert, committed after this request's count.
		if _, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context,
			`INSERT INTO it_intents (id, user_id, direction, status, asset, currency, mcgp_amount, ngn_kobo, rate_kobo, payment_ref, created_at, updated_at)
			 VALUES (?, ?, 'buy', 'pending_payment', '', 'NGN', '1', '100000', '1', ?, ?, ?)`,
			winner, user.ID, mintedRefPrefix+"WINNERTAP0001", time.Now(), time.Now()); err != nil {
			t.Errorf("winner insert: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}

	// A different amount from the winner's ₦1,000: the same amount would be
	// answered with the winner's order (reopenOrRefuse), not refused.
	_, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if !fired {
		t.Fatal("race hook never ran")
	}
	var open *ITBuyOrderOpenError
	if !errors.As(err, &open) {
		t.Fatalf("OpenBuyOrder = %v, want ITBuyOrderOpenError from the unique violation", err)
	}
	if open.Intent == nil || open.Intent.ID != winner {
		t.Errorf("refusal names %v, want the winning tap's order %s", open.Intent, winner)
	}
}

// ─── cancel ────────────────────────────────────────────────────────────────

func TestCancelBuy_CancelsAnOpenMCGPOrder(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CancelBuy(ctx, user.ID, order.ID)
	if err != nil {
		t.Fatalf("CancelBuy: %v", err)
	}
	if got == nil || got.Status != models.ITStatusCancelled {
		t.Fatalf("returned intent = %+v, want the reloaded cancelled row", got)
	}
	row := reloadIntent(t, s, order.ID)
	if row.Status != models.ITStatusCancelled || row.FailureReason != "cancelled by user" {
		t.Errorf("row = %s/%q, want cancelled/\"cancelled by user\"", row.Status, row.FailureReason)
	}
	// Cancelling is the way out of the one-open-order rule.
	if _, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000"); err != nil {
		t.Fatalf("cancelled order still blocks a new one: %v", err)
	}
}

func TestCancelBuy_RefusesAnotherUsersOrder(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelBuy(ctx, uuid.New(), order.ID); !errors.Is(err, ErrStableIntentNotFound) {
		t.Fatalf("err = %v, want ErrStableIntentNotFound", err)
	}
	if got := reloadIntent(t, s, order.ID); got.Status != models.ITStatusPendingPayment {
		t.Errorf("another user's cancel moved the order to %q", got.Status)
	}
}

// A sell is not a buy; the endpoint is for abandoning an unpaid purchase.
func TestCancelBuy_RefusesASell(t *testing.T) {
	s, user, _ := newVATestService(t)
	sell := &models.ITIntent{
		ID: uuid.New(), UserID: user.ID, Direction: models.ITDirectionSell,
		Status: models.ITStatusPendingPayment, Currency: "NGN",
		McgpAmount: "1", NgnKobo: "100000", RateKobo: "1", PaymentRef: mintedRefPrefix + "SELLROW000001",
	}
	if err := s.db.Create(sell).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelBuy(context.Background(), user.ID, sell.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState", err)
	}
}

// The CAS clauses, each proven by a writer committing between the read and the
// guarded UPDATE.

func TestCancelBuy_LosesToAPaymentThatClaimedTheOrderFirst(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()
	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatal(err)
	}

	// Settlement moved it on: status is no longer pending_payment.
	raceBeforeNextITIntentUpdate(t, s.db, `UPDATE it_intents SET status = 'processing' WHERE id = ?`, order.ID)

	if _, err := s.CancelBuy(ctx, user.ID, order.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState", err)
	}
	if got := reloadIntent(t, s, order.ID); got.Status != models.ITStatusProcessing {
		t.Errorf("status = %q — a cancel overwrote an order a payment is settling", got.Status)
	}
}

func TestCancelBuy_LosesToAPaymentThatAttributedItsRef(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()
	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatal(err)
	}

	// Settlement wrote the provider ref but has not moved status yet.
	raceBeforeNextITIntentUpdate(t, s.db, `UPDATE it_intents SET payment_ref = ? WHERE id = ?`, testInboundRef, order.ID)

	if _, err := s.CancelBuy(ctx, user.ID, order.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState", err)
	}
	if got := reloadIntent(t, s, order.ID); got.Status != models.ITStatusPendingPayment {
		t.Errorf("status = %q — cancelled an order money is already attributed to", got.Status)
	}
}

func TestCancelBuy_CASIsScopedToTheOwner(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()
	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatal(err)
	}

	stranger := uuid.New()
	raceBeforeNextITIntentUpdate(t, s.db, `UPDATE it_intents SET user_id = ? WHERE id = ?`, stranger, order.ID)

	if _, err := s.CancelBuy(ctx, user.ID, order.ID); err == nil {
		t.Fatal("cancelled a row that no longer belongs to the caller")
	}
	if got := reloadIntent(t, s, order.ID); got.Status != models.ITStatusPendingPayment {
		t.Errorf("status = %q — the CAS wrote to a row owned by someone else", got.Status)
	}
}

// ─── late-payment guard ────────────────────────────────────────────────────

func seedInboundCredit(t *testing.T, s *ITService, status, senderName string, grossKobo int64, ingestedAt time.Time) {
	t.Helper()
	c := &models.InboundCredit{
		ID: uuid.New(), Provider: models.InboundCreditProviderFlutterwave,
		ProviderRef: uuid.NewString(), Currency: "NGN",
		AmountKobo: grossKobo, GrossKobo: grossKobo,
		SenderRaw: "Received Money from GTBANK|" + senderName, SenderBank: "GTBANK", SenderName: senderName,
		TransactionDate: ingestedAt, SeenAt: ingestedAt, Status: status,
		CreatedAt: ingestedAt, UpdatedAt: ingestedAt,
	}
	if err := s.db.Create(c).Error; err != nil {
		t.Fatalf("seed inbound credit: %v", err)
	}
}

func TestCancelBuy_RefusesWhenAMatchingPaymentHasArrived(t *testing.T) {
	for _, status := range []string{models.InboundCreditUnmatched, models.InboundCreditHeld} {
		t.Run(status, func(t *testing.T) {
			s, user, _ := newVATestService(t)
			ctx := context.Background()
			order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "250000")
			if err != nil {
				t.Fatal(err)
			}
			// Bank-style account name: surname first, upper case — the matcher's
			// own name function says this is Ada Lovelace.
			seedInboundCredit(t, s, status, "LOVELACE ADA", 250_000, time.Now().Add(time.Minute))

			if _, err := s.CancelBuy(ctx, user.ID, order.ID); !errors.Is(err, ErrITBuyPaymentArrived) {
				t.Fatalf("err = %v, want ErrITBuyPaymentArrived", err)
			}
			if got := reloadIntent(t, s, order.ID); got.Status != models.ITStatusPendingPayment {
				t.Errorf("status = %q — cancelled an order whose payment is sitting in the queue", got.Status)
			}
		})
	}
}

func TestCancelBuy_IgnoresPaymentsThatAreNotThisOrders(t *testing.T) {
	past := func(order *models.ITIntent) time.Time { return order.CreatedAt.Add(-time.Minute) }
	future := func(order *models.ITIntent) time.Time { return time.Now().Add(time.Minute) }
	for _, tc := range []struct {
		name   string
		status string
		sender string
		gross  int64
		at     func(*models.ITIntent) time.Time
	}{
		{"different sender", models.InboundCreditHeld, "CHARLES BABBAGE", 250_000, future},
		{"different amount", models.InboundCreditHeld, "LOVELACE ADA", 260_000, future},
		{"ingested before the order", models.InboundCreditHeld, "LOVELACE ADA", 250_000, past},
		{"already matched", models.InboundCreditMatched, "LOVELACE ADA", 250_000, future},
		{"refunded", models.InboundCreditRefunded, "LOVELACE ADA", 250_000, future},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, user, _ := newVATestService(t)
			ctx := context.Background()
			order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "250000")
			if err != nil {
				t.Fatal(err)
			}
			seedInboundCredit(t, s, tc.status, tc.sender, tc.gross, tc.at(order))

			if _, err := s.CancelBuy(ctx, user.ID, order.ID); err != nil {
				t.Fatalf("CancelBuy refused over an unrelated credit: %v", err)
			}
		})
	}
}

// Nothing to compare a sender against, so nothing to refuse on.
func TestCancelBuy_NoVerifiedNameSkipsTheGuard(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()
	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "250000")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(`UPDATE users SET verified_legal_name = '' WHERE id = ?`, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	seedInboundCredit(t, s, models.InboundCreditHeld, "LOVELACE ADA", 250_000, time.Now().Add(time.Minute))

	if _, err := s.CancelBuy(ctx, user.ID, order.ID); err != nil {
		t.Fatalf("CancelBuy = %v, want the cancel to proceed", err)
	}
}

// The old stablecoin route keeps its scope: it must not become a second way to
// cancel an MCGP order, and it still cancels its own.
func TestCancelStableBuy_StillRefusesAnMCGPOrder(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()
	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CancelStableBuy(ctx, user.ID, order.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState", err)
	}
}

// ─── settle race ───────────────────────────────────────────────────────────

// CreditBuyOrderFromInbound reads the order, prices it, then writes the
// provider ref and the re-priced amounts. A cancel committing in between used
// to leave a CANCELLED row carrying a provider ref and amounts nobody agreed to.
func TestCreditBuyOrderFromInbound_CancelBetweenReadAndWriteLeavesTheRowUnchanged(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()
	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if err != nil {
		t.Fatal(err)
	}
	before := reloadIntent(t, s, order.ID)

	raceBeforeNextITIntentUpdate(t, s.db,
		`UPDATE it_intents SET status = 'cancelled', failure_reason = 'cancelled by user' WHERE id = ?`, order.ID)

	if err := src.Accept(ctx, order.ID, inboundCreditFor(testInboundRef, 500_000)); err == nil {
		t.Fatal("Accept reported success settling into an order cancelled underneath it")
	}

	after := reloadIntent(t, s, order.ID)
	if after.Status != models.ITStatusCancelled {
		t.Errorf("status = %q, want the cancel to stand", after.Status)
	}
	if after.PaymentRef != before.PaymentRef {
		t.Errorf("payment_ref = %q, want the minted %q — a cancelled row took the provider ref", after.PaymentRef, before.PaymentRef)
	}
	if after.NgnKobo != before.NgnKobo || after.McgpAmount != before.McgpAmount ||
		after.RateKobo != before.RateKobo || after.FeeNgnKobo != before.FeeNgnKobo ||
		after.NetNgnKobo != before.NetNgnKobo {
		t.Errorf("cancelled row was re-priced: ngn %s→%s mcgp %s→%s rate %s→%s",
			before.NgnKobo, after.NgnKobo, before.McgpAmount, after.McgpAmount, before.RateKobo, after.RateKobo)
	}
}
