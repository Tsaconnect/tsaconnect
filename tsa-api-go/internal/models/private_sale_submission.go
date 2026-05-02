package models

import (
	"time"

	"github.com/google/uuid"
)

type PrivateSaleSubmission struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name       string    `gorm:"not null" json:"name"`
	Email      string    `gorm:"not null;index" json:"email"`
	Amount     float64   `gorm:"not null" json:"amount"`
	TxHash     string    `gorm:"not null;index" json:"txHash"`
	Status     string    `gorm:"not null;default:'pending';index" json:"status"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (PrivateSaleSubmission) TableName() string {
	return "private_sale_submissions"
}
