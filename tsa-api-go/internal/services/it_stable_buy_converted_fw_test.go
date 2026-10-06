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
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// fwConvertedVenue is PriceConvertedLeg's answer for Flutterwave's worked
// example (₦165,025 → D = 94.5375 USDC on Polygon) at 50 bps: T = 94.064812.
func fwConvertedVenue() PaycrestVenue {
	v := convertedVenueForTest()
	v.ChainKey, v.Network, v.ViaChain, v.Rate = "polygon", "polygon", "polygon", "1745.61"
	v.TokenAmountBaseUnits = ConversionOwed(big.NewInt(94_537_500), 50).String()
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
	verifier.credited = big.NewInt(94_537_500)
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
		Verifiers: map[string]ERC20TransferVerifier{"polygon": &stubReceiptVerifier{credited: big.NewInt(94_537_500)}},
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
	intent.AssetAmount, intent.Status, intent.DeliveryID = "94537500", models.ITStatusDelivering, "trf_fwc"
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
		Verifiers: map[string]ERC20TransferVerifier{"polygon": &stubReceiptVerifier{credited: big.NewInt(94_537_500)}},
		Probes:    map[string]TreasuryTxProbe{"polygon": &fakeTxProbe{head: 100, receiptBlock: 90}}})
	if _, err := s.AttachConversionHash(context.Background(), uuid.New(), intent.ID, hash, "from FW support"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("held elsewhere: %v", err)
	}
	db.Delete(other)
	rec, err := s.AttachConversionHash(context.Background(), uuid.New(), intent.ID, hash, "from FW support")
	if err != nil || rec.Status != models.PaycrestStatusConverting || rec.TxHash != hash || rec.TokenAmount != "94.5375" {
		t.Fatalf("rec = %+v err = %v", rec, err)
	}
}
