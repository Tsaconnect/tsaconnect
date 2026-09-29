package handlers

import (
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// Statuses an admin may filter the review queue by.
var validAccountDeletionStatuses = []string{
	models.AccountDeletionStatusPending,
	models.AccountDeletionStatusCompleted,
	models.AccountDeletionStatusRestored,
}

type requestAccountDeletionInput struct {
	Password string `json:"password"`
	Reason   string `json:"reason"`
}

type accountDeletionNoteInput struct {
	Note string `json:"note"`
}

// blockingCommitments reports the in-flight obligations that must be settled
// before a user can walk away: live P2P trades (someone else's money is locked
// in escrow against them), live marketplace orders (goods or funds are in
// flight), a sale to her bank still in progress (her bank details are
// attached to it) and a converted purchase not yet delivered (the treasury
// holds what it owes her). Returns a human-readable list — empty means clear
// to delete.
//
// Deliberately NOT a blocker: a non-zero wallet balance. The wallet is
// non-custodial and the user keeps their seed phrase, so deleting the account
// never strands funds. We warn about it in the app instead.
func blockingCommitments(db *gorm.DB, userID uuid.UUID) ([]string, error) {
	var blockers []string

	liveTradeStatuses := []string{
		models.P2PTradeStatusPendingPayment,
		models.P2PTradeStatusPaid,
		models.P2PTradeStatusReleasing,
		models.P2PTradeStatusDisputed,
	}
	var tradeCount int64
	if err := db.Model(&models.P2PTrade{}).
		Where("(buyer_id = ? OR seller_id = ?) AND status IN ?", userID, userID, liveTradeStatuses).
		Count(&tradeCount).Error; err != nil {
		return nil, err
	}
	if tradeCount > 0 {
		blockers = append(blockers, fmt.Sprintf("%d open P2P trade(s)", tradeCount))
	}

	liveOrderStatuses := []string{
		models.OrderStatusPendingPayment,
		models.OrderStatusEscrowed,
		models.OrderStatusShipped,
		models.OrderStatusDelivered,
		models.OrderStatusRefundRequested,
		models.OrderStatusCancelRequested,
	}
	var orderCount int64
	if err := db.Model(&models.Order{}).
		Where("(buyer_id = ? OR seller_id = ?) AND status IN ?", userID, userID, liveOrderStatuses).
		Count(&orderCount).Error; err != nil {
		return nil, err
	}
	if orderCount > 0 {
		blockers = append(blockers, fmt.Sprintf("%d open marketplace order(s)", orderCount))
	}

	var saleCount, fundingCount int64
	if err := db.Model(&models.RampSellIntent{}).
		Where("user_id = ? AND purpose <> ? AND status IN ?", userID, models.SellIntentPurposeMarketplace, models.SellIntentOpenStatuses).
		Count(&saleCount).Error; err != nil {
		return nil, err
	}
	if saleCount > 0 {
		blockers = append(blockers, fmt.Sprintf("%d sale to your bank still in progress", saleCount))
	}
	if err := db.Model(&models.RampSellIntent{}).
		Where("user_id = ? AND purpose = ? AND status IN ?", userID, models.SellIntentPurposeMarketplace, models.SellIntentOpenStatuses).
		Count(&fundingCount).Error; err != nil {
		return nil, err
	}
	if fundingCount > 0 {
		blockers = append(blockers, fmt.Sprintf("%d checkout payment (a swap into your wallet) still in progress", fundingCount))
	}

	var purchaseCount int64
	if err := db.Model(&models.PaycrestOrderRecord{}).
		Where("user_id = ? AND route_kind = ? AND status NOT IN ?",
			userID, models.RouteKindConverted, models.PaycrestTerminalStatuses()).
		Count(&purchaseCount).Error; err != nil {
		return nil, err
	}
	if purchaseCount > 0 {
		blockers = append(blockers, fmt.Sprintf("%d purchase still being delivered", purchaseCount))
	}

	// A TSA Pay payment in flight moves money either way: hers to a merchant, or
	// a customer's to her own merchant account's bank or wallet.
	var payCount int64
	if err := db.Model(&models.PayPayment{}).
		Where("status = ? AND mode = ? AND (payer_user_id = ? OR merchant_id IN (?))",
			models.PayStatusProcessing, models.PayModeLive, userID,
			db.Model(&models.PayMerchant{}).Select("id").Where("user_id = ?", userID)).
		Count(&payCount).Error; err != nil {
		return nil, err
	}
	if payCount > 0 {
		blockers = append(blockers, fmt.Sprintf("%d TSA Pay payment(s) still being settled", payCount))
	}

	return blockers, nil
}

// RequestAccountDeletion handles POST /api/users/account/deletion-request.
//
// Soft-deletes the caller's account immediately and files a review row for
// admin ops. The deletion is NOT queued behind human approval — Apple 5.1.1(v)
// requires the account to actually stop working when the user asks. Admins are
// notified so they can carry out the permanent erasure once the grace window
// closes, or reinstate the account if the user comes back.
func (h *Handlers) RequestAccountDeletion(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var input requestAccountDeletionInput
	if err := c.ShouldBindJSON(&input); err != nil || input.Password == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Password is required to delete your account")
		return
	}

	// Re-authenticate. Deletion is destructive and a stolen session must not be
	// enough to trigger it.
	if err := user.ComparePassword(input.Password); err != nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Incorrect password")
		return
	}

	blockers, err := blockingCommitments(config.DB, user.ID)
	if err != nil {
		log.Printf("RequestAccountDeletion: commitment check for %s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not verify your open trades and orders")
		return
	}
	if len(blockers) > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": "Finish or cancel your open trades and orders before deleting your account",
			"data":    gin.H{"blockers": blockers},
		})
		return
	}

	now := time.Now()
	purgeAfter := now.AddDate(0, 0, models.AccountDeletionGraceDays)

	req := models.AccountDeletionRequest{
		UserID:     user.ID,
		Reason:     input.Reason,
		Status:     models.AccountDeletionStatusPending,
		PurgeAfter: purgeAfter,
	}

	// One transaction: the request row and the soft delete land together, so we
	// can never end up with a disabled account nobody is tracking (or a review
	// row for an account that still works).
	txErr := config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&req).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
			"account_status": models.AccountStatusDeleted,
			"deleted_at":     now,
			"updated_at":     now,
		}).Error; err != nil {
			return err
		}
		// Her TSA Pay merchant account stops with her: a suspended merchant's API
		// keys are refused and its payments can no longer be started, so no
		// customer money is taken for an account that no longer exists. Its
		// pending payments expire on their own clock. Reinstating the account
		// does not reactivate the merchant — that stays a super admin's call.
		// The version bump makes any in-flight admin edit fail rather than
		// overwrite the suspension.
		if err := tx.Model(&models.PayMerchant{}).
			Where("user_id = ? AND status = ?", user.ID, models.PayMerchantActive).
			Updates(map[string]interface{}{
				"status":     models.PayMerchantSuspended,
				"version":    gorm.Expr("version + 1"),
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		if err := forgetTrustedBrowsers(tx, user.ID); err != nil {
			return err
		}
		// Revoke all active refresh tokens so stale sessions cannot be reused upon reactivation
		return revokeRefreshTokens(tx, user.ID)
	})
	if txErr != nil {
		log.Printf("RequestAccountDeletion: %s: %v", user.ID, txErr)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete your account")
		return
	}

	// Drop the user's push tokens — the account is gone, the device must stop
	// receiving notifications for it. Best-effort; a stale token only costs a
	// wasted send.
	if err := config.DB.Where("user_id = ?", user.ID).Delete(&models.DeviceToken{}).Error; err != nil {
		log.Printf("RequestAccountDeletion: prune device tokens for %s: %v", user.ID, err)
	}

	h.publishAccountDeletionEvents(req, user, purgeAfter)

	utils.SuccessResponse(c, http.StatusOK, "Your account has been deleted", gin.H{
		"requestId":  req.ID,
		"status":     req.Status,
		"purgeAfter": purgeAfter,
		"graceDays":  models.AccountDeletionGraceDays,
	})
}

// publishAccountDeletionEvents notifies the departing user (confirmation +
// how to come back) and every admin/super-admin (review queue).
func (h *Handlers) publishAccountDeletionEvents(req models.AccountDeletionRequest, user *models.User, purgeAfter time.Time) {
	if h.EventBus == nil {
		return
	}

	data := map[string]interface{}{
		"requestId":  req.ID.String(),
		"userId":     user.ID.String(),
		"email":      user.Email,
		"reason":     req.Reason,
		"purgeAfter": purgeAfter,
	}

	h.EventBus.Publish(events.Event{
		Type:   events.AccountDeletionRequested,
		UserID: user.ID,
		Title:  "Your account has been deleted",
		Message: fmt.Sprintf(
			"Your TSA Connect account is now closed and you have been signed out. We keep a limited record for %d days — contact support@tsaconnectworld.com before %s if you want it restored. Your crypto wallet is non-custodial: your recovery phrase still controls your funds.",
			models.AccountDeletionGraceDays, purgeAfter.Format("2 January 2006"),
		),
		Data: data,
	})

	var admins []models.User
	if err := config.DB.Where("role IN (?)", []string{models.RoleAdmin, models.RoleSuperAdmin}).Find(&admins).Error; err != nil {
		log.Printf("publishAccountDeletionEvents: admin lookup: %v", err)
		return
	}
	reason := req.Reason
	if reason == "" {
		reason = "no reason given"
	}
	for _, a := range admins {
		h.EventBus.Publish(events.Event{
			Type:   events.AccountDeletionRequested,
			UserID: a.ID,
			Title:  "Account deletion to action",
			Message: fmt.Sprintf(
				"%s (%s) deleted their account. Reason: %s. Eligible for permanent erasure after %s.",
				user.Name, user.Email, reason, purgeAfter.Format("2 January 2006"),
			),
			Data: data,
		})
	}
}

// ListAccountDeletionRequests handles GET /api/admin/account-deletion-requests.
func (h *Handlers) ListAccountDeletionRequests(c *gin.Context) {
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

	query := config.DB.Model(&models.AccountDeletionRequest{})
	if status != "" {
		if !slices.Contains(validAccountDeletionStatuses, status) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid status filter")
			return
		}
		query = query.Where("status = ?", status)
	}

	// "due" surfaces only the requests whose grace window has closed — the
	// actual work queue for a purge run.
	if c.Query("due") == "true" {
		query = query.Where("status = ? AND purge_after <= ?", models.AccountDeletionStatusPending, time.Now())
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		log.Printf("ListAccountDeletionRequests: count: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to count deletion requests")
		return
	}

	var requests []models.AccountDeletionRequest
	if err := query.Preload("User").Preload("Reviewer").
		Order("created_at DESC").
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&requests).Error; err != nil {
		log.Printf("ListAccountDeletionRequests: find: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to retrieve deletion requests")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Deletion requests retrieved", gin.H{
		"requests": requests,
		"total":    total,
		"page":     page,
		"limit":    limit,
	})
}

// CompleteAccountDeletionRequest handles
// POST /api/admin/account-deletion-requests/:id/complete — the admin records
// that the permanent erasure/anonymisation has been carried out. The account
// stays soft-deleted; this closes the review item.
func (h *Handlers) CompleteAccountDeletionRequest(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if !hasAdminPrivilege(c, admin) {
		utils.ErrorResponse(c, http.StatusForbidden, "Admin role required")
		return
	}

	req, ok := h.loadPendingDeletionRequest(c)
	if !ok {
		return
	}

	var input accountDeletionNoteInput
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&input); err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
			return
		}
	}

	now := time.Now()
	txErr := config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.AccountDeletionRequest{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
			"status":      models.AccountDeletionStatusCompleted,
			"admin_note":  input.Note,
			"reviewed_by": admin.ID,
			"reviewed_at": now,
			"updated_at":  now,
		}).Error; err != nil {
			return err
		}

		// Permanent erasure: anonymize personal identifiers so PII is removed
		// and the user's original email, username, and phone are freed up for new registration.
		anonCleanID := strings.ReplaceAll(req.UserID.String(), "-", "")
		if len(anonCleanID) > 12 {
			anonCleanID = anonCleanID[:12]
		}
		anonEmail := fmt.Sprintf("deleted_%s@deleted.local", req.UserID.String())
		anonUsername := fmt.Sprintf("deleted_%s", anonCleanID)

		// Her bank details on finished ramp activity. blockingCommitments kept
		// anything open from reaching here; this closes out rows that ended
		// before the state transitions started clearing them. Only resolved
		// rows: an open intent is left alone (erasing its destination would
		// break an order being opened for it), and an unmatched order keeps
		// its payout details because an operator is still chasing it.
		if err := tx.Model(&models.RampSellIntent{}).
			Where("user_id = ? AND status IN ?", req.UserID,
				[]string{models.SellIntentOrdered, models.SellIntentDelivered, models.SellIntentFailed}).
			Updates(models.WithBankDetailsCleared(map[string]any{})).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.PaycrestOrderRecord{}).
			Where("user_id = ? AND status IN ?", req.UserID, models.PaycrestResolvedStatuses()).
			Updates(models.WithPayoutDetailsCleared(map[string]any{})).Error; err != nil {
			return err
		}

		// Her merchant account's payout destinations and credentials: nothing
		// may settle to them or authenticate with them after erasure.
		if err := tx.Model(&models.PayMerchant{}).Where("user_id = ?", req.UserID).Updates(map[string]interface{}{
			"status":           models.PayMerchantSuspended,
			"settlement_mode":  "",
			"bank_code":        "",
			"account_number":   "",
			"account_name":     "",
			"wallet_address":   "",
			"webhook_url":      "",
			"test_webhook_url": "",
			"live_key_hash":    nil,
			"live_key_prefix":  "",
			"test_key_hash":    nil,
			"test_key_prefix":  "",
			"version":          gorm.Expr("version + 1"),
			"updated_at":       now,
		}).Error; err != nil {
			return err
		}

		return tx.Model(&models.User{}).Where("id = ?", req.UserID).Updates(map[string]interface{}{
			"email":          anonEmail,
			"username":       anonUsername,
			"phone_number":   "",
			"name":           "Deleted User",
			"profile_photo":  "",
			"address":        "",
			"city":           "",
			"state":          "",
			"country":        "",
			"wallet_address": "",
			"updated_at":     now,
		}).Error
	})
	if txErr != nil {
		log.Printf("CompleteAccountDeletionRequest %s: %v", req.ID, txErr)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update deletion request")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Deletion request marked complete", gin.H{
		"requestId": req.ID,
		"status":    models.AccountDeletionStatusCompleted,
	})
}

// RestoreAccountDeletionRequest handles
// POST /api/admin/account-deletion-requests/:id/restore — reinstates a
// soft-deleted account (user changed their mind inside the grace window).
func (h *Handlers) RestoreAccountDeletionRequest(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if !hasAdminPrivilege(c, admin) {
		utils.ErrorResponse(c, http.StatusForbidden, "Admin role required")
		return
	}

	req, ok := h.loadPendingDeletionRequest(c)
	if !ok {
		return
	}

	var input accountDeletionNoteInput
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&input); err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
			return
		}
	}

	now := time.Now()
	txErr := config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.AccountDeletionRequest{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
			"status":      models.AccountDeletionStatusRestored,
			"admin_note":  input.Note,
			"reviewed_by": admin.ID,
			"reviewed_at": now,
			"updated_at":  now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&models.User{}).Where("id = ?", req.UserID).Updates(map[string]interface{}{
			"account_status": models.AccountStatusActive,
			"deleted_at":     nil,
			"updated_at":     now,
		}).Error
	})
	if txErr != nil {
		log.Printf("RestoreAccountDeletionRequest %s: %v", req.ID, txErr)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to restore account")
		return
	}

	if h.EventBus != nil {
		h.EventBus.Publish(events.Event{
			Type:    events.AccountDeletionRestored,
			UserID:  req.UserID,
			Title:   "Your account has been restored",
			Message: "Your TSA Connect account has been reinstated by our support team and you can sign in again. If you did not ask for this, contact support@tsaconnectworld.com immediately.",
			Data: map[string]interface{}{
				"requestId": req.ID.String(),
				"userId":    req.UserID.String(),
			},
		})
	}

	utils.SuccessResponse(c, http.StatusOK, "Account restored", gin.H{
		"requestId": req.ID,
		"status":    models.AccountDeletionStatusRestored,
	})
}

// loadPendingDeletionRequest parses :id and loads a request that is still
// pending. Writes the error response and returns ok=false on any failure, so
// callers can `if !ok { return }`.
func (h *Handlers) loadPendingDeletionRequest(c *gin.Context) (models.AccountDeletionRequest, bool) {
	var req models.AccountDeletionRequest

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request ID")
		return req, false
	}

	if err := config.DB.First(&req, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Deletion request not found")
		return req, false
	}

	if req.Status != models.AccountDeletionStatusPending {
		utils.ErrorResponse(c, http.StatusBadRequest, "Deletion request has already been actioned")
		return req, false
	}

	return req, true
}
