package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// promoProductsOnly is why a service listing cannot be put on promotion yet.
const promoProductsOnly = "Promotions are available on products only"

// promoFormTerms reads the merchant's promotion fields from a product form.
// present is false when the form doesn't mention promotions at all, so an
// update that only edits the name leaves the promo untouched.
func promoFormTerms(c *gin.Context, price float64) (terms models.PromoTerms, present bool, errMsg string) {
	raw, ok := c.GetPostForm("promoEnabled")
	if !ok {
		return terms, false, ""
	}
	terms.Enabled = raw == "true" || raw == "1"
	if !terms.Enabled {
		return terms, true, ""
	}
	req, err := strconv.Atoi(c.PostForm("promoRequiredReferrals"))
	if err != nil || req < 1 {
		return terms, true, "Required referrals must be at least 1"
	}
	terms.RequiredReferrals = req
	terms.Price, err = strconv.ParseFloat(c.PostForm("promoPrice"), 64)
	if err != nil || terms.Price <= 0 {
		return terms, true, "Enter a promotion price"
	}
	if terms.Price >= price {
		return terms, true, "Promotion price must be lower than the original price"
	}
	if cs := c.PostForm("promoCountdownSeconds"); cs != "" {
		n, err := strconv.Atoi(cs)
		if err != nil || n < 0 {
			return terms, true, "Invalid countdown"
		}
		if n > 0 {
			terms.CountdownSeconds = &n
		}
	}
	return terms, true, ""
}

// loadPromoProduct resolves :id for the claim and status endpoints.
func (h *Handlers) loadPromoProduct(c *gin.Context) (*models.Product, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return nil, false
	}
	var p models.Product
	if err := config.DB.First(&p, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return nil, false
	}
	return &p, true
}

// ClaimPromo handles POST /api/products/:id/promo/claim.
func (h *Handlers) ClaimPromo(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	p, ok := h.loadPromoProduct(c)
	if !ok {
		return
	}
	st, err := services.NewPromoService(config.DB).Claim(c.Request.Context(), p, user.ID)
	switch err {
	case nil:
		utils.SuccessResponse(c, http.StatusOK, "Promotion started", st)
	case services.ErrPromoInactive:
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case services.ErrPromoOwn:
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	default:
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start promotion")
	}
}

// GetPromoStatus handles GET /api/products/:id/promo/status.
func (h *Handlers) GetPromoStatus(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	p, ok := h.loadPromoProduct(c)
	if !ok {
		return
	}
	st, err := services.NewPromoService(config.DB).Status(c.Request.Context(), p, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load promotion")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Promotion status", st)
}

// ListPromotions handles GET /api/promotions: the Promotions Arena feed.
// Visibility mirrors the marketplace (active listing, visible seller).
func (h *Handlers) ListPromotions(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 50 {
		limit = 20
	}

	q := config.DB.Model(&models.Product{}).
		Joins("JOIN users ON users.id = products.user_id AND users.deleted_at IS NULL AND users.account_status = ?", models.AccountStatusActive).
		Where("products.promo_enabled = ? AND products.status = ? AND products.type = ? AND products.promo_price > 0 AND products.promo_price < products.price",
			true, models.ProductStatusActive, models.ProductTypeProduct)
	if cat := c.Query("category"); cat != "" {
		if id, err := uuid.Parse(cat); err == nil {
			q = q.Where("products.category_id = ?", id)
		}
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load promotions")
		return
	}
	var products []models.Product
	if err := q.Order("products.created_at DESC").Limit(limit).Offset((page - 1) * limit).Find(&products).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load promotions")
		return
	}
	populateSellerVerified(products)
	utils.SuccessResponse(c, http.StatusOK, "Promotions", gin.H{
		"products":   products,
		"pagination": gin.H{"page": page, "limit": limit, "total": total},
	})
}
