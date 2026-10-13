package services

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// reconciledReleaseHash is the release_tx_hash reconcileToReleased writes:
// an ordinary trade gets today's "reconciled" marker; a slice keeps whatever
// real hash the ROW holds at write time (the settle proof reads it) — decided
// in SQL, so a hash AutoRelease recorded after the reconciler's read is never
// overwritten (a stale read would otherwise clobber it).
func reconciledReleaseHash(t *models.P2PTrade) any {
	if !t.IsPoolSlice() {
		return "reconciled"
	}
	return gorm.Expr("CASE WHEN release_tx_hash IN ('', 'reconciled') THEN 'reconciled' ELSE release_tx_hash END")
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
