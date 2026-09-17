package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// One open MCGP buy per user (409 naming the order), the user-facing cancel,
// and the pay-into card on the order detail.

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, rec.Body.String())
	}
	return body
}

// createHandlerInboundCreditsTable is the slice of inbound_credits the
// buy-cancel late-payment guard reads.
func createHandlerInboundCreditsTable(t *testing.T, h *ITHandler) {
	t.Helper()
	if err := h.db.Exec(`CREATE TABLE inbound_credits (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL,
		gross_kobo INTEGER NOT NULL,
		sender_name TEXT,
		created_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("inbound_credits ddl: %v", err)
	}
}

func openedIntentID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	intent, _ := itBuyData(t, rec)["intent"].(map[string]any)
	id, _ := intent["id"].(string)
	if id == "" {
		t.Fatalf("no intent id in body=%s", rec.Body.String())
	}
	return id
}

func doITIntentAction(t *testing.T, h *ITHandler, user *models.User, id string, fn func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/it/buy/"+id+"/cancel", nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Set("user", user)
	fn(c)
	return w
}

func TestBuy_SecondOpenOrderIs409NamingTheFirst(t *testing.T) {
	for _, mode := range []string{"central", "va"} {
		t.Run(mode, func(t *testing.T) {
			h, db, it := newITBuyHandler(t)
			user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
			if mode == "central" {
				it.SetInboundCentral(true, testCentralAccount())
			} else {
				seedNGNVA(t, db, user.ID)
			}

			firstID := openedIntentID(t, doITBuy(t, h, user, "500000"))

			rec := doITBuy(t, h, user, "700000")
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
			}
			body := decodeBody(t, rec)
			if body["code"] != "it_buy_order_open" {
				t.Errorf("code = %v, want it_buy_order_open", body["code"])
			}
			data, _ := body["data"].(map[string]any)
			if data["intentId"] != firstID || data["ngnKobo"] != "500000" {
				t.Errorf("data = %v, want intentId %s and ngnKobo 500000", data, firstID)
			}

			var n int64
			db.Model(&models.ITIntent{}).Where("user_id = ?", user.ID).Count(&n)
			if n != 1 {
				t.Errorf("%d intents, want 1 — the refusal wrote or cancelled something", n)
			}
		})
	}
}

func TestPostCancelBuy_CancelsTheCallersOpenOrder(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	createHandlerInboundCreditsTable(t, h)
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, user, "500000"))

	rec := doITIntentAction(t, h, user, id, h.PostCancelBuy)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	data, _ := decodeBody(t, rec)["data"].(map[string]any)
	intent, _ := data["intent"].(map[string]any)
	if intent["status"] != models.ITStatusCancelled {
		t.Errorf("returned intent status = %v, want cancelled; body=%s", intent["status"], rec.Body.String())
	}

	// And the user can buy again.
	if again := doITBuy(t, h, user, "700000"); again.Code != http.StatusOK {
		t.Fatalf("new order after cancel: status %d; body=%s", again.Code, again.Body.String())
	}
}

func TestPostCancelBuy_AnotherUsersOrderIsNotFound(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	createHandlerInboundCreditsTable(t, h)
	owner := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, owner, "500000"))

	stranger := &models.User{ID: uuid.New(), Name: "JANE ROE", WalletAddress: "0xOTHER"}
	rec := doITIntentAction(t, h, stranger, id, h.PostCancelBuy)
	if rec.Code == http.StatusOK {
		t.Fatalf("a stranger cancelled the order; body=%s", rec.Body.String())
	}
	var got models.ITIntent
	if err := db.First(&got, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != models.ITStatusPendingPayment {
		t.Errorf("status = %q after a stranger's cancel", got.Status)
	}
}

func TestPostCancelBuy_PaymentArrivedIs409WithAReadableMessage(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	createHandlerInboundCreditsTable(t, h)
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, user, "500000"))
	if err := h.db.Exec(`INSERT INTO inbound_credits (id, status, gross_kobo, sender_name, created_at) VALUES (?, ?, ?, ?, ?)`,
		uuid.NewString(), models.InboundCreditHeld, 500_000, "DOE JOHN", time.Now().Add(time.Minute)).Error; err != nil {
		t.Fatal(err)
	}

	rec := doITIntentAction(t, h, user, id, h.PostCancelBuy)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["code"] != "it_buy_payment_arrived" {
		t.Errorf("code = %v, want it_buy_payment_arrived", body["code"])
	}
	if msg, _ := body["message"].(string); msg != itBuyPaymentArrivedMessage {
		t.Errorf("message = %q, want the user-readable explanation", msg)
	}
}

// The old stablecoin route still answers for older app builds, now through the
// shared cancel.
func TestPostCancelStableBuy_StillRefusesAnMCGPOrder(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	createHandlerInboundCreditsTable(t, h)
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, user, "500000"))

	rec := doITIntentAction(t, h, user, id, h.PostCancelStableBuy)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if decodeBody(t, rec)["code"] != "stable_bad_state" {
		t.Errorf("body=%s, want stable_bad_state", rec.Body.String())
	}
}

// ─── GET /it/intents/:id pay-into card ─────────────────────────────────────

func doITGetIntent(t *testing.T, h *ITHandler, user *models.User, id string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/it/intents/"+id, nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Set("user", user)
	h.GetIntent(c)
	if w.Code != http.StatusOK {
		t.Fatalf("GetIntent status = %d; body=%s", w.Code, w.Body.String())
	}
	data, _ := decodeBody(t, w)["data"].(map[string]any)
	return data
}

func TestGetIntent_PendingBuyCarriesThePayIntoCard(t *testing.T) {
	t.Run("central", func(t *testing.T) {
		h, _, it := newITBuyHandler(t)
		it.SetInboundCentral(true, testCentralAccount())
		user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
		id := openedIntentID(t, doITBuy(t, h, user, "500000"))

		payTo, _ := doITGetIntent(t, h, user, id)["payTo"].(map[string]any)
		if payTo == nil {
			t.Fatal("no payTo on a pending central-mode buy")
		}
		acct := testCentralAccount()
		if payTo["accountNumber"] != acct.AccountNumber || payTo["bank"] != acct.BankName || payTo["amountNgn"] != "5000" {
			t.Errorf("payTo = %v, want the company account asking ₦5000", payTo)
		}
		if s, _ := payTo["payerInstruction"].(string); s == "" {
			t.Error("central payTo carries no payer instruction")
		}
	})
	t.Run("va", func(t *testing.T) {
		h, db, _ := newITBuyHandler(t)
		user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
		seedNGNVA(t, db, user.ID)
		id := openedIntentID(t, doITBuy(t, h, user, "500000"))

		payTo, _ := doITGetIntent(t, h, user, id)["payTo"].(map[string]any)
		if payTo == nil || payTo["accountNumber"] != "9988776655" || payTo["bank"] != "Wema Bank" {
			t.Errorf("payTo = %v, want the user's own virtual account", payTo)
		}
		if _, ok := payTo["amountNgn"]; ok {
			t.Error("VA payTo carries a central-mode amount")
		}
	})
}

func TestGetIntent_OmitsPayToOnceTheOrderIsNotOpen(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, user, "500000"))

	for _, tc := range []struct {
		name string
		set  map[string]any
	}{
		{"cancelled", map[string]any{"status": models.ITStatusCancelled}},
		{"processing", map[string]any{"status": models.ITStatusProcessing}},
		// Still pending_payment, but money is already attributed: a pay-into
		// card here would invite a second transfer.
		{"attributed", map[string]any{"status": models.ITStatusPendingPayment, "payment_ref": "100004260910120000000000012345"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.Model(&models.ITIntent{}).Where("id = ?", id).Updates(tc.set).Error; err != nil {
				t.Fatal(err)
			}
			if payTo, ok := doITGetIntent(t, h, user, id)["payTo"]; ok && payTo != nil {
				t.Errorf("payTo = %v on a %s order", payTo, tc.name)
			}
		})
	}
}

func TestGetIntent_OmitsPayToOnASell(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	sell := &models.ITIntent{
		ID: uuid.New(), UserID: user.ID, Direction: models.ITDirectionSell,
		Status: models.ITStatusPendingPayment, Currency: "NGN",
		McgpAmount: "1", NgnKobo: "500000", RateKobo: "1",
	}
	if err := db.Create(sell).Error; err != nil {
		t.Fatal(err)
	}
	if payTo, ok := doITGetIntent(t, h, user, sell.ID.String())["payTo"]; ok && payTo != nil {
		t.Errorf("payTo = %v on a sell", payTo)
	}
}

// Detail stays scoped to the caller: another user's id is a 404, card or not.
func TestGetIntent_AnotherUsersOrderIs404(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	owner := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, owner, "500000"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/it/intents/"+id, nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Set("user", &models.User{ID: uuid.New()})
	h.GetIntent(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}
