package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/services/ramp"
)

type stubPoolCard struct{ card *services.PoolCard }

func (s stubPoolCard) PoolCard(context.Context) (*services.PoolCard, error) { return s.card, nil }

func getPoolCard(t *testing.T, r *rig) map[string]any {
	t.Helper()
	r.router.GET("/api/p2p/pool-card", r.handler.GetPoolCard)
	w := doJSON(t, r, "GET", "/api/p2p/pool-card?currency=NGN", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Data
}

// P17: a dormant pool answers enabled:false (the app shows nothing).
func TestGetPoolCard_DormantIsDisabled(t *testing.T) {
	if d := getPoolCard(t, newRig(t)); d["enabled"] != false {
		t.Fatalf("dormant card = %v", d)
	}
}

func TestGetPoolCard_ServesTheCard(t *testing.T) {
	r := newRig(t)
	r.handler.SetPoolCard(stubPoolCard{card: &services.PoolCard{Enabled: true, Name: "TSA Connect", Badge: "Recommended",
		Available: true, Rate: "1361.51", FundingFeePercent: "1.4", MinNgn: "2762.00", MaxNgn: "171000.00",
		Token: &services.PoolCardToken{Symbol: "USDT", Address: "0x55d3", Decimals: 18},
		Chain: &services.PoolCardChain{ID: 56, Key: "bsc", Name: "BNB Smart Chain"}}})
	d := getPoolCard(t, r)
	if d["enabled"] != true || d["available"] != true || d["rate"] != "1361.51" || d["name"] != "TSA Connect" {
		t.Fatalf("card = %v", d)
	}
}

// S30: pool=1 reaches the router as a pool-only quote; poolAvailable comes back.
func TestRampQuote_PoolOnlyPassesThrough(t *testing.T) {
	avail := false
	f := &fakeRampRoutes{quote: &ramp.QuoteResult{Side: "buy", Venues: []ramp.Venue{{Handle: "h", ChainKey: "bsc", Token: "USDT", Rate: "1380"}}, PoolAvailable: &avail}}
	w := rampDoJSON(rampTestServer(t, f, nil), "GET", "/api/ramp/quote?side=buy&amount=5000&pool=1", nil)
	if w.Code != 200 || !f.quoteReq.PoolOnly || !strings.Contains(w.Body.String(), `"poolAvailable":false`) {
		t.Fatalf("status=%d poolOnly=%v body=%s", w.Code, f.quoteReq.PoolOnly, w.Body)
	}
	rampDoJSON(rampTestServer(t, f, nil), "GET", "/api/ramp/quote?side=buy&amount=5000", nil)
	if f.quoteReq.PoolOnly {
		t.Fatal("pool-only without pool=1")
	}
}

// §19: when the pool was the last venue to fail, the copy is the pool's.
func TestRampError_PoolCannotFillCopy(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	rampError(c, ramp.WrapClass(ramp.ErrUnavailable, ramp.WrapClass(ramp.ErrVenueUnavailable, services.ErrPoolCannotFill)))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), poolCannotFillMessage) || !strings.Contains(w.Body.String(), "pool_cannot_fill") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

// The pinned card's converted quote is pool-only too (S30).
func TestRampConvertedQuote_PoolOnlyPassesThrough(t *testing.T) {
	f := &fakeRampRoutes{quote: &ramp.QuoteResult{Venues: []ramp.Venue{}}}
	path := "/api/ramp/converted-quote?amount=10000&requestedChainId=10&requestedTokenAddress=0xOpt&requestedToken=USDT"
	if w := rampDoJSON(rampTestServer(t, f, rampUser()), "GET", path+"&pool=1", nil); w.Code != 200 || !f.quoteReq.PoolOnly {
		t.Fatalf("status=%d poolOnly=%v body=%s", w.Code, f.quoteReq.PoolOnly, w.Body)
	}
	rampDoJSON(rampTestServer(t, f, rampUser()), "GET", path, nil)
	if f.quoteReq.PoolOnly {
		t.Fatal("pool-only without pool=1")
	}
}

// The pool's daily cap reads as the instant-purchase limit, never the pool's name.
func TestRampError_PoolDailyCapCopy(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	rampError(c, ramp.WrapClass(ramp.ErrUnavailable, ramp.WrapClass(ramp.ErrVenueUnavailable, services.ErrPoolDailyCapReached)))
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), rampDailyLimitCode) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	assertNoVendorName(t, "pool daily cap", w.Body.String())
}

// No venue filled the buy without her BVN/NIN: the app gets the code that
// opens the form, not a generic outage.
func TestRampError_BuyerIDCodesWhenNoVenueFilled(t *testing.T) {
	for cause, code := range map[error]string{
		services.ErrBuyerIDRequired:        "buyer_id_required",
		services.ErrBuyerIDRejected:        "buyer_id_rejected",
		services.ErrRefundAccountNotBuyers: "refund_account_not_yours",
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		rampError(c, ramp.WrapClass(ramp.ErrUnavailable, ramp.WrapClass(ramp.ErrVenueUnavailable, cause)))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), code) {
			t.Fatalf("%s: status=%d body=%s", code, w.Code, w.Body)
		}
	}
}
