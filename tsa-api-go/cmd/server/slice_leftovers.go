package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// checkSliceLeftovers refuses boot while the retired slice pool still has
// live rows (services.CountSliceLeftovers): this build has no path that
// releases, cancels or forwards them, so starting would strand them silently.
// Only a positive finding refuses; a read error (transient DB, a binary
// started before -migrate) is an ALERT and boot goes on, as the old
// checkPoolUnconfigure did. deploy/one-off/2026-10-14-slice-pool-leftovers.sql
// lists the rows.
func checkSliceLeftovers(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	l, err := services.CountSliceLeftovers(context.Background(), db)
	if err != nil {
		log.Printf("[liquidity] ALERT liquidity_slice_leftovers_unchecked err=%s — boot goes on; run deploy/one-off/2026-10-14-slice-pool-leftovers.sql by hand",
			services.RedactAlertText(err.Error()))
		return nil
	}
	if !l.Any() {
		return nil
	}
	log.Printf("[liquidity] ALERT liquidity_slice_leftovers_refused slices=%d intents=%d forwards=%d", l.Slices, l.Intents, l.Forwards)
	return fmt.Errorf("liquidity_slice_leftovers_refused: the retired slice pool has live rows (slices=%d intents=%d lp_forwards=%d) that this build cannot settle — settle them on the previous release (deploy/one-off/2026-10-14-slice-pool-leftovers.sql lists them) before deploying",
		l.Slices, l.Intents, l.Forwards)
}
