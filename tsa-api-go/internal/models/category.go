package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Category represents a product/service category.
type Category struct {
	ID             primitive.ObjectID  `bson:"_id,omitempty" json:"id,omitempty"`
	Title          string              `bson:"title" json:"title"`
	Description    string              `bson:"description,omitempty" json:"description,omitempty"`
	Type           string              `bson:"type,omitempty" json:"type,omitempty"`
	ParentCategory *primitive.ObjectID `bson:"parentCategory,omitempty" json:"parentCategory,omitempty"`
	Icon           string              `bson:"icon,omitempty" json:"icon,omitempty"`
	Color          string              `bson:"color" json:"color"`
	IsActive       bool                `bson:"isActive" json:"isActive"`
	Order          int                 `bson:"order" json:"order"`
	ProductCount   int                 `bson:"productCount" json:"productCount"`
	CreatedAt      time.Time           `bson:"createdAt" json:"createdAt"`
	UpdatedAt      time.Time           `bson:"updatedAt" json:"updatedAt"`
}

// CategoryWithChildren extends Category with a recursive children list for tree views.
type CategoryWithChildren struct {
	Category `bson:",inline"`
	Children []CategoryWithChildren `json:"children"`
}

// Category type constants.
const (
	CategoryTypeProduct = "Product"
	CategoryTypeService = "Service"
	CategoryTypeBoth    = "Both"

	DefaultCategoryColor = "#666666"
)
