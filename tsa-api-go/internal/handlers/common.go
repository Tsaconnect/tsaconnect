package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
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
// The account's registered address is the only one these endpoints ever use.
// `claimed` is the address the DEVICE says it holds a signing key for, and this
// is where the two are reconciled — it is never substituted for the registered
// one, only checked against it.
//
// Reconciling matters because the two can disagree, and both failure modes were
// silent before. A device with no wallet imported sent nothing and got the
// registered address's balance back: a number the user cannot spend, rendered
// as if the platform were holding their MCGP. A device holding a DIFFERENT
// wallet got a prepared tx built for an address it has no key for, which then
// fails at broadcast with a nonce/sender error that names neither address.
//
// A mismatch is 409, not 400: the request is well-formed, it is the client's
// wallet state that conflicts with the account's. The app keys on that status
// to prompt for the right seed phrase.
func callerWallet(c *gin.Context, user *models.User, claimed string) (string, bool) {
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return "", false
	}

	claimed = strings.TrimSpace(claimed)
	if claimed == "" {
		// Older app builds send no address. They keep the pre-existing
		// behaviour rather than breaking mid-rollout; the current app always
		// sends one, so it never reaches this branch.
		return user.WalletAddress, true
	}

	if !isValidEthAddress(claimed) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid wallet address")
		return "", false
	}
	if !strings.EqualFold(claimed, user.WalletAddress) {
		utils.ErrorResponse(c, http.StatusConflict,
			"This device is using a different wallet than the one registered to your account. "+
				"Switch to the registered wallet, or import its recovery phrase, to continue.")
		return "", false
	}
	return user.WalletAddress, true
}
