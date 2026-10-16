package handlers

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// SetPayers wires payer declarations (inbound_payer.go). Unset, both routes
// answer 503 rather than disappearing, so a client never reads a 404 as "no
// such order".
func (h *InboundAdminHandler) SetPayers(p *services.InboundPayerService) { h.payers = p }

type declarePayerRequest struct {
	OrderID       string `json:"orderId"`
	BankCode      string `json:"bankCode"`
	AccountNumber string `json:"accountNumber"`
}

// POST /api/inbound/payer {orderId, bankCode, accountNumber} — the account
// name comes from the bank (an accountName in the body is ignored).
//
// The buyer says which account paid for their order when it was not one in
// their own name. 200 with `status`: matched (settled now), waiting (nothing
// has arrived yet — it settles when it does) or review (an admin confirms).
func (h *InboundAdminHandler) DeclarePayer(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if h.payers == nil {
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "This isn't available right now. Try again later.", "payer_unavailable")
		return
	}
	var req declarePayerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Enter the bank and account number.", "payer_bad_input")
		return
	}
	orderID, err := uuid.Parse(req.OrderID)
	if err != nil {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Invalid order.", "payer_bad_input")
		return
	}

	res, err := h.payers.Declare(c.Request.Context(), user.ID, orderID, req.BankCode, req.AccountNumber)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrPayerBadInput):
			utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Enter the bank and a 10-digit account number.", "payer_bad_input")
		case errors.Is(err, services.ErrPayerUnknownBank):
			utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Choose the bank from the list.", "payer_unknown_bank")
		case errors.Is(err, services.ErrPayerAccountNotFound):
			utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "The bank couldn't find that account number.", "payer_account_not_found")
		case errors.Is(err, services.ErrPayerClaimNotFound):
			utils.ErrorResponseWithCode(c, http.StatusNotFound, "This order isn't waiting for a payment.", "payer_order_not_open")
		case errors.Is(err, services.ErrPayerAlreadyMatched):
			utils.ErrorResponseWithCode(c, http.StatusConflict, "This order's payment is already confirmed.", "payer_already_matched")
		case errors.Is(err, services.ErrKYCVerificationRequired), errors.Is(err, services.ErrKYCLegalNameMissing):
			utils.ErrorResponseWithCode(c, http.StatusForbidden, "Verify your identity first.", "kyc_verification_required")
		case errors.Is(err, services.ErrPayerLookupFailed):
			log.Printf("[inbound-payer] name enquiry failed: %s", payerLogSafe(err))
			utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "We couldn't reach the bank. Try again in a moment.", "payer_unavailable")
		case errors.Is(err, services.ErrPayerUnavailable):
			log.Printf("[inbound-payer] unavailable: %s", payerLogSafe(err))
			utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "This isn't available right now. Try again in a moment.", "payer_unavailable")
		default:
			log.Printf("[inbound-payer] declare failed: %s", payerLogSafe(err))
			utils.ErrorResponseWithCode(c, http.StatusInternalServerError, "Something went wrong. Try again in a moment.", "payer_failed")
		}
		return
	}

	msg := map[string]string{
		models.PayerDeclarationMatched: "Payment found and confirmed.",
		models.PayerDeclarationWaiting: "Saved. Your payment will be confirmed as soon as it arrives.",
		models.PayerDeclarationReview:  "Saved. Our team will confirm your payment shortly.",
	}[res.Outcome]
	utils.SuccessResponse(c, http.StatusOK, msg, payerDeclarationView(res.Declaration, false))
}

// GET /api/admin/inbound/declarations?status=&limit=&offset= (status: waiting|matched|review|closed)
func (h *InboundAdminHandler) ListPayerDeclarations(c *gin.Context) {
	if h.payers == nil {
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "Payer declarations are not wired.", "payer_unavailable")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	rows, err := h.payers.Declarations(c.Request.Context(), c.Query("status"), limit, offset)
	if err != nil {
		if errors.Is(err, services.ErrInboundBadRequest) {
			utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
			return
		}
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not load declarations")
		return
	}
	out := make([]gin.H, 0, len(rows))
	for i := range rows {
		out = append(out, payerDeclarationView(&rows[i], true))
	}
	utils.SuccessResponse(c, http.StatusOK, "Payer declarations", out)
}

// payerDeclarationView never returns the full account number to the buyer —
// they typed it — and shows the admin the whole record.
func payerDeclarationView(d *models.InboundPayerDeclaration, admin bool) gin.H {
	v := gin.H{
		"id":            d.ID,
		"orderId":       d.ClaimID,
		"claimType":     d.ClaimType,
		"status":        d.Status,
		"bankCode":      d.BankCode,
		"bankName":      d.BankName,
		"accountNumber": payMaskAccount(d.AccountNumber),
		"accountName":   d.ResolvedName,
		"amountKobo":    d.AmountKobo,
		"createdAt":     d.CreatedAt,
	}
	if d.MatchedAt != nil {
		v["matchedAt"] = d.MatchedAt
	}
	if admin {
		v["userId"] = d.UserID
		v["accountNumber"] = d.AccountNumber
		v["typedName"] = d.TypedName
		v["reviewReason"] = d.ReviewReason
		if d.CreditID != nil {
			v["creditId"] = d.CreditID
		}
	}
	return v
}

// payerDigitRun is any run of six or more digits: account and phone numbers.
var payerDigitRun = regexp.MustCompile(`\d{6,}`)

// payerLogSafe is an error as it may appear in the log. A name-enquiry failure
// wraps the bank provider's own text, which can carry the account number; the
// shared redaction covers credentials and URLs, and long digit runs keep only
// their last four.
func payerLogSafe(err error) string {
	out := services.RedactAlertText(err.Error())
	return payerDigitRun.ReplaceAllStringFunc(out, func(d string) string {
		return strings.Repeat("*", len(d)-4) + d[len(d)-4:]
	})
}
