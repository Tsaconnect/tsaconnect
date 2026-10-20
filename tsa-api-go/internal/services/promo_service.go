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
)

// PromoStatus is a buyer's progress on one listing's promotion.
type PromoStatus struct {
	Required  int        `json:"required"`
	Current   int        `json:"current"`
	Claimed   bool       `json:"claimed"`
	Expired   bool       `json:"expired"`
	Unlocked  bool       `json:"unlocked"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// PromoService decides whether a buyer has earned a listing's promo price.
// Only the server decides: checkout asks PriceFactor and never trusts a price
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
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&claim).Error
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
	st.Expired = claim.ExpiresAt != nil && time.Now().After(*claim.ExpiresAt)
	st.Current, err = s.graph.CountVerifiedDirectReferralsSince(ctx, userID, claim.ClaimedAt)
	if err != nil {
		return st, err
	}
	st.Unlocked = !st.Expired && st.Current >= st.Required
	return st, nil
}

// PriceFactor is the multiplier checkout applies to the listing price: the
// promo price over the original when the buyer has unlocked it, else 1 (full
// price, also after the countdown ran out).
func (s *PromoService) PriceFactor(ctx context.Context, p *models.Product, userID uuid.UUID) (float64, error) {
	if !p.PromoActive() {
		return 1, nil
	}
	st, err := s.Status(ctx, p, userID)
	if err != nil || !st.Unlocked {
		return 1, err
	}
	return p.PromoPrice / p.Price, nil
}
