package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// fwConvertedVenue is PriceConvertedLeg's answer for Flutterwave's worked
// example (₦165,025 → D = 95.137885 USDC on Polygon) at 50 bps: T = 94.662195.
func fwConvertedVenue() PaycrestVenue {
	v := convertedVenueForTest()
	v.ChainKey, v.Network, v.ViaChain, v.Rate = "polygon", "polygon", "polygon", "1658.50"
	v.TokenAmountBaseUnits = ConversionOwed(big.NewInt(95_137_885), 50).String()
	return v
}

type stubReceiptVerifier struct {
	credited      *big.Int
	err           error
	hash, tok, to string
}

func (v *stubReceiptVerifier) VerifyTransfer(_ context.Context, hash, token, to string) (*big.Int, error) {
	v.hash, v.tok, v.to = hash, token, to
	return v.credited, v.err
}

func openFWConverted(t *testing.T) (*ITService, uuid.UUID, models.ITIntent) {
	t.Helper()
	s, userID := newRoutedBuyService(t)
	if err := s.db.AutoMigrate(&models.ITIntentAuditLog{}); err != nil {
		t.Fatal(err)
	}
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury,
		Treasury: &fakeConvertedTreasury{laned: map[string]bool{"polygon": true}}})
	intent, _, _, err := s.OpenFlutterwaveConvertedBuy(context.Background(), userID, routedTestQuote(t, s),
		ConvertedOpen{Venue: fwConvertedVenue(), WalletAddress: testRoutedEVM})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, userID, reloadIntent(t, s, intent.ID)
}

func deliveringFW(t *testing.T, tr FWTransfer, sentAgo time.Duration) (*ITService, models.ITIntent) {
	t.Helper()
	s, userID, intent := openFWConverted(t)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_fwc", intent.AmountMinor); err != nil {
		t.Fatalf("pay: %v", err)
	}
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Updates(map[string]any{
		"status": models.ITStatusDelivering, "delivery_id": "trf_fwc"})
	s.db.Exec("UPDATE it_intents SET updated_at = ? WHERE id = ?", time.Now().Add(-sentAgo), intent.ID)
	s.SetStableTransfers(&stubTransfers{tr: tr})
	return s, intent
}

func TestOpenFlutterwaveConvertedBuy_DeliversToTheTreasuryOnPolygonWithTheAgreement(t *testing.T) {
	s, _, intent := openFWConverted(t)
	if intent.DestChain != "POLYGON" || !strings.EqualFold(intent.DestAddress, testConversionTreasury) || intent.Provider != models.ITProviderFlutterwave {
		t.Fatalf("intent = %+v", intent)
	}
	if rec := conversionRecord(t, s, intent.ID); rec.Holder != models.HolderFlutterwave || rec.ChainKey != "polygon" {
		t.Fatalf("record = %+v", rec)
	}
}

// Without the IT agreement, and without a conversion lane on Polygon, nothing
// is written.
func TestOpenFlutterwaveConvertedBuy_RefusesBeforeTheWrite(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	if err := s.db.AutoMigrate(&models.WalletAddress{}, &models.ITAgreementAcceptance{}); err != nil {
		t.Fatal(err)
	}
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury,
		Treasury: &fakeConvertedTreasury{laned: map[string]bool{"polygon": true}}})
	in := ConvertedOpen{Venue: fwConvertedVenue(), WalletAddress: testRoutedEVM}
	if _, _, _, err := s.OpenFlutterwaveConvertedBuy(context.Background(), userID, routedTestQuote(t, s), in); !errors.Is(err, ErrITAgreementRequired) {
		t.Fatalf("no agreement: %v", err)
	}
	acceptITAgreement(t, s, userID)
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury,
		Treasury: &fakeConvertedTreasury{laned: map[string]bool{"base": true}}})
	if _, _, _, err := s.OpenFlutterwaveConvertedBuy(context.Background(), userID, routedTestQuote(t, s), in); !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("no polygon lane: %v", err)
	}
	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	var r int64
	s.db.Model(&models.PaycrestOrderRecord{}).Count(&r)
	if n != 0 || r != 0 {
		t.Fatalf("written: intents=%d records=%d", n, r)
	}
}

func TestFinalizeBuyDelivery_ConvertedHandsOffWithFlutterwavesHash(t *testing.T) {
	hash := "0x" + strings.Repeat("ab", 32)
	s, intent := deliveringFW(t, FWTransfer{ID: "trf_fwc", Status: "SUCCESSFUL", TxHash: hash}, time.Minute)
	s.SetConvertedBuys(ConvertedBuys{}) // N1: wiring is irrelevant
	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if rec := conversionRecord(t, s, intent.ID); rec.Status != models.PaycrestStatusConverting || rec.TxHash != hash {
		t.Fatalf("record = %+v", rec)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("intent = %s", got.Status)
	}
}

// Review Focus 9: no hash — wait, then park loudly.
func TestFinalizeBuyDelivery_ConvertedWaitsForAHashThenParks(t *testing.T) {
	s, intent := deliveringFW(t, FWTransfer{ID: "trf_fwc", Status: "SUCCESSFUL"}, time.Minute)
	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); err != nil {
		t.Fatalf("finalize (waiting): %v", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("intent = %s, want still delivering inside the wait", got.Status)
	}
	s2, i2 := deliveringFW(t, FWTransfer{ID: "trf_fwc", Status: "SUCCESSFUL"}, 11*time.Minute)
	logs := captureLog(t)
	if err := s2.FinalizeBuyDelivery(context.Background(), i2.ID); err != nil {
		t.Fatalf("finalize (past the wait): %v", err)
	}
	rec := conversionRecord(t, s2, i2.ID)
	if rec.Status != models.PaycrestStatusRecoveryPending || rec.Fault != models.FaultPlatform || rec.Holder != models.HolderFlutterwave {
		t.Fatalf("record = %+v", rec)
	}
	if got := reloadIntent(t, s2, i2.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("intent = %s", got.Status)
	}
	if !strings.Contains(logs.String(), "ALERT it_stable_buy_conversion_no_hash") {
		t.Fatalf("log = %q", logs.String())
	}
}

// RAMP_FLUTTERWAVE_HASH_WAIT_MINUTES sets the wait; a value ≤ 0 keeps it.
func TestFinalizeBuyDelivery_ConvertedHashWaitIsConfigurable(t *testing.T) {
	s, intent := deliveringFW(t, FWTransfer{ID: "trf_fwc", Status: "SUCCESSFUL"}, 3*time.Minute)
	if err := s.SetRampBuyTerms(RampBuyTerms{FlutterwaveHashWait: 2 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRampBuyTerms(RampBuyTerms{}); err != nil { // 0 keeps 2 minutes
		t.Fatal(err)
	}
	if err := s.SetRampBuyTerms(RampBuyTerms{FlutterwaveHashWait: -time.Minute}); err == nil {
		t.Fatal("a negative wait was accepted")
	}
	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if rec := conversionRecord(t, s, intent.ID); rec.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("record = %s, want parked after a 2-minute wait", rec.Status)
	}
}

// A record that can no longer take the delivery (closed by hand) is never
// settled past: the intent stays delivering and the tick reports it.
func TestFinalizeBuyDelivery_ConvertedNeverSettlesPastAClosedRecord(t *testing.T) {
	for name, tr := range map[string]FWTransfer{
		"with a hash": {ID: "trf_fwc", Status: "SUCCESSFUL", TxHash: "0x" + strings.Repeat("ab", 32)},
		"no hash":     {ID: "trf_fwc", Status: "SUCCESSFUL"},
	} {
		s, intent := deliveringFW(t, tr, 11*time.Minute)
		s.db.Model(&models.PaycrestOrderRecord{}).Where("reference = ?", ITConversionRef(intent.ID)).
			Update("status", models.PaycrestStatusCancelled)
		if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); !errors.Is(err, ErrITBadState) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusDelivering {
			t.Fatalf("%s: intent = %s", name, got.Status)
		}
	}
}

// A direct routed buy is untouched by the converted branch.
func TestFinalizeBuyDelivery_DirectRoutedBuyStillSettles(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent, _, err := s.OpenRoutedStableBuy(context.Background(), userID, routedTestQuote(t, s), "POLYGON")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Updates(map[string]any{
		"status": models.ITStatusDelivering, "delivery_id": "trf_direct"})
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_direct", Status: "SUCCESSFUL"}})
	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("intent = %s", got.Status)
	}
}

// N4 on route 2: paying more than the converted buy was opened for parks.
func TestFlutterwaveConvertedBuy_OverpaymentParks(t *testing.T) {
	s, userID, intent := openFWConverted(t)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_fwc_over", "20000000"); err != nil {
		t.Fatalf("pay: %v", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusBuyAwaitingRate || !strings.Contains(got.FailureReason, "opened") {
		t.Fatalf("intent = %s %q, want parked as overpaid", got.Status, got.FailureReason)
	}
}

func parkedHashlessFW(t *testing.T) (*ITService, models.ITIntent) {
	t.Helper()
	s, intent := deliveringFW(t, FWTransfer{ID: "trf_fwc", Status: "SUCCESSFUL"}, 11*time.Minute)
	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); err != nil {
		t.Fatal(err)
	}
	return s, intent
}

// Review Focus 9: a pasted hash is attached only on the chain's word.
func TestAttachConversionHash_PreChecksTheChain(t *testing.T) {
	s, intent := parkedHashlessFW(t)
	hash := "0x" + strings.Repeat("cd", 32)
	verifier := &stubReceiptVerifier{err: ErrTransferWrongRecipient}
	probe := &fakeTxProbe{head: 100, receiptBlock: 90}
	lane := &fakeConvertedTreasury{laned: map[string]bool{"polygon": true}}
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury, Treasury: lane,
		Verifiers: map[string]ERC20TransferVerifier{"polygon": verifier}, Probes: map[string]TreasuryTxProbe{"polygon": probe}})
	admin := uuid.New()
	if _, err := s.AttachConversionHash(context.Background(), admin, intent.ID, hash, ""); !errors.Is(err, ErrITOperatorNoteRequired) {
		t.Fatalf("no note: %v", err)
	}
	if _, err := s.AttachConversionHash(context.Background(), admin, intent.ID, "0x1234", "from FW support"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("not a hash: %v", err)
	}
	if _, err := s.AttachConversionHash(context.Background(), admin, intent.ID, hash, "from FW support"); err == nil {
		t.Fatal("a hash paying someone else was attached")
	}
	if verifier.tok != "0xLaneUSDC" || verifier.to != testConversionTreasury || verifier.hash != hash {
		t.Fatalf("verified %q %q %q, want the lane's USDC to the treasury", verifier.hash, verifier.tok, verifier.to)
	}
	verifier.credited, verifier.err = big.NewInt(1), nil
	if _, err := s.AttachConversionHash(context.Background(), admin, intent.ID, hash, "from FW support"); err == nil {
		t.Fatal("a short credit was attached")
	}
	verifier.credited = big.NewInt(95_137_885)
	probe.head = 91 // 2 deep, not 5
	if _, err := s.AttachConversionHash(context.Background(), admin, intent.ID, hash, "from FW support"); err == nil {
		t.Fatal("a shallow receipt was attached")
	}
	probe.head, probe.receiptFailed = 100, true
	if _, err := s.AttachConversionHash(context.Background(), admin, intent.ID, hash, "from FW support"); err == nil {
		t.Fatal("a reverted receipt was attached")
	}
	probe.receiptFailed = false
	if rec := conversionRecord(t, s, intent.ID); rec.Status != models.PaycrestStatusRecoveryPending || rec.TxHash != "" {
		t.Fatalf("a refused attach wrote: %+v", rec)
	}
	rec, err := s.AttachConversionHash(context.Background(), admin, intent.ID, hash, "from FW support")
	if err != nil || rec.Status != models.PaycrestStatusConverting || rec.TxHash != hash || rec.Fault != "" {
		t.Fatalf("rec = %+v err = %v", rec, err)
	}
	var audits []models.ITIntentAuditLog
	s.db.Where("intent_id = ?", intent.ID).Find(&audits)
	if len(audits) != 1 || audits[0].Action != models.ITAuditConversionHashAttached || audits[0].Note != "from FW support" {
		t.Fatalf("audits = %+v", audits)
	}
	// Once attached, the record is no longer a hashless park.
	if _, err := s.AttachConversionHash(context.Background(), admin, intent.ID, hash, "again"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("second attach: %v", err)
	}
}

// A hash another record already holds is never attached twice.
func TestAttachConversionHash_RefusesAHashAnotherRecordHolds(t *testing.T) {
	s, intent := parkedHashlessFW(t)
	hash := "0x" + strings.Repeat("ef", 32)
	// Held in another case: hashes compare case-insensitively.
	if err := s.db.Create(&models.PaycrestOrderRecord{UserID: uuid.New(), Direction: models.PaycrestDirectionBuy,
		Status: models.PaycrestStatusConverting, Reference: "other", TxHash: "0x" + strings.ToUpper(hash[2:])}).Error; err != nil {
		t.Fatal(err)
	}
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury,
		Treasury:  &fakeConvertedTreasury{laned: map[string]bool{"polygon": true}},
		Verifiers: map[string]ERC20TransferVerifier{"polygon": &stubReceiptVerifier{credited: big.NewInt(95_137_885)}},
		Probes:    map[string]TreasuryTxProbe{"polygon": &fakeTxProbe{head: 100, receiptBlock: 90}}})
	if _, err := s.AttachConversionHash(context.Background(), uuid.New(), intent.ID, hash, "from FW support"); !errors.Is(err, ErrITBadState) ||
		!strings.Contains(err.Error(), "another order") {
		t.Fatalf("err = %v", err)
	}
}

// RPC error text never reaches the operator unredacted.
func TestAttachConversionHash_RedactsTheVerifierError(t *testing.T) {
	s, intent := parkedHashlessFW(t)
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury,
		Treasury: &fakeConvertedTreasury{laned: map[string]bool{"polygon": true}},
		Verifiers: map[string]ERC20TransferVerifier{"polygon": &stubReceiptVerifier{
			err: errors.New(`Post "https://polygon-mainnet.g.alchemy.com/v2/SECRETKEY123": dial tcp: timeout`)}},
		Probes: map[string]TreasuryTxProbe{"polygon": &fakeTxProbe{head: 100, receiptBlock: 90}}})
	_, err := s.AttachConversionHash(context.Background(), uuid.New(), intent.ID, "0x"+strings.Repeat("cd", 32), "from FW support")
	if err == nil || strings.Contains(err.Error(), "SECRETKEY123") {
		t.Fatalf("err = %v", err)
	}
}

func TestFWTransfer_OnChainHashReadsEitherShape(t *testing.T) {
	for body, want := range map[string]string{
		`{"id":"t","tx_hash":"0xaa"}`:                   "0xaa",
		`{"id":"t","meta":{"tx_hash":"0xbb"}}`:          "0xbb",
		`{"id":"t","meta":{"transaction_hash":"0xcc"}}`: "0xcc",
		`{"id":"t","meta":[1,2]}`:                       "",
		`{"id":"t"}`:                                    "",
	} {
		var tr FWTransfer
		if err := json.Unmarshal([]byte(body), &tr); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if got := tr.OnChainHash(); got != want {
			t.Fatalf("%s → %q, want %q", body, got, want)
		}
	}
}

// On PostgreSQL (column sizes, LOWER(tx_hash)): the hashless park, a hash
// another record holds in another case refused, then the attach.
func TestFlutterwaveConvertedPark_AttachOnPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "it_fw_converted_", &models.ITIntent{}, &models.PaycrestOrderRecord{}, &models.ITIntentAuditLog{})
	s := NewITService(db, nil, stubFx{ngn: 1500})
	intent := newUSDCOrder()
	intent.Provider, intent.DestChain, intent.DestAddress = models.ITProviderFlutterwave, "POLYGON", testConversionTreasury
	intent.AssetAmount, intent.Status, intent.DeliveryID = "95137885", models.ITStatusDelivering, "trf_fwc"
	if err := db.Create(intent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(newConversionRecord(intent, ConvertedOpen{Venue: fwConvertedVenue(), WalletAddress: testRoutedEVM},
		models.HolderFlutterwave)).Error; err != nil {
		t.Fatal(err)
	}
	db.Exec("UPDATE it_intents SET updated_at = ? WHERE id = ?", time.Now().Add(-11*time.Minute), intent.ID)
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_fwc", Status: "SUCCESSFUL"}})
	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if rec := conversionRecord(t, s, intent.ID); rec.Status != models.PaycrestStatusRecoveryPending || rec.Holder != models.HolderFlutterwave {
		t.Fatalf("record = %+v", rec)
	}
	hash := "0x" + strings.Repeat("cd", 32)
	other := &models.PaycrestOrderRecord{UserID: uuid.New(), Direction: models.PaycrestDirectionBuy,
		Status: models.PaycrestStatusConverting, Reference: "other", TxHash: "0x" + strings.ToUpper(hash[2:])}
	if err := db.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury,
		Treasury:  &fakeConvertedTreasury{laned: map[string]bool{"polygon": true}},
		Verifiers: map[string]ERC20TransferVerifier{"polygon": &stubReceiptVerifier{credited: big.NewInt(95_137_885)}},
		Probes:    map[string]TreasuryTxProbe{"polygon": &fakeTxProbe{head: 100, receiptBlock: 90}}})
	if _, err := s.AttachConversionHash(context.Background(), uuid.New(), intent.ID, hash, "from FW support"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("held elsewhere: %v", err)
	}
	db.Delete(other)
	rec, err := s.AttachConversionHash(context.Background(), uuid.New(), intent.ID, hash, "from FW support")
	if err != nil || rec.Status != models.PaycrestStatusConverting || rec.TxHash != hash || rec.TokenAmount != "95.137885" {
		t.Fatalf("rec = %+v err = %v", rec, err)
	}
}

// I1: the parked record is hers to read (the order screen renders its
// reason), so the reason never names a provider; the provider detail stays
// in the ALERT.
func TestFinalizeBuyDelivery_HashlessParkReasonNamesNoProvider(t *testing.T) {
	s, intent := deliveringFW(t, FWTransfer{ID: "trf_fwc", Status: "SUCCESSFUL"}, 11*time.Minute)
	logs := captureLog(t)
	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	reason := strings.ToLower(conversionRecord(t, s, intent.ID).FailureReason)
	if reason == "" || strings.Contains(reason, "flutterwave") || strings.Contains(reason, "paycrest") {
		t.Fatalf("stored reason %q must be neutral copy", reason)
	}
	if !strings.Contains(logs.String(), "ALERT it_stable_buy_conversion_no_hash") || !strings.Contains(strings.ToLower(logs.String()), "flutterwave") {
		t.Fatalf("the ALERT lost the provider detail: %q", logs.String())
	}
}

// M2: a hash proves THIS delivery only if it was mined after the delivery
// was sent — never an older transfer to the treasury (a top-up).
func TestAttachConversionHash_RefusesATransferMinedBeforeTheDeliverySend(t *testing.T) {
	s, intent := parkedHashlessFW(t) // sent 11 minutes ago
	hash := "0x" + strings.Repeat("cd", 32)
	probe := &fakeTxProbe{head: 100, receiptBlock: 90, blockTime: time.Now().Add(-time.Hour)}
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury,
		Treasury:  &fakeConvertedTreasury{laned: map[string]bool{"polygon": true}},
		Verifiers: map[string]ERC20TransferVerifier{"polygon": &stubReceiptVerifier{credited: big.NewInt(95_137_885)}},
		Probes:    map[string]TreasuryTxProbe{"polygon": probe}})
	if _, err := s.AttachConversionHash(context.Background(), uuid.New(), intent.ID, hash, "from FW support"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("a transfer mined before the send: err = %v", err)
	}
	if rec := conversionRecord(t, s, intent.ID); rec.Status != models.PaycrestStatusRecoveryPending || rec.TxHash != "" {
		t.Fatalf("a refused attach wrote: %+v", rec)
	}
	probe.blockTime = time.Now().Add(-10 * time.Minute) // after the send
	if rec, err := s.AttachConversionHash(context.Background(), uuid.New(), intent.ID, hash, "from FW support"); err != nil ||
		rec.Status != models.PaycrestStatusConverting {
		t.Fatalf("rec = %+v err = %v", rec, err)
	}
}

// The receipt, head and block time must come from one RPC endpoint: a
// failover between them may mix a lagging node's answers, so the attach is
// refused and the operator retries.
func TestAttachConversionHash_RefusedAcrossAnRPCFailover(t *testing.T) {
	s, intent := parkedHashlessFW(t)
	hash := "0x" + strings.Repeat("cd", 32)
	probe := &fakeTxProbe{head: 100, receiptBlock: 90, blockTime: time.Now().Add(-10 * time.Minute), switchOnReceipt: true}
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury,
		Treasury:  &fakeConvertedTreasury{laned: map[string]bool{"polygon": true}},
		Verifiers: map[string]ERC20TransferVerifier{"polygon": &stubReceiptVerifier{credited: big.NewInt(95_137_885)}},
		Probes:    map[string]TreasuryTxProbe{"polygon": probe}})
	if _, err := s.AttachConversionHash(context.Background(), uuid.New(), intent.ID, hash, "from FW support"); !errors.Is(err, ErrITBadState) ||
		!strings.Contains(err.Error(), "RPC endpoint changed") {
		t.Fatalf("attach across a failover: err = %v", err)
	}
	if rec := conversionRecord(t, s, intent.ID); rec.TxHash != "" {
		t.Fatalf("a refused attach wrote: %+v", rec)
	}
	probe.switchOnReceipt = false
	if rec, err := s.AttachConversionHash(context.Background(), uuid.New(), intent.ID, hash, "from FW support"); err != nil ||
		rec.Status != models.PaycrestStatusConverting {
		t.Fatalf("rec = %+v err = %v", rec, err)
	}
}

// M5: a hashless park whose delivery never happened has an operator exit —
// refunded in naira, the record closed in the same transaction. Any other
// settled buy stays refused.
func TestMarkStableBuyRefunded_ClosesAHashlessFlutterwavePark(t *testing.T) {
	ctx := context.Background()
	s, intent := parkedHashlessFW(t)
	got, err := s.MarkStableBuyRefunded(ctx, uuid.New(), intent.ID, "FW confirmed nothing was sent")
	if err != nil || got.Status != models.ITStatusRefunded {
		t.Fatalf("refund: %+v %v", got, err)
	}
	if rec := conversionRecord(t, s, intent.ID); rec.Status != models.PaycrestStatusCancelled || rec.TxHash != "" {
		t.Fatalf("record = %+v", rec)
	}
	// Closed: no hash attaches to it afterwards.
	if _, err := s.AttachConversionHash(ctx, uuid.New(), intent.ID, "0x"+strings.Repeat("cd", 32), "late"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("attach after refund: %v", err)
	}

	// Handed off with a hash: the conversion runs, so no naira refund.
	s2, i2 := deliveringFW(t, FWTransfer{ID: "trf_fwc", Status: "SUCCESSFUL", TxHash: "0x" + strings.Repeat("ab", 32)}, time.Minute)
	if err := s2.FinalizeBuyDelivery(ctx, i2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.MarkStableBuyRefunded(ctx, uuid.New(), i2.ID, "n"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("converting record: %v", err)
	}
	if got := reloadIntent(t, s2, i2.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("intent = %s", got.Status)
	}
	if rec := conversionRecord(t, s2, i2.ID); rec.Status != models.PaycrestStatusConverting {
		t.Fatalf("record = %s", rec.Status)
	}

	// A settled direct Flutterwave buy has no record: refused as before.
	s3, u3 := newRoutedBuyService(t)
	if err := s3.db.AutoMigrate(&models.ITIntentAuditLog{}); err != nil {
		t.Fatal(err)
	}
	direct, _, err := s3.OpenRoutedStableBuy(ctx, u3, routedTestQuote(t, s3), "POLYGON")
	if err != nil {
		t.Fatal(err)
	}
	s3.db.Model(&models.ITIntent{}).Where("id = ?", direct.ID).Update("status", models.ITStatusSettled)
	if _, err := s3.MarkStableBuyRefunded(ctx, uuid.New(), direct.ID, "n"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("settled direct buy: %v", err)
	}
}

// M7/N1 on route 2: the handoff never bridges more than the record was
// opened for — a delivery above it is capped at the opened amount (ALERT:
// refund her the excess naira) and the order still moves, never stuck.
func TestHandoffConversion_CapsADeliveryAboveTheOpenedAmount(t *testing.T) {
	hash := "0x" + strings.Repeat("ab", 32)
	s, intent := deliveringFW(t, FWTransfer{ID: "trf_fwc", Status: "SUCCESSFUL", TxHash: hash}, time.Minute)
	// A pricing path that forgot the ceiling: D one unit above the opened 95.137885.
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("asset_amount", "95137886")
	logs := captureLog(t)
	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	rec := conversionRecord(t, s, intent.ID)
	if rec.Status != models.PaycrestStatusConverting || rec.TxHash != hash || rec.TokenAmount != "95.137885" ||
		rec.QuotedAmount != ConversionOwed(big.NewInt(95_137_885), rec.MarkupBps).String() {
		t.Fatalf("record = %+v, want converting at the opened amount", rec)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("intent = %s, want settled", got.Status)
	}
	l := logs.String()
	if !strings.Contains(l, "ALERT ramp_converted_handoff_capped") || !strings.Contains(l, "excess=0.000001") ||
		!strings.Contains(l, "refund") {
		t.Fatalf("log = %q", l)
	}
}

// An opened amount that cannot be read is still refused: nothing to cap at.
func TestHandoffConversion_RefusesAnUnreadableOpenedAmount(t *testing.T) {
	s, intent := deliveringFW(t, FWTransfer{ID: "trf_fwc", Status: "SUCCESSFUL", TxHash: "0x" + strings.Repeat("ab", 32)}, time.Minute)
	s.db.Model(&models.PaycrestOrderRecord{}).Where("reference = ?", ITConversionRef(intent.ID)).Update("token_amount", "")
	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); !errors.Is(err, ErrConvertedOverpaid) {
		t.Fatalf("err = %v", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("intent = %s", got.Status)
	}
}

type recordingRatePublisher struct{ events []events.Event }

func (p *recordingRatePublisher) Publish(e events.Event) { p.events = append(p.events, e) }

// N2: a converted buy repriced live is told what SHE receives — T of her
// requested token — never the USDC leg D it settles on.
func TestNotifyRateUpdated_ConvertedBuyNamesTheRequestedTokenAndAmount(t *testing.T) {
	s, _, intent := openFWConverted(t)
	pub := &recordingRatePublisher{}
	s.rateNotifier = pub
	d := big.NewInt(90_000_000)
	s.notifyRateUpdated(&intent, d)
	owed := FormatStableUnits(ConversionOwed(d, fwConvertedVenue().MarkupBps).String())
	if len(pub.events) != 1 {
		t.Fatalf("events = %+v", pub.events)
	}
	e := pub.events[0]
	if want := "Rate updated: you'll receive " + owed + " USDT"; e.Message != want || e.Data["asset"] != "USDT" {
		t.Fatalf("event = %+v, want %q", e, want)
	}
	if strings.Contains(e.Message, "USDC") {
		t.Fatalf("message names the USDC leg: %q", e.Message)
	}
}
