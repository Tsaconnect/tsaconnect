package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// firstNonEmpty returns a if non-empty, else b.
func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// Booking requests (unified booking engine, Phase 2 — Request & Approval).
//
// When a booking service's BookingType is not "instant", the buyer's submitted
// form creates a BookingRequest (off-chain, no payment) instead of an escrowed
// order. The merchant approves or rejects it; on approval the buyer pays, which
// converts the request into a normal service Order and runs the existing escrow
// flow. This keeps the productAmount>0 escrow invariant intact — an Order only
// exists once a positive amount is due.

type createBookingRequestBody struct {
	Token        string                 `json:"token" binding:"required"`
	ServiceTier  string                 `json:"serviceTier"`
	BuyerCity    string                 `json:"buyerCity"`
	BuyerState   string                 `json:"buyerState"`
	BuyerCountry string                 `json:"buyerCountry"`
	Quantity     int                    `json:"quantity"`
	Answers      map[string]interface{} `json:"answers"`
}

// CreateBookingRequest handles POST /api/services/:id/requests — a buyer submits
// a booking request for a non-instant booking service.
func (ch *CheckoutHandler) CreateBookingRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	serviceID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}
	var req createBookingRequestBody
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "token is required")
		return
	}
	token := strings.ToUpper(req.Token)
	if token != "USDC" && token != "USDT" && token != "MCGP" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported token. Use USDC, USDT, or MCGP")
		return
	}
	// An MCGP booking is paid from Spendable MCGP; don't take a request the
	// buyer could not pay.
	if ch.refuseMcgpUnavailable(c, token) {
		return
	}

	var product models.Product
	if err := config.DB.First(&product, "id = ?", serviceID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Service not found")
		return
	}
	if product.Type != models.ProductTypeService {
		utils.ErrorResponse(c, http.StatusBadRequest, "Not a service listing")
		return
	}
	if product.UserID == user.ID {
		utils.ErrorResponse(c, http.StatusBadRequest, "You cannot book your own service")
		return
	}

	serviceType, profile := resolveServiceTypeAndProfile(config.DB, &product)
	if serviceType != models.ServiceTypeBooking {
		utils.ErrorResponse(c, http.StatusBadRequest, "Booking requests apply to booking services only")
		return
	}
	if profile.BookingType == models.BookingTypeInstant || profile.BookingType == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "This service accepts instant bookings — check out directly")
		return
	}
	form, _ := serviceBookingForm(config.DB, &product, &profile)
	if err := models.ValidateAnswers(form, req.Answers); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	units, uerr := bookingUnits(config.DB, &product, serviceType, req.Quantity)
	if uerr != "" {
		utils.ErrorResponse(c, http.StatusBadRequest, uerr)
		return
	}

	request := models.BookingRequest{
		ID:           uuid.New(),
		ProductID:    product.ID,
		BuyerID:      user.ID,
		SellerID:     product.UserID,
		Token:        token,
		ServiceTier:  req.ServiceTier,
		Quantity:     units,
		BuyerCity:    firstNonEmpty(req.BuyerCity, user.City),
		BuyerState:   firstNonEmpty(req.BuyerState, user.State),
		BuyerCountry: firstNonEmpty(req.BuyerCountry, user.Country),
		Status:       models.BookingRequestRequested,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	request.SetAnswers(req.Answers)
	if err := config.DB.Create(&request).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create booking request")
		return
	}

	shortID := request.ID.String()[:8]
	ch.EventBus.Publish(events.Event{
		Type: events.OrderPlaced, UserID: product.UserID, Title: "New Booking Request",
		Message: fmt.Sprintf("You have a new booking request #%s for %s. Review to approve or decline.", shortID, product.Name),
		Data:    map[string]interface{}{"bookingRequestId": request.ID.String(), "status": request.Status, "role": "seller"},
	})
	utils.SuccessResponse(c, http.StatusCreated, "Booking request submitted", request)
}

// ListBookingRequests handles GET /api/services/requests
//
// Query params:
//   - role=buyer (default) | seller
//   - status (optional) — filter by BookingRequest status
//   - page (default 1), limit (default 20, capped at 100)
func (ch *CheckoutHandler) ListBookingRequests(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	role := strings.ToLower(strings.TrimSpace(c.DefaultQuery("role", "buyer")))

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	// scoped applies the role + optional status filters, so the count and the
	// page query share identical conditions.
	scoped := func() *gorm.DB {
		q := config.DB.Model(&models.BookingRequest{})
		if role == "seller" {
			q = q.Where("seller_id = ?", user.ID)
		} else {
			q = q.Where("buyer_id = ?", user.ID)
		}
		if status := strings.TrimSpace(c.Query("status")); status != "" {
			q = q.Where("status = ?", status)
		}
		return q
	}

	var total int64
	if err := scoped().Count(&total).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to count booking requests")
		return
	}

	var requests []models.BookingRequest
	if err := scoped().
		Order("created_at DESC").
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&requests).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load booking requests")
		return
	}

	// Enrich each request with its service name (cheap single lookup per distinct
	// product) so the list is renderable without an extra client round-trip.
	names := map[uuid.UUID]string{}
	out := make([]gin.H, 0, len(requests))
	for _, r := range requests {
		name, ok := names[r.ProductID]
		if !ok {
			var p models.Product
			if err := config.DB.Select("name").First(&p, "id = ?", r.ProductID).Error; err == nil {
				name = p.Name
			}
			names[r.ProductID] = name
		}
		out = append(out, gin.H{"request": r, "serviceName": name})
	}
	utils.SuccessResponse(c, http.StatusOK, "Booking requests fetched", gin.H{
		"requests": out,
		"pagination": gin.H{
			"page":  page,
			"limit": limit,
			"total": total,
		},
	})
}

// loadOwnedRequest loads a booking request and enforces that the caller plays
// the expected role (buyer or seller) on it.
func loadBookingRequest(c *gin.Context, userID uuid.UUID, asSeller bool) (*models.BookingRequest, bool) {
	reqID, err := uuid.Parse(c.Param("reqId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request ID")
		return nil, false
	}
	var r models.BookingRequest
	if err := config.DB.First(&r, "id = ?", reqID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "Booking request not found")
		} else {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Database error")
		}
		return nil, false
	}
	if asSeller && r.SellerID != userID {
		utils.ErrorResponse(c, http.StatusForbidden, "Not your service")
		return nil, false
	}
	if !asSeller && r.BuyerID != userID {
		utils.ErrorResponse(c, http.StatusForbidden, "Not your booking request")
		return nil, false
	}
	return &r, true
}

// ApproveBookingRequest handles POST /api/services/requests/:reqId/approve (seller).
func (ch *CheckoutHandler) ApproveBookingRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	r, ok := loadBookingRequest(c, user.ID, true)
	if !ok {
		return
	}
	if !models.CanTransitionBookingRequest(r.Status, models.BookingRequestApproved) {
		utils.ErrorResponse(c, http.StatusConflict, fmt.Sprintf("Cannot approve a %s request", r.Status))
		return
	}

	var body struct {
		AmountUSD *float64 `json:"amountUsd"`
	}
	_ = c.ShouldBindJSON(&body)

	// Price-on-request listings require the provider to name a price at approval.
	// Fail closed if the profile can't be loaded: silently proceeding would
	// leave PricingMethod == "" and approve an on_request booking with no price,
	// so the buyer would later escrow the base fare instead of the named price.
	var profile models.ServiceProfile
	if err := config.DB.Where("product_id = ?", r.ProductID).First(&profile).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load service listing")
		return
	}
	if profile.PricingMethod == models.PricingMethodOnRequest {
		if body.AmountUSD == nil || *body.AmountUSD <= 0 {
			utils.ErrorResponse(c, http.StatusBadRequest, "Set a price to approve this request")
			return
		}
		r.QuotedAmountUSD = body.AmountUSD
	}

	r.Status = models.BookingRequestApproved
	r.UpdatedAt = time.Now()
	if err := config.DB.Save(r).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to approve request")
		return
	}
	ch.EventBus.Publish(events.Event{
		Type: events.OrderPlaced, UserID: r.BuyerID, Title: "Booking Approved",
		Message: "Your booking request was approved. Complete payment to confirm.",
		Data:    map[string]interface{}{"bookingRequestId": r.ID.String(), "status": r.Status, "role": "buyer"},
	})
	utils.SuccessResponse(c, http.StatusOK, "Booking request approved", r)
}

type rejectBookingRequestBody struct {
	Reason string `json:"reason"`
}

// maxBookingReasonLen bounds the free-text reason a party can attach when
// declining or cancelling. It lands in an unbounded TEXT column, so without a
// cap a crafted client could persist arbitrarily large rows — the same reason
// models.ValidateAnswers bounds booking-form answers.
const maxBookingReasonLen = 500

// reasonOrError returns the trimmed reason, or false after writing a 400 when
// it exceeds the cap.
func reasonOrError(c *gin.Context, raw string) (string, bool) {
	reason := strings.TrimSpace(raw)
	if len(reason) > maxBookingReasonLen {
		utils.ErrorResponse(c, http.StatusBadRequest,
			fmt.Sprintf("Reason is too long (max %d characters)", maxBookingReasonLen))
		return "", false
	}
	return reason, true
}

// RejectBookingRequest handles POST /api/services/requests/:reqId/reject (seller).
func (ch *CheckoutHandler) RejectBookingRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	r, ok := loadBookingRequest(c, user.ID, true)
	if !ok {
		return
	}
	if !models.CanTransitionBookingRequest(r.Status, models.BookingRequestRejected) {
		utils.ErrorResponse(c, http.StatusConflict, fmt.Sprintf("Cannot decline a %s request", r.Status))
		return
	}
	var body rejectBookingRequestBody
	_ = c.ShouldBindJSON(&body)
	reason, ok := reasonOrError(c, body.Reason)
	if !ok {
		return
	}
	r.Status = models.BookingRequestRejected
	r.RejectReason = reason
	r.UpdatedAt = time.Now()
	if err := config.DB.Save(r).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decline request")
		return
	}
	ch.EventBus.Publish(events.Event{
		Type: events.OrderPlaced, UserID: r.BuyerID, Title: "Booking Declined",
		Message: "Your booking request was declined by the provider.",
		Data:    map[string]interface{}{"bookingRequestId": r.ID.String(), "status": r.Status, "role": "buyer"},
	})
	utils.SuccessResponse(c, http.StatusOK, "Booking request declined", r)
}

// CancelBookingRequest handles POST /api/services/requests/:reqId/cancel.
//
// Either party may cancel, which is why it does not reuse loadBookingRequest's
// single-role check: the buyer withdraws a request they no longer want, and the
// provider backs out of one they already approved. Reject only covers the
// not-yet-approved case and is provider-only, so without this an approved
// booking had no exit at all — the buyer could still pay it weeks later.
func (ch *CheckoutHandler) CancelBookingRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	reqID, err := uuid.Parse(c.Param("reqId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request ID")
		return
	}
	var r models.BookingRequest
	if err := config.DB.First(&r, "id = ?", reqID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "Booking request not found")
		} else {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Database error")
		}
		return
	}
	if r.BuyerID != user.ID && r.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Not your booking request")
		return
	}
	if !models.CanTransitionBookingRequest(r.Status, models.BookingRequestCancelled) {
		utils.ErrorResponse(c, http.StatusConflict, fmt.Sprintf("Cannot cancel a %s request", r.Status))
		return
	}

	var body rejectBookingRequestBody
	_ = c.ShouldBindJSON(&body)
	reason, ok := reasonOrError(c, body.Reason)
	if !ok {
		return
	}

	// Conditional update on the status we read, so two concurrent cancels (or a
	// cancel racing a pay) cannot both win.
	claim := config.DB.Model(&models.BookingRequest{}).
		Where("id = ? AND status = ?", r.ID, r.Status).
		Updates(map[string]interface{}{
			"status":        models.BookingRequestCancelled,
			"reject_reason": reason,
			"updated_at":    time.Now(),
		})
	if claim.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to cancel request")
		return
	}
	if claim.RowsAffected == 0 {
		utils.ErrorResponse(c, http.StatusConflict, "This request is no longer cancellable")
		return
	}
	r.Status = models.BookingRequestCancelled
	r.RejectReason = reason
	r.UpdatedAt = time.Now()

	// Tell the other side, whichever side that is.
	notify, title := r.SellerID, "Booking Cancelled"
	message := "The buyer cancelled their booking request."
	if user.ID == r.SellerID {
		notify = r.BuyerID
		message = "The provider cancelled your booking request."
	}
	ch.EventBus.Publish(events.Event{
		Type: events.OrderPlaced, UserID: notify, Title: title, Message: message,
		Data: map[string]interface{}{"bookingRequestId": r.ID.String(), "status": r.Status},
	})
	utils.SuccessResponse(c, http.StatusOK, "Booking request cancelled", r)
}

// PayBookingRequest handles POST /api/services/requests/:reqId/pay (buyer) — for
// an approved request, creates the escrowed service order (converting the
// request) and returns it so the app can run the approve/escrow/submit flow —
// or, for an MCGP booking, pay it from Spendable MCGP (pay-spendable).
func (ch *CheckoutHandler) PayBookingRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	r, ok := loadBookingRequest(c, user.ID, false)
	if !ok {
		return
	}
	if !models.CanTransitionBookingRequest(r.Status, models.BookingRequestConverted) {
		utils.ErrorResponse(c, http.StatusConflict, "This request is not ready for payment")
		return
	}

	if ch.refuseMcgpUnavailable(c, strings.ToUpper(r.Token)) {
		return
	}
	rates, balance, perr := ch.ratesAndBalance(c.Request.Context(), r.Token, user.WalletAddress)
	if perr != nil {
		utils.ErrorResponse(c, perr.status, perr.msg)
		return
	}

	tx := config.DB.Begin()
	if tx.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start transaction")
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			tx.Rollback()
		}
	}()

	var product models.Product
	if err := tx.First(&product, "id = ?", r.ProductID).Error; err != nil {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusNotFound, "Service not found")
		return
	}
	var seller models.User
	if err := tx.First(&seller, "id = ?", r.SellerID).Error; err != nil {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusBadRequest, "Seller not found")
		return
	}
	serviceType, profile := resolveServiceTypeAndProfile(tx, &product)
	// Re-resolved rather than trusted: the category's mode may have changed
	// since the request, and the price must follow what the listing is now.
	units, uerr := bookingUnits(tx, &product, serviceType, r.Quantity)
	if uerr != "" {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusBadRequest, uerr)
		return
	}

	order, errMsg, status := ch.placeServiceOrder(
		tx, rates, user, &seller, &product, serviceType, &profile,
		r.Token, r.ServiceTier, r.BuyerCity, r.BuyerState, r.BuyerCountry,
		nil, nil, units, r.GetAnswers(), r.QuotedAmountUSD,
	)
	if errMsg != "" {
		tx.Rollback()
		utils.ErrorResponse(c, status, errMsg)
		return
	}
	if refuseShortfall(c, tx, strings.ToUpper(r.Token), orderPull(order, rates.gasFee), balance) {
		return
	}
	if ch.refuseSpendableLimit(c, tx, strings.ToUpper(r.Token), orderPull(order, rates.gasFee)) {
		return
	}

	// Atomically claim the payable → converted transition. The status check
	// above is only a cheap pre-flight; this conditional update is what actually
	// guards against two concurrent pay calls both creating an order (the order
	// created above is rolled back if we lose the race).
	claim := tx.Model(&models.BookingRequest{}).
		Where("id = ? AND status = ?", r.ID, models.BookingRequestApproved).
		Updates(map[string]interface{}{
			"status":     models.BookingRequestConverted,
			"order_id":   order.ID,
			"updated_at": time.Now(),
		})
	if claim.Error != nil {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to convert request")
		return
	}
	if claim.RowsAffected == 0 {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusConflict, "This request is not ready for payment")
		return
	}
	if err := tx.Commit().Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to commit transaction")
		return
	}

	shortID := order.ID.String()[:8]
	ch.EventBus.Publish(events.Event{
		Type: events.OrderPlaced, UserID: order.SellerID, Title: "Booking Payment Started",
		Message: fmt.Sprintf("Booking #%s is being paid. Awaiting escrow confirmation.", shortID),
		Data:    map[string]interface{}{"orderId": order.ID.String(), "status": order.Status, "role": "seller"},
	})
	utils.SuccessResponse(c, http.StatusCreated, "Order created — complete escrow to confirm", gin.H{"orders": []models.Order{*order}})
}
