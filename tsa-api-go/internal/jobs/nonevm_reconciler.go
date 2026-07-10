package jobs

import (
	"context"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// NonEVMReconciler walks pending non-EVM activity rows and finalizes them
// against on-chain truth. Sibling to the EVM ChainReconciler; family-aware
// because confirmation semantics differ (Tron ~3s, BTC ~10min, Solana sub-s).
type NonEVMReconciler struct {
	db  *gorm.DB
	reg *chains.Registry
}

func NewNonEVMReconciler(db *gorm.DB, reg *chains.Registry) *NonEVMReconciler {
	return &NonEVMReconciler{db: db, reg: reg}
}

func (r *NonEVMReconciler) Name() string            { return "nonevm-reconciler" }
func (r *NonEVMReconciler) Interval() time.Duration { return 45 * time.Second }

func (r *NonEVMReconciler) Run(ctx context.Context) error {
	if r.db == nil || r.reg == nil {
		return nil // dormant
	}
	// Oldest-first so a backlog above the cap drains from the front instead of
	// starving the earliest-pending rows.
	var pending []models.WalletActivity
	if err := r.db.Where("status = ?", "pending").Order("created_at asc").Limit(100).Find(&pending).Error; err != nil {
		return err
	}
	for i := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		row := pending[i]
		network := row.Network
		if network == "" {
			network = "mainnet" // rows written before the network column existed
		}
		client, ok := r.reg.ForKey(network, row.ChainKey)
		if !ok {
			continue
		}
		st, err := client.GetTxStatus(ctx, row.Hash)
		if err != nil || !st.Confirmed {
			continue
		}
		status := "confirmed"
		if !st.Success {
			status = "failed"
		}
		r.db.Model(&models.WalletActivity{}).Where("id = ?", row.ID).
			Updates(map[string]any{"status": status, "confirmations": st.Confirmations})
	}
	return nil
}
