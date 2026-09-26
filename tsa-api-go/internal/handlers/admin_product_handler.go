package handlers

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// admin_product_handler.go — moderation of any merchant's listing.
//
// These are deliberately separate from the /products handlers, which stay
// owner-scoped (`WHERE user_id = ?`). Widening those to admins would put
// moderation and the merchant's own edit path on the same query, so a bug in
// one becomes a bug in the other.

// requireAdmin returns the acting admin, writing the error response itself when
// the caller is not one. The routes are already adminAuth-gated; this keeps the
// handlers safe on their own so a route-registration slip cannot expose them.
func requireAdmin(c *gin.Context) (*models.User, bool) {
	admin := getUserFromContext(c)
	if !hasAdminPrivilege(c, admin) {
		utils.ErrorResponse(c, http.StatusForbidden, "Admin access required")
		return nil, false
	}
	return admin, true
}

// refreshCategoryCount is the nil-tolerant form of updateCategoryProductCount —
// a listing need not belong to a category. Every moderation action changes
// whether the listing counts toward its category, so all of them call it.
func refreshCategoryCount(categoryID *uuid.UUID) {
	if categoryID != nil {
		updateCategoryProductCount(*categoryID)
	}
}

// requireModeratableProduct loads the product named by :id, including
// soft-deleted rows, and writes the error response itself when it cannot.
func requireModeratableProduct(c *gin.Context) (*models.Product, bool) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return nil, false
	}
	var product models.Product
	if err := config.DB.Unscoped().First(&product, "id = ?", productID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return nil, false
	}
	return &product, true
}

// AdminListProducts handles GET /api/admin/products — every listing regardless
// of status, optionally including soft-deleted ones.
//
// The admin console previously read the PUBLIC /products endpoint, which
// hard-filters status = 'active', so inactive and pending_review listings were
// invisible to admins and the console's status filter did nothing.
func (h *Handlers) AdminListProducts(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	query := config.DB.Model(&models.Product{})
	if c.Query("includeDeleted") == "true" {
		// Widens the page to deleted AND live rows; MarkDeletionFlags below tells
		// them apart for the console.
		query = query.Unscoped()
	}

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if productType := c.Query("type"); productType == models.ProductTypeProduct || productType == models.ProductTypeService {
		query = query.Where("type = ?", productType)
	}
	if search := c.Query("search"); search != "" {
		like := "%" + search + "%"
		query = query.Where("(name ILIKE ? OR description ILIKE ? OR company_name ILIKE ?)", like, like, like)
	}
	if sellerID := c.Query("sellerId"); sellerID != "" {
		if uid, err := uuid.Parse(sellerID); err == nil {
			query = query.Where("user_id = ?", uid)
		}
	}

	var total int64
	query.Count(&total)

	var products []models.Product
	if err := query.Order("created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&products).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch products")
		return
	}
	populateSellerVerified(products)
	// includeDeleted widens the query to deleted AND live rows, so each row has
	// to say which it is — otherwise the console offers Restore on a live listing.
	models.MarkDeletionFlags(products)

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

// adminSeller is the seller summary attached to the admin detail view. It is a
// deliberate subset of models.User — moderation needs to identify and contact
// the merchant, not read their whole account.
type adminSeller struct {
	ID                 uuid.UUID `json:"id"`
	Name               string    `json:"name"`
	Username           string    `json:"username"`
	Email              string    `json:"email"`
	PhoneNumber        string    `json:"phoneNumber"`
	Role               string    `json:"role"`
	VerificationStatus string    `json:"verificationStatus"`
	AccountStatus      string    `json:"accountStatus"`
}

// AdminGetProduct handles GET /api/admin/products/:id — full detail, including
// soft-deleted products so an admin can review one before restoring it.
func (h *Handlers) AdminGetProduct(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}

	product, ok := requireModeratableProduct(c)
	if !ok {
		return
	}

	product.MarkDeletionFlag()

	var seller adminSeller
	config.DB.Model(&models.User{}).
		Select("id, name, username, email, phone_number, role, verification_status, account_status").
		Where("id = ?", product.UserID).
		Scan(&seller)

	var suspendedByName string
	if product.SuspendedBy != nil {
		config.DB.Model(&models.User{}).
			Select("name").Where("id = ?", *product.SuspendedBy).Scan(&suspendedByName)
	}

	// Deletion state travels on the product itself (product.isDeleted); only the
	// timestamp needs adding, since gorm.DeletedAt does not serialize usefully.
	utils.SuccessResponse(c, http.StatusOK, "Product fetched successfully", gin.H{
		"product":         product,
		"seller":          seller,
		"deletedAt":       nullableTime(product.DeletedAt),
		"suspendedByName": suspendedByName,
	})
}

func nullableTime(d gorm.DeletedAt) *time.Time {
	if !d.Valid {
		return nil
	}
	return &d.Time
}

// SuspendProduct handles POST /api/admin/products/:id/suspend.
//
// A reason is mandatory: the merchant is shown it in their listings, and
// without one a suspension is indistinguishable from a listing that broke.
func (h *Handlers) SuspendProduct(c *gin.Context) {
	admin, ok := requireAdmin(c)
	if !ok {
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "A suspension reason is required")
		return
	}

	product, ok := requireModeratableProduct(c)
	if !ok {
		return
	}
	if product.DeletedAt.Valid {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot suspend a deleted product")
		return
	}
	if product.Status == models.ProductStatusSuspended {
		utils.ErrorResponse(c, http.StatusBadRequest, "Product is already suspended")
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":                models.ProductStatusSuspended,
		"suspension_reason":     reason,
		"suspended_at":          now,
		"suspended_by":          admin.ID,
		"suspended_from_status": product.Status,
		"updated_at":            now,
	}
	if err := config.DB.Model(&models.Product{}).Where("id = ?", product.ID).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to suspend product")
		return
	}

	// A suspended listing must stop counting toward its category.
	refreshCategoryCount(product.CategoryID)

	utils.SuccessResponse(c, http.StatusOK, "Product suspended successfully", gin.H{
		"id":               product.ID,
		"status":           models.ProductStatusSuspended,
		"suspensionReason": reason,
		"suspendedAt":      now,
	})
}

// UnsuspendProduct handles POST /api/admin/products/:id/unsuspend.
//
// It restores SuspendedFromStatus rather than defaulting to active: a listing
// the merchant had paused before the suspension must come back paused.
func (h *Handlers) UnsuspendProduct(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}

	product, ok := requireModeratableProduct(c)
	if !ok {
		return
	}
	if product.Status != models.ProductStatusSuspended {
		utils.ErrorResponse(c, http.StatusBadRequest, "Product is not suspended")
		return
	}

	restored := product.SuspendedFromStatus
	if !models.MerchantSettableStatuses[restored] {
		restored = models.ProductStatusActive
	}

	updates := map[string]interface{}{
		"status":                restored,
		"suspension_reason":     "",
		"suspended_at":          nil,
		"suspended_by":          nil,
		"suspended_from_status": "",
		"updated_at":            time.Now(),
	}
	if err := config.DB.Model(&models.Product{}).Where("id = ?", product.ID).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to unsuspend product")
		return
	}

	refreshCategoryCount(product.CategoryID)

	utils.SuccessResponse(c, http.StatusOK, "Product unsuspended successfully", gin.H{
		"id":     product.ID,
		"status": restored,
	})
}

// AdminDeleteProduct handles DELETE /api/admin/products/:id.
//
// Soft delete only. The images stay on Cloudinary and the row stays in the
// table, because orders reference product_id and a restore has to bring the
// listing back intact.
func (h *Handlers) AdminDeleteProduct(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}

	product, ok := requireModeratableProduct(c)
	if !ok {
		return
	}
	if product.DeletedAt.Valid {
		utils.ErrorResponse(c, http.StatusBadRequest, "Product is already deleted")
		return
	}

	if err := config.DB.Delete(&models.Product{}, "id = ?", product.ID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete product")
		return
	}

	refreshCategoryCount(product.CategoryID)

	utils.SuccessResponse(c, http.StatusOK, "Product deleted successfully", gin.H{"id": product.ID})
}

// RestoreProduct handles POST /api/admin/products/:id/restore — undoes a soft
// delete, leaving the listing's status untouched.
func (h *Handlers) RestoreProduct(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}

	product, ok := requireModeratableProduct(c)
	if !ok {
		return
	}
	if !product.DeletedAt.Valid {
		utils.ErrorResponse(c, http.StatusBadRequest, "Product is not deleted")
		return
	}

	if err := config.DB.Unscoped().Model(&models.Product{}).
		Where("id = ?", product.ID).
		Update("deleted_at", nil).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to restore product")
		return
	}

	refreshCategoryCount(product.CategoryID)

	utils.SuccessResponse(c, http.StatusOK, "Product restored successfully", gin.H{
		"id":     product.ID,
		"status": product.Status,
	})
}
