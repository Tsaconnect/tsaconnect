package models

import (
	"testing"

	"github.com/google/uuid"
)

func TestServiceCategoryIDs(t *testing.T) {
	contact := Category{ID: uuid.New(), Type: CategoryTypeService, ServiceType: ServiceTypeContact}
	order := Category{ID: uuid.New(), Type: CategoryTypeService, ServiceType: ServiceTypeOrder}
	booking := Category{ID: uuid.New(), Type: CategoryTypeService, ServiceType: ServiceTypeBooking}
	// Legacy: ServiceType unset, fare-schedule fee mode — resolves to booking.
	legacyFare := Category{ID: uuid.New(), Type: CategoryTypeService, ServiceFeeMode: ServiceFeeModeBooking}
	// Legacy: nothing set at all — resolves to contact.
	legacyBare := Category{ID: uuid.New(), Type: CategoryTypeService}
	// "Both" categories host services too, so they belong to a service bucket.
	both := Category{ID: uuid.New(), Type: CategoryTypeBoth, ServiceType: ServiceTypeOrder}
	// Product categories never do, whatever their (ignored) service fields say.
	product := Category{ID: uuid.New(), Type: CategoryTypeProduct, ServiceType: ServiceTypeBooking}

	all := []Category{contact, order, booking, legacyFare, legacyBare, both, product}

	has := func(ids []uuid.UUID, c Category) bool {
		for _, id := range ids {
			if id == c.ID {
				return true
			}
		}
		return false
	}

	t.Run("empty serviceType returns every service-hosting category", func(t *testing.T) {
		ids := ServiceCategoryIDs(all, "")
		if len(ids) != 6 {
			t.Fatalf("want 6 service categories, got %d", len(ids))
		}
		if has(ids, product) {
			t.Error("Product-typed category must never be a service category")
		}
		if !has(ids, both) {
			t.Error("Both-typed category hosts services and must be included")
		}
	})

	t.Run("booking includes the legacy fare-schedule category", func(t *testing.T) {
		ids := ServiceCategoryIDs(all, ServiceTypeBooking)
		if !has(ids, booking) || !has(ids, legacyFare) {
			t.Errorf("booking bucket missing entries: %v", ids)
		}
		if has(ids, contact) || has(ids, order) || has(ids, both) {
			t.Error("booking bucket leaked a non-booking category")
		}
	})

	t.Run("contact absorbs categories with nothing set", func(t *testing.T) {
		ids := ServiceCategoryIDs(all, ServiceTypeContact)
		if !has(ids, contact) || !has(ids, legacyBare) {
			t.Errorf("contact bucket missing entries: %v", ids)
		}
		if has(ids, legacyFare) {
			t.Error("legacy fare category belongs to booking, not contact")
		}
	})

	t.Run("order spans Service and Both categories", func(t *testing.T) {
		ids := ServiceCategoryIDs(all, ServiceTypeOrder)
		if !has(ids, order) || !has(ids, both) {
			t.Errorf("order bucket missing entries: %v", ids)
		}
	})

	t.Run("unknown serviceType matches nothing", func(t *testing.T) {
		if ids := ServiceCategoryIDs(all, "bogus"); len(ids) != 0 {
			t.Errorf("want no matches for an unknown service type, got %v", ids)
		}
	})

	t.Run("no categories yields no ids", func(t *testing.T) {
		if ids := ServiceCategoryIDs(nil, ServiceTypeBooking); len(ids) != 0 {
			t.Errorf("want none, got %v", ids)
		}
	})
}
