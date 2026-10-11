package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// A party raising a dispute: prepare (and replacing an abandoned prepare),
// confirm with the raiser check, and the operator alert.

// disputePrepareStaleAfter is how long an unconfirmed dispute row is protected
// from replacement by the counter-party.
const disputePrepareStaleAfter = 10 * time.Minute

// replaceableDisputeRow is the existing-row gate of PrepareRaiseDispute,
// shared with the dispute gas need. An open dispute row that is not yet on
// chain is an abandoned prepare (the user closed the sign sheet), and the
// TradeID unique index allows one row per trade, so a retry rewrites it. It
// returns that row (nil when there is none: create one), or a refusal wrapping
// ErrDisputeAlreadyOpen when the counter-party's prepare is still live (only a
// stale one may be replaced by the other party) or the chain already shows
// Disputed (a real duplicate). A failed chain read is ErrChainUnavailable.
func (s *P2PTradeService) replaceableDisputeRow(ctx context.Context, trade *models.P2PTrade, userID uuid.UUID, escrow P2PEscrowAdapter) (*models.P2PDispute, error) {
	var existing models.P2PDispute
	err := s.db.WithContext(ctx).
		Where("trade_id = ? AND status = ?", trade.ID, models.P2PDisputeStatusOpen).
		First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if existing.RaisedBy != userID && s.clock.Now().Sub(existing.CreatedAt) <= disputePrepareStaleAfter {
		return nil, refuse(ErrDisputeAlreadyOpen)
	}
	onchain, err := escrow.ReadTrade(HashID(trade.ID))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChainUnavailable, err)
	}
	if onchain.Status == OnChainP2PStatusDisputed {
		return nil, refuse(ErrDisputeAlreadyOpen)
	}
	return &existing, nil
}

// requireDisputeRaiserMatches checks the TradeDisputed `by` address is the
// on-chain buyer or seller matching the role on the dispute row, so a row
// replaced by the other party cannot be confirmed with the first party's tx.
// The contract guarantees by is the trade's buyer or seller, so there is no
// "unknown" case: a failed chain read is ErrChainUnavailable, never a skip.
func (s *P2PTradeService) requireDisputeRaiserMatches(ctx context.Context, escrow P2PEscrowAdapter, trade *models.P2PTrade, confirmer uuid.UUID, ev *P2PTradeDisputedEvent) error {
	role := models.P2PDisputeRoleSeller
	if confirmer == trade.BuyerID {
		role = models.P2PDisputeRoleBuyer
	}
	var row models.P2PDispute
	if err := s.db.WithContext(ctx).Where("trade_id = ?", trade.ID).First(&row).Error; err == nil {
		role = row.RaisedRole
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	onchain, err := escrow.ReadTrade(HashID(trade.ID))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrChainUnavailable, err)
	}
	want := onchain.Seller
	if role == models.P2PDisputeRoleBuyer {
		want = onchain.Buyer
	}
	if ev.By != want {
		return fmt.Errorf("%w: dispute transaction was not sent by the party that raised it", ErrForbidden)
	}
	return nil
}

// PrepareRaiseDispute prepares an on-chain raiseDispute tx and persists a dispute
// row tied to the trade. The trade itself isn't transitioned until
// ConfirmRaiseDispute verifies the receipt — the dispute row sits in `open`
// state in the interim so the UI can surface "dispute pending broadcast".
// Either buyer or seller (in Paid state only) may dispute. An unconfirmed row
// is replaced by a retry: always by the same raiser, by the other party only
// once it is older than disputePrepareStaleAfter.
func (s *P2PTradeService) PrepareRaiseDispute(ctx context.Context, tradeID, userID uuid.UUID, callerAddress, reason string, evidenceURLs []string) ([]byte, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason required", ErrInvalidParams)
	}
	// Evidence is optional: image URLs uploaded client-side (Cloudinary). Cap the
	// count and accept only the upload host; store as JSON on the dispute row.
	evidenceJSON := ""
	if len(evidenceURLs) > 0 {
		if len(evidenceURLs) > 6 {
			return nil, fmt.Errorf("%w: at most 6 evidence images", ErrInvalidParams)
		}
		for _, u := range evidenceURLs {
			if !isUploadedImageURL(u) {
				return nil, fmt.Errorf("%w: evidence must be images uploaded in the app", ErrInvalidParams)
			}
		}
		b, _ := json.Marshal(evidenceURLs)
		evidenceJSON = string(b)
	}
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	role, err := canDispute(userID, &trade)
	if err != nil {
		return nil, err
	}
	// Resolve the trade's chain BEFORE persisting the dispute row so an
	// unconfigured chain fails loud without leaving an orphan dispute.
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}
	// A never-opened-on-chain trade has no escrowed funds to dispute; refuse so a
	// phantom trade can't be escalated to `disputed` and pull in an admin. An
	// open whose confirm was lost is backfilled from chain here.
	if err := s.requireOpenOnChain(ctx, &trade, escrow); err != nil {
		return nil, err
	}
	replace, err := s.replaceableDisputeRow(ctx, &trade, userID, escrow)
	if err != nil {
		return nil, err
	}

	// Build the unsigned tx BEFORE touching the row: a build failure (RPC nonce
	// or gas) must not destroy a replaced dispute's reason/evidence or hand the
	// replacer a fresh stale-lock.
	unsignedTx, err := escrow.PrepareRaiseDispute(HashID(trade.ID), callerAddress)
	if err != nil {
		return nil, fmt.Errorf("prepare raiseDispute: %w", err)
	}

	nonce := uuid.New()
	if replace != nil {
		// Conditional on the nonce we read: a concurrent retry that rewrote
		// the row first wins, and this one is refused.
		q := s.db.WithContext(ctx).Model(&models.P2PDispute{}).
			Where("id = ? AND status = ? AND raised_by = ?", replace.ID, models.P2PDisputeStatusOpen, replace.RaisedBy)
		if replace.PrepareNonce != nil {
			q = q.Where("prepare_nonce = ?", *replace.PrepareNonce)
		} else {
			q = q.Where("prepare_nonce IS NULL")
		}
		res := q.Updates(map[string]any{
			"raised_by":     userID,
			"raised_role":   role,
			"reason":        reason,
			"evidence_urls": evidenceJSON,
			"created_at":    s.clock.Now(),
			"prepare_nonce": nonce,
			"resolved_by":   nil,
			"resolution":    "",
		})
		if res.Error != nil {
			return nil, fmt.Errorf("replace dispute: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return nil, ErrDisputeAlreadyOpen
		}
	} else {
		dispute := &models.P2PDispute{
			ID:           uuid.New(),
			TradeID:      trade.ID,
			RaisedBy:     userID,
			RaisedRole:   role,
			Reason:       reason,
			EvidenceURLs: evidenceJSON,
			Status:       models.P2PDisputeStatusOpen,
			CreatedAt:    s.clock.Now(),
			PrepareNonce: &nonce,
		}
		if err := s.db.WithContext(ctx).Create(dispute).Error; err != nil {
			// A concurrent first prepare created the trade's one row first.
			if isDuplicateKeyErr(err) {
				return nil, ErrDisputeAlreadyOpen
			}
			return nil, fmt.Errorf("persist dispute: %w", err)
		}
	}

	auditMeta := map[string]any{"role": role, "reason": reason}
	if replace != nil {
		auditMeta["replacedRaisedBy"] = replace.RaisedBy
		auditMeta["replacedReason"] = replace.Reason
		auditMeta["replacedEvidenceUrls"] = replace.Evidence
	}
	s.audit(ctx, models.P2PAuditTradeDisputed, &userID, &trade.AdID, &trade.ID, auditMeta)
	// Notify only the raiser at prepare time. The counter-party notice and the
	// "funds frozen" chat message fire in ConfirmRaiseDispute — after the dispute
	// is actually on chain — so a never-broadcast prepare can't signal the
	// counter-party or freeze the trade chat.
	s.emit(events.P2PTradeDisputed, userID, userID, "Dispute prepared — sign to submit", "", map[string]any{
		"tradeId": trade.ID, "role": role,
	})

	return unsignedTx, nil
}

// ConfirmRaiseDispute verifies the on-chain TradeDisputed event and
// transitions the trade to Disputed. Idempotent across retries.
// errDisputeAlreadyRecorded is ConfirmRaiseDispute's internal signal that the
// conditional flip lost to a writer that had already set disputed.
var errDisputeAlreadyRecorded = errors.New("p2p: dispute already recorded")

func (s *P2PTradeService) ConfirmRaiseDispute(ctx context.Context, tradeID, userID uuid.UUID, txHash string) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.BuyerID != userID && trade.SellerID != userID {
		return nil, ErrForbidden
	}
	if trade.Status == models.P2PTradeStatusDisputed {
		return &trade, nil
	}
	if trade.Status != models.P2PTradeStatusPaid && trade.Status != models.P2PTradeStatusPendingPayment {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}

	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}
	ev, err := escrow.VerifyTradeDisputed(txHash, HashID(trade.ID))
	if err != nil {
		return nil, fmt.Errorf("verify on-chain dispute: %w", err)
	}
	if err := s.requireDisputeRaiserMatches(ctx, escrow, &trade, userID, ev); err != nil {
		return nil, err
	}

	// The status read above predates on-chain verification; re-read it under a
	// row lock so the alert reports the status that was actually flipped.
	var prior string
	flipErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cur, err := lockTradeStatus(tx, trade.ID)
		if err != nil {
			return err
		}
		res := tx.Model(&models.P2PTrade{}).
			Where("id = ? AND status IN ?", trade.ID,
				[]string{models.P2PTradeStatusPaid, models.P2PTradeStatusPendingPayment}).
			Updates(map[string]any{
				"status":     models.P2PTradeStatusDisputed,
				"updated_at": s.clock.Now(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			if cur == models.P2PTradeStatusDisputed {
				return errDisputeAlreadyRecorded
			}
			return ErrInvalidTransition
		}
		prior = cur
		return nil
	})
	if errors.Is(flipErr, errDisputeAlreadyRecorded) {
		// The reconciler (or a concurrent confirm) recorded this verified
		// dispute between our read and the flip: same outcome, so answer like
		// a replay. It already audited, notified and alerted.
		if err := s.db.WithContext(ctx).First(&trade, "id = ?", trade.ID).Error; err != nil {
			return nil, err
		}
		return &trade, nil
	}
	if flipErr != nil {
		return nil, flipErr
	}
	trade.Status = models.P2PTradeStatusDisputed

	// Symmetric with the other Confirm* paths: emit an audit row + bus event
	// once the on-chain transition is verified. Without this the operator
	// timeline only shows the intent (RaiseDispute) and not the chain-side
	// settle, making it hard to investigate disputes after the fact.
	s.audit(ctx, models.P2PAuditTradeDisputeConfirmed, &userID, &trade.AdID, &trade.ID,
		map[string]any{"txHash": txHash})
	s.emit(events.P2PTradeDisputed, userID, userID, "Dispute confirmed on chain", "",
		map[string]any{"tradeId": trade.ID})
	// Now that the dispute is on chain, notify the counter-party and freeze the
	// trade chat (deferred from PrepareRaiseDispute).
	other := trade.SellerID
	if userID == trade.SellerID {
		other = trade.BuyerID
	}
	s.emit(events.P2PTradeDisputed, other, userID, "Counter-party opened a dispute on this trade", "",
		map[string]any{"tradeId": trade.ID})
	s.systemMsg(ctx, trade.ID, "A dispute has been opened. Funds are frozen pending admin review.")
	s.alertDisputeFiled(ctx, &trade, prior, false)
	return &trade, nil
}

// alertDisputeFiled emails operators about a dispute that just flipped the
// trade to disputed. The dispute row is best effort: the alert still goes out
// without role/reason rather than being lost.
func (s *P2PTradeService) alertDisputeFiled(ctx context.Context, trade *models.P2PTrade, from string, placeholder bool) {
	var d models.P2PDispute
	_ = s.db.WithContext(ctx).Where("trade_id = ?", trade.ID).First(&d).Error
	if placeholder {
		d.RaisedRole = "unknown (reconciler backfill)"
	}
	s.alert(ctx, P2PAlertDisputeFiled, trade, func(a *P2PAlert) {
		a.FromStatus = from
		a.RaisedRole = d.RaisedRole
		a.Reason = d.Reason
	})
}
