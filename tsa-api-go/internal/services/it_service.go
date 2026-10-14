package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync"
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
	ErrITInsufficient  = errors.New("insufficient spendable premium")
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

	// fxLoggedAt rate-limits the "pricing unavailable" cause log. The rate
	// endpoint is polled by the app, so an outage would otherwise log on every
	// poll; see logFxUnavailable.
	fxLogMu    sync.Mutex
	fxLoggedAt time.Time

	// minSellNgnKobo is the smallest NGN payout a sell may produce, in kobo.
	// Flutterwave rejects transfers below ₦100 (TRANSFER_AMOUNT_LIMIT), and a
	// sell that clears the on-chain debit before that payout fails strands the
	// user's premium — so this is enforced BEFORE the debit. 0 ⇒ the default.
	minSellNgnKobo int64

	// Automation seams (all optional — absent keeps the legacy operator-manual
	// flow): payout fires sell payouts through the fiat gateway; va provisions
	// per-intent dynamic virtual accounts for buys. See it_automation.go.
	payout NombaGateway
	va     ITVAProvisioner

	// fw is the Flutterwave client for Spec 1 per-user static VA activate.
	fw *FlutterwaveClient

	// centralAccount is the company pay-into account shown in place of a
	// per-user VA, and inboundCentral is whether that mode is actually on.
	// Both are set together by SetInboundCentral and read together by
	// ActiveCentralAccount — never read either directly, or a configured-but-
	// not-enabled account starts routing payers. See inbound_central_account.go.
	centralAccount CentralAccount
	inboundCentral bool

	// feeQuoter prices the fee deducted from each leg; fees carves what the
	// platform keeps into system / cashback / TP. See it_fees.go.
	feeQuoter ITFeeQuoter
	fees      ITFeeSink
	// currencies supplies per-currency payout floors; nil ⇒ NGN floor only.
	currencies *CurrencyRegistry

	// rateQuoter is the live FX quote source for multi-currency withdrawal
	// previews (Flutterwave). Optional — nil surfaces ErrITQuoteUnavailable.
	// See it_quote.go.
	rateQuoter FXRateQuoter

	// quoteCacheMu guards quoteCache, a short-TTL cache of the derived
	// NGN-per-destination-unit rate keyed by destination currency, so repeated
	// withdrawal-quote requests don't each trigger a live Flutterwave probe.
	// See it_quote.go.
	quoteCacheMu sync.Mutex
	quoteCache   map[string]quoteCacheEntry

	// stableSpreadBps and stableRateLockTTL price brokered stablecoin trades.
	// See it_stable_quote.go.
	stableSpreadBps   int64
	stableRateLockTTL time.Duration
	stableRateTolBps  int64
	// stableOverDeliverBps bounds how far a deposit may exceed the quoted
	// amount before it is parked for an operator. See it_stable_sell.go.
	stableOverDeliverBps int64
	// stableMinSellUnits is the provider's floor for a sell, in base units.
	// Below it Flutterwave rejects the conversion — with the user's funds
	// already irreversibly sent — so the quote must refuse first. Nil means
	// unconfigured, which is treated as "no quote" rather than "no floor".
	// See it_stable_quote.go.
	stableMinSellUnits *big.Int

	// Buys the ramp router opens. Nil means the documented defaults; see
	// it_stable_buy_routed.go.
	routed       *routedStableBuyLimits // Flutterwave (RAMP_FLUTTERWAVE_*)
	rampTerm     *rampBuyTerms          // shared (RAMP_DAILY_ABANDON_CAP, RAMP_HONOUR_GRACE_SECONDS)
	treasuryLims *treasuryBuyLimits     // company wallet (RAMP_TREASURY_*)
	// treasuryRates is the company wallet's market rate (two sources that must
	// agree, else one checked against the FX feed, else closed). Nil:
	// company-wallet live pricing is unavailable. See treasury_market_rate.go.
	treasuryRates TreasuryRateSource
	// vaultLedger is the shared-pool ledger (console reads); nil otherwise.
	vaultLedger *VaultIndexer

	// wallet is the company wallet (spec 2.3 #1). Nil: the provider is dormant.
	wallet *CompanyWallet
	// poolDesk is the TSA Liquidity pool (spec 2026-10-08; plan C1 P4). Nil:
	// the pool is dormant (no pool chain, or not wired).
	poolDesk *PoolDesk
	// poolForwards pays each LP for a proven slice through the P2P settlement
	// forwarder (decision 2, plan C2 P7). Nil: proven slices wait, alerted.
	poolForwards *P2PTradeClaimSource
	// converted gates OPENING converted buys and the attach-hash pre-check
	// (spec §15.5). Nothing that settles, sends or hands off reads it (N1).
	converted ConvertedBuys

	// rateNotifier tells a buyer their payment was priced at the live rate
	// (Decision D18). Nil leaves settlement silent. See SetEventBus.
	rateNotifier rateUpdatePublisher

	// stableDepositAddrs holds FLUTTERWAVE's own deposit addresses by chain —
	// never ours. Sell deposits go straight from the user to the provider.
	stableDepositAddrs map[string]string

	// Stablecoin sell deposit verification. See it_stable_sell.go.
	stableLiFi   *LiFiStatusClient
	stableChain  ERC20TransferVerifier
	stableTokens map[string]string // asset symbol → token contract on the deposit chain

	// Stablecoin→fiat conversion at Flutterwave. See it_stable_sell.go.
	stableConverter  stableConverter
	stableTransfers  stableTransferReader
	stableMerchantID string

	// stableDeliverer sends the purchased stablecoin to the buyer's own
	// address. See it_stable_buy.go.
	stableDeliverer stableDeliverer

	// sellChain overrides the swap for the MCGP sell debit path (tests only);
	// nil means s.swap. See chain().
	sellChain itSellChain
	// payoutTransfers reads sell payouts back from the provider — the evidence
	// the sell reconciler and the admin settle/reverse guards act on. See
	// it_sell_recovery.go.
	payoutTransfers itPayoutTransfers
	// fwAbsenceTrusted: whether "no transfer at Flutterwave" may be acted on
	// (IT_SELL_FW_ABSENCE_TRUSTED).
	fwAbsenceTrusted bool
	// sellScan pages the sell reconciler's scans; deepScanMu/lastDeepScan run
	// the unwindowed late-debit audit once a day.
	sellScan     reconcileCursor
	deepScanMu   sync.Mutex
	lastDeepScan time.Time
}

// itSellChain is the slice of the swap contract the MCGP sell leg drives.
type itSellChain interface {
	PremiumBalance(user string) (*big.Int, error)
	OperatorDebitSpendable(ctx context.Context, user string, amount, maxFromBought *big.Int, debitRef [32]byte) (string, error)
	OperatorSettleDebit(ctx context.Context, debitRef [32]byte) (string, error)
	OperatorReverseDebit(ctx context.Context, debitRef [32]byte) (string, error)
	VerifyOperatorEvent(eventName, txHash string, user common.Address, ref [32]byte) (*big.Int, error)
	DebitRecord(ctx context.Context, debitRef [32]byte) (DebitRecord, error)
}

func (s *ITService) chain() itSellChain {
	if s.sellChain != nil {
		return s.sellChain
	}
	return s.swap
}

func NewITService(db *gorm.DB, swap *MCGPSwapService, fx FxProvider) *ITService {
	return &ITService{db: db, swap: swap, fx: fx}
}

// SetCompanyPMID pins which PaymentMerchant IT buyers pay into (from IT_PM_ID).
func (s *ITService) SetCompanyPMID(id string) { s.companyPMID = strings.TrimSpace(id) }

// fwNGNTransferMinKobo is Flutterwave's ₦100 NGN transfer floor
// (TRANSFER_AMOUNT_LIMIT): no payout below it is ever sent.
const fwNGNTransferMinKobo = 10000 // ₦100

// defaultMinSellNgnKobo is the sell payout floor: the transfer minimum.
const defaultMinSellNgnKobo = fwNGNTransferMinKobo

// SetMinSellNgnKobo overrides the minimum sell payout (kobo). A non-positive
// value keeps the default.
func (s *ITService) SetMinSellNgnKobo(kobo int64) {
	if kobo > 0 {
		s.minSellNgnKobo = kobo
	}
}

// minSellKobo returns the effective minimum sell payout in kobo (NGN).
func (s *ITService) minSellKobo() int64 {
	if s.minSellNgnKobo > 0 {
		return s.minSellNgnKobo
	}
	return defaultMinSellNgnKobo
}

// SetCurrencyRegistry wires per-currency payout floors. Optional: without it,
// the NGN floor applies to every currency, which is the pre-multi-currency
// behaviour.
func (s *ITService) SetCurrencyRegistry(r *CurrencyRegistry) { s.currencies = r }

// minPayoutMinorFor returns the floor for a payout currency, in that currency's
// minor units, and a human label for the refusal message.
//
// The floor exists because a payout below the provider's limit fails AFTER the
// on-chain debit, stranding the user's premium — so it has to be enforced up
// front, and it has to be the right number. Flutterwave's limits are per
// currency: ₦100 says nothing about the cedi minimum, and applying the naira
// figure to cedis would reject roughly 100x too much.
//
// A currency with no configured floor is NOT gated. That is deliberate: an
// unconfigured floor means "not yet confirmed with the provider", and inventing
// one would reject legitimate payouts. The provider still rejects a genuinely
// too-small transfer — the cost is that it does so after the debit, which is
// the situation for every currency today anyway.
func (s *ITService) minPayoutMinorFor(currency string) (int64, string) {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if cur == "" || cur == "NGN" {
		return s.minSellKobo(), "NGN"
	}
	if s.currencies == nil {
		return 0, cur
	}
	row, err := s.currencies.Get(cur)
	if err != nil {
		return 0, cur
	}
	return row.MinPayoutMinor, cur
}

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
	// LoadCompanyPM also refuses an LP row (review M4).
	pm, err := LoadCompanyPM(ctx, s.db, s.companyPMID)
	if errors.Is(err, ErrCompanyPMUnusable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pm.InboundAccountNumber == "" {
		return nil, nil
	}
	return pm, nil
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
		s.logFxUnavailable("no FX provider wired")
		return nil, nil, FxMeta{}, ErrITFxUnavailable
	}
	ngnPerUsd, stale, ferr := s.fx.NgnPerUsd(ctx)
	if ferr != nil {
		s.logFxUnavailable(ferr.Error())
		return nil, nil, FxMeta{}, ErrITFxUnavailable
	}
	if ngnPerUsd <= 0 {
		s.logFxUnavailable(fmt.Sprintf("non-positive NGN/USD rate %v", ngnPerUsd))
		return nil, nil, FxMeta{}, ErrITFxUnavailable
	}
	sellPriceUsdMicros := rate.BuyPriceUsdMicros * int64(bpsDenom-rate.SellDiscountBps) / int64(bpsDenom)
	buyKobo = usdMicrosToKobo(rate.BuyPriceUsdMicros, ngnPerUsd, true)
	sellKobo = usdMicrosToKobo(sellPriceUsdMicros, ngnPerUsd, false)
	return buyKobo, sellKobo, FxMeta{NgnPerUsd: ngnPerUsd, Stale: stale}, nil
}

// itFxCauseLogInterval bounds how often the pricing-unavailable cause is
// logged. Long enough that a polled rate endpoint cannot flood the journal
// during an outage, short enough that a recovery-then-relapse is still seen.
const itFxCauseLogInterval = time.Minute

// logFxUnavailable records WHY Instant Transfer pricing is refused.
//
// Every rate-lookup failure reaches the user as ErrITFxUnavailable — a
// deliberately generic message, because the sell handler shows err.Error() to
// the user and the underlying cause is internal. Before this, the cause was
// then dropped entirely: a refused sale left nothing in the logs, so a dead
// feed, a currency with no market anchor and a one-off blip all looked the
// same. It is an ALERT because every IT buy and sell is refused while it holds.
func (s *ITService) logFxUnavailable(cause string) {
	s.fxLogMu.Lock()
	defer s.fxLogMu.Unlock()
	now := time.Now()
	if !s.fxLoggedAt.IsZero() && now.Sub(s.fxLoggedAt) < itFxCauseLogInterval {
		return
	}
	s.fxLoggedAt = now
	log.Printf("[it] ALERT it_fx_unavailable_total=1 cause=%q — Instant Transfer buys and sells are refused until the NGN/USD rate recovers", cause)
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
// payment reference, and stores a pending intent. Used by OpenBuyOrder so the
// user has a visible awaiting-transfer order before the VA webhook settles.
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
		s.logFxUnavailable("NGN/USD rate is stale (buy refused)")
		return nil, ErrITFxUnavailable
	}
	// Fees come off the naira before it buys anything, so the intent quotes the
	// asset the user will actually receive.
	mcgp, fees, err := s.creditableMcgp(ctx, ngn, buyRate, itDepositCurrency)
	if err != nil {
		return nil, err
	}
	// Company PM is optional metadata for Spec 1 standing-VA buys — pay-into is
	// the user's FiatVirtualAccount, not the company inbound. Still attach when
	// configured so admin tooling can group IT volume.
	if pmID == nil {
		pm, e := s.CompanyPM(ctx)
		if e != nil {
			return nil, e
		}
		if pm != nil {
			pmID = &pm.ID
		}
	}
	intent := &models.ITIntent{
		ID:            uuid.New(),
		UserID:        userID,
		WalletAddress: walletAddr,
		Direction:     models.ITDirectionBuy,
		Status:        models.ITStatusPendingPayment,
		Currency:      "NGN",
		McgpAmount:    mcgp.String(),
		NgnKobo:       ngn.String(),
		RateKobo:      buyRate.String(),
		PMID:          pmID,
	}
	applyFees(intent, fees)
	intent.PaymentRef = mintedRefPrefix + strings.ToUpper(strings.ReplaceAll(intent.ID.String(), "-", ""))[:14]
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, err
	}
	return intent, nil
}

// OpenBuyOrder creates a pending buy the user can track while transferring.
// Actual settlement still prices at the live rate when funds land (amount may
// differ from the quote).
//
// ONE open MCGP buy per user, and an existing one is never superseded. It used
// to be: the prior order was cancelled so the hub showed one clear order. But
// the user may already be paying the first one, and under central-account
// attribution a transfer for its amount then matched no open claim and held
// for an operator. So a second order is refused with ITBuyOrderOpenError, which
// names the open order for the app to route to; the user cancels it (CancelBuy)
// if they really meant to start over. Asking again for the open order's own
// amount returns that order instead (see reopenOrRefuse). The partial unique index
// uq_it_intents_open_mcgp_buy backs the check against a double tap.
//
// The ask is snapped to WHOLE NAIRA before the intent is minted (design §5.4).
// Under central-account attribution the amount is half the evidence of who
// paid: a credit is matched by comparing its gross, rounded to naira, against
// this figure. An order for ₦1,000.37 could then never match the ₦1,000 the
// payer's banking app actually let them send, and would hold for an operator
// every single time. Rounding here rather than at the comparison also means the
// user is only ever shown a figure they can actually transfer.
func (s *ITService) OpenBuyOrder(ctx context.Context, userID uuid.UUID, walletAddr, ngnKobo string) (*models.ITIntent, error) {
	// First, so a refused user writes nothing — not even the stale-order sweep.
	if err := requireVerifiedForMoneyIn(ctx, s.db, userID); err != nil {
		return nil, err
	}
	ngnKobo = wholeNairaKobo(ngnKobo)

	// Retire this user's own abandoned order before counting: past the claim
	// window it can no longer be paid into, so it must not wedge them out of
	// buying until the hourly sweep reaches it.
	if _, err := s.expireStaleBuyOrders(ctx, &userID); err != nil {
		return nil, err
	}
	open, err := s.openMCGPBuyOrder(ctx, userID)
	if err != nil {
		return nil, err
	}
	if open != nil {
		return reopenOrRefuse(open, ngnKobo)
	}

	intent, err := s.CreateBuyIntent(ctx, userID, walletAddr, ngnKobo, nil)
	if err != nil && isUniqueViolation(err) {
		// A concurrent request (a double tap) won between the check above and
		// this insert, and the partial unique index refused the loser. Answer
		// exactly as if this request had arrived second.
		if winner, lookupErr := s.openMCGPBuyOrder(ctx, userID); lookupErr == nil && winner != nil {
			return reopenOrRefuse(winner, ngnKobo)
		}
	}
	return intent, err
}

// reopenOrRefuse answers a buy request that found the user's order already
// open. The same whole-naira amount is the user coming back to that order, so
// it is returned as the order opened. It is also the only route back to the
// pay-into details for an app build that predates the open-order screen and
// shows the refusal's message on a dead end. Any other amount is refused and
// names the open order.
func reopenOrRefuse(open *models.ITIntent, wantKobo string) (*models.ITIntent, error) {
	if wholeNairaKobo(open.NgnKobo) == wantKobo {
		return open, nil
	}
	return nil, &ITBuyOrderOpenError{Intent: open}
}

// ExpireStaleBuyOrders cancels unpaid MCGP buy orders past buyOrderClaimWindow,
// and reports how many it closed.
//
// The window already stops such an order from claiming a payment (see
// AutoCreditFromVA). Without this sweep it merely stops mattering: an order
// the user walks away from without cancelling sits in the hub reading
// "awaiting transfer" indefinitely while the settle path ignores it. Telling
// the user it is closed is the honest end state.
//
// Scoped by scopeOpenMCGPBuys, and both of its guards are load-bearing here:
//
//	The MINTED-ref filter. Settlement overwrites payment_ref with the provider
//	charge id the moment money is attributed, so a row without its minted ref
//	has real money against it and must never be cancelled on age.
//
//	MCGP only. Stablecoin purchases have their own expiry
//	(expireStaleStableBuys); sweeping them here would close orders out from
//	under that flow.
//
// Idempotent: a converged state cancels nothing and returns 0.
func (s *ITService) ExpireStaleBuyOrders(ctx context.Context) (int64, error) {
	return s.expireStaleBuyOrders(ctx, nil)
}

// expireStaleBuyOrders is ExpireStaleBuyOrders, optionally for one user only —
// OpenBuyOrder runs it for the caller before applying the one-open-order rule.
func (s *ITService) expireStaleBuyOrders(ctx context.Context, userID *uuid.UUID) (int64, error) {
	q := scopeOpenMCGPBuys(s.db.WithContext(ctx).Model(&models.ITIntent{})).
		Where("created_at < ?", time.Now().Add(-buyOrderClaimWindow))
	if userID != nil {
		q = q.Where("user_id = ?", *userID)
	}
	res := q.Updates(map[string]any{
		"status":         models.ITStatusCancelled,
		"failure_reason": "expired unpaid — start a new order to buy",
	})
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[it] it_buy_orders_expired_total=%d (unpaid past %s)", res.RowsAffected, buyOrderClaimWindow)
	}
	return res.RowsAffected, nil
}

// ApproveBuyCredit credits the user's spend-only premium for a paid buy intent.
// Operator-signed; verified on-chain before the intent is marked credited.
// Idempotent: a credited intent returns success.
func (s *ITService) ApproveBuyCredit(ctx context.Context, intentID uuid.UUID, walletAddr string) (*models.ITIntent, error) {
	return s.approveBuyCreditWithRef(ctx, intentID, walletAddr, nil)
}

// approveBuyCreditWithRef is ApproveBuyCredit with an optional explicit on-chain
// ref. The default ref keys on the intent id, which makes the contract's
// single-use `usedRef` idempotent per INTENT. VA settlement passes a
// charge-derived ref instead so the backstop is idempotent per PROVIDER CHARGE —
// two intents accidentally created for one charge then collide on-chain and the
// second credit is rejected (Spec 1 §3).
func (s *ITService) approveBuyCreditWithRef(ctx context.Context, intentID uuid.UUID, walletAddr string, onChainRef *[32]byte) (*models.ITIntent, error) {
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
	// Parse BEFORE claiming: an unparseable amount would otherwise reach
	// abi.Pack as a nil *big.Int and panic, with the intent already claimed
	// into `processing` and no way back.
	amount, ok := new(big.Int).SetString(strings.TrimSpace(intent.McgpAmount), 10)
	if !ok || amount.Sign() <= 0 {
		return nil, fmt.Errorf("intent %s has unusable mcgpAmount %q", intent.ID, intent.McgpAmount)
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
	ref := itRef("IT:credit:", intent.ID)
	if onChainRef != nil {
		ref = *onChainRef
	}

	txHash, err := s.swap.OperatorCreditPremium(ctx, wallet, amount, ref)
	if err != nil {
		log.Printf("[IT] credit broadcast failed intent=%s operator=%s: %v", intent.ID, s.swap.OperatorAddress(), err)
		_ = s.setStatus(ctx, intent, models.ITStatusFailed, "credit broadcast: "+err.Error())
		return nil, err
	}
	if _, err := s.swap.VerifyOperatorEvent("PremiumCredited", txHash, common.HexToAddress(wallet), ref); err != nil {
		log.Printf("[IT] credit verify failed intent=%s tx=%s: %v", intent.ID, txHash, err)
		_ = s.setStatus(ctx, intent, models.ITStatusFailed, "credit verify: "+err.Error())
		return nil, err
	}
	intent.TxHash = txHash
	if err := s.setStatus(ctx, intent, models.ITStatusCredited, ""); err != nil {
		// On-chain credit SUCCEEDED but the status write failed — do NOT report
		// failure; the reconciler/operator must heal the row (see logged warning).
		s.settleITFee(ctx, intent)
		return intent, nil
	}
	// The user's naira arrived and their (net) MCGP is credited, so the withheld
	// portion is collected — carve it into system / cashback / TP.
	s.settleITFee(ctx, intent)
	return intent, nil
}

// RepriceAndCreditAwaitingRate is the operator recovery for a buy parked as
// `awaiting_rate` — money landed while the FX feed was stale, so the intent was
// stored with no rate and no MCGP (Spec 1 §Error handling). It reprices the
// stored NGN at the CURRENT live rate and credits, keyed on the same
// charge-derived on-chain ref the automatic path would have used, so an
// operator retry can never double-credit.
func (s *ITService) RepriceAndCreditAwaitingRate(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	if !s.IsConfigured() {
		return nil, ErrITNotConfigured
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	// Asset check, not just direction+status: crediting a non-MCGP buy here
	// would issue MCGP from company supply for a purchase made in something
	// else. Stablecoin buys have their own recovery, RepriceParkedStableBuy.
	if intent.Direction != models.ITDirectionBuy ||
		intent.Status != models.ITStatusAwaitingRate ||
		models.ITIntentAsset(intent) != models.ITAssetMCGP {
		return nil, ErrITBadState
	}
	paid, ok := new(big.Int).SetString(strings.TrimSpace(intent.NgnKobo), 10)
	if !ok || paid.Sign() <= 0 {
		return nil, fmt.Errorf("intent %s has no positive ngnKobo to reprice", intent.ID)
	}
	buyRate, _, fx, err := s.EffectiveKoboRates(ctx)
	if err != nil {
		return nil, err
	}
	if fx.Stale {
		s.logFxUnavailable("NGN/USD rate is stale (reprice refused)")
		// Repricing at a stale rate is the exact thing the park exists to prevent.
		return nil, ErrITFxUnavailable
	}
	mcgp, fees, err := s.creditableMcgp(ctx, paid, buyRate, itDepositCurrency)
	if err != nil {
		return nil, err
	}
	applyFees(intent, fees)
	if err := s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
		"rate_kobo":          buyRate.String(),
		"mcgp_amount":        mcgp.String(),
		"fee_ngn_kobo":       intent.FeeNgnKobo,
		"net_ngn_kobo":       intent.NetNgnKobo,
		"platform_kept_kobo": intent.PlatformKeptKobo,
		"status":             models.ITStatusPendingPayment,
		"failure_reason":     "",
	}).Error; err != nil {
		return nil, err
	}
	intent.RateKobo = buyRate.String()
	intent.McgpAmount = mcgp.String()
	intent.Status = models.ITStatusPendingPayment
	ref := itRefKey(itVARefDomain, intent.PaymentRef)
	return s.approveBuyCreditWithRef(ctx, intent.ID, "", &ref)
}

// ─────────────────────────────── sell ─────────────────────────────────────
// CreateSellIntent debits the user's credited premium into pendingSettle
// (on-chain, operator-signed) and records the intent as `debited` — the NGN
// payout + settle/reverse follow. Quotes NGN at the locked sell rate.
//
// Catalog/destination validation (rail vs. currency, account-number format)
// is performed by the HTTP handler before this is ever called — a future
// second caller (e.g. a job or another handler) must re-validate the bank
// destination itself; this method does not repeat that check.
func (s *ITService) CreateSellIntent(ctx context.Context, userID uuid.UUID, walletAddr, mcgpWei string, bank *ITSellBank) (*models.ITIntent, error) {
	if !s.IsConfigured() {
		return nil, ErrITNotConfigured
	}
	// Once the debit is broadcast, stopping half way strands the user's MCGP,
	// so the sell must not die with the HTTP request that started it. Detached
	// from the caller's cancellation, bounded by its own deadline; anything
	// still unresolved at the deadline is picked up by the sell reconciler.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), itSellCreateTimeout)
	defer cancel()
	amount, ok := new(big.Int).SetString(strings.TrimSpace(mcgpWei), 10)
	if !ok || amount.Sign() <= 0 {
		return nil, fmt.Errorf("invalid mcgpAmount")
	}
	// Fail fast on a malformed payout rail before any chain call — no orphan
	// intent, no wasted operator gas.
	if err := validateSellBank(bank); err != nil {
		return nil, err
	}
	// Bound the operator gas/nonce surface: at most one in-flight sell per user.
	// Each sell broadcasts an operator-signed debit, so without this a user could
	// spam dust sells and saturate the shared operator key (review H1).
	var inflight int64
	if err := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("user_id = ? AND direction = ? AND status IN ?", userID, models.ITDirectionSell,
			[]string{models.ITStatusProcessing, models.ITStatusDebiting, models.ITStatusDebited}).
		Count(&inflight).Error; err != nil {
		return nil, err
	}
	if inflight > 0 {
		return nil, fmt.Errorf("you have a sale already in progress; finish it first")
	}
	// Spendable MCGP is spendable everywhere — IT sell, stores, bills — so the
	// sell may draw on the USDC-bought bucket once credited is exhausted, the
	// same way spend()/bills already do. The contract drains credited first.
	spendable, err := s.chain().PremiumBalance(walletAddr)
	if err != nil {
		return nil, err
	}
	if spendable.Cmp(amount) < 0 {
		return nil, ErrITInsufficient
	}
	// Priced through the same path as the pre-trade quote (it_sell_quote.go):
	// live rate (stale refused), fees out of the payout, the payout-currency
	// gate and the NET floor — all before the on-chain debit.
	price, err := s.QuoteSell(ctx, amount, bank.DestinationCurrency)
	if err != nil {
		return nil, err
	}
	fees, sellRate := price.Fees, price.RateKobo
	ngn := fees.Gross
	intent := &models.ITIntent{
		ID:            uuid.New(),
		UserID:        userID,
		WalletAddress: walletAddr,
		Direction:     models.ITDirectionSell,
		Status:        models.ITStatusProcessing,
		Currency:      "NGN",
		McgpAmount:    amount.String(),
		NgnKobo:       ngn.String(),
		RateKobo:      sellRate.String(),
	}
	applyFees(intent, fees)
	applySellBank(intent, bank)
	debitRef := itRef("IT:debit:", intent.ID)
	intent.DebitRef = common.BytesToHash(debitRef[:]).Hex()
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, err
	}

	// maxFromBought = amount: the whole sell may come from bought premium when
	// the user holds no credited premium. The contract records the bought draw
	// per debitRef, so a reversal restores each bucket exactly.
	txHash, err := s.chain().OperatorDebitSpendable(ctx, walletAddr, amount, amount, debitRef)
	if err != nil {
		if errors.Is(err, ErrTxNotBroadcast) {
			_ = s.setStatus(ctx, intent, models.ITStatusFailed, "debit broadcast: "+err.Error())
			return nil, err
		}
		// The send itself failed or timed out: the transaction may still be in
		// the mempool. Only chain state can say, so the reconciler decides.
		_ = s.setStatus(ctx, intent, models.ITStatusDebiting, "debit broadcast unknown: "+err.Error())
		log.Printf("[it] sell intent=%s debit outcome unknown (left debiting for the reconciler): %v", intent.ID, err)
		return intent, fmt.Errorf("%w: %v", ErrITSellDebitPending, err)
	}
	// Keep the hash before verifying: if verification never returns, it is the
	// operator's pointer to the transaction.
	intent.TxHash = txHash
	_ = s.setStatus(ctx, intent, models.ITStatusDebiting, "")
	if _, err := s.chain().VerifyOperatorEvent("PremiumDebitPending", txHash, common.HexToAddress(walletAddr), debitRef); err != nil {
		// Broadcast but unverified (the receipt wait is capped): the debit may
		// still mine. Marking it failed here is what let a user be told "failed"
		// while their MCGP sat debited.
		_ = s.setStatus(ctx, intent, models.ITStatusDebiting, "debit verify: "+err.Error())
		log.Printf("[it] sell intent=%s debit unverified (left debiting for the reconciler): %v", intent.ID, err)
		return intent, fmt.Errorf("%w: %v", ErrITSellDebitPending, err)
	}
	_ = s.setStatus(ctx, intent, models.ITStatusDebited, "")

	// Auto-payout: fire the fiat leg immediately when the gateway is wired and
	// the intent carries a destination. Three outcomes:
	//   sent     — the transfer webhook (or the reconciler) closes the sell;
	//   rejected — no transfer exists and none is coming, so the debit is
	//              reversed now and the caller is told the sale did not go
	//              through (a failed reverse stays marked for the reconciler);
	//   unknown  — left `debited` for the reconciler to resolve from the
	//              provider. Never reversed on a guess: the money may have gone.
	if s.payout != nil && sellIntentHasPayoutDestination(intent) {
		if err := s.AutoPayout(ctx, intent); err != nil {
			if errors.Is(err, ErrITSellPayoutRejected) {
				return s.reverseRejectedSell(ctx, intent, err)
			}
			log.Printf("[it] auto-payout intent=%s outcome unknown (reconciler will resolve): %v", intent.ID, err)
		}
	}
	return intent, nil
}

// SettleSell finalizes a sell after a confirmed NGN payout (operator settleDebit).
func (s *ITService) SettleSell(ctx context.Context, intentID uuid.UUID, walletAddr string) (*models.ITIntent, error) {
	return s.resolveSell(ctx, intentID, walletAddr, true)
}

// ReverseSell restores the user's premium (each bucket exactly) after a failed NGN payout.
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
		if (intent.Status == models.ITStatusSettled) != settle {
			// E.g. a success webhook for a sell already reversed: the user may
			// now hold both the naira and the MCGP. Acknowledged (nothing left to
			// do on-chain) but never silent.
			log.Printf("[it] ALERT it_sell_resolution_conflict intent=%s status=%s requested_settle=%v — operator review required",
				intent.ID, intent.Status, settle)
		}
		return intent, nil // idempotent
	}
	if intent.Direction != models.ITDirectionSell || intent.Status != models.ITStatusDebited {
		return nil, ErrITBadState
	}
	// A brokered stablecoin sell never debited the swap contract — the user's
	// asset went straight to the provider — so there is nothing on-chain to
	// settle or reverse. Calling the contract anyway reverts on the unknown ref,
	// which failed the webhook and had it redelivered forever.
	if models.ITIntentAsset(intent) != models.ITAssetMCGP {
		return s.resolveStableSellPayout(ctx, intent, settle)
	}
	wallet := s.intentWallet(intent, walletAddr)
	debitRef := itRef("IT:debit:", intent.ID)
	var txHash string
	var event, doneStatus string
	if settle {
		txHash, err = s.chain().OperatorSettleDebit(ctx, debitRef)
		event, doneStatus = "PremiumDebitSettled", models.ITStatusSettled
	} else {
		// Claim the row for the reverse BEFORE broadcasting it. The payout send
		// claims the same row with payout_rejected=false, so exactly one of
		// "send the naira" and "return the MCGP" can ever win.
		if cerr := s.claimReverse(ctx, intent); cerr != nil {
			return nil, cerr
		}
		txHash, err = s.chain().OperatorReverseDebit(ctx, debitRef)
		event, doneStatus = "PremiumDebitReversed", models.ITStatusReversed
	}
	if err != nil {
		s.afterResolveFailure(ctx, intent, settle, err)
		return nil, err
	}
	if _, err := s.chain().VerifyOperatorEvent(event, txHash, common.HexToAddress(wallet), debitRef); err != nil {
		s.afterResolveFailure(ctx, intent, settle, fmt.Errorf("tx %s: %w", txHash, err))
		return nil, err
	}
	intent.TxHash = txHash
	_ = s.setStatus(ctx, intent, doneStatus, "")
	// The payout landed and the debit is settled, so the fee is genuinely
	// collected — only now may it fund cashback and TP. A reversal collects
	// nothing and books nothing.
	if settle {
		s.settleITFee(ctx, intent)
	}
	return intent, nil
}

// resolveStableSellPayout closes the payout hop of a brokered stablecoin sell.
// Success settles it. Failure parks it: the user's fiat is still in the
// provider balance, and re-sending the same reference would only replay the
// failure, so it stays `debited` with the rejection recorded — visible in the
// stuck-stablecoin-sell list, and skipped by the stable watcher — for an
// operator to pay out.
func (s *ITService) resolveStableSellPayout(ctx context.Context, intent *models.ITIntent, settle bool) (*models.ITIntent, error) {
	if settle {
		// Status CAS: only the caller that moves the row books the fee, so a
		// redelivered or concurrent success cannot pay the carve twice.
		res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
			Where("id = ? AND status = ?", intent.ID, models.ITStatusDebited).
			Updates(map[string]any{"status": models.ITStatusSettled, "failure_reason": ""})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected != 1 {
			return s.getIntent(ctx, intent.ID)
		}
		intent.Status, intent.FailureReason = models.ITStatusSettled, ""
		s.settleITFee(ctx, intent)
		return intent, nil
	}
	reason := truncateReason("payout FAILED at provider (transfer " + intent.PayoutID + ") — operator payout required")
	if err := s.db.WithContext(ctx).Model(&models.ITIntent{}).Where("id = ?", intent.ID).
		Updates(map[string]any{"payout_rejected": true, "failure_reason": reason}).Error; err != nil {
		return nil, err
	}
	intent.PayoutRejected = true
	intent.FailureReason = reason
	log.Printf("[it] ALERT it_stable_sell_payout_failed intent=%s asset=%s payout=%s — parked for operator payout",
		intent.ID, models.ITIntentAsset(intent), intent.PayoutID)
	return intent, nil
}

// claimReverse takes a debited MCGP sell for reversal. A row already marked
// payout_rejected is already claimed (a send can no longer win it). Otherwise
// the claim is a CAS on exactly the payout state that was read, and — when a
// payout was sent with no transfer id recorded — on the last attempt being old
// enough that it is not still in flight.
func (s *ITService) claimReverse(ctx context.Context, intent *models.ITIntent) error {
	q := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ? AND COALESCE(payout_id, '') = ? AND payout_attempts = ? AND payout_ref_gen = ?",
			intent.ID, models.ITStatusDebited, intent.PayoutID, intent.PayoutAttempts, intent.PayoutRefGen)
	if intent.PayoutRejected {
		// Already claimed — but the claim is only good if the row still looks
		// the way it was read. Re-asserting it here is what stops a stale
		// "rejected" copy reversing a payout that has since gone out.
		q = q.Where("payout_rejected = ?", true)
	} else {
		q = q.Where("payout_rejected = ?", false)
		if intent.PayoutID == "" && intent.PayoutAttempts > 0 {
			// Evaluate the age in the DATABASE: comparing one process's clock to
			// another's stored timestamp would let clock skew weaken a money guard.
			expr, args := olderThanExpr(s.db, "last_payout_attempt_at", itPayoutAbsentSettle)
			q = q.Where("(last_payout_attempt_at IS NULL OR "+expr+")", args...)
		}
	}
	res := q.Update("payout_rejected", true)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: sell %s changed or has a payout in flight; not reversing", ErrITSellPayoutConflict, intent.ID)
	}
	intent.PayoutRejected = true
	return nil
}

// reviewReasonSettle / reviewReasonReverse prefix the failure_reason written
// when a settle or reverse did not verify. ReevaluateSell reads them to know
// which operation the resolved on-chain debit most likely came from.
const (
	reviewReasonSettle  = "settle did not verify"
	reviewReasonReverse = "reverse did not verify"
)

// afterResolveFailure runs when a settle or reverse did not verify. If the
// debit is nonetheless already resolved on-chain, the operation (or another)
// took effect and the row can no longer say which: it moves to needs_review so
// nothing retries the call or pays the sell. A debit still Pending is left as
// is — a reverse keeps its claim, so the row stays unsendable.
func (s *ITService) afterResolveFailure(ctx context.Context, intent *models.ITIntent, settle bool, cause error) {
	rec, err := s.chain().DebitRecord(ctx, itRef("IT:debit:", intent.ID))
	if err != nil || rec.State != DebitStateResolved {
		return
	}
	marker := reviewReasonReverse
	if settle {
		marker = reviewReasonSettle
	}
	s.moveToReview(ctx, intent, []string{models.ITStatusDebited},
		fmt.Sprintf("%s (the debit is already resolved on-chain): %v", marker, cause))
}

// moveToReview parks a sell whose on-chain debit contradicts its row.
func (s *ITService) moveToReview(ctx context.Context, intent *models.ITIntent, from []string, reason string) {
	reason = truncateReason(reason)
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status IN ?", intent.ID, from).
		Updates(map[string]any{"status": models.ITStatusNeedsReview, "failure_reason": reason})
	if res.Error == nil && res.RowsAffected == 1 {
		intent.Status, intent.FailureReason = models.ITStatusNeedsReview, reason
		log.Printf("[it] ALERT it_sell_needs_review intent=%s: %s", intent.ID, reason)
	}
}

// ─────────────────────────────── reads/helpers ────────────────────────────
func (s *ITService) GetIntent(ctx context.Context, id uuid.UUID) (*models.ITIntent, error) {
	return s.getIntent(ctx, id)
}

func (s *ITService) ListUserIntents(ctx context.Context, userID uuid.UUID) ([]models.ITIntent, error) {
	var out []models.ITIntent
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).
		// M14: she never saw a pay-in for these (spec §7.5).
		Where("status <> ? AND NOT (status = ? AND COALESCE(failure_reason, '') = ?)",
			models.ITStatusOpening, models.ITStatusCancelled, models.ITFailurePoolOpenFailed).
		Order("created_at DESC").Limit(100).Find(&out).Error
	return out, err
}

// ListIntentsAdmin returns intents across all users for the operator console,
// newest first, with optional direction/status filters and offset pagination.
// Empty direction/status mean "any". Returns the page plus the unpaginated
// total for the filter (so the UI can show counts / more-pages).
func (s *ITService) ListIntentsAdmin(ctx context.Context, direction, status string, limit, offset int) ([]models.ITIntent, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.ITIntent{})
	if d := strings.TrimSpace(direction); d != "" {
		q = q.Where("direction = ?", d)
	}
	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("status = ?", st)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []models.ITIntent
	if err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
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
	// failure_reason is varchar(256); a raw RPC/revert string can exceed it and
	// a rejected write would strand the intent in `processing`.
	reason = truncateReason(reason)
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
	return itRefKey(domain, id.String())
}

// itRefKey is itRef over an arbitrary key so a ref can be derived from
// something other than the intent id (VA settlement keys on the charge id).
func itRefKey(domain, key string) [32]byte {
	h := crypto.Keccak256Hash([]byte(domain + key))
	var out [32]byte
	copy(out[:], h.Bytes())
	return out
}

// maxFailureReason matches ITIntent.FailureReason's varchar(256).
const maxFailureReason = 256

// mintedRefPrefix marks a payment_ref this service generated at intent creation
// (as opposed to a provider charge id written in at settlement).
const mintedRefPrefix = "IT-"

func truncateReason(reason string) string {
	return truncateRunes(reason, maxFailureReason)
}

// minorLabel renders a currency's symbol for a user-facing minimum, falling back
// to the code itself. Display only — never used to select behaviour.
func minorLabel(currency string) string {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "NGN":
		return "\u20a6"
	case "GHS":
		return "\u20b5"
	case "USD":
		return "$"
	case "EUR":
		return "\u20ac"
	case "GBP":
		return "\u00a3"
	default:
		return strings.ToUpper(strings.TrimSpace(currency)) + " "
	}
}

// exponentFor returns a currency's minor-unit decimal count, defaulting to 2.
//
// The registry carries this precisely so amounts are not rendered with a
// hardcoded /100. Every currency enabled today is 2-decimal, so the default is
// currently always right — but writing the division inline is how the
// assumption gets re-embedded after the field was added to remove it.
func (s *ITService) exponentFor(currency string) int {
	if s.currencies == nil {
		return 2
	}
	row, err := s.currencies.Get(currency)
	if err != nil || row.Exponent < 0 {
		return 2
	}
	return row.Exponent
}

// minorToMajor renders a minor-unit amount in major units for display.
func minorToMajor(minor int64, exponent int) string {
	if exponent <= 0 {
		return strconv.FormatInt(minor, 10)
	}
	div := int64(1)
	for i := 0; i < exponent; i++ {
		div *= 10
	}
	whole := minor / div
	frac := minor % div
	if frac == 0 {
		return strconv.FormatInt(whole, 10)
	}
	return fmt.Sprintf("%d.%0*d", whole, exponent, frac)
}
