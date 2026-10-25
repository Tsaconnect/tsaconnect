package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// payMerchantProvisioner is the seam ApproveMerchantRequest uses to provision
// a TSA Pay merchant from an approved payment_acceptance request. Satisfied
// by *services.PayMerchantService. Optional: if nil, approving a
// payment_acceptance request returns 503 (TSA Pay dormant-by-default).
type payMerchantProvisioner interface {
	CreateFromRequest(ctx context.Context, req *models.MerchantRequest) (*models.PayMerchant, error)
	// NormalizeBusinessName is the business-name rule (hidden characters,
	// length, reserved words): an application is held to it when submitted
	// and again when approved.
	NormalizeBusinessName(name string) (string, error)
}

type MerchantRequestHandler struct {
	DB           *gorm.DB
	EventBus     *events.Bus
	PMOnboarding *services.PMOnboardingService
	// PMService is used to check whether the caller already has an active PM
	// before allowing a p2p_merchant upgrade submit. Optional: if nil, the
	// active-PM guard is skipped (no ErrPMNotFound check can run).
	PMService *services.PMService
	// PayMerchants provisions a TSA Pay merchant from an approved
	// payment_acceptance request. Optional: if nil, submitting or approving
	// that type of request returns 503. Assign only a non-nil service — a
	// typed-nil pointer here would read as wired.
	PayMerchants payMerchantProvisioner
	// WebAppOrigin is where an approved TSA Pay merchant sets up (cfg.WebAppOrigin);
	// empty names the web app without a link.
	WebAppOrigin string
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
	// Website (business_type='payment_acceptance'): optional — a business with
	// no site takes payments through payment links — but when given, validated
	// as a well-formed https URL in SubmitMerchantRequest.
	Website string `json:"website"`

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

// approveMerchantInput is an approval's body. BusinessName (TSA Pay only, super
// admin only) renames the business as it is approved: the way on for a request
// whose name the business-name rule refuses — including one already approved,
// whose provisioning failed, which can no longer be rejected.
type approveMerchantInput struct {
	Note         string `json:"note"`
	BusinessName string `json:"businessName"`
}

// businessNameRefusal is a refused business name's reason, written for the
// person who typed it, or "" when err is not one.
func businessNameRefusal(err error) string {
	var bn *services.BusinessNameError
	if !errors.As(err, &bn) {
		return ""
	}
	reason := []rune(bn.Reason)
	if len(reason) > 0 {
		reason[0] = unicode.ToUpper(reason[0])
	}
	return string(reason)
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
		// merchant row blocks the submit (prevents a duplicate active PM); an
		// LP row is the other role and never blocks (owner, 2026-10-13).
		if h.PMService != nil {
			_, err := h.PMService.GetActiveByUserIDKind(c.Request.Context(), user.ID, models.PaymentMerchantKindMerchant)
			if err == nil {
				// Active merchant row found — block.
				utils.ErrorResponse(c, http.StatusBadRequest, "You are already a payment merchant")
				return
			}
			if !errors.Is(err, services.ErrPMNotFound) {
				log.Printf("SubmitMerchantRequest: GetActiveByUserIDKind(merchant) error: %v", err)
				utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to check payment merchant status")
				return
			}
			// ErrPMNotFound — no active merchant row, allow submission to continue.
		}
	} else if input.BusinessType != models.BusinessTypePaymentAcceptance {
		// Non-PM, non-TSA-Pay path: a user who is already a merchant cannot
		// re-apply as a general merchant (unchanged behaviour).
		if user.Role == models.RoleMerchant {
			utils.ErrorResponse(c, http.StatusBadRequest, "You are already a merchant")
			return
		}
	}
	// Before the one-off drops the one-role index, an application for a second
	// role could never be approved; refuse it now rather than leave it pending.
	if kind, ok := models.PaymentMerchantKindFor(input.BusinessType); ok && h.PMOnboarding != nil {
		switch err := h.PMOnboarding.RefuseSecondRoleWhileDisabled(c.Request.Context(), user.ID, kind); {
		case errors.Is(err, services.ErrSecondRoleNotEnabled):
			utils.ErrorResponseWithCode(c, http.StatusConflict,
				"You can't hold both roles yet. Try again later.", "second_role_not_enabled")
			return
		case err != nil:
			log.Printf("SubmitMerchantRequest: second-role check failed for %s: %v", user.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to check your profile")
			return
		}
	}
	// payment_acceptance: no role guard — a legacy applicant or an existing
	// merchant may still submit one. Approved merchants get TSA Pay without it
	// (PayMerchantService.GetOrProvisionByOwner).

	// A TSA Pay application would sit unprovisionable while TSA Pay is
	// dormant, so refuse it up front rather than take the applicant's details.
	if input.BusinessType == models.BusinessTypePaymentAcceptance && h.PayMerchants == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "TSA Pay isn't available yet")
		return
	}

	// A TSA Pay merchant takes other people's money, so only a verified identity
	// may apply. Checked before anything else about the application. The refusal
	// carries the same code as every money-in KYC refusal, so the app's one
	// kyc_verification_required handler sends her to verification.
	if input.BusinessType == models.BusinessTypePaymentAcceptance {
		verified, err := services.IsKYCVerified(c.Request.Context(), h.DB, user.ID)
		if err != nil {
			log.Printf("SubmitMerchantRequest: KYC check failed for %s: %v", user.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to check your verification")
			return
		}
		if !verified {
			utils.ErrorResponseWithCode(c, http.StatusForbidden, "Verify your identity before applying for TSA Pay", services.KYCVerificationRequiredCode)
			return
		}
	}

	// A TSA Pay business name is what customers are shown and what payout
	// accounts are matched against: the same rule as every later rename.
	if input.BusinessType == models.BusinessTypePaymentAcceptance {
		name, err := h.PayMerchants.NormalizeBusinessName(input.BusinessName)
		if reason := businessNameRefusal(err); reason != "" {
			utils.ErrorResponseWithCode(c, http.StatusBadRequest, reason, services.BusinessNameInvalidCode)
			return
		} else if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid business name")
			return
		}
		input.BusinessName = name
	}

	// A TSA Pay application's website is optional: a business with none sends
	// payment links instead. One that is given must be a well-formed https
	// address — it becomes the merchant's checkout return domain.
	if raw := strings.TrimSpace(input.Website); input.BusinessType == models.BusinessTypePaymentAcceptance && raw != "" {
		site, err := url.Parse(raw)
		if err != nil || len(raw) > 255 || site.Scheme != "https" || site.User != nil ||
			!services.ValidPayDomain(strings.ToLower(site.Hostname())) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Enter your website's https address")
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

	// Type-scoped duplicate-request check (Critical-1). Every business type gets
	// its own one-pending-or-approved-at-a-time rule: a pending/approved
	// payment_acceptance request must not block a general_products/
	// digital_products/service_provider submit (or vice versa), same as a stale
	// approved general_products row must not block a p2p_merchant upgrade. Scope
	// by business_type unconditionally rather than special-casing p2p_merchant/
	// payment_acceptance — a cross-type block is a bug for any pair of types.
	var existing models.MerchantRequest
	dupErr := h.DB.Where("user_id = ? AND business_type = ? AND status IN ?", user.ID,
		input.BusinessType,
		[]string{models.MerchantRequestStatusPending, models.MerchantRequestStatusApproved}).
		First(&existing).Error
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
		// Trimmed, not the raw input: CreateFromRequest re-parses this value at
		// approval time and a whitespace-padded URL must not become unparsable
		// (or worse, silently different) between submit and approval.
		Website: strings.TrimSpace(input.Website),
		Status:  models.MerchantRequestStatusPending,

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

// isBusinessTypeFilter: a type a read may filter on — every type a request
// can be submitted as, plus the retired liquidity_provider, whose old rows
// still exist and must stay readable. Submitting one stays refused.
func isBusinessTypeFilter(t string) bool {
	return slices.Contains(models.ValidBusinessTypes, t) || t == models.BusinessTypeLiquidityProvider
}

// GetMyMerchantRequest handles GET /api/merchant-requests/my-request[?type=<businessType>].
// Returns the caller's most recent request (of that type, when given), or 200
// with nil data if none exists. An unknown type is 400.
func (h *MerchantRequestHandler) GetMyMerchantRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	q := h.DB.Where("user_id = ?", user.ID)
	if t := c.Query("type"); t != "" {
		if !isBusinessTypeFilter(t) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid business type")
			return
		}
		q = q.Where("business_type = ?", t)
	}
	var req models.MerchantRequest
	err := q.Order("created_at DESC").First(&req).Error
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
	caller := getUserFromContext(c)
	if caller == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if !hasStaffPrivilege(c, caller) {
		utils.ErrorResponse(c, http.StatusForbidden, "Staff role required")
		return
	}
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
	if bt := c.Query("businessType"); bt != "" {
		if !isBusinessTypeFilter(bt) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid business type filter")
			return
		}
		query = query.Where("business_type = ?", bt)
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

	for i := range requests {
		redactMerchantRequest(c, caller, &requests[i])
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

	if !hasStaffPrivilege(c, admin) {
		utils.ErrorResponse(c, http.StatusForbidden, "Staff role required")
		return
	}

	var input approveMerchantInput
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
	renameTo := strings.TrimSpace(input.BusinessName)
	if renameTo != "" && req.BusinessType != models.BusinessTypePaymentAcceptance {
		utils.ErrorResponse(c, http.StatusBadRequest, "Only a TSA Pay request's business name can be changed as it is approved")
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
		if !hasAdminPrivilege(c, admin) {
			utils.ErrorResponse(c, http.StatusForbidden, "P2P admin role required to approve a payment merchant")
			return
		}
		if h.PMOnboarding == nil {
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "PM provisioning unavailable: Nomba not configured")
			return
		}
	}

	// A slice-pool LP application left pending: the shared pool activates LPs
	// itself (spec 2026-10-14 §5), so it is never approved here.
	if req.BusinessType == models.BusinessTypeLiquidityProvider {
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Liquidity providers now activate from the Provide liquidity screen; reject this request", "use_liquidity_activate")
		return
	}

	// Pre-flight gate for TSA Pay (payment_acceptance) approvals — same shape
	// as the PM gate above: admin-only, and the provisioner must be wired up,
	// checked before any state mutation.
	if req.BusinessType == models.BusinessTypePaymentAcceptance {
		if !hasAdminPrivilege(c, admin) {
			utils.ErrorResponse(c, http.StatusForbidden, "Admin role required to approve a TSA Pay merchant")
			return
		}
		if h.PayMerchants == nil {
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "TSA Pay is not enabled")
			return
		}
		// Approval makes the merchant live at once, so the applicant must still
		// be verified now, not only when they applied: a verification that lapsed
		// in between must not start taking real money.
		verified, err := services.IsKYCVerified(c.Request.Context(), h.DB, req.UserID)
		if err != nil {
			log.Printf("ApproveMerchantRequest: KYC check failed for request %s: %v", req.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to check the applicant's identity")
			return
		}
		if !verified {
			utils.ErrorResponseWithCode(c, http.StatusConflict,
				"The applicant's identity is no longer verified. They need to verify again before TSA Pay can be approved.", "applicant_not_verified")
			return
		}
		// Requests filed before business names were held to the rule are
		// refused here rather than approved into a merchant that can't exist.
		// A super admin may rename the business as they approve (renaming a
		// merchant is theirs everywhere else too).
		name := req.BusinessName
		if renameTo != "" {
			if !hasSuperAdminPrivilege(c, admin) {
				utils.ErrorResponse(c, http.StatusForbidden, "Only a super admin can rename a business")
				return
			}
			name = renameTo
		}
		normalized, err := h.PayMerchants.NormalizeBusinessName(name)
		if reason := businessNameRefusal(err); reason != "" {
			next := " Reject the request so the applicant can apply again, or have a super admin approve it with a corrected business name."
			if renameTo != "" {
				next = ""
			} else if req.Status == models.MerchantRequestStatusApproved {
				// Approved already, its setup unfinished: it can no longer be
				// rejected, so renaming is the only way on.
				next = " The request is already approved, so a super admin needs to approve it again with a corrected business name to finish setting up TSA Pay."
			}
			utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity,
				"This business name can't be used ("+strings.TrimSuffix(reason, ".")+")."+next, services.BusinessNameInvalidCode)
			return
		} else if err != nil {
			log.Printf("ApproveMerchantRequest: business name check failed for request %s: %v", req.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to check the business name")
			return
		}
		renameTo = ""
		if normalized != req.BusinessName {
			renameTo = normalized
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
		isPMRequestType(req.BusinessType) {
		kind, _ := models.PaymentMerchantKindFor(req.BusinessType)
		pmRow, err := h.pmRowFor(c.Request.Context(), req.UserID, kind)
		switch {
		case err != nil:
			log.Printf("ApproveMerchantRequest: payment_merchants lookup error: %v", err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to check payment merchant status")
			return
		case pmRow == nil:
			// No row of the request's kind — resume by provisioning from scratch.
			isPMRetry = true
		case pmRow.ProvisioningStatus != models.PMProvisioningActive &&
			pmRow.Status != models.PaymentMerchantStatusActive && pmRow.Status != models.PaymentMerchantStatusPaused:
			// Partially-provisioned row — resume where it left off. A live
			// (active or paused) row is never a retry, whatever its
			// provisioning_status: re-running the approval would overwrite an
			// LP's bank-verified settlement name (S34).
			isPMRetry = true
		}
	}

	// TSA Pay retry: an already-approved payment_acceptance request either
	// already has a provisioned pay_merchants row (re-approving it is a no-op
	// the caller shouldn't repeat — 409) or provisioning failed/never ran
	// after the first approval and this call resumes it.
	isPayRetry := false
	if req.Status == models.MerchantRequestStatusApproved &&
		req.BusinessType == models.BusinessTypePaymentAcceptance {
		var payRow models.PayMerchant
		err := h.DB.Where("merchant_request_id = ?", req.ID).First(&payRow).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			isPayRetry = true
		case err == nil:
			utils.ErrorResponse(c, http.StatusConflict, "This request has already been approved")
			return
		default:
			log.Printf("ApproveMerchantRequest: pay_merchants lookup error: %v", err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to check TSA Pay merchant status")
			return
		}
	}

	if !isPMRetry && !isPayRetry && req.Status != models.MerchantRequestStatusPending {
		utils.ErrorResponse(c, http.StatusBadRequest, "Request is not in pending status")
		return
	}

	now := time.Now()
	ctx := c.Request.Context()

	// One transaction settles everything a PM-kind approval writes (review I2):
	// the row of the request's own kind, then the request's status and the
	// role, under the user-row lock (ApproveFromRequestTx); the merchant role
	// is granted only by a p2p_merchant approval and never removed by an LP
	// one. A refused approval rolls all of it back, never half-applied.
	if isPMRequestType(req.BusinessType) || (!isPMRetry && !isPayRetry) {
		var pmErr error
		lostRace := false
		txErr := h.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if isPMRequestType(req.BusinessType) {
				if _, pmErr = h.PMOnboarding.ApproveFromRequestTx(ctx, tx, &req); pmErr != nil {
					return pmErr
				}
			}
			if isPMRetry || isPayRetry {
				return nil // the status flip and role were done on attempt 1
			}
			// First approval: flip status → approved, then grant the merchant role.
			// Only a still-pending request moves (review M9): a concurrent
			// approval that committed first keeps its reviewer and note, and
			// this one rolls back and sends no second notification.
			upd := tx.Model(&req).Where("status = ?", models.MerchantRequestStatusPending).Updates(map[string]interface{}{
				"status":      models.MerchantRequestStatusApproved,
				"reviewed_by": admin.ID,
				"reviewed_at": now,
				"admin_note":  input.Note,
			})
			if upd.Error != nil {
				return upd.Error
			}
			if upd.RowsAffected == 0 {
				lostRace = true
				return errApprovalRaceLost
			}

			// One merchant role for everything a merchant does: marketplace, TSA
			// Pay and payment-merchant approvals all grant it (owner 2026-10-19).
			// Only the retired liquidity-provider application does not: LPs are
			// not console merchants.
			if req.BusinessType != models.BusinessTypeLiquidityProvider {
				if err := tx.Model(&models.User{}).Where("id = ?", req.UserID).
					Update("role", models.RoleMerchant).Error; err != nil {
					return err
				}
			}

			return nil
		})

		switch {
		case pmErr != nil && errors.Is(pmErr, services.ErrSecondRoleNotEnabled):
			utils.ErrorResponseWithCode(c, http.StatusConflict,
				"This applicant already has the other role, and holding both isn't switched on yet. Nothing was approved; try again after the database update.",
				"second_role_not_enabled")
			return
		case pmErr != nil && errors.Is(pmErr, services.ErrInvalidPMTransition):
			utils.ErrorResponse(c, http.StatusConflict, "This liquidity provider is already live")
			return
		case pmErr != nil:
			log.Printf("ApproveMerchantRequest: PM approval failed for request %s: %v", req.ID, pmErr)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Payment merchant setup failed and nothing was approved — try again")
			return
		case lostRace:
			if err := h.DB.Preload("User").First(&req, "id = ?", id).Error; err != nil {
				log.Printf("ApproveMerchantRequest: reload error: %v", err)
				utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to approve merchant request")
				return
			}
			// Another admin decided it first; only an approval is this one's outcome.
			if req.Status != models.MerchantRequestStatusApproved {
				utils.ErrorResponse(c, http.StatusConflict, "Request was "+req.Status+" by another admin")
				return
			}
			redactMerchantRequest(c, admin, &req)
			utils.SuccessResponse(c, http.StatusOK, "Merchant request approved", req)
			return
		case txErr != nil:
			log.Printf("ApproveMerchantRequest: transaction error: %v", txErr)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to approve merchant request")
			return
		}
	}

	// A TSA Pay request approved before the one-role change never got the role,
	// and its retry skips the transaction above. The grant is idempotent.
	if isPayRetry {
		if err := h.DB.Model(&models.User{}).Where("id = ?", req.UserID).
			Update("role", models.RoleMerchant).Error; err != nil {
			log.Printf("ApproveMerchantRequest: merchant role grant on pay retry failed for %s: %v", req.UserID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to grant the merchant role — retry the approval to resume")
			return
		}
	}

	if renameTo != "" {
		// Recorded on the request, which the merchant is provisioned from.
		if err := h.DB.Model(&models.MerchantRequest{}).Where("id = ?", req.ID).Update("business_name", renameTo).Error; err != nil {
			log.Printf("ApproveMerchantRequest: rename of request %s failed: %v", req.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Merchant approved but the business name couldn't be changed — approve again to finish")
			return
		}
		log.Printf("[merchant-request] AUDIT request=%s admin=%s action=rename_on_approval from=%q to=%q", req.ID, admin.ID, req.BusinessName, renameTo)
	}

	if err := h.DB.Preload("User").First(&req, "id = ?", id).Error; err != nil {
		log.Printf("ApproveMerchantRequest: reload error: %v", err)
	}

	// TSA Pay provisioning: create the pay_merchants row from the approved
	// request. The request stays re-approvable for retry (via isPayRetry
	// above) if this fails. Admin-role and PayMerchants-availability are
	// already enforced in the pre-flight gate above.
	if req.BusinessType == models.BusinessTypePaymentAcceptance {
		if _, err := h.PayMerchants.CreateFromRequest(c.Request.Context(), &req); err != nil {
			if errors.Is(err, services.ErrPayBusinessNameTaken) {
				utils.ErrorResponseWithCode(c, http.StatusConflict,
					"Merchant approved, but another merchant already shows that business name. Have a super admin approve it again with a different name.", services.BusinessNameTakenCode)
				return
			}
			log.Printf("ApproveMerchantRequest: TSA Pay provisioning failed for request %s: %v", req.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Merchant approved but TSA Pay setup failed — retry the approval to resume")
			return
		}
	}

	if h.EventBus != nil {
		title := "Merchant Request Approved"
		h.EventBus.Publish(events.Event{
			Type:    events.MerchantApproved,
			UserID:  req.UserID,
			Title:   title,
			Message: approvalMessage(req.BusinessType, h.WebAppOrigin),
			Data: map[string]interface{}{
				"requestId":    req.ID,
				"businessName": req.BusinessName,
			},
		})
	}

	redactMerchantRequest(c, admin, &req)
	utils.SuccessResponse(c, http.StatusOK, "Merchant request approved", req)
}

// errApprovalRaceLost rolls back an approval whose request another approval
// already moved out of pending.
var errApprovalRaceLost = errors.New("merchant request is no longer pending")

// approvalMessage is the notification text for an approved request. TSA Pay
// applicants sell nothing on the marketplace, so they must not be told they
// can list products; they set up on the web app, at webAppOrigin when known.
func approvalMessage(businessType, webAppOrigin string) string {
	if businessType == models.BusinessTypePaymentAcceptance {
		where := "the TSA Connect web app"
		if webAppOrigin != "" {
			where = webAppOrigin
		}
		return "Your TSA Pay application is approved. Sign in at " + where +
			" with your TSA Connect account to create API keys, set your webhook and payouts, and start taking payments."
	}
	return "Your merchant application has been approved. You can now list products."
}

// RejectMerchantRequest handles POST /api/admin/merchant-requests/:id/reject.
// A note explaining the rejection reason is required.
func (h *MerchantRequestHandler) RejectMerchantRequest(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	// Rejecting is gated like approving (review I3), and before the lookup so a
	// non-staff caller cannot tell an unknown id from a forbidden one (N4).
	// AdminAuth alone admits support and every marketplace merchant.
	if !hasStaffPrivilege(c, admin) {
		utils.ErrorResponse(c, http.StatusForbidden, "Staff role required")
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

	// A payment merchant, LP or TSA Pay application is P2P-admin work either
	// way (S35).
	if needsAdminToDecide(req.BusinessType) && !hasAdminPrivilege(c, admin) {
		utils.ErrorResponse(c, http.StatusForbidden, "P2P admin role required to reject this application")
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

	redactMerchantRequest(c, admin, &req)
	utils.SuccessResponse(c, http.StatusOK, "Merchant request rejected", req)
}

// needsAdminToDecide: approving or rejecting this application type is P2P
// admin work, never support's (payment merchants, LPs, TSA Pay).
func needsAdminToDecide(businessType string) bool {
	return isPMRequestType(businessType) || businessType == models.BusinessTypePaymentAcceptance
}

// redactMerchantRequest strips what only a P2P admin may see (review I3):
// settlement bank details, the maker wallet (also on the preloaded user) and,
// for an LP, the business name, which is the applicant's verified legal name.
func redactMerchantRequest(c *gin.Context, caller *models.User, req *models.MerchantRequest) {
	if hasAdminPrivilege(c, caller) {
		return
	}
	req.SettlementBankCode, req.SettlementAccountNumber, req.SettlementAccountName = "", "", ""
	req.MakerWalletAddress = ""
	req.User.WalletAddress = "" // usually the same wallet (review N1)
	if req.BusinessType == models.BusinessTypeLiquidityProvider {
		req.BusinessName = ""
	}
}

// isPMRequestType: the request approves into a payment_merchants row.
func isPMRequestType(t string) bool {
	_, ok := models.PaymentMerchantKindFor(t)
	return ok
}

// pmRowFor is the user's payment_merchants row of kind, any status: a
// REJECTED LP row still holds the LP slot, because upsertPendingRow reuses it
// (review I2). (nil, nil) when there is none; an error is never read as "no
// row" (review M6).
func (h *MerchantRequestHandler) pmRowFor(ctx context.Context, userID uuid.UUID, kind string) (*models.PaymentMerchant, error) {
	pms := h.PMService
	if pms == nil {
		pms = services.NewPMService(h.DB)
	}
	return services.OptionalPM(pms.GetByUserIDKind(ctx, userID, kind))
}
