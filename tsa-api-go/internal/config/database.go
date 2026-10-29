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

// migratePayMerchantLiveSwitch adds pay_merchants.live_enabled and, in the same
// transaction, switches on every merchant that already holds a live key, so the
// per-merchant live switch breaks no integration that was live before it existed.
//
// It runs only while the column is missing. That is what makes the backfill
// one-time: once a super admin has switched a merchant off, no later migrate may
// switch it back on. Adding the column here, before AutoMigrate, rather than
// letting AutoMigrate add it keeps the column and its backfill atomic — a failed
// backfill leaves the column absent and the next migrate retries both. A fresh
// database has no table yet; AutoMigrate creates it with the column and there is
// nothing to backfill.
func migratePayMerchantLiveSwitch(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable(&models.PayMerchant{}) || m.HasColumn(&models.PayMerchant{}, "live_enabled") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Migrator().AddColumn(&models.PayMerchant{}, "LiveEnabled"); err != nil {
			return err
		}
		res := tx.Exec(`UPDATE pay_merchants SET live_enabled = true WHERE live_key_hash IS NOT NULL`)
		if res.Error != nil {
			return res.Error
		}
		log.Printf("[Migrate] pay_merchants.live_enabled added; %d merchant(s) holding a live key kept live", res.RowsAffected)
		return nil
	})
}

// migrateCategoryServiceType adds categories.service_type and, in the same
// transaction, moves legacy booking-fare categories (service_fee_mode
// 'booking') to service type 'booking' so they keep their reserve-a-slot flow;
// every other category takes the column default 'contact'.
//
// It runs only while the column is missing, like migratePayMerchantLiveSwitch.
// The column default is 'contact', so a backfill run on every boot could not
// tell a never-classified category from one an admin later set to 'contact',
// and kept flipping the admin's choice back to 'booking'.
func migrateCategoryServiceType(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable(&models.Category{}) || m.HasColumn(&models.Category{}, "service_type") {
		return nil
	}
	hasFeeMode := m.HasColumn(&models.Category{}, "service_fee_mode")
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Migrator().AddColumn(&models.Category{}, "ServiceType"); err != nil {
			return err
		}
		if !hasFeeMode {
			return nil // no fee-mode column, so no booking-fare category to carry over
		}
		res := tx.Exec(`UPDATE categories SET service_type = 'booking' WHERE service_fee_mode = 'booking'`)
		if res.Error != nil {
			return res.Error
		}
		log.Printf("[Migrate] categories.service_type added; %d booking-fare categor(ies) set to booking", res.RowsAffected)
		return nil
	})
}

// backfillCategorySlugs gives every category without a slug one derived from
// its title, de-duplicated against every slug already taken. It only ever
// fills NULLs, so it is safe on every boot and never rewrites a slug an admin
// set; it also catches a category an older release created mid-deploy.
func backfillCategorySlugs(db *gorm.DB) error {
	var missing []models.Category
	if err := db.Select("id", "title").Where("slug IS NULL OR slug = ''").
		Order("created_at ASC, id ASC").Find(&missing).Error; err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	var existing []string
	if err := db.Model(&models.Category{}).Where("slug IS NOT NULL AND slug <> ''").
		Pluck("slug", &existing).Error; err != nil {
		return err
	}
	taken := make(map[string]bool, len(existing)+len(missing))
	for _, s := range existing {
		taken[s] = true
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, c := range missing {
			slug := models.UniqueSlug(models.Slugify(c.Title), func(s string) bool { return taken[s] })
			taken[slug] = true
			if err := tx.Model(&models.Category{}).Where("id = ? AND (slug IS NULL OR slug = '')", c.ID).
				UpdateColumn("slug", slug).Error; err != nil {
				return err
			}
		}
		log.Printf("[Migrate] categories.slug backfilled for %d categor(ies)", len(missing))
		return nil
	})
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

	// Same move again for the transaction-tax work: fee_configs gained a
	// nullable direction column and the unique index widened from
	// (feature, token_symbol, chain_id) → (..., direction) so a per-leg rate can
	// coexist with the NULL any-direction row. The 3-col UNIQUE would reject the
	// new rows, so drop it explicitly before AutoMigrate adds the 4-col one.
	// Existing rows keep direction = NULL and resolve exactly as before.
	DB.Exec("DROP INDEX IF EXISTS idx_fee_config_feature_token_chain")

	if err := migratePayMerchantLiveSwitch(DB); err != nil {
		return fmt.Errorf("pay_merchants.live_enabled: %w", err)
	}

	if err := migrateCategoryServiceType(DB); err != nil {
		return fmt.Errorf("categories.service_type: %w", err)
	}

	if err := DB.AutoMigrate(
		// Currency registry — the sole validation authority for any currency
		// reaching a money path. Rows are seeded per-row (never overwritten) by
		// services.NewCurrencyRegistry at boot, so operator-tuned payout floors
		// survive a deploy.
		&models.Currency{},
		// Hot keys' nonce allocations (one call per nonce, DB-authoritative
		// across processes) and every signing of them, written before its
		// broadcast (signer consolidation).
		&models.KeyLaneNonce{},
		&models.KeyLaneTx{},
		&models.KeyLaneState{},
		&models.KeyLaneRetiredOp{},
		// Per-currency PM fiat balances. Supersedes payment_merchants'
		// available_ngn/reserved_ngn, which can only express one implicit NGN
		// balance; both are written until the legacy columns are dropped.
		&models.PMBalance{},
		&models.User{},
		&models.Product{},
		&models.PromoClaim{},
		&models.Cart{},
		&models.DeliveryAddress{},
		&models.Category{},
		&models.CategoryBookingFee{},
		// Field catalogue for category listing/booking forms; seeded below.
		&models.FieldDef{},
		// Three service types (booking/order/contact) — per-service config, menus,
		// availability windows and reserved slots. Additive; only services use them.
		&models.ServiceProfile{},
		&models.ServiceMenuItem{},
		&models.ServiceAvailability{},
		&models.ServiceBooking{},
		&models.BookingRequest{},
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
		&models.ServiceContactIntent{},
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
		&models.TPProgramConfig{},
		&models.FlutterwaveCustomer{},
		&models.TPEarning{},
		&models.CashbackEarning{},
		// Fee/TP overhaul (SP3) — tables for the fee/TP engine.
		&models.FeeDistribution{},
		&models.PendingSwapFee{},
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
		&models.SellerPayoutMethod{},
		&models.SellerPayoutChange{},
		&models.SellerPayoutOrder{},
		&models.SellerPayoutSetting{},
		&models.P2PAd{},
		&models.P2PTrade{},
		&models.PoolOrder{},
		&models.LiquidityLedgerEntry{},
		&models.FiatLPDeposit{},
		&models.FiatLPWithdrawal{},
		&models.LiquidityIndexCursor{},
		&models.P2PMessage{},
		&models.P2PDispute{},
		&models.P2PAuditLog{},
		&models.P2PMerchantStats{},
		&models.P2PRating{},
		// Instant per-trade sweep of a merchant's naira proceeds to their own
		// bank (central-account design decision 7). Also the idempotency claim
		// that stops a replayed inbound credit crediting the ledger twice.
		&models.P2PSettlementForward{},
		// PM (Payment Merchant) — automated P2P liquidity provider (Phase 1: TSA).
		&models.PaymentMerchant{},
		// MCGP Swap Contract B — entitlement-ledger swap (premium buy/spend/sell,
		// legacy redeem). Append-only audit + idempotency for /api/mcgp.
		&models.MCGPSwapLedgerEntry{},
		// IT (Instant Transfer) — buy/sell MCGP with Naira via the company PM.
		&models.ITIntent{},
		&models.JobRunMarker{},
		&models.BillPayment{},
		// Utility bills v2 — what was bought (ClubKonnect vend); the MCGP money
		// leg lives in BillPayment, keyed by order ID.
		&models.BillOrder{},
		// User complaints about bill orders, reviewed by admins. One per order.
		&models.BillReport{},
		// Marketplace orders paid from Spendable MCGP — the operator-debit hold
		// that replaces ProductEscrow for MCGP. Keyed by order ID.
		&models.OrderMcgpPayment{},
		&models.Dispute{},
		&models.ITRate{},
		// Spec 1 — permanent per-user static VA for IT bank-transfer buys.
		&models.FiatVirtualAccount{},
		// Central-account inbound attribution — the immutable record of every
		// credit into the company account, written by the statement poller
		// before any matching runs.
		&models.InboundCredit{},
		&models.InboundPayerDeclaration{},
		// Append-only record of every operator decision on a held credit.
		// Separate from inbound_credits because that row only ever shows the
		// current state — who acted, and on what, has to outlive it.
		&models.InboundCreditAuditLog{},
		&models.PaycrestOrderRecord{},
		// One row per transaction we paid gas for on a user's behalf; the
		// (order_id, kind) unique index is the once-per-order rule.
		&models.GasSponsorship{},
		// Ramp conversion (Phase 2) — one row per on-chain leg of a converted
		// Paycrest order. (order_id, kind, attempt) unique index is the
		// once-per-attempt rule.
		&models.RampLeg{},
		&models.ITIntentAuditLog{},
		// Ramp conversion (sell) — one row per sale in progress: which swap
		// was hers, which quote it executed, whether the payout is open. The
		// unique index on swap_tx_hash is the "one swap backs one sale" rule;
		// the partial one-open-intent index is built below (AutoMigrate
		// cannot express a WHERE clause).
		&models.RampSellIntent{},
		// IT trading agreement — append-only acceptance audit trail.
		&models.ITAgreementAcceptance{},
		// Spot Trade — admin-curated pairs shown on the Spot tab; Buy/Sell reuse
		// existing swap flows. Composite unique index on (base,quote,chain).
		// quote_chain arrives here as a plain additive column: it is nullable
		// with no backfill because every pre-existing listing has both legs on
		// `chain`, which is exactly what an empty quote chain means. Nothing is
		// dropped or rewritten, so the older release still serving during a
		// deploy keeps working against the same rows.
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
		// TSA Pay (merchant payments)
		&models.PayMerchant{},
		&models.PayPayment{},
		&models.PayRefund{},
		&models.PayWebhookDelivery{},
		&models.PayAuditEvent{},
		&models.PayOwnerAction{},
		&models.PayOpenLink{},
		&models.PayRetiredSlug{},
		// Owners' business renames and whether payouts may use them.
		&models.PayMerchantNameChange{},
		// Verified users' name changes by re-verification, with their fees.
		&models.UserNameChange{},
		// TSA Connect web sign-in
		&models.WebSigninChallenge{},
		&models.WebTrustedBrowser{},
		// Proof of wallet ownership before a wallet is linked to an account
		&models.WalletLinkChallenge{},
	); err != nil {
		return err
	}

	// Field catalogue seed: per row, never overwritten (an admin's edit to a
	// system field survives every boot).
	if err := models.SeedFieldDefs(DB); err != nil {
		return fmt.Errorf("field_defs seed: %w", err)
	}
	if err := backfillCategorySlugs(DB); err != nil {
		return fmt.Errorf("categories.slug backfill: %w", err)
	}

	// A TSA Pay payment expires only when its merchant set an expiry (owner,
	// 2026-10-09), so expires_at is NULL on one that never does. AutoMigrate
	// does not relax an existing NOT NULL; this does, and is idempotent.
	if err := DB.Exec(`ALTER TABLE pay_payments ALTER COLUMN expires_at DROP NOT NULL`).Error; err != nil {
		return err
	}
	// With no default expiry, untried open-link payments (an amount entered,
	// never paid) are kept, not swept, and the owner's payment list hides
	// them. This index serves that list without reading them.
	if err := DB.Exec(`CREATE INDEX IF NOT EXISTS idx_pay_payments_owner_list
		ON pay_payments (merchant_id, mode, created_at DESC) WHERE NOT (` + models.PayUntriedOpenSQL + `)`).Error; err != nil {
		log.Printf("[Migrate] WARNING could not build idx_pay_payments_owner_list: %v", err)
	}

	// The 4-column UNIQUE GORM builds from the struct tags does NOT make a
	// fee rate unique in Postgres: NULLs compare as DISTINCT in a unique index,
	// so two identical (feature, token, NULL chain, NULL direction) rows both
	// insert cleanly. Every row written to date carries both NULLs, so nothing
	// in this table is actually deduplicated — and a duplicate is a money bug:
	// the cache keeps whichever row Load() saw last, and the replica writer /
	// drift reconciler pick one by UUID order.
	//
	// This expression index restores the invariant the column list only implies.
	// It is additive: on a table that already holds duplicates it simply fails
	// to build, logged below, and the operator de-duplicates before it takes.
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_fee_config_feature_token_chain_direction
		ON fee_configs (feature, token_symbol, COALESCE(chain_id, -1), lower(COALESCE(direction, '')))`).Error; err != nil {
		log.Printf("[Migrate] WARNING could not build uq_fee_config_feature_token_chain_direction: %v — fee_configs most likely holds DUPLICATE (feature, token, chain, direction) rows, which resolve non-deterministically. De-duplicate it.", err)
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

	// Category search filters match listing values by JSON containment
	// (listing_values @> {...}); GIN keeps that off a sequential scan.
	DB.Exec(`CREATE INDEX IF NOT EXISTS idx_products_listing_values
		ON products USING GIN (listing_values) WHERE listing_values IS NOT NULL`)

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

	// TSA Liquidity slices (spec §7.4): the order lookup and one live slice per
	// LP per generation. uq_p2p_active_trade_per_ad_opener is untouched: a
	// slice's opener_id is NULL (S10).
	if err := MigratePoolSliceIndexes(DB); err != nil {
		log.Printf("[Migrate] WARNING pool slice indexes: %v", err)
	}

	// P2P rating value + one-rating-per-(trade,rater)
	DB.Exec(`ALTER TABLE p2p_ratings
	         DROP CONSTRAINT IF EXISTS p2p_ratings_value_chk`)
	DB.Exec(`ALTER TABLE p2p_ratings
	         ADD CONSTRAINT p2p_ratings_value_chk CHECK (value IN (-1, 1))`)
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS p2p_ratings_trade_rater_uq
	         ON p2p_ratings (trade_id, rater_id)`)

	// KYC provider column lineage: smile_job_id → persona_inquiry_id → didit_session_id.
	// AutoMigrate above has already added didit_session_id from the model, so a
	// RENAME would fail ("column already exists") and — because the error is
	// swallowed — leave the stale column behind forever, re-running the
	// in_review reset on every boot and wiping legitimate Didit sessions.
	// The old provider IDs are dead data: DROP the stale columns instead. The
	// drop is what makes the guarded reset genuinely one-time.
	if DB.Migrator().HasColumn(&models.User{}, "smile_job_id") {
		DB.Exec("ALTER TABLE users DROP COLUMN smile_job_id")
	}
	if DB.Migrator().HasColumn(&models.User{}, "persona_inquiry_id") {
		DB.Exec("ALTER TABLE users DROP COLUMN persona_inquiry_id")
		DB.Exec("UPDATE users SET verification_status = 'pending', didit_session_id = '' WHERE verification_status = 'in_review'")
	}

	// NOTE: the `assets` and `portfolios` tables and the wallets balance columns
	// are retired by deploy/one-off/2026-09-01-drop-db-balance-mirrors.sql, NOT
	// here. Crypto balances are read from the chain, so the mirrors are dead —
	// but dropping them at boot would break the older release still serving from
	// this database mid-deploy. The models are gone, so AutoMigrate simply stops
	// maintaining them; leftover tables are inert until an operator runs that
	// script.

	// One wallet per account, compared case-insensitively (an address in a
	// different case is the same wallet). Never fatal — production may hold
	// case-variant duplicates, which are alerted and must be resolved by hand;
	// see migrateWalletAddressUniqueness.
	migrateWalletAddressUniqueness(DB)

	// Same move for phone_number: signup no longer requires it, so multiple users
	// can legitimately have an empty phone number. The plain UNIQUE GORM built
	// from the old `uniqueIndex` tag treats '' as a value, so the second empty
	// phone number would collide. Replace it with a partial index that only
	// deduplicates non-empty numbers (and still rejects two users sharing one).
	DB.Exec("DROP INDEX IF EXISTS idx_users_phone_number")
	DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_users_phone_number ON users (phone_number) WHERE phone_number != ''")

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

	// One row per (user, role): a user may be both a payment merchant and an
	// LP (owner, 2026-10-13). Built CONCURRENTLY on the live table; the old
	// one-per-user index is dropped only by deploy/one-off/2026-10-13-…sql.
	if err := MigratePaymentMerchantRoleIndex(DB); err != nil {
		log.Printf("[Migrate] WARNING %s: %v — a second role cannot be added until it exists", PaymentMerchantRoleIndexName, err)
	}
	// One fee payment per service contact: a contact paid by a plain transfer (contracts v2 slice 5)
	// is proven by its tx, so one tx must never unlock two contacts.
	if err := MigrateServiceContactFeeTxIndex(DB); err != nil {
		log.Printf("[Migrate] ALERT %s: %v — duplicate fee txs? find them with SELECT LOWER(pay_fee_tx_hash), count(*) FROM service_contact_payments WHERE pay_fee_tx_hash <> '' GROUP BY 1 HAVING count(*) > 1", ServiceContactFeeTxIndexName, err)
	}
	// One order per OrderEscrow key: the escrow watcher and claimTimeout settlement find the
	// order by it, so two rows mirroring one on-chain order would settle the wrong one.
	if err := MigrateOrderEscrowKeyIndex(DB); err != nil {
		log.Printf("[Migrate] ALERT %s: %v — duplicate escrow keys? find them with SELECT escrow_order_key, count(*) FROM orders WHERE escrow_order_key <> '' GROUP BY 1 HAVING count(*) > 1", OrderEscrowKeyIndexName, err)
	}
	// One LP account per wallet (shared pool, spec 2026-10-14 §5): a vault
	// position must never be read or claimed through two accounts.
	if err := MigrateLPWalletIndex(DB); err != nil {
		log.Printf("[Migrate] ALERT %s: %v — duplicate LP wallets? find them with SELECT LOWER(operator_wallet_address), count(*) FROM payment_merchants WHERE kind = 'lp' AND operator_wallet_address <> '' GROUP BY 1 HAVING count(*) > 1", LPWalletIndexName, err)
	}

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

	// Per-currency PM balances carry the SAME integrity guarantee as the NGN
	// columns they replace. Porting this is not optional: it is the last line of
	// defence under every ledger operation, and without it the multi-currency
	// table would silently hold a weaker invariant than the single-currency one
	// it supersedes — a regression that would only surface as a negative or
	// over-reserved balance in production.
	DB.Exec(`ALTER TABLE pm_balances
		DROP CONSTRAINT IF EXISTS chk_pm_balances_ledger`)
	DB.Exec(`ALTER TABLE pm_balances
		ADD CONSTRAINT chk_pm_balances_ledger CHECK (
			CAST(available AS NUMERIC) >= 0 AND
			CAST(reserved AS NUMERIC) >= 0 AND
			CAST(reserved AS NUMERIC) <= CAST(available AS NUMERIC)
		)`)

	// One-time backfill: every existing PM gets an NGN row mirroring its legacy
	// columns. Guarded on absence rather than on an empty table so a PM created
	// between deploys is still picked up. Dual-write (pm_ngn_ledger.go) keeps
	// the two representations in step until the legacy columns are dropped.
	DB.Exec(`INSERT INTO pm_balances (pm_id, currency, available, reserved, created_at, updated_at)
		SELECT id, 'NGN', available_ngn, reserved_ngn, NOW(), NOW()
		FROM payment_merchants pm
		WHERE NOT EXISTS (
			SELECT 1 FROM pm_balances b WHERE b.pm_id = pm.id AND b.currency = 'NGN'
		)`)

	// IT intents gain a currency + minor-unit columns. Backfill marks every
	// existing intent NGN and copies each kobo column into its minor twin —
	// identical values, since minor units ARE kobo for naira. Guarded on
	// currency being unset so it runs once and never rewrites a row that a
	// later release has denominated in something else.
	DB.Exec(`UPDATE it_intents SET
			currency            = 'NGN',
			amount_minor        = ngn_kobo,
			rate_minor          = rate_kobo,
			fee_minor           = fee_ngn_kobo,
			net_minor           = net_ngn_kobo,
			platform_kept_minor = platform_kept_kobo
		WHERE currency IS NULL OR currency = ''`)

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

	// IT stablecoin-sell deposits: the on-chain fact that a transfer landed at
	// Flutterwave's shared merchant address proves the transfer happened, NOT
	// which user sent it — the chain is public and the address is the same for
	// everyone. Without this, the SAME deposit tx could be attached to multiple
	// intents and each would verify successfully. One deposit funds at most one
	// intent. Partial over non-blank hashes, since both columns are empty for
	// buy intents and for sells that have not reported/confirmed yet.
	// Idempotent.
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_it_intents_deposit_tx_hash
	    ON it_intents (deposit_tx_hash) WHERE deposit_tx_hash <> ''`)
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_it_intents_settled_tx_hash
	    ON it_intents (settled_tx_hash) WHERE settled_tx_hash <> ''`)

	// IT stablecoin-buy "one open purchase per user": CreateStableBuyIntent's
	// Count-then-Create is not atomic, so a losing race must fail at the
	// database rather than mint a second claimable intent for the same
	// standing VA reference. Pool `opening` included (TSA Liquidity S11, plan
	// C1 P2). Built under a new name first; the old definition is dropped only
	// once the new one exists.
	if err := MigrateOpenStableBuyIndex(DB); err != nil {
		log.Printf("[Migrate] WARNING %s: %v — the one-open check still runs in the service", OpenStableBuyIndexName, err)
	}

	// IT MCGP-buy "one open order per user": OpenBuyOrder no longer supersedes
	// a prior order (that made the order the user was paying vanish, and in
	// central mode held their transfer), it refuses. Its check-then-create is
	// not atomic, so a double tap must fail here and is reported as the same
	// refusal. The predicate is services.scopeOpenMCGPBuys in SQL — change them
	// together.
	//
	// Additive, and NON-FATAL on existing data: the previous release
	// superseded before inserting, but a race there could still leave one user
	// with two open orders, and then the build fails. That must not stop the
	// migration — the application-level check still refuses a second order,
	// only the double-tap backstop is missing. Cancel the older duplicate(s)
	// and the next migrate builds it.
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_it_intents_open_mcgp_buy
	    ON it_intents (user_id)
	    WHERE direction = 'buy' AND status = 'pending_payment'
	      AND (asset IS NULL OR asset = '' OR asset = 'MCGP')
	      AND payment_ref LIKE 'IT-%'`).Error; err != nil {
		log.Printf("[Migrate] WARNING could not build uq_it_intents_open_mcgp_buy: %v — it_intents most likely holds a user with MORE THAN ONE open MCGP buy order (direction=buy, status=pending_payment, MCGP/empty asset, payment_ref LIKE 'IT-%%'). The one-open-order check still runs, but a double tap can mint a second order until the older duplicate(s) are cancelled and migrate is re-run.", err)
	}

	// Ramp treasury refund — "at most one refund IN FLIGHT per order": the
	// existing (order_id, kind, attempt) unique index only rejects two callers
	// landing on the SAME attempt number, and Reserve's read-then-decide
	// NextAttempt call is not atomic with the insert — two independent callers
	// (the automatic Advance/refund() path and an admin's RefundFromTreasury)
	// can each compute a DIFFERENT, non-colliding attempt number and both go on
	// to broadcast a refund for the same order. This is the one remaining path
	// to paying a user's refund twice, and closing it needs a constraint the
	// application cannot enforce by reading first. Partial over PENDING refund
	// legs only: once a refund confirms, the row's status leaves this index,
	// same as every other partial index here. Idempotent.
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_ramp_legs_one_pending_refund
	    ON ramp_legs (order_id)
	    WHERE kind = 'refund' AND status = 'pending'`)

	// Seller payouts — at most one deposit leg per payout. Kind 'deposit' was
	// reserved and unused, so no existing row can block the build. Idempotent.
	if err := DB.Exec(models.RampLegsOneLiveDepositIndexSQL).Error; err != nil {
		log.Printf("[Migrate] WARNING could not build uq_ramp_legs_one_live_deposit: %v — a second deposit for one payout is then stopped only by the machine, not the database", err)
	}

	// Company wallet — "one live sale per order" (spec 2026-10-03 §5.4). Kind
	// 'sale' is new, so no existing row can block the build. Idempotent.
	if err := DB.Exec(models.RampLegsOneLiveSaleIndexSQL).Error; err != nil {
		log.Printf("[Migrate] WARNING could not build uq_ramp_legs_one_live_sale: %v — the company wallet stays dormant without it", err)
	}

	// Ramp sell — "at most one sale in progress per user" (design §5.2). Keep
	// the WHERE list equal to models.SellIntentOpenStatuses. The
	// service checks it first, but the check is read-then-insert: two taps on
	// one button both read "none open" and both insert. Only a constraint can
	// refuse the second, and a second open intent means two swaps pointed at
	// one payout. Partial over the open statuses only, so finishing or
	// abandoning a sale frees her to start another. Idempotent.
	//
	// Per user AND purpose since marketplace checkouts are funded through the
	// same intents (purpose 'marketplace'): a sale and a checkout being funded
	// are different slots, and each still holds at most one. Every row written
	// before the purpose column reads '' — the old index's exact meaning — so
	// the old index is dropped only once this one is built, and a failed build
	// leaves the old one in place.
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_ramp_sell_intents_one_open_per_purpose
	    ON ramp_sell_intents (user_id, purpose)
	    WHERE status IN ('swapping','swapped','ordering')`).Error; err != nil {
		log.Printf("[Migrate] WARNING could not build uq_ramp_sell_intents_one_open_per_purpose: %v — ramp_sell_intents most likely holds a user with MORE THAN ONE open sale (or checkout funding). The one-open check still runs in the service, but a double tap can open a second until the duplicates are failed and migrate is re-run.", err)
		if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_ramp_sell_intents_one_open
		    ON ramp_sell_intents (user_id)
		    WHERE status IN ('swapping','swapped','ordering')`).Error; err != nil {
			log.Printf("[Migrate] WARNING could not build uq_ramp_sell_intents_one_open either: %v", err)
		}
	} else {
		DB.Exec(`DROP INDEX IF EXISTS uq_ramp_sell_intents_one_open`)
	}

	// Marketplace checkout funding — at most one open funding swap per checkout
	// group, the same second line of defence the per-payment index is for TSA
	// Pay. Partial over non-null reference_id and the open statuses. Idempotent.
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_ramp_sell_intents_one_open_per_reference
	    ON ramp_sell_intents (reference_id)
	    WHERE reference_id IS NOT NULL AND status IN ('swapping','swapped','ordering')`).Error; err != nil {
		log.Printf("[Migrate] WARNING could not build uq_ramp_sell_intents_one_open_per_reference: %v — a checkout group most likely holds MORE THAN ONE open funding swap. The one-open check still runs in the service; migrate again once the duplicates are failed.", err)
	}

	// TSA Pay — "at most one open sale per PAYMENT" (a plain index on
	// pay_payment_id is not unique: a payment can legitimately have several
	// sell_intent rows over its life, one per attempt, so a bare unique index
	// would refuse a retry after the first attempt was abandoned or failed).
	// The user-level index above already caps her to one open sale system-wide
	// while a payment is locked to her, so this is a second line of defence,
	// not the only one — the same posture as every other partial index here:
	// a two-open-intents-for-one-payment bug is exactly a second swap able to
	// pay (or be mistaken for) the same attempt. Partial over the open
	// statuses and non-null pay_payment_id only. Idempotent.
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_ramp_sell_intents_one_open_per_payment
	    ON ramp_sell_intents (pay_payment_id)
	    WHERE pay_payment_id IS NOT NULL AND status IN ('swapping','swapped','ordering')`).Error; err != nil {
		log.Printf("[Migrate] WARNING could not build uq_ramp_sell_intents_one_open_per_payment: %v — a TSA Pay payment most likely holds MORE THAN ONE open sale. The one-open-sale check still runs in the service; migrate again once the duplicates are resolved.", err)
	}

	// Name change — "at most one open change per user". The service checks
	// first, but that is read-then-insert: two taps both read "none open" and
	// both pay. Only a constraint refuses the second. Keep the WHERE list equal
	// to models.NameChangeTerminalStatuses. Idempotent.
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_user_name_changes_one_open
	    ON user_name_changes (user_id)
	    WHERE status NOT IN ('approved','rejected','lapsed','cancelled')`).Error; err != nil {
		log.Printf("[Migrate] WARNING could not build uq_user_name_changes_one_open: %v — a user most likely holds MORE THAN ONE open name change. The service check still runs; close the duplicates and migrate again.", err)
	}

	// Booking timezone backfill: availability windows were always wall-clock in
	// the merchant's own zone, but were read as UTC, so every slot surfaced an
	// hour early in Lagos. Existing profiles adopt the platform default rather
	// than being migrated — stored booking instants are left alone. Idempotent.
	DB.Exec(`UPDATE service_profiles SET timezone = 'Africa/Lagos'
	         WHERE timezone IS NULL OR timezone = ''`)

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

// OpenStableBuyIndexName is the one-open-buy index (plan C1 P2).
const OpenStableBuyIndexName = "uq_it_intents_open_stable_buy_v2"

// MigrateOpenStableBuyIndex builds the v2 one-open-buy index (pending_payment
// and the pool's opening) and drops the v1 one only after it exists, so a
// failed build never leaves the table without the backstop. Idempotent.
func MigrateOpenStableBuyIndex(db *gorm.DB) error {
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ` + OpenStableBuyIndexName + `
	    ON it_intents (user_id)
	    WHERE direction = 'buy' AND status IN ('pending_payment','opening')
	      AND asset IN ('USDC','USDT')`).Error; err != nil {
		return err
	}
	return db.Exec(`DROP INDEX IF EXISTS uq_it_intents_open_stable_buy`).Error
}

// MigratePoolSliceIndexes is spec §7.4's two slice indexes. Idempotent.
func MigratePoolSliceIndexes(db *gorm.DB) error {
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_p2p_trades_it_intent
	    ON p2p_trades (it_intent_id) WHERE it_intent_id IS NOT NULL`).Error; err != nil {
		return err
	}
	return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_pool_slice_per_order_gen_ad
	    ON p2p_trades (it_intent_id, generation, ad_id)
	    WHERE it_intent_id IS NOT NULL AND status <> 'cancelled'`).Error
}

// ServiceContactFeeTxIndexName keeps one fee tx from paying for two contacts.
const ServiceContactFeeTxIndexName = "uq_service_contact_payments_fee_tx"

// MigrateServiceContactFeeTxIndex builds the fee-tx index without locking out writes.
func MigrateServiceContactFeeTxIndex(db *gorm.DB) error {
	if uniqueIndexReady(db, "service_contact_payments", ServiceContactFeeTxIndexName) {
		return nil
	}
	return buildUniqueIndex(db, ServiceContactFeeTxIndexName,
		` ON service_contact_payments (LOWER(pay_fee_tx_hash)) WHERE pay_fee_tx_hash <> ''`)
}

// OrderEscrowKeyIndexName keeps one order per OrderEscrow order key.
const OrderEscrowKeyIndexName = "uq_orders_escrow_order_key"

// MigrateOrderEscrowKeyIndex builds the escrow-key index without locking out writes.
func MigrateOrderEscrowKeyIndex(db *gorm.DB) error {
	if uniqueIndexReady(db, "orders", OrderEscrowKeyIndexName) {
		return nil
	}
	return buildUniqueIndex(db, OrderEscrowKeyIndexName, ` ON orders (escrow_order_key) WHERE escrow_order_key <> ''`)
}

// LPWalletIndexName keeps one LP row per wallet (case-insensitive).
const LPWalletIndexName = "uq_payment_merchants_lp_wallet"

// MigrateLPWalletIndex builds the LP wallet index without locking out writes.
func MigrateLPWalletIndex(db *gorm.DB) error {
	if uniqueIndexReady(db, "payment_merchants", LPWalletIndexName) {
		return nil
	}
	return buildUniqueIndex(db, LPWalletIndexName,
		` ON payment_merchants (LOWER(operator_wallet_address)) WHERE kind = 'lp' AND operator_wallet_address <> ''`)
}

// PaymentMerchantRoleIndexName keeps one payment_merchants row per (user, kind).
const PaymentMerchantRoleIndexName = "uq_payment_merchants_user_kind"

// MigratePaymentMerchantRoleIndex builds the role index without locking out
// writes, rebuilding an INVALID leftover. Returns at once when it is ready.
func MigratePaymentMerchantRoleIndex(db *gorm.DB) error {
	if uniqueIndexReady(db, "payment_merchants", PaymentMerchantRoleIndexName) {
		return nil
	}
	return buildUniqueIndex(db, PaymentMerchantRoleIndexName, ` ON payment_merchants (user_id, kind)`)
}
