package handlers

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// centralSourceFor hands the P2P handler the REAL IT service rather than a
// double, so these tests exercise the same ActiveCentralAccount() the buy flows
// use — including its refusal to serve a configured account while the mode is
// off.
func centralSourceFor(t *testing.T, enabled bool) *services.ITService {
	t.Helper()
	_, _, it := newITBuyHandler(t)
	it.SetInboundCentral(enabled, testCentralAccount())
	return it
}

func pmPayToOf(t *testing.T, r *rig, path string) map[string]any {
	t.Helper()
	rec := doJSON(t, r, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	data, _ := decode(t, rec)["data"].(map[string]any)
	if data == nil {
		t.Fatalf("no data in body=%s", rec.Body.String())
	}
	if data["isPm"] != true {
		t.Fatalf("isPm = %v, want true; body=%s", data["isPm"], rec.Body.String())
	}
	payTo, _ := data["pmPayTo"].(map[string]any)
	if payTo == nil {
		t.Fatalf("pmPayTo missing; body=%s", rec.Body.String())
	}
	return payTo
}

func assertCentralPayTo(t *testing.T, payTo map[string]any) {
	t.Helper()
	want := testCentralAccount()
	if payTo["accountNumber"] != want.AccountNumber || payTo["bank"] != want.BankName ||
		payTo["accountName"] != want.AccountName {
		t.Fatalf("pmPayTo = %v, want the company account %+v", payTo, want)
	}
	// The company account is only safe to show with the own-name notice beside
	// it — on the ad screen as much as the trade screen, since both render it.
	// These fixtures store no verified legal name, so the generic wording.
	if payTo["payerInstruction"] != want.PayerInstruction("") {
		t.Fatalf("pmPayTo.payerInstruction = %v, want %q", payTo["payerInstruction"], want.PayerInstruction(""))
	}
}

// The buyer of a PM trade must be sent to the company account once the cutover
// is on. Sent to the merchant's own account instead, their transfer never
// reaches the statement the ingestor reads, while the trade is still offered to
// the matcher as a claim against credits on that statement — the two live
// attribution paths the design forbids.
func TestGetTrade_PMPayTo_CentralMode(t *testing.T) {
	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, true))
	tradeID := seedPMTrade(t, r, "JOHN DOE")

	assertCentralPayTo(t, pmPayToOf(t, r, "/api/p2p/trades/"+tradeID.String()))
}

// Configured but not cut over: the merchant's account, exactly as before.
func TestGetTrade_PMPayTo_ConfiguredButVAMode(t *testing.T) {
	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, false))
	tradeID := seedPMTrade(t, r, "JOHN DOE")

	payTo := pmPayToOf(t, r, "/api/p2p/trades/"+tradeID.String())
	if payTo["accountNumber"] != "1234567890" || payTo["bank"] != "Nomba" {
		t.Fatalf("pmPayTo = %v, want the merchant's own account", payTo)
	}
}

func seedPMAd(t *testing.T, r *rig) uuid.UUID {
	t.Helper()
	pmID := uuid.New()
	if err := r.db.Exec(
		`INSERT INTO payment_merchants (id, user_id, name, status, inbound_account_number, inbound_bank) VALUES (?,?,?,?,?,?)`,
		pmID.String(), uuid.New().String(), "TSA Connect", models.PaymentMerchantStatusActive, "1234567890", "Nomba",
	).Error; err != nil {
		t.Fatalf("seed payment_merchant: %v", err)
	}
	adID := uuid.New()
	if err := r.db.Create(&models.P2PAd{ID: adID, PMID: &pmID, SellerID: uuid.New(), Side: models.P2PAdSideSell,
		Token: "USDT", FiatCurrency: "NGN", Amount: "1000000", Remaining: "1000000", Reserved: "0", Rate: "1500",
		RateType: models.P2PAdRateTypeFixed, FloatAnchor: models.P2PAdFloatAnchorBybitSell,
		Status: models.P2PAdStatusActive}).Error; err != nil {
		t.Fatalf("seed pm ad: %v", err)
	}
	// As in production: without a PM service a tagged ad hides as LP stock (S29).
	r.trade.SetPMService(services.NewPMService(r.db))
	return adID
}

// The ad screen shows the same card before a trade exists, so it has to move
// with the trade screen or the buyer is told two different things about where
// their money goes.
func TestGetAd_PMPayTo_CentralMode(t *testing.T) {
	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, true))
	adID := seedPMAd(t, r)

	assertCentralPayTo(t, pmPayToOf(t, r, "/api/p2p/ads/"+adID.String()))
}

func TestGetAd_PMPayTo_ConfiguredButVAMode(t *testing.T) {
	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, false))
	adID := seedPMAd(t, r)

	payTo := pmPayToOf(t, r, "/api/p2p/ads/"+adID.String())
	if payTo["accountNumber"] != "1234567890" || payTo["bank"] != "Nomba" {
		t.Fatalf("pmPayTo = %v, want the merchant's own account", payTo)
	}
	if _, present := payTo["payerInstruction"]; present {
		t.Fatalf("pmPayTo carries payerInstruction for a merchant's own account: %v", payTo)
	}
}
