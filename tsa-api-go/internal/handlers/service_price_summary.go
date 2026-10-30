package handlers

import (
	"math"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// populateServicePrices sets ServicePrice on every service listing in products,
// so a card or detail view can say "From $50 / night" instead of the listing's
// Price (0 for a booking, whose fare lives in the per-area fee columns).
// Profiles, categories, menus and legacy category fares are read in batches.
func populateServicePrices(db *gorm.DB, products []models.Product) {
	var ids, catIDs []uuid.UUID
	seenCat := map[uuid.UUID]bool{}
	for i := range products {
		p := &products[i]
		if p.Type != models.ProductTypeService {
			continue
		}
		ids = append(ids, p.ID)
		if p.CategoryID != nil && !seenCat[*p.CategoryID] {
			seenCat[*p.CategoryID] = true
			catIDs = append(catIDs, *p.CategoryID)
		}
	}
	if len(ids) == 0 {
		return
	}

	var profiles []models.ServiceProfile
	db.Where("product_id IN ?", ids).Find(&profiles)
	profileOf := make(map[uuid.UUID]models.ServiceProfile, len(profiles))
	for _, sp := range profiles {
		profileOf[sp.ProductID] = sp
	}

	type categoryView struct {
		serviceType, bookingMode string
		legacyFare               float64 // cheapest admin booking fare
	}
	catOf := map[uuid.UUID]categoryView{}
	if len(catIDs) > 0 {
		var cats []models.Category
		db.Where("id IN ?", catIDs).Find(&cats)
		for _, cat := range cats {
			v := categoryView{serviceType: models.EffectiveServiceType(cat), bookingMode: models.BookingModeSlot}
			if cfg, err := models.ResolveCategoryConfig(db, cat); err == nil && cfg.BookingMode.Value != nil {
				v.bookingMode = *cfg.BookingMode.Value
			}
			catOf[cat.ID] = v
		}
		var fares []struct {
			CategoryID uuid.UUID
			Min        float64
		}
		db.Model(&models.CategoryBookingFee{}).Select("category_id, MIN(amount_usd) AS min").
			Where("category_id IN ? AND amount_usd > 0", catIDs).Group("category_id").Scan(&fares)
		for _, f := range fares {
			v := catOf[f.CategoryID]
			v.legacyFare = f.Min
			catOf[f.CategoryID] = v
		}
	}

	var menus []struct {
		ProductID uuid.UUID
		Min       float64
	}
	db.Model(&models.ServiceMenuItem{}).Select("product_id, MIN(price_usd) AS min").
		Where("product_id IN ? AND is_available = ? AND price_usd > 0", ids, true).Group("product_id").Scan(&menus)
	menuFrom := make(map[uuid.UUID]float64, len(menus))
	for _, m := range menus {
		menuFrom[m.ProductID] = m.Min
	}

	for i := range products {
		p := &products[i]
		if p.Type != models.ProductTypeService {
			continue
		}
		cat := categoryView{serviceType: models.ServiceTypeContact}
		if p.CategoryID != nil {
			cat = catOf[*p.CategoryID]
		}
		prof := profileOf[p.ID]
		s := &models.ServicePriceSummary{ServiceType: cat.serviceType}
		switch cat.serviceType {
		case models.ServiceTypeBooking:
			s.BookingMode = cat.bookingMode
			s.BookingType = orDefault(prof.BookingType, models.BookingTypeInstant)
			s.PriceUnit = prof.PriceUnit
			if prof.PricingMethod == models.PricingMethodOnRequest {
				s.OnRequest = true
				break
			}
			fare := cheapestFare(p)
			if fare <= 0 {
				fare = cat.legacyFare
			}
			s.ChargesPerUnit = models.ChargesPerUnit(s.BookingMode, prof.PriceUnit)
			// A per-unit price is shown as "/ night" or "/ seat", so it is the fare
			// alone: the access fee is charged once per booking, not per unit.
			if fare > 0 {
				s.FromUSD = fare
				if !s.ChargesPerUnit {
					s.FromUSD += prof.AccessFeeUSD
				}
			}
		case models.ServiceTypeOrder:
			s.FromUSD = menuFrom[p.ID]
		default:
			s.FromUSD = p.Price
			if prof.AccessFeeUSD > 0 {
				s.FromUSD = prof.AccessFeeUSD
			}
		}
		p.ServicePrice = s
	}
}

// cheapestFare is the lowest non-zero per-area fee the listing charges, the
// price a buyer in its cheapest area pays (GetServiceTierFee reads the same
// columns at checkout).
func cheapestFare(p *models.Product) float64 {
	low := math.MaxFloat64
	for _, f := range []float64{p.ShippingResidential, p.ShippingSameCity, p.ShippingSameState,
		p.ShippingSameCountry, p.ShippingInternational} {
		if f > 0 && f < low {
			low = f
		}
	}
	if low == math.MaxFloat64 {
		return 0
	}
	return low
}
