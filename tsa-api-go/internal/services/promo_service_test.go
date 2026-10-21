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
		expires_at DATETIME,
		order_id TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE orders (id TEXT PRIMARY KEY, status TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_promo_claim_unique ON promo_claims(product_id, user_id, promo_version)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func promoProduct(required int, countdown *int) *models.Product {
	return &models.Product{
		ID: uuid.New(), UserID: uuid.New(), Status: models.ProductStatusActive, Type: models.ProductTypeProduct,
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
	pp, _ := svc.Pricing(ctx, p, buyer.ID)
	if f := pp.Factor; f != 1 {
		t.Fatalf("factor before unlock = %v, want 1", f)
	}

	refer(t, db, "new2", buyer.ID, time.Now().Add(time.Second), true)
	st, _ = svc.Status(ctx, p, buyer.ID)
	if st.Current != 2 || !st.Unlocked {
		t.Fatalf("two verified referrals should unlock: %+v", st)
	}
	if pp, _ = svc.Pricing(ctx, p, buyer.ID); pp.Factor != 0.1 || pp.ClaimID == nil {
		t.Fatalf("pricing after unlock = %+v, want 0.1 with a claim", pp)
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
	if pp, _ := svc.Pricing(ctx, p, buyer.ID); pp.Factor != 1 {
		t.Fatalf("expired factor = %v, want 1 (full price)", pp.Factor)
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
	if pp, _ := svc.Pricing(ctx, p, uuid.New()); pp.Factor != 1 {
		t.Fatalf("inactive factor = %v", pp.Factor)
	}
}

func TestProduct_DiscountPercent(t *testing.T) {
	p := promoProduct(5, nil)
	if got := p.DiscountPercent(); got != 90 {
		t.Fatalf("discount = %d, want 90", got)
	}
}

// unlock claims p for buyer and gives them one verified referral after the claim.
func unlock(t *testing.T, db *gorm.DB, svc *PromoService, p *models.Product, buyer models.User, name string) PromoPricing {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.Claim(ctx, p, buyer.ID); err != nil {
		t.Fatal(err)
	}
	refer(t, db, name, buyer.ID, time.Now().Add(time.Second), true)
	pp, err := svc.Pricing(ctx, p, buyer.ID)
	if err != nil || pp.ClaimID == nil {
		t.Fatalf("not unlocked: %+v err=%v", pp, err)
	}
	return pp
}

func TestPromo_ClaimBuysOneItemThenNeedsNewReferrals(t *testing.T) {
	db := setupPromoTestDB(t)
	ctx := context.Background()
	svc := NewPromoService(db)
	buyer := mkUser(t, db, "buyer", nil, models.AccountStatusActive)
	p := promoProduct(1, nil)

	pp := unlock(t, db, svc, p, buyer, "r1")
	order := uuid.New()
	db.Exec(`INSERT INTO orders (id, status) VALUES (?, 'pending_payment')`, order.String())
	if err := svc.Redeem(ctx, *pp.ClaimID, order); err != nil {
		t.Fatal(err)
	}
	// A second order on the same claim loses, whatever raced it.
	if err := svc.Redeem(ctx, *pp.ClaimID, uuid.New()); err != ErrPromoUsed {
		t.Fatalf("second redeem err = %v, want ErrPromoUsed", err)
	}
	st, _ := svc.Status(ctx, p, buyer.ID)
	if !st.Redeemed || st.Unlocked {
		t.Fatalf("after use: %+v", st)
	}
	if pp2, _ := svc.Pricing(ctx, p, buyer.ID); pp2.Factor != 1 || pp2.ClaimID != nil {
		t.Fatalf("used claim must price at full: %+v", pp2)
	}

	// Tapping again starts a fresh window: the earlier referral does not count.
	db.Exec(`UPDATE users SET created_at = ? WHERE username = 'r1'`, time.Now().Add(-time.Minute))
	st, err := svc.Claim(ctx, p, buyer.ID)
	if err != nil || !st.Claimed || st.Redeemed || st.Current != 0 || st.Unlocked {
		t.Fatalf("fresh window: %+v err=%v", st, err)
	}
	unlock2 := time.Now().Add(time.Second)
	refer(t, db, "r2", buyer.ID, unlock2, true)
	if pp3, _ := svc.Pricing(ctx, p, buyer.ID); pp3.ClaimID == nil {
		t.Fatal("a new verified referral should unlock the next purchase")
	}
}

func TestPromo_CancelledOrderFreesTheClaim(t *testing.T) {
	db := setupPromoTestDB(t)
	ctx := context.Background()
	svc := NewPromoService(db)
	buyer := mkUser(t, db, "buyer", nil, models.AccountStatusActive)
	p := promoProduct(1, nil)

	pp := unlock(t, db, svc, p, buyer, "r1")
	order := uuid.New()
	db.Exec(`INSERT INTO orders (id, status) VALUES (?, 'pending_payment')`, order.String())
	if err := svc.Redeem(ctx, *pp.ClaimID, order); err != nil {
		t.Fatal(err)
	}
	db.Exec(`UPDATE orders SET status = 'cancelled' WHERE id = ?`, order.String())

	if pp2, _ := svc.Pricing(ctx, p, buyer.ID); pp2.ClaimID == nil {
		t.Fatal("an unpaid order that was cancelled must give the discount back")
	}
	if err := svc.Redeem(ctx, *pp.ClaimID, uuid.New()); err != nil {
		t.Fatalf("redeem after cancel: %v", err)
	}
}

func TestPromo_ServicesAreNeverOnPromotion(t *testing.T) {
	p := promoProduct(1, nil)
	p.Type = models.ProductTypeService
	if p.PromoActive() {
		t.Fatal("a service must not be promotable")
	}
}
