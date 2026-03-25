package handlers

import (
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
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
	var categoryID *uuid.UUID
	var categoryName string
	if categoryStr != "" {
		catOID, err := uuid.Parse(categoryStr)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
			return
		}

		var cat models.Category
		if err := config.DB.First(&cat, "id = ?", catOID).Error; err != nil {
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

			if result.URL == "" {
				log.Printf("Cloudinary upload returned empty URL for file %s", fileHeader.Filename)
				continue
			}

			uploadedImages = append(uploadedImages, models.ProductImage{
				ID:       uuid.New(),
				URL:      result.URL,
				PublicID: result.PublicID,
				Order:    i,
			})
		}
	}

	now := time.Now()
	product := models.Product{
		ID:           uuid.New(),
		UserID:       user.ID,
		Name:         name,
		Description:  description,
		Price:        price,
		Stock:        stock,
		CategoryID:   categoryID,
		CategoryName: categoryName,
		Location:     location,
		PhoneNumber:  phoneNumber,
		Email:        strings.ToLower(strings.TrimSpace(email)),
		CompanyName:  companyName,
		Status:       models.ProductStatusActive,
		Type:         productType,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	product.SetImages(uploadedImages)
	product.SetAttributes(parsedAttributes)

	if err := config.DB.Create(&product).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create product")
		return
	}

	// Update category product count
	if categoryID != nil {
		updateCategoryProductCount(*categoryID)
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
	sortBy := c.DefaultQuery("sortBy", "created_at")
	sortOrder := c.DefaultQuery("sortOrder", "desc")

	query := config.DB.Model(&models.Product{}).Where("user_id = ?", user.ID)

	if status != "" {
		query = query.Where("status = ?", status)
	}
	if category != "" {
		catOID, err := uuid.Parse(category)
		if err == nil {
			query = query.Where("category_id = ?", catOID)
		}
	}
	if search != "" {
		query = query.Where("(name ILIKE ? OR description ILIKE ? OR company_name ILIKE ?)",
			"%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products")
		return
	}

	// Map JSON field names to DB column names
	sortFieldMap := map[string]string{
		"createdAt": "created_at",
		"updatedAt": "updated_at",
		"price":     "price",
		"name":      "name",
		"views":     "views",
		"sales":     "sales",
	}
	if mapped, ok := sortFieldMap[sortBy]; ok {
		sortBy = mapped
	}

	orderClause := sortBy + " " + strings.ToUpper(sortOrder)
	offset := (page - 1) * limit

	var products []models.Product
	if err := query.Order(orderClause).Offset(offset).Limit(limit).Find(&products).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products")
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
	rawID := c.Param("id")
	log.Printf("[GetProductByID] raw param 'id': %q (len=%d)", rawID, len(rawID))
	productID, err := uuid.Parse(rawID)
	if err != nil {
		log.Printf("[GetProductByID] uuid.Parse failed: %v", err)
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	var product models.Product
	if err := config.DB.First(&product, "id = ?", productID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	// Increment views
	config.DB.Model(&product).Update("views", product.Views+1)
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

	rawID := c.Param("id")
	log.Printf("[UpdateProduct] raw param 'id': %q (len=%d)", rawID, len(rawID))
	productID, err := uuid.Parse(rawID)
	if err != nil {
		log.Printf("[UpdateProduct] uuid.Parse failed: %v", err)
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	var product models.Product
	if err := config.DB.Where("id = ? AND user_id = ?", productID, user.ID).First(&product).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	updates := map[string]interface{}{}

	if name := c.PostForm("name"); name != "" {
		updates["name"] = name
	}
	if desc := c.PostForm("description"); desc != "" {
		updates["description"] = desc
	}
	if priceStr := c.PostForm("price"); priceStr != "" {
		if p, err := strconv.ParseFloat(priceStr, 64); err == nil {
			updates["price"] = p
		}
	}
	if stockStr := c.PostForm("stock"); stockStr != "" {
		if s, err := strconv.Atoi(stockStr); err == nil {
			updates["stock"] = s
		}
	}
	if location := c.PostForm("location"); location != "" {
		updates["location"] = location
	}
	if phone := c.PostForm("phoneNumber"); phone != "" {
		updates["phone_number"] = phone
	}
	if email := c.PostForm("email"); email != "" {
		updates["email"] = strings.ToLower(strings.TrimSpace(email))
	}
	if company := c.PostForm("companyName"); company != "" {
		updates["company_name"] = company
	}
	if t := c.PostForm("type"); t != "" {
		updates["type"] = t
	}

	// Parse attributes
	if attrStr := c.PostForm("attributes"); attrStr != "" {
		var attrs []models.ProductAttribute
		if err := json.Unmarshal([]byte(attrStr), &attrs); err == nil {
			attrJSON, _ := json.Marshal(attrs)
			updates["attributes"] = attrJSON
		}
	}

	// Handle category update
	if catStr := c.PostForm("category"); catStr != "" {
		catOID, err := uuid.Parse(catStr)
		if err == nil {
			updates["category_id"] = catOID
		}
	}

	// Handle new image uploads
	form, _ := c.MultipartForm()
	if form != nil && form.File["images"] != nil && len(form.File["images"]) > 0 {
		// Delete old images from Cloudinary
		for _, img := range product.GetImages() {
			if img.PublicID != "" {
				_ = middleware.DeleteFromCloudinary(h.Config, img.PublicID)
			}
		}

		var newImages []models.ProductImage
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

			if result.URL == "" {
				log.Printf("Cloudinary upload returned empty URL for file %s", fileHeader.Filename)
				continue
			}

			newImages = append(newImages, models.ProductImage{
				ID:       uuid.New(),
				URL:      result.URL,
				PublicID: result.PublicID,
				Order:    i,
			})
		}
		imagesJSON, _ := json.Marshal(newImages)
		updates["images"] = imagesJSON
	}

	if len(updates) == 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "No fields to update")
		return
	}
	updates["updated_at"] = time.Now()

	if err := config.DB.Model(&product).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update product")
		return
	}

	// Re-fetch updated product
	var updated models.Product
	config.DB.First(&updated, "id = ?", productID)

	// Update category product count if category changed
	if catID, ok := updates["category_id"]; ok {
		if oid, ok := catID.(uuid.UUID); ok {
			updateCategoryProductCount(oid)
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

	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	var product models.Product
	if err := config.DB.Where("id = ? AND user_id = ?", productID, user.ID).First(&product).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	// Delete images from Cloudinary
	for _, img := range product.GetImages() {
		if img.PublicID != "" {
			_ = middleware.DeleteFromCloudinary(h.Config, img.PublicID)
		}
	}

	categoryID := product.CategoryID

	if err := config.DB.Delete(&product).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete product")
		return
	}

	if categoryID != nil {
		updateCategoryProductCount(*categoryID)
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

	productID, err := uuid.Parse(c.Param("id"))
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

	result := config.DB.Model(&models.Product{}).
		Where("id = ? AND user_id = ?", productID, user.ID).
		Updates(map[string]interface{}{"stock": body.Quantity, "updated_at": time.Now()})
	if result.Error != nil || result.RowsAffected == 0 {
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

	productID, err := uuid.Parse(c.Param("id"))
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

	result := config.DB.Model(&models.Product{}).
		Where("id = ? AND user_id = ?", productID, user.ID).
		Updates(map[string]interface{}{"status": body.Status, "updated_at": time.Now()})
	if result.Error != nil || result.RowsAffected == 0 {
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

	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	form, _ := c.MultipartForm()
	if form == nil || form.File["images"] == nil || len(form.File["images"]) == 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "No images provided")
		return
	}

	var product models.Product
	if err := config.DB.Where("id = ? AND user_id = ?", productID, user.ID).First(&product).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	existingImages := product.GetImages()
	var uploaded []models.ProductImage
	existingCount := len(existingImages)

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
			ID:       uuid.New(),
			URL:      result.URL,
			PublicID: result.PublicID,
			Order:    existingCount + i,
		}
		uploaded = append(uploaded, img)
	}

	if len(uploaded) > 0 {
		allImages := append(existingImages, uploaded...)
		product.SetImages(allImages)
		product.UpdatedAt = time.Now()
		if err := config.DB.Save(&product).Error; err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to upload images")
			return
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Images uploaded successfully", gin.H{
		"images": uploaded,
	})
}

// DeleteProductImage handles DELETE /api/products/:productId/images/:imageId - removes an image from a product.
func (h *Handlers) DeleteProductImage(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	imageID, err := uuid.Parse(c.Param("imageId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid image ID")
		return
	}

	var product models.Product
	if err := config.DB.Where("id = ? AND user_id = ?", productID, user.ID).First(&product).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	images := product.GetImages()

	// Find image
	imageIndex := -1
	for i, img := range images {
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
	if images[imageIndex].PublicID != "" {
		_ = middleware.DeleteFromCloudinary(h.Config, images[imageIndex].PublicID)
	}

	// Remove from array and reorder
	newImages := make([]models.ProductImage, 0, len(images)-1)
	for i, img := range images {
		if i != imageIndex {
			img.Order = len(newImages)
			newImages = append(newImages, img)
		}
	}

	product.SetImages(newImages)
	product.UpdatedAt = time.Now()
	if err := config.DB.Save(&product).Error; err != nil {
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

	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	var product models.Product
	if err := config.DB.First(&product, "id = ?", productID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	// Check ownership or admin
	if product.UserID != user.ID && user.Role != models.RoleAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Not authorized to update this product")
		return
	}

	newFeatured := !product.IsFeatured
	if err := config.DB.Model(&product).Updates(map[string]interface{}{
		"is_featured": newFeatured,
		"updated_at":  time.Now(),
	}).Error; err != nil {
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

	baseQuery := config.DB.Model(&models.Product{}).Where("user_id = ?", user.ID)

	var totalProducts int64
	baseQuery.Count(&totalProducts)

	// Status counts
	type StatusCount struct {
		Status     string  `json:"_id"`
		Count      int64   `json:"count"`
		TotalValue float64 `json:"totalValue"`
		TotalStock int64   `json:"totalStock"`
		TotalSales int64   `json:"totalSales"`
		TotalViews int64   `json:"totalViews"`
	}

	var stats []StatusCount
	config.DB.Model(&models.Product{}).
		Select("status as _id, COUNT(*) as count, COALESCE(SUM(price * stock), 0) as total_value, COALESCE(SUM(stock), 0) as total_stock, COALESCE(SUM(sales), 0) as total_sales, COALESCE(SUM(views), 0) as total_views").
		Where("user_id = ?", user.ID).
		Group("status").
		Find(&stats)

	// Total inventory value
	var totalValue float64
	config.DB.Model(&models.Product{}).
		Select("COALESCE(SUM(price * stock), 0)").
		Where("user_id = ?", user.ID).
		Row().Scan(&totalValue)

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
	var products []models.Product
	if err := config.DB.Where("is_featured = ?", false).Find(&products).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products")
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
	sortBy := c.DefaultQuery("sortBy", "created_at")
	sortOrder := c.DefaultQuery("sortOrder", "desc")
	minPriceStr := c.Query("minPrice")
	maxPriceStr := c.Query("maxPrice")
	productType := c.Query("type")
	status := c.DefaultQuery("status", "active")
	search := c.Query("search")

	query := config.DB.Model(&models.Product{})

	if categoryIDStr != "" {
		if catOID, err := uuid.Parse(categoryIDStr); err == nil {
			query = query.Where("category_id = ?", catOID)
		}
	}

	if productType != "" && (productType == "Product" || productType == "Service") {
		query = query.Where("type = ?", productType)
	}

	validStatuses := map[string]bool{"active": true, "inactive": true, "sold_out": true, "pending_review": true}
	if validStatuses[status] {
		query = query.Where("status = ?", status)
	} else {
		query = query.Where("status = ?", "active")
	}

	if minPriceStr != "" {
		if v, err := strconv.ParseFloat(minPriceStr, 64); err == nil {
			query = query.Where("price >= ?", v)
		}
	}
	if maxPriceStr != "" {
		if v, err := strconv.ParseFloat(maxPriceStr, 64); err == nil {
			query = query.Where("price <= ?", v)
		}
	}

	if search != "" {
		query = query.Where("(name ILIKE ? OR description ILIKE ? OR company_name ILIKE ?)",
			"%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	// Handle subcategory by name
	if subcategoryIDStr != "" {
		if _, err := uuid.Parse(subcategoryIDStr); err != nil {
			// Not a valid UUID, search by name
			var cats []models.Category
			if config.DB.Where("title ILIKE ?", "%"+subcategoryIDStr+"%").Find(&cats).Error == nil && len(cats) > 0 {
				ids := make([]uuid.UUID, len(cats))
				for i, cat := range cats {
					ids[i] = cat.ID
				}
				query = query.Where("category_id IN ?", ids)
			}
		}
	}

	// Map JSON sort field names to DB column names
	sortFieldMap := map[string]string{
		"createdAt":      "created_at",
		"updatedAt":      "updated_at",
		"price":          "price",
		"name":           "name",
		"views":          "views",
		"sales":          "sales",
		"rating.average": "rating->>'average'",
	}
	if mapped, ok := sortFieldMap[sortBy]; ok {
		sortBy = mapped
	}
	if sortBy != "created_at" && sortBy != "updated_at" && sortBy != "price" &&
		sortBy != "name" && sortBy != "views" && sortBy != "sales" && sortBy != "rating->>'average'" {
		sortBy = "created_at"
	}

	var total int64
	query.Count(&total)

	orderClause := sortBy + " " + strings.ToUpper(sortOrder)
	offset := (page - 1) * limit

	var products []models.Product
	if err := query.Order(orderClause).Offset(offset).Limit(limit).Find(&products).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products by category")
		return
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	// Get category details if categoryId is provided
	var categoryDetails *models.Category
	if categoryIDStr != "" {
		if catOID, err := uuid.Parse(categoryIDStr); err == nil {
			var cat models.Category
			if config.DB.First(&cat, "id = ?", catOID).Error == nil {
				categoryDetails = &cat
			}
		}
	}

	// Price stats using simple queries
	type PriceStats struct {
		MinPrice      *float64 `json:"minPrice"`
		MaxPrice      *float64 `json:"maxPrice"`
		AvgPrice      *float64 `json:"avgPrice"`
		TotalProducts int      `json:"totalProducts"`
		TotalInStock  int      `json:"totalInStock"`
	}
	var priceStats PriceStats
	// We need a fresh query for stats since the original query has offset/limit applied
	statsQuery := config.DB.Model(&models.Product{})
	if categoryIDStr != "" {
		if catOID, err := uuid.Parse(categoryIDStr); err == nil {
			statsQuery = statsQuery.Where("category_id = ?", catOID)
		}
	}
	if productType != "" && (productType == "Product" || productType == "Service") {
		statsQuery = statsQuery.Where("type = ?", productType)
	}
	if validStatuses[status] {
		statsQuery = statsQuery.Where("status = ?", status)
	} else {
		statsQuery = statsQuery.Where("status = ?", "active")
	}
	if minPriceStr != "" {
		if v, err := strconv.ParseFloat(minPriceStr, 64); err == nil {
			statsQuery = statsQuery.Where("price >= ?", v)
		}
	}
	if maxPriceStr != "" {
		if v, err := strconv.ParseFloat(maxPriceStr, 64); err == nil {
			statsQuery = statsQuery.Where("price <= ?", v)
		}
	}
	if search != "" {
		statsQuery = statsQuery.Where("(name ILIKE ? OR description ILIKE ? OR company_name ILIKE ?)",
			"%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	statsQuery.Select("MIN(price) as min_price, MAX(price) as max_price, AVG(price) as avg_price, COUNT(*) as total_products, SUM(CASE WHEN stock > 0 THEN 1 ELSE 0 END) as total_in_stock").
		Row().Scan(&priceStats.MinPrice, &priceStats.MaxPrice, &priceStats.AvgPrice, &priceStats.TotalProducts, &priceStats.TotalInStock)

	minP := 0.0
	maxP := 0.0
	avgP := 0.0
	if priceStats.MinPrice != nil {
		minP = *priceStats.MinPrice
	}
	if priceStats.MaxPrice != nil {
		maxP = *priceStats.MaxPrice
	}
	if priceStats.AvgPrice != nil {
		avgP = *priceStats.AvgPrice
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
					"min":     minP,
					"max":     maxP,
					"average": avgP,
				},
				"count": gin.H{
					"total":      priceStats.TotalProducts,
					"inStock":    priceStats.TotalInStock,
					"outOfStock": priceStats.TotalProducts - priceStats.TotalInStock,
				},
			},
		},
	})
}

// GetProductsByCategoryTree handles GET /api/products/public/category/tree/:categoryId - products with subcategory grouping.
func (h *Handlers) GetProductsByCategoryTree(c *gin.Context) {
	categoryIDStr := c.Param("categoryId")
	categoryID, err := uuid.Parse(categoryIDStr)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}

	includeSubcategories := c.DefaultQuery("includeSubcategories", "true")

	// Collect category IDs
	categoryIDs := []uuid.UUID{categoryID}
	if includeSubcategories == "true" {
		var cats []models.Category
		if err := config.DB.Where("id = ? OR parent_category_id = ?", categoryID, categoryID).Find(&cats).Error; err == nil {
			categoryIDs = make([]uuid.UUID, len(cats))
			for i, cat := range cats {
				categoryIDs[i] = cat.ID
			}
		}
	}

	// Get products
	var products []models.Product
	if err := config.DB.Where("category_id IN ? AND status = ?", categoryIDs, "active").
		Order("created_at DESC").Find(&products).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products by category tree")
		return
	}

	// Main category details
	var mainCategory models.Category
	config.DB.First(&mainCategory, "id = ?", categoryID)

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

		if p.CategoryID != nil {
			catID := p.CategoryID.String()
			if _, ok := groupedBySubcategory[catID]; !ok {
				groupedBySubcategory[catID] = gin.H{
					"category": p.CategoryID,
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

// GetFeeConfig handles GET /api/fees - returns the platform fee schedule.
func (h *Handlers) GetFeeConfig(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Fee configuration", models.GetFeeConfig())
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

	query := config.DB.Model(&models.Product{}).Where("status = ?", "active")

	// Category filter
	if category != "" {
		if catOID, err := uuid.Parse(category); err == nil {
			query = query.Where("category_id = ?", catOID)
		} else {
			// Search by category name
			var cats []models.Category
			if config.DB.Where("title ILIKE ?", "%"+category+"%").Find(&cats).Error == nil && len(cats) > 0 {
				ids := make([]uuid.UUID, len(cats))
				for i, cat := range cats {
					ids[i] = cat.ID
				}
				query = query.Where("category_id IN ?", ids)
			}
		}
	}

	if productType != "" && (productType == "Product" || productType == "Service") {
		query = query.Where("type = ?", productType)
	}

	if location != "" {
		query = query.Where("location ILIKE ?", "%"+location+"%")
	}

	if minPriceStr != "" {
		if v, err := strconv.ParseFloat(minPriceStr, 64); err == nil {
			query = query.Where("price >= ?", v)
		}
	}
	if maxPriceStr != "" {
		if v, err := strconv.ParseFloat(maxPriceStr, 64); err == nil {
			query = query.Where("price <= ?", v)
		}
	}

	if search != "" {
		query = query.Where("(name ILIKE ? OR description ILIKE ? OR company_name ILIKE ?)",
			"%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	// Sort options
	sortOptions := map[string]string{
		"recent":     "created_at DESC",
		"popular":    "views DESC, sales DESC",
		"price_low":  "price ASC",
		"price_high": "price DESC",
		"rating":     "rating->>'average' DESC",
	}

	sortClause, ok := sortOptions[sortParam]
	if !ok {
		sortClause = sortOptions["recent"]
	}

	var total int64
	query.Count(&total)

	offset := (page - 1) * limit

	var products []models.Product
	if err := query.Order(sortClause).Offset(offset).Limit(limit).Find(&products).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch marketplace products")
		return
	}

	// Get featured products (same filters but featured only, limit 5)
	featuredQuery := config.DB.Model(&models.Product{}).Where("status = ? AND is_featured = ?", "active", true)
	if category != "" {
		if catOID, err := uuid.Parse(category); err == nil {
			featuredQuery = featuredQuery.Where("category_id = ?", catOID)
		}
	}
	if productType != "" && (productType == "Product" || productType == "Service") {
		featuredQuery = featuredQuery.Where("type = ?", productType)
	}

	var featuredProducts []models.Product
	featuredQuery.Limit(5).Find(&featuredProducts)

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
func updateCategoryProductCount(categoryID uuid.UUID) {
	var count int64
	if err := config.DB.Model(&models.Product{}).Where("category_id = ? AND status = ?", categoryID, "active").Count(&count).Error; err != nil {
		log.Printf("Error counting products for category %s: %v", categoryID.String(), err)
		return
	}

	if err := config.DB.Model(&models.Category{}).Where("id = ?", categoryID).Update("product_count", count).Error; err != nil {
		log.Printf("Error updating category product count: %v", err)
	}
}
