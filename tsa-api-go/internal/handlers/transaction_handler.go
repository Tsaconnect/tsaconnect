package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// GetTransactions returns paginated, filtered transactions.
// GET /api/transactions/ — auth, pagination, filter by type/status/asset/dateRange.
func (h *Handlers) GetTransactions(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Where("user_id = ?", user.ID)

	if txType := c.Query("type"); txType != "" {
		query = query.Where("type = ?", txType)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if asset := c.Query("asset"); asset != "" {
		query = query.Where("(from_asset->>'symbol' = ? OR to_asset->>'symbol' = ?)", asset, asset)
	}

	if startDate := c.Query("startDate"); startDate != "" {
		if t, err := time.Parse(time.RFC3339, startDate); err == nil {
			query = query.Where("created_at >= ?", t)
		}
	}
	if endDate := c.Query("endDate"); endDate != "" {
		if t, err := time.Parse(time.RFC3339, endDate); err == nil {
			query = query.Where("created_at <= ?", t)
		}
	}

	var total int64
	if err := query.Model(&models.Transaction{}).Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to count transactions"})
		return
	}

	var transactions []models.Transaction
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&transactions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch transactions"})
		return
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Transactions retrieved successfully",
		"data": gin.H{
			"transactions": transactions,
			"pagination": gin.H{
				"page":       page,
				"limit":      limit,
				"total":      total,
				"totalPages": totalPages,
			},
		},
	})
}

// GetTransactionByID returns details for a specific transaction.
// GET /api/transactions/:id — auth, include blockchain confirmation.
func (h *Handlers) GetTransactionByID(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	txID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid transaction ID"})
		return
	}

	var tx models.Transaction
	if err := config.DB.Where("id = ? AND user_id = ?", txID, user.ID).First(&tx).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Transaction not found"})
		return
	}

	// Include blockchain confirmation info
	confirmation := gin.H{
		"confirmed":     tx.Confirmations >= tx.RequiredConfirms,
		"confirmations": tx.Confirmations,
		"required":      tx.RequiredConfirms,
		"blockchain":    tx.Blockchain,
	}
	txHash := tx.TxHash
	if txHash == "" {
		txHash = tx.TransactionHash
	}
	if txHash != "" {
		confirmation["txHash"] = txHash
		confirmation["explorerUrl"] = fmt.Sprintf("https://etherscan.io/tx/%s", txHash)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Transaction details retrieved successfully",
		"data": gin.H{
			"transaction":  tx,
			"confirmation": confirmation,
		},
	})
}

// GetTransactionStats returns transaction statistics/aggregation.
// GET /api/transactions/stats/summary — auth, period: day/week/month/year, aggregation.
func (h *Handlers) GetTransactionStats(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	period := c.DefaultQuery("period", "month")
	now := time.Now()
	var startDate time.Time
	switch period {
	case "day":
		startDate = now.AddDate(0, 0, -1)
	case "week":
		startDate = now.AddDate(0, 0, -7)
	case "month":
		startDate = now.AddDate(0, -1, 0)
	case "year":
		startDate = now.AddDate(-1, 0, 0)
	default:
		startDate = now.AddDate(0, -1, 0)
	}

	var transactions []models.Transaction
	if err := config.DB.Where("user_id = ? AND created_at >= ?", user.ID, startDate).Find(&transactions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch transactions"})
		return
	}

	stats := models.TransactionStats{
		TotalTransactions: len(transactions),
	}

	for _, tx := range transactions {
		stats.TotalFees += tx.FeeUSD
		switch tx.Type {
		case models.TransactionTypeDeposit:
			stats.TotalDeposits += tx.USDValue
		case models.TransactionTypeWithdrawal:
			stats.TotalWithdrawals += tx.USDValue
		case models.TransactionTypeSwap:
			stats.TotalSwaps += tx.USDValue
		}
	}
	stats.NetFlow = stats.TotalDeposits - stats.TotalWithdrawals

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Transaction stats retrieved successfully",
		"data": gin.H{
			"period": period,
			"stats":  stats,
		},
	})
}

// CreateTransaction creates a deposit transaction.
// POST /api/transactions/deposit — auth, validate assetSymbol/amount, create/get asset, calc USD value.
func (h *Handlers) CreateTransaction(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var body struct {
		AssetSymbol string  `json:"assetSymbol" binding:"required"`
		Amount      float64 `json:"amount" binding:"required,gt=0"`
		Network     string  `json:"network"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Asset symbol and positive amount are required"})
		return
	}

	// Get or create asset
	var asset models.Asset
	err := config.DB.Where("user_id = ? AND symbol = ?", user.ID, body.AssetSymbol).First(&asset).Error
	if err != nil {
		// Create the asset
		now := time.Now()
		asset = models.Asset{
			ID:        uuid.New(),
			UserID:    user.ID,
			Symbol:    body.AssetSymbol,
			Name:      body.AssetSymbol,
			Balance:   0,
			IsHidden:  false,
			CreatedAt: now,
			UpdatedAt: now,
		}
		asset.SetDetails(&models.AssetDetails{
			Type:  models.AssetTypeToken,
			Chain: models.ChainEthereum,
		})
		config.DB.Create(&asset)
	}

	// Get price and calculate USD value
	price := h.getAssetPrice(body.AssetSymbol)
	usdValue := price * body.Amount
	walletAddr := h.getWalletAddress(user.ID.String(), body.Network)

	blockchain := body.Network
	if blockchain == "" {
		blockchain = models.BlockchainEthereum
	}

	now := time.Now()
	tx := models.Transaction{
		ID:               uuid.New(),
		UserID:           user.ID,
		Type:             models.TransactionTypeDeposit,
		Status:           models.TransactionStatusCompleted,
		Amount:           body.Amount,
		USDValue:         usdValue,
		Blockchain:       blockchain,
		Confirmations:    12,
		RequiredConfirms: 12,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	tx.SetToAsset(&models.TransactionAsset{
		Symbol:  body.AssetSymbol,
		Amount:  body.Amount,
		Address: walletAddr,
	})

	if err := config.DB.Create(&tx).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create deposit"})
		return
	}

	// Update asset balance
	newBalance := asset.Balance + body.Amount
	newUSDValue := price * newBalance
	config.DB.Model(&asset).Updates(map[string]interface{}{
		"balance":    newBalance,
		"usd_value":  newUSDValue,
		"updated_at": now,
	})

	h.EventBus.Publish(events.Event{
		Type:    events.TransactionPending,
		UserID:  user.ID,
		Title:   "Deposit Initiated",
		Message: fmt.Sprintf("Deposit of %.6f %s is being processed", body.Amount, body.AssetSymbol),
		Data: map[string]interface{}{
			"transactionId": tx.ID.String(),
			"type":          "deposit",
			"amount":        body.Amount,
			"asset":         body.AssetSymbol,
			"usdValue":      usdValue,
		},
	})

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Deposit created successfully",
		"data":    tx,
	})
}

// CreateWithdrawal creates a withdrawal transaction.
// POST /api/transactions/withdraw — auth, check balance, check limits, calc fees: 0.5% platform + network.
func (h *Handlers) CreateWithdrawal(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var body struct {
		AssetSymbol   string  `json:"assetSymbol" binding:"required"`
		Amount        float64 `json:"amount" binding:"required,gt=0"`
		Network       string  `json:"network"`
		WalletAddress string  `json:"walletAddress" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Asset symbol, positive amount, and wallet address are required"})
		return
	}

	// Find the asset
	var asset models.Asset
	if err := config.DB.Where("user_id = ? AND symbol = ?", user.ID, body.AssetSymbol).First(&asset).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	// Calculate fees
	fees := h.calculateWithdrawalFees(body.AssetSymbol, body.Network, body.Amount)
	platformFee := fees["platformFee"]
	networkFee := fees["networkFee"]
	totalFee := platformFee + networkFee
	totalDeduction := body.Amount + totalFee

	// Check balance
	if asset.Balance < totalDeduction {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Insufficient balance",
			"data": gin.H{
				"available": asset.Balance,
				"required":  totalDeduction,
				"fee":       totalFee,
			},
		})
		return
	}

	// Check withdrawal limits
	if err := h.checkWithdrawalLimits(user.ID, body.Amount, body.AssetSymbol); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}

	price := h.getAssetPrice(body.AssetSymbol)
	usdValue := body.Amount * price
	feeUSD := totalFee * price

	blockchain := body.Network
	if blockchain == "" {
		blockchain = models.BlockchainEthereum
	}

	now := time.Now()
	tx := models.Transaction{
		ID:               uuid.New(),
		UserID:           user.ID,
		Type:             models.TransactionTypeWithdrawal,
		Status:           models.TransactionStatusPending,
		Amount:           body.Amount,
		USDValue:         usdValue,
		FeeUSD:           feeUSD,
		Blockchain:       blockchain,
		Confirmations:    0,
		RequiredConfirms: 12,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	tx.SetFromAsset(&models.TransactionAsset{
		Symbol:  body.AssetSymbol,
		Amount:  body.Amount,
		Address: h.getWalletAddress(user.ID.String(), body.Network),
	})
	tx.SetToAsset(&models.TransactionAsset{
		Symbol:  body.AssetSymbol,
		Amount:  body.Amount,
		Address: body.WalletAddress,
	})

	feesJSON, _ := json.Marshal(&models.TransactionFees{
		Platform: platformFee,
		Network:  networkFee,
	})
	tx.Fees = feesJSON

	if err := config.DB.Create(&tx).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create withdrawal"})
		return
	}

	// Update asset balance
	newBalance := asset.Balance - totalDeduction
	newUSDValue := newBalance * price
	config.DB.Model(&asset).Updates(map[string]interface{}{
		"balance":    newBalance,
		"usd_value":  newUSDValue,
		"updated_at": now,
	})

	h.EventBus.Publish(events.Event{
		Type:    events.TransactionPending,
		UserID:  user.ID,
		Title:   "Withdrawal Initiated",
		Message: fmt.Sprintf("Withdrawal of %.6f %s is being processed", body.Amount, body.AssetSymbol),
		Data: map[string]interface{}{
			"transactionId": tx.ID.String(),
			"type":          "withdrawal",
			"amount":        body.Amount,
			"asset":         body.AssetSymbol,
			"usdValue":      usdValue,
			"toAddress":     body.WalletAddress,
		},
	})

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Withdrawal created successfully",
		"data":    tx,
	})
}

// CreateSwap creates a swap transaction between two assets.
// POST /api/transactions/swap — auth, check same asset, check balance, get exchange rate, 0.1% fee, update both balances.
func (h *Handlers) CreateSwap(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var body struct {
		FromAsset string  `json:"fromAsset" binding:"required"`
		ToAsset   string  `json:"toAsset" binding:"required"`
		Amount    float64 `json:"amount" binding:"required,gt=0"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "From asset, to asset, and positive amount are required"})
		return
	}

	if body.FromAsset == body.ToAsset {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Cannot swap the same asset"})
		return
	}

	// Find source asset
	var fromAssetDoc models.Asset
	if err := config.DB.Where("user_id = ? AND symbol = ?", user.ID, body.FromAsset).First(&fromAssetDoc).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Source asset not found"})
		return
	}

	// Check balance (including 0.1% fee)
	fee := body.Amount * 0.001
	totalDeduction := body.Amount + fee
	if fromAssetDoc.Balance < totalDeduction {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Insufficient balance for swap",
			"data": gin.H{
				"available": fromAssetDoc.Balance,
				"required":  totalDeduction,
				"fee":       fee,
			},
		})
		return
	}

	// Get exchange rate
	fromPrice := h.getAssetPrice(body.FromAsset)
	toPrice := h.getAssetPrice(body.ToAsset)
	if toPrice == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Unable to determine exchange rate"})
		return
	}
	exchangeRate := fromPrice / toPrice
	toAmount := body.Amount * exchangeRate

	usdValue := body.Amount * fromPrice
	feeUSD := fee * fromPrice

	now := time.Now()
	tx := models.Transaction{
		ID:               uuid.New(),
		UserID:           user.ID,
		Type:             models.TransactionTypeSwap,
		Status:           models.TransactionStatusCompleted,
		Amount:           body.Amount,
		USDValue:         usdValue,
		FeeUSD:           feeUSD,
		Blockchain:       models.BlockchainInternal,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	tx.SetFromAsset(&models.TransactionAsset{
		Symbol: body.FromAsset,
		Amount: body.Amount,
	})
	tx.SetToAsset(&models.TransactionAsset{
		Symbol: body.ToAsset,
		Amount: toAmount,
	})

	feesJSON, _ := json.Marshal(&models.TransactionFees{
		Platform: fee,
	})
	tx.Fees = feesJSON

	metaJSON, _ := json.Marshal(&models.TransactionMetadata{
		ExchangeRate: exchangeRate,
	})
	tx.Metadata = metaJSON

	if err := config.DB.Create(&tx).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create swap"})
		return
	}

	// Update from asset balance
	newFromBalance := fromAssetDoc.Balance - totalDeduction
	config.DB.Model(&fromAssetDoc).Updates(map[string]interface{}{
		"balance":    newFromBalance,
		"usd_value":  newFromBalance * fromPrice,
		"updated_at": now,
	})

	// Update or create to asset
	var toAssetDoc models.Asset
	err := config.DB.Where("user_id = ? AND symbol = ?", user.ID, body.ToAsset).First(&toAssetDoc).Error
	if err != nil {
		// Create the to asset
		toAssetDoc = models.Asset{
			ID:        uuid.New(),
			UserID:    user.ID,
			Symbol:    body.ToAsset,
			Name:      body.ToAsset,
			Balance:   toAmount,
			USDValue:  toAmount * toPrice,
			CreatedAt: now,
			UpdatedAt: now,
		}
		toAssetDoc.SetDetails(&models.AssetDetails{
			Type:  models.AssetTypeToken,
			Chain: models.ChainEthereum,
		})
		config.DB.Create(&toAssetDoc)
	} else {
		newToBalance := toAssetDoc.Balance + toAmount
		config.DB.Model(&toAssetDoc).Updates(map[string]interface{}{
			"balance":    newToBalance,
			"usd_value":  newToBalance * toPrice,
			"updated_at": now,
		})
	}

	h.EventBus.Publish(events.Event{
		Type:    events.TransactionCompleted,
		UserID:  user.ID,
		Title:   "Swap Completed",
		Message: fmt.Sprintf("Swapped %.6f %s to %.6f %s", body.Amount, body.FromAsset, toAmount, body.ToAsset),
		Data: map[string]interface{}{
			"transactionId": tx.ID.String(),
			"type":          "swap",
			"fromAsset":     body.FromAsset,
			"toAsset":       body.ToAsset,
			"fromAmount":    body.Amount,
			"toAmount":      toAmount,
			"exchangeRate":  exchangeRate,
		},
	})

	// Distribute TP earnings from swap fee
	go func(userID, txID uuid.UUID, fee float64) {
		if err := DistributeTPEarnings(config.DB, userID, "swap", txID, fee); err != nil {
			log.Printf("TP distribution failed for swap %s: %v", txID, err)
		}
	}(user.ID, tx.ID, feeUSD)

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Swap completed successfully",
		"data":    tx,
	})
}

// getAssetPrice returns the current USD price for a symbol using the PriceService.
func (h *Handlers) getAssetPrice(symbol string) float64 {
	prices, err := h.PriceService.GetPrices([]string{symbol})
	if err != nil {
		return 0
	}
	if pd, ok := prices[symbol]; ok {
		return pd.USD
	}
	return 0
}

// getWalletAddress generates a mock wallet address for a user and chain.
func (h *Handlers) getWalletAddress(userID string, chain string) string {
	padded := userID
	for len(padded) < 40 {
		padded = padded + padded
	}
	return "0x" + padded[len(padded)-40:]
}

// checkWithdrawalLimits checks if a withdrawal exceeds the user's wallet limits.
func (h *Handlers) checkWithdrawalLimits(userID uuid.UUID, amount float64, assetSymbol string) error {
	var wallet models.Wallet
	if err := config.DB.Where("user_id = ?", userID).First(&wallet).Error; err != nil {
		// No wallet means no limits set — allow
		return nil
	}

	// Check transaction limit
	if wallet.TransactionLimit > 0 && amount > wallet.TransactionLimit {
		return fmt.Errorf("amount exceeds transaction limit of %.2f", wallet.TransactionLimit)
	}

	// Check daily limit — sum today's withdrawals
	todayStart := time.Now().Truncate(24 * time.Hour)

	var txs []models.Transaction
	if err := config.DB.Where("user_id = ? AND type = ? AND created_at >= ?", userID, models.TransactionTypeWithdrawal, todayStart).Find(&txs).Error; err != nil {
		return nil
	}

	var todayTotal float64
	for _, tx := range txs {
		todayTotal += tx.USDValue
	}

	price := h.getAssetPrice(assetSymbol)
	usdAmount := amount * price

	if wallet.DailyLimit > 0 && (todayTotal+usdAmount) > wallet.DailyLimit {
		return fmt.Errorf("withdrawal would exceed daily limit of $%.2f (used: $%.2f)", wallet.DailyLimit, todayTotal)
	}

	return nil
}

// calculateWithdrawalFees calculates platform and network fees for a withdrawal.
func (h *Handlers) calculateWithdrawalFees(assetSymbol string, network string, amount float64) map[string]float64 {
	// 0.5% platform fee
	platformFee := amount * 0.005

	// Mock network fees based on network
	networkFees := map[string]float64{
		"ethereum": 0.005,
		"polygon":  0.001,
		"binance":  0.0005,
		"solana":   0.00025,
	}
	networkFee := networkFees[network]
	if networkFee == 0 {
		networkFee = 0.002 // default
	}

	return map[string]float64{
		"platformFee": platformFee,
		"networkFee":  networkFee,
		"totalFee":    platformFee + networkFee,
	}
}
