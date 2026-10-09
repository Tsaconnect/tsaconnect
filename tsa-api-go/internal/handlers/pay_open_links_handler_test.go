package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// openLinkResponse reads every open-link answer: create, update and replace
// answer the link itself as data; the list {items, total, page, limit}; the
// detail {openLink, payments}.
type openLinkResponse struct {
	status int
	raw    string
	Code   string `json:"code"`
	Msg    string `json:"message"`
	Link   services.PayOpenLinkView
	Detail struct {
		OpenLink services.PayOpenLinkView  `json:"openLink"`
		Payments []services.PayPaymentView `json:"payments"`
	}
	List struct {
		Items []services.PayOpenLinkView `json:"items"`
		Total int64                      `json:"total"`
		Page  int                        `json:"page"`
		Limit int                        `json:"limit"`
	}
}

func (o *ownerRig) openLink(method, path string, body any) *openLinkResponse {
	w := payDo(o.r, method, path, body, nil)
	res := &openLinkResponse{status: w.Code, raw: w.Body.String()}
	var env struct {
		Code string          `json:"code"`
		Msg  string          `json:"message"`
		Data json.RawMessage `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &env)
	res.Code, res.Msg = env.Code, env.Msg
	json.Unmarshal(env.Data, &res.Link)
	json.Unmarshal(env.Data, &res.Detail)
	json.Unmarshal(env.Data, &res.List)
	return res
}

func (o *ownerRig) newOpenLink(t *testing.T, body map[string]any) services.PayOpenLinkView {
	t.Helper()
	res := o.openLink(http.MethodPost, "/api/pay/merchant/open-links?mode=test", body)
	if res.status != http.StatusCreated {
		t.Fatalf("create: %d %s", res.status, res.raw)
	}
	return res.Link
}

func TestPayOpenLinks_CreateAndItsRefusals(t *testing.T) {
	o := newOwnerRig(t)
	l := o.newOpenLink(t, map[string]any{"label": "Shop 12, Balogun Market", "minAmount": 200_000})
	if !strings.HasPrefix(l.Slug, "sky-air-") || l.URL != "https://x/l/"+l.Slug || l.Label != "Shop 12, Balogun Market" ||
		l.MinAmount != 200_000 || l.MaxAmount != 0 || l.MerchantMaxAmount != 500_000_000 || !l.Active || l.Version != 1 || l.Mode != models.PayModeTest {
		t.Fatalf("link %+v", l)
	}
	if res := o.openLink(http.MethodPost, "/api/pay/merchant/open-links?mode=live", map[string]any{"label": "Shop"}); res.status != http.StatusForbidden || res.Code != "live_not_enabled" {
		t.Fatalf("live: %d %s", res.status, res.raw)
	}
	if res := o.openLink(http.MethodPost, "/api/pay/merchant/open-links?mode=test", map[string]any{"label": " "}); res.status != http.StatusUnprocessableEntity ||
		res.Msg != "Give the link a name your customers will recognise" {
		t.Fatalf("no label: %d %s", res.status, res.raw)
	}
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("settlement_mode", "")
	if res := o.openLink(http.MethodPost, "/api/pay/merchant/open-links?mode=test", map[string]any{"label": "Shop"}); res.status != http.StatusConflict || res.Code != "settlement_not_set" {
		t.Fatalf("no payout: %d %s", res.status, res.raw)
	}
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Updates(map[string]any{"settlement_mode": models.PaySettlementFiat, "status": models.PayMerchantSuspended})
	if res := o.openLink(http.MethodPost, "/api/pay/merchant/open-links?mode=test", map[string]any{"label": "Shop"}); res.status != http.StatusForbidden || res.Code != "merchant_suspended" {
		t.Fatalf("suspended: %d %s", res.status, res.raw)
	}
}

func TestPayOpenLinks_ListDetailUpdateAndReplace(t *testing.T) {
	o := newOwnerRig(t)
	l := o.newOpenLink(t, map[string]any{"label": "Shop 12"})
	// A customer pays through it.
	w := payDo(o.r, http.MethodPost, "/api/pay/public/open/"+l.Slug+"/payments", map[string]any{"amount": 450_000, "note": "rice"}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("pay: %d %s", w.Code, w.Body.String())
	}
	var started struct {
		Data struct {
			ID          uuid.UUID `json:"id"`
			CheckoutURL string    `json:"checkoutUrl"`
			Description string    `json:"description"`
			LinkLabel   string    `json:"linkLabel"`
			Source      string    `json:"source"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &started)
	if started.Data.CheckoutURL != "https://x/"+started.Data.ID.String() || started.Data.Description != "rice" ||
		started.Data.LinkLabel != "Shop 12" || started.Data.Source != models.PaySourceOpen {
		t.Fatalf("started %s", w.Body.String())
	}
	if _, err := o.payments.SimulateTestOutcome(t.Context(), &models.User{ID: o.owner.ID}, started.Data.ID, true); err != nil {
		t.Fatal(err)
	}

	// Another visitor enters an amount and goes no further: scan noise, not a
	// payment the owner is shown.
	if w := payDo(o.r, http.MethodPost, "/api/pay/public/open/"+l.Slug+"/payments", map[string]any{"amount": 450_000}, nil); w.Code != http.StatusCreated {
		t.Fatalf("an untried payment: %d", w.Code)
	}

	list := o.openLink(http.MethodGet, "/api/pay/merchant/open-links?mode=test&limit=500", nil)
	if list.status != http.StatusOK || len(list.List.Items) != 1 || list.List.Items[0].PaidCount != 1 || list.List.Items[0].PaidTotal != 450_000 ||
		list.List.Items[0].ReceivedTotal != 450_000 || !list.List.Items[0].TakingPayments || list.List.Items[0].NotTakingReason != "" ||
		list.List.Total != 1 || list.List.Page != 1 || list.List.Limit != 50 {
		t.Fatalf("list: %d %s", list.status, list.raw)
	}
	if !strings.Contains(list.raw, `"receivedTotal":450000`) || !strings.Contains(list.raw, `"takingPayments":true`) {
		t.Fatalf("list JSON: %s", list.raw)
	}
	detail := o.openLink(http.MethodGet, "/api/pay/merchant/open-links/"+l.ID.String()+"?mode=test", nil)
	if detail.status != http.StatusOK || detail.Detail.OpenLink.ID != l.ID || detail.Detail.OpenLink.PaidCount != 1 || len(detail.Detail.Payments) != 1 ||
		detail.Detail.Payments[0].ID != started.Data.ID || detail.Detail.Payments[0].Source != models.PaySourceOpen ||
		detail.Detail.Payments[0].OpenLinkID == nil || *detail.Detail.Payments[0].OpenLinkID != l.ID {
		t.Fatalf("detail: %d %s", detail.status, detail.raw)
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/payments?mode=test&source=open", nil); !strings.Contains(res.raw, `"total":1`) ||
		!strings.Contains(res.raw, `"linkLabel":"Shop 12"`) || !strings.Contains(res.raw, `"openLinkId":"`+l.ID.String()+`"`) {
		t.Fatalf("source=open: %s", res.raw)
	}

	up := o.openLink(http.MethodPut, "/api/pay/merchant/open-links/"+l.ID.String()+"?mode=test",
		map[string]any{"label": "Shop 14", "maxAmount": 900_000, "active": false, "expectedVersion": 1})
	if up.status != http.StatusOK || up.Link.Label != "Shop 14" || up.Link.Active || up.Link.Version != 2 || up.Link.MaxAmount != 900_000 ||
		up.Link.TakingPayments || up.Link.NotTakingReason != "paused" {
		t.Fatalf("update: %d %s", up.status, up.raw)
	}
	if res := o.openLink(http.MethodPut, "/api/pay/merchant/open-links/"+l.ID.String()+"?mode=test",
		map[string]any{"label": "Shop 15", "expectedVersion": 1}); res.status != http.StatusConflict || res.Code != "link_stale" {
		t.Fatalf("stale: %d %s", res.status, res.raw)
	}

	rep := o.openLink(http.MethodPost, "/api/pay/merchant/open-links/"+l.ID.String()+"/replace?mode=test", nil)
	if rep.status != http.StatusOK || rep.Link.ID != l.ID || rep.Link.Slug == l.Slug || rep.Link.URL != "https://x/l/"+rep.Link.Slug ||
		rep.Link.Label != "Shop 14" || rep.Link.PaidCount != 1 {
		t.Fatalf("replace: %d %s", rep.status, rep.raw)
	}
	w = payDo(o.r, http.MethodPost, "/api/pay/public/open/"+l.Slug+"/payments", map[string]any{"amount": 450_000}, nil)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"link_not_found"`) {
		t.Fatalf("paying the old address: %d %s", w.Code, w.Body.String())
	}
	// Paused, the new address reads closed.
	w = payDo(o.r, http.MethodPost, "/api/pay/public/open/"+rep.Link.Slug+"/payments", map[string]any{"amount": 450_000}, nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"link_closed"`) {
		t.Fatalf("paying a paused link: %d %s", w.Code, w.Body.String())
	}
}

func TestPayOpenLinks_AnOwnerReachesOnlyTheirOwn(t *testing.T) {
	o := newOwnerRig(t)
	stranger := seedHandlerMerchant(t, o.db)
	theirs, err := o.payments.CreateOpenLink(t.Context(), stranger, models.PayModeTest, services.PayOpenLinkInput{Label: "Theirs"})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/pay/merchant/open-links/" + theirs.ID.String()
	for _, rt := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, base + "?mode=test", nil},
		{http.MethodPut, base + "?mode=test", map[string]any{"label": "Mine now", "expectedVersion": 1}},
		{http.MethodPost, base + "/replace?mode=test", nil},
		{http.MethodGet, "/api/pay/merchant/open-links/not-an-id?mode=test", nil},
	} {
		if res := o.openLink(rt.method, rt.path, rt.body); res.status != http.StatusNotFound || res.Code != "link_not_found" {
			t.Fatalf("%s %s: %d %s", rt.method, rt.path, res.status, res.raw)
		}
	}
	if list := o.openLink(http.MethodGet, "/api/pay/merchant/open-links?mode=test", nil); len(list.List.Items) != 0 || list.List.Total != 0 {
		t.Fatalf("another merchant's link listed: %s", list.raw)
	}
	if got, _ := o.payments.OpenLinkForMerchant(t.Context(), stranger.ID, models.PayModeTest, theirs.ID); got.Label != "Theirs" || got.Version != 1 {
		t.Fatalf("another merchant's link changed: %+v", got)
	}
}

// The payer's page: exactly the fields the landing reads, and every refusal in
// the payer's own words.
func TestPayOpenLinks_PublicReadAndPay(t *testing.T) {
	o := newOwnerRig(t)
	l := o.newOpenLink(t, map[string]any{"label": "Shop 12", "minAmount": 200_000, "maxAmount": 900_000})
	w := payDo(o.r, http.MethodGet, "/api/pay/public/open/"+l.Slug, nil, nil)
	var pub struct {
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &pub)
	want := map[string]any{"merchantName": "Sky Air", "label": "Shop 12", "mode": "test", "minAmount": 200_000.0, "maxAmount": 900_000.0, "active": true}
	if w.Code != http.StatusOK || len(pub.Data) != len(want) {
		t.Fatalf("public: %d %s", w.Code, w.Body.String())
	}
	for k, v := range want {
		if pub.Data[k] != v {
			t.Fatalf("public %s = %v, want %v (%s)", k, pub.Data[k], v, w.Body.String())
		}
	}
	if w := payDo(o.r, http.MethodGet, "/api/pay/public/open/no-such-link", nil, nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"link_not_found"`) {
		t.Fatalf("unknown slug: %d %s", w.Code, w.Body.String())
	}
	pay := func(body any) (int, string) {
		w := payDo(o.r, http.MethodPost, "/api/pay/public/open/"+l.Slug+"/payments", body, nil)
		return w.Code, w.Body.String()
	}
	if code, body := pay(map[string]any{"amount": 100}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "Enter an amount between ₦2,000.00 and ₦9,000.00") {
		t.Fatalf("too little: %d %s", code, body)
	}
	if code, body := pay(map[string]any{"amount": 450_000, "note": "rice‮"}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "can't contain hidden or control characters") {
		t.Fatalf("hidden note: %d %s", code, body)
	}
	if code, _ := pay("not an object"); code != http.StatusBadRequest {
		t.Fatalf("a malformed body: %d", code)
	}
	if w := payDo(o.r, http.MethodPost, "/api/pay/public/open/no-such-link/payments", map[string]any{"amount": 450_000}, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown slug: %d", w.Code)
	}
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("status", models.PayMerchantSuspended)
	if code, body := pay(map[string]any{"amount": 450_000}); code != http.StatusConflict || !strings.Contains(body, `"code":"link_closed"`) {
		t.Fatalf("suspended: %d %s", code, body)
	}
	w = payDo(o.r, http.MethodGet, "/api/pay/public/open/"+l.Slug, nil, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"active":false`) {
		t.Fatalf("suspended public read: %d %s", w.Code, w.Body.String())
	}
}

// An edit sends only what changed: resuming a link leaves its name and range.
func TestPayOpenLinks_AnEditSendsOnlyWhatChanged(t *testing.T) {
	o := newOwnerRig(t)
	l := o.newOpenLink(t, map[string]any{"label": "Shop 12", "minAmount": 200_000})
	res := o.openLink(http.MethodPut, "/api/pay/merchant/open-links/"+l.ID.String()+"?mode=test", map[string]any{"active": false, "expectedVersion": 1})
	if res.status != http.StatusOK || res.Link.Label != "Shop 12" || res.Link.MinAmount != 200_000 || res.Link.Active {
		t.Fatalf("pause: %d %s", res.status, res.raw)
	}
}

// The merchant API cancels only what the merchant's server created: not an
// owner's link, nor a customer's open-link payment.
func TestPayOpenLinks_TheMerchantAPICannotCancelThem(t *testing.T) {
	o := newOwnerRig(t)
	l := o.newOpenLink(t, map[string]any{"label": "Shop 12"})
	w := payDo(o.r, http.MethodPost, "/api/pay/public/open/"+l.Slug+"/payments", map[string]any{"amount": 450_000}, nil)
	var started struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &started)
	link := o.link(http.MethodPost, "/api/pay/merchant/links?mode=test", linkBody())

	r := gin.New()
	r.POST("/pay/v1/payments/:id/cancel", func(c *gin.Context) {
		c.Set("payMerchant", o.m)
		c.Set("payMode", models.PayModeTest)
	}, o.h.CancelPayment)
	for name, id := range map[string]uuid.UUID{"open payment": started.Data.ID, "link": link.Data.ID} {
		if w := payDo(r, http.MethodPost, "/pay/v1/payments/"+id.String()+"/cancel", nil, nil); w.Code != http.StatusConflict {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
}

// paymentExpiresIn goes in and comes back by that name, on create and edit;
// a value outside 15 minutes to 24 hours is refused with the payer-safe text.
func TestPayOpenLinks_PaymentExpiryRoundTrips(t *testing.T) {
	o := newOwnerRig(t)
	l := o.newOpenLink(t, map[string]any{"label": "Till 2", "paymentExpiresIn": 1800})
	if l.PaymentExpiresIn != 1800 {
		t.Fatalf("create: paymentExpiresIn %d", l.PaymentExpiresIn)
	}
	up := o.openLink(http.MethodPut, "/api/pay/merchant/open-links/"+l.ID.String()+"?mode=test",
		map[string]any{"paymentExpiresIn": 0, "expectedVersion": l.Version})
	if up.status != http.StatusOK || up.Link.PaymentExpiresIn != 0 || !strings.Contains(up.raw, `"paymentExpiresIn":0`) {
		t.Fatalf("clear: %d %s", up.status, up.raw)
	}
	if res := o.openLink(http.MethodPost, "/api/pay/merchant/open-links?mode=test",
		map[string]any{"label": "Till 3", "paymentExpiresIn": 60}); res.status != http.StatusUnprocessableEntity ||
		res.Msg != "Payments can expire after 15 minutes to 24 hours, or never" {
		t.Fatalf("60 seconds: %d %s", res.status, res.raw)
	}
}
