package models

import (
	"time"

	"github.com/google/uuid"
)

// Category represents a product/service category.
type Category struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Title            string     `gorm:"uniqueIndex;not null" json:"title"`
	Description      string     `json:"description,omitempty"`
	Type             string     `json:"type,omitempty"`
	ParentCategoryID *uuid.UUID `gorm:"type:uuid;column:parent_category_id" json:"parentCategory,omitempty"`
	Icon             string     `json:"icon,omitempty"`
	Color            string     `json:"color"`
	IsActive         bool       `json:"isActive"`
	Order            int        `gorm:"column:sort_order" json:"order"`
	ProductCount     int        `json:"productCount"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// TableName overrides the default table name.
func (Category) TableName() string {
	return "categories"
}

// CategoryWithChildren extends Category with a recursive children list for tree views.
type CategoryWithChildren struct {
	Category
	Children []CategoryWithChildren `json:"children"`
}

// Category type constants.
const (
	CategoryTypeProduct = "Product"
	CategoryTypeService = "Service"
	CategoryTypeBoth    = "Both"

	DefaultCategoryColor = "#666666"
)
