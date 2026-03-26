package config

import (
	"fmt"
	"log"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

func ConnectDB(cfg *Config) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	DB = db
	log.Println("Connected to PostgreSQL successfully")
	return db, nil
}

func AutoMigrate() error {
	if err := DB.AutoMigrate(
		&models.User{},
		&models.Asset{},
		&models.Product{},
		&models.Cart{},
		&models.Category{},
		&models.Portfolio{},
		&models.Transaction{},
		&models.Wallet{},
		&models.WalletTransaction{},
		&models.SupportedToken{},
		&models.VerificationLog{},
		&models.Order{},
		&models.Deposit{},
		&models.ServiceContactPayment{},
		&models.MerchantRequest{},
		&models.Notification{},
	); err != nil {
		return err
	}

	// Replace the default unique index on wallet_address with a partial
	// unique index that only enforces uniqueness for non-empty values.
	// This allows multiple users to have an empty wallet_address (before
	// they register a wallet).
	DB.Exec("DROP INDEX IF EXISTS idx_users_wallet_address")
	DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_users_wallet_address ON users (wallet_address) WHERE wallet_address != ''")

	return nil
}
