package services

import (
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// notPoolSlice keeps a p2p_trades query to ordinary trades (spec 2026-10-08
// §23, plan C1 P13). Every P2P/PM path that lists trades for a user, a
// watchdog nudge, a payment match or an automated release takes it: a slice
// is pool money, settled only by the pool's own paths.
func notPoolSlice(db *gorm.DB) *gorm.DB { return db.Where("p2p_trades.it_intent_id IS NULL") }

// hideSlice answers a pool slice as not found on every single-trade user
// path, so no user (the LP included) can read or act on one through the API.
func hideSlice(t *models.P2PTrade) error {
	if t.IsPoolSlice() {
		return ErrTradeNotFound
	}
	return nil
}
