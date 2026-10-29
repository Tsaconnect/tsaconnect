package services

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Coverage pins for direct P2P on BSC (plan docs/plans/p2p-bsc API-10): the
// existing reconciler, release invariant, fee drift reconciler and fee-wallet
// panel must treat the BSC direct escrow (56, 18 dp) as they treat Sonic.

const directBSCFeeManager = "0x00000000000000000000000000000000000FEE01"

// seedDirectBSCTrade inserts a BSC (56) sell ad and one trade on it in the
// given status, sized in 18-dp base units.
func seedDirectBSCTrade(t *testing.T, r *audienceRig, status, amount string) *models.P2PTrade {
	t.Helper()
	seller, buyer := uuid.New(), uuid.New()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ad := &models.P2PAd{
		ID: uuid.New(), SellerID: seller, Side: models.P2PAdSideSell, Token: "USDT", ChainID: bscDirectChain,
		Amount: amount, Remaining: "0", Reserved: amount,
		FiatCurrency: "NGN", Rate: "1300", RateType: "fixed", FloatAnchor: "bybit_sell",
		Status: models.P2PAdStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := r.db.Create(ad).Error; err != nil {
		t.Fatalf("seed BSC ad: %v", err)
	}
	tr := &models.P2PTrade{
		ID: uuid.New(), AdID: ad.ID, SellerID: seller, BuyerID: buyer,
		Token: "USDT", ChainID: bscDirectChain, Funding: models.P2PTradeFundingPooled,
		TokenAmount: amount, FiatAmount: "13000", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: status, OpenTxHash: "0xopen",
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := r.db.Create(tr).Error; err != nil {
		t.Fatalf("seed BSC trade: %v", err)
	}
	return tr
}

// The chain reconciler converges a BSC trade from the BSC escrow, never from
// Sonic's: Sonic's mock answers "cancelled", so reading it would cancel.
func TestDirectBSC_ReconcileTrade_ReadsTheDirectEscrow(t *testing.T) {
	r := newAudienceRig(t)
	amount := "30000000000000000000" // 30 USDT at 18 dp
	tr := seedDirectBSCTrade(t, r, models.P2PTradeStatusPaid, amount)
	amt, _ := new(big.Int).SetString(amount, 10)
	r.bsc.onChainTrade = &OnChainP2PTrade{
		Status: OnChainP2PStatusReleased, Funding: models.P2PTradeFundingPooled,
		Token: common.HexToAddress("0x29219dd400f2Bf60E5a23d13Be72B486D4038894"), Amount: amt, Exists: true,
	}
	r.sonic.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusCancelled, Amount: big.NewInt(1), Exists: true}

	out, err := r.svc.ReconcileTrade(context.Background(), tr.ID)
	if err != nil {
		t.Fatalf("ReconcileTrade: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusReleased {
		t.Fatalf("reconcile = %+v, want released from the BSC escrow", out)
	}
	var after models.P2PTrade
	r.db.First(&after, "id = ?", tr.ID)
	if after.Status != models.P2PTradeStatusReleased {
		t.Fatalf("status = %q, want released", after.Status)
	}
}

// The release money invariant (AmountToBuyer + FeeToSystem == amount) holds
// to the wei on 18-dp BSC amounts, and a 1-wei shortfall still refuses.
func TestDirectBSC_ConfirmRelease_MoneyInvariant18dp(t *testing.T) {
	for _, amount := range []string{"10000000000000000000", "123456789012345678901"} {
		t.Run(amount, func(t *testing.T) {
			r := newAudienceRig(t)
			tr := seedDirectBSCTrade(t, r, models.P2PTradeStatusPaid, amount)
			amt, _ := new(big.Int).SetString(amount, 10)
			fee := new(big.Int).Div(new(big.Int).Mul(amt, big.NewInt(150)), big.NewInt(10000))
			toBuyer := new(big.Int).Sub(amt, fee)

			// One wei short: refused, the trade stays paid.
			r.bsc.releasedEvent = &P2PTradeReleasedEvent{TradeID: HashID(tr.ID),
				AmountToBuyer: new(big.Int).Sub(toBuyer, big.NewInt(1)), FeeToSystem: fee}
			if _, err := r.svc.ConfirmRelease(context.Background(), tr.ID, tr.SellerID, "0xrel-short"); err == nil {
				t.Fatal("a release 1 wei short of the trade amount must be refused")
			}
			var held models.P2PTrade
			r.db.First(&held, "id = ?", tr.ID)
			if held.Status != models.P2PTradeStatusPaid {
				t.Fatalf("status after the refused release = %q, want paid", held.Status)
			}

			r.bsc.releasedEvent = &P2PTradeReleasedEvent{TradeID: HashID(tr.ID), AmountToBuyer: toBuyer, FeeToSystem: fee}
			got, err := r.svc.ConfirmRelease(context.Background(), tr.ID, tr.SellerID, "0xrel")
			if err != nil {
				t.Fatalf("ConfirmRelease: %v", err)
			}
			if got.Status != models.P2PTradeStatusReleased {
				t.Fatalf("status = %q, want released", got.Status)
			}
			var ad models.P2PAd
			r.db.First(&ad, "id = ?", tr.AdID)
			// SQLite renders the NUMERIC drain as "0.0"; compare the value.
			if left, ok := new(big.Rat).SetString(ad.Reserved); !ok || left.Sign() != 0 {
				t.Fatalf("ad reserved = %s after release, want 0 (18-dp NUMERIC drain)", ad.Reserved)
			}
		})
	}
}

// recordingFMReader records which FeeManager each read hit.
type recordingFMReader struct {
	*stubFeeManagerReader
	hits map[int64]string
}

func (r *recordingFMReader) RateFor(ctx context.Context, chainID int64, feeManager, feature, token string) (FeeManagerRate, error) {
	r.hits[chainID] = feeManager
	return r.stubFeeManagerReader.RateFor(ctx, chainID, feeManager, feature, token)
}

// The fee drift reconciler reads the BSC direct FeeManager beside Sonic's and
// refreshes BSC's replica from it.
func TestDirectBSC_FeeDriftReconciler_ReadsTheDirectFeeManager(t *testing.T) {
	svc, _ := newSeededChainService(t)
	cfg := &config.Config{FeeManagerChains: map[int64]config.FeeManagerChainConfig{
		sonicC: {ChainID: sonicC, ChainKey: "sonic", Address: "0x00000000000000000000000000000000000FEE00"},
		bscC:   {ChainID: bscC, ChainKey: "bsc", Address: directBSCFeeManager},
	}}
	reader := &recordingFMReader{stubFeeManagerReader: newStubReader(), hits: map[int64]string{}}
	reader.set(bscC, models.FeatureP2P, models.FeeTokenAny, FeeManagerRate{BPS: 77, Active: true})

	res, err := svc.ReconcileChains(context.Background(), cfg, reader)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.ChainsChecked != 2 {
		t.Fatalf("chains checked = %d, want 2 (Sonic + BSC direct)", res.ChainsChecked)
	}
	if reader.hits[bscC] != directBSCFeeManager {
		t.Fatalf("BSC read hit %q, want the direct FeeManager %s", reader.hits[bscC], directBSCFeeManager)
	}
	if bps, _, _, _ := svc.RateFor(models.FeatureP2P, "USDT", i64p(bscC)); bps != 77 {
		t.Fatalf("BSC p2p replica = %d bps after reconcile, want 77 (chain wins)", bps)
	}
}
