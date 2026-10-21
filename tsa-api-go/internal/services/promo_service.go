package services

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrPromoInactive = errors.New("listing has no active promotion")
	ErrPromoOwn      = errors.New("you can't claim the promotion on your own listing")
	ErrPromoUsed     = errors.New("this promotion has already been used")
)

// PromoStatus is a buyer's progress on one listing's promotion.
type PromoStatus struct {
	Required int  `json:"required"`
	Current  int  `json:"current"`
	Claimed  bool `json:"claimed"`
	// Redeemed: the claim already bought its one discounted item.
	Redeemed  bool       `json:"redeemed"`
	Expired   bool       `json:"expired"`
	Unlocked  bool       `json:"unlocked"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// PromoService decides whether a buyer has earned a listing's promo price.
// Only the server decides: checkout asks Pricing and never trusts a price
// the client displayed.
type PromoService struct {
	db    *gorm.DB
	graph *ReferralGraphService
}

func NewPromoService(db *gorm.DB) *PromoService {
	return &PromoService{db: db, graph: NewReferralGraphService(db)}
}

// Claim starts the buyer's window. It is idempotent per (listing, buyer, terms
// version): a second tap returns the original claim, so the clock never restarts.
func (s *PromoService) Claim(ctx context.Context, p *models.Product, userID uuid.UUID) (PromoStatus, error) {
	if !p.PromoActive() {
		return PromoStatus{}, ErrPromoInactive
	}
	if p.UserID == userID {
		return PromoStatus{}, ErrPromoOwn
	}
	now := time.Now()
	claim := models.PromoClaim{
		ID: uuid.New(), ProductID: p.ID, UserID: userID,
		PromoVersion: p.PromoVersion, ClaimedAt: now,
	}
	if p.PromoCountdownSeconds != nil && *p.PromoCountdownSeconds > 0 {
		exp := now.Add(time.Duration(*p.PromoCountdownSeconds) * time.Second)
		claim.ExpiresAt = &exp
	}
	db := s.db.WithContext(ctx)
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim).Error; err != nil {
		return PromoStatus{}, err
	}
	// A claim that already bought its discounted item starts over: a fresh
	// window, so the referrals that earned the first purchase never count twice.
	// An unredeemed claim (expired or not) is left alone; tapping again never
	// restarts the clock.
	err := db.Model(&models.PromoClaim{}).
		Where("product_id = ? AND user_id = ? AND promo_version = ?", p.ID, userID, p.PromoVersion).
		Where("order_id IN (SELECT id FROM orders WHERE status <> ?)", models.OrderStatusCancelled).
		Updates(map[string]interface{}{"claimed_at": now, "expires_at": claim.ExpiresAt, "order_id": nil}).Error
	if err != nil {
		return PromoStatus{}, err
	}
	return s.Status(ctx, p, userID)
}

// Status reports progress without creating a claim.
func (s *PromoService) Status(ctx context.Context, p *models.Product, userID uuid.UUID) (PromoStatus, error) {
	st := PromoStatus{Required: p.PromoRequiredReferrals}
	if !p.PromoActive() {
		return st, nil
	}
	var claim models.PromoClaim
	err := s.db.WithContext(ctx).
		Where("product_id = ? AND user_id = ? AND promo_version = ?", p.ID, userID, p.PromoVersion).
		First(&claim).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Claimed = true
	st.ExpiresAt = claim.ExpiresAt
	if claim.OrderID != nil {
		var live int64
		if err := s.db.WithContext(ctx).Table("orders").
			Where("id = ? AND status <> ?", *claim.OrderID, models.OrderStatusCancelled).
			Count(&live).Error; err != nil {
			return st, err
		}
		st.Redeemed = live > 0
	}
	st.Expired = claim.ExpiresAt != nil && time.Now().After(*claim.ExpiresAt)
	st.Current, err = s.graph.CountVerifiedDirectReferralsSince(ctx, userID, claim.ClaimedAt)
	if err != nil {
		return st, err
	}
	st.Unlocked = !st.Redeemed && !st.Expired && st.Current >= st.Required
	return st, nil
}

// PromoPricing is what checkout applies for one buyer and listing.
type PromoPricing struct {
	// Factor multiplies the unit price: promo price over original once unlocked,
	// else 1 (full price, also after the countdown ran out or the claim was used).
	Factor float64
	// ClaimID is set only when Factor discounts; the order must Redeem it.
	ClaimID *uuid.UUID
}

func (s *PromoService) Pricing(ctx context.Context, p *models.Product, userID uuid.UUID) (PromoPricing, error) {
	full := PromoPricing{Factor: 1}
	if !p.PromoActive() {
		return full, nil
	}
	st, err := s.Status(ctx, p, userID)
	if err != nil || !st.Unlocked {
		return full, err
	}
	var claim models.PromoClaim
	if err := s.db.WithContext(ctx).
		Where("product_id = ? AND user_id = ? AND promo_version = ?", p.ID, userID, p.PromoVersion).
		First(&claim).Error; err != nil {
		return full, err
	}
	return PromoPricing{Factor: p.PromoPrice / p.Price, ClaimID: &claim.ID}, nil
}

// Redeem spends the claim on an order, in the order's own transaction. The
// conditional update is the guard: two orders racing on one claim cannot both
// win, and a claim whose order was cancelled can be spent again.
func (s *PromoService) Redeem(ctx context.Context, claimID, orderID uuid.UUID) error {
	res := s.db.WithContext(ctx).Exec(
		`UPDATE promo_claims SET order_id = ? WHERE id = ? AND (order_id IS NULL OR order_id IN (SELECT id FROM orders WHERE status = ?))`,
		orderID, claimID, models.OrderStatusCancelled)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrPromoUsed
	}
	return nil
}
