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
	// fee_configs gained a nullable chain_id column and the unique index moved
	// from (feature, token_symbol) → (feature, token_symbol, chain_id) so a
	// chain-specific override can coexist with the NULL global row
	// (configurable-FeeManager design §4). AutoMigrate adds the new column +
	// index but never drops the obsolete one, and the old 2-col UNIQUE would
	// reject the new per-chain rows — so drop it explicitly first. Existing rows
	// keep chain_id = NULL (the legacy global), so this is a safe backfill.
	DB.Exec("DROP INDEX IF EXISTS idx_fee_config_feature_token")

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
		&models.FeeConfig{},
		&models.FeeRateIntent{},
		&models.VerificationLog{},
		&models.Order{},
		&models.Deposit{},
		&models.ServiceContactPayment{},
		&models.MerchantRequest{},
		&models.Notification{},
		&models.DeviceToken{},
		&models.EmailVerification{},
		&models.PasswordReset{},
		&models.PrivateSaleSubmission{},
		&models.TPEarning{},
		&models.CashbackEarning{},
		// Fee/TP overhaul (SP3) — tables for the fee/TP engine.
		&models.FeeDistribution{},
		&models.GlobalTPPool{},
		&models.GlobalTPAccrual{},
		// SP4 — global-pool cumulative-index settlement ledger. The new
		// GlobalTPPool index columns and users.last_settled_global_index are
		// added by AutoMigrate on the existing structs above / models.User.
		&models.GlobalTPEpoch{},
		// SP5 — payout computation: per-user claimable-MCGP ledger + checkpoints.
		&models.RewardEntry{},
		&models.RewardClaimable{},
		&models.UserRewardState{},
		// Admin-configurable reward payout-token mapping (flexible payout token).
		&models.RewardPayoutConfig{},
		&models.PrizeDue{},
		// SP7 — claim-eligibility gate (anti-sybil + KYC). An ACTIVE reward_flags
		// row makes a user claim-ineligible (SP6 excludes them from merkle roots).
		&models.RewardFlag{},
		// SP6 — root-publisher: published cumulative merkle epochs + per-(epoch,
		// user) leaf/proof rows. Additive and dormant until an admin publishes.
		&models.RewardDistribution{},
		&models.RewardDistributionEntry{},
		&models.RefreshToken{},
		&models.RequestIdempotency{},
		&models.WebhookEvent{},
		&models.P2PPaymentMethod{},
		&models.P2PAd{},
		&models.P2PTrade{},
		&models.P2PMessage{},
		&models.P2PDispute{},
		&models.P2PAuditLog{},
		&models.P2PMerchantStats{},
		&models.P2PRating{},
		// PM (Payment Merchant) — automated P2P liquidity provider (Phase 1: TSA).
		&models.PaymentMerchant{},
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

	// P2P trade funding discriminator: funding ∈ {0 (pooled sell-ad), 1
	// (deposited-by-taker buy-ad)}. AutoMigrate adds the column (default 0);
	// existing rows are all sell-ads so the constraint add is safe on a
	// non-empty table.
	DB.Exec(`ALTER TABLE p2p_trades
		DROP CONSTRAINT IF EXISTS chk_p2p_trades_funding`)
	DB.Exec(`ALTER TABLE p2p_trades
		ADD CONSTRAINT chk_p2p_trades_funding CHECK (funding IN (0,1))`)

	// P2P ad rate-type constraints: rate_type ∈ {fixed,float}, and
	// float_offset_bps must be present iff rate_type='float'. Existing rows
	// default to rate_type='fixed' / float_offset_bps=NULL so the constraint
	// add is safe on a non-empty table.
	DB.Exec(`ALTER TABLE p2p_ads
		DROP CONSTRAINT IF EXISTS p2p_ads_rate_type_chk`)
	DB.Exec(`ALTER TABLE p2p_ads
		ADD CONSTRAINT p2p_ads_rate_type_chk CHECK (rate_type IN ('fixed','float'))`)
	DB.Exec(`ALTER TABLE p2p_ads
		DROP CONSTRAINT IF EXISTS p2p_ads_float_fields_chk`)
	DB.Exec(`ALTER TABLE p2p_ads
		ADD CONSTRAINT p2p_ads_float_fields_chk CHECK (
			(rate_type = 'fixed' AND float_offset_bps IS NULL) OR
			(rate_type = 'float' AND float_offset_bps IS NOT NULL)
		)`)

	// Composite index for the timeout watchdog: it scans trades whose
	// expires_at has elapsed in pending_payment state. Without this index
	// the query is a full-table scan once we have thousands of trades.
	DB.Exec(`CREATE INDEX IF NOT EXISTS idx_p2p_trades_status_expires
		ON p2p_trades (status, expires_at) WHERE status = 'pending_payment'`)

	// One active trade per (ad, opener) — the concurrent-race backstop for the
	// service-layer dedup guard (a double-submit that slips past the SELECT check
	// fails the second INSERT here, rolling back its capacity debit). opener_id is
	// nullable, so pre-existing rows (NULL) are excluded and don't collide.
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_p2p_active_trade_per_ad_opener
		ON p2p_trades (ad_id, opener_id)
		WHERE opener_id IS NOT NULL AND status IN ('pending_payment','paid','releasing','disputed')`)

	// P2P rating value + one-rating-per-(trade,rater)
	DB.Exec(`ALTER TABLE p2p_ratings
	         DROP CONSTRAINT IF EXISTS p2p_ratings_value_chk`)
	DB.Exec(`ALTER TABLE p2p_ratings
	         ADD CONSTRAINT p2p_ratings_value_chk CHECK (value IN (-1, 1))`)
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS p2p_ratings_trade_rater_uq
	         ON p2p_ratings (trade_id, rater_id)`)

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

	// Backfill P2P payment-method providers for rows created before the
	// `provider` column existed. AutoMigrate defaults new/legacy rows to
	// 'bank_transfer'; refine known mobile-money providers by bank name.
	// Idempotent — only touches rows still on the default.
	DB.Exec(`UPDATE p2p_payment_methods
	         SET provider = 'opay'
	         WHERE provider = 'bank_transfer' AND type = 'mobile_money'
	           AND lower(bank_name) LIKE '%opay%'`)
	DB.Exec(`UPDATE p2p_payment_methods
	         SET provider = 'palmpay'
	         WHERE provider = 'bank_transfer' AND type = 'mobile_money'
	           AND lower(bank_name) LIKE '%palmpay%'`)
	DB.Exec(`UPDATE p2p_payment_methods
	         SET provider = 'kuda'
	         WHERE provider = 'bank_transfer'
	           AND lower(bank_name) LIKE '%kuda%'`)

	// PM (payment-merchant) inbound account must be unique — PMAutomationService
	// resolves the PM by destination account on every inbound Nomba credit
	// (GetByInboundAccount assumes a single match). A partial index enforces
	// uniqueness only over non-blank account numbers so multiple PMs can exist
	// before their inbound account is configured (idempotent, like the others).
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_merchants_inbound_account
	    ON payment_merchants (inbound_account_number) WHERE inbound_account_number <> ''`)

	// Nomba walletId is the real inbound key (data.merchant.walletId): the buy
	// flow resolves the PM by walletId on every settled credit. A partial unique
	// index enforces one PM per non-blank walletId (idempotent, like the others).
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_merchants_nomba_wallet
	    ON payment_merchants (nomba_wallet_id) WHERE nomba_wallet_id <> ''`)

	return nil
}
