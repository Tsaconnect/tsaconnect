package handlers

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
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







