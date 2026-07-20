package handlers

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// reward_claim_handler.go — the PM "Reward" programme: two one-time enrolment
// forms plus the admin review surface and downline explorer.
//
// Payout is deliberately NOT executed here. The signup reward pays airtime, and
// no airtime rail exists in this tree (the Nomba bill-vending client lives on
// the unmerged feat/utility-bills branch). Approving a claim records the intent;
// settlement lands behind that seam later. See RewardClaimStatusPaid.

type RewardClaimHandler struct {
	DB      *gorm.DB
	Service *services.RewardClaimService
	Graph   *services.ReferralGraphService
}

func NewRewardClaimHandler(db *gorm.DB, svc *services.RewardClaimService, graph *services.ReferralGraphService) *RewardClaimHandler {
	return &RewardClaimHandler{DB: db, Service: svc, Graph: graph}
}

// rewardClaimInput is the submission body. Which fields matter depends on kind;
// validation branches below rather than using binding tags, so each form gets a
// message naming the field the user actually sees.
//
// Name and Username are DELIBERATELY ABSENT: they are derived from the
// authenticated user. Accepting them from the body let a user file a claim
// under someone else's username, and since the admin list shows the claimed
// username next to the downline explorer, a reviewer could verify the wrong
// person's referrals and approve a payout to the submitter's account.
type rewardClaimInput struct {
	Kind             string `json:"kind"`
	PhoneNumber      string `json:"phoneNumber"`
	PhoneNetwork     string `json:"phoneNetwork"`
	InviterUsername  string `json:"inviterUsername"`
	SocialLinks      string `json:"socialLinks"`
	PayoutMethod     string `json:"payoutMethod"`
	PayoutDetails    string `json:"payoutDetails"`
	InvitedUsernames string `json:"invitedUsernames"`
}

// Free-text fields are capped so a submission can't store unbounded input.
const (
	maxPayoutDetailsLen    = 200
	maxSocialLinksLen      = 500
	maxInvitedUsernamesLen = 2000
)

// splitUsernames splits on commas and newlines ONLY — not spaces, so a name
// typed with a space isn't counted twice — and returns the distinct
// lowercased entries. Deduping matters: "a,a,a,…" must not satisfy the
// minimum-count rule.
func splitUsernames(raw string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	}) {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// countInvited reports the number of DISTINCT usernames listed.
func countInvited(raw string) int { return len(splitUsernames(raw)) }

// normalizePhone converts a Nigerian local number to E.164 so it satisfies
// utils.IsValidPhoneNumber (^\+?[1-9]\d{6,14}$ — no leading zero) and is stored
// in the canonical form an airtime rail expects. "0803 000 0000" → "2348030000000".
// Returns "" when the input can't be made valid.
func normalizePhone(raw string) string {
	var digits strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	n := digits.String()
	// Local format: 11 digits starting 0 (0803…) → swap the trunk 0 for +234.
	if len(n) == 11 && strings.HasPrefix(n, "0") {
		n = "234" + n[1:]
	}
	if !utils.IsValidPhoneNumber(n) {
		return ""
	}
	return n
}

// isDuplicateKeyErr is the fallback for drivers that don't surface a translated
// error (notably the SQLite driver used in tests, which reports
// "UNIQUE constraint failed"). Postgres unique violations are SQLSTATE 23505.
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "23505")
}

// rewardClaimTransitions is the allowed status state machine. `paid` and
// `rejected` are terminal: without this, a paid claim could be moved back to
// pending and approved again — a double-payout primitive once a payout rail
// exists behind the seam.
var rewardClaimTransitions = map[string][]string{
	models.RewardClaimStatusPending:  {models.RewardClaimStatusApproved, models.RewardClaimStatusRejected},
	models.RewardClaimStatusApproved: {models.RewardClaimStatusPaid, models.RewardClaimStatusRejected},
	models.RewardClaimStatusRejected: {},
	models.RewardClaimStatusPaid:     {},
}

func canTransition(from, to string) bool {
	for _, allowed := range rewardClaimTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// SubmitRewardClaim — POST /api/reward-claims (auth).
func (h *RewardClaimHandler) SubmitRewardClaim(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var in rewardClaimInput
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if !models.ValidRewardClaimKind(kind) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unknown reward form")
		return
	}

	// Identity comes from the token, never the body — see rewardClaimInput.
	claim := models.RewardClaim{
		UserID: user.ID, Kind: kind, Status: models.RewardClaimStatusPending,
		Name: user.Name, Username: strings.ToLower(user.Username),
	}

	switch kind {
	case models.RewardClaimKindSignup:
		// This number is the airtime destination — a malformed one means a
		// silently misdirected payout. Normalise to E.164 and store that.
		phone := normalizePhone(in.PhoneNumber)
		network := strings.ToLower(strings.TrimSpace(in.PhoneNetwork))
		if phone == "" {
			utils.ErrorResponse(c, http.StatusBadRequest, "Enter a valid phone number for the airtime payout")
			return
		}
		if !models.ValidRewardPhoneNetwork(network) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Select a valid phone network")
			return
		}
		socials := strings.TrimSpace(in.SocialLinks)
		if len(socials) > maxSocialLinksLen {
			utils.ErrorResponse(c, http.StatusBadRequest, "Social links are too long")
			return
		}
		// The inviter is already recorded at signup; an inviter claimed here
		// that contradicts it is a data-integrity problem on a payout record.
		inviter := strings.ToLower(strings.TrimSpace(in.InviterUsername))
		if inviter != "" {
			actual, err := h.actualInviterUsername(c, user)
			if err != nil {
				utils.ErrorResponse(c, http.StatusInternalServerError, "Could not verify your inviter")
				return
			}
			if actual == "" || actual != inviter {
				utils.ErrorResponse(c, http.StatusBadRequest,
					"That inviter doesn't match the account that referred you")
				return
			}
		}
		claim.PhoneNumber = phone
		claim.PhoneNetwork = network
		claim.InviterUsername = inviter
		claim.SocialLinks = socials

	case models.RewardClaimKindOnboarding:
		method := strings.TrimSpace(in.PayoutMethod)
		details := strings.TrimSpace(in.PayoutDetails)
		if method == "" || details == "" {
			utils.ErrorResponse(c, http.StatusBadRequest, "Bank or mobile money details are required")
			return
		}
		if len(details) > maxPayoutDetailsLen {
			utils.ErrorResponse(c, http.StatusBadRequest, "Account details are too long")
			return
		}
		if len(in.InvitedUsernames) > maxInvitedUsernamesLen {
			utils.ErrorResponse(c, http.StatusBadRequest, "Too many usernames listed")
			return
		}
		// Distinct count — a repeated name must not satisfy the minimum.
		invited := splitUsernames(in.InvitedUsernames)
		if len(invited) < models.MinOnboardingReferrals {
			utils.ErrorResponse(c, http.StatusBadRequest,
				"List at least "+strconv.Itoa(models.MinOnboardingReferrals)+" different usernames you onboarded")
			return
		}
		claim.PayoutMethod = method
		claim.PayoutDetails = details
		// Store the normalised, deduped list so the admin reads what was counted.
		claim.InvitedUsernames = strings.Join(invited, ",")
	}

	// Server-side gate: never trust the app's locked/unlocked rendering.
	elig, err := h.Service.Eligibility(c.Request.Context(), user.ID, kind)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not check eligibility")
		return
	}
	if !elig.Eligible {
		msg := "You are not eligible for this reward yet"
		if len(elig.Reasons) > 0 {
			msg = strings.Join(elig.Reasons, "; ")
		}
		utils.ErrorResponse(c, http.StatusForbidden, msg)
		return
	}

	if err := h.DB.WithContext(c.Request.Context()).Create(&claim).Error; err != nil {
		// The (user_id, kind) unique index is what enforces one-time; a racing
		// double-submit lands here rather than creating a second row. Prefer
		// GORM's translated error over substring-matching driver text, which
		// would also swallow unrelated constraint violations.
		if errors.Is(err, gorm.ErrDuplicatedKey) || isDuplicateKeyErr(err) {
			utils.ErrorResponse(c, http.StatusConflict, "You have already submitted this form")
			return
		}
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to submit the form")
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Submission received", claim)
}

// GetMyRewardClaims — GET /api/reward-claims/me (auth). Both forms' status +
// eligibility in one call so the hub can render without a second round trip.
func (h *RewardClaimHandler) GetMyRewardClaims(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var claims []models.RewardClaim
	if err := h.DB.Where("user_id = ?", user.ID).Find(&claims).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load your submissions")
		return
	}
	byKind := map[string]*models.RewardClaim{}
	for i := range claims {
		byKind[claims[i].Kind] = &claims[i]
	}

	out := gin.H{}
	for _, kind := range []string{models.RewardClaimKindSignup, models.RewardClaimKindOnboarding} {
		elig, err := h.Service.Eligibility(c.Request.Context(), user.ID, kind)
		if err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Could not check eligibility")
			return
		}
		out[kind] = gin.H{"claim": byKind[kind], "eligibility": elig}
	}
	utils.SuccessResponse(c, http.StatusOK, "Reward claims", out)
}

// ListRewardClaimsAdmin — GET /api/admin/reward-claims?kind=&status=
func (h *RewardClaimHandler) ListRewardClaimsAdmin(c *gin.Context) {
	if h.requireAdmin(c) == nil {
		return
	}
	q := h.DB.Order("created_at DESC")
	if kind := strings.ToLower(strings.TrimSpace(c.Query("kind"))); kind != "" {
		if !models.ValidRewardClaimKind(kind) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Unknown reward form")
			return
		}
		q = q.Where("kind = ?", kind)
	}
	if status := strings.ToLower(strings.TrimSpace(c.Query("status"))); status != "" {
		if !models.ValidRewardClaimStatus(status) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Unknown status")
			return
		}
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Model(&models.RewardClaim{}).Count(&total).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load submissions")
		return
	}

	limit := clamp(atoiDefault(c.Query("limit"), defaultPageSize), 1, maxPageSize)
	offset := clamp(atoiDefault(c.Query("offset"), 0), 0, math.MaxInt32)

	var claims []models.RewardClaim
	if err := q.Limit(limit).Offset(offset).Find(&claims).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load submissions")
		return
	}

	// The claim carries the submitter's identity, but resolving it against the
	// users table is what lets a reviewer trust it — the downline explorer on
	// the same screen is keyed by username, so a wrong one would verify the
	// wrong person's referrals.
	rows := make([]gin.H, 0, len(claims))
	for _, cl := range claims {
		row := gin.H{"claim": cl}
		var u models.User
		if err := h.DB.WithContext(c.Request.Context()).
			Select("username", "name", "verification_status").
			First(&u, "id = ?", cl.UserID).Error; err == nil {
			row["account"] = gin.H{
				"username":           u.Username,
				"name":               u.Name,
				"verificationStatus": u.VerificationStatus,
			}
		}
		rows = append(rows, row)
	}

	utils.SuccessResponse(c, http.StatusOK, "Reward claims", gin.H{
		"claims": rows,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

const (
	defaultPageSize = 50
	maxPageSize     = 200
	maxDownlineGen  = 10
)

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// UpdateRewardClaimStatus — PATCH /api/admin/reward-claims/:id.
func (h *RewardClaimHandler) UpdateRewardClaimStatus(c *gin.Context) {
	admin := h.requireAdmin(c)
	if admin == nil {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	var in struct {
		Status string `json:"status"`
		// Pointer so an omitted field leaves the previous reviewer's note
		// intact — blanking it would erase the audit trail on a payout decision.
		ReviewNotes *string `json:"reviewNotes"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if !models.ValidRewardClaimStatus(status) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unknown status")
		return
	}
	ctx := c.Request.Context()
	var claim models.RewardClaim
	if err := h.DB.WithContext(ctx).First(&claim, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Submission not found")
		return
	}
	if !canTransition(claim.Status, status) {
		utils.ErrorResponse(c, http.StatusConflict,
			"Cannot change a "+claim.Status+" submission to "+status)
		return
	}

	updates := map[string]interface{}{
		"status":      status,
		"reviewed_by": admin.ID,
	}
	if in.ReviewNotes != nil {
		updates["review_notes"] = strings.TrimSpace(*in.ReviewNotes)
	}

	// Conditional on the status we validated, so two admins PATCHing
	// concurrently can't both succeed with the loser silently winning.
	res := h.DB.WithContext(ctx).Model(&models.RewardClaim{}).
		Where("id = ? AND status = ?", id, claim.Status).
		Updates(updates)
	if res.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update the submission")
		return
	}
	if res.RowsAffected == 0 {
		utils.ErrorResponse(c, http.StatusConflict, "This submission was updated by someone else — reload and retry")
		return
	}

	// Approval is what authorises the reward. Airtime still has no rail in this
	// tree, but the TP leg is available today, so award it here rather than
	// leaving approval with no effect at all.
	if status == models.RewardClaimStatusApproved {
		if err := h.Service.AwardSignupTP(ctx, &claim); err != nil {
			// The status change already committed; surface the failure rather
			// than rolling back a legitimate approval decision.
			utils.ErrorResponse(c, http.StatusInternalServerError,
				"Approved, but the TradePoint award failed — retry the award")
			return
		}
	}

	if err := h.DB.WithContext(ctx).First(&claim, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Updated, but could not reload the submission")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Submission updated", claim)
}

// GetDownlinesAdmin — GET /api/admin/reward-claims/downlines?username=&gen=&limit=&offset=
// The admin tool: paste a username, see who they onboarded and who's verified.
func (h *RewardClaimHandler) GetDownlinesAdmin(c *gin.Context) {
	if h.requireAdmin(c) == nil {
		return
	}
	username := strings.ToLower(strings.TrimSpace(c.Query("username")))
	if username == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "username is required")
		return
	}
	// Referral code IS the lowercased username (auth_handler sets it at signup),
	// so accept either spelling of the same thing.
	var user models.User
	if err := h.DB.Where("username = ? OR referral_code = ?", username, username).
		First(&user).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "User not found")
		return
	}

	// Clamped: gen feeds a recursive CTE and limit is otherwise only floored by
	// the service, so an admin-authenticated ?gen=500&limit=10000000 would walk
	// the whole graph. Neither bound is reachable by legitimate use.
	gen := clamp(atoiDefault(c.Query("gen"), 1), 1, maxDownlineGen)
	limit := clamp(atoiDefault(c.Query("limit"), defaultPageSize), 1, maxPageSize)
	offset := clamp(atoiDefault(c.Query("offset"), 0), 0, math.MaxInt32)

	rows, err := h.Graph.Downlines(c.Request.Context(), user.ID, gen, limit, offset)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load downlines")
		return
	}
	counts, err := h.Graph.DownlineCounts(c.Request.Context(), user.ID, gen)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to count downlines")
		return
	}
	// Sum is over generations 1..gen, so name it for what it is — with the
	// default gen=1 this is the direct-referral count, not a whole-tree total.
	total := 0
	for _, n := range counts {
		total += n
	}
	utils.SuccessResponse(c, http.StatusOK, "Downlines", gin.H{
		"user":               gin.H{"id": user.ID, "username": user.Username, "name": user.Name},
		"downlines":          rows,
		"countsByGen":        counts,
		"generationsCounted": gen,
		"downlinesToGen":     total,
	})
}

func atoiDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// actualInviterUsername returns the lowercased username of the account that
// referred this user, or "" when they weren't referred.
func (h *RewardClaimHandler) actualInviterUsername(c *gin.Context, user *models.User) (string, error) {
	if user.ReferredBy == nil {
		return "", nil
	}
	var inviter models.User
	if err := h.DB.WithContext(c.Request.Context()).
		Select("username").First(&inviter, "id = ?", *user.ReferredBy).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	return strings.ToLower(inviter.Username), nil
}

// requireAdmin re-checks admin/super_admin inside the handler; the route group's
// adminAuth also admits merchant/support roles.
func (h *RewardClaimHandler) requireAdmin(c *gin.Context) *models.User {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil
	}
	if user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Admin only.")
		return nil
	}
	return user
}
