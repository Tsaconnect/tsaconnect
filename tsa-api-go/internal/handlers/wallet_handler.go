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

	"github.com/ethereum/go-ethereum/common/hexutil"
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
	ChainID     int64  `json:"chainId" binding:"required"`
}

// submitTxRequest is the request body for SubmitTransaction.
type submitTxRequest struct {
	SignedTx    string `json:"signedTx" binding:"required"`
	TxType      string `json:"txType" binding:"required"`
	TokenSymbol string `json:"tokenSymbol" binding:"required"`
	ToAddress   string `json:"toAddress" binding:"required"`
	Amount      string `json:"amount" binding:"required"`
	ChainID     int64  `json:"chainId" binding:"required"`
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

func parseTokenAmount(amount string, decimals int) (*big.Int, bool) {
	f, ok := new(big.Float).SetString(amount)
	if !ok {
		return nil, false
	}
	multiplier := new(big.Float).SetInt(
		new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil),
	)
	f.Mul(f, multiplier)
	result, _ := f.Int(nil)
	if result.Sign() <= 0 {
		return nil, false
	}
	return result, true
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

// resolveNetwork reads the "network" query param and returns "mainnet" or "testnet".
func resolveNetwork(c *gin.Context) string {
	n := c.DefaultQuery("network", "mainnet")
	if n == "testnet" {
		return "testnet"
	}
	return "mainnet"
}

// GetWalletBalances handles GET /api/wallet/balances.
// Query params: ?network=mainnet|testnet (default mainnet), ?chainId=<numeric> (optional).
func (h *Handlers) GetWalletBalances(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	network := resolveNetwork(c)
	netCfg, ok := h.Config.Networks[network]
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid network")
		return
	}

	chainIDParam := c.Query("chainId")

	type chainQuery struct {
		name string
		cfg  config.ChainConfig
	}
	var chains []chainQuery

	if chainIDParam != "" {
		cid, err := strconv.ParseInt(chainIDParam, 10, 64)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid chainId")
			return
		}
		for name, cfg := range netCfg.Chains {
			if cfg.ChainID == cid {
				chains = append(chains, chainQuery{name, cfg})
				break
			}
		}
		if len(chains) == 0 {
			utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain ID")
			return
		}
	} else {
		for name, cfg := range netCfg.Chains {
			chains = append(chains, chainQuery{name, cfg})
		}
	}

	var supportedTokens []models.SupportedToken
	config.DB.Where("is_active = ?", true).Find(&supportedTokens)

	type tokenBalance struct {
		Balance  string  `json:"balance"`
		UsdPrice float64 `json:"usdPrice"`
		UsdValue float64 `json:"usdValue"`
	}
	result := make(map[string]map[string]tokenBalance)

	// Collect all token symbols to fetch prices
	symbolSet := make(map[string]bool)
	for _, cq := range chains {
		symbolSet[cq.cfg.NativeCurrency] = true
	}
	for _, tok := range supportedTokens {
		symbolSet[tok.Symbol] = true
	}
	var priceSymbols []string
	for s := range symbolSet {
		priceSymbols = append(priceSymbols, s)
	}

	// Fetch USD prices from CoinGecko
	prices := make(map[string]float64)
	if h.PriceService != nil && len(priceSymbols) > 0 {
		if priceData, err := h.PriceService.GetPrices(priceSymbols); err == nil {
			for sym, pd := range priceData {
				prices[sym] = pd.USD
			}
		}
	}

	// Override MCGP price with OTC contract buy price
	if h.OTCService != nil {
		if priceWei, err := h.OTCService.GetBuyPricePerToken(); err == nil && priceWei.Sign() > 0 {
			// priceWei is USDC (6 decimals) per 1 MCGP — convert to float USD
			prices["MCGP"] = float64(priceWei.Int64()) / 1e6
		}
	}

	for _, cq := range chains {
		client := h.BlockchainService.ClientForNetwork(network, cq.name)
		chainBalances := make(map[string]tokenBalance)

		if client != nil {
			if nativeBal, err := client.GetBalance(user.WalletAddress); err == nil {
				balStr := formatTokenBalance(nativeBal, 18)
				balFloat, _ := strconv.ParseFloat(balStr, 64)
				price := prices[cq.cfg.NativeCurrency]
				chainBalances[cq.cfg.NativeCurrency] = tokenBalance{
					Balance:  balStr,
					UsdPrice: price,
					UsdValue: balFloat * price,
				}
			}
		}

		for _, tok := range supportedTokens {
			var tokenChains []string
			json.Unmarshal(tok.Chains, &tokenChains)
			for _, tc := range tokenChains {
				if tc == cq.name {
					tokenAddr := h.BlockchainService.TokenAddressForNetwork(network, cq.name, tok.Symbol)
					if tokenAddr != "" && client != nil {
						if bal, err := client.GetTokenBalance(tokenAddr, user.WalletAddress); err == nil {
							balStr := formatTokenBalance(bal, tok.Decimals)
							balFloat, _ := strconv.ParseFloat(balStr, 64)
							price := prices[tok.Symbol]
							chainBalances[tok.Symbol] = tokenBalance{
								Balance:  balStr,
								UsdPrice: price,
								UsdValue: balFloat * price,
							}
						} else {
							chainBalances[tok.Symbol] = tokenBalance{Balance: "0"}
						}
					} else {
						chainBalances[tok.Symbol] = tokenBalance{Balance: "0"}
					}
				}
			}
		}

		result[cq.name] = chainBalances
	}

	utils.SuccessResponse(c, http.StatusOK, "Wallet balances retrieved", gin.H{
		"walletAddress": user.WalletAddress,
		"network":       network,
		"balances":      result,
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
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	var req prepareTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "tokenSymbol, toAddress, amount, and chainId are required")
		return
	}

	tokenUpper := strings.ToUpper(req.TokenSymbol)
	if !isValidEthAddress(req.ToAddress) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid destination address format")
		return
	}
	if !isPositiveAmount(req.Amount) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Amount must be a positive number")
		return
	}

	client, chainName := h.BlockchainService.ClientForChainID(req.ChainID)
	if client == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain ID")
		return
	}

	// Resolve which network this chainId belongs to
	network, _, _ := h.BlockchainService.NetworkForChainID(req.ChainID)
	if network == "" {
		network = "mainnet"
	}

	decimals := 18
	if tokenUpper == "USDT" || tokenUpper == "USDC" {
		decimals = 6
	}
	amountWei, ok := parseTokenAmount(req.Amount, decimals)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid amount format")
		return
	}

	var txBytes []byte
	var err error

	if tokenUpper == "S" || tokenUpper == "TBNB" || tokenUpper == "BNB" {
		txBytes, err = client.PrepareNativeTransfer(user.WalletAddress, req.ToAddress, amountWei)
	} else {
		tokenAddr := h.BlockchainService.TokenAddressForNetwork(network, chainName, tokenUpper)
		if tokenAddr == "" {
			utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Token %s not configured on %s", tokenUpper, chainName))
			return
		}
		txBytes, err = client.PrepareERC20Transfer(tokenAddr, user.WalletAddress, req.ToAddress, amountWei)
	}

	if err != nil {
		log.Printf("Failed to prepare tx: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare transaction")
		return
	}

	var txData map[string]interface{}
	json.Unmarshal(txBytes, &txData)

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
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	var req submitTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "signedTx, txType, tokenSymbol, toAddress, amount, and chainId are required")
		return
	}

	validTxTypes := map[string]bool{
		models.TxTypeSend: true, models.TxTypeReceive: true,
		models.TxTypeApprove: true, models.TxTypeEscrow: true,
	}
	if !validTxTypes[req.TxType] {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid transaction type")
		return
	}

	tokenUpper := strings.ToUpper(req.TokenSymbol)
	if !isValidEthAddress(req.ToAddress) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid destination address format")
		return
	}
	if !isPositiveAmount(req.Amount) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Amount must be a positive number")
		return
	}

	client, chainName := h.BlockchainService.ClientForChainID(req.ChainID)
	if client == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain ID")
		return
	}

	signedTxBytes, err := hexutil.Decode(req.SignedTx)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid signed transaction format")
		return
	}

	txHash, err := client.SendRawTransaction(signedTxBytes)
	if err != nil {
		log.Printf("Failed to submit tx to %s: %v", chainName, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to submit transaction to blockchain")
		return
	}

	walletTx := models.WalletTransaction{
		UserID:      user.ID,
		TxHash:      txHash,
		TokenSymbol: tokenUpper,
		TxType:      req.TxType,
		FromAddress: user.WalletAddress,
		ToAddress:   req.ToAddress,
		Amount:      req.Amount,
		Status:      models.TxStatusPending,
		Chain:       chainName,
		ChainID:     req.ChainID,
	}

	if err := config.DB.Create(&walletTx).Error; err != nil {
		log.Printf("tx submitted (hash=%s) but failed to record in DB: %v", txHash, err)
		utils.SuccessResponse(c, http.StatusCreated, "Transaction submitted but failed to record", gin.H{
			"txHash": txHash,
		})
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

	chainIDFilter := c.Query("chainId")
	if chainIDFilter != "" {
		cid, _ := strconv.ParseInt(chainIDFilter, 10, 64)
		if cid > 0 {
			query = query.Where("chain_id = ?", cid)
			countQuery = countQuery.Where("chain_id = ?", cid)
		}
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

// ResolveUsername handles GET /api/wallet/resolve/:username
// Looks up a user by username and returns their public wallet address.
func (h *Handlers) ResolveUsername(c *gin.Context) {
	caller := getUserFromContext(c)
	if caller == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	username := strings.TrimSpace(c.Param("username"))
	if username == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Username is required")
		return
	}

	// Prevent sending to self
	if strings.EqualFold(username, caller.Username) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot send to yourself")
		return
	}

	var target models.User
	if err := config.DB.Where("LOWER(username) = LOWER(?)", username).First(&target).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "User not found")
		return
	}

	if target.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "This user has not set up a wallet yet")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "User resolved", gin.H{
		"username":           target.Username,
		"name":               target.Name,
		"walletAddress":      target.WalletAddress,
		"verificationStatus": target.VerificationStatus,
	})
}
