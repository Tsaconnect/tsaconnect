package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// TSA Pay refunds (spec 2026-10-08 Part 3). TSA never holds TSA Pay money, so
// a merchant who refunds pays the customer back in USDT or USDC from the owner's
// own proven TSA wallet. The owner's app signs the transfer; the server checks it
// is exactly this refund, records its hash and only then broadcasts it; the
// confirmer settles the refund once the chain shows the transfer deep enough.
// The money unit is micro-USD (1 USDT = 1 USDC = 1,000,000).

// PayRefundNetwork is what refunds read of the chains — the live mainnet chains
// and their listed stablecoins, a token's decimals — and the two things they do
// there: broadcast a signed transfer, read a deep nonce.
// *BlockchainRefundNetwork is the real one.
type PayRefundNetwork interface {
	payChainRegistry
	// ChainName is the chain's display name ("BNB Smart Chain"), else its key.
	ChainName(chain string) string
	TokenDecimals(chain, tokenAddress string) (int, error)
	Broadcast(ctx context.Context, chain string, tx *types.Transaction) error
	// ConfirmedNonce is account's nonce as of the block `depth` deep: one past a
	// transaction's own nonce means some transaction with that nonce is mined at
	// least that deep.
	ConfirmedNonce(ctx context.Context, chain string, account common.Address, depth uint64) (uint64, error)
}

// PayRefundVerifier proves a refund transfer. *StableTransferVerifier is the
// real one — a second instance over every mainnet chain, never the treasury's.
type PayRefundVerifier interface {
	Verify(ctx context.Context, chain, txHash, tokenAddress, from, to string) (*StablePayment, error)
	TransactionKnown(ctx context.Context, chain, txHash string) (bool, string, error)
}

var (
	ErrPayRefundNotFound      = errors.New("pay refund: not found")
	ErrPayRefundInvalid       = errors.New("pay refund: amount must be positive USD with at most 6 decimal places")
	ErrPayRefundNotSucceeded  = errors.New("pay refund: only a succeeded payment can be refunded")
	ErrPayRefundOpen          = errors.New("pay refund: a refund is already open on this payment")
	ErrPayRefundNotRefundable = errors.New("pay refund: refunds aren't available for this payment")
	ErrPayRefundPayerDeleted  = errors.New("pay refund: the payer's account is deleted")
	ErrPayRefundState         = errors.New("pay refund: the refund is not in a state that allows this")
	ErrPayRefundTxUsed        = errors.New("pay refund: that transaction is already attached to another refund")
	ErrPayRefundUnavailable   = errors.New("pay refund: refunds are unavailable right now")
	// ErrPayRefundChainUnreadable wraps every failure to read a refund's chain
	// (an RPC error, a chain with no client, the endpoint moving mid-check) —
	// never a database error. Nothing about the refund changed.
	ErrPayRefundChainUnreadable = errors.New("pay refund: couldn't read the chain")
	// ErrPayRefundNonceUsed: the signed transfer's nonce is already used on
	// its chain, so it can never be mined — the owner signs again (409).
	ErrPayRefundNonceUsed = errors.New("pay refund: the transaction's nonce is already used")
	// ErrPayRefundNonceFar: the signed transfer's nonce is so far past the
	// wallet's next one that it would wait unmined behind transactions that
	// don't exist — the owner signs again (409).
	ErrPayRefundNonceFar = errors.New("pay refund: the transaction's nonce is too far ahead of the wallet's")
)

// PayRefundOverCapError: the amount asked for is more than is left to refund.
type PayRefundOverCapError struct{ RemainingMicroUSD int64 }

func (e *PayRefundOverCapError) Error() string {
	return fmt.Sprintf("pay refund: over the refundable cap (%s left)", FormatMicroUSD(e.RemainingMicroUSD))
}

// PayRefundTxError: the signed transaction is not exactly this refund. Why is
// said to the owner as-is.
type PayRefundTxError struct{ Why string }

func (e *PayRefundTxError) Error() string { return "pay refund: transaction refused: " + e.Why }

// The refund state of a payment as a whole: none refunded, part, or all of
// what can be.
const (
	PayRefundStatusNone    = "none"
	PayRefundStatusPartial = "partial"
	PayRefundStatusFull    = "full"
)

// Open refunds may still move money; with confirmed ones they count against
// the cap. Failed and cancelled ones are proven to have moved none.
var (
	payRefundOpenStatuses    = []string{models.PayRefundRequested, models.PayRefundSubmitted, models.PayRefundNeedsReview}
	payRefundCountedStatuses = []string{models.PayRefundConfirmed, models.PayRefundRequested, models.PayRefundSubmitted, models.PayRefundNeedsReview}
)

// PayRefundService owns pay_refunds. network and verifier nil leave live
// refunds unavailable (test-mode ones need neither).
type PayRefundService struct {
	db        *gorm.DB
	payments  *PayPaymentService
	merchants *PayMerchantService
	network   PayRefundNetwork
	verifier  PayRefundVerifier
	minConf   uint64
	now       func() time.Time
	events    *events.Bus
	// alerted: the payRefundAlertKeys this process has already ALERTed, with when.
	alerted sync.Map
}

// NewPayRefundService wires refunds. minConf is how deep a refund transfer
// must be to count (RAMP_TREASURY_CONFIRMATIONS); below 1 reads as 1.
func NewPayRefundService(db *gorm.DB, payments *PayPaymentService, merchants *PayMerchantService,
	network PayRefundNetwork, verifier PayRefundVerifier, minConf int, now func() time.Time) *PayRefundService {
	if minConf < 1 {
		minConf = 1
	}
	return &PayRefundService{db: db, payments: payments, merchants: merchants, network: network, verifier: verifier,
		minConf: uint64(minConf), now: now}
}

// SetEvents wires the bus refund notices go out on. Unset, refunds are silent.
func (s *PayRefundService) SetEvents(bus *events.Bus) { s.events = bus }

var _ PayRefundAggregator = (*PayRefundService)(nil)

// PayRefundView is a refund as the owner, the web and the admin console read it.
type PayRefundView struct {
	ID            uuid.UUID  `json:"id"`
	PaymentID     uuid.UUID  `json:"paymentId"`
	Mode          string     `json:"mode"`
	Status        string     `json:"status"`
	Amount        string     `json:"amount"`
	Cap           string     `json:"cap"`
	Credited      string     `json:"credited"`
	Reason        string     `json:"reason"`
	ToAddress     string     `json:"toAddress"`
	FromAddress   string     `json:"fromAddress"`
	ChainID       int64      `json:"chainId"`
	TokenSymbol   string     `json:"tokenSymbol"`
	TokenAddress  string     `json:"tokenAddress"`
	TxHash        string     `json:"txHash"`
	FailureReason string     `json:"failureReason"`
	RequestedVia  string     `json:"requestedVia"`
	CreatedAt     time.Time  `json:"createdAt"`
	SubmittedAt   *time.Time `json:"submittedAt"`
	ConfirmedAt   *time.Time `json:"confirmedAt"`
	// LastBroadcastError: the network refused the signed transfer the last time
	// it was sent (owner-safe words; "" when the last send went through). The
	// refund is still submitted and is sent again automatically.
	LastBroadcastError string     `json:"lastBroadcastError"`
	LastBroadcastAt    *time.Time `json:"lastBroadcastAt"`
}

// View is r's read shape.
func (s *PayRefundService) View(r *models.PayRefund) PayRefundView {
	v := PayRefundView{ID: r.ID, PaymentID: r.PaymentID, Mode: r.Mode, Status: r.Status,
		Amount: FormatMicroUSD(r.AmountMicroUSD), Cap: FormatMicroUSD(r.CapMicroUSD), Credited: FormatMicroUSD(r.CreditedMicroUSD),
		Reason: r.Reason, ToAddress: r.ToAddress, FromAddress: r.FromAddress, ChainID: r.ChainID, TokenSymbol: r.TokenSymbol,
		TokenAddress: r.TokenAddress, FailureReason: r.FailureReason, RequestedVia: r.RequestedVia,
		CreatedAt: r.CreatedAt, SubmittedAt: r.SubmittedAt, ConfirmedAt: r.ConfirmedAt,
		LastBroadcastError: r.LastBroadcastError, LastBroadcastAt: r.LastBroadcastAt}
	if r.TxHash != nil {
		v.TxHash = *r.TxHash
	}
	return v
}

// payRefundStatus is a payment's refund state from its confirmed total and cap.
func payRefundStatus(refunded, capMicroUSD int64) string {
	switch {
	case refunded <= 0:
		return PayRefundStatusNone
	case capMicroUSD > 0 && refunded >= capMicroUSD:
		return PayRefundStatusFull
	}
	return PayRefundStatusPartial
}

// payRefundAggregates is each of ids' confirmed refund total and cap, in one
// grouped query on db — which may be a transaction, so a refund confirmed in
// it is already counted. A payment with no refund rows is absent.
func payRefundAggregates(db *gorm.DB, ids []uuid.UUID) (map[uuid.UUID]PayRefundAgg, error) {
	out := make(map[uuid.UUID]PayRefundAgg, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		PaymentID   uuid.UUID
		Refunded    int64
		CapMicroUSD int64
	}
	err := db.Model(&models.PayRefund{}).
		// SUM of a bigint is numeric on Postgres: cast back to the int64 it scans into.
		Select("payment_id, CAST(COALESCE(SUM(CASE WHEN status = ? THEN amount_micro_usd ELSE 0 END), 0) AS BIGINT) AS refunded, MAX(cap_micro_usd) AS cap_micro_usd",
			models.PayRefundConfirmed).
		Where("payment_id IN ?", ids).Group("payment_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.PaymentID] = PayRefundAgg{RefundedMicroUSD: r.Refunded, CapMicroUSD: r.CapMicroUSD, Status: payRefundStatus(r.Refunded, r.CapMicroUSD)}
	}
	return out, nil
}

// AggregateByPayment implements PayRefundAggregator.
func (s *PayRefundService) AggregateByPayment(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]PayRefundAgg, error) {
	return payRefundAggregates(s.db.WithContext(ctx), ids)
}

// TotalsConfirmed implements PayRefundAggregator: the merchant's confirmed
// refunds in mode, by the day they were confirmed ([start, end); zero is open).
func (s *PayRefundService) TotalsConfirmed(ctx context.Context, merchantID uuid.UUID, mode string, start, end time.Time) (PaySummaryRefunds, error) {
	q := s.db.WithContext(ctx).Model(&models.PayRefund{}).
		Where("merchant_id = ? AND mode = ? AND status = ?", merchantID, mode, models.PayRefundConfirmed)
	var row struct {
		Count  int64
		Amount int64
	}
	err := payBetween(q, "confirmed_at", start, end).
		Select("COUNT(*) AS count, CAST(COALESCE(SUM(amount_micro_usd), 0) AS BIGINT) AS amount").Scan(&row).Error
	return PaySummaryRefunds{Count: row.Count, AmountMicroUSD: row.Amount}, err
}
