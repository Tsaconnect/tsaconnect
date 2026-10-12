package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// poolDeskOver is a second saleDesk over another wallet, owning tsa_pool rows
// — what Task 7 builds from the conversion treasury.
func poolDeskOver(w *CompanyWallet) *saleDesk {
	return &saleDesk{wallet: w, label: "TSA pool", tag: "[liquidity]", scope: scopePoolBuys,
		owns:       func(i *models.ITIntent) bool { t, err := providerTerms(i); return err == nil && t.pool },
		checkOrder: func(*models.ITIntent, *big.Int) error { return nil }}
}

func seedDeliveringPoolIntent(t *testing.T, db *gorm.DB, units string) *models.ITIntent {
	t.Helper()
	i := &models.ITIntent{ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionBuy, Asset: "USDC",
		Currency: "NGN", Provider: models.ITProviderPool, Status: models.ITStatusDelivering, DestChain: "base",
		DestAddress: testRoutedEVM, AssetAmount: units, AmountMinor: "1000000", ReceivedAmount: "1000000"}
	if err := db.Create(i).Error; err != nil {
		t.Fatal(err)
	}
	return i
}

// S20: the same send path, from the desk's own wallet, for the desk's own
// provider — the leg's from address is that wallet's, and the nonce is
// written on the leg before the broadcast, exactly as for the company wallet.
func TestSaleDesk_PoolDeskSendsFromItsOwnWallet(t *testing.T) {
	r := newDeliverRig(t)
	w2, native2, _ := newTestCompanyWallet(t, 50_000_000)
	native2.transferNonce = 21
	pool := NewCompanyWallet(CompanyWalletConfig{Treasury: w2.Treasury(), Decimals: map[string]map[string]int{"base": {"USDC": 6}},
		Verifiers: map[string]ERC20SourcedTransferVerifier{"base": r.verifier}, Probes: map[string]TreasuryTxProbe{"base": r.probe}})
	sd := poolDeskOver(pool)
	intent := seedDeliveringPoolIntent(t, r.s.db, "7278150")
	if err := r.s.sendSale(context.Background(), sd, intent.ID); err != nil {
		t.Fatal(err)
	}
	var leg models.RampLeg
	r.s.db.First(&leg, "order_id = ?", intent.ID)
	if !SameEVMAddress(leg.FromAddr, pool.Address()) || leg.Nonce == nil || *leg.Nonce != 21 {
		t.Fatalf("leg from=%s nonce=%v, want the pool wallet's address and its own nonce 21", leg.FromAddr, leg.Nonce)
	}
}

// Each desk acts only on its own provider's orders.
func TestSaleDesk_ProviderMismatchRefused(t *testing.T) {
	r := newDeliverRig(t)
	intent := seedDeliveringPoolIntent(t, r.s.db, "7278150")
	if err := r.s.SendTreasuryBuy(context.Background(), intent.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("company wallet on a pool order = %v, want ErrITBadState", err)
	}
	if err := r.s.sendSale(context.Background(), poolDeskOver(r.s.wallet), r.intent.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("pool desk on a company-wallet order = %v, want ErrITBadState", err)
	}
}

// onSettle runs inside the settle transaction: a failing hook rolls the
// settle back (the books and the settle are one write, P9).
func TestSaleDesk_OnSettleIsInTheSettleTransaction(t *testing.T) {
	r := newDeliverRig(t)
	intent := seedDeliveringPoolIntent(t, r.s.db, "7278150")
	sd := poolDeskOver(r.s.wallet)
	sd.onSettle = func(*gorm.DB, *models.ITIntent, *models.RampLeg) error { return errors.New("books refused") }
	leg := &models.RampLeg{ID: uuid.New(), OrderID: intent.ID, Kind: models.RampLegKindSale, Attempt: 1, ChainKey: "base",
		Token: "USDC", FromAddr: r.s.wallet.Address(), ToAddr: testRoutedEVM, Amount: "7278150",
		Status: models.RampLegStatusPending, TxHash: "0xsale", CreatedAt: time.Now()}
	r.s.db.Create(leg)
	if err := r.s.settleSale(context.Background(), sd, intent, leg, "7278150"); err == nil {
		t.Fatal("want the hook's error")
	}
	var got models.ITIntent
	r.s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want delivering (rolled back with the hook)", got.Status)
	}
}

// receiptDeep (Task 1) is the sale settle's rule, shared with the release proof.
func TestReceiptDeep_NeedsDepthStatusAndOneEndpoint(t *testing.T) {
	p := &fakeTxProbe{head: 100, receiptBlock: 96}
	if ok, _ := receiptDeep(context.Background(), p, "0xsale", 1, 5, p.EndpointGeneration()); !ok {
		t.Fatal("96..100 is 5 deep")
	}
	if ok, _ := receiptDeep(context.Background(), p, "0xsale", 1, 6, p.EndpointGeneration()); ok {
		t.Fatal("5 deep is not 6")
	}
	if ok, _ := receiptDeep(context.Background(), p, "0xsale", 0, 5, p.EndpointGeneration()); ok {
		t.Fatal("a success receipt never proves a revert")
	}
}
