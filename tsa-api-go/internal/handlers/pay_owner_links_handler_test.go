package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type linkResponse struct {
	status int
	raw    string
	Code   string                  `json:"code"`
	Msg    string                  `json:"message"`
	Data   services.PayPaymentView `json:"data"`
}

func (o *ownerRig) link(method, path string, body map[string]any) *linkResponse {
	w := payDo(o.r, method, path, body, nil)
	res := &linkResponse{status: w.Code, raw: w.Body.String()}
	json.Unmarshal(w.Body.Bytes(), res)
	return res
}

func linkBody() map[string]any {
	return map[string]any{"amount": 2_500_000, "description": "Wedding cake deposit", "customerName": "Ada Obi"}
}

// The owner's merchant is not switched live (and live is off globally), so a
// live link is refused up front; a test link is made.
func TestPayOwnerLinks_LiveNeedsLivePaymentsSwitchedOn(t *testing.T) {
	o := newOwnerRig(t)
	res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=live", linkBody())
	if res.status != http.StatusForbidden || res.Code != "live_not_enabled" ||
		res.Msg != "Live payments aren't switched on for your business yet. Try a test link first." {
		t.Fatalf("live: %d %s", res.status, res.raw)
	}
	res = o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", linkBody())
	if res.status != http.StatusCreated || res.Data.Source != models.PaySourceLink || res.Data.Mode != models.PayModeTest ||
		res.Data.CustomerName != "Ada Obi" || res.Data.Description != "Wedding cake deposit" ||
		res.Data.CheckoutURL != "https://x/"+res.Data.ID.String() || !strings.HasPrefix(res.Data.Reference, "link_") ||
		res.Data.MerchantNet != 2_500_000 || !res.Data.ExpiresAt.Equal(payHandlerNow.AddDate(0, 0, 7)) {
		t.Fatalf("test: %d %s", res.status, res.raw)
	}
	// Both switches on: a live link is made.
	o.h.liveEnabled = true
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("live_enabled", true)
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=live", linkBody()); res.status != http.StatusCreated || res.Data.Mode != models.PayModeLive {
		t.Fatalf("live, switched on: %d %s", res.status, res.raw)
	}
}

func TestPayOwnerLinks_CreateRefusals(t *testing.T) {
	o := newOwnerRig(t)
	no := linkBody()
	no["description"] = "  "
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", no); res.status != http.StatusUnprocessableEntity ||
		res.Msg != "Say what the payment is for" {
		t.Fatalf("no description: %d %s", res.status, res.raw)
	}
	days := linkBody()
	days["expiresInDays"] = 2
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", days); res.status != http.StatusUnprocessableEntity ||
		!strings.Contains(res.Msg, "1, 3 or 7 days") {
		t.Fatalf("2 days: %d %s", res.status, res.raw)
	}
	hidden := linkBody()
	hidden["customerName"] = "Ada\u202eObi"
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", hidden); res.status != http.StatusUnprocessableEntity ||
		res.Msg != "The customer's name can't contain hidden or control characters" {
		t.Fatalf("an RTL override in the name: %d %s", res.status, res.raw)
	}
	cheap := linkBody()
	cheap["amount"] = 1
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", cheap); res.status != http.StatusUnprocessableEntity ||
		res.Msg != "Enter an amount between ₦1,000.00 and ₦5,000,000.00" {
		t.Fatalf("below the minimum: %d %s", res.status, res.raw)
	}
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=nope", linkBody()); res.status != http.StatusBadRequest {
		t.Fatalf("unknown mode: %d", res.status)
	}
	ref := linkBody()
	ref["reference"] = "INV-1"
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", ref); res.status != http.StatusCreated {
		t.Fatalf("first INV-1: %d %s", res.status, res.raw)
	}
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", ref); res.status != http.StatusConflict ||
		res.Code != "reference_taken" || res.Msg != "You've already used this reference — use another or leave it blank" {
		t.Fatalf("INV-1 again: %d %s", res.status, res.raw)
	}

	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("settlement_mode", "")
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", linkBody()); res.status != http.StatusConflict ||
		res.Code != "settlement_not_set" || res.Msg != "Set where you want to be paid before you request a payment" {
		t.Fatalf("no settlement: %d %s", res.status, res.raw)
	}
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Updates(map[string]any{"settlement_mode": models.PaySettlementFiat, "status": models.PayMerchantSuspended})
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", linkBody()); res.status != http.StatusForbidden || res.Code != "merchant_suspended" {
		t.Fatalf("suspended: %d %s", res.status, res.raw)
	}
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("user_id", uuid.New())
	if res := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", linkBody()); res.status != http.StatusNotFound || res.Code != "no_merchant" {
		t.Fatalf("no merchant: %d %s", res.status, res.raw)
	}
}

func TestPayOwnerLinks_ListIsLinksOnlyAndPaymentsFilterBySource(t *testing.T) {
	o := newOwnerRig(t)
	ctx := context.Background()
	link := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", linkBody())
	api, _, err := o.payments.Create(ctx, o.m, models.PayModeTest, services.CreatePayPaymentInput{AmountKobo: 5_000_000, Reference: "PNR-API", Description: "LOS-ABV"})
	if err != nil {
		t.Fatal(err)
	}
	stranger := seedHandlerMerchant(t, o.db)
	if _, err := o.payments.CreateLink(ctx, stranger, models.PayModeTest, services.CreatePayLinkInput{AmountKobo: 5_000_000, Description: "theirs"}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/api/pay/merchant/links?mode=test":                link.Data.ID.String(),
		"/api/pay/merchant/payments?mode=test&source=link": link.Data.ID.String(),
		"/api/pay/merchant/payments?mode=test&source=api":  api.ID.String(),
	} {
		res := o.do(http.MethodGet, path, nil)
		if res.status != http.StatusOK || !strings.Contains(res.raw, `"total":1`) || !strings.Contains(res.raw, want) {
			t.Fatalf("%s: %d %s", path, res.status, res.raw)
		}
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/payments?mode=test", nil); !strings.Contains(res.raw, `"total":2`) {
		t.Fatalf("every source: %s", res.raw)
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/links?mode=live", nil); !strings.Contains(res.raw, `"total":0`) {
		t.Fatalf("live links: %s", res.raw)
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/payments?source=web", nil); res.status != http.StatusBadRequest {
		t.Fatalf("unknown source: %d", res.status)
	}
}

func TestPayOwnerLinks_CancelIsTheOwnersPendingLinksOnly(t *testing.T) {
	o := newOwnerRig(t)
	ctx := context.Background()
	link := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", linkBody())
	api, _, _ := o.payments.Create(ctx, o.m, models.PayModeTest, services.CreatePayPaymentInput{AmountKobo: 5_000_000, Reference: "PNR-API", Description: "LOS-ABV"})
	stranger := seedHandlerMerchant(t, o.db)
	theirs, _ := o.payments.CreateLink(ctx, stranger, models.PayModeTest, services.CreatePayLinkInput{AmountKobo: 5_000_000, Description: "theirs"})

	cancel := func(id, mode string) *linkResponse {
		return o.link(http.MethodPost, "/api/pay/merchant/links/"+id+"/cancel?mode="+mode, nil)
	}
	if res := cancel(theirs.ID.String(), "test"); res.status != http.StatusNotFound {
		t.Fatalf("another owner's link: %d %s", res.status, res.raw)
	}
	if got, _ := o.payments.Get(ctx, theirs.ID); got.Status != models.PayStatusPending {
		t.Fatalf("another owner's link was cancelled: %s", got.Status)
	}
	if res := cancel(link.Data.ID.String(), "live"); res.status != http.StatusNotFound {
		t.Fatalf("the other mode: %d", res.status)
	}
	if res := cancel("not-an-id", "test"); res.status != http.StatusNotFound {
		t.Fatalf("a malformed id: %d", res.status)
	}
	const refused = "This link can't be cancelled — it's already being paid, paid, or closed"
	if res := cancel(api.ID.String(), "test"); res.status != http.StatusConflict || res.Code != "not_cancellable" || res.Msg != refused {
		t.Fatalf("an API-made payment: %d %s", res.status, res.raw)
	}
	res := cancel(link.Data.ID.String(), "test")
	if res.status != http.StatusOK || res.Data.Status != models.PayStatusExpired || !res.Data.Cancelled {
		t.Fatalf("cancel: %d %s", res.status, res.raw)
	}
	if res := cancel(link.Data.ID.String(), "test"); res.status != http.StatusConflict || res.Code != "not_cancellable" {
		t.Fatalf("cancel twice: %d %s", res.status, res.raw)
	}
	busy := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", linkBody())
	if _, _, err := o.payments.Lock(ctx, &models.User{ID: uuid.New()}, busy.Data.ID, false); err != nil {
		t.Fatal(err)
	}
	if res := cancel(busy.Data.ID.String(), "test"); res.status != http.StatusConflict || res.Code != "not_cancellable" {
		t.Fatalf("a link being paid: %d %s", res.status, res.raw)
	}
}

// What the payer's page reads for a link: what it is for, who it is for, and —
// once called off — that it was cancelled rather than run out.
func TestPayOwnerLinks_PublicViewCarriesTheLinkFields(t *testing.T) {
	o := newOwnerRig(t)
	link := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", linkBody())
	var pub struct {
		Data map[string]any `json:"data"`
	}
	w := payDo(o.r, http.MethodGet, "/api/pay/public/"+link.Data.ID.String(), nil, nil)
	json.Unmarshal(w.Body.Bytes(), &pub)
	if pub.Data["description"] != "Wedding cake deposit" || pub.Data["customerName"] != "Ada Obi" || pub.Data["cancelled"] != nil {
		t.Fatalf("public: %s", w.Body.String())
	}
	o.link(http.MethodPost, "/api/pay/merchant/links/"+link.Data.ID.String()+"/cancel?mode=test", nil)
	w = payDo(o.r, http.MethodGet, "/api/pay/public/"+link.Data.ID.String(), nil, nil)
	json.Unmarshal(w.Body.Bytes(), &pub)
	if pub.Data["status"] != models.PayStatusExpired || pub.Data["cancelled"] != true {
		t.Fatalf("public after cancel: %s", w.Body.String())
	}
}

// status=cancelled is the links the owner called off; status=expired only those
// that ran out — on /links and on /payments alike.
func TestPayOwnerLinks_StatusFilterSplitsCancelledFromExpired(t *testing.T) {
	o := newOwnerRig(t)
	ctx := context.Background()
	cancelled := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", linkBody())
	lapsed, err := o.payments.CreateLink(ctx, o.m, models.PayModeTest, services.CreatePayLinkInput{AmountKobo: 2_500_000, Description: "Cupcakes", ExpiresInDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	o.link(http.MethodPost, "/api/pay/merchant/links/"+cancelled.Data.ID.String()+"/cancel?mode=test", nil)
	// Ran out of time: expired by the sweep, as the reconciler would.
	o.db.Model(&models.PayPayment{}).Where("id = ?", lapsed.ID).Updates(map[string]any{"status": models.PayStatusExpired, "failure_reason": "expired"})

	for _, base := range []string{"/api/pay/merchant/links?mode=test", "/api/pay/merchant/payments?mode=test"} {
		for status, want := range map[string]string{"cancelled": cancelled.Data.ID.String(), "expired": lapsed.ID.String()} {
			res := o.do(http.MethodGet, base+"&status="+status, nil)
			if res.status != http.StatusOK || !strings.Contains(res.raw, `"total":1`) || !strings.Contains(res.raw, want) {
				t.Fatalf("%s status=%s: %d %s", base, status, res.status, res.raw)
			}
		}
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/links?mode=test&q=ada", nil); !strings.Contains(res.raw, `"total":1`) {
		t.Fatalf("search by customer name: %s", res.raw)
	}
}
