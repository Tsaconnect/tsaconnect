package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// FeeWalletHandler serves the admin portal's "fee wallet" surface: which wallet
// the platform's system fees are assigned to, who assigned it, and — when the
// chain is reachable — whether the deployed P2P escrow actually pays that
// address.
//
// Assignment here is a RECORD, not enforcement. The escrow/swap contracts hold
// their own `systemWallet` and only their owner can change it
// (P2PEscrow.setSystemWallet). Pointing the contracts at a newly assigned
// address is a separate owner transaction; this handler surfaces the mismatch
// so it can't go unnoticed.
type FeeWalletHandler struct {
	DB *gorm.DB
	// SeedAddress is config.SystemWalletAddress — the deployed default used to
	// seed the singleton on first read, so a fresh DB never shows "unset".
	SeedAddress string
	// Escrow is optional; when present the GET reports the on-chain
	// systemWallet for comparison. A nil service (or an RPC failure) degrades
	// to "unverified" rather than failing the request.
	Escrow SystemWalletReader
}

// SystemWalletReader is the slice of the escrow service this handler needs —
// declared here so the handler doesn't depend on the whole service (and so
// tests can supply a stub).
type SystemWalletReader interface {
	ContractAddress() string
	ReadSystemWallet() (string, error)
}

// NewFeeWalletHandler creates a FeeWalletHandler. escrow may be nil.
func NewFeeWalletHandler(db *gorm.DB, seedAddress string, escrow SystemWalletReader) *FeeWalletHandler {
	return &FeeWalletHandler{DB: db, SeedAddress: seedAddress, Escrow: escrow}
}

// loadOrSeed returns the singleton row, creating it from SeedAddress on first
// read.
func (h *FeeWalletHandler) loadOrSeed() (*models.FeeWalletSetting, error) {
	var row models.FeeWalletSetting
	err := h.DB.Order("created_at ASC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = models.FeeWalletSetting{
			Address: strings.TrimSpace(h.SeedAddress),
			Note:    "Seeded from SYSTEM_WALLET_ADDRESS",
		}
		if err := h.DB.Create(&row).Error; err != nil {
			return nil, err
		}
		return &row, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// onChainCheck reports what the deployed escrow pays fees to. Never an error
// path for the caller: an unconfigured or unreachable chain yields a nil block
// and a human-readable reason.
func (h *FeeWalletHandler) onChainCheck(assigned string) gin.H {
	if h.Escrow == nil || h.Escrow.ContractAddress() == "" {
		return gin.H{"verified": false, "reason": "No escrow contract configured on this deployment"}
	}
	addr, err := h.Escrow.ReadSystemWallet()
	if err != nil || addr == "" {
		return gin.H{
			"verified": false,
			"contract": h.Escrow.ContractAddress(),
			"reason":   "Could not read systemWallet from the escrow contract",
		}
	}
	return gin.H{
		"verified": true,
		"contract": h.Escrow.ContractAddress(),
		"address":  addr,
		// The assignment is only real once the contract agrees.
		"matches": strings.EqualFold(strings.TrimSpace(addr), strings.TrimSpace(assigned)),
	}
}

// respond writes the settings row plus the assigning admin's username and the
// on-chain comparison.
func (h *FeeWalletHandler) respond(c *gin.Context, status int, msg string, row *models.FeeWalletSetting) {
	username := ""
	if row.UpdatedBy != nil {
		var u models.User
		if err := h.DB.Select("username").First(&u, "id = ?", *row.UpdatedBy).Error; err == nil {
			username = u.Username
		}
	}
	utils.SuccessResponse(c, status, msg, gin.H{
		"address":         row.Address,
		"note":            row.Note,
		"updatedAt":       row.UpdatedAt,
		"updatedBy":       row.UpdatedBy,
		"updatedByName":   username,
		"configuredValue": h.SeedAddress,
		"onChain":         h.onChainCheck(row.Address),
	})
}

// GetFeeWallet handles GET /api/admin/fees/wallet (AdminAuth + RequireP2PAdmin).
func (h *FeeWalletHandler) GetFeeWallet(c *gin.Context) {
	row, err := h.loadOrSeed()
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
func (h *FeeWalletHandler) UpdateFeeWallet(c *gin.Context) {
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

	row, err := h.loadOrSeed()
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load the fee wallet")
		return
	}

	updates := map[string]interface{}{
		"address": addr,
		"note":    strings.TrimSpace(req.Note),
	}
	if admin := getUserFromContext(c); admin != nil {
		id := admin.ID
		updates["updated_by"] = id
		row.UpdatedBy = &id
	} else {
		// Route middleware guarantees a user; belt-and-braces so an unattributed
		// change is recorded as such rather than keeping the previous admin.
		updates["updated_by"] = (*uuid.UUID)(nil)
		row.UpdatedBy = nil
	}

	if err := h.DB.Model(&models.FeeWalletSetting{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to save the fee wallet")
		return
	}

	fresh, err := h.loadOrSeed()
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Saved, but failed to reload the fee wallet")
		return
	}
	h.respond(c, http.StatusOK, "Fee wallet updated", fresh)
}
