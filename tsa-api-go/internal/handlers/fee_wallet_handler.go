package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// FeeWalletHandler serves the admin portal's "fee wallet" surface: which wallet
// the platform's system fees are assigned to, who assigned it and when, and
// what each deployed escrow actually pays.
//
// Assignment here is a RECORD, not enforcement. Every escrow holds its own
// `systemWallet` and only its owner can change it (P2PEscrow.setSystemWallet),
// so the panel reports per contract rather than in aggregate — a green verdict
// covering contracts nobody read would be worse than no verdict at all.
type FeeWalletHandler struct {
	DB *gorm.DB
	// SeedAddress is config.SystemWalletAddress — the deployed default used to
	// seed the singleton on first read, so a fresh DB never shows "unset".
	SeedAddress string
	// Escrows is optional; when present the GET reports each enabled chain's
	// on-chain systemWallet for comparison. Nil (or a failed read) degrades to
	// "unverified" rather than failing the request.
	Escrows FeeSinkReader
}

// FeeSinkReader is the slice of the escrow registry this handler needs —
// declared here so the handler doesn't depend on the whole registry (and so
// tests can supply a stub).
type FeeSinkReader interface {
	SystemWallets() []services.FeeSinkStatus
}

// NewFeeWalletHandler creates a FeeWalletHandler. escrows may be nil.
func NewFeeWalletHandler(db *gorm.DB, seedAddress string, escrows FeeSinkReader) *FeeWalletHandler {
	return &FeeWalletHandler{DB: db, SeedAddress: seedAddress, Escrows: escrows}
}

// requireAdmin mirrors TPAdminHandler.requireAdmin — the route middleware
// already gates these endpoints, but this is the destination of every fee the
// platform charges, so the guard is repeated where the work happens rather
// than trusted to route wiring alone.
func (h *FeeWalletHandler) requireAdmin(c *gin.Context, superOnly bool) *models.User {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil
	}
	if superOnly {
		if user.Role != models.RoleSuperAdmin {
			utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Super admin only.")
			return nil
		}
		return user
	}
	if user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Admin only.")
		return nil
	}
	return user
}

// loadOrSeed returns the singleton row, creating it from SeedAddress on first
// read (with a matching history row, so the trail starts at the seed).
func (h *FeeWalletHandler) loadOrSeed(c *gin.Context) (*models.FeeWalletSetting, error) {
	db := h.DB.WithContext(c.Request.Context())
	var row models.FeeWalletSetting
	err := db.Order("created_at ASC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = models.FeeWalletSetting{
			Address: strings.TrimSpace(h.SeedAddress),
			Note:    "Seeded from SYSTEM_WALLET_ADDRESS",
		}
		if err := db.Create(&row).Error; err != nil {
			return nil, err
		}
		db.Create(&models.FeeWalletAssignment{
			Address: row.Address,
			Note:    row.Note,
		})
		return &row, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// onChainChecks reports what each enabled escrow pays fees to. Never an error
// path for the caller: an unconfigured or unreachable chain still produces a
// row, carrying the reason it couldn't be read.
func (h *FeeWalletHandler) onChainChecks(assigned string) []gin.H {
	if h.Escrows == nil {
		return []gin.H{}
	}
	sinks := h.Escrows.SystemWallets()
	out := make([]gin.H, 0, len(sinks))
	for _, s := range sinks {
		row := gin.H{
			"chainId":  s.ChainID,
			"chainKey": s.ChainKey,
			"contract": s.Contract,
		}
		if s.SystemWallet == "" {
			row["verified"] = false
			row["reason"] = s.Reason
		} else {
			row["verified"] = true
			row["systemWallet"] = s.SystemWallet
			// The assignment is only real for this contract once it agrees.
			row["matches"] = strings.EqualFold(strings.TrimSpace(s.SystemWallet), strings.TrimSpace(assigned))
		}
		out = append(out, row)
	}
	return out
}

// recentAssignments returns the newest history rows with the assigning admin's
// username resolved, batched into one lookup.
func (h *FeeWalletHandler) recentAssignments(c *gin.Context) []gin.H {
	var rows []models.FeeWalletAssignment
	if err := h.DB.WithContext(c.Request.Context()).
		Order("created_at DESC").Limit(feeWalletHistoryLimit).Find(&rows).Error; err != nil {
		return []gin.H{}
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		if r.AssignedBy != nil {
			ids = append(ids, *r.AssignedBy)
		}
	}
	names := h.usernamesByID(c, ids)

	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		name := ""
		if r.AssignedBy != nil {
			name = names[*r.AssignedBy]
		}
		out = append(out, gin.H{
			"address":         r.Address,
			"previousAddress": r.PreviousAddress,
			"assignedBy":      r.AssignedBy,
			"assignedByName":  name,
			"note":            r.Note,
			"createdAt":       r.CreatedAt,
		})
	}
	return out
}

// feeWalletHistoryLimit bounds the history the panel loads — it is a recent-
// changes list, not an audit export.
const feeWalletHistoryLimit = 20

// usernamesByID resolves user IDs to usernames in one query. Missing/deleted
// users simply have no entry.
func (h *FeeWalletHandler) usernamesByID(c *gin.Context, ids []uuid.UUID) map[uuid.UUID]string {
	names := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return names
	}
	var users []models.User
	if err := h.DB.WithContext(c.Request.Context()).
		Select("id", "username").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return names
	}
	for _, u := range users {
		names[u.ID] = u.Username
	}
	return names
}

// respond writes the settings row plus the assigning admin's username, the
// per-contract on-chain comparison, and the recent assignment history.
func (h *FeeWalletHandler) respond(c *gin.Context, status int, msg string, row *models.FeeWalletSetting) {
	username := ""
	if row.UpdatedBy != nil {
		username = h.usernamesByID(c, []uuid.UUID{*row.UpdatedBy})[*row.UpdatedBy]
	}
	utils.SuccessResponse(c, status, msg, gin.H{
		"address":         row.Address,
		"note":            row.Note,
		"updatedAt":       row.UpdatedAt,
		"updatedBy":       row.UpdatedBy,
		"updatedByName":   username,
		"configuredValue": h.SeedAddress,
		"onChain":         h.onChainChecks(row.Address),
		"history":         h.recentAssignments(c),
	})
}

// GetFeeWallet handles GET /api/admin/fees/wallet (AdminAuth + RequireP2PAdmin).
func (h *FeeWalletHandler) GetFeeWallet(c *gin.Context) {
	if h.requireAdmin(c, false) == nil {
		return
	}
	row, err := h.loadOrSeed(c)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load the fee wallet")
		return
	}
	h.respond(c, http.StatusOK, "Fee wallet", row)
}

type updateFeeWalletRequest struct {
	Address string `json:"address"`
	Note    string `json:"note"`
}

// UpdateFeeWallet handles PUT /api/admin/fees/wallet (AdminAuth +
// RequireSuperAdmin). Re-pointing where every section's fees are meant to land
// is money routing, so a plain admin can read the panel but not reassign it.
// The change and the address it replaced are appended to the history in the
// same transaction as the update.
func (h *FeeWalletHandler) UpdateFeeWallet(c *gin.Context) {
	admin := h.requireAdmin(c, true)
	if admin == nil {
		return
	}
	var req updateFeeWalletRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	addr := strings.TrimSpace(req.Address)
	if !models.ValidEVMAddress(addr) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Enter a valid 0x wallet address")
		return
	}
	note := strings.TrimSpace(req.Note)

	row, err := h.loadOrSeed(c)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load the fee wallet")
		return
	}
	previous := row.Address

	// One transaction: the pointer and its history entry must not diverge —
	// a saved reassignment with no trail is the thing this history exists to
	// prevent.
	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.FeeWalletSetting{}).Where("id = ?", row.ID).
			Updates(map[string]interface{}{
				"address":    addr,
				"note":       note,
				"updated_by": admin.ID,
			}).Error; err != nil {
			return err
		}
		return tx.Create(&models.FeeWalletAssignment{
			Address:         addr,
			PreviousAddress: previous,
			AssignedBy:      &admin.ID,
			Note:            note,
		}).Error
	})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to save the fee wallet")
		return
	}

	fresh, err := h.loadOrSeed(c)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Saved, but failed to reload the fee wallet")
		return
	}
	h.respond(c, http.StatusOK, "Fee wallet updated", fresh)
}
