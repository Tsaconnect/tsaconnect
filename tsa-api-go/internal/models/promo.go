package models

import (
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PromoClaim records that a buyer started a listing's promotion. Referrals
// count only when they signed up after ClaimedAt, so the unique key keeps a
// second tap from restarting the window. A merchant edit to the terms bumps
// Product.PromoVersion and lets the buyer claim again.
type PromoClaim struct {
	ID           uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	ProductID    uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_promo_claim_unique" json:"productId"`
	UserID       uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_promo_claim_unique;index" json:"userId"`
	PromoVersion int        `gorm:"not null;uniqueIndex:idx_promo_claim_unique" json:"promoVersion"`
	ClaimedAt    time.Time  `gorm:"not null" json:"claimedAt"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

func (PromoClaim) TableName() string { return "promo_claims" }

// PromoActive reports whether the listing currently offers a promotion.
func (p *Product) PromoActive() bool {
	return p.PromoEnabled &&
		p.Status == ProductStatusActive &&
		p.PromoRequiredReferrals >= 1 &&
		p.PromoPrice > 0 && p.PromoPrice < p.Price
}

// DiscountPercent is derived from the two prices and never stored.
func (p *Product) DiscountPercent() int {
	if !p.PromoActive() {
		return 0
	}
	return int(math.Round((1 - p.PromoPrice/p.Price) * 100))
}

// AfterFind fills the derived discount so every read path serialises it.
func (p *Product) AfterFind(_ *gorm.DB) error {
	p.PromoDiscountPercent = p.DiscountPercent()
	return nil
}

// PromoTerms are the merchant-editable promotion settings, compared on update
// to decide whether PromoVersion must change.
type PromoTerms struct {
	Enabled           bool
	RequiredReferrals int
	CountdownSeconds  *int
	Price             float64
}

func (p *Product) PromoTerms() PromoTerms {
	return PromoTerms{p.PromoEnabled, p.PromoRequiredReferrals, p.PromoCountdownSeconds, p.PromoPrice}
}

func (t PromoTerms) Equal(o PromoTerms) bool {
	if t.Enabled != o.Enabled || t.RequiredReferrals != o.RequiredReferrals || t.Price != o.Price {
		return false
	}
	if (t.CountdownSeconds == nil) != (o.CountdownSeconds == nil) {
		return false
	}
	return t.CountdownSeconds == nil || *t.CountdownSeconds == *o.CountdownSeconds
}
