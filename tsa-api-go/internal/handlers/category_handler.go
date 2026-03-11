package handlers

import (
	"context"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GetCategories handles GET /api/products/category/all - returns categories with optional filters.
func (h *Handlers) GetCategories(c *gin.Context) {
	typeFilter := c.Query("type")
	parentFilter := c.Query("parent")
	activeFilter := c.DefaultQuery("active", "true")

	filter := bson.M{}

	if typeFilter != "" {
		filter["type"] = bson.M{"$in": bson.A{typeFilter, "Both"}}
	}

	if parentFilter != "" {
		if parentFilter == "null" || parentFilter == "" {
			filter["parentCategory"] = nil
		} else {
			if parentOID, err := primitive.ObjectIDFromHex(parentFilter); err == nil {
				filter["parentCategory"] = parentOID
			}
		}
	}

	if activeFilter != "" {
		filter["isActive"] = activeFilter == "true"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	col := config.GetCollection("categories")

	opts := options.Find().SetSort(bson.D{
		{Key: "order", Value: 1},
		{Key: "title", Value: 1},
	})

	cursor, err := col.Find(ctx, filter, opts)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch categories")
		return
	}
	defer cursor.Close(ctx)

	var categories []models.Category
	if err := cursor.All(ctx, &categories); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode categories")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Categories fetched successfully", categories)
}

// GetCategoryTree handles GET /api/products/category/tree - returns nested category tree.
func (h *Handlers) GetCategoryTree(c *gin.Context) {
	typeFilter := c.Query("type")

	filter := bson.M{"isActive": true}
	if typeFilter != "" {
		filter["type"] = bson.M{"$in": bson.A{typeFilter, "Both"}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	col := config.GetCollection("categories")

	opts := options.Find().SetSort(bson.D{
		{Key: "order", Value: 1},
		{Key: "title", Value: 1},
	})

	cursor, err := col.Find(ctx, filter, opts)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch category tree")
		return
	}
	defer cursor.Close(ctx)

	var categories []models.Category
	if err := cursor.All(ctx, &categories); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode categories")
		return
	}

	// Build tree structure
	tree := buildCategoryTree(categories, nil)

	utils.SuccessResponse(c, http.StatusOK, "Category tree fetched successfully", tree)
}

// buildCategoryTree recursively builds a nested tree from a flat list of categories.
func buildCategoryTree(categories []models.Category, parentID *primitive.ObjectID) []models.CategoryWithChildren {
	var result []models.CategoryWithChildren

	for _, cat := range categories {
		isMatch := false
		if parentID == nil {
			isMatch = cat.ParentCategory == nil
		} else {
			isMatch = cat.ParentCategory != nil && *cat.ParentCategory == *parentID
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
	categoryID, err := primitive.ObjectIDFromHex(c.Param("categoryId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	catCol := config.GetCollection("categories")

	var category models.Category
	err = catCol.FindOne(ctx, bson.M{"_id": categoryID}).Decode(&category)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Category not found")
		return
	}

	// Get child categories
	childCursor, err := catCol.Find(ctx, bson.M{
		"parentCategory": categoryID,
		"isActive":       true,
	})
	var children []models.Category
	if err == nil {
		_ = childCursor.All(ctx, &children)
		childCursor.Close(ctx)
	}

	// Get product count
	prodCol := config.GetCollection("products")
	productCount, _ := prodCol.CountDocuments(ctx, bson.M{
		"category": categoryID,
		"status":   "active",
	})

	utils.SuccessResponse(c, http.StatusOK, "Category fetched successfully", gin.H{
		"id":             category.ID,
		"title":          category.Title,
		"description":    category.Description,
		"type":           category.Type,
		"parentCategory": category.ParentCategory,
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	catCol := config.GetCollection("categories")

	// Check if category already exists (case-insensitive)
	var existing models.Category
	err := catCol.FindOne(ctx, bson.M{
		"title": bson.M{"$regex": "^" + title + "$", "$options": "i"},
	}).Decode(&existing)
	if err == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Category already exists")
		return
	}

	// Validate parent category if provided
	var parentCategory *primitive.ObjectID
	if parentCategoryStr != "" {
		parentOID, err := primitive.ObjectIDFromHex(parentCategoryStr)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid parent category ID")
			return
		}
		var parent models.Category
		err = catCol.FindOne(ctx, bson.M{"_id": parentOID}).Decode(&parent)
		if err != nil {
			utils.ErrorResponse(c, http.StatusNotFound, "Parent category not found")
			return
		}
		parentCategory = &parentOID
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
		Title:          title,
		Description:    description,
		Type:           catType,
		ParentCategory: parentCategory,
		Icon:           iconURL,
		Color:          color,
		IsActive:       true,
		Order:          order,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	result, err := catCol.InsertOne(ctx, category)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create category")
		return
	}

	category.ID = result.InsertedID.(primitive.ObjectID)

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

	categoryID, err := primitive.ObjectIDFromHex(c.Param("categoryId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	catCol := config.GetCollection("categories")

	var category models.Category
	err = catCol.FindOne(ctx, bson.M{"_id": categoryID}).Decode(&category)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Category not found")
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Remove _id from updates
	delete(updates, "_id")

	// Check title uniqueness if being changed
	if newTitle, ok := updates["title"].(string); ok && newTitle != category.Title {
		var existing models.Category
		err := catCol.FindOne(ctx, bson.M{
			"title": bson.M{"$regex": "^" + newTitle + "$", "$options": "i"},
			"_id":   bson.M{"$ne": categoryID},
		}).Decode(&existing)
		if err == nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Category title already exists")
			return
		}
	}

	// Validate parent category if being changed
	if parentStr, ok := updates["parentCategory"]; ok {
		if parentStr != nil {
			parentHex, ok := parentStr.(string)
			if ok {
				if parentHex == categoryID.Hex() {
					utils.ErrorResponse(c, http.StatusBadRequest, "Category cannot be its own parent")
					return
				}
				parentOID, err := primitive.ObjectIDFromHex(parentHex)
				if err != nil {
					utils.ErrorResponse(c, http.StatusBadRequest, "Invalid parent category ID")
					return
				}
				var parent models.Category
				err = catCol.FindOne(ctx, bson.M{"_id": parentOID}).Decode(&parent)
				if err != nil {
					utils.ErrorResponse(c, http.StatusNotFound, "Parent category not found")
					return
				}
				updates["parentCategory"] = parentOID
			}
		}
	}

	updates["updatedAt"] = time.Now()

	_, err = catCol.UpdateOne(ctx, bson.M{"_id": categoryID}, bson.M{"$set": updates})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update category")
		return
	}

	var updated models.Category
	_ = catCol.FindOne(ctx, bson.M{"_id": categoryID}).Decode(&updated)

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

	categoryID, err := primitive.ObjectIDFromHex(c.Param("categoryId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	catCol := config.GetCollection("categories")

	var category models.Category
	err = catCol.FindOne(ctx, bson.M{"_id": categoryID}).Decode(&category)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Category not found")
		return
	}

	// Check if category has products
	prodCol := config.GetCollection("products")
	productCount, _ := prodCol.CountDocuments(ctx, bson.M{"category": categoryID})
	if productCount > 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot delete category with existing products")
		return
	}

	// Check if category has subcategories
	childCount, _ := catCol.CountDocuments(ctx, bson.M{"parentCategory": categoryID})
	if childCount > 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot delete category with subcategories")
		return
	}

	_, err = catCol.DeleteOne(ctx, bson.M{"_id": categoryID})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete category")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Category deleted successfully", nil)
}
