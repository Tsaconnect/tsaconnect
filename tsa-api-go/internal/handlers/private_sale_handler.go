package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// PrivateSaleHandler handles private sale submissions from the landing page.
type PrivateSaleHandler struct {
	DB           *gorm.DB
	EmailService *services.EmailService
	Config       *config.Config
}

// NewPrivateSaleHandler creates a new PrivateSaleHandler.
func NewPrivateSaleHandler(db *gorm.DB, es *services.EmailService, cfg *config.Config) *PrivateSaleHandler {
	return &PrivateSaleHandler{
		DB:           db,
		EmailService: es,
		Config:       cfg,
	}
}

// SubmitPrivateSale handles POST /api/private-sale/submit
func (h *PrivateSaleHandler) SubmitPrivateSale(c *gin.Context) {
	var req struct {
		Name   string `json:"name" binding:"required"`
		Email  string `json:"email" binding:"required,email"`
		Amount string `json:"amount" binding:"required"`
		TxHash string `json:"txHash" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "All fields are required. Please provide a valid name, email, purchase amount, and transaction hash.")
		return
	}

	// Clean and parse amount
	cleaned := strings.ReplaceAll(strings.TrimSpace(req.Amount), ",", "")
	amount, err := strconv.ParseFloat(cleaned, 64)
	if err != nil || amount < 100 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Minimum purchase amount is $100.")
		return
	}

	submission := models.PrivateSaleSubmission{
		Name:   strings.TrimSpace(req.Name),
		Email:  strings.TrimSpace(strings.ToLower(req.Email)),
		Amount: amount,
		TxHash: strings.TrimSpace(req.TxHash),
		Status: "pending",
	}

	if err := h.DB.Create(&submission).Error; err != nil {
		if strings.Contains(err.Error(), "duplicate key") ||
			strings.Contains(err.Error(), "unique") {
			utils.ErrorResponse(c, http.StatusConflict, "This transaction hash has already been submitted.")
			return
		}
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to submit your details. Please try again.")
		return
	}

	utils.SuccessResponse(c, http.StatusCreated, "Submission received. We will review and confirm your participation shortly.", submission)
}

// ListPrivateSaleSubmissions handles GET /api/admin/private-sale/submissions
func (h *PrivateSaleHandler) ListPrivateSaleSubmissions(c *gin.Context) {
	var submissions []models.PrivateSaleSubmission
	query := h.DB.Order("created_at DESC")

	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Find(&submissions).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to retrieve submissions.")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Submissions retrieved successfully.", submissions)
}

// UpdatePrivateSaleStatus handles PATCH /api/admin/private-sale/submissions/:id/status
func (h *PrivateSaleHandler) UpdatePrivateSaleStatus(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		Status string `json:"status" binding:"required,oneof=pending approved rejected"`
		Note   string `json:"note"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid status. Must be one of: pending, approved, rejected.")
		return
	}

	var submission models.PrivateSaleSubmission
	if err := h.DB.First(&submission, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Submission not found.")
		return
	}

	submission.Status = req.Status
	if err := h.DB.Save(&submission).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update submission status.")
		return
	}

	// Send email notification for approved/rejected status
	if req.Status == "approved" || req.Status == "rejected" {
		go h.sendStatusEmail(submission, req.Status, req.Note)
	}

	utils.SuccessResponse(c, http.StatusOK, "Submission status updated successfully.", submission)
}

// privateSaleEmail is one composed status notice.
type privateSaleEmail struct {
	Subject string
	Title   string
	Body    services.EmailBody
	CTAText string
	CTAURL  string
}

// privateSaleStatusEmail composes the approved/rejected notice.
//
// This is the only email in the product with structure of its own, and the two
// values it is built around are the two nobody here wrote: the submitter chose
// their name and an admin typed the note. Written as a string of literal
// markup they either arrive as visible tag text (once the template escapes
// them) or as live HTML the submitter controls (once it does not) — the body is
// composed of spans instead so the emphasis is the template's and the words
// stay words. Split from the send so a test can read what the recipient gets.
func privateSaleStatusEmail(submission models.PrivateSaleSubmission, status, note, landingURL string) privateSaleEmail {
	amount := fmt.Sprintf("$%.2f", submission.Amount)
	greeting := services.EmailLine{services.EmailText("Hi " + submission.Name + ",")}

	var msg privateSaleEmail
	if status == "approved" {
		msg = privateSaleEmail{
			Subject: "🎉 Your TSA Connect Private Share Purchase has been Approved!",
			Title:   "Purchase Approved",
			Body: services.EmailBody{
				greeting,
				{},
				{
					services.EmailText("Your private share purchase of "),
					services.EmailBold(amount),
					services.EmailText(" has been "),
					services.EmailBold("approved"),
					services.EmailText("! Thank you for investing in the TSA Connect ecosystem."),
				},
				{},
				{services.EmailText("You can track all updates and future announcements on our official channels.")},
			},
			CTAText: "Visit TSA Connect",
			CTAURL:  landingURL,
		}
	} else {
		msg = privateSaleEmail{
			Subject: "TSA Connect Private Share Purchase — Status Update",
			Title:   "Purchase Not Approved",
			Body: services.EmailBody{
				greeting,
				{},
				{
					services.EmailText("After reviewing your private share purchase submission for "),
					services.EmailBold(amount),
					services.EmailText(", we are unable to approve it at this time."),
				},
				{},
				{services.EmailText("This could be due to incomplete payment or missing details. If you have any questions, please reach out to our support team.")},
			},
			CTAText: "Contact Support",
			CTAURL:  "mailto:support@tsaconnectworld.com",
		}
	}

	if note != "" {
		msg.Body = append(msg.Body,
			services.EmailLine{},
			services.EmailLine{
				services.EmailBold("Note from admin:"),
				services.EmailText(" " + note),
			},
		)
	}
	return msg
}

// sendStatusEmail sends an email to the submitter when their private sale is approved or rejected.
func (h *PrivateSaleHandler) sendStatusEmail(submission models.PrivateSaleSubmission, status string, note string) {
	landingURL := h.Config.LandingURL
	if landingURL == "" {
		landingURL = "https://tsaconnectworld.com"
	}

	msg := privateSaleStatusEmail(submission, status, note, landingURL)
	if err := h.EmailService.SendBody(
		submission.Email, submission.Name, msg.Subject, msg.Title, msg.Body, msg.CTAText, msg.CTAURL,
	); err != nil {
		// Log error but don't block — email failure shouldn't break the status update
		fmt.Printf("[PrivateSaleHandler] failed to send %s email to %s: %v\n", status, submission.Email, err)
	}
}
