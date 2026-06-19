package handlers

import (
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

type MerchantRequestHandler struct {
	DB           *gorm.DB
	EventBus     *events.Bus
	PMOnboarding *services.PMOnboardingService
	// PMService is used to check whether the caller already has an active PM
	// before allowing a p2p_merchant upgrade submit. Optional: if nil, the
	// active-PM guard is skipped (no ErrPMNotFound check can run).
	PMService *services.PMService
}

func NewMerchantRequestHandler(db *gorm.DB, bus *events.Bus) *MerchantRequestHandler {
	return &MerchantRequestHandler{DB: db, EventBus: bus}
}

// NewMerchantRequestHandlerWithPM constructs a MerchantRequestHandler with an
// optional PM onboarding service.  When pmOnboarding is nil, corporate PM
// provisioning is dormant-by-default (approval of p2p_merchant requests returns
// a 503 until Nomba creds are configured).
func NewMerchantRequestHandlerWithPM(db *gorm.DB, bus *events.Bus, pmOnboarding *services.PMOnboardingService) *MerchantRequestHandler {
	return &MerchantRequestHandler{DB: db, EventBus: bus, PMOnboarding: pmOnboarding}
}

type submitMerchantRequestInput struct {
	BusinessType        string `json:"businessType" binding:"required"`
	BusinessName        string `json:"businessName" binding:"required"`
	BusinessDescription string `json:"businessDescription"`
	Address             string `json:"address" binding:"required"`
	City                string `json:"city" binding:"required"`
	State               string `json:"state" binding:"required"`
	Country             string `json:"country" binding:"required"`
	Phone               string `json:"phone" binding:"required"`
	RegistrationNumber  string `json:"registrationNumber"`

	// Corporate Payment-Merchant (business_type='p2p_merchant') fields. Required
	// and validated only for p2p_merchant submissions (see SubmitMerchantRequest).
	SettlementBankCode      string  `json:"settlementBankCode"`
	SettlementAccountNumber string  `json:"settlementAccountNumber"`
	SettlementAccountName   string  `json:"settlementAccountName"`
	MakerWalletAddress      string  `json:"makerWalletAddress"`
	RequestedPerTradeCapUsd float64 `json:"requestedPerTradeCapUsd"`
	RequestedDailyCapUsd    float64 `json:"requestedDailyCapUsd"`
}

type adminNoteInput struct {
	Note string `json:"note"`
}

var validStatuses = []string{
	models.MerchantRequestStatusPending,
	models.MerchantRequestStatusApproved,
	models.MerchantRequestStatusRejected,
}

// SubmitMerchantRequest handles POST /api/merchant-requests.
// For p2p_merchant submits: existing merchants may upgrade; only an already-active
// PM is blocked. For non-PM submits: any caller already holding role=merchant is
// blocked. Both paths enforce a type-scoped duplicate-request check.
// Users with previously rejected requests may re-apply.
func (h *MerchantRequestHandler) SubmitMerchantRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	// Bind + validate business type BEFORE role checks so the guard knows
	// what type of request is being submitted.
	var input submitMerchantRequestInput
	if err := c.ShouldBindJSON(&input); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	if !slices.Contains(models.ValidBusinessTypes, input.BusinessType) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid business type")
		return
	}

	if input.BusinessType == models.BusinessTypeP2PMerchant {
		// PM upgrade path: existing merchants are allowed. Only an already-active
		// PM row blocks the submit (prevents a duplicate active PM).
		if h.PMService != nil {
			_, err := h.PMService.GetActiveByUserID(c.Request.Context(), user.ID)
			if err == nil {
				// Active PM found — block.
				utils.ErrorResponse(c, http.StatusBadRequest, "You are already a payment merchant")
				return
			}
			if !errors.Is(err, services.ErrPMNotFound) {
				log.Printf("SubmitMerchantRequest: GetActiveByUserID error: %v", err)
				utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to check payment merchant status")
				return
			}
			// ErrPMNotFound — no active PM, allow submission to continue.
		}
	} else {
		// Non-PM path: a user who is already a merchant cannot re-apply as a
		// general merchant (unchanged behaviour).
		if user.Role == models.RoleMerchant {
			utils.ErrorResponse(c, http.StatusBadRequest, "You are already a merchant")
			return
		}
	}

	// I2: corporate Payment-Merchant applications must supply a complete,
	// well-formed settlement destination and a valid non-zero maker wallet.
	// The applicant provides the already-resolved settlement account name — no
	// server-side Nomba bank lookup happens here (format/presence only).
	if input.BusinessType == models.BusinessTypeP2PMerchant {
		if input.SettlementBankCode == "" || input.SettlementAccountNumber == "" || input.SettlementAccountName == "" {
			utils.ErrorResponse(c, http.StatusBadRequest,
				"Settlement bank code, account number, and account name are required for a payment merchant")
			return
		}
		if !common.IsHexAddress(input.MakerWalletAddress) ||
			common.HexToAddress(input.MakerWalletAddress) == (common.Address{}) {
			utils.ErrorResponse(c, http.StatusBadRequest,
				"A valid non-zero maker wallet address is required for a payment merchant")
			return
		}
	}

	// Type-scoped duplicate-request check (Critical-1).
	// For p2p_merchant: only a prior pending/approved PM request blocks — a stale
	// approved general_products row from a prior merchant role must NOT block.
	// For non-PM: unchanged unscoped check (any prior pending/approved row blocks).
	var existing models.MerchantRequest
	var dupErr error
	if input.BusinessType == models.BusinessTypeP2PMerchant {
		dupErr = h.DB.Where("user_id = ? AND business_type = ? AND status IN ?", user.ID,
			models.BusinessTypeP2PMerchant,
			[]string{models.MerchantRequestStatusPending, models.MerchantRequestStatusApproved}).
			First(&existing).Error
	} else {
		dupErr = h.DB.Where("user_id = ? AND status IN ?", user.ID,
			[]string{models.MerchantRequestStatusPending, models.MerchantRequestStatusApproved}).
			First(&existing).Error
	}
	if dupErr == nil {
		if existing.Status == models.MerchantRequestStatusApproved {
			utils.ErrorResponse(c, http.StatusBadRequest, "You already have an approved merchant request")
		} else {
			utils.ErrorResponse(c, http.StatusBadRequest, "You already have a pending merchant request")
		}
		return
	}
	if !errors.Is(dupErr, gorm.ErrRecordNotFound) {
		log.Printf("SubmitMerchantRequest: failed to check existing requests: %v", dupErr)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to check existing requests")
		return
	}

	req := models.MerchantRequest{
		UserID:              user.ID,
		BusinessType:        input.BusinessType,
		BusinessName:        input.BusinessName,
		BusinessDescription: input.BusinessDescription,
		Address:             input.Address,
		City:                input.City,
		State:               input.State,
		Country:             input.Country,
		Phone:               input.Phone,
		RegistrationNumber:  input.RegistrationNumber,
		Status:              models.MerchantRequestStatusPending,

		// Corporate PM fields (consumed at approval/provisioning time).
		SettlementBankCode:      input.SettlementBankCode,
		SettlementAccountNumber: input.SettlementAccountNumber,
		SettlementAccountName:   input.SettlementAccountName,
		MakerWalletAddress:      input.MakerWalletAddress,
		RequestedPerTradeCapUSD: input.RequestedPerTradeCapUsd,
		RequestedDailyCapUSD:    input.RequestedDailyCapUsd,
	}

	if err := h.DB.Create(&req).Error; err != nil {
		log.Printf("SubmitMerchantRequest: failed to create: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create merchant request")
		return
	}

	utils.SuccessResponse(c, http.StatusCreated, "Merchant request submitted successfully", req)
}

// GetMyMerchantRequest handles GET /api/merchant-requests/my-request.
// Returns the most recent request for the caller, or 200 with nil data if none exists.
func (h *MerchantRequestHandler) GetMyMerchantRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var req models.MerchantRequest
	err := h.DB.Where("user_id = ?", user.ID).Order("created_at DESC").First(&req).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.SuccessResponse(c, http.StatusOK, "No merchant request found", nil)
			return
		}
		log.Printf("GetMyMerchantRequest: DB error: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to retrieve merchant request")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Merchant request retrieved", req)
}

// ListMerchantRequests handles GET /api/admin/merchant-requests.
// Supports optional status filter and pagination via page/limit query params.
func (h *MerchantRequestHandler) ListMerchantRequests(c *gin.Context) {
	status := c.Query("status")
	page := 1
	limit := 20

	if p := c.Query("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	query := h.DB.Model(&models.MerchantRequest{})
	if status != "" {
		if !slices.Contains(validStatuses, status) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid status filter")
			return
		}
		query = query.Where("status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		log.Printf("ListMerchantRequests: count error: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to count merchant requests")
		return
	}

	var requests []models.MerchantRequest
	if err := query.Preload("User").Preload("Reviewer").
		Order("created_at DESC").
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&requests).Error; err != nil {
		log.Printf("ListMerchantRequests: find error: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to retrieve merchant requests")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Merchant requests retrieved", gin.H{
		"requests": requests,
		"total":    total,
		"page":     page,
		"limit":    limit,
	})
}

// ApproveMerchantRequest handles POST /api/admin/merchant-requests/:id/approve.
// Atomically marks the request as approved and upgrades the user's role to merchant.
func (h *MerchantRequestHandler) ApproveMerchantRequest(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request ID")
		return
	}

	var input adminNoteInput
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&input); err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
			return
		}
	}

	var req models.MerchantRequest
	if err := h.DB.First(&req, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Merchant request not found")
		return
	}

	// Pre-flight gate for corporate PM (p2p_merchant) approvals. These checks
	// run BEFORE any state mutation so that an approval which cannot provision a
	// PM never half-commits — i.e. never leaves the request "approved" with the
	// user upgraded to merchant but no active payment_merchants row. A half-
	// commit split-brains the UI: the dashboard (GET /p2p/pm/me) finds no active
	// PM and shows "Upgrade", while the application screen (GET /merchant-
	// requests/my-request) sees an approved PM request and claims the user is
	// already a Payment Merchant.
	if req.BusinessType == models.BusinessTypeP2PMerchant {
		// I1: provisioning a corporate PM (Nomba sub-account + balance-gated
		// money path) is reserved for true admins. The route middleware
		// (adminAuth) also admits merchant/support for generic merchant
		// approvals, so we re-check the role here.
		if admin.Role != models.RoleAdmin && admin.Role != models.RoleSuperAdmin {
			utils.ErrorResponse(c, http.StatusForbidden, "P2P admin role required to approve a payment merchant")
			return
		}
		if h.PMOnboarding == nil {
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "PM provisioning unavailable: Nomba not configured")
			return
		}
	}

	// For non-PM requests: only pending → approved is valid.
	// For PM (p2p_merchant) requests: also allow a retry when the request is
	// already approved but the user has no ACTIVE payment_merchants row — this is
	// the resume path for a prior failed/skipped ProvisionAndActivate call. Two
	// shapes qualify:
	//   - a PM row exists with a non-active provisioning_status (Nomba failed
	//     partway), or
	//   - no PM row exists at all (approval happened while PM was dormant, so
	//     provisioning never ran — true of accounts approved before the pre-flight
	//     gate above existed).
	// Without this second case, a stuck "approved-but-not-provisioned" account
	// could never be activated: the 400 below would reject every re-approval.
	// The role-upgrade/status-flip work is skipped on the retry path (it was
	// already done on attempt 1).
	isPMRetry := false
	if req.Status == models.MerchantRequestStatusApproved &&
		req.BusinessType == models.BusinessTypeP2PMerchant {
		var pmRow models.PaymentMerchant
		err := h.DB.Where("user_id = ?", req.UserID).First(&pmRow).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			// No PM row at all — resume by provisioning from scratch.
			isPMRetry = true
		case err == nil && pmRow.ProvisioningStatus != models.PMProvisioningActive:
			// Partially-provisioned row — resume where it left off.
			isPMRetry = true
		}
	}

	if !isPMRetry && req.Status != models.MerchantRequestStatusPending {
		utils.ErrorResponse(c, http.StatusBadRequest, "Request is not in pending status")
		return
	}

	now := time.Now()

	if !isPMRetry {
		// First approval: flip status → approved and upgrade the user's role.
		txErr := h.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&req).Updates(map[string]interface{}{
				"status":      models.MerchantRequestStatusApproved,
				"reviewed_by": admin.ID,
				"reviewed_at": now,
				"admin_note":  input.Note,
			}).Error; err != nil {
				return err
			}

			if err := tx.Model(&models.User{}).Where("id = ?", req.UserID).
				Update("role", models.RoleMerchant).Error; err != nil {
				return err
			}

			return nil
		})

		if txErr != nil {
			log.Printf("ApproveMerchantRequest: transaction error: %v", txErr)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to approve merchant request")
			return
		}
	}

	if err := h.DB.Preload("User").First(&req, "id = ?", id).Error; err != nil {
		log.Printf("ApproveMerchantRequest: reload error: %v", err)
	}

	// Corporate PM provisioning: if this is a p2p_merchant request, spin up the
	// Nomba sub-account + virtual account inline — the request stays re-approvable
	// for retry (via isPMRetry above) if provisioning fails here. Admin-role and
	// PMOnboarding-availability are already enforced in the pre-flight gate above.
	if req.BusinessType == models.BusinessTypeP2PMerchant {
		if _, err := h.PMOnboarding.ProvisionAndActivate(c.Request.Context(), &req); err != nil {
			log.Printf("ApproveMerchantRequest: PM provisioning failed for request %s: %v", req.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Merchant approved but PM provisioning failed — retry the approval to resume")
			return
		}
	}

	if h.EventBus != nil {
		h.EventBus.Publish(events.Event{
			Type:    events.MerchantApproved,
			UserID:  req.UserID,
			Title:   "Merchant Request Approved",
			Message: "Your merchant application has been approved. You can now list products.",
			Data: map[string]interface{}{
				"requestId":    req.ID,
				"businessName": req.BusinessName,
			},
		})
	}

	utils.SuccessResponse(c, http.StatusOK, "Merchant request approved", req)
}

// RejectMerchantRequest handles POST /api/admin/merchant-requests/:id/reject.
// A note explaining the rejection reason is required.
func (h *MerchantRequestHandler) RejectMerchantRequest(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request ID")
		return
	}

	var input adminNoteInput
	if err := c.ShouldBindJSON(&input); err != nil || input.Note == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Note is required for rejection")
		return
	}

	var req models.MerchantRequest
	if err := h.DB.First(&req, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Merchant request not found")
		return
	}

	if req.Status != models.MerchantRequestStatusPending {
		utils.ErrorResponse(c, http.StatusBadRequest, "Request is not in pending status")
		return
	}

	now := time.Now()
	if err := h.DB.Model(&req).Updates(map[string]interface{}{
		"status":      models.MerchantRequestStatusRejected,
		"reviewed_by": admin.ID,
		"reviewed_at": now,
		"admin_note":  input.Note,
	}).Error; err != nil {
		log.Printf("RejectMerchantRequest: update error: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reject merchant request")
		return
	}

	if err := h.DB.Preload("User").First(&req, "id = ?", id).Error; err != nil {
		log.Printf("RejectMerchantRequest: reload error: %v", err)
	}

	if h.EventBus != nil {
		h.EventBus.Publish(events.Event{
			Type:    events.MerchantRejected,
			UserID:  req.UserID,
			Title:   "Merchant Request Rejected",
			Message: "Your merchant application has been rejected: " + input.Note,
			Data: map[string]interface{}{
				"requestId":    req.ID,
				"businessName": req.BusinessName,
				"reason":       input.Note,
			},
		})
	}

	utils.SuccessResponse(c, http.StatusOK, "Merchant request rejected", req)
}
