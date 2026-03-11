package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ProductImage represents an image associated with a product.
type ProductImage struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	URL      string             `bson:"url,omitempty" json:"url,omitempty"`
	PublicID string             `bson:"publicId,omitempty" json:"publicId,omitempty"`
	Order    int                `bson:"order,omitempty" json:"order,omitempty"`
}

// ProductAttribute represents a key-value attribute on a product.
type ProductAttribute struct {
	Name  string `bson:"name,omitempty" json:"name,omitempty"`
	Value string `bson:"value,omitempty" json:"value,omitempty"`
}

// ProductRating holds aggregated rating data.
type ProductRating struct {
	Average float64 `bson:"average" json:"average"`
	Count   int     `bson:"count" json:"count"`
}

// ProductDimensions holds physical dimensions of a product.
type ProductDimensions struct {
	Length float64 `bson:"length,omitempty" json:"length,omitempty"`
	Width  float64 `bson:"width,omitempty" json:"width,omitempty"`
	Height float64 `bson:"height,omitempty" json:"height,omitempty"`
}

// ProductMetadata holds additional product metadata.
type ProductMetadata struct {
	SKU        string            `bson:"sku,omitempty" json:"sku,omitempty"`
	Weight     float64           `bson:"weight,omitempty" json:"weight,omitempty"`
	Dimensions *ProductDimensions `bson:"dimensions,omitempty" json:"dimensions,omitempty"`
	Tags       []string          `bson:"tags,omitempty" json:"tags,omitempty"`
}

// Product represents a product or service listing.
type Product struct {
	ID           primitive.ObjectID  `bson:"_id,omitempty" json:"id,omitempty"`
	UserID       primitive.ObjectID  `bson:"userId" json:"userId"`
	Name         string              `bson:"name,omitempty" json:"name,omitempty"`
	Description  string              `bson:"description,omitempty" json:"description,omitempty"`
	Price        float64             `bson:"price" json:"price"`
	Stock        int                 `bson:"stock" json:"stock"`
	Category     *primitive.ObjectID `bson:"category,omitempty" json:"category,omitempty"`
	CategoryName string              `bson:"categoryName,omitempty" json:"categoryName,omitempty"`
	Location     string              `bson:"location,omitempty" json:"location,omitempty"`
	PhoneNumber  string              `bson:"phoneNumber,omitempty" json:"phoneNumber,omitempty"`
	Email        string              `bson:"email,omitempty" json:"email,omitempty"`
	CompanyName  string              `bson:"companyName,omitempty" json:"companyName,omitempty"`
	Images       []ProductImage      `bson:"images,omitempty" json:"images,omitempty"`
	Attributes   []ProductAttribute  `bson:"attributes,omitempty" json:"attributes,omitempty"`
	Status       string              `bson:"status" json:"status"`
	Type         string              `bson:"type" json:"type"`
	IsFeatured   bool                `bson:"isFeatured" json:"isFeatured"`
	Views        int                 `bson:"views" json:"views"`
	Sales        int                 `bson:"sales" json:"sales"`
	Rating       ProductRating       `bson:"rating" json:"rating"`
	Metadata     *ProductMetadata    `bson:"metadata,omitempty" json:"metadata,omitempty"`
	CreatedAt    time.Time           `bson:"createdAt" json:"createdAt"`
	UpdatedAt    time.Time           `bson:"updatedAt" json:"updatedAt"`
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
