package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// TransactionHandler handles transaction-related HTTP requests.
type TransactionHandler struct {
	priceService *services.PriceService
}

// NewTransactionHandler creates a new TransactionHandler.
func NewTransactionHandler(ps *services.PriceService) *TransactionHandler {
	return &TransactionHandler{priceService: ps}
}

// GetTransactions returns paginated, filtered transactions.
// GET /api/transactions/ — auth, pagination, filter by type/status/asset/dateRange.
func (h *TransactionHandler) GetTransactions(c *gin.Context) {
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
	skip := int64((page - 1) * limit)

	filter := bson.M{"userId": user.ID}

	if txType := c.Query("type"); txType != "" {
		filter["type"] = txType
	}
	if status := c.Query("status"); status != "" {
		filter["status"] = status
	}
	if asset := c.Query("asset"); asset != "" {
		filter["$or"] = bson.A{
			bson.M{"fromAsset.symbol": asset},
			bson.M{"toAsset.symbol": asset},
		}
	}

	dateFilter := bson.M{}
	if startDate := c.Query("startDate"); startDate != "" {
		if t, err := time.Parse(time.RFC3339, startDate); err == nil {
			dateFilter["$gte"] = t
		}
	}
	if endDate := c.Query("endDate"); endDate != "" {
		if t, err := time.Parse(time.RFC3339, endDate); err == nil {
			dateFilter["$lte"] = t
		}
	}
	if len(dateFilter) > 0 {
		filter["createdAt"] = dateFilter
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("transactions")

	total, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to count transactions"})
		return
	}

	opts := options.Find().
		SetSkip(skip).
		SetLimit(int64(limit)).
		SetSort(bson.M{"createdAt": -1})

	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch transactions"})
		return
	}
	defer cursor.Close(ctx)

	var transactions []models.Transaction
	if err := cursor.All(ctx, &transactions); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to decode transactions"})
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

// GetTransactionDetails returns details for a specific transaction.
// GET /api/transactions/:transactionId — auth, include blockchain confirmation.
func (h *TransactionHandler) GetTransactionDetails(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	txID, err := primitive.ObjectIDFromHex(c.Param("transactionId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid transaction ID"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("transactions")
	var tx models.Transaction
	err = collection.FindOne(ctx, bson.M{"_id": txID, "userId": user.ID}).Decode(&tx)
	if err != nil {
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
func (h *TransactionHandler) GetTransactionStats(c *gin.Context) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("transactions")
	filter := bson.M{
		"userId":    user.ID,
		"createdAt": bson.M{"$gte": startDate},
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch transactions"})
		return
	}
	defer cursor.Close(ctx)

	var transactions []models.Transaction
	if err := cursor.All(ctx, &transactions); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to decode transactions"})
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

// CreateDeposit creates a deposit transaction.
// POST /api/transactions/deposit — auth, validate assetSymbol/amount, create/get asset, calc USD value.
func (h *TransactionHandler) CreateDeposit(c *gin.Context) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get or create asset
	assetsCol := config.GetCollection("assets")
	var asset models.Asset
	err := assetsCol.FindOne(ctx, bson.M{"userId": user.ID, "symbol": body.AssetSymbol}).Decode(&asset)
	if err != nil {
		// Create the asset
		now := time.Now()
		asset = models.Asset{
			ID:       primitive.NewObjectID(),
			UserID:   user.ID,
			Symbol:   body.AssetSymbol,
			Name:     body.AssetSymbol,
			Balance:  0,
			IsHidden: false,
			Details: &models.AssetDetails{
				Type:  models.AssetTypeToken,
				Chain: models.ChainEthereum,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
		_, _ = assetsCol.InsertOne(ctx, asset)
	}

	// Get price and calculate USD value
	price := h.getAssetPrice(body.AssetSymbol)
	usdValue := price * body.Amount
	walletAddr := h.getWalletAddress(user.ID.Hex(), body.Network)

	blockchain := body.Network
	if blockchain == "" {
		blockchain = models.BlockchainEthereum
	}

	now := time.Now()
	tx := models.Transaction{
		ID:     primitive.NewObjectID(),
		UserID: user.ID,
		Type:   models.TransactionTypeDeposit,
		Status: models.TransactionStatusCompleted,
		ToAsset: &models.TransactionAsset{
			Symbol:  body.AssetSymbol,
			Amount:  body.Amount,
			Address: walletAddr,
		},
		Amount:           body.Amount,
		USDValue:         usdValue,
		Blockchain:       blockchain,
		Confirmations:    12,
		RequiredConfirms: 12,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	txCol := config.GetCollection("transactions")
	_, err = txCol.InsertOne(ctx, tx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create deposit"})
		return
	}

	// Update asset balance
	newBalance := asset.Balance + body.Amount
	newUSDValue := price * newBalance
	_, _ = assetsCol.UpdateOne(ctx,
		bson.M{"_id": asset.ID},
		bson.M{"$set": bson.M{"balance": newBalance, "usdValue": newUSDValue, "updatedAt": now}},
	)

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Deposit created successfully",
		"data":    tx,
	})
}

// CreateWithdrawal creates a withdrawal transaction.
// POST /api/transactions/withdraw — auth, check balance, check limits, calc fees: 0.5% platform + network.
func (h *TransactionHandler) CreateWithdrawal(c *gin.Context) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Find the asset
	assetsCol := config.GetCollection("assets")
	var asset models.Asset
	err := assetsCol.FindOne(ctx, bson.M{"userId": user.ID, "symbol": body.AssetSymbol}).Decode(&asset)
	if err != nil {
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
	if err := h.checkWithdrawalLimits(ctx, user.ID, body.Amount, body.AssetSymbol); err != nil {
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
		ID:     primitive.NewObjectID(),
		UserID: user.ID,
		Type:   models.TransactionTypeWithdrawal,
		Status: models.TransactionStatusPending,
		FromAsset: &models.TransactionAsset{
			Symbol:  body.AssetSymbol,
			Amount:  body.Amount,
			Address: h.getWalletAddress(user.ID.Hex(), body.Network),
		},
		ToAsset: &models.TransactionAsset{
			Symbol:  body.AssetSymbol,
			Amount:  body.Amount,
			Address: body.WalletAddress,
		},
		Amount:   body.Amount,
		USDValue: usdValue,
		FeeUSD:   feeUSD,
		Fees: &models.TransactionFees{
			Platform: platformFee,
			Network:  networkFee,
		},
		Blockchain:       blockchain,
		Confirmations:    0,
		RequiredConfirms: 12,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	txCol := config.GetCollection("transactions")
	_, err = txCol.InsertOne(ctx, tx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create withdrawal"})
		return
	}

	// Update asset balance
	newBalance := asset.Balance - totalDeduction
	newUSDValue := newBalance * price
	_, _ = assetsCol.UpdateOne(ctx,
		bson.M{"_id": asset.ID},
		bson.M{"$set": bson.M{"balance": newBalance, "usdValue": newUSDValue, "updatedAt": now}},
	)

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Withdrawal created successfully",
		"data":    tx,
	})
}

// CreateSwap creates a swap transaction between two assets.
// POST /api/transactions/swap — auth, check same asset, check balance, get exchange rate, 0.1% fee, update both balances.
func (h *TransactionHandler) CreateSwap(c *gin.Context) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	assetsCol := config.GetCollection("assets")

	// Find source asset
	var fromAssetDoc models.Asset
	err := assetsCol.FindOne(ctx, bson.M{"userId": user.ID, "symbol": body.FromAsset}).Decode(&fromAssetDoc)
	if err != nil {
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
		ID:     primitive.NewObjectID(),
		UserID: user.ID,
		Type:   models.TransactionTypeSwap,
		Status: models.TransactionStatusCompleted,
		FromAsset: &models.TransactionAsset{
			Symbol: body.FromAsset,
			Amount: body.Amount,
		},
		ToAsset: &models.TransactionAsset{
			Symbol: body.ToAsset,
			Amount: toAmount,
		},
		Amount:   body.Amount,
		USDValue: usdValue,
		FeeUSD:   feeUSD,
		Fees: &models.TransactionFees{
			Platform: fee,
		},
		Metadata: &models.TransactionMetadata{
			ExchangeRate: exchangeRate,
		},
		Blockchain: models.BlockchainInternal,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	txCol := config.GetCollection("transactions")
	_, err = txCol.InsertOne(ctx, tx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create swap"})
		return
	}

	// Update from asset balance
	newFromBalance := fromAssetDoc.Balance - totalDeduction
	_, _ = assetsCol.UpdateOne(ctx,
		bson.M{"_id": fromAssetDoc.ID},
		bson.M{"$set": bson.M{
			"balance":   newFromBalance,
			"usdValue":  newFromBalance * fromPrice,
			"updatedAt": now,
		}},
	)

	// Update or create to asset
	var toAssetDoc models.Asset
	err = assetsCol.FindOne(ctx, bson.M{"userId": user.ID, "symbol": body.ToAsset}).Decode(&toAssetDoc)
	if err != nil {
		// Create the to asset
		toAssetDoc = models.Asset{
			ID:       primitive.NewObjectID(),
			UserID:   user.ID,
			Symbol:   body.ToAsset,
			Name:     body.ToAsset,
			Balance:  toAmount,
			USDValue: toAmount * toPrice,
			Details: &models.AssetDetails{
				Type:  models.AssetTypeToken,
				Chain: models.ChainEthereum,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
		_, _ = assetsCol.InsertOne(ctx, toAssetDoc)
	} else {
		newToBalance := toAssetDoc.Balance + toAmount
		_, _ = assetsCol.UpdateOne(ctx,
			bson.M{"_id": toAssetDoc.ID},
			bson.M{"$set": bson.M{
				"balance":   newToBalance,
				"usdValue":  newToBalance * toPrice,
				"updatedAt": now,
			}},
		)
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Swap completed successfully",
		"data":    tx,
	})
}

// getAssetPrice returns the current USD price for a symbol using the PriceService.
func (h *TransactionHandler) getAssetPrice(symbol string) float64 {
	prices, err := h.priceService.GetPrices([]string{symbol})
	if err != nil {
		return 0
	}
	if pd, ok := prices[symbol]; ok {
		return pd.USD
	}
	return 0
}

// getWalletAddress generates a mock wallet address for a user and chain.
func (h *TransactionHandler) getWalletAddress(userID string, chain string) string {
	padded := userID
	for len(padded) < 40 {
		padded = padded + padded
	}
	return "0x" + padded[len(padded)-40:]
}

// checkWithdrawalLimits checks if a withdrawal exceeds the user's wallet limits.
func (h *TransactionHandler) checkWithdrawalLimits(ctx context.Context, userID primitive.ObjectID, amount float64, assetSymbol string) error {
	walletsCol := config.GetCollection("wallets")
	var wallet models.Wallet
	err := walletsCol.FindOne(ctx, bson.M{"userId": userID}).Decode(&wallet)
	if err != nil {
		// No wallet means no limits set — allow
		return nil
	}

	// Check transaction limit
	if wallet.TransactionLimit > 0 && amount > wallet.TransactionLimit {
		return fmt.Errorf("amount exceeds transaction limit of %.2f", wallet.TransactionLimit)
	}

	// Check daily limit — sum today's withdrawals
	todayStart := time.Now().Truncate(24 * time.Hour)
	txCol := config.GetCollection("transactions")

	cursor, err := txCol.Find(ctx, bson.M{
		"userId":    userID,
		"type":      models.TransactionTypeWithdrawal,
		"createdAt": bson.M{"$gte": todayStart},
	})
	if err != nil {
		return nil
	}
	defer cursor.Close(ctx)

	var todayTotal float64
	var txs []models.Transaction
	if err := cursor.All(ctx, &txs); err == nil {
		for _, tx := range txs {
			todayTotal += tx.USDValue
		}
	}

	price := h.getAssetPrice(assetSymbol)
	usdAmount := amount * price

	if wallet.DailyLimit > 0 && (todayTotal+usdAmount) > wallet.DailyLimit {
		return fmt.Errorf("withdrawal would exceed daily limit of $%.2f (used: $%.2f)", wallet.DailyLimit, todayTotal)
	}

	return nil
}

// calculateWithdrawalFees calculates platform and network fees for a withdrawal.
func (h *TransactionHandler) calculateWithdrawalFees(assetSymbol string, network string, amount float64) map[string]float64 {
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
