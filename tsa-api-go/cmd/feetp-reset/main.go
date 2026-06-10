// Operator command: run the one-time v2 cutover-reset directly against the DB,
// without going through the admin HTTP endpoint (avoids needing an admin JWT).
// Calls the exact same services.ResetForV2Cutover the handler does.
//
// Run (from tsa-api-go/):
//   RESET_LABEL=2026-06-10-testnet-rehearsal go run ./cmd/feetp-reset
//
// This ZEROES tp/cashback balances and clears all v2 reward state (archiving the
// legacy ledgers first). One-time per label (a re-run with the same label is
// refused). Must run BEFORE FEE_MODEL_V2_ENABLED is flipped on.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func main() {
	_ = godotenv.Load()
	cfg := config.Load()
	db, err := config.ConnectDB(cfg)
	if err != nil {
		log.Fatalf("[feetp-reset] db connect failed: %v", err)
	}

	label := os.Getenv("RESET_LABEL")
	if label == "" {
		log.Fatalf("[feetp-reset] set RESET_LABEL (e.g. 2026-06-10-testnet-rehearsal)")
	}
	const confirm = "I_UNDERSTAND_THIS_ZEROES_BALANCES"

	if cfg.IsFeeModelV2Enabled() {
		log.Printf("[feetp-reset] WARNING: FEE_MODEL_V2_ENABLED is ON — the reset is meant to run BEFORE the flag flip.")
	}

	summary, err := services.ResetForV2Cutover(context.Background(), db, label, confirm)
	if err != nil {
		log.Fatalf("[feetp-reset] reset failed: %v", err)
	}
	fmt.Printf("[feetp-reset] OK label=%q summary=%+v\n", label, summary)
}
