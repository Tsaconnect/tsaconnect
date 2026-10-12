package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PaycrestOrderRecord is our side of a Paycrest ramp order.
//
// Paycrest is non-custodial on BOTH legs: the user pays the provider's bank
// account directly on a buy, and sends from their own connected wallet on a
// sell. So this row is not an accounting record of money we hold — it is the
// audit trail of an order we brokered, and the thing an operator reads when a
// user says "I paid and got nothing".
//
// That last case is real and has no API remedy (see PaycrestStatusUnmatched):
// a deposit can reach the provider's bank without ever registering against the
// order. Every field an operator would need to chase that manually is stored
// here at creation time, because the order itself expires and Paycrest's copy
// tells you nothing about who the user was.
const (
	PaycrestDirectionBuy  = "buy"  // onramp:  NGN in  -> stablecoin to the user's wallet
	PaycrestDirectionSell = "sell" // offramp: stablecoin from the user's wallet -> NGN out

	// Lifecycle. The first three mirror Paycrest's own vocabulary; the last two
	// are ours, for states their API has no word for.
	PaycrestStatusAwaitingPayment = "awaiting_payment" // buy: user has not transferred yet
	PaycrestStatusAwaitingDeposit = "awaiting_deposit" // sell: user has not sent crypto yet
	PaycrestStatusPending         = "pending"          // provider assigned, funds seen
	PaycrestStatusSettling        = "settling"         // provider is delivering
	PaycrestStatusSettled         = "settled"          // complete
	PaycrestStatusRefunded        = "refunded"         // returned to the refund destination
	PaycrestStatusExpired         = "expired"          // window passed, nothing paid
	// PaycrestStatusUnmatched is OURS, not Paycrest's. It marks the failure we
	// hit live on 2026-09-03: the user paid in full and inside the window, the
	// bank confirmed receipt, and the order still expired with amountPaid=0.
	// There is no refund endpoint and no documented recovery — this state
	// exists so those orders surface to an operator instead of looking like an
	// ordinary expiry the user simply ignored.
	PaycrestStatusUnmatched = "unmatched"
	PaycrestStatusFailed    = "failed"
	// PaycrestStatusCancelled is OURS too, and means the USER abandoned the
	// order before sending anything. Paycrest has no cancel endpoint, so their
	// copy stays live until it expires on its own — this status only says we
	// have stopped treating it as the user's open order. Distinct from
	// PaycrestStatusFailed, which is where Paycrest's own "cancelled" lands.
	PaycrestStatusCancelled = "cancelled"

	// Converted-order states. converting: Paycrest delivered to the treasury
	// and the conversion has not finished. refunding: a refund leg is in
	// flight. recovery_pending: S6, funds stranded at Paycrest, worked through
	// a dispute. None of them are terminal.
	PaycrestStatusConverting      = "converting"
	PaycrestStatusRefunding       = "refunding"
	PaycrestStatusRecoveryPending = "recovery_pending"

	RouteKindDirect    = "direct"
	RouteKindConverted = "converted"

	HolderUser     = "user"
	HolderTreasury = "treasury"
	HolderPaycrest = "paycrest"
	// HolderCompanyWallet / HolderFlutterwave: an IT-settled converted buy
	// (spec §15) whose settlement leg the company wallet or Flutterwave has not
	// yet proven into the conversion treasury. Like HolderPaycrest, the
	// orchestrator's custody step still has to prove it.
	HolderCompanyWallet = "company_wallet"
	HolderFlutterwave   = "flutterwave"
	HolderPool          = "tsa_pool" // a converted pool order's leg (spec S22): custody is summed from its slices' releases

	FaultPlatform = "platform"
	FaultUser     = "user"
)

// paycrestTerminalStatuses is the single list of states nothing can move on
// from. Both readers below derive from it: they used to keep separate copies,
// and a status added to one but not the other lets the poller resurrect a row.
var paycrestTerminalStatuses = []string{
	PaycrestStatusSettled, PaycrestStatusRefunded, PaycrestStatusExpired,
	PaycrestStatusUnmatched, PaycrestStatusFailed, PaycrestStatusCancelled,
}

// PaycrestTerminalStatuses returns the terminal set for query use (SQL NOT IN).
func PaycrestTerminalStatuses() []string {
	out := make([]string, len(paycrestTerminalStatuses))
	copy(out, paycrestTerminalStatuses)
	return out
}

// PaycrestResolvedStatuses are the terminal statuses nobody will chase
// again: every terminal one except unmatched, which is terminal only for the
// automated path — an operator is still working it by hand, from the payout
// details on the row. Account erasure scrubs the user's payout destination
// only from these.
func PaycrestResolvedStatuses() []string {
	out := make([]string, 0, len(paycrestTerminalStatuses))
	for _, st := range paycrestTerminalStatuses {
		if st != PaycrestStatusUnmatched {
			out = append(out, st)
		}
	}
	return out
}

// WithPayoutDetailsCleared adds, to a column update, the blanking of the
// user's own payout destination on a sell. Only the payout_* columns: the
// pay_* columns on a buy are the PROVIDER's collection account, not her data,
// and are what an operator needs to chase a deposit that never matched.
// Blank, not NULL, matching WithBankDetailsCleared.
func WithPayoutDetailsCleared(updates map[string]any) map[string]any {
	updates["payout_bank_code"] = ""
	updates["payout_institution"] = ""
	updates["payout_account"] = ""
	updates["payout_account_name"] = ""
	return updates
}

// PaycrestOrderRecord is one brokered ramp order.
type PaycrestOrderRecord struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`

	Direction string `gorm:"size:8;not null;index" json:"direction"`
	Status    string `gorm:"size:24;not null;index" json:"status"`

	// Reference is OUR key, sent to Paycrest and echoed back on the order and
	// every webhook. Unique so a retried create can never open two orders.
	Reference string `gorm:"size:128;not null;uniqueIndex" json:"reference"`
	// PaycrestID is their order id. Empty only in the window between our row
	// being written and their API answering.
	PaycrestID string `gorm:"size:64;index" json:"paycrestId,omitempty"`

	FiatCode   string `gorm:"size:8;not null" json:"currency"`
	FiatAmount string `gorm:"size:32;not null" json:"amount"`
	Token      string `gorm:"size:16;not null" json:"token"`
	// ChainKey is OUR chain vocabulary ("bsc"); Network is Paycrest's
	// ("bnb-smart-chain"). Both are stored because a support ticket needs
	// theirs and blockchainService needs ours.
	ChainKey string `gorm:"size:24;not null" json:"chain"`
	Network  string `gorm:"size:32;not null" json:"-"`

	// Rate is what we quoted: the provider's rate with our sender fee priced
	// in (orders before 2026-09-14 hold the raw provider rate). TokenAmount is
	// what the user receives (buy) or sends (sell).
	//
	// TokenAmount is a PLAIN HUMAN-READABLE DECIMAL STRING ("100", "36.4218")
	// — Paycrest's own order.Amount on a buy, exactly as CreateBuy writes it
	// verbatim, NEVER base units. Every other converted-buy site that reads
	// this field (authorization_relayer.go, gas_topup.go, ramp_sponsorship.go,
	// ConversionOrchestrator) already converts it with DecimalToBaseUnits and
	// the SOURCE token's own decimals before treating it as an integer — do
	// not add a new site that parses it with big.Int.SetString directly, that
	// silently misreads it as base units (fixed after a fork rehearsal found
	// the short-delivery check and every automatic bridge/refund tick doing
	// exactly that). PromisedAmount below, and every RampLeg.Amount /
	// AmountReceived, ARE base units — this field alone is not.
	Rate        string `gorm:"size:32" json:"rate"`
	TokenAmount string `gorm:"size:40" json:"tokenAmount"`
	// RawRate is Paycrest's own rate on the order, as it answered it: unrounded,
	// before our sender fee — the rate it actually pays at. Empty on rows opened
	// before it was recorded.
	RawRate string `gorm:"size:32" json:"-"`
	// ProviderID is which liquidity provider took it. Stored because provider
	// quality varies sharply — one stranded our funds and left the book the
	// next day — and per-provider failure rates are only visible if recorded.
	ProviderID string `gorm:"size:32;index" json:"-"`

	// SenderFee is our margin as reported by Paycrest, settled on-chain.
	SenderFee        string `gorm:"size:32" json:"senderFee,omitempty"`
	SenderFeePercent string `gorm:"size:16" json:"senderFeePercent,omitempty"`

	// ── Conversion (Phase 2). RouteKind is direct for every pre-Phase-2 row,
	// which is what the zero value must mean, so reads treat "" as direct.
	RouteKind      string `gorm:"size:12;index" json:"routeKind,omitempty"`
	RequestedToken string `gorm:"size:16" json:"requestedToken,omitempty"`
	RequestedChain string `gorm:"size:24" json:"requestedChain,omitempty"`
	// LegDecimals is an IT converted record's leg-token decimals ("ITC-"
	// records only; spec 2026-10-08 S23). Nil reads as 6 — every record
	// written before the pool. The handoff restates owed units in them.
	LegDecimals *int `gorm:"default:null" json:"-"`
	// RequestedChainID/RequestedTokenAddress/RequestedTokenDecimals identify
	// the destination asset exactly, which the symbol pair above cannot: two
	// chains can serve a token of the same name, a symbol is not unique on one
	// chain, and a token the config map has never heard of has no symbol here
	// at all. Every one of them is DERIVED server-side (chain key from the
	// chain id, decimals and symbol from LiFi or our own config) — the client
	// sends only the chain id and the address.
	RequestedChainID       int64  `gorm:"default:0" json:"requestedChainId,omitempty"`
	RequestedTokenAddress  string `gorm:"size:128" json:"requestedTokenAddress,omitempty"`
	RequestedTokenDecimals int    `json:"requestedTokenDecimals,omitempty"`
	// DestinationAddress is where the converted asset must land. Today it is
	// always the user's own wallet (the same value as WalletAddress), stored
	// separately because the delivery target and the account's wallet are not
	// the same question — a non-EVM destination will not be an EVM address.
	DestinationAddress string `gorm:"size:128" json:"destinationAddress,omitempty"`
	MarkupBps          int    `gorm:"default:0" json:"markupBps,omitempty"`
	// QuotedAmount is T: what she is owed, in the SOURCE (settlement leg's)
	// base units. Computed from Paycrest's own delivered amount less
	// MarkupBps (the route's cost B′), never from the venue's quote — see
	// services.ConversionOwed.
	QuotedAmount string `gorm:"size:80" json:"quotedAmount,omitempty"`
	// EstimatedAmount is what the route was expected to deliver, in the
	// DESTINATION token's base units. An estimate shown to the user, never a
	// promise: only PromisedAmount (stablecoin destinations) binds.
	EstimatedAmount string `gorm:"size:80" json:"estimatedAmount,omitempty"`
	// PromisedAmount is what we guaranteed the user receives: token base units
	// on a buy. The conversion must land at least this much.
	PromisedAmount string `gorm:"size:64" json:"promisedAmount,omitempty"`
	// size 24: "company_wallet" (14) does not fit the original 12.
	Holder string `gorm:"size:24" json:"holder,omitempty"`
	// OwedToUser is computed from the legs, never entered by hand. It has three
	// states and they are not interchangeable: a number is what we still owe,
	// "0" means nothing is owed, and "" means UNDETERMINED — a transfer is in
	// flight, so the user may already hold the money and NOBODY MAY PAY against
	// this field until it resolves. It carries no omitempty for exactly that
	// reason: dropping "" would make the one value that forbids a payout look
	// identical to a direct order that never had the field.
	OwedToUser string `gorm:"size:64" json:"owedToUser"`
	Fault      string `gorm:"size:12" json:"fault,omitempty"`

	// WalletAddress is the user's own connected wallet: the delivery target on
	// a buy, the funding source on a sell. We never custody the asset.
	WalletAddress string `gorm:"size:128;not null" json:"walletAddress"`

	// ── Buy leg: where the user must send naira ──
	PayInstitution   string     `gorm:"size:64" json:"payInstitution,omitempty"`
	PayAccountNumber string     `gorm:"size:32" json:"payAccountNumber,omitempty"`
	PayAccountName   string     `gorm:"size:128" json:"payAccountName,omitempty"`
	PayAmount        string     `gorm:"size:32" json:"payAmount,omitempty"`
	ValidUntil       *time.Time `json:"validUntil,omitempty"`

	// ── Sell leg: where the user must send crypto ──
	DepositAddress string `gorm:"size:128" json:"depositAddress,omitempty"`
	// PayoutBankCode is stored in NIBSS form (what the app speaks);
	// PayoutInstitution is Paycrest's code, as sent on the order.
	PayoutBankCode    string `gorm:"size:16" json:"payoutBankCode,omitempty"`
	PayoutInstitution string `gorm:"size:32" json:"-"`
	PayoutAccount     string `gorm:"size:32" json:"payoutAccount,omitempty"`
	PayoutAccountName string `gorm:"size:128" json:"payoutAccountName,omitempty"`

	// AmountPaid mirrors Paycrest's field. Zero on an expired order is the
	// unmatched-deposit signature.
	AmountPaid     string `gorm:"size:32" json:"amountPaid,omitempty"`
	AmountReturned string `gorm:"size:32" json:"amountReturned,omitempty"`
	// TxHash is the on-chain delivery (buy) or the user's deposit (sell).
	TxHash string `gorm:"size:128" json:"txHash,omitempty"`

	// PaymentClaimedAt is set when the user taps "I've sent the money" (buy)
	// or "I've sent it" (sell) — the app's only signal that they believe they
	// completed their side. It is NOT proof of payment (we never see the bank
	// or the chain confirm this ourselves) and never moves Status on its own.
	// Its one job is distinguishing, on an order that later expires with
	// amountPaid=0, a genuine unmatched deposit (paid, bank confirmed, protocol
	// never saw it — see PaycrestStatusUnmatched) from an order the user simply
	// abandoned before paying, which looks identical to Paycrest. See
	// mapPaycrestStatus in paycrest_order_service.go for where this is read.
	PaymentClaimedAt *time.Time `json:"paymentClaimedAt,omitempty"`

	// PayPaymentID marks an order that is a TSA Pay payment's attempt: its payout
	// bank is the MERCHANT's, so the customer's routes mask it. Set at creation
	// and never cleared — the payment's own link to the order is dropped when it
	// is released, which would leave a refunded attempt unrecognisable. Not part
	// of the JSON, so no route changes shape.
	PayPaymentID *uuid.UUID `gorm:"type:uuid;index" json:"-"`

	// FailureReason is operator-facing text for anything the user cannot fix.
	FailureReason string `gorm:"size:512" json:"failureReason,omitempty"`
	// LastCheckedAt drives the poller's ordering. Paycrest webhooks require a
	// public endpoint; until one exists this is how orders progress at all.
	LastCheckedAt *time.Time `json:"-"`
	// RampCheckedAt is when the conversion orchestrator last LOOKED at this
	// order, which is a different question from when the order last moved.
	// It exists so a tick that makes no progress can rotate the row to the back
	// of its queue without touching UpdatedAt. Staleness itself is read from the
	// LEG LEDGER, never from UpdatedAt: the Paycrest poller bumps UpdatedAt on
	// every cycle, so it says nothing about whether a conversion is stuck.
	RampCheckedAt *time.Time `json:"-"`
	SettledAt     *time.Time `json:"settledAt,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (o *PaycrestOrderRecord) BeforeCreate(tx *gorm.DB) error {
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	return nil
}

// PaycrestOrderTerminal reports whether a status can still change. Unmatched
// is terminal for the automated path even though the money is unresolved —
// only an operator can move it.
func PaycrestOrderTerminal(status string) bool {
	for _, s := range paycrestTerminalStatuses {
		if s == status {
			return true
		}
	}
	return false
}

// NeedsOperator reports whether a row is stuck in a way no retry can fix, so
// the admin queue and alerting can select on one predicate.
// A converted buy in recovery_pending is the same shape of problem as an
// unmatched deposit: the money's position is known but only a human can settle
// what is still owed, so both must surface through one predicate.
func (o *PaycrestOrderRecord) NeedsOperator() bool {
	return o != nil && (o.Status == PaycrestStatusUnmatched || o.Status == PaycrestStatusRecoveryPending)
}

// Converted reports whether this order routes through the treasury. Rows
// written before Phase 2 have an empty RouteKind and are direct.
func (o *PaycrestOrderRecord) Converted() bool { return o.RouteKind == RouteKindConverted }
