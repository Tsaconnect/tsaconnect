package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// createWebhookEventsTable adds the webhook_events table to the shared
// trade-service SQLite DB (setupTradeDB doesn't include it). Mirrors the DDL in
// handlers/nomba_webhook_handler_test.go's newSQLiteWebhookStore.
func createWebhookEventsTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS webhook_events (
		id TEXT PRIMARY KEY,
		provider TEXT,
		event_id TEXT,
		payload TEXT,
		signature TEXT,
		status TEXT,
		error TEXT,
		received_at DATETIME,
		processed_at DATETIME,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create webhook_events: %v", err)
	}
}

// setTradeUpdatedAt back-dates a trade's updated_at so the RecoverStuckTrades
// `updated_at < now-olderThan` cutoff (which uses real time.Now) can match it
// regardless of the service's fixed test clock.
func setTradeUpdatedAt(t *testing.T, db *gorm.DB, id uuid.UUID, at time.Time) {
	t.Helper()
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", id).
		Update("updated_at", at).Error; err != nil {
		t.Fatalf("set updated_at: %v", err)
	}
}

// markPaidNoRelease flips a seeded trade straight to `paid` with an empty
// release_tx_hash — the paid-orphan shape RecoverStuckTrades targets.
func markPaidNoRelease(t *testing.T, db *gorm.DB, id uuid.UUID) {
	t.Helper()
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", id).
		Update("status", models.P2PTradeStatusPaid).Error; err != nil {
		t.Fatalf("flip to paid: %v", err)
	}
}

func TestRecoverStuckTrades(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())

	// 1. An old paid-orphan on a PM ad → should be recovered.
	stuck := seedBuyTradeOnAd(t, db, ad, uuid.New(), "1500.00")
	markPaidNoRelease(t, db, stuck.ID)
	setTradeUpdatedAt(t, db, stuck.ID, time.Now().Add(-30*time.Minute))
	setReleasedEvent(esc, stuck)

	// 2. A recent paid-orphan (updated just now) → NOT touched (under threshold).
	recent := seedBuyTradeOnAd(t, db, ad, uuid.New(), "1600.00")
	markPaidNoRelease(t, db, recent.ID)
	setTradeUpdatedAt(t, db, recent.ID, time.Now())

	// 3. A non-PM paid-orphan (ad.pm_id NULL) → NOT touched.
	nonPMAd := &models.P2PAd{
		ID: uuid.New(), SellerID: uuid.New(),
		Side: models.P2PAdSideSell, Token: "USDT", ChainID: 146,
		Amount: "1000000", Remaining: "1000000", Reserved: "0",
		FiatCurrency: "NGN", Rate: "1300", RateType: models.P2PAdRateTypeFixed,
		Status: models.P2PAdStatusActive,
	}
	if err := db.Create(nonPMAd).Error; err != nil {
		t.Fatalf("seed non-PM ad: %v", err)
	}
	nonPM := seedBuyTradeOnAd(t, db, nonPMAd, uuid.New(), "1700.00")
	markPaidNoRelease(t, db, nonPM.ID)
	setTradeUpdatedAt(t, db, nonPM.ID, time.Now().Add(-30*time.Minute))

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(nil))

	got, err := auto.RecoverStuckTrades(context.Background(), 5*time.Minute)
	if err != nil {
		t.Fatalf("RecoverStuckTrades: %v", err)
	}
	if got != 1 {
		t.Fatalf("recovered = %d, want 1", got)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want exactly 1", len(signer.calls))
	}

	var stuckP, recentP, nonPMP models.P2PTrade
	db.First(&stuckP, "id = ?", stuck.ID)
	db.First(&recentP, "id = ?", recent.ID)
	db.First(&nonPMP, "id = ?", nonPM.ID)
	if stuckP.Status != models.P2PTradeStatusReleased {
		t.Errorf("stuck trade status = %q, want released", stuckP.Status)
	}
	if recentP.Status != models.P2PTradeStatusPaid {
		t.Errorf("recent trade status = %q, want untouched (paid)", recentP.Status)
	}
	if nonPMP.Status != models.P2PTradeStatusPaid {
		t.Errorf("non-PM trade status = %q, want untouched (paid)", nonPMP.Status)
	}
}

func TestRetryFailedWebhooks(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	createWebhookEventsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	// A `failed` nomba inbound event whose payload will now match the pending
	// trade (same wallet, sender, amount) → re-dispatch settles it.
	body := []byte(`{
		"event_type": "payment_success",
		"requestId": "REQ-retry-1",
		"data": {
			"merchant": {"walletId": "` + pm.NombaWalletID + `", "userId": "u1"},
			"transaction": {"transactionId": "txn-1", "type": "vact_transfer", "transactionAmount": 1500, "fee": 0, "time": "2026-06-16T09:00:00Z", "responseCode": "00"},
			"customer": {"senderName": "JOHN DOE", "accountNumber": "0987654321"}
		}
	}`)
	failedRow := &models.WebhookEvent{
		ID: uuid.New(), Provider: ProviderNomba, EventID: "REQ-retry-1",
		Payload: body, Signature: "sig", Status: "failed",
		ReceivedAt: time.Now().Add(-10 * time.Minute),
	}
	if err := db.Create(failedRow).Error; err != nil {
		t.Fatalf("seed failed webhook: %v", err)
	}

	// A `pending` row received just now → NOT stale yet → NOT touched.
	pendingRow := &models.WebhookEvent{
		ID: uuid.New(), Provider: ProviderNomba, EventID: "REQ-pending-1",
		Payload: body, Signature: "sig", Status: "pending",
		ReceivedAt: time.Now(),
	}
	if err := db.Create(pendingRow).Error; err != nil {
		t.Fatalf("seed pending webhook: %v", err)
	}

	got, err := auto.RetryFailedWebhooks(context.Background(), 5*time.Minute)
	if err != nil {
		t.Fatalf("RetryFailedWebhooks: %v", err)
	}
	if got != 1 {
		t.Fatalf("retried = %d, want 1", got)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1 (the matched trade released)", len(signer.calls))
	}

	var failedP, pendingP models.WebhookEvent
	db.First(&failedP, "id = ?", failedRow.ID)
	db.First(&pendingP, "id = ?", pendingRow.ID)
	if failedP.Status != "processed" {
		t.Errorf("failed row status = %q, want processed", failedP.Status)
	}
	if pendingP.Status != "pending" {
		t.Errorf("recent pending row status = %q, want untouched (pending)", pendingP.Status)
	}

	var tradeP models.P2PTrade
	db.First(&tradeP, "id = ?", trade.ID)
	if tradeP.Status != models.P2PTradeStatusReleased {
		t.Errorf("trade status = %q, want released", tradeP.Status)
	}
}

// TestRetryFailedWebhooks_Flutterwave guards the recovery job against a
// provider-scoping regression: FW is the primary gateway, so a failed FW event
// must be selected and re-driven via DispatchFlutterwave. The job previously
// hardcoded provider=nomba and would leave FW money permanently stuck.
func TestRetryFailedWebhooks_Flutterwave(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	createWebhookEventsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	// FW attributes inbound credits by the VA accountRef (nomba_account_ref
	// column) via GetByAccountRef, not walletId — set a deterministic ref and
	// key the charge.completed payload on it.
	accountRef := "pm-" + pm.UserID.String()
	if err := db.Model(&models.PaymentMerchant{}).Where("id = ?", pm.ID).
		Update("nomba_account_ref", accountRef).Error; err != nil {
		t.Fatalf("set account ref: %v", err)
	}
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	// A `failed` Flutterwave charge.completed whose reference matches the PM's VA
	// and whose amount/sender match the pending trade → re-dispatch settles it.
	body := []byte(`{
		"webhook_id": "fw-retry-1",
		"type": "charge.completed",
		"data": {
			"reference": "` + accountRef + `",
			"status": "succeeded",
			"amount": 1500,
			"currency": "NGN",
			"customer": {"name": "JOHN DOE"}
		}
	}`)
	failedRow := &models.WebhookEvent{
		ID: uuid.New(), Provider: ProviderFlutterwave, EventID: "fw-retry-1",
		Payload: body, Signature: "sig", Status: "failed",
		ReceivedAt: time.Now().Add(-10 * time.Minute),
	}
	if err := db.Create(failedRow).Error; err != nil {
		t.Fatalf("seed failed FW webhook: %v", err)
	}

	got, err := auto.RetryFailedWebhooks(context.Background(), 5*time.Minute)
	if err != nil {
		t.Fatalf("RetryFailedWebhooks: %v", err)
	}
	if got != 1 {
		t.Fatalf("retried = %d, want 1 (the FW event re-driven)", got)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1 (the matched trade released)", len(signer.calls))
	}

	var failedP models.WebhookEvent
	db.First(&failedP, "id = ?", failedRow.ID)
	if failedP.Status != "processed" {
		t.Errorf("failed FW row status = %q, want processed", failedP.Status)
	}
	var tradeP models.P2PTrade
	db.First(&tradeP, "id = ?", trade.ID)
	if tradeP.Status != models.P2PTradeStatusReleased {
		t.Errorf("trade status = %q, want released", tradeP.Status)
	}
}

func TestDispatch_RoutesInboundAndPayout(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	// Inbound payment_success → HandleInbound → release.
	inbound := []byte(`{
		"event_type": "payment_success",
		"requestId": "REQ-in-1",
		"data": {
			"merchant": {"walletId": "` + pm.NombaWalletID + `", "userId": "u1"},
			"transaction": {"transactionId": "txn-1", "type": "vact_transfer", "transactionAmount": 1500, "fee": 0, "time": "2026-06-16T09:00:00Z", "responseCode": "00"},
			"customer": {"senderName": "JOHN DOE", "accountNumber": "0987654321"}
		}
	}`)
	if err := auto.Dispatch(context.Background(), inbound); err != nil {
		t.Fatalf("Dispatch inbound: %v", err)
	}
	var tradeP models.P2PTrade
	db.First(&tradeP, "id = ?", trade.ID)
	if tradeP.Status != models.P2PTradeStatusReleased {
		t.Fatalf("inbound dispatch: trade status = %q, want released", tradeP.Status)
	}
	if len(signer.calls) != 1 {
		t.Fatalf("inbound dispatch: signer calls = %d, want 1", len(signer.calls))
	}

	// Payout success → HandlePayoutStatus. Resolve via merchantTxRef = a deposited
	// sell trade. We seed a deposited sell trade keyed by its UUID.
	sell := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1300")
	setReleasedEvent(esc, sell)
	payout := []byte(`{
		"event_type": "payout_success",
		"requestId": "REQ-po-1",
		"data": {
			"merchant": {"walletId": "` + pm.NombaWalletID + `", "userId": "u1"},
			"transaction": {"transactionId": "po-1", "type": "transfer", "time": "2026-06-16T09:00:00Z", "responseCode": "00", "merchantTxRef": "` + sell.ID.String() + `"},
			"customer": {}
		}
	}`)
	if err := auto.Dispatch(context.Background(), payout); err != nil {
		t.Fatalf("Dispatch payout: %v", err)
	}
	var sellP models.P2PTrade
	db.First(&sellP, "id = ?", sell.ID)
	if sellP.Status != models.P2PTradeStatusReleased {
		t.Fatalf("payout dispatch: sell trade status = %q, want released", sellP.Status)
	}
	if len(signer.calls) != 2 {
		t.Fatalf("payout dispatch: signer calls = %d, want 2", len(signer.calls))
	}
}
