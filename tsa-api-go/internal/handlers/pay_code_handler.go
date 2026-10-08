package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// payCodeNotFoundMessage answers every code that names nothing the caller may
// see — malformed or unknown, live or test — so none can be told apart.
const payCodeNotFoundMessage = "We couldn't find that code. Check it and try again."

// PayCodeGuard answers a :code no payment could have exactly as an unknown
// one, before any lookup.
func PayCodeGuard(c *gin.Context) {
	if _, ok := services.NormalizePayCode(c.Param("code")); !ok {
		utils.ErrorResponse(c, http.StatusNotFound, payCodeNotFoundMessage)
		c.Abort()
	}
}

// GET /api/pay/code/:code — the payment a code names, for the app's TSA Pay
// screen, which then opens the ordinary checkout by id.
func (h *PayHandler) CodeLookup(c *gin.Context) {
	id, err := h.payments.IDByCode(c.Request.Context(), c.Param("code"))
	if err != nil {
		payCodeFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment", gin.H{"id": id})
}

// GET /api/pay/public/receipt/:code — a succeeded live payment's public
// receipt, for tsaconnectworld.com/r/<code>. No session.
func (h *PayHandler) PublicReceipt(c *gin.Context) {
	v, err := h.payments.PublicReceipt(c.Request.Context(), c.Param("code"))
	if err != nil {
		payCodeFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Receipt", v)
}

func payCodeFail(c *gin.Context, err error) {
	if errors.Is(err, services.ErrPayCodeNotFound) {
		utils.ErrorResponse(c, http.StatusNotFound, payCodeNotFoundMessage)
		return
	}
	payPaymentFail(c, err)
}
