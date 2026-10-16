package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	directMovedTitle   = "P2P has moved to BNB Chain"
	directMovedMessage = "Direct P2P trading now runs on BNB Chain. Your ads on Sonic are hidden and can no longer be opened. " +
		"You can still cancel them and withdraw your Sonic balance, then post again on BNB Chain."
)

// NotifyDirectMoved tells every non-PM user still holding something on a pm
// escrow — an ad pending, active or paused there, or a deposit confirmed
// there — that direct P2P moved to BNB Chain (plan p2p-bsc OPS-5). Their ads
// are already hidden and unopenable; cancel and withdraw keep working.
//
// Once per user: anyone who already has the notice is skipped, so it is safe
// to run on every boot. Deposits count by their audit row: a seller who has
// since withdrawn everything gets a harmless notice rather than a balance read
// per user. Returns how many notices were sent.
func (s *P2PTradeService) NotifyDirectMoved(ctx context.Context) (int, error) {
	chains := s.escrows.ChainsWithAudience(config.P2PAudiencePM)
	if len(chains) == 0 {
		return 0, nil
	}
	onChain := make(map[int64]bool, len(chains))
	for _, c := range chains {
		onChain[c] = true
	}

	candidates := map[uuid.UUID]bool{}
	var sellers []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&models.P2PAd{}).
		Where("chain_id IN ? AND pm_id IS NULL AND status IN ?", chains,
			[]string{models.P2PAdStatusPending, models.P2PAdStatusActive, models.P2PAdStatusPaused}).
		Distinct().Pluck("seller_id", &sellers).Error; err != nil {
		return 0, fmt.Errorf("direct-moved notice: ads: %w", err)
	}
	for _, id := range sellers {
		candidates[id] = true
	}

	var deposits []models.P2PAuditLog
	if err := s.db.WithContext(ctx).Select("actor_id", "metadata").
		Where("action = ? AND actor_id IS NOT NULL", models.P2PAuditDepositConfirmed).
		Find(&deposits).Error; err != nil {
		return 0, fmt.Errorf("direct-moved notice: deposits: %w", err)
	}
	for _, d := range deposits {
		var meta struct {
			ChainID int64 `json:"chainId"`
		}
		if json.Unmarshal(d.Metadata, &meta) == nil && onChain[meta.ChainID] {
			candidates[*d.ActorID] = true
		}
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	var notified []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&models.Notification{}).
		Where("type = ?", events.P2PDirectMoved).Distinct().Pluck("user_id", &notified).Error; err != nil {
		return 0, fmt.Errorf("direct-moved notice: sent: %w", err)
	}
	for _, id := range notified {
		delete(candidates, id)
	}

	sent := 0
	for userID := range candidates {
		pm, err := s.lookupActivePM(ctx, userID)
		if err != nil {
			return sent, err
		}
		if pm != nil {
			continue // a PM keeps trading on Sonic
		}
		// No actorId: a system notice, so it is pushed and emailed.
		s.emit(events.P2PDirectMoved, userID, uuid.Nil, directMovedTitle, directMovedMessage,
			map[string]any{"chainIds": chains})
		sent++
	}
	if sent > 0 {
		log.Printf("[p2p] direct-moved notice sent to %d user(s) on chains %v", sent, chains)
	}
	return sent, nil
}
