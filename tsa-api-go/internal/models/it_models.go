package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// IT (Instant Transfer) — buy/sell MCGP with Naira via the company PM. Buys
// credit Contract B's premiumCredited bucket; sells debit spendable premium
// (credited first, then bought).

// IT intent directions + statuses.
const (
	ITDirectionBuy  = "buy"  // NGN in  -> creditPremium
	ITDirectionSell = "sell" // debitPremium -> NGN out

	ITStatusPendingPayment = "pending_payment" // buy: awaiting the user's NGN transfer
	ITStatusProcessing     = "processing"      // operator on-chain op in flight
	ITStatusCredited       = "credited"        // buy complete (premium credited)
	ITStatusDebiting       = "debiting"        // sell: debit broadcast (or attempted) but not confirmed — resolved from chain state
	ITStatusDebited        = "debited"         // sell: on-chain debit done, NGN payout in flight
	// ITStatusNeedsReview: a sell whose debit is already settled or reversed
	// on-chain but whose row never recorded which — nothing automated may send
	// or resolve it again; an operator reads the chain and closes it.
	ITStatusNeedsReview = "needs_review"
	ITStatusSettled     = "settled"  // sell complete (NGN paid, backing freed)
	ITStatusReversed    = "reversed" // sell payout failed, premium restored
	ITStatusFailed      = "failed"
	ITStatusCancelled   = "cancelled"
	// ITStatusAwaitingRate parks a VA credit when FX is stale at settlement —
	// money already landed; never credit at a stale rate (Spec 1).
	ITStatusAwaitingRate = "awaiting_rate"
)

const (
	ITAssetMCGP = "MCGP"
	ITAssetUSDC = "USDC"
	ITAssetUSDT = "USDT"
)

const (
	// Stablecoin sell states. The chain ends at the EXISTING debited → settled
	// pair so AutoPayout and ResolvePayoutFromWebhook are reused unchanged.
	ITStatusPendingDeposit   = "pending_deposit"   // awaiting the user's on-chain send
	ITStatusDepositReported  = "deposit_reported"  // user reported a source tx hash
	ITStatusDepositConfirmed = "deposit_confirmed" // LiFi DONE + verified on Polygon
	ITStatusConverting       = "converting"        // FW stablecoin→fiat conversion in flight
)

// ── Stablecoin buy (Phase B1) ──
// pending_payment is reused from the MCGP buy flow: it means the same thing
// here, and it is the ONLY state that can claim an incoming charge.
const (
	ITStatusPaymentReceived = "payment_received" // charge matched to this intent
	ITStatusAwaitingFunds   = "awaiting_funds"   // naira not yet spendable at FW
	ITStatusDelivering      = "delivering"       // crypto transfer accepted, not settled
	// ITStatusBuyAwaitingRate parks a STABLECOIN buy whose money landed but
	// could not be priced. Deliberately NOT the MCGP flow's awaiting_rate:
	// RepriceAndCreditAwaitingRate is already wired to that status and would
	// credit MCGP from company supply, then set the intent back to
	// pending_payment — re-opening it for the next incoming charge to claim.
	ITStatusBuyAwaitingRate = "buy_awaiting_rate"
)

// ITIntentAsset reads an intent's asset, treating rows written before the
// column existed as MCGP. Like ITIntentCurrency, this fallback is scoped to
// historical rows and lives in exactly one place so it cannot quietly become
// the general default — the column itself has NO default.
func ITIntentAsset(i *ITIntent) string {
	if i == nil || i.Asset == "" {
		return ITAssetMCGP
	}
	return i.Asset
}

// ITIntent is one IT buy or sell. Amounts are base-unit decimal strings (MCGP
// 18dp wei; NGN in kobo). The rate is locked at creation.
type ITIntent struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`

	Direction string `gorm:"size:8;not null;index" json:"direction"`
	Status    string `gorm:"size:20;not null;default:'pending_payment';index" json:"status"`

	// WalletAddress is captured at intent creation and is the address the on-chain
	// credit/debit targets — used for event verification so a later wallet
	// rotation can't strand the intent (review M2).
	WalletAddress string `gorm:"size:42" json:"walletAddress"`

	// ── The numeric-column rule ──
	// GORM puts every field WITHOUT a column `default:` tag into the INSERT
	// whether or not it was set, so an unset Go string arrives at PostgreSQL as
	// ''::numeric and the whole INSERT is rejected (SQLSTATE 22P02). SQLite's
	// type affinity accepts it silently, so the SQLite-backed suite cannot see
	// it — internal/services/it_stable_postgres_test.go is the counterweight.
	//
	// Every OPTIONAL numeric column below therefore carries `default:0`: GORM
	// omits a zero-value field that has one and lets the database supply the
	// value, which makes "a new write path forgot this column" structurally
	// impossible instead of a thing each literal must remember.
	//
	// Five columns are deliberately EXCLUDED from that rule:
	//
	//   McgpAmount, NgnKobo, RateKobo (the `not null` trio) are the quote
	//   itself. A path that fails to set them has not priced the trade, and a
	//   default would turn that into a live intent quoting zero rather than an
	//   INSERT that fails before any money moves.
	//
	//   NetNgnKobo and its NetMinor twin are the payout amount, and "" is
	//   load-bearing there: payoutKobo (it_fees.go) reads an empty net as
	//   "created before fee deduction shipped, settle at the gross". A default
	//   of 0 would turn that legacy reading into "pay this person nothing".
	//
	// For all five, an unset value is a bug the INSERT should reject, not a
	// value the database should invent.
	McgpAmount string `gorm:"type:numeric(78,0);not null" json:"mcgpAmount"` // wei

	// Asset selects which leg runs: MCGP moves company inventory on-chain,
	// USDC/USDT are brokered through Flutterwave and never held by us.
	//
	// Deliberately has NO column default — see ITIntentAsset.
	Asset string `gorm:"size:8;index" json:"asset"`

	// AssetAmount is the asset leg in its own base units (6 decimals for both
	// stablecoins). For MCGP intents it mirrors McgpAmount via BeforeSave;
	// McgpAmount stays authoritative until it is dropped.
	AssetAmount string `gorm:"type:numeric(78,0);default:0" json:"assetAmount,omitempty"`

	// Currency is the fiat leg's ISO 4217 code. Validated against the currency
	// registry — never against display metadata.
	//
	// Deliberately has NO column default. A default would make every write path
	// that forgets this field silently produce an NGN-denominated intent, and
	// for a cedi amount that is a ~100x mispricing. Rows written before this
	// column existed are read as NGN by ITIntentCurrency() — that fallback is
	// scoped to historical rows and lives in exactly one place, so it cannot
	// quietly become the general default.
	Currency string `gorm:"size:3;index" json:"currency"`

	// ── Amounts ──
	// The *Kobo fields are the original NGN-only representation; the *Minor
	// fields are the same values in the intent currency's minor unit. Both are
	// written until the legacy columns are dropped.
	//
	// For NGN the two are identical — minor units ARE kobo — which is what lets
	// this migration be additive. Every currency in the registry today is
	// 2-decimal, so the arithmetic in it_automation.go stays correct for all of
	// them; the first 0- or 3-decimal currency is what would force it to become
	// exponent-aware.
	NgnKobo  string `gorm:"type:numeric(38,0);not null" json:"ngnKobo"`  // naira*100
	RateKobo string `gorm:"type:numeric(38,0);not null" json:"rateKobo"` // kobo per 1 MCGP, locked

	AmountMinor string `gorm:"type:numeric(38,0);default:0" json:"amountMinor,omitempty"`
	RateMinor   string `gorm:"type:numeric(38,0);default:0" json:"rateMinor,omitempty"`

	// ── Fees (both directions) ──
	// NgnKobo above stays the GROSS valuation of the MCGP leg — what the asset
	// was worth at the locked rate. These record what the fee engine took out
	// of it and what actually moves as a result:
	//
	//	sell:  payout disburses NetNgnKobo; FeeNgnKobo stays in the payout balance
	//	buy:   McgpAmount credited is already net; FeeNgnKobo is the naira value
	//	       withheld, whose MCGP equivalent is never minted to the user
	//
	// Empty on intents created before fee deduction shipped — readers must
	// treat "" as "no fee taken, net == gross" so in-flight intents settle at
	// the terms they were created under.
	FeeNgnKobo string `gorm:"type:numeric(38,0);default:0" json:"feeNgnKobo,omitempty"`
	NetNgnKobo string `gorm:"type:numeric(38,0)" json:"netNgnKobo,omitempty"`
	// PlatformKeptKobo is the slice of FeeNgnKobo the platform actually retains
	// (swap + platform fee) — the provider's pass-through and tax are excluded.
	// This is the base the cashback/TP carve is computed from, recorded so a
	// later rate change can't retro-explain an old payout.
	PlatformKeptKobo string `gorm:"type:numeric(38,0);default:0" json:"platformKeptKobo,omitempty"`

	// Minor-unit twins of the three fee columns above. Same dual-write rule:
	// written alongside the *Kobo fields, identical for NGN.
	FeeMinor          string `gorm:"type:numeric(38,0);default:0" json:"feeMinor,omitempty"`
	NetMinor          string `gorm:"type:numeric(38,0)" json:"netMinor,omitempty"`
	PlatformKeptMinor string `gorm:"type:numeric(38,0);default:0" json:"platformKeptMinor,omitempty"`

	// PaymentRef is the strong NGN attribution key: the minted "IT-…" reference
	// at creation, overwritten with the provider charge id once VA settlement
	// attributes a payment to this intent. Buy only. Uniqueness is enforced by
	// the partial index uq_it_intents_payment_ref (config/database.go) rather
	// than a struct tag, because sell intents leave it empty.
	PaymentRef string `gorm:"size:64;index" json:"paymentRef,omitempty"`
	// DebitRef is the on-chain bytes32 ref (hex) for the sell two-phase debit.
	DebitRef string `gorm:"size:66;index" json:"debitRef,omitempty"`

	TxHash        string `gorm:"size:66" json:"txHash,omitempty"`
	FailureReason string `gorm:"size:256" json:"failureReason,omitempty"`

	// ── Stablecoin ramp (brokered through Flutterwave; we custody nothing) ──
	// SourceChain is where the user's stablecoin starts; the funds are always
	// routed to Flutterwave's Polygon address, never to an address of ours.
	SourceChain string `gorm:"size:16" json:"sourceChain,omitempty"`
	// DestChain / DestAddress are the buy-side delivery target (Phase B).
	// DestAddress is 64 wide because Solana base58 runs to 44 characters —
	// WalletAddress is size:42 and cannot hold one.
	DestChain   string `gorm:"size:16" json:"destChain,omitempty"`
	DestAddress string `gorm:"size:64" json:"destAddress,omitempty"`

	// DepositAddress is FLUTTERWAVE'S deposit address the user was quoted at
	// intent creation (see CreateStableSellIntent). Persisted — not just
	// returned once — so a user who closes the app before sending funds can
	// re-open the intent (GET /it/intents/:id) and see where to send them
	// without minting a second intent for the same deposit.
	DepositAddress string `gorm:"size:64" json:"depositAddress,omitempty"`

	// DepositTxHash is the SOURCE-chain transaction the user broadcast, as
	// reported by the app. SettledTxHash is the Polygon transaction that
	// actually delivered to Flutterwave — resolved from LiFi and then verified
	// independently on-chain, which is what makes it authoritative. Both are
	// the on-chain fact that a transfer happened, NOT proof of who sent it
	// (Flutterwave exposes one shared merchant address), so each is further
	// constrained to fund at most one intent by the partial indexes
	// uq_it_intents_deposit_tx_hash / uq_it_intents_settled_tx_hash
	// (config/database.go) rather than a struct tag, because both columns are
	// empty for buy intents and for sells that have not reported yet.
	DepositTxHash string `gorm:"size:120;index" json:"depositTxHash,omitempty"`
	SettledTxHash string `gorm:"size:66;index" json:"settledTxHash,omitempty"`
	// ReceivedAmount is overloaded across the two legs — same column, two
	// different units:
	//
	//   sell: the asset base units that actually arrived at Flutterwave. The
	//         payout is computed from THIS, never from the quoted gross —
	//         quoting the payout would pay out more than was received
	//         whenever a bridge or provider fee came in above estimate.
	//   buy:  the FIAT MINOR UNITS the user actually paid, written at claim
	//         time (see settleStableBuyFromCharge in it_stable_buy_settle.go).
	//         Not asset base units on this leg.
	ReceivedAmount string `gorm:"type:numeric(78,0);default:0" json:"receivedAmount,omitempty"`

	// LockedRate is fiat minor units per one whole asset unit, locked at intent
	// creation. RateExpiresAt bounds the lock; Repriced records that the lock
	// lapsed and a live rate was used instead.
	LockedRate    string     `gorm:"type:numeric(38,0);default:0" json:"lockedRate,omitempty"`
	RateExpiresAt *time.Time `json:"rateExpiresAt,omitempty"`
	Repriced      bool       `gorm:"default:false" json:"repriced,omitempty"`

	// Sell payout destination (captured at sell creation for auto-payout; empty
	// on legacy manual-payout intents).
	BankCode      string `gorm:"size:16" json:"bankCode,omitempty"`
	AccountNumber string `gorm:"size:20" json:"accountNumber,omitempty"`
	AccountName   string `gorm:"size:128" json:"accountName,omitempty"`
	// Multi-currency / non-bank payout rail (optional; empty ⇒ legacy NGN bank
	// via BankCode/AccountNumber above). Mirrors services.PayoutRequest so the
	// sell payout path can pass these straight through to the gateway.
	DestinationCurrency string `gorm:"size:8" json:"destinationCurrency,omitempty"`
	RailType            string `gorm:"size:16" json:"railType,omitempty"`
	MobileNetwork       string `gorm:"size:32" json:"mobileNetwork,omitempty"`
	MobileCountry       string `gorm:"size:2" json:"mobileCountry,omitempty"`
	MobileMsisdn        string `gorm:"size:20" json:"mobileMsisdn,omitempty"`
	// PayoutID is the fiat provider's transfer id for the sell payout leg; the
	// transfer.disburse webhook closes the intent against it.
	PayoutID string `gorm:"size:64;index" json:"payoutId,omitempty"`
	// PayoutAttempts counts payout requests sent for this intent's (single)
	// payout reference. It is incremented BEFORE the request goes out, so a
	// crash mid-request still reads as "may have been sent" — 0 is the only
	// value that proves the reference was never used. MCGP sells only.
	PayoutAttempts int `gorm:"not null;default:0" json:"payoutAttempts,omitempty"`
	// PayoutRejected records positive evidence that no payout moved money: the
	// provider refused the first request (or replayed that refusal), or a
	// transfer webhook reported FAILED. An MCGP sell carrying it is reversed; a
	// stablecoin sell is parked for an operator.
	PayoutRejected bool `gorm:"not null;default:false" json:"payoutRejected,omitempty"`
	// PayoutRefusedOnRetry records that the provider refused a RETRY of the
	// payout reference. Unlike a first-attempt refusal this proves nothing — it
	// can mean the reference is taken because the first attempt went out — so
	// the sell is reversed only after the provider confirms no transfer exists.
	PayoutRefusedOnRetry bool `gorm:"not null;default:false" json:"payoutRefusedOnRetry,omitempty"`
	// LastPayoutAttemptAt is when the current reference was last sent. The
	// provider transfer search is centred on it, and a reverse is refused while
	// an attempt is recent enough to still be in flight. NULL on rows from
	// before the column existed.
	LastPayoutAttemptAt *time.Time `json:"lastPayoutAttemptAt,omitempty"`
	// PayoutRefGen is the payout reference generation. 0 is the original
	// reference; an operator repay of a provider-confirmed FAILED stablecoin
	// payout moves to a fresh one. MCGP sells never leave generation 0.
	PayoutRefGen int `gorm:"not null;default:0" json:"payoutRefGen,omitempty"`
	// ConversionID is the Flutterwave transfer id of the stablecoin→fiat
	// conversion. Conversion is asynchronous, so the intent waits in
	// `converting` on this id until the transfer settles.
	ConversionID string `gorm:"size:64;index" json:"conversionId,omitempty"`
	// ConversionAttempts counts how many times a conversion has been
	// initiated for this intent. Flutterwave's transfer reference doubles as
	// an idempotency key, so a retry after a FAILED conversion must use a
	// fresh reference or GetTransfer would keep returning the same stale
	// FAILED transfer forever — this is folded into that reference. 0 is the
	// correct value for every historical row (unlike Asset, this needs no
	// separate "treat empty as X" reader function).
	ConversionAttempts int `gorm:"default:0" json:"conversionAttempts,omitempty"`

	// DeliveryID is Flutterwave's transfer id for the OUTBOUND crypto payout
	// (type:"crypto"). Persisted for the same reason ConversionID is: the
	// transfer is asynchronous, so the poll that settles it needs the id, and
	// an operator recovering a stuck delivery needs it too.
	DeliveryID string `gorm:"size:64" json:"deliveryId,omitempty"`
	// DeliveryAttempts bounds retries the same way ConversionAttempts does.
	// A fresh attempt mints a fresh reference, so a retry cannot collide with
	// Flutterwave's idempotency on the previous failed transfer.
	DeliveryAttempts int `gorm:"default:0" json:"deliveryAttempts,omitempty"`

	// Dynamic virtual-account details issued for this buy intent (Flutterwave
	// collections); shown to the buyer as the pay-into account.
	VAAccountNumber string `gorm:"size:20" json:"vaAccountNumber,omitempty"`
	VABankName      string `gorm:"size:64" json:"vaBankName,omitempty"`

	// PMID is the company MCGP Payment Merchant this intent settles against.
	PMID *uuid.UUID `gorm:"type:uuid;index" json:"pmId,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (i *ITIntent) BeforeCreate(tx *gorm.DB) error {
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	return nil
}

// BeforeSave keeps the minor-unit columns in step with the kobo columns.
//
// Doing this in a hook rather than at each write site is deliberate: the two
// representations must never disagree, and there are enough places that build or
// update an ITIntent that relying on every one of them to remember would make
// divergence a matter of time. The hook makes the dual-write structural.
//
// The mirror is one-directional (kobo → minor) because kobo remains the
// authoritative representation until the legacy columns are dropped; at that
// point this hook and the *Kobo fields go together.
func (i *ITIntent) BeforeSave(tx *gorm.DB) error {
	i.AmountMinor = i.NgnKobo
	i.RateMinor = i.RateKobo
	i.FeeMinor = i.FeeNgnKobo
	i.NetMinor = i.NetNgnKobo
	i.PlatformKeptMinor = i.PlatformKeptKobo

	// MCGP intents keep McgpAmount authoritative and mirror it into the general
	// AssetAmount column. Stablecoin intents write AssetAmount directly and have
	// no McgpAmount — mirroring unconditionally would zero their amount.
	if ITIntentAsset(i) == ITAssetMCGP && i.McgpAmount != "" {
		i.AssetAmount = i.McgpAmount
	}
	return nil
}

// ITRate is the admin-set MCGP price for IT (singleton). MCGP is priced in USD;
// the Naira buy/sell rates are derived at request time from the live NGN/USD FX
// feed (same as other P2P assets). Only the active row is used.
//
//   - BuyPriceUsdMicros: USD per 1 MCGP in 6dp micro-dollars ($0.02 = 20000).
//   - SellDiscountBps:   discount applied to the sell leg vs buy; 5% = 500. The
//     company margin = the spread between buy and the discounted sell price.
type ITRate struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	BuyPriceUsdMicros int64      `gorm:"not null" json:"buyPriceUsdMicros"`
	SellDiscountBps   int        `gorm:"not null;default:0" json:"sellDiscountBps"`
	Active            bool       `gorm:"not null;default:true;index" json:"active"`
	UpdatedBy         *uuid.UUID `gorm:"type:uuid" json:"updatedBy,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

func (r *ITRate) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// ITIntentCurrency returns the intent's fiat currency, treating an empty value
// as NGN.
//
// This is the ONE place that fallback is allowed to live. The column has no
// database default precisely so a write path that forgets to set Currency
// fails loudly rather than producing a silently naira-denominated intent — but
// rows written before the column existed legitimately have no value, and they
// were all naira. Mirrors the existing "an empty fee column means no fee was
// taken" convention for pre-fee-engine intents: historical absence is read as
// the behaviour of the time, and never as a default for new writes.
//
// Do not inline this as `if cur == "" { cur = "NGN" }` at a call site. Spread
// across the codebase that pattern becomes indistinguishable from a real
// default, which is exactly what the missing column default is there to prevent.
func ITIntentCurrency(i *ITIntent) string {
	if i == nil || strings.TrimSpace(i.Currency) == "" {
		return "NGN"
	}
	return strings.ToUpper(strings.TrimSpace(i.Currency))
}
