package services

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// recordSliceReleaseHash records a release attempt's hash on a pool slice
// BEFORE its receipt is read (plan C2 P2) — only into an empty column: a
// recorded hash is never overwritten unless proven reverted, and that path
// clears it first (re-review I-A). Detached: the caller's context may end.
func (s *P2PTradeService) recordSliceReleaseHash(ctx context.Context, id uuid.UUID, hash string) {
	if hash == "" {
		return
	}
	res := s.db.WithContext(detached(ctx)).Model(&models.P2PTrade{}).Where("id = ? AND release_tx_hash = ''", id).
		Update("release_tx_hash", hash)
	if res.Error != nil || res.RowsAffected != 1 {
		log.Printf("[liquidity] ALERT liquidity_pool_release_hash_unrecorded slice=%s tx=%s — a hash is already recorded, or the write failed", id, hash)
	}
}

// sliceReconciledHash is the release_tx_hash reconcileToReleased writes: a
// slice keeps a real hash (the settle proof reads it); everything else gets
// today's "reconciled" marker.
func sliceReconciledHash(t *models.P2PTrade) string {
	if t.IsPoolSlice() && t.ReleaseTxHash != "" && t.ReleaseTxHash != "reconciled" {
		return t.ReleaseTxHash
	}
	return "reconciled"
}

// sliceResolveClaimAbandoned (review I-3): the only proof that clears a
// resolve claim no hash ever replaced.
func (s *P2PTradeService) sliceResolveClaimAbandoned(ctx context.Context, t *models.P2PTrade) bool {
	if s.poolSlices == nil || !strings.HasPrefix(t.ReleaseTxHash, sliceResolveClaimPrefix) {
		return false
	}
	gone, err := s.poolSlices.SliceResolveClaimAbandoned(ctx, t)
	return err == nil && gone
}

// sliceHashProvenReverted (re-review I-A/I-C): the only proof that clears a
// slice's recorded hash. "" and a resolve claim are never reverted.
func (s *P2PTradeService) sliceHashProvenReverted(ctx context.Context, t *models.P2PTrade) bool {
	if s.poolSlices == nil || t.ReleaseTxHash == "" || strings.HasPrefix(t.ReleaseTxHash, sliceResolveClaimPrefix) {
		return false
	}
	gone, err := s.poolSlices.SliceHashReverted(ctx, t)
	return err == nil && gone
}

// noteSliceUnresolved alerts, at most hourly per slice, a slice whose
// recorded (or missing) hash cannot be decided automatically.
func (s *P2PTradeService) noteSliceUnresolved(ctx context.Context, t *models.P2PTrade, alert string) {
	if claimRunAt(ctx, s.db, alert+":"+t.ID.String(), time.Hour, s.clock.Now()) {
		log.Printf("[liquidity] ALERT %s slice=%s intent=%s status=%s hash=%q — not decided automatically; check the release key's transactions for this trade on the explorer",
			alert, t.ID, t.ITIntentID, t.Status, t.ReleaseTxHash)
	}
}
