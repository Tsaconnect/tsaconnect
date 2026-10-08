package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// Reconciliation routes on a merchant owner's own account: payments by day,
// period totals, the paid-payments CSV and a webhook sent again.

// payOwnerDateRange reads ?from&to (YYYY-MM-DD days in WAT, to inclusive) as
// [start, end). No dates is no range (zero times). false means the response
// has been written.
func payOwnerDateRange(c *gin.Context) (time.Time, time.Time, bool) {
	start, end, err := services.ParsePayDateRange(c.Query("from"), c.Query("to"))
	if err != nil {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Choose both dates as YYYY-MM-DD, with the end on or after the start", "date_range")
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// GET /api/pay/merchant/payments/summary?mode&from&to — the period's totals:
// what was paid in it (by the day it was paid), how the payments made in it
// ended, and the refunds confirmed in it. No dates: all time.
func (h *PayHandler) OwnerPaymentSummary(c *gin.Context) {
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return
	}
	mode, ok := payOwnerMode(c)
	if !ok {
		return
	}
	start, end, ok := payOwnerDateRange(c)
	if !ok {
		return
	}
	sum, err := h.payments.Summary(c.Request.Context(), m.ID, mode, start, end)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Summary", sum)
}

// GET /api/pay/merchant/payments/export?mode&from&to — every payment paid in
// the range as a CSV download (services.ExportCSV). Refusals come before the
// first byte, as JSON, and so does a failure reading the first chunk. Once the
// file has started, a failure is logged and the connection aborted
// (http.ErrAbortHandler, passed on by middleware.Recover): the download then
// fails, rather than ending cleanly as a short file that looks complete.
func (h *PayHandler) OwnerExportPayments(c *gin.Context) {
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return
	}
	mode, ok := payOwnerMode(c)
	if !ok {
		return
	}
	start, end, ok := payOwnerDateRange(c)
	if !ok {
		return
	}
	file := &payCSVDownload{c: c, name: fmt.Sprintf("tsa-pay-%s-%s-%s.csv", mode,
		start.Format("2006-01-02"), end.AddDate(0, 0, -1).Format("2006-01-02"))}
	err := h.payments.ExportCSV(c.Request.Context(), file, m.ID, mode, start, end)
	switch {
	case err == nil:
	case file.started:
		log.Printf("[tsa-pay] export for merchant %s stopped part-way, aborting: %v", m.ID, err)
		panic(http.ErrAbortHandler)
	case errors.Is(err, services.ErrPayDateRange):
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Choose a date range of at most 366 days", "date_range")
	case errors.Is(err, services.ErrPayExportTooLarge):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "That's more than 10,000 payments. Narrow the date range.", "export_too_large")
	default:
		payOwnerFail(c, err)
	}
}

// payCSVDownload is an export's response body. The download headers go out
// with its first byte, so a refusal before then can still answer JSON.
type payCSVDownload struct {
	c       *gin.Context
	name    string
	started bool
}

func (d *payCSVDownload) Write(b []byte) (int, error) {
	if !d.started {
		d.started = true
		h := d.c.Writer.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+d.name+`"`)
		h.Set("X-Content-Type-Options", "nosniff")
		d.c.Status(http.StatusOK)
	}
	return d.c.Writer.Write(b)
}

// Flush sends what has been written so far, so a long export streams.
func (d *payCSVDownload) Flush() { d.c.Writer.Flush() }

// POST /api/pay/merchant/deliveries/:id/resend?mode — the owner sends one of
// their webhooks again. One that never arrived is retried now. One that did is
// sent anew, under a new delivery id with the payment as it is now, and its
// record is kept (services.ResendForMerchant). Another merchant's delivery, or
// one in the other mode, is not found.
func (h *PayHandler) OwnerResendDelivery(c *gin.Context) {
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return
	}
	mode, ok := payOwnerMode(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Webhook delivery not found")
		return
	}
	if h.webhooks == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Webhooks aren't available right now")
		return
	}
	d, err := h.webhooks.ResendForMerchant(c.Request.Context(), m.ID, mode, id)
	switch {
	case err == nil:
		utils.SuccessResponse(c, http.StatusOK, "Webhook queued", gin.H{"delivery": payOwnerDelivery(d)})
	case errors.Is(err, services.ErrPayDeliveryNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Webhook delivery not found")
	case errors.Is(err, services.ErrPayWebhookNoURL):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "Add a webhook URL for this mode under API keys & webhooks first", "no_webhook_url")
	default:
		payOwnerFail(c, err)
	}
}
