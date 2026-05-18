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
		&models.EmailVerification{},
		&models.PasswordReset{},
		&models.PrivateSaleSubmission{},
		&models.TPEarning{},
		&models.CashbackEarning{},
		&models.RefreshToken{},
		&models.RequestIdempotency{},
		&models.WebhookEvent{},
		&models.P2PPaymentMethod{},
		&models.P2PAd{},
		&models.P2PTrade{},
		&models.P2PMessage{},
		&models.P2PDispute{},
		&models.P2PAuditLog{},
	); err != nil {
		return err
	}

	// P2P trade integrity constraints — buyer cannot equal seller. AutoMigrate
	// doesn't emit CHECK constraints, so this is added explicitly. IF NOT
	// EXISTS keeps repeat boots idempotent.
	DB.Exec(`ALTER TABLE p2p_trades
		DROP CONSTRAINT IF EXISTS p2p_trades_buyer_not_seller`)
	DB.Exec(`ALTER TABLE p2p_trades
		ADD CONSTRAINT p2p_trades_buyer_not_seller CHECK (buyer_id <> seller_id)`)

	// Composite index for the timeout watchdog: it scans trades whose
	// expires_at has elapsed in pending_payment state. Without this index
	// the query is a full-table scan once we have thousands of trades.
	DB.Exec(`CREATE INDEX IF NOT EXISTS idx_p2p_trades_status_expires
		ON p2p_trades (status, expires_at) WHERE status = 'pending_payment'`)

	// Rename smile_job_id → persona_inquiry_id (Persona KYC migration)
	DB.Exec("ALTER TABLE users RENAME COLUMN smile_job_id TO persona_inquiry_id")

	// Reset any in_review users from old provider to pending
	DB.Exec("UPDATE users SET verification_status = 'pending', persona_inquiry_id = '' WHERE verification_status = 'in_review'")

	// Replace the default unique index on wallet_address with a partial
	// unique index that only enforces uniqueness for non-empty values.
	// This allows multiple users to have an empty wallet_address (before
	// they register a wallet).
	DB.Exec("DROP INDEX IF EXISTS idx_users_wallet_address")
	DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_users_wallet_address ON users (wallet_address) WHERE wallet_address != ''")

	return nil
}
