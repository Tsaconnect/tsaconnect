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

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// deliveryReceiptGrace is how long, once a delivery's swap first reads
// COMPLETED, the destination receipt may show no credit before that is taken as
// the answer: a node lags its chain, and "no matching Transfer" a few seconds
// after LiFi says done is usually just that. Past it, the merchant was not paid.
const deliveryReceiptGrace = 10 * time.Minute

// deliveryValueBand is how far our price for the merchant's token may sit from
// the route's own value for the amount it delivers (5%, either way) before the
// sale is refused: a bogus price would size an amount that under-pays the
// merchant, and nothing after it would notice.
const deliveryValueBand = 0.05

// payValueAgrees reports whether our USD value for an amount and the route's
// agree to within deliveryValueBand. A route that names no value cannot be
// cross-checked, which is a disagreement.
func payValueAgrees(expectedUSD, routeUSD float64) bool {
	if expectedUSD <= 0 || routeUSD <= 0 {
		return false
	}
	return math.Abs(routeUSD-expectedUSD)/routeUSD <= deliveryValueBand
}

// checkDelivery is a delivery sale's own precondition: everything
// ConfirmDelivery will read must be there before a route is quoted, or the
// sale could be paid for and never proven.
func (in CreateSellIntentInput) checkDelivery() error {
	owed, ok := new(big.Int).SetString(strings.TrimSpace(in.DestAmount), 10)
	switch {
	case in.DestChainID <= 0 || strings.TrimSpace(in.DestTokenAddress) == "":
		return fmt.Errorf("%w: a delivery names its chain and token", ErrSellIntentState)
	case strings.EqualFold(strings.TrimSpace(in.DestTokenAddress), evmNativeAddress) ||
		strings.EqualFold(strings.TrimSpace(in.DestTokenAddress), evmNativeAliasAddress):
		// The proof reads ERC-20 Transfer logs; a native coin leaves none.
		return fmt.Errorf("%w: a delivery is an ERC-20 token, not a native coin", ErrSellIntentState)
	case !ok || owed.Sign() <= 0:
		return fmt.Errorf("%w: %q is not an amount to deliver", ErrSellIntentState, in.DestAmount)
	case strings.TrimSpace(in.ToAmount) == "":
		return fmt.Errorf("%w: a delivery is an exact-output swap", ErrSellIntentState)
	}
	return nil
}

// checkDeliveryRoute is what a delivery needs of its route beyond an ordinary
// exact-output one, all refused before she signs — a delivery has no naira
// backstop, so a route that falls short ends with her already charged:
//
//   - its floor (toAmountMin) covers what the merchant is owed;
//   - it carries the transactionId that binds the swap she signs to THIS quote
//     (checkSwapBinding tolerates an empty one; for a delivery that clause is
//     the only thing tying the swap to us);
//   - our price for the token agrees with the route's own value for it.
func checkDeliveryRoute(in CreateSellIntentInput, route *LiFiRoute) error {
	owed, _ := new(big.Int).SetString(strings.TrimSpace(in.DestAmount), 10)
	switch {
	case route.ToAmountMin.Cmp(owed) < 0:
		return fmt.Errorf("%w: the route's floor is under what the merchant is owed", ErrSellUnavailable)
	case strings.TrimSpace(route.TransactionID) == "":
		return fmt.Errorf("%w: the route carries no transaction id to bind the swap to", ErrSellUnavailable)
	case in.ExpectedToUSD > 0 && !payValueAgrees(in.ExpectedToUSD, route.ToAmountUSD):
		return fmt.Errorf("%w: the token's price and the route's value disagree", ErrSellUnavailable)
	}
	return nil
}

// ConfirmDelivery is OpenOrder's twin for a delivery sale (DestAddress set): it
// answers "did the merchant's wallet get what it is owed?" and, only when it did,
// moves the sale to delivered and books our fee. It returns the destination
// transaction that credited the merchant.
//
// This decides whether a merchant was paid, so nothing here takes LiFi's word:
// LiFi's status finds the swap and binds it to the sale (checkSwapBinding, the
// receiving side being the merchant's own token, chain and wallet), and the
// destination chain's own receipt says how much that wallet was credited. Less
// than DestAmount — by a single base unit — is ErrSwapMismatch: the money may
// be on its way, elsewhere, or short, and a person has to look.
//
// Idempotent: a sale already delivered is re-proven and answers with the same
// transaction, booking nothing more. The fee is booked before the guarded
// update so a crash between the two is healed by the next call (the engine's
// guard makes a repeat a no-op), never lost.
func (s *SellIntentService) ConfirmDelivery(ctx context.Context, id uuid.UUID) (string, error) {
	if !s.ready() {
		return "", ErrSellUnavailable
	}
	var in models.RampSellIntent
	if err := s.db.WithContext(ctx).First(&in, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrSellIntentNotFound
		}
		return "", err
	}
	if !in.IsDelivery() || in.SwapTxHash == nil ||
		(in.Status != models.SellIntentSwapped && in.Status != models.SellIntentDelivered) {
		return "", ErrSellIntentState
	}
	owed, ok := new(big.Int).SetString(strings.TrimSpace(in.DestAmount), 10)
	if !ok || owed.Sign() <= 0 {
		log.Printf("[ramp-sell] ALERT ramp_sell_delivery_unreadable_total=1 intent=%s dest_amount=%q", in.ID, in.DestAmount)
		return "", ErrSellIntentState
	}

	st, err := s.status.Status(ctx, *in.SwapTxHash)
	if err != nil {
		return "", err
	}
	if st == nil || st.Status != LiFiStatusDone || st.Substatus != LiFiSubstatusCompleted {
		return "", &SwapNotLandedError{Status: st}
	}
	if err := checkSwapBinding(st, &in, rampSettlementChainID, s.usdc); err != nil {
		log.Printf("[ramp-sell] ALERT ramp_sell_swap_mismatch_total=1 intent=%s tx=%s status=%s/%s: %v",
			in.ID, *in.SwapTxHash, st.Status, st.Substatus, err)
		return "", err
	}

	verifier, ok := s.destVerifier(in.DestChainID)
	if !ok {
		log.Printf("[ramp-sell] ALERT ramp_sell_delivery_unverifiable_total=1 intent=%s chain=%d", in.ID, in.DestChainID)
		return "", fmt.Errorf("%w: no way to prove a delivery on chain %d", ErrSellUnavailable, in.DestChainID)
	}
	// The first time the swap reads COMPLETED starts the wait for its receipt to
	// be readable; it is written once and survives a restart.
	seen, err := s.deliverySeen(ctx, &in)
	if err != nil {
		return "", err
	}
	got, err := verifier.VerifyTransfer(ctx, st.DestTxHash, in.DestTokenAddress, in.DestAddress)
	switch {
	case errors.Is(err, ErrTransferReverted):
		// The chain says the destination transaction did nothing.
		log.Printf("[ramp-sell] ALERT ramp_sell_delivery_reverted_total=1 intent=%s tx=%s: %v", in.ID, st.DestTxHash, err)
		return "", fmt.Errorf("%w: %v", ErrSwapMismatch, err)
	case errors.Is(err, ErrTransferNotFound):
		// The real verifier says "no Transfer log credits the merchant" this way,
		// never as a zero. Right after LiFi says done it may be a lagging node: a
		// wait, carrying the status read so the caller need not read it again.
		if s.now().Sub(seen) < deliveryReceiptGrace {
			return "", &SwapNotLandedError{Status: st}
		}
		log.Printf("[ramp-sell] ALERT ramp_sell_delivery_no_credit_total=1 intent=%s tx=%s seen_for=%s: %v",
			in.ID, st.DestTxHash, s.now().Sub(seen).Round(time.Second), err)
		return "", fmt.Errorf("%w: %v", ErrSwapMismatch, err)
	case err != nil:
		return "", err
	}
	if got == nil || got.Cmp(owed) < 0 {
		log.Printf("[ramp-sell] ALERT ramp_sell_delivery_short_total=1 intent=%s tx=%s credited=%v owed=%s",
			in.ID, st.DestTxHash, got, owed)
		return "", fmt.Errorf("%w: %s credited less than the %s owed", ErrSwapMismatch, st.DestTxHash, owed)
	}

	if in.Status == models.SellIntentDelivered {
		return st.DestTxHash, nil
	}
	destTx := strings.ToLower(st.DestTxHash)
	// One destination transaction proves one sale (see RampSellIntent.DestTxHash).
	// Checked before the fee is booked; the unique index below is the backstop.
	var reused int64
	if err := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("dest_tx_hash = ? AND id <> ?", destTx, in.ID).Count(&reused).Error; err != nil {
		return "", err
	}
	if reused > 0 {
		return "", s.deliveryTxReused(&in, st.DestTxHash)
	}
	s.bookFee(&in, in.UserID, tsaPayFeeSourceType)
	res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ? AND status = ?", in.ID, models.SellIntentSwapped).
		Updates(models.WithBankDetailsCleared(map[string]any{"status": models.SellIntentDelivered, "dest_tx_hash": destTx, "updated_at": s.now()}))
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return "", s.deliveryTxReused(&in, st.DestTxHash)
		}
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		// Another caller got there first. Fine if it delivered; anything else
		// (a reconciler failed the sale in between) is not a delivery we made.
		var now models.RampSellIntent
		if err := s.db.WithContext(ctx).First(&now, "id = ?", in.ID).Error; err != nil {
			return "", err
		}
		if now.Status != models.SellIntentDelivered {
			return "", ErrSellIntentState
		}
	}
	return st.DestTxHash, nil
}

// deliverySeen is when in's swap was first seen COMPLETED, writing now if it is
// the first time. Guarded, so of two concurrent first sights one time wins.
func (s *SellIntentService) deliverySeen(ctx context.Context, in *models.RampSellIntent) (time.Time, error) {
	if in.DeliverySeenAt != nil {
		return *in.DeliverySeenAt, nil
	}
	now := s.now()
	res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ? AND delivery_seen_at IS NULL", in.ID).
		Update("delivery_seen_at", now)
	if res.Error != nil {
		return time.Time{}, res.Error
	}
	if res.RowsAffected == 0 {
		var cur models.RampSellIntent
		if err := s.db.WithContext(ctx).First(&cur, "id = ?", in.ID).Error; err != nil {
			return time.Time{}, err
		}
		if cur.DeliverySeenAt != nil {
			return *cur.DeliverySeenAt, nil
		}
	}
	return now, nil
}

// deliveryTxReused is the answer for a destination transaction another sale
// already used to prove its delivery: a mismatch — one credit cannot pay two.
func (s *SellIntentService) deliveryTxReused(in *models.RampSellIntent, tx string) error {
	log.Printf("[ramp-sell] ALERT ramp_sell_delivery_tx_reused_total=1 intent=%s tx=%s", in.ID, tx)
	return fmt.Errorf("%w: %s already proved another sale's delivery", ErrSwapMismatch, tx)
}

// destVerifier is the on-chain proof for a chain id, by way of the chain key the
// ramp's verifiers are stored under.
func (s *SellIntentService) destVerifier(chainID int64) (ERC20TransferVerifier, bool) {
	if s.chainName == nil || s.destVerifiers == nil {
		return nil, false
	}
	key, ok := s.chainName(chainID)
	if !ok {
		return nil, false
	}
	v, ok := s.destVerifiers[key]
	return v, ok && v != nil
}

// SwapNotLandedError is ErrSwapNotLanded carrying the LiFi status the answer
// was read from (nil when LiFi has no record), so the pay reconciler decides
// what the wait means from it instead of asking LiFi again.
type SwapNotLandedError struct{ Status *LiFiTransferStatus }

func (e *SwapNotLandedError) Error() string { return ErrSwapNotLanded.Error() }

func (e *SwapNotLandedError) Unwrap() error { return ErrSwapNotLanded }
