package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// maxDeliveryAddresses bounds a user's address book so the list stays a
// picker, not a dump.
const maxDeliveryAddresses = 20

type deliveryAddressRequest struct {
	Label         string `json:"label"`
	RecipientName string `json:"recipientName"`
	Phone         string `json:"phone"`
	Street        string `json:"street"`
	City          string `json:"city"`
	State         string `json:"state"`
	Country       string `json:"country"`
	IsDefault     bool   `json:"isDefault"`
}

// normalize trims every field and returns a message naming the first problem.
func (r *deliveryAddressRequest) normalize() string {
	fields := []struct {
		val      *string
		name     string
		max      int
		required bool
	}{
		{&r.Label, "label", 40, false},
		{&r.RecipientName, "recipient name", 120, true},
		{&r.Phone, "phone", 32, true},
		{&r.Street, "street address", 255, true},
		{&r.City, "city", 100, true},
		{&r.State, "state", 100, true},
		{&r.Country, "country", 100, true},
	}
	for _, f := range fields {
		*f.val = strings.TrimSpace(*f.val)
		if f.required && *f.val == "" {
			return "Missing " + f.name
		}
		if len(*f.val) > f.max {
			return "The " + f.name + " is too long"
		}
	}
	return ""
}

func (r *deliveryAddressRequest) applyTo(a *models.DeliveryAddress) {
	a.Label = r.Label
	a.RecipientName = r.RecipientName
	a.Phone = r.Phone
	a.Street = r.Street
	a.City = r.City
	a.State = r.State
	a.Country = r.Country
}

func bindDeliveryAddress(c *gin.Context) (*deliveryAddressRequest, bool) {
	var req deliveryAddressRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return nil, false
	}
	if msg := req.normalize(); msg != "" {
		utils.ErrorResponse(c, http.StatusBadRequest, msg)
		return nil, false
	}
	return &req, true
}

// clearDefault unsets the user's current default. It must run before another
// address is marked default: the one-default index rejects a second one.
func clearDefault(tx *gorm.DB, userID uuid.UUID) error {
	return tx.Model(&models.DeliveryAddress{}).
		Where("user_id = ? AND is_default = ?", userID, true).
		Update("is_default", false).Error
}

// findUserDeliveryAddress loads an address only if userID owns it.
func findUserDeliveryAddress(db *gorm.DB, userID, id uuid.UUID) (*models.DeliveryAddress, error) {
	var a models.DeliveryAddress
	if err := db.Where("id = ? AND user_id = ?", id, userID).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// ListDeliveryAddresses handles GET /api/delivery-addresses — default first,
// then newest.
func (h *Handlers) ListDeliveryAddresses(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var rows []models.DeliveryAddress
	if err := config.DB.Where("user_id = ?", user.ID).
		Order("is_default DESC, updated_at DESC").Find(&rows).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load addresses")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Addresses retrieved", rows)
}

// CreateDeliveryAddress handles POST /api/delivery-addresses. A user's first
// address is always their default.
func (h *Handlers) CreateDeliveryAddress(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	req, ok := bindDeliveryAddress(c)
	if !ok {
		return
	}

	addr := models.DeliveryAddress{ID: uuid.New(), UserID: user.ID}
	req.applyTo(&addr)
	var capped bool
	err := config.DB.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&models.DeliveryAddress{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
			return err
		}
		if count >= maxDeliveryAddresses {
			capped = true
			return nil
		}
		addr.IsDefault = req.IsDefault || count == 0
		if addr.IsDefault {
			if err := clearDefault(tx, user.ID); err != nil {
				return err
			}
		}
		return tx.Create(&addr).Error
	})
	if capped {
		utils.ErrorResponse(c, http.StatusBadRequest, "You can save up to 20 addresses. Delete one to add another.")
		return
	}
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to save address")
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Address saved", addr)
}

// UpdateDeliveryAddress handles PUT /api/delivery-addresses/:id. Setting
// isDefault=false never un-defaults an address — pick another one instead —
// so a user with addresses always has a default.
func (h *Handlers) UpdateDeliveryAddress(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	req, ok := bindDeliveryAddress(c)
	if !ok {
		return
	}

	var addr *models.DeliveryAddress
	err := config.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		if addr, err = findUserDeliveryAddress(tx, user.ID, id); err != nil {
			return err
		}
		if req.IsDefault && !addr.IsDefault {
			if err := clearDefault(tx, user.ID); err != nil {
				return err
			}
			addr.IsDefault = true
		}
		req.applyTo(addr)
		return tx.Save(addr).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		utils.ErrorResponse(c, http.StatusNotFound, "Address not found")
		return
	}
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update address")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Address updated", addr)
}

// DeleteDeliveryAddress handles DELETE /api/delivery-addresses/:id. Orders
// keep their own snapshot, so a hard delete is safe. Deleting the default
// promotes the most recently used remaining address.
func (h *Handlers) DeleteDeliveryAddress(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	err := config.DB.Transaction(func(tx *gorm.DB) error {
		addr, err := findUserDeliveryAddress(tx, user.ID, id)
		if err != nil {
			return err
		}
		if err := tx.Delete(addr).Error; err != nil {
			return err
		}
		if !addr.IsDefault {
			return nil
		}
		var next models.DeliveryAddress
		err = tx.Where("user_id = ?", user.ID).Order("updated_at DESC").First(&next).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return tx.Model(&next).Update("is_default", true).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		utils.ErrorResponse(c, http.StatusNotFound, "Address not found")
		return
	}
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete address")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Address deleted", nil)
}
