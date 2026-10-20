package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

func setupPromoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupReferralGraphTestDB(t)
	if err := db.Exec(`CREATE TABLE promo_claims (
		id TEXT PRIMARY KEY,
		product_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		promo_version INTEGER NOT NULL,
		claimed_at DATETIME NOT NULL,
		expires_at DATETIME
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_promo_claim_unique ON promo_claims(product_id, user_id, promo_version)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func promoProduct(required int, countdown *int) *models.Product {
	return &models.Product{
		ID: uuid.New(), UserID: uuid.New(), Status: models.ProductStatusActive,
		Price: 2000, PromoEnabled: true, PromoPrice: 200,
		PromoRequiredReferrals: required, PromoCountdownSeconds: countdown,
	}
}

// refer adds a referral of buyer created at `at`, optionally verified.
func refer(t *testing.T, db *gorm.DB, name string, buyer uuid.UUID, at time.Time, verified bool) {
	t.Helper()
	u := mkUser(t, db, name, &buyer, models.AccountStatusActive)
	status := "pending"
	if verified {
		status = models.VerificationStatusVerified
	}
	if err := db.Exec(`UPDATE users SET created_at = ?, verification_status = ? WHERE id = ?`, at, status, u.ID.String()).Error; err != nil {
		t.Fatal(err)
	}
}

func TestPromo_CountsOnlyNewVerifiedReferralsAfterClaim(t *testing.T) {
	db := setupPromoTestDB(t)
	ctx := context.Background()
	svc := NewPromoService(db)
	buyer := mkUser(t, db, "buyer", nil, models.AccountStatusActive)
	p := promoProduct(2, nil)

	// A verified referral made BEFORE the claim never counts.
	refer(t, db, "old", buyer.ID, time.Now().Add(-48*time.Hour), true)

	st, err := svc.Claim(ctx, p, buyer.ID)
	if err != nil || !st.Claimed || st.Current != 0 || st.Unlocked {
		t.Fatalf("after claim: %+v err=%v", st, err)
	}

	refer(t, db, "unverified", buyer.ID, time.Now().Add(time.Second), false)
	refer(t, db, "new1", buyer.ID, time.Now().Add(time.Second), true)
	if st, _ = svc.Status(ctx, p, buyer.ID); st.Current != 1 || st.Unlocked {
		t.Fatalf("one verified referral should not unlock 2: %+v", st)
	}
	f, _ := svc.PriceFactor(ctx, p, buyer.ID)
	if f != 1 {
		t.Fatalf("factor before unlock = %v, want 1", f)
	}

	refer(t, db, "new2", buyer.ID, time.Now().Add(time.Second), true)
	st, _ = svc.Status(ctx, p, buyer.ID)
	if st.Current != 2 || !st.Unlocked {
		t.Fatalf("two verified referrals should unlock: %+v", st)
	}
	if f, _ = svc.PriceFactor(ctx, p, buyer.ID); f != 0.1 {
		t.Fatalf("factor after unlock = %v, want 0.1", f)
	}
}

func TestPromo_ExpiredCountdownPaysFullPrice(t *testing.T) {
	db := setupPromoTestDB(t)
	ctx := context.Background()
	svc := NewPromoService(db)
	buyer := mkUser(t, db, "buyer", nil, models.AccountStatusActive)
	secs := 120
	p := promoProduct(1, &secs)

	if _, err := svc.Claim(ctx, p, buyer.ID); err != nil {
		t.Fatal(err)
	}
	refer(t, db, "r1", buyer.ID, time.Now().Add(time.Second), true)
	// Run the clock out.
	db.Exec(`UPDATE promo_claims SET expires_at = ?`, time.Now().Add(-time.Minute))

	st, _ := svc.Status(ctx, p, buyer.ID)
	if !st.Expired || st.Unlocked {
		t.Fatalf("expired claim must not unlock: %+v", st)
	}
	if f, _ := svc.PriceFactor(ctx, p, buyer.ID); f != 1 {
		t.Fatalf("expired factor = %v, want 1 (full price)", f)
	}
}

func TestPromo_SecondClaimDoesNotRestartWindow(t *testing.T) {
	db := setupPromoTestDB(t)
	ctx := context.Background()
	svc := NewPromoService(db)
	buyer := mkUser(t, db, "buyer", nil, models.AccountStatusActive)
	secs := 60
	p := promoProduct(1, &secs)

	first, _ := svc.Claim(ctx, p, buyer.ID)
	db.Exec(`UPDATE promo_claims SET claimed_at = ?, expires_at = ?`, time.Now().Add(-time.Hour), time.Now().Add(-59*time.Minute))
	again, err := svc.Claim(ctx, p, buyer.ID)
	if err != nil || !again.Expired {
		t.Fatalf("re-claim must keep the expired window: %+v err=%v (first %+v)", again, err, first)
	}
	var n int64
	db.Model(&models.PromoClaim{}).Count(&n)
	if n != 1 {
		t.Fatalf("claims = %d, want 1", n)
	}
}

func TestPromo_NewTermsVersionAllowsNewClaim(t *testing.T) {
	db := setupPromoTestDB(t)
	ctx := context.Background()
	svc := NewPromoService(db)
	buyer := mkUser(t, db, "buyer", nil, models.AccountStatusActive)
	p := promoProduct(1, nil)

	svc.Claim(ctx, p, buyer.ID)
	p.PromoVersion++
	if st, _ := svc.Status(ctx, p, buyer.ID); st.Claimed {
		t.Fatalf("old-version claim must not apply: %+v", st)
	}
	if st, err := svc.Claim(ctx, p, buyer.ID); err != nil || !st.Claimed {
		t.Fatalf("new version claim: %+v err=%v", st, err)
	}
}

func TestPromo_RefusesOwnerAndInactive(t *testing.T) {
	db := setupPromoTestDB(t)
	ctx := context.Background()
	svc := NewPromoService(db)
	p := promoProduct(1, nil)

	if _, err := svc.Claim(ctx, p, p.UserID); err != ErrPromoOwn {
		t.Fatalf("owner claim err = %v", err)
	}
	p.PromoPrice = 2500 // not below the price: promo is not active
	if _, err := svc.Claim(ctx, p, uuid.New()); err != ErrPromoInactive {
		t.Fatalf("inactive claim err = %v", err)
	}
	if f, _ := svc.PriceFactor(ctx, p, uuid.New()); f != 1 {
		t.Fatalf("inactive factor = %v", f)
	}
}

func TestProduct_DiscountPercent(t *testing.T) {
	p := promoProduct(5, nil)
	if got := p.DiscountPercent(); got != 90 {
		t.Fatalf("discount = %d, want 90", got)
	}
}
