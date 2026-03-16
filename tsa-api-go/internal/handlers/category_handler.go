package handlers

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// GetCategories handles GET /api/products/category/all - returns categories with optional filters.
func (h *Handlers) GetCategories(c *gin.Context) {
	typeFilter := c.Query("type")
	parentFilter := c.Query("parent")
	activeFilter := c.DefaultQuery("active", "true")

	query := config.DB.Model(&models.Category{})

	if typeFilter != "" {
		query = query.Where("type IN ?", []string{typeFilter, "Both"})
	}

	if parentFilter != "" {
		if parentFilter == "null" || parentFilter == "" {
			query = query.Where("parent_category_id IS NULL")
		} else {
			if parentOID, err := uuid.Parse(parentFilter); err == nil {
				query = query.Where("parent_category_id = ?", parentOID)
			}
		}
	}

	if activeFilter != "" {
		query = query.Where("is_active = ?", activeFilter == "true")
	}

	var categories []models.Category
	if err := query.Order("sort_order ASC, title ASC").Find(&categories).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch categories")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Categories fetched successfully", categories)
}

// GetCategoryTree handles GET /api/products/category/tree - returns nested category tree.
func (h *Handlers) GetCategoryTree(c *gin.Context) {
	typeFilter := c.Query("type")

	query := config.DB.Model(&models.Category{}).Where("is_active = ?", true)
	if typeFilter != "" {
		query = query.Where("type IN ?", []string{typeFilter, "Both"})
	}

	var categories []models.Category
	if err := query.Order("sort_order ASC, title ASC").Find(&categories).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch category tree")
		return
	}

	// Build tree structure
	tree := buildCategoryTree(categories, nil)

	utils.SuccessResponse(c, http.StatusOK, "Category tree fetched successfully", tree)
}

// buildCategoryTree recursively builds a nested tree from a flat list of categories.
func buildCategoryTree(categories []models.Category, parentID *uuid.UUID) []models.CategoryWithChildren {
	var result []models.CategoryWithChildren

	for _, cat := range categories {
		isMatch := false
		if parentID == nil {
			isMatch = cat.ParentCategoryID == nil
		} else {
			isMatch = cat.ParentCategoryID != nil && *cat.ParentCategoryID == *parentID
		}

		if isMatch {
			catID := cat.ID
			node := models.CategoryWithChildren{
				Category: cat,
				Children: buildCategoryTree(categories, &catID),
			}
			result = append(result, node)
		}
	}

	return result
}

// GetCategoryByID handles GET /api/products/category/:categoryId - returns a category with children.
func (h *Handlers) GetCategoryByID(c *gin.Context) {
	categoryID, err := uuid.Parse(c.Param("categoryId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}

	var category models.Category
	if err := config.DB.First(&category, "id = ?", categoryID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Category not found")
		return
	}

	// Get child categories
	var children []models.Category
	config.DB.Where("parent_category_id = ? AND is_active = ?", categoryID, true).Find(&children)

	// Get product count
	var productCount int64
	config.DB.Model(&models.Product{}).Where("category_id = ? AND status = ?", categoryID, "active").Count(&productCount)

	utils.SuccessResponse(c, http.StatusOK, "Category fetched successfully", gin.H{
		"id":             category.ID,
		"title":          category.Title,
		"description":    category.Description,
		"type":           category.Type,
		"parentCategory": category.ParentCategoryID,
		"icon":           category.Icon,
		"color":          category.Color,
		"isActive":       category.IsActive,
		"order":          category.Order,
		"productCount":   productCount,
		"children":       children,
		"createdAt":      category.CreatedAt,
		"updatedAt":      category.UpdatedAt,
	})
}

// CreateCategory handles POST /api/products/category - creates a new category (admin only).
func (h *Handlers) CreateCategory(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if user.Role != models.RoleAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Admin only.")
		return
	}

	title := c.PostForm("title")
	catType := c.PostForm("type")
	if title == "" || catType == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Title and type are required")
		return
	}

	description := c.PostForm("description")
	parentCategoryStr := c.PostForm("parentCategory")
	color := c.DefaultPostForm("color", "#666666")
	orderStr := c.DefaultPostForm("order", "0")
	order, _ := strconv.Atoi(orderStr)

	// Check if category already exists (case-insensitive)
	var existing models.Category
	if err := config.DB.Where("LOWER(title) = LOWER(?)", title).First(&existing).Error; err == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Category already exists")
		return
	}

	// Validate parent category if provided
	var parentCategoryID *uuid.UUID
	if parentCategoryStr != "" {
		parentOID, err := uuid.Parse(parentCategoryStr)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid parent category ID")
			return
		}
		var parent models.Category
		if err := config.DB.First(&parent, "id = ?", parentOID).Error; err != nil {
			utils.ErrorResponse(c, http.StatusNotFound, "Parent category not found")
			return
		}
		parentCategoryID = &parentOID
	}

	// Handle icon upload
	var iconURL string
	file, err := c.FormFile("icon")
	if err == nil && file != nil {
		f, err := file.Open()
		if err == nil {
			fileData, err := io.ReadAll(f)
			f.Close()
			if err == nil {
				result, err := middleware.UploadToCloudinary(h.Config, fileData, "categories")
				if err == nil {
					iconURL = result.URL
				} else {
					log.Printf("Cloudinary upload error: %v", err)
				}
			}
		}
	}

	now := time.Now()
	category := models.Category{
		ID:               uuid.New(),
		Title:            title,
		Description:      description,
		Type:             catType,
		ParentCategoryID: parentCategoryID,
		Icon:             iconURL,
		Color:            color,
		IsActive:         true,
		Order:            order,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := config.DB.Create(&category).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create category")
		return
	}

	utils.SuccessResponse(c, http.StatusCreated, "Category created successfully", category)
}

// UpdateCategory handles PUT /api/products/category/:categoryId - updates a category (admin only).
func (h *Handlers) UpdateCategory(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if user.Role != models.RoleAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Admin only.")
		return
	}

	categoryID, err := uuid.Parse(c.Param("categoryId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}

	var category models.Category
	if err := config.DB.First(&category, "id = ?", categoryID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Category not found")
		return
	}

	updates := map[string]interface{}{}

	if title := c.PostForm("title"); title != "" {
		if title != category.Title {
			var existing models.Category
			if err := config.DB.Where("LOWER(title) = LOWER(?) AND id != ?", title, categoryID).First(&existing).Error; err == nil {
				utils.ErrorResponse(c, http.StatusBadRequest, "Category title already exists")
				return
			}
		}
		updates["title"] = title
	}
	if desc := c.PostForm("description"); desc != "" {
		updates["description"] = desc
	}
	if catType := c.PostForm("type"); catType != "" {
		updates["type"] = catType
	}
	if color := c.PostForm("color"); color != "" {
		updates["color"] = color
	}
	if orderStr := c.PostForm("order"); orderStr != "" {
		if o, err := strconv.Atoi(orderStr); err == nil {
			updates["sort_order"] = o
		}
	}
	if isActiveStr := c.PostForm("isActive"); isActiveStr != "" {
		updates["is_active"] = isActiveStr == "true"
	}

	// Handle parent category
	parentCategoryStr := c.PostForm("parentCategory")
	if parentCategoryStr != "" {
		if parentCategoryStr == categoryID.String() {
			utils.ErrorResponse(c, http.StatusBadRequest, "Category cannot be its own parent")
			return
		}
		parentOID, err := uuid.Parse(parentCategoryStr)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid parent category ID")
			return
		}
		var parent models.Category
		if err := config.DB.First(&parent, "id = ?", parentOID).Error; err != nil {
			utils.ErrorResponse(c, http.StatusNotFound, "Parent category not found")
			return
		}
		updates["parent_category_id"] = parentOID
	}

	// Handle icon upload
	file, err := c.FormFile("icon")
	if err == nil && file != nil {
		f, err := file.Open()
		if err == nil {
			fileData, err := io.ReadAll(f)
			f.Close()
			if err == nil {
				result, err := middleware.UploadToCloudinary(h.Config, fileData, "categories")
				if err == nil {
					updates["icon"] = result.URL
				} else {
					log.Printf("Cloudinary upload error: %v", err)
				}
			}
		}
	}

	updates["updated_at"] = time.Now()

	if err := config.DB.Model(&category).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update category")
		return
	}

	var updated models.Category
	config.DB.First(&updated, "id = ?", categoryID)

	utils.SuccessResponse(c, http.StatusOK, "Category updated successfully", updated)
}

// DeleteCategory handles DELETE /api/products/category/:categoryId - deletes a category (admin only).
func (h *Handlers) DeleteCategory(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if user.Role != models.RoleAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Admin only.")
		return
	}

	categoryID, err := uuid.Parse(c.Param("categoryId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}

	var category models.Category
	if err := config.DB.First(&category, "id = ?", categoryID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Category not found")
		return
	}

	// Check if category has products
	var productCount int64
	config.DB.Model(&models.Product{}).Where("category_id = ?", categoryID).Count(&productCount)
	if productCount > 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot delete category with existing products")
		return
	}

	// Check if category has subcategories
	var childCount int64
	config.DB.Model(&models.Category{}).Where("parent_category_id = ?", categoryID).Count(&childCount)
	if childCount > 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot delete category with subcategories")
		return
	}

	if err := config.DB.Delete(&category).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete category")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Category deleted successfully", nil)
}

// ReorderCategories handles PATCH /api/products/category/reorder - bulk reorder categories (admin only).
func (h *Handlers) ReorderCategories(c *gin.Context) {
	var body struct {
		Orders []struct {
			ID    uuid.UUID `json:"id" binding:"required"`
			Order int       `json:"order"`
		} `json:"orders" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	tx := config.DB.Begin()
	for _, item := range body.Orders {
		if err := tx.Model(&models.Category{}).Where("id = ?", item.ID).Update("sort_order", item.Order).Error; err != nil {
			tx.Rollback()
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reorder categories")
			return
		}
	}
	tx.Commit()

	utils.SuccessResponse(c, http.StatusOK, "Categories reordered successfully", nil)
}

// Ensure gorm and errors imports are used.
var (
	_ = (*gorm.DB)(nil)
	_ = errors.New
)
