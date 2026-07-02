package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// ownedServiceProduct loads a Service product by ID and verifies the caller owns
// it. Returns the product (nil on failure) and writes the error response itself.
// Shared by the service profile / menu / availability write handlers.
func ownedServiceProduct(c *gin.Context, productID uuid.UUID) *models.Product {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil
	}
	var product models.Product
	if err := config.DB.Where("id = ? AND user_id = ?", productID, user.ID).First(&product).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Service not found")
		return nil
	}
	if product.Type != models.ProductTypeService {
		utils.ErrorResponse(c, http.StatusBadRequest, "Not a service listing")
		return nil
	}
	return &product
}

// ListServiceMenu handles GET /api/services/:id/menu — public list of a service's
// menu items (ordered by sort order). Used by the buyer's menu picker and the
// merchant's editor.
func (h *Handlers) ListServiceMenu(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}
	var items []models.ServiceMenuItem
	if err := config.DB.Where("product_id = ?", productID).
		Order("sort_order asc, created_at asc").Find(&items).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch menu")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Menu fetched successfully", gin.H{"items": items})
}

type menuItemRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	PriceUSD    float64 `json:"priceUsd"`
	ImageURL    string  `json:"imageUrl"`
	IsAvailable *bool   `json:"isAvailable"`
	SortOrder   int     `json:"sortOrder"`
}

// CreateServiceMenuItem handles POST /api/services/:id/menu — merchant adds a menu
// item to their Order-type service.
func (h *Handlers) CreateServiceMenuItem(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}
	product := ownedServiceProduct(c, productID)
	if product == nil {
		return
	}
	var req menuItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid menu item")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Item name is required")
		return
	}
	if req.PriceUSD < 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Item price cannot be negative")
		return
	}
	available := true
	if req.IsAvailable != nil {
		available = *req.IsAvailable
	}
	item := models.ServiceMenuItem{
		ID:          uuid.New(),
		ProductID:   productID,
		Name:        strings.TrimSpace(req.Name),
		Description: req.Description,
		PriceUSD:    req.PriceUSD,
		ImageURL:    req.ImageURL,
		IsAvailable: available,
		SortOrder:   req.SortOrder,
	}
	if err := config.DB.Create(&item).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create menu item")
		return
	}
	// The default:true column tag makes GORM drop a false IsAvailable on Create;
	// persist the merchant's explicit choice with an update.
	if !available {
		config.DB.Model(&item).Update("is_available", false)
		item.IsAvailable = false
	}
	utils.SuccessResponse(c, http.StatusCreated, "Menu item created", item)
}

// UpdateServiceMenuItem handles PUT /api/services/:id/menu/:itemId.
func (h *Handlers) UpdateServiceMenuItem(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}
	itemID, err := uuid.Parse(c.Param("itemId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid item ID")
		return
	}
	if ownedServiceProduct(c, productID) == nil {
		return
	}
	var item models.ServiceMenuItem
	if err := config.DB.Where("id = ? AND product_id = ?", itemID, productID).First(&item).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Menu item not found")
		return
	}
	var req menuItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid menu item")
		return
	}
	updates := map[string]interface{}{}
	if strings.TrimSpace(req.Name) != "" {
		updates["name"] = strings.TrimSpace(req.Name)
	}
	updates["description"] = req.Description
	if req.PriceUSD >= 0 {
		updates["price_usd"] = req.PriceUSD
	}
	updates["image_url"] = req.ImageURL
	if req.IsAvailable != nil {
		updates["is_available"] = *req.IsAvailable
	}
	updates["sort_order"] = req.SortOrder
	if err := config.DB.Model(&item).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update menu item")
		return
	}
	config.DB.First(&item, "id = ?", itemID)
	utils.SuccessResponse(c, http.StatusOK, "Menu item updated", item)
}

// DeleteServiceMenuItem handles DELETE /api/services/:id/menu/:itemId.
func (h *Handlers) DeleteServiceMenuItem(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}
	itemID, err := uuid.Parse(c.Param("itemId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid item ID")
		return
	}
	if ownedServiceProduct(c, productID) == nil {
		return
	}
	res := config.DB.Where("id = ? AND product_id = ?", itemID, productID).Delete(&models.ServiceMenuItem{})
	if res.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete menu item")
		return
	}
	if res.RowsAffected == 0 {
		utils.ErrorResponse(c, http.StatusNotFound, "Menu item not found")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Menu item deleted", gin.H{"id": itemID})
}
