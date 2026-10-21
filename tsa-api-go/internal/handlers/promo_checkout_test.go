package handlers

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// listingPriceWei runs a one-item checkout for buyer and returns what the
// buyer is charged for the item: productAmount + platformFee.
func listingPriceWei(t *testing.T, db *gorm.DB, buyer, seller *models.User, product *models.Product, qty int) *big.Int {
	t.Helper()
	ch := setupCheckoutHandler(t)
	createTestCart(t, db, buyer.ID, []models.CartItem{{
		ID: uuid.New(), Product: product.ID, Seller: seller.ID,
		Quantity: qty, Price: product.Price, AddedAt: time.Now(),
	}})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	body, _ := json.Marshal(map[string]interface{}{
		"token": "USDC", "buyerCity": "Lagos", "buyerState": "Lagos", "buyerCountry": "Nigeria",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ch.CreateOrderFromCart(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Orders []struct {
				ProductAmount string `json:"productAmount"`
				PlatformFee   string `json:"platformFee"`
			} `json:"orders"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || len(resp.Data.Orders) != 1 {
		t.Fatalf("response: %v %s", err, w.Body.String())
	}
	pa, _ := new(big.Int).SetString(resp.Data.Orders[0].ProductAmount, 10)
	fee, _ := new(big.Int).SetString(resp.Data.Orders[0].PlatformFee, 10)
	return pa.Add(pa, fee)
}

func TestCheckout_ChargesPromoPriceOnlyOnceUnlocked(t *testing.T) {
	db := setupCheckoutTestDB(t)
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS promo_claims (
		id TEXT PRIMARY KEY, product_id TEXT NOT NULL, user_id TEXT NOT NULL,
		promo_version INTEGER NOT NULL, claimed_at DATETIME NOT NULL, expires_at DATETIME, order_id TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 10.0)
	db.Model(product).Updates(map[string]interface{}{
		"promo_enabled": true, "promo_required_referrals": 1, "promo_price": 1.0,
	})

	full := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	fullPrice := listingPriceWei(t, db, full, seller, product, 1)

	// Claimed, but no verified referral yet: still full price.
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	db.Exec(`INSERT INTO promo_claims (id, product_id, user_id, promo_version, claimed_at) VALUES (?,?,?,?,?)`,
		uuid.NewString(), product.ID.String(), buyer.ID.String(), 0, time.Now().Add(-time.Hour))
	if got := listingPriceWei(t, db, buyer, seller, product, 1); got.Cmp(fullPrice) != 0 {
		t.Fatalf("locked buyer paid %s, want full %s", got, fullPrice)
	}

	// One verified referral after the claim unlocks the promo price (1/10th).
	ref := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	db.Exec(`UPDATE users SET referred_by = ?, verification_status = ?, created_at = ? WHERE id = ?`,
		buyer.ID.String(), models.VerificationStatusVerified, time.Now(), ref.ID.String())
	// Two units: only ONE is discounted, so 1/10 + 1 units' worth.
	got := listingPriceWei(t, db, buyer, seller, product, 2)
	want := new(big.Int).Add(fullPrice, new(big.Int).Div(fullPrice, big.NewInt(10)))
	if diff := new(big.Int).Abs(new(big.Int).Sub(got, want)); diff.Cmp(big.NewInt(10)) > 0 {
		t.Fatalf("unlocked buyer paid %s for 2 units, want ~%s (full unit %s)", got, want, fullPrice)
	}

	// The order spent the claim: the next purchase is full price again.
	if again := listingPriceWei(t, db, buyer, seller, product, 1); again.Cmp(fullPrice) != 0 {
		t.Fatalf("second purchase paid %s, want full %s", again, fullPrice)
	}
}

func TestListPromotions_OnlyActivePromotedListings(t *testing.T) {
	db := setupCheckoutTestDB(t)
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")

	promoted := createTestProduct(t, db, seller.ID, 100)
	db.Model(promoted).Updates(map[string]interface{}{"promo_enabled": true, "promo_required_referrals": 2, "promo_price": 10.0})
	createTestProduct(t, db, seller.ID, 100) // not promoted
	paused := createTestProduct(t, db, seller.ID, 100)
	db.Model(paused).Updates(map[string]interface{}{"promo_enabled": true, "promo_required_referrals": 2, "promo_price": 10.0, "status": "inactive"})
	bad := createTestProduct(t, db, seller.ID, 100) // promo price not below price
	db.Model(bad).Updates(map[string]interface{}{"promo_enabled": true, "promo_required_referrals": 2, "promo_price": 100.0})

	h := &Handlers{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("GET", "/promotions", nil)
	h.ListPromotions(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Products []struct {
				ID                   string `json:"id"`
				PromoDiscountPercent int    `json:"promoDiscountPercent"`
			} `json:"products"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Products) != 1 || resp.Data.Products[0].ID != promoted.ID.String() {
		t.Fatalf("feed = %+v, want only %s", resp.Data.Products, promoted.ID)
	}
	if resp.Data.Products[0].PromoDiscountPercent != 90 {
		t.Fatalf("discount = %d, want 90", resp.Data.Products[0].PromoDiscountPercent)
	}
}
