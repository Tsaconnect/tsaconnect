package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// ethAddressRegex validates Ethereum-style wallet addresses.
var ethAddressRegex = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// registerWalletRequest is the request body for RegisterWalletAddress.
type registerWalletRequest struct {
	WalletAddress string `json:"walletAddress" binding:"required"`
}

// prepareTxRequest is the request body for PrepareSendTransaction.
type prepareTxRequest struct {
	TokenSymbol string `json:"tokenSymbol" binding:"required"`
	ToAddress   string `json:"toAddress" binding:"required"`
	Amount      string `json:"amount" binding:"required"`
}

// submitTxRequest is the request body for SubmitTransaction.
type submitTxRequest struct {
	SignedTx    string `json:"signedTx" binding:"required"`
	TxType      string `json:"txType" binding:"required"`
	TokenSymbol string `json:"tokenSymbol" binding:"required"`
	ToAddress   string `json:"toAddress" binding:"required"`
	Amount      string `json:"amount" binding:"required"`
}

// isValidEthAddress checks if the given string is a valid Ethereum address.
func isValidEthAddress(addr string) bool {
	return ethAddressRegex.MatchString(addr)
}

// isPositiveAmount checks if the given string represents a positive numeric amount.
func isPositiveAmount(amount string) bool {
	val, err := strconv.ParseFloat(amount, 64)
	if err != nil {
		return false
	}
	return val > 0
}

// formatTokenBalance converts a raw big.Int balance to a human-readable string using token decimals.
func formatTokenBalance(balance *big.Int, decimals int) string {
	if balance == nil || balance.Sign() == 0 {
		return "0"
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole := new(big.Int).Div(balance, divisor)
	remainder := new(big.Int).Mod(balance, divisor)

	if remainder.Sign() == 0 {
		return whole.String()
	}

	format := fmt.Sprintf("%%s.%%0%ds", decimals)
	result := fmt.Sprintf(format, whole.String(), remainder)
	result = strings.TrimRight(result, "0")
	return result
}

// RegisterWalletAddress handles POST /api/wallet/register.
// Associates a blockchain wallet address with the authenticated user.
func (h *Handlers) RegisterWalletAddress(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var req registerWalletRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "walletAddress is required")
		return
	}

	if !isValidEthAddress(req.WalletAddress) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid wallet address format. Must be a valid Ethereum address (0x followed by 40 hex characters)")
		return
	}

	// Check if user already has a wallet address set.
	if user.WalletAddress != "" {
		// If the new address is the same, no-op.
		if user.WalletAddress == req.WalletAddress {
			utils.SuccessResponse(c, http.StatusOK, "Wallet address already registered", gin.H{
				"walletAddress": user.WalletAddress,
			})
			return
		}
		// Allow update to a different address; log the change for audit.
		log.Printf("User %s changed wallet from %s to %s", user.ID, user.WalletAddress, req.WalletAddress)
	}

	// Check if the address is already taken by another user.
	var existingUser models.User
	if err := config.DB.Where("wallet_address = ?", req.WalletAddress).First(&existingUser).Error; err == nil {
		utils.ErrorResponse(c, http.StatusConflict, "Wallet address already in use by another account")
		return
	}

	// Update user's wallet address.
	if err := config.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("wallet_address", req.WalletAddress).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to register wallet address")
		return
	}

	// Create Wallet record if one doesn't exist yet.
	var wallet models.Wallet
	if err := config.DB.Where("user_id = ?", user.ID).First(&wallet).Error; err != nil {
		now := time.Now()
		wallet = models.Wallet{
			ID:               uuid.New(),
			UserID:           user.ID,
			TotalBalance:     0,
			TotalUSDValue:    0,
			TransactionLimit: models.DefaultTransactionLimit,
			DailyLimit:       models.DefaultDailyLimit,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if createErr := config.DB.Create(&wallet).Error; createErr != nil {
			log.Printf("Failed to create wallet record for user %s: %v", user.ID, createErr)
		}
	}

	user.WalletAddress = req.WalletAddress
	utils.SuccessResponse(c, http.StatusOK, "Wallet address registered successfully", gin.H{
		"walletAddress": user.WalletAddress,
	})
}

// GetWalletBalances handles GET /api/wallet/balances.
// Returns token balances for the user's registered wallet address by querying the Sonic blockchain.
func (h *Handlers) GetWalletBalances(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered. Please register a wallet address first")
		return
	}

	balances := gin.H{
		"MCGP": "0",
		"USDT": "0",
		"USDC": "0",
		"S":    "0",
	}

	// Query real on-chain balances if blockchain client is available.
	client := h.BlockchainService.Client()
	if client != nil {
		// Native S token balance (18 decimals)
		if sBalance, err := client.GetBalance(user.WalletAddress); err == nil {
			balances["S"] = formatTokenBalance(sBalance, 18)
		} else {
			log.Printf("Failed to fetch S balance for %s: %v", user.WalletAddress, err)
		}

		// ERC-20 token balances
		tokenBalances, err := client.GetAllBalances(user.WalletAddress)
		if err == nil {
			for symbol, balance := range tokenBalances {
				decimals := 18
				if symbol == "USDT" || symbol == "USDC" {
					decimals = 6
				}
				balances[symbol] = formatTokenBalance(balance, decimals)
			}
		} else {
			log.Printf("Failed to fetch token balances for %s: %v", user.WalletAddress, err)
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Wallet balances retrieved", gin.H{
		"walletAddress": user.WalletAddress,
		"balances":      balances,
	})
}

// PrepareSendTransaction handles POST /api/wallet/prepare-tx.
// Prepares an unsigned transaction for the mobile app to sign.
func (h *Handlers) PrepareSendTransaction(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered. Please register a wallet address first")
		return
	}

	var req prepareTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "tokenSymbol, toAddress, and amount are required")
		return
	}

	// Validate token symbol.
	tokenUpper := strings.ToUpper(req.TokenSymbol)
	if !models.SupportedTokens[tokenUpper] {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported token. Supported tokens: MCGP, USDT, USDC, S")
		return
	}

	// Validate destination address.
	if !isValidEthAddress(req.ToAddress) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid destination address format")
		return
	}

	// Validate amount.
	if !isPositiveAmount(req.Amount) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Amount must be a positive number")
		return
	}

	// TODO: Integrate with BlockchainService / SonicClient to build unsigned transaction data.
	// Example future implementation:
	//   unsignedTx, err := h.BlockchainService.SonicClient.BuildTransferTx(tokenUpper, user.WalletAddress, req.ToAddress, req.Amount)
	//
	// For now, return stub transaction data.
	txData := gin.H{
		"from":        user.WalletAddress,
		"to":          req.ToAddress,
		"tokenSymbol": tokenUpper,
		"amount":      req.Amount,
		"chainId":     "0xFA",  // Sonic chain ID placeholder
		"data":        "0x",    // Encoded contract call data placeholder
		"gasLimit":    "65000", // Estimated gas limit placeholder
		"nonce":       "0",     // Nonce placeholder
	}

	utils.SuccessResponse(c, http.StatusOK, "Transaction prepared", gin.H{
		"transaction": txData,
	})
}

// SubmitTransaction handles POST /api/wallet/submit-tx.
// Submits a signed transaction to the blockchain and records it in the database.
func (h *Handlers) SubmitTransaction(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered. Please register a wallet address first")
		return
	}

	var req submitTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "signedTx, txType, tokenSymbol, toAddress, and amount are required")
		return
	}

	// Validate tx type.
	validTxTypes := map[string]bool{
		models.TxTypeSend:    true,
		models.TxTypeReceive: true,
		models.TxTypeApprove: true,
		models.TxTypeEscrow:  true,
	}
	if !validTxTypes[req.TxType] {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid transaction type. Must be: send, receive, approve, or escrow")
		return
	}

	// Validate token symbol.
	tokenUpper := strings.ToUpper(req.TokenSymbol)
	if !models.SupportedTokens[tokenUpper] {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported token. Supported tokens: MCGP, USDT, USDC, S")
		return
	}

	// Validate destination address.
	if !isValidEthAddress(req.ToAddress) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid destination address format")
		return
	}

	// Validate amount.
	if !isPositiveAmount(req.Amount) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Amount must be a positive number")
		return
	}

	// TODO: Integrate with BlockchainService / SonicClient to submit the signed transaction.
	// Example future implementation:
	//   txHash, err := h.BlockchainService.SonicClient.SendRawTransaction(req.SignedTx)
	//
	// For now, generate a mock tx hash.
	mockTxHash := "0x" + uuid.New().String()[:32] + "00000000"

	// Create wallet transaction record.
	walletTx := models.WalletTransaction{
		UserID:      user.ID,
		TxHash:      mockTxHash,
		TokenSymbol: tokenUpper,
		TxType:      req.TxType,
		FromAddress: user.WalletAddress,
		ToAddress:   req.ToAddress,
		Amount:      req.Amount,
		Status:      models.TxStatusPending,
	}

	if err := config.DB.Create(&walletTx).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to record transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusCreated, "Transaction submitted", gin.H{
		"transaction": walletTx,
	})
}

// GetTransactionHistory handles GET /api/wallet/transactions.
// Returns paginated transaction history for the authenticated user.
func (h *Handlers) GetTransactionHistory(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	// Parse pagination parameters.
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	txType := c.Query("type")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	// Build query.
	query := config.DB.Where("user_id = ?", user.ID)
	countQuery := config.DB.Model(&models.WalletTransaction{}).Where("user_id = ?", user.ID)

	if txType != "" {
		query = query.Where("tx_type = ?", txType)
		countQuery = countQuery.Where("tx_type = ?", txType)
	}

	// Get total count.
	var total int64
	if err := countQuery.Count(&total).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to count transactions")
		return
	}

	// Get transactions.
	var transactions []models.WalletTransaction
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&transactions).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch transactions")
		return
	}

	totalPages := int(total) / limit
	if int(total)%limit != 0 {
		totalPages++
	}

	utils.SuccessResponse(c, http.StatusOK, "Transactions retrieved", gin.H{
		"transactions": transactions,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}

// GetSupportedTokens handles GET /api/wallet/supported-tokens.
// Returns the list of active tokens with their chain availability.
// If no tokens exist in the database yet, seeds default tokens and returns them.
func (h *Handlers) GetSupportedTokens(c *gin.Context) {
	var tokens []models.SupportedToken
	if err := config.DB.Where("is_active = ?", true).Order("symbol ASC").Find(&tokens).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch supported tokens")
		return
	}

	// Seed defaults if table is empty (first-time setup).
	if len(tokens) == 0 {
		tokens = seedDefaultTokens()
	}

	// Map to the response shape the frontend expects.
	result := make([]gin.H, 0, len(tokens))
	for _, t := range tokens {
		// Parse chains from JSON.
		var chains []string
		if err := json.Unmarshal(t.Chains, &chains); err != nil {
			chains = []string{}
		}
		result = append(result, gin.H{
			"symbol":    t.Symbol,
			"name":      t.Name,
			"decimals":  t.Decimals,
			"iconColor": t.IconColor,
			"chains":    chains,
		})
	}

	utils.SuccessResponse(c, http.StatusOK, "Supported tokens retrieved", result)
}

// seedDefaultTokens inserts the default token set into the database.
func seedDefaultTokens() []models.SupportedToken {
	type tokenDef struct {
		Symbol    string
		Name      string
		Decimals  int
		IconColor string
		Chains    []string
	}

	defaults := []tokenDef{
		{"USDT", "Tether", 6, "#26A17B", []string{"sonic", "bsc"}},
		{"USDC", "USD Coin", 6, "#2775CA", []string{"sonic", "bsc"}},
		{"MCGP", "MCG Protocol", 18, "#FFD700", []string{"sonic"}},
	}

	var created []models.SupportedToken
	for _, d := range defaults {
		chainsJSON, _ := json.Marshal(d.Chains)
		token := models.SupportedToken{
			ID:        uuid.New(),
			Symbol:    d.Symbol,
			Name:      d.Name,
			Decimals:  d.Decimals,
			IconColor: d.IconColor,
			Chains:    chainsJSON,
			IsActive:  true,
		}
		if err := config.DB.Create(&token).Error; err != nil {
			log.Printf("Failed to seed token %s: %v", d.Symbol, err)
			continue
		}
		created = append(created, token)
	}
	return created
}

// ConfirmSeedPhraseBackup handles POST /api/wallet/seed-phrase-backed-up.
// Marks the user's seed phrase as backed up.
func (h *Handlers) ConfirmSeedPhraseBackup(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if err := config.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("seed_phrase_backed_up", true).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update seed phrase backup status")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Seed phrase backup confirmed", gin.H{
		"seedPhraseBackedUp": true,
	})
}
