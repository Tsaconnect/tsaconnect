package handlers

import (
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
type rewardClaimInput struct {
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Username         string `json:"username"`
	PhoneNumber      string `json:"phoneNumber"`
	PhoneNetwork     string `json:"phoneNetwork"`
	InviterUsername  string `json:"inviterUsername"`
	SocialLinks      string `json:"socialLinks"`
	PayoutMethod     string `json:"payoutMethod"`
	PayoutDetails    string `json:"payoutDetails"`
	InvitedUsernames string `json:"invitedUsernames"`
}

// countInvited splits the comma/whitespace-separated username list, ignoring
// blanks so trailing commas don't inflate the count.
func countInvited(raw string) int {
	n := 0
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ' ' || r == '\t'
	}) {
		if strings.TrimSpace(part) != "" {
			n++
		}
	}
	return n
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
	name := strings.TrimSpace(in.Name)
	username := strings.ToLower(strings.TrimSpace(in.Username))
	if name == "" || username == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Name and username are required")
		return
	}

	claim := models.RewardClaim{
		UserID: user.ID, Kind: kind, Status: models.RewardClaimStatusPending,
		Name: name, Username: username,
	}

	switch kind {
	case models.RewardClaimKindSignup:
		phone := strings.TrimSpace(in.PhoneNumber)
		network := strings.ToLower(strings.TrimSpace(in.PhoneNetwork))
		if phone == "" {
			utils.ErrorResponse(c, http.StatusBadRequest, "Phone number is required for airtime payout")
			return
		}
		if !models.ValidRewardPhoneNetwork(network) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Select a valid phone network")
			return
		}
		claim.PhoneNumber = phone
		claim.PhoneNetwork = network
		claim.InviterUsername = strings.ToLower(strings.TrimSpace(in.InviterUsername))
		claim.SocialLinks = strings.TrimSpace(in.SocialLinks)

	case models.RewardClaimKindOnboarding:
		method := strings.TrimSpace(in.PayoutMethod)
		details := strings.TrimSpace(in.PayoutDetails)
		if method == "" || details == "" {
			utils.ErrorResponse(c, http.StatusBadRequest, "Bank or mobile money details are required")
			return
		}
		if n := countInvited(in.InvitedUsernames); n < models.MinOnboardingReferrals {
			utils.ErrorResponse(c, http.StatusBadRequest,
				"List at least "+strconv.Itoa(models.MinOnboardingReferrals)+" usernames you onboarded")
			return
		}
		claim.PayoutMethod = method
		claim.PayoutDetails = details
		claim.InvitedUsernames = strings.TrimSpace(in.InvitedUsernames)
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

	if err := h.DB.Create(&claim).Error; err != nil {
		// The (user_id, kind) unique index is what enforces one-time; a racing
		// double-submit lands here rather than creating a second row.
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "UNIQUE") ||
			strings.Contains(err.Error(), "unique") {
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
	var claims []models.RewardClaim
	if err := q.Find(&claims).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load submissions")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Reward claims", gin.H{"claims": claims})
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
		Status      string `json:"status"`
		ReviewNotes string `json:"reviewNotes"`
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
	var claim models.RewardClaim
	if err := h.DB.First(&claim, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Submission not found")
		return
	}
	updates := map[string]interface{}{
		"status":       status,
		"reviewed_by":  admin.ID,
		"review_notes": strings.TrimSpace(in.ReviewNotes),
	}
	if err := h.DB.Model(&claim).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update the submission")
		return
	}
	h.DB.First(&claim, "id = ?", id)
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

	gen := atoiDefault(c.Query("gen"), 1)
	limit := atoiDefault(c.Query("limit"), 50)
	offset := atoiDefault(c.Query("offset"), 0)

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
	total := 0
	for _, n := range counts {
		total += n
	}
	utils.SuccessResponse(c, http.StatusOK, "Downlines", gin.H{
		"user":            gin.H{"id": user.ID, "username": user.Username, "name": user.Name},
		"downlines":       rows,
		"countsByGen":     counts,
		"totalDownlines":  total,
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
