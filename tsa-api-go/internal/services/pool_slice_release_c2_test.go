package services

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// revertingProbe reports the listed hashes' receipts as reverted.
type revertingProbe struct {
	TreasuryTxProbe
	reverted map[common.Hash]bool
}

func (p *revertingProbe) TransactionReceipt(ctx context.Context, h common.Hash) (*types.Receipt, error) {
	r, err := p.TreasuryTxProbe.TransactionReceipt(ctx, h)
	if err == nil && r != nil && p.reverted[h] {
		cp := *r
		cp.Status = types.ReceiptStatusFailed
		return &cp, nil
	}
	return r, err
}

func paidOne(t *testing.T) (*poolOpenRig, models.P2PTrade, *chainCountingSender) {
	t.Helper()
	r, intent, sl, _, sender := openOne(t)
	if ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_"+intent.ID.String()[:6], intent.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim %v %v", ok, err)
	}
	return r, sl, sender
}

// C-1: a stale `releasing` slice whose release may still land is never rolled
// back; once its recorded receipt reverted deep, it is (and the hash cleared).
func TestReconcile_SliceStaleReleasingWaitsForTheReceipt(t *testing.T) {
	r, sl, _ := paidOne(t)
	ctx := context.Background()
	hash := common.HexToHash("0xcd00000000000000000000000000000000000000000000000000000000000001")
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Updates(map[string]any{
		"status": models.P2PTradeStatusReleasing, "release_tx_hash": hash.Hex(), "updated_at": time.Now().Add(-11 * time.Minute)})
	r.chain.nextNonce, r.chain.latest = 11, 10 // the release key still has a tx pending
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusReleasing || got.ReleaseTxHash != hash.Hex() {
		t.Fatalf("slice = %s %q, want still releasing: its release may land", got.Status, got.ReleaseTxHash)
	}
	r.chain.receipts[hash] = r.chain.head - 10                                                             // mined 11+ deep …
	r.desk.c.Probe = &revertingProbe{TreasuryTxProbe: r.chain, reverted: map[common.Hash]bool{hash: true}} // … and reverted
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("updated_at", time.Now().Add(-11*time.Minute))
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	r.db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusPaid || got.ReleaseTxHash != "" {
		t.Fatalf("slice = %s %q, want back to paid with the reverted hash cleared", got.Status, got.ReleaseTxHash)
	}
}

// I-7 under re-review I-A's rule: a disputed slice whose recorded hash is
// PROVEN reverted (receipt failed, 5 deep) is cleared and decided at once.
func TestPoolSlice_DisputeWithAStaleReleaseHashIsDecided(t *testing.T) {
	r, sl, sender := paidOne(t)
	stale := common.HexToHash("0xee00000000000000000000000000000000000000000000000000000000000001")
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("release_tx_hash", stale.Hex())
	r.chain.receipts[stale] = r.chain.head - 10 // 11+ deep
	r.desk.c.Probe = &revertingProbe{TreasuryTxProbe: r.chain, reverted: map[common.Hash]bool{stale: true}}
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusDisputed)
	if _, err := r.trades.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.chain.resolves) != 1 || !r.chain.resolves[0] || len(sender.sendChains) != 1 {
		t.Fatalf("resolves=%v sends=%v, want the dispute decided to the treasury despite the stale hash", r.chain.resolves, sender.sendChains)
	}
}

// Re-review I-A: a recorded hash that is NOT proven reverted (no receipt yet)
// is never replaced — the dispute waits; no second resolve is sent.
func TestPoolSlice_DisputeNeverOverwritesAnUnprovenHash(t *testing.T) {
	r, sl, sender := paidOne(t)
	inflight := common.HexToHash("0xab00000000000000000000000000000000000000000000000000000000000002")
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("release_tx_hash", inflight.Hex())
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusDisputed)
	for i := 0; i < 2; i++ {
		if _, err := r.trades.ReconcileTrade(context.Background(), sl.ID); err != nil {
			t.Fatal(err)
		}
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if len(sender.sendChains) != 0 || got.ReleaseTxHash != inflight.Hex() {
		t.Fatalf("sends=%d hash=%q, want no resend and the hash kept", len(sender.sendChains), got.ReleaseTxHash)
	}
}
