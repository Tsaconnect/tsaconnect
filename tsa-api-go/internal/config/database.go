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

	// IT rates moved from Naira columns (buy_rate_kobo / sell_rate_kobo) to an
	// admin-set USD price + sell-discount, with Naira derived live from FX. The
	// legacy row carries no USD value and the new columns are NOT NULL, so drop
	// the legacy table and let AutoMigrate recreate it; the admin re-sets the
	// price after deploy (no data migration — dev feature). Guarded on the legacy
	// column so this runs once and never wipes a USD-priced row on later boots.
	if DB.Migrator().HasTable("it_rates") && DB.Migrator().HasColumn(&models.ITRate{}, "buy_rate_kobo") {
		if err := DB.Migrator().DropTable("it_rates"); err != nil {
			return err
		}
	}

	if err := DB.AutoMigrate(
		&models.User{},
		&models.Asset{},
		&models.Product{},
		&models.Cart{},
		&models.Category{},
		&models.CategoryBookingFee{},
		// Three service types (booking/order/contact) — per-service config, menus,
		// availability windows and reserved slots. Additive; only services use them.
		&models.ServiceProfile{},
		&models.ServiceMenuItem{},
		&models.ServiceAvailability{},
		&models.ServiceBooking{},
		&models.BookingRequest{},
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
		// Self-service account deletion audit trail (Apple 5.1.1(v)).
		&models.AccountDeletionRequest{},
		&models.Notification{},
		&models.DeviceToken{},
		&models.EmailVerification{},
		&models.PasswordReset{},
		&models.PrivateSaleSubmission{},
		&models.RewardClaim{},
		&models.FeeSplitConfig{},
		&models.FlutterwaveCustomer{},
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
		// MCGP Swap Contract B — entitlement-ledger swap (premium buy/spend/sell,
		// legacy redeem). Append-only audit + idempotency for /api/mcgp.
		&models.MCGPSwapLedgerEntry{},
		// IT (Instant Transfer) — buy/sell MCGP with Naira via the company PM.
		&models.ITIntent{},
		&models.ITRate{},
		// Spec 1 — permanent per-user static VA for IT bank-transfer buys.
		&models.FiatVirtualAccount{},
		// IT trading agreement — append-only acceptance audit trail.
		&models.ITAgreementAcceptance{},
		// Spot Trade — admin-curated pairs shown on the Spot tab; Buy/Sell reuse
		// existing swap flows. Composite unique index on (base,quote,chain).
		&models.SpotListing{},
		// OTC listing controls — singleton row of per-direction kill-switches
		// (buy/sell) + listing metadata for the app's Easyswap "OTC" tab.
		&models.OTCSettings{},
		// Fee wallet — singleton record of the admin-assigned destination for
		// system fees (seeded from SYSTEM_WALLET_ADDRESS on first read), plus
		// the append-only history of every reassignment.
		&models.FeeWalletSetting{},
		&models.FeeWalletAssignment{},
		// Non-EVM chains — per-wallet derived addresses (keyed wallet_id+chain_key)
		// and cached normalized tx history.
		&models.WalletAddress{},
		&models.WalletActivity{},
	); err != nil {
		return err
	}

	// The admin fee tracker filters and aggregates fee_distributions by date
	// (list ORDER BY created_at DESC, summary WHERE created_at >= ?); without
	// this index both are sequential scans that grow with every settled fee.
	DB.Exec(`CREATE INDEX IF NOT EXISTS idx_fee_distributions_created_at
		ON fee_distributions (created_at DESC)`)

	// TP leaderboard ranks by users.tp_balance; without this index it's a full
	// scan once the table grows. Partial: only rows the leaderboard can return.
	DB.Exec(`CREATE INDEX IF NOT EXISTS idx_users_tp_balance
		ON users (tp_balance DESC) WHERE deleted_at IS NULL`)

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

	// One PM per user: the upsert in PMOnboardingService relies on ON CONFLICT on
	// user_id, which requires a unique constraint (not just a non-unique index).
	// Idempotent — IF NOT EXISTS keeps repeat boots safe. At most one row exists
	// (the TSA seed) so there is no uniqueness violation to worry about.
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_merchants_user
	    ON payment_merchants (user_id)`)

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

	// VA-only corporate model: a corporate PM is a Nomba virtual account under the
	// single parent, matched on every settled inbound by its accountRef. A partial
	// unique index enforces one PM per non-blank accountRef (idempotent, like the
	// others above) so an inbound accountRef resolves to exactly one PM.
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_merchants_nomba_account_ref
	    ON payment_merchants (nomba_account_ref) WHERE nomba_account_ref <> ''`)

	// NGN ledger integrity: available_ngn and reserved_ngn must be non-negative,
	// and reserved_ngn must never exceed available_ngn. The DROP+ADD pattern
	// (like other constraints above) keeps repeat boots idempotent.
	DB.Exec(`ALTER TABLE payment_merchants
		DROP CONSTRAINT IF EXISTS chk_payment_merchants_ngn_ledger`)
	DB.Exec(`ALTER TABLE payment_merchants
		ADD CONSTRAINT chk_payment_merchants_ngn_ledger CHECK (
			CAST(available_ngn AS NUMERIC) >= 0 AND
			CAST(reserved_ngn AS NUMERIC) >= 0 AND
			CAST(reserved_ngn AS NUMERIC) <= CAST(available_ngn AS NUMERIC)
		)`)

	// MCGP swap (Contract B) spend idempotency: the on-chain bytes32 ref is
	// single-use, so its ledger mirror must be unique. Partial index over
	// non-blank refs (buy/sell/legacy/withdraw entries carry no ref). Idempotent.
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_mcgp_swap_ledger_ref
	    ON mcgp_swap_ledger_entries (ref_hash) WHERE ref_hash <> ''`)

	// Buy/sell/legacy/withdraw confirms carry no ref but must still be
	// single-write per (tx, user, direction) — a DB backstop behind the
	// handler's idempotency check. Partial over non-blank tx_hash. Idempotent.
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_mcgp_swap_ledger_tx
	    ON mcgp_swap_ledger_entries (tx_hash, user_id, direction) WHERE tx_hash <> ''`)

	// IT (Instant Transfer) buy intents carry a unique NGN payment reference (the
	// strong attribution key). Partial over non-blank refs. Idempotent.
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_it_intents_payment_ref
	    ON it_intents (payment_ref) WHERE payment_ref <> ''`)

	// Service-type backfill: legacy booking-fare categories (ServiceFeeMode
	// 'booking') become ServiceType 'booking' so they keep their reserve-a-slot
	// flow. All other Service categories default to 'contact' (the AutoMigrate
	// column default), preserving their pay-to-reveal behaviour. Idempotent —
	// only touches rows still on the default.
	DB.Exec(`UPDATE categories SET service_type = 'booking'
	         WHERE service_fee_mode = 'booking'
	           AND (service_type IS NULL OR service_type = '' OR service_type = 'contact')`)

	// Seed the first Spot Trade listing (MCGP/USDC on Sonic) once, at startup —
	// NOT on every read, so an admin who deletes it stays deleted. Guarded on an
	// empty table + a unique (base,quote,chain) index, so repeat boots are no-ops.
	var spotCount int64
	if DB.Model(&models.SpotListing{}).Count(&spotCount); spotCount == 0 {
		DB.Create(&models.SpotListing{
			BaseSymbol: "MCGP", QuoteSymbol: "USDC", Chain: "sonic",
			Route: models.SpotRouteMCGP, Enabled: true, DisplayOrder: 0,
		})
	}

	return nil
}
