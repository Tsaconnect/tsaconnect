package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// A verified user changing their name by verifying again (services.NameChangeService
// owns the rules and the fee). This file is the HTTP side and the Didit side:
// buying the session for an attempt, and routing that session's webhook to the
// change rather than to first-time KYC.

// nameChangeVendorPrefix marks a Didit session bought for a name change. Didit
// echoes vendor_data on every event, so a re-verification's result is
// recognisable even before its session is looked up.
const nameChangeVendorPrefix = "name_change:"

// nameChangeCallbackFlow is added to the hosted flow's callback so the app can
// tell a name change's return from first-time KYC's (whose callback is unchanged).
const nameChangeCallbackFlow = "flow=name_change"

// nameChangeDiditTimeout bounds the Didit purchase, which runs on a context
// detached from the request: a client that hangs up mid-purchase must not leave
// a session bought and unrecorded.
const nameChangeDiditTimeout = 30 * time.Second

// nameChangeService is the service, or nil (with a 503 written) when name
// changes are not wired at all.
func (h *Handlers) nameChangeService(c *gin.Context) *services.NameChangeService {
	if h.NameChanges == nil {
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, services.ErrNameChangeDisabled.Error(), "name_change_disabled")
		return nil
	}
	return h.NameChanges
}

// nameChangeCaller is the signed-in user and the service, for routes that act
// on a change already running — those keep working with the feature switched
// off, so a paid change can always be finished or cancelled.
func (h *Handlers) nameChangeCaller(c *gin.Context) (*models.User, *services.NameChangeService, bool) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return nil, nil, false
	}
	svc := h.nameChangeService(c)
	if svc == nil {
		return nil, nil, false
	}
	return user, svc, true
}

// nameChangeStarter is nameChangeCaller for routes that start a new change:
// refused while the feature is switched off.
func (h *Handlers) nameChangeStarter(c *gin.Context) (*models.User, *services.NameChangeService, bool) {
	user, svc, ok := h.nameChangeCaller(c)
	if !ok {
		return nil, nil, false
	}
	if !svc.Enabled() {
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, services.ErrNameChangeDisabled.Error(), "name_change_disabled")
		return nil, nil, false
	}
	return user, svc, true
}

// GET /api/users/me/name-change?walletAddress= — the change (open, or the
// latest), whether a new one can start and why not, the limits, and the fee per
// payment method. walletAddress (optional, the device's) adds the spendable MCGP
// balance — only of the account's own linked wallet: a read never links, so any
// other address is 403 WALLET_PROOF_REQUIRED (requireOwnWallet). Answers even
// while the feature is off (enabled:false), so the app can hide it.
func (h *Handlers) GetNameChange(c *gin.Context) {
	user, svc, ok := h.nameChangeCaller(c)
	if !ok {
		return
	}
	wallet := ""
	if claimed := strings.TrimSpace(c.Query("walletAddress")); claimed != "" && user.WalletAddress != "" {
		if wallet, ok = requireOwnWallet(c, user, claimed); !ok {
			return
		}
	}
	out, err := svc.Overview(c.Request.Context(), user.ID, wallet)
	if err != nil {
		nameChangeFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Name change", out)
}

type createNameChangeRequest struct {
	services.NameChangeRequest
	Password string `json:"password"`
}

// POST /api/users/me/name-change?walletAddress= {payMethod, chain?, token?,
// password} — open a change and start paying for it. The account password is
// asked again (counted toward the lockout): a change takes a fee and ends with a
// new name on the account. A paid fee comes from the account's linked wallet,
// which must be one the account has proven it owns (requireProvenWallet: 403
// WALLET_PROOF_REQUIRED otherwise — the app proves it through the wallet-link
// flow and retries). walletAddress (the device's) holds that to the wallet that
// will actually sign: one other than the linked wallet is linked only with a
// proof (callerWallet), never paid from blind. Idempotency-Key: a retried
// create replays the first answer (the password is left out of what the key is
// matched on — see routes).
func (h *Handlers) CreateNameChange(c *gin.Context) {
	user, svc, ok := h.nameChangeStarter(c)
	if !ok {
		return
	}
	var req createNameChangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Invalid request body", "invalid_request")
		return
	}
	if !requireAccountPassword(c, h.EventBus, user, req.Password) {
		return
	}
	if !svc.FeeIsFree() {
		if device := strings.TrimSpace(c.Query("walletAddress")); device != "" {
			if _, ok := callerWallet(c, user, device); !ok {
				return
			}
		}
		// The fee is held on (MCGP) or accepted from and refunded to (stablecoin)
		// this wallet with no further signature from the user.
		wallet, ok := requireProvenWallet(c, user)
		if !ok {
			return
		}
		req.ProvenWallet = wallet
	}
	out, err := svc.Create(c.Request.Context(), user.ID, req.NameChangeRequest)
	if err != nil {
		nameChangeFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Name change started", out)
}

// POST /api/users/me/name-change/confirm-payment {txHash} — record and prove the
// stablecoin fee transfer. Also takes a payment for a change that closed unpaid
// in the last week, which is then refunded.
func (h *Handlers) ConfirmNameChangePayment(c *gin.Context) {
	user, svc, ok := h.nameChangeCaller(c)
	if !ok {
		return
	}
	var req struct {
		TxHash string `json:"txHash"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Invalid request body", "invalid_request")
		return
	}
	out, err := svc.ConfirmPayment(c.Request.Context(), user.ID, req.TxHash)
	if err != nil {
		nameChangeFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment recorded", gin.H{"change": out})
}

// POST /api/users/me/name-change/start-verification — a Didit session for the
// next attempt, or the one already open. This is the one way a verified user
// gets a verification session: first-time KYC keeps refusing them.
func (h *Handlers) StartNameChangeVerification(c *gin.Context) {
	user, svc, ok := h.nameChangeCaller(c)
	if !ok {
		return
	}
	cfg := h.Config
	if cfg == nil || cfg.DiditAPIKey == "" || cfg.DiditWorkflowID == "" {
		failKYCSession(c, http.StatusServiceUnavailable, "DIDIT_API_KEY or DIDIT_WORKFLOW_ID is unset (name change)")
		return
	}
	attempt, err := svc.BeginAttempt(c.Request.Context(), user.ID)
	if err != nil {
		nameChangeFail(c, err)
		return
	}
	if attempt.Resume {
		nameChangeSessionAnswer(c, svc, attempt.Change, true)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), nameChangeDiditTimeout)
	defer cancel()
	ch := attempt.Change
	resp, serr := createDiditSession(ctx, strings.TrimRight(cfg.DiditBaseURL, "/"), cfg.DiditAPIKey, cfg.DiditWorkflowID,
		nameChangeCallback(cfg.DiditCallbackURL), nameChangeVendorPrefix+ch.ID.String(), nil)
	if serr != nil {
		svc.AbandonAttempt(ctx, attempt)
		failKYCSession(c, serr.status, serr.detail)
		return
	}
	if err := svc.RecordSession(ctx, ch.ID, attempt.Claim, resp.SessionID, resp.URL); err != nil {
		// Bought, but the change closed in between (the claim makes a second
		// purchase for the same attempt impossible). Its result will be ignored.
		log.Printf("[name-change] change=%s could not record session %s: %v", ch.ID, resp.SessionID, err)
		nameChangeFail(c, err)
		return
	}
	ch.DiditSessionID, ch.DiditSessionURL = resp.SessionID, resp.URL
	nameChangeSessionAnswer(c, svc, ch, false)
}

// nameChangeCallback is first-time KYC's callback with the name-change flow
// marked, so the app returns the user to the right screen.
func nameChangeCallback(callback string) string {
	if callback == "" {
		return ""
	}
	if strings.Contains(callback, "?") {
		return callback + "&" + nameChangeCallbackFlow
	}
	return callback + "?" + nameChangeCallbackFlow
}

func nameChangeSessionAnswer(c *gin.Context, svc *services.NameChangeService, ch *models.UserNameChange, resumed bool) {
	utils.SuccessResponse(c, http.StatusOK, "Verification session ready", gin.H{
		"url":         ch.DiditSessionURL,
		"sessionId":   ch.DiditSessionID,
		"resumed":     resumed,
		"attempts":    ch.Attempts,
		"maxAttempts": svc.MaxAttempts(),
	})
}

// POST /api/users/me/name-change/cancel — stop before the first attempt; the fee
// comes back (within the refund allowance).
func (h *Handlers) CancelNameChange(c *gin.Context) {
	user, svc, ok := h.nameChangeCaller(c)
	if !ok {
		return
	}
	out, err := svc.Cancel(c.Request.Context(), user.ID)
	if err != nil {
		nameChangeFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Name change cancelled", gin.H{"change": out})
}

// handleNameChangeWebhook takes a Didit event that belongs to a name change and
// reports whether it did. A session is a change's when it is the change's
// current session; vendor_data carrying the name-change marker only says the
// event is not first-time KYC's — it cannot, on its own, point an event at a
// change (the simple signature does not cover it).
func (h *Handlers) handleNameChangeWebhook(c *gin.Context, payload diditWebhookPayload) bool {
	claimed := strings.HasPrefix(payload.VendorData, nameChangeVendorPrefix)
	var ch *models.UserNameChange
	if h.NameChanges != nil && payload.SessionID != "" {
		found, err := h.NameChanges.FindBySession(c.Request.Context(), payload.SessionID)
		if err != nil {
			log.Printf("[name-change] webhook session lookup failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to look up session"})
			return true
		}
		ch = found
	}
	if ch == nil {
		if claimed {
			c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored event for no current name-change session"})
		}
		return claimed
	}
	// Name changes act on decisions only; a reviewer's later edit of the data
	// on a re-verification session changes nothing already decided.
	if payload.WebhookType != diditWebhookStatusUpdated {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored name-change data update"})
		return true
	}

	decision := services.NameChangeDecision{SessionID: payload.SessionID}
	switch strings.TrimSpace(payload.Status) {
	case "Approved":
		decision.Approved = true
		if !h.readNameChangeIdentities(c, ch, &decision) {
			return true
		}
	case "Declined", "Abandoned", "Expired", "Kyc Expired":
	default:
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored non-terminal status for name change"})
		return true
	}
	if _, err := h.NameChanges.ApplyDecision(c.Request.Context(), ch.ID, decision); err != nil {
		log.Printf("[name-change] change=%s could not apply Didit decision: %v", ch.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to apply decision"})
		return true
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Webhook processed"})
	return true
}

// readNameChangeIdentities reads what both sessions say about the person: the
// change's (the new document) and the account's original KYC session. A session
// Didit no longer holds leaves that side empty (the change is then held for an
// admin); any other failure answers 500 so Didit delivers the event again, as
// the evidence is the whole point of the decision. false: the response is written.
func (h *Handlers) readNameChangeIdentities(c *gin.Context, ch *models.UserNameChange, d *services.NameChangeDecision) bool {
	if h.Config == nil || h.Config.DiditAPIKey == "" {
		return true
	}
	ctx := c.Request.Context()
	read := func(session string) (*services.NameChangeIdentity, bool) {
		id, err := fetchDiditIdentity(ctx, h.Config.DiditBaseURL, h.Config.DiditAPIKey, session)
		switch {
		case errors.Is(err, errDiditNameUnresolved):
			return nil, true
		case err != nil:
			log.Printf("[name-change] change=%s identity lookup failed for session %s: %v", ch.ID, session, err)
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to read the verification result"})
			return nil, false
		}
		return id, true
	}
	fresh, ok := read(d.SessionID)
	if !ok {
		return false
	}
	if fresh != nil {
		d.New = *fresh
	}
	if ch.OriginalDiditSessionID != "" {
		original, ok := read(ch.OriginalDiditSessionID)
		if !ok {
			return false
		}
		d.Original = original
	}
	return true
}

// GET /api/admin/name-changes?status=&feeStatus=&userId=&page=&limit= — the
// review queue and every user's name history. feeStatus is a comma-separated
// list (e.g. refund_sending,refund_uncertain,refund_failed): the changes whose
// money needs a look.
func (h *Handlers) AdminListNameChanges(c *gin.Context) {
	svc := h.nameChangeService(c)
	if svc == nil {
		return
	}
	f := services.NameChangeFilter{Status: strings.TrimSpace(c.Query("status"))}
	for _, fs := range strings.Split(c.Query("feeStatus"), ",") {
		if fs = strings.TrimSpace(fs); fs != "" {
			f.FeeStatuses = append(f.FeeStatuses, fs)
		}
	}
	if raw := strings.TrimSpace(c.Query("userId")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Invalid userId", "invalid_request")
			return
		}
		f.UserID = &id
	}
	f.Page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	f.Limit, _ = strconv.Atoi(c.DefaultQuery("limit", "20"))
	rows, total, err := svc.AdminList(c.Request.Context(), f)
	if err != nil {
		nameChangeFail(c, err)
		return
	}
	page, limit := max(f.Page, 1), f.Limit
	if limit < 1 || limit > 100 {
		limit = 20
	}
	utils.SuccessResponse(c, http.StatusOK, "Name changes", gin.H{"changes": rows, "total": total, "page": page, "limit": limit})
}

// POST /api/admin/name-changes/:id/approve {note}
func (h *Handlers) AdminApproveNameChange(c *gin.Context) { h.adminReviewNameChange(c, true) }

// POST /api/admin/name-changes/:id/reject {note}
func (h *Handlers) AdminRejectNameChange(c *gin.Context) { h.adminReviewNameChange(c, false) }

func (h *Handlers) adminReviewNameChange(c *gin.Context, approve bool) {
	admin, svc, id, ok := h.nameChangeAdmin(c)
	if !ok {
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&req) // the note is optional
	review, decision := svc.AdminReject, "reject"
	if approve {
		review, decision = svc.AdminApprove, "approve"
	}
	out, err := review(c.Request.Context(), admin.ID, id, req.Note)
	if err != nil {
		nameChangeFail(c, err)
		return
	}
	// The row records who decided (reviewed_by/at); this line puts it in the
	// log stream the other admin money decisions are audited in.
	log.Printf("[name-change] AUDIT change=%s admin=%s decision=%s", id, admin.ID, decision)
	utils.SuccessResponse(c, http.StatusOK, "Name change reviewed", gin.H{"change": out})
}

// POST /api/admin/name-changes/:id/refund-outcome {sent: true, txHash, note} —
// super admin: record a refund whose broadcast ended uncertain as sent. Checked
// against the chain: txHash must be a mined treasury transfer of the refund to
// the paying wallet (422 refund_unproven). Only "sent" is taken — sent:false or
// no txHash is 422 refund_outcome_sent_only: a refund that never left stays
// flagged for support to resolve by hand. Never on the admin's own change.
func (h *Handlers) AdminRecordNameChangeRefund(c *gin.Context) {
	admin, svc, id, ok := h.nameChangeAdmin(c)
	if !ok {
		return
	}
	var req struct {
		Sent   bool   `json:"sent"`
		TxHash string `json:"txHash"`
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Invalid request body", "invalid_request")
		return
	}
	out, err := svc.AdminRecordRefundOutcome(c.Request.Context(), admin.ID, id,
		services.NameChangeRefundOutcome{Sent: req.Sent, TxHash: req.TxHash, Note: req.Note})
	if err != nil {
		nameChangeFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Refund outcome recorded", gin.H{"change": out})
}

// POST /api/admin/name-changes/:id/clear-payment {reason} — super admin: forget
// a stablecoin change's named transaction, closing the change unpaid if it is
// still paying. The chain is read first: a payment that is proven, mined with
// money in it, or known to the chain is refused (409 payment_may_land). An
// unknown one is still watched for a week and sent back if it lands. Audited.
func (h *Handlers) AdminClearNameChangePayment(c *gin.Context) {
	admin, svc, id, ok := h.nameChangeAdmin(c)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Reason) == "" {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Say what the chain shows for this payment", "invalid_request")
		return
	}
	out, err := svc.AdminClearUnprovenPayment(c.Request.Context(), admin.ID, id, req.Reason)
	if err != nil {
		nameChangeFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment cleared", gin.H{"change": out})
}

// nameChangeAdmin is the admin, the service and the :id of an admin route.
func (h *Handlers) nameChangeAdmin(c *gin.Context) (*models.User, *services.NameChangeService, uuid.UUID, bool) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return nil, nil, uuid.Nil, false
	}
	svc := h.nameChangeService(c)
	if svc == nil {
		return nil, nil, uuid.Nil, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponseWithCode(c, http.StatusNotFound, services.ErrNameChangeNotFound.Error(), "not_found")
		return nil, nil, uuid.Nil, false
	}
	return admin, svc, id, true
}

// nameChangeFail maps a refusal to its status and machine code. Refusals a
// client acts on carry what it needs in data (blockers, the next allowed date).
func nameChangeFail(c *gin.Context, err error) {
	var limit *services.NameChangeLimitError
	var blocked *services.NameChangeBlockedError
	switch {
	case errors.As(err, &limit):
		code := "name_change_limit"
		if limit.Cancel {
			code = "cancel_limit"
		}
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": err.Error(), "code": code,
			"data": gin.H{"nextAllowedAt": limit.NextAllowedAt.UTC().Format(time.RFC3339)}})
	case errors.As(err, &blocked):
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": err.Error(), "code": "money_in_flight",
			"data": gin.H{"blockers": blocked.Blockers}})
	case errors.Is(err, services.ErrNameChangeDisabled):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, err.Error(), "name_change_disabled")
	case errors.Is(err, services.ErrNameChangeNotVerified):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), "not_verified")
	case errors.Is(err, services.ErrNameChangeUserNotFound):
		utils.ErrorResponseWithCode(c, http.StatusNotFound, err.Error(), "user_not_found")
	case errors.Is(err, services.ErrNameChangeOpen):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "change_open")
	case errors.Is(err, services.ErrNameChangeNotFound):
		utils.ErrorResponseWithCode(c, http.StatusNotFound, err.Error(), "no_open_change")
	case errors.Is(err, services.ErrNameChangeInvalid):
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, err.Error(), "invalid_request")
	case errors.Is(err, services.ErrNameChangeWalletUnproven):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, walletProofRequiredMessage, WalletProofRequiredCode)
	case errors.Is(err, services.ErrNameChangeMethodUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "method_unavailable")
	case errors.Is(err, services.ErrNameChangeInsufficient):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "insufficient_balance")
	case errors.Is(err, services.ErrNameChangePaymentFailed):
		utils.ErrorResponseWithCode(c, http.StatusBadGateway, err.Error(), "payment_failed")
	case errors.Is(err, services.ErrNameChangePaymentOnChain):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "payment_pending_chain")
	case errors.Is(err, services.ErrNameChangePaymentMismatch):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "payment_mismatch")
	case errors.Is(err, services.ErrNameChangePaymentPending):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "payment_pending")
	case errors.Is(err, services.ErrNameChangeAttemptsExhausted):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "attempts_exhausted")
	case errors.Is(err, services.ErrNameChangeSessionLimit):
		utils.ErrorResponseWithCode(c, http.StatusTooManyRequests, err.Error(), "session_limit")
	case errors.Is(err, services.ErrNameChangeSessionPending):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "session_pending")
	case errors.Is(err, services.ErrNameChangeLapsed):
		utils.ErrorResponseWithCode(c, http.StatusGone, err.Error(), "change_lapsed")
	case errors.Is(err, services.ErrNameChangeUnderReview):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "under_review")
	case errors.Is(err, services.ErrNameChangeCancelNotAllowed):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "cancel_not_allowed")
	case errors.Is(err, services.ErrNameChangeSelfReview):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), "self_review")
	case errors.Is(err, services.ErrNameChangeBadState):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "bad_state")
	case errors.Is(err, services.ErrNameChangePaymentLive):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "payment_may_land")
	case errors.Is(err, services.ErrNameChangeRefundSentOnly):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "refund_outcome_sent_only")
	case errors.Is(err, services.ErrNameChangeRefundUnproven):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "refund_unproven")
	case errors.Is(err, services.ErrNameChangeChainUnreadable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, err.Error(), "chain_unreadable")
	case errors.Is(err, services.ErrNameChangeAccountUnverified):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "account_not_verified")
	default:
		log.Printf("[name-change] request failed: %s %s: %v", c.Request.Method, c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
	}
}
