package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// PortfolioTotalValue tracks portfolio value over different periods.
type PortfolioTotalValue struct {
	Current   float64 `json:"current"`
	Yesterday float64 `json:"yesterday"`
	LastWeek  float64 `json:"lastWeek"`
	LastMonth float64 `json:"lastMonth"`
}

// PortfolioAllocation represents the allocation of a single asset in the portfolio.
type PortfolioAllocation struct {
	Asset      uuid.UUID `json:"asset"`
	Symbol     string    `json:"symbol,omitempty"`
	Value      float64   `json:"value"`
	Percentage float64   `json:"percentage"`
}

// PerformanceMetric holds change and percentage values for a time period.
type PerformanceMetric struct {
	Change     float64 `json:"change"`
	Percentage float64 `json:"percentage"`
}

// PortfolioPerformance tracks portfolio performance across time periods.
type PortfolioPerformance struct {
	Daily   PerformanceMetric `json:"daily"`
	Weekly  PerformanceMetric `json:"weekly"`
	Monthly PerformanceMetric `json:"monthly"`
	Yearly  PerformanceMetric `json:"yearly"`
}

// PortfolioHistoryEntry represents a single point in portfolio value history.
type PortfolioHistoryEntry struct {
	Date  time.Time `json:"date"`
	Value float64   `json:"value"`
}

// PortfolioGoalAsset represents an asset within a portfolio goal.
type PortfolioGoalAsset struct {
	Asset  uuid.UUID `json:"asset"`
	Symbol string    `json:"symbol,omitempty"`
	Amount float64   `json:"amount"`
}

// PortfolioGoal represents a financial goal within the portfolio.
type PortfolioGoal struct {
	Name          string               `json:"name,omitempty"`
	TargetAmount  float64              `json:"targetAmount"`
	CurrentAmount float64              `json:"currentAmount"`
	TargetDate    *time.Time           `json:"targetDate,omitempty"`
	Assets        []PortfolioGoalAsset `json:"assets,omitempty"`
}

// PortfolioHistory represents a snapshot of portfolio value at a point in time.
type PortfolioHistory struct {
	Date       time.Time `json:"date"`
	TotalValue float64   `json:"totalValue"`
}

// AllocationEntry represents a single asset's allocation within the portfolio.
type AllocationEntry struct {
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Value      float64 `json:"value"`
	Percentage float64 `json:"percentage"`
	Type       string  `json:"type,omitempty"`
}

// RiskMetrics holds risk analysis metrics for a portfolio.
type RiskMetrics struct {
	Volatility  float64 `json:"volatility"`
	SharpeRatio float64 `json:"sharpeRatio"`
	MaxDrawdown float64 `json:"maxDrawdown"`
	VaR         float64 `json:"var"`
	Beta        float64 `json:"beta"`
}

// Portfolio represents a user's portfolio summary.
type Portfolio struct {
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID      uuid.UUID      `gorm:"type:uuid;uniqueIndex" json:"userId"`
	TotalValue  datatypes.JSON `gorm:"type:jsonb" json:"totalValue"`
	Allocation  datatypes.JSON `gorm:"type:jsonb" json:"allocation,omitempty"`
	Performance datatypes.JSON `gorm:"type:jsonb" json:"performance"`
	History     datatypes.JSON `gorm:"type:jsonb" json:"history,omitempty"`
	Goals       datatypes.JSON `gorm:"type:jsonb" json:"goals,omitempty"`
	LastUpdated *time.Time     `json:"lastUpdated,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

// TableName overrides the default table name.
func (Portfolio) TableName() string {
	return "portfolios"
}
