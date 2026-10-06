package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// newConvertedDeliverRig is newDeliverRig for a route-1 converted buy: the
// company wallet knows the conversion treasury (derived from its key at boot),
// and the converted wiring is set only for the open.
func newConvertedDeliverRig(t *testing.T) *deliverRig {
	t.Helper()
	ctx := context.Background()
	s, userID := newRoutedBuyService(t)
	w0, native, signer := newTestCompanyWallet(t, 50_000_000)
	native.transferNonce = 7
	r := &deliverRig{s: s, native: native, signer: signer,
		verifier: &recordingSaleVerifier{err: errors.New("verify: not found")},
		probe:    &fakeTxProbe{head: 100, receiptBlock: 90, background: []types.Log{{BlockNumber: 100}}}}
	s.SetCompanyWallet(NewCompanyWallet(CompanyWalletConfig{
		Treasury: w0.Treasury(), Decimals: map[string]map[string]int{"base": {"USDC": 6}},
		Verifiers:          map[string]ERC20SourcedTransferVerifier{"base": r.verifier},
		Probes:             map[string]TreasuryTxProbe{"base": r.probe},
		ConversionTreasury: testConversionTreasury,
	}))
	s.SetTreasuryMarketRate(&fixedTreasuryRate{rate: treasuryMarket})
	if err := s.db.AutoMigrate(&models.RampLeg{}, &models.ITIntentAuditLog{}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(models.RampLegsOneLiveSaleIndexSQL).Error; err != nil {
		t.Fatalf("index: %v", err)
	}
	wireConverted(s)
	intent, _, _, err := s.OpenTreasuryConvertedBuy(ctx, userID, treasuryTestQuote(t, s), "base",
		ConvertedOpen{Venue: convertedVenueForTest(), WalletAddress: testRoutedEVM})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.settleStableBuyFromCharge(ctx, userID, "chg_conv_deliver", "1000000"); err != nil {
		t.Fatalf("pay: %v", err)
	}
	r.intent = intent
	return r
}

// Review Focus 1 / N1: the record, not the wiring, decides the handoff.
func TestConvertedBuy_HandoffIsDecidedByTheRecordNotTheWiring(t *testing.T) {
	r := newConvertedDeliverRig(t)
	ctx := context.Background()
	r.s.SetConvertedBuys(ConvertedBuys{}) // switch off, conversion dormant: nothing wired
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, _, to, amt := r.native.transferCall(); !strings.EqualFold(to, testConversionTreasury) || amt.Int64() != 7_232_937 {
		t.Fatalf("sent %v to %s, want the leg to the conversion treasury", amt, to)
	}
	r.verifier.credited, r.verifier.err = big.NewInt(7_232_937), nil
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	got, rec := reloadIntent(t, r.s, r.intent.ID), conversionRecord(t, r.s, r.intent.ID)
	if got.Status != models.ITStatusSettled || rec.Status != models.PaycrestStatusConverting || rec.TxHash != "0xsponsored" ||
		rec.TokenAmount != "7.232937" || rec.QuotedAmount != "7196772" || rec.Holder != models.HolderCompanyWallet {
		t.Fatalf("intent=%s record=%+v", got.Status, rec)
	}
}

// Review Focus 2 / N1: no record, no sale to the conversion treasury.
func TestSendTreasuryBuy_RefusesASaleToTheConversionTreasuryWithoutARecord(t *testing.T) {
	r := newConvertedDeliverRig(t)
	if err := r.s.db.Where("reference = ?", ITConversionRef(r.intent.ID)).Delete(&models.PaycrestOrderRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	if err := r.s.SendTreasuryBuy(context.Background(), r.intent.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v", err)
	}
	if r.signer.calls() != 0 || len(r.legs(t)) != 0 {
		t.Fatalf("broadcasts=%d legs=%d, want nothing", r.signer.calls(), len(r.legs(t)))
	}
	if !strings.Contains(logs.String(), "ALERT ramp_treasury_send_to_conversion_without_record") {
		t.Fatalf("log = %q", logs.String())
	}
}

// Review Focus 3: a refused handoff rolls the settle back with it.
func TestConvertedBuy_HandoffRollsBackWithTheSettle(t *testing.T) {
	r := newConvertedDeliverRig(t)
	ctx := context.Background()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	r.s.db.Model(&models.PaycrestOrderRecord{}).Where("reference = ?", ITConversionRef(r.intent.ID)).
		Update("status", models.PaycrestStatusRecoveryPending) // an operator took it
	r.verifier.credited, r.verifier.err = big.NewInt(7_232_937), nil
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("finalize: %v", err)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("intent = %s, want delivering", got.Status)
	}
	if legs := r.legs(t); legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("leg = %s, want still pending", legs[0].Status)
	}
}

// Review Focus 4 / N4: more than was opened parks, and is never waived.
func TestConvertedBuy_OverpaymentParksAndIsNeverWaived(t *testing.T) {
	ctx := context.Background()
	s, userID := newConvertedTreasuryService(t, 50_000_000)
	intent, _ := openConvertedTreasury(t, s, userID)
	// VA: ₦12,000 against a ₦10,000 order binds and is priced live — more USDC than was opened.
	if _, err := s.settleStableBuyFromCharge(ctx, userID, "chg_conv_over", "1200000"); err != nil {
		t.Fatalf("pay: %v", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusBuyAwaitingRate || !strings.Contains(got.FailureReason, "opened") {
		t.Fatalf("intent = %s %q, want parked as overpaid", got.Status, got.FailureReason)
	}
	if _, err := s.DeliverParkedTreasuryBuy(ctx, uuid.New(), intent.ID, "deliver it"); !errors.Is(err, ErrConvertedNoWaiver) {
		t.Fatalf("deliver anyway: %v", err)
	}
	if _, err := s.RepriceParkedStableBuy(ctx, intent.ID); !errors.Is(err, ErrConvertedOverpaid) {
		t.Fatalf("re-price: %v", err)
	}
	orders, err := s.ListCompanyWalletOrders(ctx)
	if err != nil || len(orders) != 1 || len(orders[0].Actions) != 1 || orders[0].Actions[0] != "refund" || orders[0].ConvertsTo == "" {
		t.Fatalf("orders = %+v err = %v", orders, err)
	}
	if _, err := s.MarkStableBuyRefunded(ctx, uuid.New(), intent.ID, "naira returned"); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if rec := conversionRecord(t, s, intent.ID); rec.Status != models.PaycrestStatusCancelled {
		t.Fatalf("record = %s, want cancelled with the refund", rec.Status)
	}
}

// Review Focus 8 / N3: the processing bound applies to a converted buy.
func TestConvertedBuy_HonourWindowHasTheProcessingBound(t *testing.T) {
	cheaper := &fixedTreasuryRate{rate: big.NewRat(1300, 1)} // live would now give MORE USDC
	for _, tc := range []struct {
		name     string
		ago      time.Duration
		honoured bool
	}{{"inside the grace", 2 * time.Minute, true}, {"after the grace", 11 * time.Minute, false}} {
		s, userID := newConvertedTreasuryService(t, 50_000_000)
		intent, _ := openConvertedTreasury(t, s, userID)
		expiry := time.Now().Add(-tc.ago)
		s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("rate_expires_at", expiry)
		s.SetTreasuryMarketRate(cheaper)
		if err := NewITStableClaimSource(s).Accept(context.Background(), intent.ID,
			centralCredit("1000042610031200000000000000"+tc.name[:2], 1_000_000, expiry.Add(-time.Minute))); err != nil {
			t.Fatalf("%s: accept: %v", tc.name, err)
		}
		got := reloadIntent(t, s, intent.ID)
		if tc.honoured && (got.Status != models.ITStatusPaymentReceived || got.AssetAmount != "7232937" || got.Repriced) {
			t.Fatalf("%s: %s %s repriced=%v, want honoured", tc.name, got.Status, got.AssetAmount, got.Repriced)
		}
		if !tc.honoured && got.Status != models.ITStatusBuyAwaitingRate {
			t.Fatalf("%s: %s, want priced live — larger than opened, so parked", tc.name, got.Status)
		}
	}
}

// N1: a record that is no longer waiting for its leg refuses the sale too.
func TestSendTreasuryBuy_RefusesWhenItsConversionRecordIsClosed(t *testing.T) {
	r := newConvertedDeliverRig(t)
	r.s.db.Model(&models.PaycrestOrderRecord{}).Where("reference = ?", ITConversionRef(r.intent.ID)).
		Update("status", models.PaycrestStatusRecoveryPending)
	logs := captureLog(t)
	if err := r.s.SendTreasuryBuy(context.Background(), r.intent.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v", err)
	}
	if r.signer.calls() != 0 || len(r.legs(t)) != 0 {
		t.Fatalf("broadcasts=%d legs=%d, want nothing", r.signer.calls(), len(r.legs(t)))
	}
	if !strings.Contains(logs.String(), "ALERT ramp_treasury_send_to_conversion_without_record") {
		t.Fatalf("log = %q", logs.String())
	}
}

// Defence in depth for N1: a direct company-wallet buy can never name the
// conversion treasury as her address — it would open a sale with no record.
func TestOpenTreasuryBuy_RefusesTheConversionTreasuryAsHerAddress(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	w0, _, _ := newTestCompanyWallet(t, 50_000_000)
	s.SetCompanyWallet(NewCompanyWallet(CompanyWalletConfig{
		Treasury: w0.Treasury(), Decimals: map[string]map[string]int{"base": {"USDC": 6}},
		ConversionTreasury: testConversionTreasury,
	}))
	logs := captureLog(t)
	for _, addr := range []string{testConversionTreasury, " " + testConversionTreasury + " "} {
		if _, _, err := s.OpenTreasuryBuy(context.Background(), userID, treasuryTestQuote(t, s), "base", addr); !errors.Is(err, ErrBadDeliveryAddress) {
			t.Fatalf("%q: err = %v", addr, err)
		}
	}
	if n := intentCount(s); n != 0 {
		t.Fatalf("intents = %d, want none", n)
	}
	if !strings.Contains(logs.String(), "ALERT ramp_treasury_direct_buy_to_conversion_treasury") {
		t.Fatalf("log = %q", logs.String())
	}
}

// M7/N1 on route 1: a sale proven above the record's opened amount settles —
// the handoff caps the bridge at the opened amount (ALERT: refund the excess
// naira) instead of rolling the settle back every tick.
func TestConvertedBuy_Route1DeliveryAboveTheOpenedAmountIsCappedNotStuck(t *testing.T) {
	r := newConvertedDeliverRig(t)
	ctx := context.Background()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	// A pricing path that forgot the ceiling: the record was opened for 7 USDC.
	r.s.db.Model(&models.PaycrestOrderRecord{}).Where("reference = ?", ITConversionRef(r.intent.ID)).
		Update("token_amount", "7")
	r.verifier.credited, r.verifier.err = big.NewInt(7_232_937), nil
	logs := captureLog(t)
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	got, rec := reloadIntent(t, r.s, r.intent.ID), conversionRecord(t, r.s, r.intent.ID)
	if got.Status != models.ITStatusSettled || rec.Status != models.PaycrestStatusConverting || rec.TokenAmount != "7" ||
		rec.QuotedAmount != ConversionOwed(big.NewInt(7_000_000), rec.MarkupBps).String() {
		t.Fatalf("intent=%s record=%+v", got.Status, rec)
	}
	if legs := r.legs(t); legs[0].Status == models.RampLegStatusPending {
		t.Fatalf("leg still pending")
	}
	if !strings.Contains(logs.String(), "ALERT ramp_converted_handoff_capped") || !strings.Contains(logs.String(), "excess=0.232937") {
		t.Fatalf("log = %q", logs.String())
	}
}
