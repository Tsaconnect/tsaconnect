package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// The payer's on-chain mark-paid: prepare, confirm, and the transition the
// reconciler shares.

// PrepareMarkPaid builds the on-chain markPaid tx for the fiat payer. The
// payer is always trade.BuyerID — the on-chain buyer, on either ad side (the
// taker on a sell-ad, the merchant on a buy-ad). The proof is saved now so it
// survives an app restart between signing and confirming, but the trade stays
// pending_payment: only a verified TradePaid receipt (ConfirmMarkPaid) or the
// reconciler seeing chain Paid moves it, because until markPaid lands on-chain
// anyone may cancelByTimeout and claw the crypto back.
func (s *P2PTradeService) PrepareMarkPaid(ctx context.Context, tradeID, payerID uuid.UUID, payerAddress, proofURL string) (*models.P2PTrade, []byte, error) {
	proofURL = strings.TrimSpace(proofURL)
	if proofURL == "" {
		return nil, nil, fmt.Errorf("%w: proof URL required", ErrInvalidParams)
	}
	// The proof renders in the seller's app and the admin console.
	if !isUploadedImageURL(proofURL) {
		return nil, nil, fmt.Errorf("%w: payment proof must be an image uploaded in the app", ErrInvalidParams)
	}
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrTradeNotFound
		}
		return nil, nil, err
	}
	if err := hideSlice(&trade); err != nil {
		return nil, nil, err
	}
	if err := canMarkPaid(payerID, &trade); err != nil {
		return nil, nil, err
	}
	if !common.IsHexAddress(payerAddress) {
		return nil, nil, fmt.Errorf("%w: invalid payer address", ErrInvalidParams)
	}
	escrow, key, err := s.tradeEscrow(&trade)
	if err != nil {
		return nil, nil, err
	}
	if err := s.requireOpenOnChain(ctx, &trade, escrow); err != nil {
		return nil, nil, err
	}
	// Build the tx before saving anything so a refused prepare changes nothing.
	unsignedTx, err := escrow.PrepareMarkPaid(key, payerAddress)
	if err != nil {
		log.Printf("[p2p PrepareMarkPaid] prepare failed: trade=%s payer=%s err=%v", trade.ID, payerAddress, err)
		if isChainRevert(err) {
			return nil, nil, fmt.Errorf("%w: %v", ErrTradePrepareReverted, err)
		}
		return nil, nil, fmt.Errorf("%w: %v", ErrChainUnavailable, err)
	}
	now := s.clock.Now()
	// dropped_at: the expiry canceller has decided this trade (plan C1 P15).
	res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND status = ? AND dropped_at IS NULL", trade.ID, models.P2PTradeStatusPendingPayment).
		Updates(map[string]any{"proof_image_url": proofURL, "updated_at": now})
	if res.Error != nil {
		return nil, nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil, ErrInvalidTransition
	}
	trade.ProofImageURL = proofURL
	trade.UpdatedAt = now
	return &trade, unsignedTx, nil
}

// ConfirmMarkPaid flips pending_payment → paid once the payer's markPaid tx is
// verified on-chain (receipt status 1 + TradePaid for this trade). A replay, or
// a confirm that arrives after the reconciler or a later step already moved the
// trade on, returns the trade unchanged.
func (s *P2PTradeService) ConfirmMarkPaid(ctx context.Context, tradeID, payerID uuid.UUID, txHash string) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if err := hideSlice(&trade); err != nil {
		return nil, err
	}
	if trade.BuyerID != payerID {
		return nil, ErrForbidden
	}
	switch trade.Status {
	case models.P2PTradeStatusPendingPayment:
	case models.P2PTradeStatusPaid, models.P2PTradeStatusReleasing, models.P2PTradeStatusReleased,
		models.P2PTradeStatusDisputed, models.P2PTradeStatusResolved:
		return &trade, nil
	default:
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}
	escrow, key, err := s.tradeEscrow(&trade)
	if err != nil {
		return nil, err
	}
	if _, err := escrow.VerifyTradePaid(txHash, key); err != nil {
		return nil, fmt.Errorf("verify on-chain markPaid: %w", err)
	}
	// Defence in depth: the prepare already required a recorded open, but the
	// row may have changed since; never flip paid on an open we haven't matched.
	if err := s.requireOpenOnChain(ctx, &trade, escrow); err != nil {
		return nil, err
	}
	flipped, err := s.markPaidCore(ctx, &trade, &payerID, map[string]any{"txHash": txHash, "proofUrl": trade.ProofImageURL})
	if err != nil {
		return nil, err
	}
	if !flipped {
		// Lost the race (reconciler or a concurrent confirm got there first).
		if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
			return nil, err
		}
	}
	return &trade, nil
}

// markPaidCore is the pending_payment → paid transition shared by the payer's
// verified confirm and the reconciler's chain-Paid backfill: the guarded
// update, audit, both parties' notifications and the system chat message. It
// reports false (no error) when the trade was no longer pending_payment.
// SystemMarkPaid (PM) keeps its own body — it stamps a provenance ref into
// proof_image_url and posts different copy.
//
// No dropped_at guard here, unlike the prepare: both callers act on a chain
// that already says Paid, so any cancelByTimeout the expiry canceller sent
// reverted, and refusing would strand the row in pending_payment.
func (s *P2PTradeService) markPaidCore(ctx context.Context, t *models.P2PTrade, actor *uuid.UUID, meta map[string]any) (bool, error) {
	now := s.clock.Now()
	res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND status = ?", t.ID, models.P2PTradeStatusPendingPayment).
		Updates(map[string]any{
			"status":         models.P2PTradeStatusPaid,
			"marked_paid_at": now,
			"updated_at":     now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	t.Status = models.P2PTradeStatusPaid
	t.MarkedPaidAt = &now
	t.UpdatedAt = now
	// A slice writes only the caller's drift audit, as on main; an LP sees pool
	// events (C2), never a P2P trade push.
	if t.IsPoolSlice() {
		return true, nil
	}
	s.audit(ctx, models.P2PAuditTradeMarkedPaid, actor, &t.AdID, &t.ID, meta)
	s.emit(events.P2PTradePaid, t.BuyerID, actorOf(actor), "Buyer marked payment sent", "", map[string]any{"tradeId": t.ID})
	s.emit(events.P2PTradePaid, t.SellerID, actorOf(actor), "Buyer marked payment sent — please confirm", "", map[string]any{"tradeId": t.ID})
	s.systemMsg(ctx, t.ID, "Buyer marked payment as sent. Seller — verify your bank and release.")
	return true, nil
}
