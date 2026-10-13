package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/services/ramp"
)

// End users never see third-party vendor names, in our copy or in server text
// the app renders. Operators (admin routes, logs) keep them.

var userBannedNames = []string{"lifi", "li.fi", "paycrest", "flutterwave", "clubkonnect", "nomba", "stargate", "across"}

func assertNoVendorName(t *testing.T, where, text string) {
	t.Helper()
	lower := strings.ToLower(text)
	for _, name := range userBannedNames {
		if strings.Contains(lower, name) {
			t.Fatalf("%s names %q: %s", where, name, text)
		}
	}
}

// ── swap proxy ───────────────────────────────────────────────────────────────

func TestSwapProxy_UpstreamErrorBodyIsNeverPassedThrough(t *testing.T) {
	cases := []struct {
		name, path string
		status     int
		body       string
		wantMsg    string
	}{
		{"quote no route", "/swap/lifi/quote?fromChain=1", http.StatusNotFound,
			`{"message":"No available quotes for the requested transfer via Stargate (LiFi)","code":1002}`, swapNoRouteMessage},
		{"quote not supported", "/swap/lifi/quote?fromChain=1", http.StatusBadRequest,
			`{"message":"Token pair not supported by LI.FI"}`, swapNotSupportedMessage},
		{"quote other", "/swap/lifi/quote?fromChain=1", http.StatusInternalServerError,
			`{"message":"LiFi internal error from Across"}`, "The swap provider returned an error (500). Try again shortly."},
		{"chains", "/swap/lifi/chains", http.StatusServiceUnavailable, `{"message":"LiFi is down"}`, ""},
		{"tokens", "/swap/lifi/tokens?chainId=1", http.StatusTooManyRequests, `{"message":"LiFi rate limit"}`, ""},
		{"token", "/swap/lifi/token?chain=1&token=0xabc", http.StatusNotFound, `{"message":"LiFi: token not found"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			w := httptest.NewRecorder()
			setupTestRouter(newTestLiFiHandler(srv.URL)).ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if w.Code != tc.status {
				t.Fatalf("status must be kept: want %d, got %d", tc.status, w.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("not our envelope: %s", w.Body.String())
			}
			if body["success"] != false {
				t.Fatalf("want success=false envelope, got %s", w.Body.String())
			}
			msg, _ := body["message"].(string)
			if msg == "" || (tc.wantMsg != "" && msg != tc.wantMsg) {
				t.Fatalf("message: want %q, got %q", tc.wantMsg, msg)
			}
			assertNoVendorName(t, "swap error", w.Body.String())
		})
	}
}

// The app keys its "no route" state on these phrases (screens/all-swap.tsx).
func TestSwapProxy_NoRouteMessagesKeepTheAppsPhrases(t *testing.T) {
	if !strings.Contains(strings.ToLower(swapNoRouteMessage), "no available quotes") {
		t.Fatalf("no-route message lost the app's phrase: %q", swapNoRouteMessage)
	}
	if !strings.Contains(strings.ToLower(swapNotSupportedMessage), "not supported") {
		t.Fatalf("not-supported message lost the app's phrase: %q", swapNotSupportedMessage)
	}
}

func TestSwapProxy_UnreachableMessagesAreNeutral(t *testing.T) {
	assertNoVendorName(t, "unreachable", swapProviderUnreachable)
	assertNoVendorName(t, "unreadable", swapProviderUnreadable)

	h := newTestLiFiHandler("http://127.0.0.1:1")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/swap/lifi/tokens/all", h.GetAllChainTokens)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/swap/lifi/tokens/all", nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", w.Code)
	}
	assertNoVendorName(t, "all-chain tokens", w.Body.String())
}

// ── ramp orders ──────────────────────────────────────────────────────────────

func TestOrderForCaller_ReplacesOperatorFailureReason(t *testing.T) {
	raw := "LiFi delivered USDC, not USDT (tx 0xdeadbeef)"
	plain := &models.PaycrestOrderRecord{ID: uuid.New(), Status: models.PaycrestStatusRecoveryPending, FailureReason: raw}
	payment := uuid.New()
	pay := &models.PaycrestOrderRecord{ID: uuid.New(), Status: models.PaycrestStatusRefunded, FailureReason: raw, PayPaymentID: &payment}

	views := map[string]*models.PaycrestOrderRecord{
		"orderForCaller (plain)": orderForCaller(plain),
		"orderForCaller (pay)":   orderForCaller(pay),
		"payOrderView":           payOrderView(pay),
	}
	list := ordersForCaller([]models.PaycrestOrderRecord{*plain, *pay})
	views["ordersForCaller[0]"], views["ordersForCaller[1]"] = &list[0], &list[1]

	for where, v := range views {
		if v.FailureReason == "" || v.FailureReason == raw {
			t.Fatalf("%s: want a user sentence, got %q", where, v.FailureReason)
		}
		b, _ := json.Marshal(v)
		assertNoVendorName(t, where, string(b))
		if strings.Contains(string(b), "0xdeadbeef") {
			t.Fatalf("%s leaks the tx hash: %s", where, b)
		}
	}
	// The stored record — what the DB and the admin views read — keeps the raw text.
	if plain.FailureReason != raw || pay.FailureReason != raw {
		t.Fatal("a user view changed the record it was handed")
	}
}

// ── bills ────────────────────────────────────────────────────────────────────

func TestBillRoutes_UserGetsNeutralFailureAdminGetsVendorRemark(t *testing.T) {
	remark := "ClubKonnect: INVALID METER NUMBER"
	order := models.BillOrder{ID: uuid.New(), Status: models.BillStatusVendFail, FailureReason: remark}
	svc := &fakeBillSvc{order: order}
	r := billTestRouter(svc, &models.User{ID: uuid.New(), WalletAddress: "0x1111111111111111111111111111111111111111"})

	for _, path := range []string{"/bills/orders/" + order.ID.String(), "/bills/orders"} {
		w, _ := billDo(t, r, http.MethodGet, path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		assertNoVendorName(t, path, w.Body.String())
		if strings.Contains(w.Body.String(), "INVALID METER") {
			t.Fatalf("%s passes the vendor remark: %s", path, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "couldn't complete this purchase") {
			t.Fatalf("%s: want the neutral sentence, got %s", path, w.Body.String())
		}
	}

	w, _ := billDo(t, r, http.MethodGet, "/admin/bills/reports", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), remark) {
		t.Fatalf("admin review must keep the vendor remark: %d %s", w.Code, w.Body.String())
	}
	if svc.order.FailureReason != remark {
		t.Fatal("a user view changed the stored order")
	}
}

// ── stablecoin ramp ──────────────────────────────────────────────────────────

func TestStableMessageFor_NeverEchoesTheError(t *testing.T) {
	leaky := []error{
		fmt.Errorf("%w: Flutterwave does not deliver USDC on BSC", services.ErrUnsupportedChainAsset),
		fmt.Errorf("%w: a routed Flutterwave buy delivers on Polygon only, not BSC", services.ErrUnsupportedChainAsset),
		fmt.Errorf("%w: POLYGON (no Flutterwave deposit address)", services.ErrStableNoDepositAddress),
		fmt.Errorf("%w: no Flutterwave rate source", services.ErrStableQuoteUnavailable),
		fmt.Errorf("it stable buy quote: %w", fmt.Errorf("%w: Paycrest says no", services.ErrStableRateImplausible)),
		fmt.Errorf("%w: LiFi hash 0xabc", services.ErrITBadState),
		errors.New("flutterwave: 500 internal error at https://api.flutterwave.com/v3/x"),
	}
	for _, err := range leaky {
		msg := stableMessageFor(err)
		if msg == "" || msg == err.Error() || strings.Contains(msg, err.Error()) {
			t.Fatalf("echoes the error: %q", msg)
		}
		assertNoVendorName(t, "stable message", msg)
	}
}

func TestStableMessageFor_KeepsOurOwnLimit(t *testing.T) {
	err := fmt.Errorf("%w: minimum is %s %s", services.ErrStableBuyBelowMinimum, "1,000.00", "NGN")
	if got := stableMessageFor(err); !strings.HasSuffix(got, "Minimum is 1,000.00 NGN.") {
		t.Fatalf("limit lost: %q", got)
	}
	err = fmt.Errorf("%w: maximum is 2000 USDC", services.ErrStableBuyAboveMaximum)
	if got := stableMessageFor(err); !strings.HasSuffix(got, "Maximum is 2000 USDC.") || stableStatusFor(err) != http.StatusBadRequest {
		t.Fatalf("max: %q %d", got, stableStatusFor(err))
	}
	// Any other detail on a limit error is dropped, not echoed.
	err = fmt.Errorf("%w: unusable amount %q", services.ErrStableBuyBelowMinimum, "abc")
	if got := stableMessageFor(err); strings.Contains(got, "abc") {
		t.Fatalf("non-limit detail echoed: %q", got)
	}
}

func TestStableFail_SendsFixedMessageWithStatusAndCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	err := fmt.Errorf("%w: POLYGON via Flutterwave", services.ErrStableNoDepositAddress)
	r.GET("/x", func(c *gin.Context) { stableFail(c, err) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != "stable_not_configured" {
		t.Fatalf("code: %v", body["code"])
	}
	assertNoVendorName(t, "stableFail", w.Body.String())
}

// §19: the pool's buyer-facing bodies never name a provider or carry an LP's
// voucher signature (S29, Plan B M10).
func TestProviderNames_PoolResponses(t *testing.T) {
	r := newRig(t)
	r.handler.SetPoolCard(stubPoolCard{card: &services.PoolCard{Enabled: true, Name: "TSA Connect", Badge: "Recommended",
		Available: true, Rate: "1361.51", FundingFeePercent: "1.4", MinNgn: "2762.00", MaxNgn: "171000.00",
		Token: &services.PoolCardToken{Symbol: "USDT", Address: "0x55d3", Decimals: 18},
		Chain: &services.PoolCardChain{ID: 56, Key: "bsc", Name: "BNB Smart Chain"}}})
	r.router.GET("/api/p2p/pool-card", r.handler.GetPoolCard)
	card := doJSON(t, r, "GET", "/api/p2p/pool-card?currency=NGN", nil).Body.String()
	assertNoVendorName(t, "pool card", card)

	// The venue's fee lines come from the REAL pool quote path (review I5):
	// QuotePoolBuy on central funding, rendered by ramp.VenueFees exactly as
	// the pool provider renders them. A hand-built venue would never see a
	// provider tag the pricing code adds.
	it := services.NewITService(nil, nil, nil)
	it.SetInboundCentral(true, services.CentralAccount{AccountNumber: "0123456789", BankName: "Test Bank", AccountName: "TSA Connect"})
	if err := it.SetRampBuyTerms(services.RampBuyTerms{FundingFeeBps: 140}); err != nil {
		t.Fatal(err)
	}
	p, err := it.QuotePoolBuy(context.Background(), "USDT", "100000", "NGN", big.NewRat(134800, 100), services.CurrentPoolSettings())
	if err != nil {
		t.Fatal(err)
	}
	fees := ramp.VenueFees(p.Quote.Fees)
	if len(fees) == 0 {
		t.Fatal("the pool quote has no fee lines")
	}
	f := &fakeRampRoutes{quote: &ramp.QuoteResult{Side: "buy", Venues: []ramp.Venue{{
		Handle: ramp.EncodeHandle(ramp.ProviderPool, ramp.VenueKey{ChainKey: "bsc", Token: "USDT"}), Provider: ramp.ProviderPool,
		ChainKey: "bsc", Token: "USDT", Rate: "1361.51", EffectiveRate: "1380.85", Fees: fees}}}}
	quote := rampDoJSON(rampTestServer(t, f, nil), "GET", "/api/ramp/quote?side=buy&amount=100000&pool=1", nil).Body.String()
	assertNoVendorName(t, "pool quote", quote)
	if !strings.Contains(quote, `"provider":"provider"`) {
		t.Fatalf("the funding line is not tagged \"provider\": %s", quote)
	}

	f.quoteErr = ramp.WrapClass(ramp.ErrUnavailable, ramp.WrapClass(ramp.ErrVenueUnavailable, services.ErrPoolCannotFill))
	refusal := rampDoJSON(rampTestServer(t, f, nil), "GET", "/api/ramp/quote?side=buy&amount=100000", nil).Body.String()
	assertNoVendorName(t, "pool refusal", refusal)
	for where, body := range map[string]string{"card": card, "quote": quote, "refusal": refusal} {
		if strings.Contains(strings.ToLower(body), "voucher") || strings.Contains(body, "tsa_pool") {
			t.Fatalf("%s leaks pool internals: %s", where, body)
		}
	}
}

// Owner rule (C1): her order view never names a provider either. The
// conversion line of a converted order is tagged "provider", as its label
// already says; the pool's own order is the case that must hold.
func TestProviderNames_OrderViewConversionLine(t *testing.T) {
	for _, tc := range []struct{ provider, holder, ramp string }{
		{models.ITProviderPool, models.HolderPool, ramp.ProviderPool},
		{models.ITProviderTreasury, models.HolderCompanyWallet, ramp.ProviderTreasury},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			intent := &models.ITIntent{ID: uuid.New(), Direction: models.ITDirectionBuy, Status: models.ITStatusPendingPayment,
				Provider: tc.provider, Asset: "USDT", AssetAmount: "7232937", DestChain: "BSC",
				DestAddress: "0x7777777777777777777777777777777777777777", AmountMinor: "1000000", LockedRate: "138257", DisplayRate: "138257"}
			rec := &models.PaycrestOrderRecord{ID: uuid.New(), Status: models.PaycrestStatusAwaitingPayment, Holder: tc.holder,
				RequestedToken: "USDT", RequestedChain: "optimism", RequestedChainID: 10, RequestedTokenDecimals: 6,
				EstimatedAmount: "7.19", PromisedAmount: "7.05", MarkupBps: 50, WalletAddress: "0xAbC0000000000000000000000000000000000001"}
			b, err := json.Marshal(rampOrderView(&ramp.Order{Provider: tc.ramp, IT: intent, Conversion: rec}))
			if err != nil {
				t.Fatal(err)
			}
			assertNoVendorName(t, "order view", string(b))
			if !strings.Contains(string(b), `"provider":"provider"`) {
				t.Fatalf("the conversion line is not tagged \"provider\": %s", b)
			}
		})
	}
}
