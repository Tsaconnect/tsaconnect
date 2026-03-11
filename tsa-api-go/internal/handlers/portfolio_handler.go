package handlers

import (
	"context"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// riskMetrics holds mock portfolio risk metrics.
type riskMetrics struct {
	Volatility  float64 `json:"volatility"`
	SharpeRatio float64 `json:"sharpeRatio"`
	MaxDrawdown float64 `json:"maxDrawdown"`
	VaR         float64 `json:"valueAtRisk"`
	Beta        float64 `json:"beta"`
}

// allocationEntry is used for detailed allocation responses including asset name and type.
type allocationEntry struct {
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Value      float64 `json:"value"`
	Percentage float64 `json:"percentage"`
	Type       string  `json:"type"`
}

// PortfolioHandler handles portfolio-related HTTP requests.
type PortfolioHandler struct {
	priceService *services.PriceService
}

// NewPortfolioHandler creates a new PortfolioHandler.
func NewPortfolioHandler(ps *services.PriceService) *PortfolioHandler {
	return &PortfolioHandler{priceService: ps}
}

// GetPortfolioOverview returns a portfolio overview for the authenticated user.
// GET /api/portfolio/overview — auth, get/create portfolio, calc allocation, recent txns, top performers.
func (h *PortfolioHandler) GetPortfolioOverview(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get or create portfolio
	portfolioCol := config.GetCollection("portfolios")
	var portfolio models.Portfolio
	err := portfolioCol.FindOne(ctx, bson.M{"userId": user.ID}).Decode(&portfolio)
	if err != nil {
		now := time.Now()
		portfolio = models.Portfolio{
			ID:        primitive.NewObjectID(),
			UserID:    user.ID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		_, _ = portfolioCol.InsertOne(ctx, portfolio)
	}

	// Get user assets
	assetsCol := config.GetCollection("assets")
	cursor, err := assetsCol.Find(ctx, bson.M{"userId": user.ID, "isHidden": bson.M{"$ne": true}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch assets"})
		return
	}
	defer cursor.Close(ctx)

	var assets []models.Asset
	if err := cursor.All(ctx, &assets); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to decode assets"})
		return
	}

	// Fetch prices and calculate totals
	var totalValue, totalChange24h float64
	symbols := make([]string, 0, len(assets))
	for _, a := range assets {
		symbols = append(symbols, a.Symbol)
	}

	if len(symbols) > 0 {
		prices, err := h.priceService.GetPrices(symbols)
		if err == nil {
			for i := range assets {
				if pd, ok := prices[assets[i].Symbol]; ok {
					assets[i].USDValue = assets[i].Balance * pd.USD
					totalValue += assets[i].USDValue
					totalChange24h += pd.USD24hChange
				}
			}
		}
	}

	// Calculate allocation
	allocation := make([]allocationEntry, 0, len(assets))
	for _, a := range assets {
		pct := 0.0
		if totalValue > 0 {
			pct = (a.USDValue / totalValue) * 100
		}
		assetType := ""
		if a.Details != nil {
			assetType = a.Details.Type
		}
		allocation = append(allocation, allocationEntry{
			Symbol:     a.Symbol,
			Name:       a.Name,
			Value:      a.USDValue,
			Percentage: math.Round(pct*100) / 100,
			Type:       assetType,
		})
	}

	// Get recent transactions
	txCol := config.GetCollection("transactions")
	txOpts := options.Find().SetLimit(5).SetSort(bson.M{"createdAt": -1})
	txCursor, err := txCol.Find(ctx, bson.M{"userId": user.ID}, txOpts)
	var recentTxns []models.Transaction
	if err == nil {
		defer txCursor.Close(ctx)
		_ = txCursor.All(ctx, &recentTxns)
	}

	// Top performers
	topPerformers := h.getTopPerformingAssets(assets)

	// Update portfolio
	now := time.Now()
	_, _ = portfolioCol.UpdateOne(ctx,
		bson.M{"_id": portfolio.ID},
		bson.M{"$set": bson.M{
			"totalValue.current": totalValue,
			"updatedAt":          now,
			"lastUpdated":        now,
		}},
	)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Portfolio overview retrieved successfully",
		"data": gin.H{
			"portfolio": gin.H{
				"totalValue":     totalValue,
				"totalChange24h": totalChange24h,
				"assetCount":     len(assets),
			},
			"allocation":         allocation,
			"recentTransactions": recentTxns,
			"topPerformers":      topPerformers,
		},
	})
}

// GetPortfolioPerformance returns portfolio performance over a time period.
// GET /api/portfolio/performance — auth, period: week/month/year/all, filter history.
func (h *PortfolioHandler) GetPortfolioPerformance(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	period := c.DefaultQuery("period", "month")
	now := time.Now()
	var startDate time.Time
	switch period {
	case "week":
		startDate = now.AddDate(0, 0, -7)
	case "month":
		startDate = now.AddDate(0, -1, 0)
	case "year":
		startDate = now.AddDate(-1, 0, 0)
	case "all":
		startDate = time.Time{}
	default:
		startDate = now.AddDate(0, -1, 0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	portfolioCol := config.GetCollection("portfolios")
	var portfolio models.Portfolio
	err := portfolioCol.FindOne(ctx, bson.M{"userId": user.ID}).Decode(&portfolio)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "No portfolio data available",
			"data": gin.H{
				"period":  period,
				"history": []interface{}{},
			},
		})
		return
	}

	// Filter history by period
	var filteredHistory []models.PortfolioHistoryEntry
	for _, entry := range portfolio.History {
		if !startDate.IsZero() && entry.Date.Before(startDate) {
			continue
		}
		filteredHistory = append(filteredHistory, entry)
	}

	// Calculate performance metrics
	var startValue, endValue, totalChange float64
	if len(filteredHistory) > 0 {
		startValue = filteredHistory[0].Value
		endValue = filteredHistory[len(filteredHistory)-1].Value
		if startValue > 0 {
			totalChange = ((endValue - startValue) / startValue) * 100
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Portfolio performance retrieved successfully",
		"data": gin.H{
			"period":      period,
			"startValue":  startValue,
			"endValue":    endValue,
			"totalChange": totalChange,
			"history":     filteredHistory,
		},
	})
}

// GetAssetAllocation returns the asset allocation for the user's portfolio.
// GET /api/portfolio/allocation — auth, calc percentages, group by type.
func (h *PortfolioHandler) GetAssetAllocation(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	assetsCol := config.GetCollection("assets")
	cursor, err := assetsCol.Find(ctx, bson.M{"userId": user.ID, "isHidden": bson.M{"$ne": true}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch assets"})
		return
	}
	defer cursor.Close(ctx)

	var assets []models.Asset
	if err := cursor.All(ctx, &assets); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to decode assets"})
		return
	}

	// Calculate total value
	var totalValue float64
	for _, a := range assets {
		totalValue += a.USDValue
	}

	// Build allocation entries
	allocation := make([]allocationEntry, 0, len(assets))
	byType := make(map[string]float64)

	for _, a := range assets {
		pct := 0.0
		if totalValue > 0 {
			pct = (a.USDValue / totalValue) * 100
		}
		assetType := models.AssetTypeToken
		if a.Details != nil && a.Details.Type != "" {
			assetType = a.Details.Type
		}
		allocation = append(allocation, allocationEntry{
			Symbol:     a.Symbol,
			Name:       a.Name,
			Value:      a.USDValue,
			Percentage: math.Round(pct*100) / 100,
			Type:       assetType,
		})
		byType[assetType] += a.USDValue
	}

	// Group by type
	typeAllocation := make([]gin.H, 0)
	for t, v := range byType {
		pct := 0.0
		if totalValue > 0 {
			pct = (v / totalValue) * 100
		}
		typeAllocation = append(typeAllocation, gin.H{
			"type":       t,
			"value":      v,
			"percentage": math.Round(pct*100) / 100,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset allocation retrieved successfully",
		"data": gin.H{
			"totalValue":     totalValue,
			"allocation":     allocation,
			"typeAllocation": typeAllocation,
		},
	})
}

// GetPortfolioAnalytics returns portfolio analytics.
// GET /api/portfolio/analytics — auth, transaction analytics, risk metrics, diversification score.
func (h *PortfolioHandler) GetPortfolioAnalytics(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get assets
	assetsCol := config.GetCollection("assets")
	cursor, err := assetsCol.Find(ctx, bson.M{"userId": user.ID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch assets"})
		return
	}
	defer cursor.Close(ctx)

	var assets []models.Asset
	if err := cursor.All(ctx, &assets); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to decode assets"})
		return
	}

	// Transaction analytics
	txCol := config.GetCollection("transactions")
	thirtyDaysAgo := time.Now().AddDate(0, -1, 0)
	txCursor, err := txCol.Find(ctx, bson.M{
		"userId":    user.ID,
		"createdAt": bson.M{"$gte": thirtyDaysAgo},
	})

	var totalTxns int
	var totalVolume float64
	typeCounts := map[string]int{}
	if err == nil {
		defer txCursor.Close(ctx)
		var txns []models.Transaction
		if err := txCursor.All(ctx, &txns); err == nil {
			totalTxns = len(txns)
			for _, tx := range txns {
				totalVolume += tx.USDValue
				typeCounts[tx.Type]++
			}
		}
	}

	// Calculate metrics
	rm := h.calculateRiskMetrics(assets)
	diversificationScore := h.calculateDiversificationScore(assets)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Portfolio analytics retrieved successfully",
		"data": gin.H{
			"transactionAnalytics": gin.H{
				"totalTransactions": totalTxns,
				"totalVolume":       totalVolume,
				"typeCounts":        typeCounts,
				"period":            "30d",
			},
			"riskMetrics":          rm,
			"diversificationScore": diversificationScore,
			"assetCount":           len(assets),
		},
	})
}

// CreatePortfolioGoal creates a new portfolio goal.
// POST /api/portfolio/goals — auth, validate name/targetAmount.
// Goals are stored as embedded documents within the portfolio.
func (h *PortfolioHandler) CreatePortfolioGoal(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var body struct {
		Name         string     `json:"name" binding:"required"`
		TargetAmount float64    `json:"targetAmount" binding:"required,gt=0"`
		TargetDate   *time.Time `json:"targetDate"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Name and positive target amount are required"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	goal := models.PortfolioGoal{
		Name:          body.Name,
		TargetAmount:  body.TargetAmount,
		CurrentAmount: 0,
		TargetDate:    body.TargetDate,
	}

	portfolioCol := config.GetCollection("portfolios")

	// Upsert portfolio and push the goal
	now := time.Now()
	result, err := portfolioCol.UpdateOne(ctx,
		bson.M{"userId": user.ID},
		bson.M{
			"$push": bson.M{"goals": goal},
			"$set":  bson.M{"updatedAt": now},
			"$setOnInsert": bson.M{
				"_id":       primitive.NewObjectID(),
				"userId":    user.ID,
				"createdAt": now,
			},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create goal"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Portfolio goal created successfully",
		"data": gin.H{
			"goal":     goal,
			"modified": result.ModifiedCount + result.UpsertedCount,
		},
	})
}

// UpdatePortfolioGoal updates an existing portfolio goal by index (goalId used as goal name).
// PUT /api/portfolio/goals/:goalId — auth.
func (h *PortfolioHandler) UpdatePortfolioGoal(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	goalName := c.Param("goalId")

	var body struct {
		Name          string     `json:"name"`
		TargetAmount  *float64   `json:"targetAmount"`
		CurrentAmount *float64   `json:"currentAmount"`
		TargetDate    *time.Time `json:"targetDate"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request body"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	portfolioCol := config.GetCollection("portfolios")

	update := bson.M{"updatedAt": time.Now()}
	if body.Name != "" {
		update["goals.$.name"] = body.Name
	}
	if body.TargetAmount != nil {
		update["goals.$.targetAmount"] = *body.TargetAmount
	}
	if body.CurrentAmount != nil {
		update["goals.$.currentAmount"] = *body.CurrentAmount
	}
	if body.TargetDate != nil {
		update["goals.$.targetDate"] = body.TargetDate
	}

	result, err := portfolioCol.UpdateOne(ctx,
		bson.M{"userId": user.ID, "goals.name": goalName},
		bson.M{"$set": update},
	)
	if err != nil || result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Goal not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Portfolio goal updated successfully",
		"data": gin.H{
			"goalName": goalName,
			"modified": result.ModifiedCount,
		},
	})
}

// getTopPerformingAssets returns the top 5 assets by daily performance.
func (h *PortfolioHandler) getTopPerformingAssets(assets []models.Asset) []gin.H {
	type assetPerf struct {
		Symbol      string
		Name        string
		DailyChange float64
		USDValue    float64
	}

	perfs := make([]assetPerf, 0, len(assets))
	for _, a := range assets {
		daily := 0.0
		if a.Performance != nil {
			daily = a.Performance.DailyChange
		}
		perfs = append(perfs, assetPerf{
			Symbol:      a.Symbol,
			Name:        a.Name,
			DailyChange: daily,
			USDValue:    a.USDValue,
		})
	}

	sort.Slice(perfs, func(i, j int) bool {
		return perfs[i].DailyChange > perfs[j].DailyChange
	})

	limit := 5
	if len(perfs) < limit {
		limit = len(perfs)
	}

	result := make([]gin.H, 0, limit)
	for _, p := range perfs[:limit] {
		result = append(result, gin.H{
			"symbol":      p.Symbol,
			"name":        p.Name,
			"dailyChange": p.DailyChange,
			"usdValue":    p.USDValue,
		})
	}
	return result
}

// calculateRiskMetrics returns mock risk metrics for the portfolio.
func (h *PortfolioHandler) calculateRiskMetrics(assets []models.Asset) riskMetrics {
	n := float64(len(assets))
	if n == 0 {
		return riskMetrics{}
	}

	return riskMetrics{
		Volatility:  math.Round((15.5+n*0.5)*100) / 100,
		SharpeRatio: math.Round((1.2+n*0.1)*100) / 100,
		MaxDrawdown: math.Round((-12.5-n*0.3)*100) / 100,
		VaR:         math.Round((-5.0-n*0.2)*100) / 100,
		Beta:        math.Round((0.85+n*0.02)*100) / 100,
	}
}

// calculateDiversificationScore calculates an HHI-based diversification score (0-100).
func (h *PortfolioHandler) calculateDiversificationScore(assets []models.Asset) float64 {
	if len(assets) == 0 {
		return 0
	}

	var totalValue float64
	for _, a := range assets {
		totalValue += a.USDValue
	}

	if totalValue == 0 {
		return 0
	}

	// Calculate Herfindahl-Hirschman Index (HHI)
	var hhi float64
	for _, a := range assets {
		share := a.USDValue / totalValue
		hhi += share * share
	}

	// Convert HHI to 0-100 score (lower HHI = more diversified = higher score)
	n := float64(len(assets))
	minHHI := 1.0 / n
	if minHHI >= 1 {
		return 100
	}

	score := ((1 - hhi) / (1 - minHHI)) * 100
	return math.Round(score*100) / 100
}
