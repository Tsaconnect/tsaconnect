package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// SellerPayoutAdminHandler is the super-admin view of marketplace seller
// payouts: the queue, one payout in full, the actions that get a parked one
// moving (retry, mark paid, cancel, pay the wallet) and the on/off switch.
type SellerPayoutAdminHandler struct {
	Admin  *services.SellerPayoutAdmin
	Switch *services.SellerPayoutSwitch
}

type payoutActionBody struct {
	Note      string `json:"note"`
	Force     bool   `json:"force"`
	Reference string `json:"reference"`
}

// List is GET /api/admin/seller-payouts?status=needs_review,paying_wallet&limit=.
func (h *SellerPayoutAdminHandler) List(c *gin.Context) {
	var statuses []string
	for _, s := range strings.Split(c.Query("status"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			statuses = append(statuses, s)
		}
	}
	limit := 0
	if v := c.Query("limit"); v != "" {
		for _, ch := range v {
			if ch < '0' || ch > '9' {
				limit = 0
				break
			}
			limit = limit*10 + int(ch-'0')
		}
	}
	rows, err := h.Admin.List(c.Request.Context(), statuses, limit)
	if err != nil {
		log.Printf("[seller-payout-admin] list: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not load payouts")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Seller payouts", gin.H{"payouts": rows})
}

// Get is GET /api/admin/seller-payouts/:id.
func (h *SellerPayoutAdminHandler) Get(c *gin.Context) {
	id, ok := payoutID(c)
	if !ok {
		return
	}
	d, err := h.Admin.Detail(c.Request.Context(), id)
	if err != nil {
		payoutAdminFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Seller payout", d)
}

func (h *SellerPayoutAdminHandler) act(c *gin.Context, do func(id, admin uuid.UUID, b payoutActionBody) error, done string) {
	id, ok := payoutID(c)
	if !ok {
		return
	}
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var b payoutActionBody
	if err := c.ShouldBindJSON(&b); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "A note is required")
		return
	}
	if err := do(id, admin.ID, b); err != nil {
		payoutAdminFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, done, gin.H{"payoutId": id})
}

// Retry is POST /:id/retry {note, force}.
func (h *SellerPayoutAdminHandler) Retry(c *gin.Context) {
	h.act(c, func(id, admin uuid.UUID, b payoutActionBody) error {
		return h.Admin.Retry(c.Request.Context(), id, admin, b.Note, b.Force)
	}, "Payout set to run again")
}

// MarkPaid is POST /:id/mark-paid {note, reference}.
func (h *SellerPayoutAdminHandler) MarkPaid(c *gin.Context) {
	h.act(c, func(id, admin uuid.UUID, b payoutActionBody) error {
		return h.Admin.MarkPaid(c.Request.Context(), id, admin, b.Note, b.Reference)
	}, "Payout marked paid")
}

// Cancel is POST /:id/cancel {note}.
func (h *SellerPayoutAdminHandler) Cancel(c *gin.Context) {
	h.act(c, func(id, admin uuid.UUID, b payoutActionBody) error {
		return h.Admin.Cancel(c.Request.Context(), id, admin, b.Note)
	}, "Payout cancelled")
}

// PayWallet is POST /:id/pay-wallet {note}.
func (h *SellerPayoutAdminHandler) PayWallet(c *gin.Context) {
	h.act(c, func(id, admin uuid.UUID, b payoutActionBody) error {
		return h.Admin.PayWallet(c.Request.Context(), id, admin, b.Note)
	}, "Paying the seller's wallet")
}

// GetSwitch is GET /api/admin/seller-payouts/switch.
func (h *SellerPayoutAdminHandler) GetSwitch(c *gin.Context) {
	on, err := h.Switch.Enabled(c.Request.Context())
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not read the switch")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Seller payouts switch", gin.H{"enabled": on})
}

// SetSwitch is PUT /api/admin/seller-payouts/switch {enabled, note}.
func (h *SellerPayoutAdminHandler) SetSwitch(c *gin.Context) {
	admin := getUserFromContext(c)
	var b struct {
		Enabled *bool  `json:"enabled"`
		Note    string `json:"note"`
	}
	if admin == nil || c.ShouldBindJSON(&b) != nil || b.Enabled == nil || strings.TrimSpace(b.Note) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "enabled and a note are required")
		return
	}
	if err := h.Switch.Set(c.Request.Context(), *b.Enabled, admin.ID, b.Note); err != nil {
		log.Printf("[seller-payout-admin] set switch: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not change the switch")
		return
	}
	log.Printf("[seller-payout-admin] AUDIT switch enabled=%v by=%s note=%q", *b.Enabled, admin.ID, b.Note)
	utils.SuccessResponse(c, http.StatusOK, "Seller payouts switch", gin.H{"enabled": *b.Enabled})
}

func payoutID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid payout id")
		return uuid.Nil, false
	}
	return id, true
}

func payoutAdminFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPayoutNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrPayoutNeedsNote):
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrPayoutRetryUnsafe):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "retry_unsafe")
	case errors.Is(err, services.ErrPayoutNotParked):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "not_parked")
	case errors.Is(err, services.ErrWalletPaymentNotSafe):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "money_may_have_moved")
	case errors.Is(err, services.ErrWalletPaymentUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "nothing_to_pay")
	default:
		log.Printf("[seller-payout-admin] %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "That did not work — try again")
	}
}
