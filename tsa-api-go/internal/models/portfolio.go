package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// PortfolioTotalValue tracks portfolio value over different periods.
type PortfolioTotalValue struct {
	Current   float64 `bson:"current" json:"current"`
	Yesterday float64 `bson:"yesterday" json:"yesterday"`
	LastWeek  float64 `bson:"lastWeek" json:"lastWeek"`
	LastMonth float64 `bson:"lastMonth" json:"lastMonth"`
}

// PortfolioAllocation represents the allocation of a single asset in the portfolio.
type PortfolioAllocation struct {
	Asset      primitive.ObjectID `bson:"asset" json:"asset"`
	Symbol     string             `bson:"symbol,omitempty" json:"symbol,omitempty"`
	Value      float64            `bson:"value" json:"value"`
	Percentage float64            `bson:"percentage" json:"percentage"`
}

// PerformanceMetric holds change and percentage values for a time period.
type PerformanceMetric struct {
	Change     float64 `bson:"change" json:"change"`
	Percentage float64 `bson:"percentage" json:"percentage"`
}

// PortfolioPerformance tracks portfolio performance across time periods.
type PortfolioPerformance struct {
	Daily   PerformanceMetric `bson:"daily" json:"daily"`
	Weekly  PerformanceMetric `bson:"weekly" json:"weekly"`
	Monthly PerformanceMetric `bson:"monthly" json:"monthly"`
	Yearly  PerformanceMetric `bson:"yearly" json:"yearly"`
}

// PortfolioHistoryEntry represents a single point in portfolio value history.
type PortfolioHistoryEntry struct {
	Date  time.Time `bson:"date" json:"date"`
	Value float64   `bson:"value" json:"value"`
}

// PortfolioGoalAsset represents an asset within a portfolio goal.
type PortfolioGoalAsset struct {
	Asset  primitive.ObjectID `bson:"asset" json:"asset"`
	Symbol string             `bson:"symbol,omitempty" json:"symbol,omitempty"`
	Amount float64            `bson:"amount" json:"amount"`
}

// PortfolioGoal represents a financial goal within the portfolio.
type PortfolioGoal struct {
	Name          string               `bson:"name,omitempty" json:"name,omitempty"`
	TargetAmount  float64              `bson:"targetAmount" json:"targetAmount"`
	CurrentAmount float64              `bson:"currentAmount" json:"currentAmount"`
	TargetDate    *time.Time           `bson:"targetDate,omitempty" json:"targetDate,omitempty"`
	Assets        []PortfolioGoalAsset `bson:"assets,omitempty" json:"assets,omitempty"`
}

// PortfolioHistory represents a snapshot of portfolio value at a point in time.
type PortfolioHistory struct {
	Date       time.Time `bson:"date" json:"date"`
	TotalValue float64   `bson:"totalValue" json:"totalValue"`
}

// AllocationEntry represents a single asset's allocation within the portfolio.
type AllocationEntry struct {
	Symbol     string  `bson:"symbol" json:"symbol"`
	Name       string  `bson:"name" json:"name"`
	Value      float64 `bson:"value" json:"value"`
	Percentage float64 `bson:"percentage" json:"percentage"`
	Type       string  `bson:"type,omitempty" json:"type,omitempty"`
}

// RiskMetrics holds risk analysis metrics for a portfolio.
type RiskMetrics struct {
	Volatility  float64 `bson:"volatility" json:"volatility"`
	SharpeRatio float64 `bson:"sharpeRatio" json:"sharpeRatio"`
	MaxDrawdown float64 `bson:"maxDrawdown" json:"maxDrawdown"`
	VaR         float64 `bson:"var" json:"var"`
	Beta        float64 `bson:"beta" json:"beta"`
}

// Portfolio represents a user's portfolio summary.
type Portfolio struct {
	ID          primitive.ObjectID      `bson:"_id,omitempty" json:"id,omitempty"`
	UserID      primitive.ObjectID      `bson:"userId" json:"userId"`
	Assets      []primitive.ObjectID    `bson:"assets,omitempty" json:"assets,omitempty"`
	TotalValue  PortfolioTotalValue     `bson:"totalValue" json:"totalValue"`
	Allocation  []PortfolioAllocation   `bson:"allocation,omitempty" json:"allocation,omitempty"`
	Performance PortfolioPerformance    `bson:"performance" json:"performance"`
	History     []PortfolioHistoryEntry `bson:"history,omitempty" json:"history,omitempty"`
	Goals       []PortfolioGoal         `bson:"goals,omitempty" json:"goals,omitempty"`
	LastUpdated *time.Time              `bson:"lastUpdated,omitempty" json:"lastUpdated,omitempty"`
	CreatedAt   time.Time               `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time               `bson:"updatedAt" json:"updatedAt"`
}
