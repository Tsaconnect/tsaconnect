package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// ServiceContactHandler handles service contact fee endpoints.
type ServiceContactHandler struct {
	Config                *config.Config
	BlockchainService     *services.BlockchainService
	ServiceContactService *services.ServiceContactService
}

// NewServiceContactHandler creates a new ServiceContactHandler.
func NewServiceContactHandler(cfg *config.Config, bs *services.BlockchainService, scs *services.ServiceContactService) *ServiceContactHandler {
	return &ServiceContactHandler{
		Config:                cfg,
		BlockchainService:     bs,
		ServiceContactService: scs,
	}
}

type submitContactFeeRequest struct {
	ApproveTxHash string `json:"approveTxHash" binding:"required"`
	PayFeeTxHash  string `json:"payFeeTxHash" binding:"required"`
	Token         string `json:"token"`
}

// tokenSymbolFromAddress returns the token symbol for a known address, or empty string.
func (h *ServiceContactHandler) tokenSymbolFromAddress(addr string) string {
	lower := strings.ToLower(addr)
	for _, symbol := range []string{"USDC", "USDT"} {
		if strings.ToLower(h.BlockchainService.TokenAddress("sonic", symbol)) == lower {
			return symbol
		}
	}
	return ""
}

// PrepareContactFee handles POST /api/services/:id/prepare-contact-fee
func (h *ServiceContactHandler) PrepareContactFee(c *gin.Context) {
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

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	// Look up the service advert
	var service models.Product
	if err := config.DB.First(&service, "id = ?", serviceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "Service not found")
		} else {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Database error")
		}
		return
	}

	if service.Type != models.ProductTypeService {
		utils.ErrorResponse(c, http.StatusBadRequest, "This listing is not a service")
		return
	}

	// Get the service provider's wallet address
	var provider models.User
	if err := config.DB.First(&provider, "id = ?", service.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "Service provider not found")
		} else {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Database error")
		}
		return
	}

	if provider.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Service provider has no wallet address")
		return
	}

	if user.ID == provider.ID {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot pay contact fee for your own service")
		return
	}

	// Check if already paid
	var existingPayment models.ServiceContactPayment
	if err := config.DB.Where("caller_id = ? AND service_id = ?", user.ID, serviceID).First(&existingPayment).Error; err == nil {
		utils.ErrorResponse(c, http.StatusConflict, "Contact fee already paid for this service")
		return
	}

	// Determine token (default USDC, accept query param)
	tokenSymbol := strings.ToUpper(c.DefaultQuery("token", "USDC"))
	if tokenSymbol != "USDC" && tokenSymbol != "USDT" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported token. Use USDC or USDT")
		return
	}

	tokenAddr := h.BlockchainService.TokenAddress("sonic", tokenSymbol)
	if tokenAddr == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Token not configured on Sonic chain")
		return
	}

	// Determine caller's upline (Fix #8: distinguish not-found from DB error)
	upline := "0x0000000000000000000000000000000000000000"
	if user.ReferredBy != nil {
		var referrer models.User
		if err := config.DB.First(&referrer, "id = ?", *user.ReferredBy).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				log.Printf("Failed to look up referrer %s: %v", user.ReferredBy, err)
				utils.ErrorResponse(c, http.StatusInternalServerError, "Database error looking up referrer")
				return
			}
			// Referrer not found — use zero address
		} else if referrer.WalletAddress != "" {
			upline = referrer.WalletAddress
		}
	}

	feeAmount := h.ServiceContactService.GetFeeAmount()

	// Prepare approve tx
	client := h.BlockchainService.ClientForChain("sonic")
	if client == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Blockchain service unavailable")
		return
	}

	approveTxBytes, err := client.PrepareERC20Approve(tokenAddr, user.WalletAddress, h.Config.ServiceContractAddress, feeAmount)
	if err != nil {
		log.Printf("Failed to prepare approve tx: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare approve transaction")
		return
	}

	// Prepare payContactFee tx
	payFeeTxBytes, err := h.ServiceContactService.PreparePayContactFee(
		user.WalletAddress,
		provider.WalletAddress,
		upline,
		tokenAddr,
	)
	if err != nil {
		log.Printf("Failed to prepare payContactFee tx: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare contact fee transaction")
		return
	}

	var approveTx, payFeeTx map[string]interface{}
	if err := json.Unmarshal(approveTxBytes, &approveTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode approve transaction")
		return
	}
	if err := json.Unmarshal(payFeeTxBytes, &payFeeTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode contact fee transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Contact fee transactions prepared", gin.H{
		"approveTx": approveTx,
		"payFeeTx":  payFeeTx,
		"feeAmount": feeAmount.String(),
		"token":     tokenSymbol,
	})
}

// SubmitContactFee handles POST /api/services/:id/submit-contact-fee
func (h *ServiceContactHandler) SubmitContactFee(c *gin.Context) {
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

	var req submitContactFeeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "approveTxHash and payFeeTxHash are required")
		return
	}

	// Look up the service advert (Fix #5: proper error handling)
	var service models.Product
	if err := config.DB.First(&service, "id = ?", serviceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "Service not found")
		} else {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Database error")
		}
		return
	}

	// Get provider wallet for on-chain verification
	var provider models.User
	if err := config.DB.First(&provider, "id = ?", service.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "Service provider not found")
		} else {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Database error")
		}
		return
	}

	// Check if already paid
	var existingPayment models.ServiceContactPayment
	if err := config.DB.Where("caller_id = ? AND service_id = ?", user.ID, serviceID).First(&existingPayment).Error; err == nil {
		utils.ErrorResponse(c, http.StatusConflict, "Contact fee already paid for this service")
		return
	}

	// Verify approve tx
	approveReceipt := h.BlockchainService.GetTransactionReceipt(req.ApproveTxHash, "sonic")
	switch approveReceipt.Status {
	case "confirmed":
		// OK
	case "error":
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Unable to verify approve transaction — try again later")
		return
	case "pending":
		utils.ErrorResponse(c, http.StatusConflict, "Approve transaction is still pending — please wait and retry")
		return
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Approve transaction failed on-chain")
		return
	}

	// Verify payContactFee tx
	payFeeReceipt := h.BlockchainService.GetTransactionReceipt(req.PayFeeTxHash, "sonic")
	switch payFeeReceipt.Status {
	case "confirmed":
		// OK
	case "error":
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Unable to verify pay fee transaction — try again later")
		return
	case "pending":
		utils.ErrorResponse(c, http.StatusConflict, "Pay fee transaction is still pending — please wait and retry")
		return
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Pay fee transaction failed on-chain")
		return
	}

	// Fix #1: Verify ContactFeePaid event with caller/provider address validation
	// Fix #9: Separate blockchain error (503) from verification failure (400)
	event, err := h.ServiceContactService.VerifyContactFeePaid(req.PayFeeTxHash, user.WalletAddress, provider.WalletAddress)
	if err != nil {
		errMsg := err.Error()
		log.Printf("Failed to verify ContactFeePaid event: %v", err)
		if strings.Contains(errMsg, "failed to get receipt") || strings.Contains(errMsg, "blockchain client not available") {
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "Blockchain unavailable — try again later")
			return
		}
		utils.ErrorResponse(c, http.StatusBadRequest, "Contact fee payment verification failed: "+errMsg)
		return
	}

	// Fix #2: Determine token from on-chain event, fall back to request body, then USDC
	tokenSymbol := "USDC"
	if eventToken := h.tokenSymbolFromAddress(event.Token.Hex()); eventToken != "" {
		tokenSymbol = eventToken
	} else if req.Token != "" {
		candidate := strings.ToUpper(req.Token)
		if candidate == "USDC" || candidate == "USDT" {
			tokenSymbol = candidate
		}
	}

	// Record the payment
	feeAmount := h.ServiceContactService.GetFeeAmount()
	payment := models.ServiceContactPayment{
		ID:                uuid.New(),
		CallerID:          user.ID,
		ServiceProviderID: service.UserID,
		ServiceID:         serviceID,
		Token:             tokenSymbol,
		FeeAmount:         feeAmount.String(),
		ApproveTxHash:     req.ApproveTxHash,
		PayFeeTxHash:      req.PayFeeTxHash,
		CreatedAt:         time.Now(),
	}

	if err := config.DB.Create(&payment).Error; err != nil {
		log.Printf("Failed to record contact fee payment: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to record payment")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Contact fee paid successfully", gin.H{
		"payment": payment,
		"contact": gin.H{
			"name":    provider.Name,
			"phone":   provider.PhoneNumber,
			"email":   provider.Email,
			"address": provider.Address,
		},
	})
}

// GetServiceContact handles GET /api/services/:id/contact
func (h *ServiceContactHandler) GetServiceContact(c *gin.Context) {
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

	// Look up the service (Fix #5)
	var service models.Product
	if err := config.DB.First(&service, "id = ?", serviceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "Service not found")
		} else {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Database error")
		}
		return
	}

	// If the user is the provider, they can see their own contact info
	if user.ID == service.UserID {
		utils.SuccessResponse(c, http.StatusOK, "Contact details retrieved", gin.H{
			"paid": true,
			"contact": gin.H{
				"name":    user.Name,
				"phone":   user.PhoneNumber,
				"email":   user.Email,
				"address": user.Address,
			},
		})
		return
	}

	// Check if the user has already paid
	var payment models.ServiceContactPayment
	if err := config.DB.Where("caller_id = ? AND service_id = ?", user.ID, serviceID).First(&payment).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Database error")
			return
		}
		// Not paid — return 402 (using utils.ErrorResponse for consistent format)
		feeAmount := h.ServiceContactService.GetFeeAmount()
		c.JSON(http.StatusPaymentRequired, gin.H{
			"success": false,
			"message": "Contact fee not paid",
			"data": gin.H{
				"paid":      false,
				"feeAmount": feeAmount.String(),
				"token":     "USDC",
			},
		})
		return
	}

	// Paid — return contact details (Fix #5)
	var provider models.User
	if err := config.DB.First(&provider, "id = ?", service.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "Provider not found")
		} else {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Database error")
		}
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Contact details retrieved", gin.H{
		"paid": true,
		"contact": gin.H{
			"name":    provider.Name,
			"phone":   provider.PhoneNumber,
			"email":   provider.Email,
			"address": provider.Address,
		},
	})
}
