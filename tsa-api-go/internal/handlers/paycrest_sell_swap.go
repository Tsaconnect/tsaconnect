package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// paycrest_sell_swap.go — GET /api/paycrest/sell-swap-quote.
//
// She holds a token Paycrest cannot offramp. This prices routing it through
// LiFi into USDC on Base, in her own wallet, and offramping that. It is a
// price and nothing else: no order, no row, no signature, nothing moved.

// sellQuoter is the port *services.RampSellQuoter fills. Narrow on purpose —
// the handler's job is to say who is asking and where the USDC lands.
type sellQuoter interface {
	Quote(ctx context.Context, in services.SellQuoteInput) (*services.SellSwapQuote, error)
}

// WithSellQuoter turns the route on. Nil (RAMP_CONVERSION_SELL unset) leaves
// it unregistered, so a deploy that does not set the flag behaves exactly as
// it does today.
func (h *PaycrestHandler) WithSellQuoter(q sellQuoter) *PaycrestHandler {
	h.sellQuotes = q
	return h
}

func (h *PaycrestHandler) SellQuoteEnabled() bool { return h != nil && h.sellQuotes != nil }

func (h *PaycrestHandler) SellSwapQuote(c *gin.Context) {
	if !h.SellQuoteEnabled() {
		utils.ErrorResponse(c, http.StatusNotFound, "Selling this token isn't available")
		return
	}
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Sign in to get a quote")
		return
	}
	chainID, err := strconv.ParseInt(strings.TrimSpace(c.Query("fromChainId")), 10, 64)
	if err != nil || chainID <= 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "fromChainId is required")
		return
	}
	fromToken := strings.TrimSpace(c.Query("fromToken"))
	fromAmount := strings.TrimSpace(c.Query("fromAmount"))
	fromAddress := strings.TrimSpace(c.Query("fromAddress"))
	if fromToken == "" || fromAmount == "" || fromAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "fromToken, fromAmount and fromAddress are required")
		return
	}
	// The USDC receiver is the account's own reconciled wallet, never the
	// query value on its own: a device holding another key must not be able
	// to have a sale delivered to it. fromAddress is different — it is where
	// the swap is SENT from, which may be a Solana address, and the quoter
	// validates it for that chain's family.
	wallet, ok := callerWallet(c, user, c.Query("walletAddress"))
	if !ok {
		return
	}
	res, err := h.sellQuotes.Quote(c.Request.Context(), services.SellQuoteInput{
		FromChainID: chainID, FromToken: fromToken, FromAmount: fromAmount,
		FromAddress: fromAddress, WalletAddress: wallet,
		FiatCode: c.DefaultQuery("currency", "NGN"),
	})
	if err != nil {
		sellSwapError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sell quote", res)
}

// sellSwapError separates "this token, right now, costs too much" from "we
// cannot price this at all". The first is a real answer and she should pick
// another token or wait; the second is ours to fix.
func sellSwapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrSellRouteTooCostly):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity,
			"This token can't be sold for naira right now — try again later or pick another")
	case errors.Is(err, services.ErrSellUnavailable):
		utils.ErrorResponse(c, http.StatusServiceUnavailable,
			"Selling this token for naira isn't available right now")
	case errors.Is(err, services.ErrSellSourceUnsupported):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity,
			"Selling from this network isn't supported yet — pick a coin on another network")
	case errors.Is(err, services.ErrSellWalletMCGP):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, services.ErrSellWalletMCGP.Error())
	case errors.Is(err, services.ErrSellBadSourceAddress):
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrLiFiNoRoute):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity,
			"No route can swap that token right now — try a different amount or token")
	default:
		paycrestError(c, err)
	}
}
