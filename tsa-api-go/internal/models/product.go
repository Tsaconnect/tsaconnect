package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ProductImage represents an image associated with a product.
type ProductImage struct {
	ID       uuid.UUID `json:"id"`
	URL      string    `json:"url"`
	PublicID string    `json:"publicId,omitempty"`
	Order    int       `json:"order"`
}

// ProductAttribute represents a key-value attribute on a product.
type ProductAttribute struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// ProductRating holds aggregated rating data.
type ProductRating struct {
	Average float64 `json:"average"`
	Count   int     `json:"count"`
}

// ProductDimensions holds physical dimensions of a product.
type ProductDimensions struct {
	Length float64 `json:"length,omitempty"`
	Width  float64 `json:"width,omitempty"`
	Height float64 `json:"height,omitempty"`
}

// ProductMetadata holds additional product metadata.
type ProductMetadata struct {
	SKU        string             `json:"sku,omitempty"`
	Weight     float64            `json:"weight,omitempty"`
	Dimensions *ProductDimensions `json:"dimensions,omitempty"`
	Tags       []string           `json:"tags,omitempty"`
}

// Product represents a product or service listing.
type Product struct {
	ID           uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID       uuid.UUID      `gorm:"type:uuid;index" json:"userId"`
	Name         string         `json:"name,omitempty"`
	Description  string         `json:"description,omitempty"`
	Location     string         `json:"location,omitempty"`
	PhoneNumber  string         `json:"phoneNumber,omitempty"`
	Email        string         `json:"email,omitempty"`
	CompanyName  string         `json:"companyName,omitempty"`
	Price        float64        `json:"price"`
	Stock        int            `json:"stock"`
	CategoryID   *uuid.UUID     `gorm:"type:uuid;column:category_id" json:"category,omitempty"`
	CategoryName string         `json:"categoryName,omitempty"`
	Images       datatypes.JSON `gorm:"type:jsonb" json:"images,omitempty"`
	Attributes   datatypes.JSON `gorm:"type:jsonb" json:"attributes,omitempty"`
	Status       string         `json:"status"`
	Type         string         `json:"type"`
	IsFeatured   bool           `json:"isFeatured"`
	Views        int            `json:"views"`
	Sales        int            `json:"sales"`
	Rating       datatypes.JSON `gorm:"type:jsonb" json:"rating"`
	Metadata     datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`
	// ShippingResidential is the "same residential area/street" tier of a
	// merchant-set distance fee. For products it is unused (0). For order/booking
	// services it holds the residential-tier delivery/booking fee — the one tier
	// PR #61 hard-coded to free. Defaults 0 → still free unless the merchant sets it.
	ShippingResidential   float64 `gorm:"default:0" json:"shippingResidential"`
	ShippingSameCity      float64 `gorm:"default:0" json:"shippingSameCity"`
	ShippingSameState     float64 `gorm:"default:0" json:"shippingSameState"`
	ShippingSameCountry   float64 `gorm:"default:0" json:"shippingSameCountry"`
	ShippingInternational float64 `gorm:"default:0" json:"shippingInternational"`
	SellerVerified        bool    `gorm:"-" json:"sellerVerified"`

	// Admin moderation. Suspension is admin-only: a merchant can neither set nor
	// clear it (see UpdateStatus), which is what distinguishes it from the
	// merchant-controlled `inactive`.
	SuspensionReason string     `json:"suspensionReason,omitempty"`
	SuspendedAt      *time.Time `json:"suspendedAt,omitempty"`
	SuspendedBy      *uuid.UUID `gorm:"type:uuid" json:"-"`
	// SuspendedFromStatus is the status to return to on unsuspend, so lifting a
	// suspension on a listing the merchant had paused does not silently publish it.
	SuspendedFromStatus string `json:"-"`

	// DeletedAt makes admin deletion reversible and keeps order history intact.
	// GORM excludes soft-deleted rows from every query automatically; admin
	// queries opt back in with .Unscoped().
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
	// IsDeleted is the serializable form of DeletedAt (which marshals as a gorm
	// struct, not a bool). Only admin listings can contain a deleted row, so
	// everywhere else this is simply false.
	IsDeleted bool `gorm:"-" json:"isDeleted"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (Product) TableName() string {
	return "products"
}

// Product status constants.
const (
	ProductStatusActive        = "active"
	ProductStatusInactive      = "inactive"
	ProductStatusSoldOut       = "sold_out"
	ProductStatusPendingReview = "pending_review"
	ProductStatusSuspended     = "suspended"
	DefaultProductStatus       = ProductStatusActive

	ProductTypeProduct = "Product"
	ProductTypeService = "Service"
	DefaultProductType = ProductTypeProduct
)

// GetImages deserializes the Images JSONB field.
func (p *Product) GetImages() []ProductImage {
	var images []ProductImage
	if p.Images != nil {
		_ = json.Unmarshal(p.Images, &images)
	}
	return images
}

// SetImages serializes the []ProductImage slice into the JSONB field.
func (p *Product) SetImages(images []ProductImage) {
	data, _ := json.Marshal(images)
	p.Images = data
}

// GetAttributes deserializes the Attributes JSONB field.
func (p *Product) GetAttributes() []ProductAttribute {
	var attrs []ProductAttribute
	if p.Attributes != nil {
		_ = json.Unmarshal(p.Attributes, &attrs)
	}
	return attrs
}

// SetAttributes serializes the []ProductAttribute slice into the JSONB field.
func (p *Product) SetAttributes(attrs []ProductAttribute) {
	data, _ := json.Marshal(attrs)
	p.Attributes = data
}

// MerchantSettableStatuses are the statuses a merchant may set on their own
// listing. `suspended` is deliberately absent — it is an admin action, and
// letting a merchant write it (or write over it) would make suspension
// meaningless.
var MerchantSettableStatuses = map[string]bool{
	ProductStatusActive:        true,
	ProductStatusInactive:      true,
	ProductStatusSoldOut:       true,
	ProductStatusPendingReview: true,
}

// PublicListableStatuses are the statuses an unauthenticated caller may ask for
// via a `status` query param. Only `active` is public: the others expose
// listings their owners have not published, and `suspended` would leak
// moderation decisions.
var PublicListableStatuses = map[string]bool{
	ProductStatusActive: true,
}

// VisibleToPublic narrows a product query to listings buyers are allowed to see.
// Every public-facing product query goes through this so the rule cannot drift
// between the marketplace, category and featured queries.
//
// Two conditions, not one:
//   - the listing itself is active, and
//   - its seller's account is active.
//
// The second is why suspending an account hides that seller's listings without
// writing anything to their products: a suspended seller cannot log in to ship
// or confirm delivery, so leaving their listings buyable would take money into
// escrow that nobody can fulfil. Reactivating restores them with no unwind step.
//
// Expressed as a subquery rather than a join so callers can keep composing
// Where clauses without worrying about ambiguous column names.
//
// Soft deletion is NOT handled here — gorm.DeletedAt already excludes deleted
// rows from every query, including ones this scope never touches.
func VisibleToPublic(q *gorm.DB) *gorm.DB {
	return q.Where("status = ?", ProductStatusActive).
		Where("user_id IN (SELECT id FROM users WHERE account_status = ?)", AccountStatusActive)
}

// IsVisibleToPublic reports whether a single loaded product may be shown to a
// buyer, given its seller. The query-level counterpart is VisibleToPublic, and
// the two must agree — both check the listing AND the seller's account.
//
// seller may be nil when the caller has not loaded it, which is treated as not
// visible: a listing whose seller cannot be found is not one to show a buyer.
func (p *Product) IsVisibleToPublic(seller *User) bool {
	return p.Status == ProductStatusActive && seller != nil && seller.IsActive()
}

// MarkDeletionFlag populates the serializable IsDeleted field from DeletedAt.
// Only admin queries run .Unscoped(), so only they need to call this.
func (p *Product) MarkDeletionFlag() {
	p.IsDeleted = p.DeletedAt.Valid
}

// MarkDeletionFlags applies MarkDeletionFlag across a page of listings.
func MarkDeletionFlags(products []Product) {
	for i := range products {
		products[i].MarkDeletionFlag()
	}
}
