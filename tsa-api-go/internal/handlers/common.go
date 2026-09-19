package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// getUserFromContext extracts the authenticated user from the Gin context.
func getUserFromContext(c *gin.Context) *models.User {
	user, exists := c.Get("user")
	if !exists {
		return nil
	}
	switch v := user.(type) {
	case *models.User:
		return v
	case models.User:
		return &v
	default:
		return nil
	}
}

// callerWallet resolves the wallet address an entitlement endpoint acts on.
//
// In an active-wallet / decentralized model, `claimed` is the wallet address the
// calling device currently holds active keys for. If provided and valid:
//  - It must not be in use by another active account (account_status != 'deleted').
//  - If it differs from the account's registered wallet_address (or if the account
//    has no registered wallet yet), it is dynamically synced to users.wallet_address.
//  - It is returned as the active wallet for this request.
//
// If `claimed` is omitted, the user's previously registered wallet_address is used
// as a fallback. If both are empty, the request is refused with "Set up a wallet first".
func callerWallet(c *gin.Context, user *models.User, claimed string) (string, bool) {
	claimed = strings.TrimSpace(claimed)
	if claimed != "" {
		if !isValidEthAddress(claimed) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid wallet address")
			return "", false
		}

		if config.DB != nil && config.DB.Migrator().HasTable(&models.User{}) {
			var otherUser models.User
			if err := config.DB.Where("wallet_address = ? AND account_status != ? AND id != ?",
				claimed, models.AccountStatusDeleted, user.ID).First(&otherUser).Error; err == nil {
				utils.ErrorResponse(c, http.StatusConflict, "Wallet address already in use by another account")
				return "", false
			}

			// Clean up any stale record on deleted accounts
			_ = config.DB.Model(&models.User{}).
				Where("wallet_address = ? AND account_status = ?", claimed, models.AccountStatusDeleted).
				Update("wallet_address", "").Error

			// Auto-sync user's registered wallet to match this active wallet
			if !strings.EqualFold(claimed, user.WalletAddress) {
				_ = config.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("wallet_address", claimed).Error
				user.WalletAddress = claimed
			}
		} else {
			user.WalletAddress = claimed
		}
		return claimed, true
	}

	// Fallback to previously registered address if device did not send an active wallet
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return "", false
	}
	return user.WalletAddress, true
}
