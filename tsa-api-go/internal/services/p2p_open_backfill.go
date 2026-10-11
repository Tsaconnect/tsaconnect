package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// openTxReconciled is the open_tx_hash marker written when the open was found
// on chain but its confirm-open never reached us (app killed after broadcast).
// It is not a tx hash: nothing may look up a receipt or build an explorer link
// from it. Same convention as release_tx_hash = "reconciled".
const openTxReconciled = "reconciled"

// openDeadlineFromChain is the contract's cancelByTimeout deadline:
// on-chain createdAt + paymentWindow. False when createdAt is unknown.
func openDeadlineFromChain(onchain *OnChainP2PTrade) (time.Time, bool) {
	if onchain == nil || onchain.CreatedAt == nil || onchain.CreatedAt.Sign() <= 0 {
		return time.Time{}, false
	}
	return time.Unix(onchain.CreatedAt.Int64(), 0).UTC().Add(p2pPaymentWindow), true
}

// openRecordUpdates is the column set written when a trade's on-chain open is
// recorded — by ConfirmOpenTrade (real tx hash) and by the backfill (marker).
// The off-chain deadline is re-anchored to the on-chain createdAt because
// expires_at was set at intent time and drifts early if the open mined late,
// which would nudge a timeout-cancel the contract reverts. With no on-chain
// createdAt the intent-time deadline is kept.
//
// fee_bps is the trade's snapshotted platform+gas bps (the fee the escrow takes
// at release); an unreadable snapshot leaves it unset.
func (s *P2PTradeService) openRecordUpdates(openTxHash string, escrow P2PEscrowAdapter, t *models.P2PTrade, onchain *OnChainP2PTrade) map[string]any {
	updates := map[string]any{"open_tx_hash": openTxHash, "updated_at": s.clock.Now()}
	if escrow != nil {
		p, g, found := escrow.ReadTradeFeeSnapshot(HashID(t.ID))
		if bps, ok := feeBpsFromSnapshot(p, g, found); ok {
			updates["fee_bps"] = bps
			t.FeeBps = &bps
		}
	}
	if deadline, ok := openDeadlineFromChain(onchain); ok {
		updates["expires_at"] = deadline
	}
	return updates
}

// onChainOpenMatches decides whether the escrow's trade under HashID(t.ID) is
// the open we prepared for t on ad. The contract lets the opener choose the
// token (any accepted one) and, on openTradeWithDeposit, the merchant (the
// on-chain buyer) freely — so an opener naming their own second wallet as
// merchant could later cancelByTimeout and keep both the crypto and the fiat
// the real merchant paid. Amount alone is therefore never enough: the token
// and the ad's own wallet on its side of the trade must match too.
//
// Sell-ad (pooled, openTrade): the ad's wallet signed the voucher, so it is
// the on-chain seller. Buy-ad (JIT, openTradeWithDeposit): the prepare passes
// ad.SellerAddress as merchant, so it is the on-chain buyer. PM ads are
// ordinary sell/buy ads whose SellerAddress is the PM maker wallet.
//
// The opener's side must be the wallet the open was prepared for
// (t.OpenerAddress): openTrade sets buyer = msg.sender and
// openTradeWithDeposit sets seller = msg.sender, so anyone holding the ad's
// voucher (or any wallet, on a buy-ad) can open under our trade id first and
// would otherwise be recorded — and later paid — as the counterparty. A row
// with no stored opener (prepared before the column existed) is refused.
func onChainOpenMatches(t *models.P2PTrade, ad *models.P2PAd, tokenAddr string, onchain *OnChainP2PTrade) error {
	mismatch := func(what string) error {
		return fmt.Errorf("%w: on-chain trade does not match this trade's %s", ErrInvalidTransition, what)
	}
	if onchain == nil || !onchain.Exists {
		return fmt.Errorf("%w: trade not opened on-chain", ErrInvalidTransition)
	}
	expected, ok := new(big.Int).SetString(t.TokenAmount, 10)
	if !ok || onchain.Amount == nil || onchain.Amount.Cmp(expected) != 0 {
		return mismatch("amount")
	}
	if ad == nil || ad.ID != t.AdID {
		return fmt.Errorf("%w: trade's ad not found", ErrInvalidTransition)
	}
	wantFunding, adSide, openerSide := models.P2PTradeFundingPooled, onchain.Seller, onchain.Buyer
	if ad.Side == models.P2PAdSideBuy {
		wantFunding, adSide, openerSide = models.P2PTradeFundingDepositedByTaker, onchain.Buyer, onchain.Seller
	}
	if onchain.Funding != wantFunding || t.Funding != wantFunding {
		return mismatch("funding")
	}
	if !common.IsHexAddress(tokenAddr) || !strings.EqualFold(onchain.Token.Hex(), tokenAddr) {
		return mismatch("token")
	}
	// No recorded wallet means nothing to check against: refuse, never skip.
	if !common.IsHexAddress(ad.SellerAddress) {
		return fmt.Errorf("%w: the trade's ad has no wallet recorded", ErrInvalidTransition)
	}
	if adSide != common.HexToAddress(ad.SellerAddress) {
		return mismatch("counterparty")
	}
	if !common.IsHexAddress(t.OpenerAddress) {
		return fmt.Errorf("%w: trade has no opener wallet recorded", ErrInvalidTransition)
	}
	if openerSide != common.HexToAddress(t.OpenerAddress) {
		return mismatch("opener")
	}
	return nil
}

// verifyOpenOnChain loads what onChainOpenMatches needs for t (its ad and
// the token's configured address) and applies it. Every refusal is logged as
// an ALERT (path = "confirm" or "backfill"): a mismatch is either a bug or an
// opener tampering with the open.
func (s *P2PTradeService) verifyOpenOnChain(ctx context.Context, path string, t *models.P2PTrade, onchain *OnChainP2PTrade) error {
	var ad *models.P2PAd
	var row models.P2PAd
	err := s.db.WithContext(ctx).First(&row, "id = ?", t.AdID).Error
	switch {
	case err == nil:
		ad = &row
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return err
	}
	tokenAddr, err := s.resolveTokenAddress(t.ChainID, t.Token)
	if err != nil {
		return err
	}
	if err := onChainOpenMatches(t, ad, tokenAddr, onchain); err != nil {
		if onchain != nil && onchain.Exists {
			log.Printf("[p2p] ALERT p2p_open_backfill_mismatch_total=1 path=%s trade=%s err=%v chainAmount=%v dbAmount=%s chainFunding=%d dbFunding=%d chainToken=%s token=%s chainSeller=%s chainBuyer=%s opener=%q",
				path, t.ID, err, onchain.Amount, t.TokenAmount, onchain.Funding, t.Funding,
				onchain.Token.Hex(), tokenAddr, onchain.Seller.Hex(), onchain.Buyer.Hex(), t.OpenerAddress)
		}
		return err
	}
	return nil
}

// recordOpenFromChain records a trade's open from the escrow's view when the
// confirm-open was lost. It refuses an on-chain trade that onChainOpenMatches
// rejects — that is not the open we prepared. Reports true only when this
// call wrote the open; false when one was already recorded (t is refreshed
// from the row in that case).
func (s *P2PTradeService) recordOpenFromChain(ctx context.Context, escrow P2PEscrowAdapter, t *models.P2PTrade, onchain *OnChainP2PTrade) (bool, error) {
	if err := s.verifyOpenOnChain(ctx, "backfill", t, onchain); err != nil {
		return false, err
	}
	updates := s.openRecordUpdates(openTxReconciled, escrow, t, onchain)
	res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND (open_tx_hash = '' OR open_tx_hash IS NULL)", t.ID).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		// A confirm-open (or another backfill) recorded it between our read and here.
		var current models.P2PTrade
		if err := s.db.WithContext(ctx).Select("open_tx_hash").First(&current, "id = ?", t.ID).Error; err != nil {
			return false, err
		}
		t.OpenTxHash = current.OpenTxHash
		return false, nil
	}
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &t.AdID, &t.ID, map[string]any{
		"reason": "open on chain, confirm lost",
	})
	t.OpenTxHash = openTxReconciled
	if deadline, ok := updates["expires_at"].(time.Time); ok {
		t.ExpiresAt = deadline
	}
	t.UpdatedAt = updates["updated_at"].(time.Time)
	return true, nil
}

// openRecorded reports whether the trade's escrow is known to be funded,
// backfilling a lost confirm-open from chain when it is.
func (s *P2PTradeService) openRecorded(ctx context.Context, t *models.P2PTrade, escrow P2PEscrowAdapter) (bool, error) {
	if strings.TrimSpace(t.OpenTxHash) != "" {
		return true, nil
	}
	onchain, err := escrow.ReadTrade(HashID(t.ID))
	if err != nil {
		return false, fmt.Errorf("%w: read on-chain trade: %v", ErrChainUnavailable, err)
	}
	if !onchain.Exists {
		return false, nil
	}
	if _, err := s.recordOpenFromChain(ctx, escrow, t, onchain); err != nil {
		return false, err
	}
	return strings.TrimSpace(t.OpenTxHash) != "", nil
}

// requireOpenOnChain is the one gate for "the escrow is funded" on the
// payer's mark-paid and on raising a dispute: a never-opened trade has nothing
// escrowed, so flipping it to paid or disputed would leave a state the
// reconciler can't clear. SystemMarkPaid (PM) keeps its own inline gate.
func (s *P2PTradeService) requireOpenOnChain(ctx context.Context, t *models.P2PTrade, escrow P2PEscrowAdapter) error {
	opened, err := s.openRecorded(ctx, t, escrow)
	if err != nil {
		return err
	}
	if !opened {
		return fmt.Errorf("%w: trade not yet opened on-chain", ErrInvalidTransition)
	}
	return nil
}
