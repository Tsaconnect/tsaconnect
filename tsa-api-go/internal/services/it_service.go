package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var (
	ErrITNotConfigured = errors.New("IT not configured")
	ErrITNoRate        = errors.New("IT rate not set")
	ErrITNoPM          = errors.New("Instant Transfer is unavailable: no payment account configured")
	ErrITBadState      = errors.New("invalid IT intent state")
	ErrITInsufficient  = errors.New("insufficient credited premium")
	ErrITFxUnavailable = errors.New("Instant Transfer pricing is temporarily unavailable")
)

const bpsDenom = 10000

// maxBuyPriceUsdMicros caps the admin-set MCGP price at $1,000,000 per MCGP — an
// absurd ceiling that exists only to keep the sell-discount arithmetic
// (price * bps) well clear of int64 overflow.
const maxBuyPriceUsdMicros = 1_000_000 * 1_000_000

var oneE18 = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// FxProvider supplies the NGN/USD rate IT uses to convert MCGP's USD price into
// Naira. It returns the same value other P2P assets quote against, whether that
// value is being served stale, and an error when the feed is unavailable.
type FxProvider interface {
	NgnPerUsd(ctx context.Context) (ngnPerUsd float64, stale bool, err error)
}

// FxMeta describes the FX rate used for a conversion, surfaced to admins.
type FxMeta struct {
	NgnPerUsd float64
	Stale     bool
}

// ITService orchestrates Instant Transfer: rate-locked intents + the operator
// credit/debit/settle/reverse on Contract B. The NGN legs (Nomba pay-in match
// and payout) are admin/operator-driven in this phase — auto-credit is gated on
// the confirmed strong attribution key (see the spec §7.1 blocker), so credit
// and settle/reverse are explicit approvals here, not webhook-automatic.
type ITService struct {
	db          *gorm.DB
	swap        *MCGPSwapService
	fx          FxProvider // NGN/USD source for USD→Naira conversion
	companyPMID string     // PaymentMerchant UUID for the IT pay-into account (optional)

	// Automation seams (all optional — absent keeps the legacy operator-manual
	// flow): payout fires sell payouts through the fiat gateway; va provisions
	// per-intent dynamic virtual accounts for buys. See it_automation.go.
	payout NombaGateway
	va     ITVAProvisioner
}

func NewITService(db *gorm.DB, swap *MCGPSwapService, fx FxProvider) *ITService {
	return &ITService{db: db, swap: swap, fx: fx}
}

// SetCompanyPMID pins which PaymentMerchant IT buyers pay into (from IT_PM_ID).
func (s *ITService) SetCompanyPMID(id string) { s.companyPMID = strings.TrimSpace(id) }

// IsConfigured reports whether IT can operate (swap + operator signer present).
func (s *ITService) IsConfigured() bool {
	return s != nil && s.swap != nil && s.swap.IsConfigured() && s.swap.HasOperator()
}

// Swap exposes the underlying swap service for balance reads.
func (s *ITService) Swap() *MCGPSwapService { return s.swap }

// CompanyPM resolves the active company Payment Merchant whose Nomba inbound
// account buyers pay into (Phase 1: the single seeded TSA PM). Returns nil if
// none is configured so callers surface a clear "not configured" message rather
// than a hardcoded account.
func (s *ITService) CompanyPM(ctx context.Context) (*models.PaymentMerchant, error) {
	// IT ALWAYS uses the explicitly-configured IT_PM_ID — never auto-pick another
	// merchant. If it's unset or not an active PM with an inbound account, return
	// nil so callers surface "payments unavailable" rather than misrouting funds.
	if s.companyPMID == "" {
		return nil, nil
	}
	var pm models.PaymentMerchant
	err := s.db.WithContext(ctx).
		Where("id = ? AND status = ? AND inbound_account_number <> ''",
			s.companyPMID, models.PaymentMerchantStatusActive).
		First(&pm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pm, nil
}

// ─────────────────────────────── rate ─────────────────────────────────────
func (s *ITService) GetActiveRate(ctx context.Context) (*models.ITRate, error) {
	var r models.ITRate
	err := s.db.WithContext(ctx).Where("active = ?", true).Order("created_at DESC").First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrITNoRate
	}
	return &r, err
}

// SetRate deactivates prior rows and inserts a new active rate. The price is the
// MCGP buy price in USD micro-dollars; sellDiscountBps is the sell-leg discount.
func (s *ITService) SetRate(ctx context.Context, adminID uuid.UUID, buyPriceUsdMicros int64, sellDiscountBps int) (*models.ITRate, error) {
	if buyPriceUsdMicros <= 0 || buyPriceUsdMicros > maxBuyPriceUsdMicros {
		return nil, fmt.Errorf("invalid buyPriceUsdMicros")
	}
	if sellDiscountBps < 0 || sellDiscountBps >= bpsDenom {
		return nil, fmt.Errorf("invalid sellDiscountBps")
	}
	rate := &models.ITRate{
		BuyPriceUsdMicros: buyPriceUsdMicros,
		SellDiscountBps:   sellDiscountBps,
		Active:            true,
		UpdatedBy:         &adminID,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&models.ITRate{}).Where("active = ?", true).Update("active", false).Error; e != nil {
			return e
		}
		return tx.Create(rate).Error
	})
	return rate, err
}

// EffectiveKoboRates resolves the live Naira buy/sell rates (kobo per 1 MCGP)
// from the admin-set USD price and the current NGN/USD FX feed. Buy rounds up
// and sell rounds down (vault-favorable, matching the on-chain math). Returns
// ErrITNoRate when no price is set and ErrITFxUnavailable when the FX feed is
// down (stale-but-served data is used and flagged, mirroring other P2P assets).
func (s *ITService) EffectiveKoboRates(ctx context.Context) (buyKobo, sellKobo *big.Int, fx FxMeta, err error) {
	rate, err := s.GetActiveRate(ctx)
	if err != nil {
		return nil, nil, FxMeta{}, err
	}
	if s.fx == nil {
		return nil, nil, FxMeta{}, ErrITFxUnavailable
	}
	ngnPerUsd, stale, ferr := s.fx.NgnPerUsd(ctx)
	if ferr != nil || ngnPerUsd <= 0 {
		return nil, nil, FxMeta{}, ErrITFxUnavailable
	}
	sellPriceUsdMicros := rate.BuyPriceUsdMicros * int64(bpsDenom-rate.SellDiscountBps) / int64(bpsDenom)
	buyKobo = usdMicrosToKobo(rate.BuyPriceUsdMicros, ngnPerUsd, true)
	sellKobo = usdMicrosToKobo(sellPriceUsdMicros, ngnPerUsd, false)
	return buyKobo, sellKobo, FxMeta{NgnPerUsd: ngnPerUsd, Stale: stale}, nil
}

// usdMicrosToKobo converts a USD price (micro-dollars per MCGP) to kobo per MCGP
// at the given NGN/USD rate: kobo = usdMicros/1e6 * ngnPerUsd * 100. roundUp
// favors the vault on the buy leg; the sell leg rounds down.
func usdMicrosToKobo(usdMicros int64, ngnPerUsd float64, roundUp bool) *big.Int {
	kobo := float64(usdMicros) / 1e6 * ngnPerUsd * 100
	if roundUp {
		kobo = math.Ceil(kobo)
	} else {
		kobo = math.Floor(kobo)
	}
	if kobo < 0 {
		kobo = 0
	}
	return new(big.Int).SetInt64(int64(kobo))
}

// mcgpForNgn = ngnKobo * 1e18 / buyRateKobo (round DOWN — vault never over-issues).
func mcgpForNgn(ngnKobo, buyRateKobo *big.Int) *big.Int {
	num := new(big.Int).Mul(ngnKobo, oneE18)
	return num.Div(num, buyRateKobo)
}

// ngnForMcgp = mcgpWei * sellRateKobo / 1e18 (round DOWN — vault never over-pays).
func ngnForMcgp(mcgpWei, sellRateKobo *big.Int) *big.Int {
	num := new(big.Int).Mul(mcgpWei, sellRateKobo)
	return num.Div(num, oneE18)
}

// ─────────────────────────────── buy ──────────────────────────────────────
// CreateBuyIntent locks the rate, computes the MCGP for the given NGN, mints a
// payment reference, and stores a pending intent. The user pays the company PM's
// Nomba VA including PaymentRef; an admin/automation then approves the credit.
func (s *ITService) CreateBuyIntent(ctx context.Context, userID uuid.UUID, walletAddr, ngnKobo string, pmID *uuid.UUID) (*models.ITIntent, error) {
	if !s.IsConfigured() {
		return nil, ErrITNotConfigured
	}
	ngn, ok := new(big.Int).SetString(strings.TrimSpace(ngnKobo), 10)
	if !ok || ngn.Sign() <= 0 {
		return nil, fmt.Errorf("invalid ngnKobo")
	}
	buyRate, _, fx, err := s.EffectiveKoboRates(ctx)
	if err != nil {
		return nil, err
	}
	// Refuse to lock a real money quote against a stale FX rate — the admin
	// preview may show stale data, but the transactional paths must not (mirrors
	// the P2P trade service's requireFreshRate).
	if fx.Stale {
		return nil, ErrITFxUnavailable
	}
	mcgp := mcgpForNgn(ngn, buyRate)
	if mcgp.Sign() <= 0 {
		return nil, fmt.Errorf("amount too small")
	}
	// Resolve the company PM (the pay-into account). IT requires it — fail fast
	// rather than create an unpayable intent with no destination account.
	if pmID == nil {
		pm, e := s.CompanyPM(ctx)
		if e != nil {
			return nil, e
		}
		if pm == nil {
			return nil, ErrITNoPM
		}
		pmID = &pm.ID
	}
	intent := &models.ITIntent{
		ID:            uuid.New(),
		UserID:        userID,
		WalletAddress: walletAddr,
		Direction:     models.ITDirectionBuy,
		Status:        models.ITStatusPendingPayment,
		McgpAmount:    mcgp.String(),
		NgnKobo:       ngn.String(),
		RateKobo:      buyRate.String(),
		PMID:          pmID,
	}
	intent.PaymentRef = "IT-" + strings.ToUpper(strings.ReplaceAll(intent.ID.String(), "-", ""))[:14]
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, err
	}
	return intent, nil
}

// ApproveBuyCredit credits the user's spend-only premium for a paid buy intent.
// Operator-signed; verified on-chain before the intent is marked credited.
// Idempotent: a credited intent returns success.
func (s *ITService) ApproveBuyCredit(ctx context.Context, intentID uuid.UUID, walletAddr string) (*models.ITIntent, error) {
	if !s.IsConfigured() {
		return nil, ErrITNotConfigured
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if intent.Status == models.ITStatusCredited {
		return intent, nil
	}
	if intent.Direction != models.ITDirectionBuy {
		return nil, ErrITBadState
	}
	// Atomically claim the intent so two concurrent admin approvals can't both
	// broadcast (the on-chain usedRef is the backstop; this avoids wasted gas).
	// Claimable only from a non-in-flight state — never from `processing`.
	claimed, err := s.claim(ctx, intent.ID, []string{models.ITStatusPendingPayment, models.ITStatusFailed}, models.ITStatusProcessing)
	if err != nil {
		return nil, err
	}
	if !claimed {
		reloaded, _ := s.getIntent(ctx, intentID)
		if reloaded != nil && reloaded.Status == models.ITStatusCredited {
			return reloaded, nil
		}
		return nil, ErrITBadState // already processing or terminal
	}
	wallet := s.intentWallet(intent, walletAddr)
	amount, _ := new(big.Int).SetString(intent.McgpAmount, 10)
	ref := itRef("IT:credit:", intent.ID)

	txHash, err := s.swap.OperatorCreditPremium(ctx, wallet, amount, ref)
	if err != nil {
		_ = s.setStatus(ctx, intent, models.ITStatusFailed, "credit broadcast: "+err.Error())
		return nil, err
	}
	if _, err := s.swap.VerifyOperatorEvent("PremiumCredited", txHash, common.HexToAddress(wallet), ref); err != nil {
		_ = s.setStatus(ctx, intent, models.ITStatusFailed, "credit verify: "+err.Error())
		return nil, err
	}
	intent.TxHash = txHash
	if err := s.setStatus(ctx, intent, models.ITStatusCredited, ""); err != nil {
		// On-chain credit SUCCEEDED but the status write failed — do NOT report
		// failure; the reconciler/operator must heal the row (see logged warning).
		return intent, nil
	}
	return intent, nil
}

// ─────────────────────────────── sell ─────────────────────────────────────
// CreateSellIntent debits the user's credited premium into pendingSettle
// (on-chain, operator-signed) and records the intent as `debited` — the NGN
// payout + settle/reverse follow. Quotes NGN at the locked sell rate.
func (s *ITService) CreateSellIntent(ctx context.Context, userID uuid.UUID, walletAddr, mcgpWei string, bank *ITSellBank) (*models.ITIntent, error) {
	if !s.IsConfigured() {
		return nil, ErrITNotConfigured
	}
	amount, ok := new(big.Int).SetString(strings.TrimSpace(mcgpWei), 10)
	if !ok || amount.Sign() <= 0 {
		return nil, fmt.Errorf("invalid mcgpAmount")
	}
	// Bound the operator gas/nonce surface: at most one in-flight sell per user.
	// Each sell broadcasts an operator-signed debit, so without this a user could
	// spam dust sells and saturate the shared operator key (review H1).
	var inflight int64
	if err := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("user_id = ? AND direction = ? AND status IN ?", userID, models.ITDirectionSell,
			[]string{models.ITStatusProcessing, models.ITStatusDebited}).
		Count(&inflight).Error; err != nil {
		return nil, err
	}
	if inflight > 0 {
		return nil, fmt.Errorf("you have a sale already in progress; finish it first")
	}
	credited, err := s.swap.PremiumCredited(walletAddr)
	if err != nil {
		return nil, err
	}
	if credited.Cmp(amount) < 0 {
		return nil, ErrITInsufficient
	}
	_, sellRate, fx, err := s.EffectiveKoboRates(ctx)
	if err != nil {
		return nil, err
	}
	if fx.Stale {
		return nil, ErrITFxUnavailable
	}
	ngn := ngnForMcgp(amount, sellRate)
	if ngn.Sign() <= 0 {
		return nil, fmt.Errorf("amount too small")
	}
	intent := &models.ITIntent{
		ID:            uuid.New(),
		UserID:        userID,
		WalletAddress: walletAddr,
		Direction:     models.ITDirectionSell,
		Status:        models.ITStatusProcessing,
		McgpAmount:    amount.String(),
		NgnKobo:       ngn.String(),
		RateKobo:      sellRate.String(),
	}
	if bank != nil {
		intent.BankCode = strings.TrimSpace(bank.Code)
		intent.AccountNumber = strings.TrimSpace(bank.Number)
		intent.AccountName = strings.TrimSpace(bank.Name)
	}
	debitRef := itRef("IT:debit:", intent.ID)
	intent.DebitRef = common.BytesToHash(debitRef[:]).Hex()
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, err
	}

	txHash, err := s.swap.OperatorDebitPremium(ctx, walletAddr, amount, debitRef)
	if err != nil {
		_ = s.setStatus(ctx, intent, models.ITStatusFailed, "debit broadcast: "+err.Error())
		return nil, err
	}
	if _, err := s.swap.VerifyOperatorEvent("PremiumDebitPending", txHash, common.HexToAddress(walletAddr), debitRef); err != nil {
		_ = s.setStatus(ctx, intent, models.ITStatusFailed, "debit verify: "+err.Error())
		return nil, err
	}
	intent.TxHash = txHash
	_ = s.setStatus(ctx, intent, models.ITStatusDebited, "")

	// Auto-payout: fire the fiat leg immediately when the gateway is wired and
	// the intent carries a destination. A payout error is NOT a sell failure —
	// the debit stands and the operator settles manually (legacy path); the
	// error is recorded on the intent for visibility.
	if s.payout != nil && intent.BankCode != "" && intent.AccountNumber != "" {
		if err := s.AutoPayout(ctx, intent); err != nil {
			log.Printf("[it] auto-payout intent=%s failed (operator fallback): %v", intent.ID, err)
		}
	}
	return intent, nil
}

// SettleSell finalizes a sell after a confirmed NGN payout (operator settleDebit).
func (s *ITService) SettleSell(ctx context.Context, intentID uuid.UUID, walletAddr string) (*models.ITIntent, error) {
	return s.resolveSell(ctx, intentID, walletAddr, true)
}

// ReverseSell restores the user's credited premium after a failed NGN payout.
func (s *ITService) ReverseSell(ctx context.Context, intentID uuid.UUID, walletAddr string) (*models.ITIntent, error) {
	return s.resolveSell(ctx, intentID, walletAddr, false)
}

func (s *ITService) resolveSell(ctx context.Context, intentID uuid.UUID, walletAddr string, settle bool) (*models.ITIntent, error) {
	if !s.IsConfigured() {
		return nil, ErrITNotConfigured
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if intent.Status == models.ITStatusSettled || intent.Status == models.ITStatusReversed {
		return intent, nil // idempotent
	}
	if intent.Direction != models.ITDirectionSell || intent.Status != models.ITStatusDebited {
		return nil, ErrITBadState
	}
	wallet := s.intentWallet(intent, walletAddr)
	debitRef := itRef("IT:debit:", intent.ID)
	var txHash string
	var event, doneStatus string
	if settle {
		txHash, err = s.swap.OperatorSettleDebit(ctx, debitRef)
		event, doneStatus = "PremiumDebitSettled", models.ITStatusSettled
	} else {
		txHash, err = s.swap.OperatorReverseDebit(ctx, debitRef)
		event, doneStatus = "PremiumDebitReversed", models.ITStatusReversed
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.swap.VerifyOperatorEvent(event, txHash, common.HexToAddress(wallet), debitRef); err != nil {
		return nil, err
	}
	intent.TxHash = txHash
	_ = s.setStatus(ctx, intent, doneStatus, "")
	return intent, nil
}

// ─────────────────────────────── reads/helpers ────────────────────────────
func (s *ITService) GetIntent(ctx context.Context, id uuid.UUID) (*models.ITIntent, error) {
	return s.getIntent(ctx, id)
}

func (s *ITService) ListUserIntents(ctx context.Context, userID uuid.UUID) ([]models.ITIntent, error) {
	var out []models.ITIntent
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Limit(100).Find(&out).Error
	return out, err
}

func (s *ITService) getIntent(ctx context.Context, id uuid.UUID) (*models.ITIntent, error) {
	var i models.ITIntent
	err := s.db.WithContext(ctx).First(&i, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("intent not found")
	}
	return &i, err
}

// setStatus persists the intent status. Returns the DB error so callers can
// detect an on-chain-succeeded-but-DB-failed desync and avoid reporting a false
// failure (review H2). A failed write past a verified on-chain op is logged as
// RECONCILE NEEDED for the operator/reconciler to heal.
func (s *ITService) setStatus(ctx context.Context, intent *models.ITIntent, status, reason string) error {
	intent.Status = status
	intent.FailureReason = reason
	intent.UpdatedAt = time.Now()
	err := s.db.WithContext(ctx).Model(intent).Updates(map[string]interface{}{
		"status": status, "failure_reason": reason, "tx_hash": intent.TxHash,
	}).Error
	if err != nil {
		log.Printf("[IT] RECONCILE NEEDED: intent %s on-chain op done (tx=%s) but status write to %q failed: %v",
			intent.ID, intent.TxHash, status, err)
	}
	return err
}

// claim atomically transitions an intent from one of `from` to `to`, returning
// true iff exactly this call won (no row matched `to` already). Guards against
// concurrent admin double-broadcast; `to` MUST NOT be in `from`.
func (s *ITService) claim(ctx context.Context, id uuid.UUID, from []string, to string) (bool, error) {
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status IN ?", id, from).
		Update("status", to)
	return res.RowsAffected == 1, res.Error
}

// intentWallet prefers the wallet captured on the intent at creation (the
// address the on-chain op targets), falling back to the supplied one.
func (s *ITService) intentWallet(intent *models.ITIntent, fallback string) string {
	if intent != nil && intent.WalletAddress != "" {
		return intent.WalletAddress
	}
	return fallback
}

// itRef = keccak256(domain || intentID) — domain-separated, single-use on-chain.
func itRef(domain string, id uuid.UUID) [32]byte {
	h := crypto.Keccak256Hash([]byte(domain + id.String()))
	var out [32]byte
	copy(out[:], h.Bytes())
	return out
}
