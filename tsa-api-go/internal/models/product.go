package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ProductImage represents an image associated with a product.
type ProductImage struct {
	ID       uuid.UUID `json:"id,omitempty"`
	URL      string    `json:"url,omitempty"`
	PublicID string    `json:"publicId,omitempty"`
	Order    int       `json:"order,omitempty"`
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
	SKU        string            `json:"sku,omitempty"`
	Weight     float64           `json:"weight,omitempty"`
	Dimensions *ProductDimensions `json:"dimensions,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
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
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
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
