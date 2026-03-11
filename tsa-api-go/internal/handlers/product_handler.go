package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
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

// CreateProduct handles POST /api/products/ - creates a new product with optional image uploads.
func (h *Handlers) CreateProduct(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	name := c.PostForm("name")
	description := c.PostForm("description")
	priceStr := c.PostForm("price")
	location := c.PostForm("location")
	phoneNumber := c.PostForm("phoneNumber")
	email := c.PostForm("email")

	if name == "" || description == "" || priceStr == "" || location == "" || phoneNumber == "" || email == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Missing required fields")
		return
	}

	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil || price < 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid price")
		return
	}

	stockStr := c.PostForm("stock")
	stock := 0
	if stockStr != "" {
		stock, _ = strconv.Atoi(stockStr)
	}

	companyName := c.PostForm("companyName")
	productType := c.PostForm("type")
	if productType == "" {
		productType = models.DefaultProductType
	}

	// Parse attributes from JSON string
	var parsedAttributes []models.ProductAttribute
	attributesStr := c.PostForm("attributes")
	if attributesStr != "" {
		if err := json.Unmarshal([]byte(attributesStr), &parsedAttributes); err != nil {
			log.Printf("Error parsing attributes: %v", err)
		}
	}

	// Validate category if provided
	categoryStr := c.PostForm("category")
	var categoryID *primitive.ObjectID
	var categoryName string
	if categoryStr != "" {
		catOID, err := primitive.ObjectIDFromHex(categoryStr)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var cat models.Category
		err = config.GetCollection("categories").FindOne(ctx, bson.M{"_id": catOID}).Decode(&cat)
		if err != nil {
			utils.ErrorResponse(c, http.StatusNotFound, "Category not found")
			return
		}
		categoryID = &catOID
		categoryName = cat.Title
	}

	// Handle image uploads
	var uploadedImages []models.ProductImage
	form, _ := c.MultipartForm()
	if form != nil && form.File["images"] != nil {
		for i, fileHeader := range form.File["images"] {
			file, err := fileHeader.Open()
			if err != nil {
				log.Printf("Error opening file: %v", err)
				continue
			}
			fileData, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				log.Printf("Error reading file: %v", err)
				continue
			}

			result, err := middleware.UploadToCloudinary(h.Config, fileData, "products")
			if err != nil {
				log.Printf("Cloudinary upload error: %v", err)
				continue
			}

			uploadedImages = append(uploadedImages, models.ProductImage{
				URL:      result.URL,
				PublicID: result.PublicID,
				Order:    i,
			})
		}
	}

	now := time.Now()
	product := models.Product{
		UserID:       user.ID,
		Name:         name,
		Description:  description,
		Price:        price,
		Stock:        stock,
		Category:     categoryID,
		CategoryName: categoryName,
		Location:     location,
		PhoneNumber:  phoneNumber,
		Email:        strings.ToLower(strings.TrimSpace(email)),
		CompanyName:  companyName,
		Images:       uploadedImages,
		Attributes:   parsedAttributes,
		Status:       models.ProductStatusActive,
		Type:         productType,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := config.GetCollection("products").InsertOne(ctx, product)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create product")
		return
	}

	product.ID = result.InsertedID.(primitive.ObjectID)

	// Update category product count
	if categoryID != nil {
		updateCategoryProductCount(ctx, *categoryID)
	}

	utils.SuccessResponse(c, http.StatusCreated, "Product created successfully", product)
}

// GetUserProducts handles GET /api/products/ - returns paginated products for the authenticated user.
func (h *Handlers) GetUserProducts(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	status := c.Query("status")
	category := c.Query("category")
	search := c.Query("search")
	sortBy := c.DefaultQuery("sortBy", "createdAt")
	sortOrder := c.DefaultQuery("sortOrder", "desc")

	filter := bson.M{"userId": user.ID}
	if status != "" {
		filter["status"] = status
	}
	if category != "" {
		catOID, err := primitive.ObjectIDFromHex(category)
		if err == nil {
			filter["category"] = catOID
		}
	}
	if search != "" {
		filter["$or"] = bson.A{
			bson.M{"name": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"description": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"companyName": bson.M{"$regex": search, "$options": "i"}},
		}
	}

	sortDir := -1
	if sortOrder == "asc" {
		sortDir = 1
	}
	skip := int64((page - 1) * limit)
	limitInt := int64(limit)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	col := config.GetCollection("products")

	total, err := col.CountDocuments(ctx, filter)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products")
		return
	}

	opts := options.Find().
		SetSort(bson.D{{Key: sortBy, Value: sortDir}}).
		SetSkip(skip).
		SetLimit(limitInt)

	cursor, err := col.Find(ctx, filter, opts)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products")
		return
	}
	defer cursor.Close(ctx)

	var products []models.Product
	if err := cursor.All(ctx, &products); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode products")
		return
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	utils.SuccessResponse(c, http.StatusOK, "Products fetched successfully", gin.H{
		"products": products,
		"pagination": gin.H{
			"page":        page,
			"limit":       limit,
			"total":       total,
			"totalPages":  totalPages,
			"hasNextPage": page < totalPages,
			"hasPrevPage": page > 1,
		},
	})
}

// GetProductByID handles GET /api/products/:productId - returns a product and increments views.
func (h *Handlers) GetProductByID(c *gin.Context) {
	productID, err := primitive.ObjectIDFromHex(c.Param("productId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	col := config.GetCollection("products")

	var product models.Product
	err = col.FindOne(ctx, bson.M{"_id": productID}).Decode(&product)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	// Increment views
	_, _ = col.UpdateOne(ctx, bson.M{"_id": productID}, bson.M{"$inc": bson.M{"views": 1}})
	product.Views++

	utils.SuccessResponse(c, http.StatusOK, "Product fetched successfully", product)
}

// UpdateProduct handles PUT /api/products/:productId - updates an existing product (owner only).
func (h *Handlers) UpdateProduct(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	productID, err := primitive.ObjectIDFromHex(c.Param("productId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	col := config.GetCollection("products")

	var product models.Product
	err = col.FindOne(ctx, bson.M{"_id": productID, "userId": user.ID}).Decode(&product)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	update := bson.M{}

	if name := c.PostForm("name"); name != "" {
		update["name"] = name
	}
	if desc := c.PostForm("description"); desc != "" {
		update["description"] = desc
	}
	if priceStr := c.PostForm("price"); priceStr != "" {
		if p, err := strconv.ParseFloat(priceStr, 64); err == nil {
			update["price"] = p
		}
	}
	if stockStr := c.PostForm("stock"); stockStr != "" {
		if s, err := strconv.Atoi(stockStr); err == nil {
			update["stock"] = s
		}
	}
	if location := c.PostForm("location"); location != "" {
		update["location"] = location
	}
	if phone := c.PostForm("phoneNumber"); phone != "" {
		update["phoneNumber"] = phone
	}
	if email := c.PostForm("email"); email != "" {
		update["email"] = strings.ToLower(strings.TrimSpace(email))
	}
	if company := c.PostForm("companyName"); company != "" {
		update["companyName"] = company
	}
	if t := c.PostForm("type"); t != "" {
		update["type"] = t
	}

	// Parse attributes
	if attrStr := c.PostForm("attributes"); attrStr != "" {
		var attrs []models.ProductAttribute
		if err := json.Unmarshal([]byte(attrStr), &attrs); err == nil {
			update["attributes"] = attrs
		}
	}

	// Handle category update
	if catStr := c.PostForm("category"); catStr != "" {
		catOID, err := primitive.ObjectIDFromHex(catStr)
		if err == nil {
			update["category"] = catOID
		}
	}

	// Handle new image uploads
	form, _ := c.MultipartForm()
	if form != nil && form.File["images"] != nil && len(form.File["images"]) > 0 {
		// Delete old images from Cloudinary
		for _, img := range product.Images {
			if img.PublicID != "" {
				_ = middleware.DeleteFromCloudinary(h.Config, img.PublicID)
			}
		}

		var newImages []models.ProductImage
		for i, fileHeader := range form.File["images"] {
			file, err := fileHeader.Open()
			if err != nil {
				continue
			}
			fileData, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				continue
			}

			result, err := middleware.UploadToCloudinary(h.Config, fileData, "products")
			if err != nil {
				log.Printf("Cloudinary upload error: %v", err)
				continue
			}

			newImages = append(newImages, models.ProductImage{
				URL:      result.URL,
				PublicID: result.PublicID,
				Order:    i,
			})
		}
		update["images"] = newImages
	}

	if len(update) == 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "No fields to update")
		return
	}
	update["updatedAt"] = time.Now()

	_, err = col.UpdateOne(ctx, bson.M{"_id": productID}, bson.M{"$set": update})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update product")
		return
	}

	// Re-fetch updated product
	var updated models.Product
	_ = col.FindOne(ctx, bson.M{"_id": productID}).Decode(&updated)

	// Update category product count if category changed
	if catID, ok := update["category"]; ok {
		if oid, ok := catID.(primitive.ObjectID); ok {
			updateCategoryProductCount(ctx, oid)
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Product updated successfully", updated)
}

// DeleteProduct handles DELETE /api/products/:productId - deletes a product (owner only).
func (h *Handlers) DeleteProduct(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	productID, err := primitive.ObjectIDFromHex(c.Param("productId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	col := config.GetCollection("products")

	var product models.Product
	err = col.FindOne(ctx, bson.M{"_id": productID, "userId": user.ID}).Decode(&product)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	// Delete images from Cloudinary
	for _, img := range product.Images {
		if img.PublicID != "" {
			_ = middleware.DeleteFromCloudinary(h.Config, img.PublicID)
		}
	}

	categoryID := product.Category

	_, err = col.DeleteOne(ctx, bson.M{"_id": productID})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete product")
		return
	}

	if categoryID != nil {
		updateCategoryProductCount(ctx, *categoryID)
	}

	utils.SuccessResponse(c, http.StatusOK, "Product deleted successfully", nil)
}

// UpdateStock handles PATCH /api/products/:productId/stock - updates product stock.
func (h *Handlers) UpdateStock(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	productID, err := primitive.ObjectIDFromHex(c.Param("productId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	var body struct {
		Quantity int `json:"quantity"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Valid quantity is required")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	col := config.GetCollection("products")
	result, err := col.UpdateOne(ctx,
		bson.M{"_id": productID, "userId": user.ID},
		bson.M{"$set": bson.M{"stock": body.Quantity, "updatedAt": time.Now()}},
	)
	if err != nil || result.MatchedCount == 0 {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Stock updated successfully", gin.H{
		"stock": body.Quantity,
	})
}

// UpdateStatus handles PATCH /api/products/:productId/status - updates product status.
func (h *Handlers) UpdateStatus(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	productID, err := primitive.ObjectIDFromHex(c.Param("productId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Status is required")
		return
	}

	validStatuses := map[string]bool{
		"active": true, "inactive": true, "sold_out": true, "pending_review": true,
	}
	if !validStatuses[body.Status] {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid status")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	col := config.GetCollection("products")
	result, err := col.UpdateOne(ctx,
		bson.M{"_id": productID, "userId": user.ID},
		bson.M{"$set": bson.M{"status": body.Status, "updatedAt": time.Now()}},
	)
	if err != nil || result.MatchedCount == 0 {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Status updated successfully", gin.H{
		"status": body.Status,
	})
}

// UploadImages handles POST /api/products/:productId/images - adds images to a product.
func (h *Handlers) UploadImages(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	productID, err := primitive.ObjectIDFromHex(c.Param("productId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	form, _ := c.MultipartForm()
	if form == nil || form.File["images"] == nil || len(form.File["images"]) == 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "No images provided")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	col := config.GetCollection("products")

	var product models.Product
	err = col.FindOne(ctx, bson.M{"_id": productID, "userId": user.ID}).Decode(&product)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	var uploaded []models.ProductImage
	existingCount := len(product.Images)

	for i, fileHeader := range form.File["images"] {
		file, err := fileHeader.Open()
		if err != nil {
			continue
		}
		fileData, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			continue
		}

		result, err := middleware.UploadToCloudinary(h.Config, fileData, "products")
		if err != nil {
			log.Printf("Cloudinary upload error: %v", err)
			continue
		}

		img := models.ProductImage{
			URL:      result.URL,
			PublicID: result.PublicID,
			Order:    existingCount + i,
		}
		uploaded = append(uploaded, img)
	}

	if len(uploaded) > 0 {
		_, err = col.UpdateOne(ctx, bson.M{"_id": productID}, bson.M{
			"$push":   bson.M{"images": bson.M{"$each": uploaded}},
			"$set":    bson.M{"updatedAt": time.Now()},
		})
		if err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to upload images")
			return
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Images uploaded successfully", gin.H{
		"images": uploaded,
	})
}

// DeleteImage handles DELETE /api/products/:productId/images/:imageId - removes an image from a product.
func (h *Handlers) DeleteImage(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	productID, err := primitive.ObjectIDFromHex(c.Param("productId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	imageID, err := primitive.ObjectIDFromHex(c.Param("imageId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid image ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	col := config.GetCollection("products")

	var product models.Product
	err = col.FindOne(ctx, bson.M{"_id": productID, "userId": user.ID}).Decode(&product)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	// Find image
	imageIndex := -1
	for i, img := range product.Images {
		if img.ID == imageID {
			imageIndex = i
			break
		}
	}
	if imageIndex == -1 {
		utils.ErrorResponse(c, http.StatusNotFound, "Image not found")
		return
	}

	// Delete from Cloudinary
	if product.Images[imageIndex].PublicID != "" {
		_ = middleware.DeleteFromCloudinary(h.Config, product.Images[imageIndex].PublicID)
	}

	// Remove from array and reorder
	newImages := make([]models.ProductImage, 0, len(product.Images)-1)
	for i, img := range product.Images {
		if i != imageIndex {
			img.Order = len(newImages)
			newImages = append(newImages, img)
		}
	}

	_, err = col.UpdateOne(ctx, bson.M{"_id": productID}, bson.M{
		"$set": bson.M{"images": newImages, "updatedAt": time.Now()},
	})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete image")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Image deleted successfully", nil)
}

// ToggleFeatured handles PATCH /api/products/:id/featured - toggles featured status (admin).
func (h *Handlers) ToggleFeatured(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	productID, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	col := config.GetCollection("products")

	var product models.Product
	err = col.FindOne(ctx, bson.M{"_id": productID}).Decode(&product)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	// Check ownership or admin
	if product.UserID != user.ID && user.Role != models.RoleAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Not authorized to update this product")
		return
	}

	newFeatured := !product.IsFeatured
	_, err = col.UpdateOne(ctx, bson.M{"_id": productID}, bson.M{
		"$set": bson.M{"isFeatured": newFeatured, "updatedAt": time.Now()},
	})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Server error")
		return
	}

	msg := "Product removed from featured"
	if newFeatured {
		msg = "Product added to featured"
	}

	var featuredAt *time.Time
	if newFeatured {
		now := time.Now()
		featuredAt = &now
	}

	utils.SuccessResponse(c, http.StatusOK, msg, gin.H{
		"productId":  product.ID,
		"isFeatured": newFeatured,
		"featuredAt": featuredAt,
	})
}

// GetProductStats handles GET /api/products/stats - returns aggregated product statistics.
func (h *Handlers) GetProductStats(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	col := config.GetCollection("products")

	// Aggregation: group by status
	pipeline := bson.A{
		bson.M{"$match": bson.M{"userId": user.ID}},
		bson.M{"$group": bson.M{
			"_id":        "$status",
			"count":      bson.M{"$sum": 1},
			"totalValue": bson.M{"$sum": bson.M{"$multiply": bson.A{"$price", "$stock"}}},
			"totalStock": bson.M{"$sum": "$stock"},
			"totalSales": bson.M{"$sum": "$sales"},
			"totalViews": bson.M{"$sum": "$views"},
		}},
	}

	cursor, err := col.Aggregate(ctx, pipeline)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch product statistics")
		return
	}
	defer cursor.Close(ctx)

	var stats []bson.M
	if err := cursor.All(ctx, &stats); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode statistics")
		return
	}

	totalProducts, _ := col.CountDocuments(ctx, bson.M{"userId": user.ID})

	// Total value aggregation
	valuePipeline := bson.A{
		bson.M{"$match": bson.M{"userId": user.ID}},
		bson.M{"$group": bson.M{
			"_id":   nil,
			"total": bson.M{"$sum": bson.M{"$multiply": bson.A{"$price", "$stock"}}},
		}},
	}
	valueCursor, err := col.Aggregate(ctx, valuePipeline)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch product statistics")
		return
	}
	defer valueCursor.Close(ctx)

	var valueResults []bson.M
	_ = valueCursor.All(ctx, &valueResults)
	totalValue := 0.0
	if len(valueResults) > 0 {
		if v, ok := valueResults[0]["total"].(float64); ok {
			totalValue = v
		} else if v, ok := valueResults[0]["total"].(int64); ok {
			totalValue = float64(v)
		} else if v, ok := valueResults[0]["total"].(int32); ok {
			totalValue = float64(v)
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Product statistics fetched successfully", gin.H{
		"stats": stats,
		"totals": gin.H{
			"products": totalProducts,
			"value":    totalValue,
		},
	})
}

// GetNonFeaturedProducts handles GET /api/products/non-featured - returns non-featured products (admin/merchant).
func (h *Handlers) GetNonFeaturedProducts(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	col := config.GetCollection("products")

	cursor, err := col.Find(ctx, bson.M{"isFeatured": false})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products")
		return
	}
	defer cursor.Close(ctx)

	var products []models.Product
	if err := cursor.All(ctx, &products); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode products")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Non-featured products fetched successfully", products)
}

// GetProductsByCategory handles GET /api/products/public/category - public product listing with extensive filtering.
func (h *Handlers) GetProductsByCategory(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	categoryIDStr := c.Query("categoryId")
	subcategoryIDStr := c.Query("subcategoryId")
	sortBy := c.DefaultQuery("sortBy", "createdAt")
	sortOrder := c.DefaultQuery("sortOrder", "desc")
	minPriceStr := c.Query("minPrice")
	maxPriceStr := c.Query("maxPrice")
	productType := c.Query("type")
	status := c.DefaultQuery("status", "active")
	search := c.Query("search")

	filter := bson.M{}

	if categoryIDStr != "" {
		if catOID, err := primitive.ObjectIDFromHex(categoryIDStr); err == nil {
			filter["category"] = catOID
		}
	}

	if productType != "" && (productType == "Product" || productType == "Service") {
		filter["type"] = productType
	}

	validStatuses := map[string]bool{"active": true, "inactive": true, "sold_out": true, "pending_review": true}
	if validStatuses[status] {
		filter["status"] = status
	} else {
		filter["status"] = "active"
	}

	if minPriceStr != "" || maxPriceStr != "" {
		priceFilter := bson.M{}
		if minPriceStr != "" {
			if v, err := strconv.ParseFloat(minPriceStr, 64); err == nil {
				priceFilter["$gte"] = v
			}
		}
		if maxPriceStr != "" {
			if v, err := strconv.ParseFloat(maxPriceStr, 64); err == nil {
				priceFilter["$lte"] = v
			}
		}
		if len(priceFilter) > 0 {
			filter["price"] = priceFilter
		}
	}

	if search != "" {
		filter["$or"] = bson.A{
			bson.M{"name": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"description": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"companyName": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"attributes.value": bson.M{"$regex": search, "$options": "i"}},
		}
	}

	// Handle subcategory by name
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if subcategoryIDStr != "" {
		if _, err := primitive.ObjectIDFromHex(subcategoryIDStr); err != nil {
			// Not a valid ObjectID, search by name
			catCol := config.GetCollection("categories")
			catCursor, err := catCol.Find(ctx, bson.M{
				"title": bson.M{"$regex": subcategoryIDStr, "$options": "i"},
			})
			if err == nil {
				var cats []models.Category
				if catCursor.All(ctx, &cats) == nil && len(cats) > 0 {
					ids := make(bson.A, len(cats))
					for i, cat := range cats {
						ids[i] = cat.ID
					}
					filter["category"] = bson.M{"$in": ids}
				}
				catCursor.Close(ctx)
			}
		}
	}

	sortDir := -1
	if sortOrder == "asc" {
		sortDir = 1
	}

	validSortFields := map[string]bool{
		"createdAt": true, "updatedAt": true, "price": true,
		"name": true, "views": true, "sales": true, "rating.average": true,
	}
	if !validSortFields[sortBy] {
		sortBy = "createdAt"
	}

	skip := int64((page - 1) * limit)
	limitInt := int64(limit)

	col := config.GetCollection("products")

	total, err := col.CountDocuments(ctx, filter)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products by category")
		return
	}

	opts := options.Find().
		SetSort(bson.D{{Key: sortBy, Value: sortDir}}).
		SetSkip(skip).
		SetLimit(limitInt)

	cursor, err := col.Find(ctx, filter, opts)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products by category")
		return
	}
	defer cursor.Close(ctx)

	var products []models.Product
	if err := cursor.All(ctx, &products); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode products")
		return
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	// Get category details if categoryId is provided
	var categoryDetails *models.Category
	if categoryIDStr != "" {
		if catOID, err := primitive.ObjectIDFromHex(categoryIDStr); err == nil {
			var cat models.Category
			catCol := config.GetCollection("categories")
			if catCol.FindOne(ctx, bson.M{"_id": catOID}).Decode(&cat) == nil {
				categoryDetails = &cat
			}
		}
	}

	// Price stats aggregation
	statsPipeline := bson.A{
		bson.M{"$match": filter},
		bson.M{"$group": bson.M{
			"_id":           nil,
			"minPrice":      bson.M{"$min": "$price"},
			"maxPrice":      bson.M{"$max": "$price"},
			"avgPrice":      bson.M{"$avg": "$price"},
			"totalProducts": bson.M{"$sum": 1},
			"totalInStock":  bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$gt": bson.A{"$stock", 0}}, 1, 0}}},
		}},
	}

	statsCursor, err := col.Aggregate(ctx, statsPipeline)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch product statistics")
		return
	}
	defer statsCursor.Close(ctx)

	var statsResults []bson.M
	_ = statsCursor.All(ctx, &statsResults)

	priceStats := gin.H{
		"minPrice": 0, "maxPrice": 0, "avgPrice": 0, "totalProducts": 0, "totalInStock": 0,
	}
	if len(statsResults) > 0 {
		priceStats = gin.H{
			"minPrice":      statsResults[0]["minPrice"],
			"maxPrice":      statsResults[0]["maxPrice"],
			"avgPrice":      statsResults[0]["avgPrice"],
			"totalProducts": statsResults[0]["totalProducts"],
			"totalInStock":  statsResults[0]["totalInStock"],
		}
	}

	totalProductsCount := 0
	totalInStockCount := 0
	if v, ok := priceStats["totalProducts"]; ok {
		switch tv := v.(type) {
		case int32:
			totalProductsCount = int(tv)
		case int64:
			totalProductsCount = int(tv)
		}
	}
	if v, ok := priceStats["totalInStock"]; ok {
		switch tv := v.(type) {
		case int32:
			totalInStockCount = int(tv)
		case int64:
			totalInStockCount = int(tv)
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Products fetched successfully", gin.H{
		"products": products,
		"category": categoryDetails,
		"pagination": gin.H{
			"page":        page,
			"limit":       limit,
			"total":       total,
			"totalPages":  totalPages,
			"hasNextPage": page < totalPages,
			"hasPrevPage": page > 1,
		},
		"filters": gin.H{
			"applied": gin.H{
				"categoryId": categoryIDStr,
				"type":       productType,
				"status":     status,
				"minPrice":   minPriceStr,
				"maxPrice":   maxPriceStr,
				"search":     search,
			},
			"stats": gin.H{
				"priceRange": gin.H{
					"min":     priceStats["minPrice"],
					"max":     priceStats["maxPrice"],
					"average": priceStats["avgPrice"],
				},
				"count": gin.H{
					"total":      totalProductsCount,
					"inStock":    totalInStockCount,
					"outOfStock": totalProductsCount - totalInStockCount,
				},
			},
		},
	})
}

// GetProductsByCategoryTree handles GET /api/products/public/category/tree/:categoryId - products with subcategory grouping.
func (h *Handlers) GetProductsByCategoryTree(c *gin.Context) {
	categoryIDStr := c.Param("categoryId")
	categoryID, err := primitive.ObjectIDFromHex(categoryIDStr)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}

	includeSubcategories := c.DefaultQuery("includeSubcategories", "true")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	catCol := config.GetCollection("categories")
	prodCol := config.GetCollection("products")

	// Collect category IDs
	categoryIDs := bson.A{categoryID}
	if includeSubcategories == "true" {
		cursor, err := catCol.Find(ctx, bson.M{
			"$or": bson.A{
				bson.M{"_id": categoryID},
				bson.M{"parentCategory": categoryID},
			},
		})
		if err == nil {
			var cats []models.Category
			if cursor.All(ctx, &cats) == nil {
				categoryIDs = make(bson.A, len(cats))
				for i, cat := range cats {
					categoryIDs[i] = cat.ID
				}
			}
			cursor.Close(ctx)
		}
	}

	// Get products
	cursor, err := prodCol.Find(ctx,
		bson.M{"category": bson.M{"$in": categoryIDs}, "status": "active"},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}),
	)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products by category tree")
		return
	}
	defer cursor.Close(ctx)

	var products []models.Product
	if err := cursor.All(ctx, &products); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode products")
		return
	}

	// Main category details
	var mainCategory models.Category
	catCol.FindOne(ctx, bson.M{"_id": categoryID}).Decode(&mainCategory)

	// Group by subcategory
	groupedBySubcategory := make(map[string]gin.H)
	inStockCount := 0
	outOfStockCount := 0

	for _, p := range products {
		if p.Stock > 0 {
			inStockCount++
		} else {
			outOfStockCount++
		}

		if p.Category != nil {
			catID := p.Category.Hex()
			if _, ok := groupedBySubcategory[catID]; !ok {
				groupedBySubcategory[catID] = gin.H{
					"category": p.Category,
					"products": []models.Product{},
				}
			}
			entry := groupedBySubcategory[catID]
			entry["products"] = append(entry["products"].([]models.Product), p)
			groupedBySubcategory[catID] = entry
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Products fetched successfully", gin.H{
		"mainCategory":         mainCategory,
		"products":             products,
		"groupedBySubcategory": groupedBySubcategory,
		"statistics": gin.H{
			"totalProducts":      len(products),
			"totalSubcategories": len(groupedBySubcategory),
			"productsByStatus": gin.H{
				"inStock":    inStockCount,
				"outOfStock": outOfStockCount,
			},
		},
	})
}

// GetMarketplaceProducts handles GET /api/products/public/marketplace - public marketplace listing with sorting.
func (h *Handlers) GetMarketplaceProducts(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	category := c.Query("category")
	productType := c.Query("type")
	location := c.Query("location")
	minPriceStr := c.Query("minPrice")
	maxPriceStr := c.Query("maxPrice")
	sortParam := c.DefaultQuery("sort", "recent")
	search := c.Query("search")

	filter := bson.M{"status": "active"}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Category filter
	if category != "" {
		if catOID, err := primitive.ObjectIDFromHex(category); err == nil {
			filter["category"] = catOID
		} else {
			// Search by category name
			catCol := config.GetCollection("categories")
			catCursor, err := catCol.Find(ctx, bson.M{
				"title": bson.M{"$regex": category, "$options": "i"},
			})
			if err == nil {
				var cats []models.Category
				if catCursor.All(ctx, &cats) == nil && len(cats) > 0 {
					ids := make(bson.A, len(cats))
					for i, cat := range cats {
						ids[i] = cat.ID
					}
					filter["category"] = bson.M{"$in": ids}
				}
				catCursor.Close(ctx)
			}
		}
	}

	if productType != "" && (productType == "Product" || productType == "Service") {
		filter["type"] = productType
	}

	if location != "" {
		filter["location"] = bson.M{"$regex": location, "$options": "i"}
	}

	if minPriceStr != "" || maxPriceStr != "" {
		priceFilter := bson.M{}
		if minPriceStr != "" {
			if v, err := strconv.ParseFloat(minPriceStr, 64); err == nil {
				priceFilter["$gte"] = v
			}
		}
		if maxPriceStr != "" {
			if v, err := strconv.ParseFloat(maxPriceStr, 64); err == nil {
				priceFilter["$lte"] = v
			}
		}
		if len(priceFilter) > 0 {
			filter["price"] = priceFilter
		}
	}

	if search != "" {
		filter["$or"] = bson.A{
			bson.M{"name": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"description": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"companyName": bson.M{"$regex": search, "$options": "i"}},
		}
	}

	// Sort options
	sortOptions := map[string]bson.D{
		"recent":     {{Key: "createdAt", Value: -1}},
		"popular":    {{Key: "views", Value: -1}, {Key: "sales", Value: -1}},
		"price_low":  {{Key: "price", Value: 1}},
		"price_high": {{Key: "price", Value: -1}},
		"rating":     {{Key: "rating.average", Value: -1}, {Key: "rating.count", Value: -1}},
	}

	sortQuery, ok := sortOptions[sortParam]
	if !ok {
		sortQuery = sortOptions["recent"]
	}

	skip := int64((page - 1) * limit)
	limitInt := int64(limit)

	col := config.GetCollection("products")

	total, err := col.CountDocuments(ctx, filter)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch marketplace products")
		return
	}

	opts := options.Find().SetSort(sortQuery).SetSkip(skip).SetLimit(limitInt)
	cursor, err := col.Find(ctx, filter, opts)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch marketplace products")
		return
	}
	defer cursor.Close(ctx)

	var products []models.Product
	if err := cursor.All(ctx, &products); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode products")
		return
	}

	// Get featured products
	featuredFilter := bson.M{}
	for k, v := range filter {
		featuredFilter[k] = v
	}
	featuredFilter["isFeatured"] = true

	featuredCursor, err := col.Find(ctx, featuredFilter, options.Find().SetLimit(5))
	var featuredProducts []models.Product
	if err == nil {
		_ = featuredCursor.All(ctx, &featuredProducts)
		featuredCursor.Close(ctx)
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	utils.SuccessResponse(c, http.StatusOK, "Marketplace products fetched successfully", gin.H{
		"products":         products,
		"featuredProducts": featuredProducts,
		"pagination": gin.H{
			"page":        page,
			"limit":       limit,
			"total":       total,
			"totalPages":  totalPages,
			"hasNextPage": page < totalPages,
			"hasPrevPage": page > 1,
		},
		"filters": gin.H{
			"category":   category,
			"type":       productType,
			"location":   location,
			"priceRange": gin.H{"minPrice": minPriceStr, "maxPrice": maxPriceStr},
			"sort":       sortParam,
			"search":     search,
		},
	})
}

// updateCategoryProductCount updates the productCount field for a category.
func updateCategoryProductCount(ctx context.Context, categoryID primitive.ObjectID) {
	prodCol := config.GetCollection("products")
	catCol := config.GetCollection("categories")

	count, err := prodCol.CountDocuments(ctx, bson.M{
		"category": categoryID,
		"status":   "active",
	})
	if err != nil {
		log.Printf("Error counting products for category %s: %v", categoryID.Hex(), err)
		return
	}

	_, err = catCol.UpdateOne(ctx, bson.M{"_id": categoryID}, bson.M{
		"$set": bson.M{"productCount": count},
	})
	if err != nil {
		log.Printf("Error updating category product count: %v", err)
	}
}
