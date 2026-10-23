package services

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// conversion_creates_buy_test.go — Fix round 4.
//
// Every orchestrator test elsewhere in this package (conversion_orchestrator_test.go,
// conversion_refund_test.go) hand-seeds a *models.PaycrestOrderRecord directly into
// the database with `TokenAmount` written the way the orchestrator's PARSER
// expected — a plain base-units integer string ("100000000"). That is not what
// CreateBuy, the only real writer of a converted order, has ever written there:
// TokenAmount is Paycrest's own order.Amount, a human-readable decimal string
// ("100"), exactly as a direct order has always stored it and the app renders
// it. The rig and production disagreed about the format of a field, and only a
// test that spans BOTH the real writer and the real reader can catch that.
//
// This file drives a converted buy through CreateBuy (the real writer), then
// hands the resulting row — untouched — to a real ConversionOrchestrator, and
// proves every site that reads TokenAmount interprets it correctly: the
// short-delivery check actually fires, and startBridge does not error out on
// every tick forever the way it did in production before this fix.
// TestConvertedBuy_EndToEnd_OrchestratorReadsWhatCreateBuyWrote is THE
// regression test for this bug class. It never hand-seeds order.TokenAmount:
// the row comes entirely from a real PaycrestOrderService.CreateBuy call, the
// same path a live buy request runs. A verifier reporting a SHORT delivery
// (99 of the 100 USDC actually promised) exercises the exact scenario the
// fork rehearsal proved broken: takeCustody's short-delivery comparison
// against a human-decimal TokenAmount, and startBridge's own parse of the
// same field one tick later.
func TestConvertedBuy_EndToEnd_OrchestratorReadsWhatCreateBuyWrote(t *testing.T) {
	ordersRig := newOrderServiceWithFakes(t) // base/USDC -> optimism/USDT, via CreateBuy's real path
	ordersRig.svc.SetConversionEnabled(true)
	ordersRig.svc.SetTreasury("0xTreasury")
	buyer := seedVerifiedUser(t, ordersRig.db)

	req := ordersRig.buyRequest(buyer, "base", "USDC", "optimism", "USDT")
	// A real address: the orchestrator decodes the route's receiver and
	// compares it to this wallet before signing, so a placeholder can never
	// be paid.
	req.WalletAddress = sponsorTestUser
	row, err := ordersRig.svc.CreateBuy(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateBuy: %v", err)
	}
	// Pin the defect itself: CreateBuy writes the plain decimal string
	// Paycrest returned, never base units. If this assertion ever starts
	// failing because CreateBuy changed, every assertion below is checking
	// the wrong thing.
	if row.TokenAmount != "100" {
		t.Fatalf("TokenAmount = %q, want the plain decimal CreateBuy actually writes (\"100\"), not base units", row.TokenAmount)
	}
	if row.ChainKey != "base" || row.Token != "USDC" || row.RequestedChain != "optimism" || row.RequestedToken != "USDT" {
		t.Fatalf("row legs = %+v", row)
	}

	// Same database CreateBuy just wrote into, additionally migrated for the
	// orchestrator's own ledger — this stays the row CreateBuy inserted, not
	// a re-seeded copy of it.
	if err := ordersRig.db.AutoMigrate(&models.RampLeg{}, &models.SellerPayoutOrder{}); err != nil {
		t.Fatalf("migrate ramp_legs: %v", err)
	}

	// Simulate what a real webhook/poller does once Paycrest has delivered:
	// only TxHash and Status change. TokenAmount's own representation is
	// untouched — it is still exactly what CreateBuy wrote.
	if err := ordersRig.db.Model(row).Updates(map[string]any{
		"tx_hash": "0xpaycrestdelivery", "status": models.PaycrestStatusConverting,
	}).Error; err != nil {
		t.Fatalf("mark delivered: %v", err)
	}

	src, _ := newSponsorRig("base", "USDC")     // 6dp, matches CreateBuy's settlement leg
	dst, _ := newSponsorRig("optimism", "USDT") // 6dp, matches CreateBuy's requested leg
	clock := sponsorTestNow
	treasury := NewTreasury(map[string]*TreasuryLane{
		"base":     {SponsorLane: src.lane, ChainID: 8453, Routers: treasuryTestRouters},
		"optimism": {SponsorLane: dst.lane, ChainID: 10, Routers: treasuryTestRouters},
	}, TreasuryCaps{MaxInFlightUSDPerOrder: 2000, MaxInFlightUSDPerChain: 10000}, func() time.Time { return clock })

	// The verifier reports a SHORT delivery — 99 of the 100 USDC promised —
	// on the same transaction hash the poller just recorded.
	transfers := &fakeTransferVerifier{amount: big.NewInt(99_000000)}
	lifiQuoter := &fakeLiFiQuoter{route: &LiFiRoute{
		To: treasuryTestRouter, Data: testBridgeCalldata(t, row.WalletAddress, 10, false),
		ToAmount: big.NewInt(98_800000), ToAmountMin: big.NewInt(98_700000),
		// Priced in USD, exactly as the quote side's own fixture is: the
		// bridge amount is sized from LiFi's cost, so an unpriced route is
		// one the orchestrator refuses rather than one it sends blind.
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across",
	}}
	// The SAME registry CreateBuy resolved the destination against, so the
	// orchestrator recognises optimism USDT as the configured stablecoin the
	// order was written for — a registry with no token addresses would make
	// the identical row read as a volatile destination here and nowhere else.
	chains := testChainRegistry()
	legStore := &GormRampLegStore{DB: ordersRig.db}
	orch := NewConversionOrchestrator(ordersRig.db, treasury, legStore,
		lifiQuoter, NewLiFiStatusClient("http://unused.invalid", ""),
		map[string]ERC20TransferVerifier{"base": transfers}, chains,
		30*time.Minute, 0, 0, 0, func() time.Time { return clock })

	// ── Step 1: takeCustody ──────────────────────────────────────────────
	//
	// Before this fix, order.TokenAmount ("100") failed new(big.Int).SetString
	// entirely, so the short-delivery branch silently never ran and the
	// order proceeded as though the full 100 USDC had landed.
	if err := orch.Advance(context.Background(), row); err != nil {
		t.Fatalf("Advance (custody): %v", err)
	}
	var afterCustody models.PaycrestOrderRecord
	if err := ordersRig.db.First(&afterCustody, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload after custody: %v", err)
	}
	if afterCustody.Holder != models.HolderTreasury {
		t.Fatalf("holder = %s, want treasury", afterCustody.Holder)
	}
	if afterCustody.TokenAmount != "99" {
		t.Fatalf("tokenAmount = %q, want the short delivery detected and recorded as \"99\" "+
			"(the SAME human-decimal convention it started in — not \"99000000\", and not left at \"100\")",
			afterCustody.TokenAmount)
	}
	if afterCustody.PromisedAmount == "" {
		t.Fatal("promisedAmount was never set")
	}

	// ── Step 2: startBridge ──────────────────────────────────────────────
	//
	// Before this fix, EVERY converted buy died here on every tick, forever:
	// "conversion: unreadable order amount \"100\"" (or whatever the row's
	// real decimal happened to be) — no leg, no park, no operator queue,
	// just a repeating log line. This is the core of the production defect.
	if err := orch.Advance(context.Background(), &afterCustody); err != nil {
		t.Fatalf("Advance (bridge): %v", err)
	}
	legs, err := legStore.ForOrder(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("ForOrder: %v", err)
	}
	if len(legs) != 1 {
		t.Fatalf("legs = %+v, want exactly one bridge leg reserved", legs)
	}
	if legs[0].Kind != models.RampLegKindBridge {
		t.Fatalf("leg kind = %s, want bridge", legs[0].Kind)
	}
	// 99 USDC landed (99000000 base units at 6dp, read from the human-decimal
	// TokenAmount). What she is owed — T = 100 × (1 − B′ 30 bps) = 99.7 — was
	// scaled to that same 99/100 at custody (98.703), and the bridge sends
	// T/(1 − l) of it — 98900801 at LiFi's 20 bps — so the rest stays behind as
	// USDC. The figure that matters to this regression is that it is derived
	// from 99, not 100: a leg sized off the full order amount would need
	// 99899799, more than the 99000000 that landed, and could not be funded.
	if legs[0].Amount != "98900801" {
		t.Fatalf("leg amount = %q, want 98900801 — T/(1 − l) of the SHORT-delivered 99 USDC, "+
			"correctly converted from the human-decimal TokenAmount", legs[0].Amount)
	}
	// Two: the probe that prices l, then the route actually executed.
	if lifiQuoter.quoteCalls() != 2 {
		t.Fatalf("lifi quotes = %d, want 2 — startBridge must actually reach the quote step, not error out first", lifiQuoter.quoteCalls())
	}
	if legs[0].TxHash == "" {
		t.Fatal("leg never broadcast — startBridge must complete, not just reserve")
	}
}
