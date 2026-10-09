package services

import (
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// WalletProven reports whether the account has ever proven it owns address:
// a used wallet-link challenge for it (handlers/wallet_proof.go writes them).
func WalletProven(db *gorm.DB, userID uuid.UUID, address string) (bool, error) {
	var n int64
	err := db.Model(&models.WalletLinkChallenge{}).
		Where("user_id = ? AND address = ? AND used_at IS NOT NULL", userID, strings.ToLower(address)).
		Count(&n).Error
	return n > 0, err
}
