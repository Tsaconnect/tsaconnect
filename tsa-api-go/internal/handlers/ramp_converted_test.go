package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/services/ramp"
)

func TestRampConvertedQuote_AuthenticatedAndCarriesTheTarget(t *testing.T) {
	f := &fakeRampRoutes{quote: &ramp.QuoteResult{Venues: []ramp.Venue{}}}
	if w := rampDoJSON(rampTestServer(t, f, nil), "GET", "/api/ramp/converted-quote?amount=10000&requestedChainId=10&requestedTokenAddress=0xOpt&requestedToken=USDT", nil); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	user := rampUser()
	w := rampDoJSON(rampTestServer(t, f, user), "GET",
		"/api/ramp/converted-quote?amount=10000&currency=NGN&requestedChainId=10&requestedTokenAddress=0xOpt&requestedToken=USDT&requestedChain=optimism&destinationAddress=SoLDest", nil)
	want := f.quoteReq.Want
	if w.Code != 200 || want == nil || want.ChainID != 10 || want.Address != "0xOpt" || want.Token != "USDT" ||
		want.Chain != "optimism" || want.Destination != "SoLDest" || want.Recipient != user.WalletAddress ||
		f.quoteReq.UserID != user.ID || f.quoteReq.Side != ramp.Buy || f.quoteReq.FiatAmount != "10000" {
		t.Fatalf("%d %s req=%+v", w.Code, w.Body, f.quoteReq)
	}
}

func TestRampConvertedQuote_RefusesABadRequest(t *testing.T) {
	for _, q := range []string{
		"requestedChainId=10&requestedToken=USDT",                         // no amount
		"amount=1000&currency=N1&requestedChainId=10&requestedToken=USDT", // bad currency
	} {
		w := rampDoJSON(rampTestServer(t, &fakeRampRoutes{}, rampUser()), "GET", "/api/ramp/converted-quote?"+q, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
	}
}

func TestRampBuy_ConvertedBodyWithACompanyWalletHandleOpensConverted(t *testing.T) {
	intent := &models.ITIntent{ID: uuid.New(), Direction: models.ITDirectionBuy, Status: models.ITStatusPendingPayment,
		Provider: models.ITProviderTreasury, Asset: "USDC", AssetAmount: "7232937", DestChain: "BASE",
		DestAddress: "0x7777777777777777777777777777777777777777", AmountMinor: "1000000", LockedRate: "138257", DisplayRate: "138257"}
	rec := &models.PaycrestOrderRecord{ID: uuid.New(), Status: models.PaycrestStatusAwaitingPayment, Holder: models.HolderCompanyWallet,
		RequestedToken: "USDT", RequestedChain: "optimism", RequestedChainID: 10, RequestedTokenDecimals: 6,
		EstimatedAmount: "7.19", PromisedAmount: "7.05", MarkupBps: 50, WalletAddress: "0xAbC0000000000000000000000000000000000001"}
	f := &fakeRampRoutes{order: &ramp.Order{Provider: ramp.ProviderTreasury, IT: intent, Conversion: rec}}
	user := rampUser()
	body := rampBuyJSON()
	body["requestedChainId"], body["requestedTokenAddress"], body["requestedToken"], body["requestedChain"] = 10, "0xOpt", "USDT", "optimism"
	body["handle"] = ramp.EncodeHandle(ramp.ProviderTreasury, ramp.VenueKey{ChainKey: "base", Token: "USDC"})
	w := rampDoJSON(rampTestServer(t, f, user), "POST", "/api/ramp/buy", body)
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	r := f.convertedReq
	if r.Request.Want == nil || r.Request.Want.ChainID != 10 || r.Request.Want.Address != "0xOpt" || r.Request.Want.Chain != "optimism" ||
		r.Request.Want.Recipient != user.WalletAddress || r.Request.UserID != user.ID || r.Request.Side != ramp.Buy ||
		r.Handle != body["handle"] || r.QuotedRate != "1397.59" || r.Input.WalletAddress != user.WalletAddress || r.Input.RefundAccount != "0123456789" {
		t.Fatalf("converted request = %+v want=%+v", r, r.Request.Want)
	}
	if f.openReq.Handle != "" {
		t.Fatal("a converted body must not open a direct buy")
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			ID            string         `json:"id"`
			WalletAddress string         `json:"walletAddress"`
			Conversion    map[string]any `json:"conversion"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	cv := env.Data.Conversion
	if !env.Success || env.Data.ID != intent.ID.String() || env.Data.WalletAddress != rec.WalletAddress {
		t.Fatalf("view = %s", w.Body)
	}
	// The app's ITConversion (tsa-app services/itApi.ts) reads exactly these.
	for k, v := range map[string]any{
		"orderId": rec.ID.String(), "status": rec.Status, "holder": models.HolderCompanyWallet,
		"requestedToken": "USDT", "requestedChain": "optimism", "requestedChainId": float64(10),
		"requestedTokenDecimals": float64(6), "estimatedAmount": "7.19", "promisedAmount": "7.05",
		"markupBps": float64(50), "walletAddress": rec.WalletAddress,
	} {
		if cv[k] != v {
			t.Fatalf("conversion.%s = %v, want %v (%s)", k, cv[k], v, w.Body)
		}
	}
	fee, _ := cv["fee"].(map[string]any)
	if fee == nil || fee["label"] != services.ConversionFeeLabel || fee["units"] == nil || fee["ngnMinor"] == nil {
		t.Fatalf("conversion.fee = %v", cv["fee"])
	}
}

// A direct IT order carries no conversion, and its wallet is still its own
// destination.
func TestRampOrderView_DirectITOrderHasNoConversion(t *testing.T) {
	intent := &models.ITIntent{ID: uuid.New(), Direction: models.ITDirectionBuy, Status: models.ITStatusPendingPayment,
		Provider: models.ITProviderTreasury, Asset: "USDC", AssetAmount: "7232937", DestChain: "BASE",
		DestAddress: "0xAbC0000000000000000000000000000000000001", AmountMinor: "1000000", LockedRate: "138257"}
	b, _ := json.Marshal(rampOrderView(&ramp.Order{Provider: ramp.ProviderTreasury, IT: intent}))
	if bytes.Contains(b, []byte(`"conversion"`)) || !bytes.Contains(b, []byte(intent.DestAddress)) {
		t.Fatalf("view = %s", b)
	}
}

func TestRampBuy_ConvertedBodyWithAPaycrestHandleStillSaysUsePaycrestRoute(t *testing.T) {
	for name, handle := range map[string]string{
		"paycrest": ramp.EncodeHandle(ramp.ProviderPaycrest, ramp.VenueKey{ChainKey: "base", Token: "USDC"}),
		"none":     "",
		"garbage":  "!!",
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRampRoutes{}
			body := rampBuyJSON()
			body["requestedChainId"] = 10
			body["handle"] = handle
			w := rampDoJSON(rampTestServer(t, f, rampUser()), "POST", "/api/ramp/buy", body)
			if w.Code != 400 || !bytes.Contains(w.Body.Bytes(), []byte("use_paycrest_route")) || f.convertedReq.Request.Want != nil {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		})
	}
}

// N2 and N4 refusals reach the user with their own codes and no provider name.
func TestRampError_ConvertedRefusals(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("open: %w", services.ErrStableBuyAbandonCapReached), http.StatusTooManyRequests, rampAbandonCapCode},
		{fmt.Errorf("claim: %w", services.ErrConvertedOverpaid), http.StatusBadRequest, "converted_overpaid"},
	} {
		f := &fakeRampRoutes{openErr: tc.err}
		body := rampBuyJSON()
		body["requestedChainId"] = 10
		body["handle"] = ramp.EncodeHandle(ramp.ProviderTreasury, ramp.VenueKey{ChainKey: "base", Token: "USDC"})
		w := rampDoJSON(rampTestServer(t, f, rampUser()), "POST", "/api/ramp/buy", body)
		var env struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if w.Code != tc.status || env.Success || env.Code != tc.code || env.Message == "" ||
			bytes.Contains(bytes.ToLower(w.Body.Bytes()), []byte("paycrest")) || bytes.Contains(bytes.ToLower(w.Body.Bytes()), []byte("flutterwave")) {
			t.Fatalf("%v: %d %s", tc.err, w.Code, w.Body)
		}
	}
}

// The IT endpoints answer the same N2 refusal with the same code.
func TestStableErrorMapping_AbandonCap(t *testing.T) {
	err := fmt.Errorf("x: %w", services.ErrStableBuyAbandonCapReached)
	if stableStatusFor(err) != http.StatusTooManyRequests || stableCodeFor(err) != rampAbandonCapCode {
		t.Fatalf("status=%d code=%q", stableStatusFor(err), stableCodeFor(err))
	}
}

// GET /api/it/intents/:id and check-payment carry the conversion beside the
// intent: what the app's intent screen reads (spec §15.8).
func TestGetIntent_CarriesTheConversion(t *testing.T) {
	h, db, _ := newITBuyHandler(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xAbC0000000000000000000000000000000000001"}
	expires := time.Now().Add(15 * time.Minute)
	intent := &models.ITIntent{ID: uuid.New(), UserID: user.ID, Direction: models.ITDirectionBuy,
		Status: models.ITStatusProcessing, Currency: "NGN", Provider: models.ITProviderTreasury,
		Asset: "USDC", AssetAmount: "7232937", DestChain: "BASE", DestAddress: "0x7777777777777777777777777777777777777777",
		NgnKobo: "1000000", AmountMinor: "1000000", LockedRate: "138257", DisplayRate: "138257", RateKobo: "138257",
		McgpAmount: "0", RateExpiresAt: &expires}
	if err := db.Create(intent).Error; err != nil {
		t.Fatal(err)
	}
	rec := &models.PaycrestOrderRecord{ID: uuid.New(), UserID: user.ID, Status: models.PaycrestStatusPending,
		Reference: services.ITConversionRef(intent.ID), RouteKind: models.RouteKindConverted, Holder: models.HolderCompanyWallet,
		RequestedToken: "USDT", RequestedChain: "optimism", RequestedChainID: 10, MarkupBps: 50,
		WalletAddress: user.WalletAddress}
	if err := db.Create(rec).Error; err != nil {
		t.Fatal(err)
	}

	check := func(name string, data map[string]any) {
		t.Helper()
		cv, _ := data["conversion"].(map[string]any)
		fee, _ := cv["fee"].(map[string]any)
		if cv == nil || cv["orderId"] != rec.ID.String() || cv["requestedToken"] != "USDT" || cv["walletAddress"] != user.WalletAddress ||
			fee == nil || fee["label"] != "Conversion (provider)" || data["intent"] == nil {
			t.Fatalf("%s: data = %v", name, data)
		}
	}
	check("get", doITGetIntent(t, h, user, intent.ID.String()))
	check("check-payment", itBuyData(t, doITIntentAction(t, h, user, intent.ID.String(), h.PostCheckBuyPayment)))

	// A direct routed buy has no record: no conversion key at all.
	direct := *intent
	direct.ID = uuid.New()
	if err := db.Create(&direct).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := doITGetIntent(t, h, user, direct.ID.String())["conversion"]; ok {
		t.Fatal("a direct buy carries a conversion")
	}
}
