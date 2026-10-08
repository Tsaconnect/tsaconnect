package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// poolAdHandlerRig: a handler over the BSC testnet pool chain (97) and an
// active LP whose stock ad carries a voucher signature.
func poolAdHandlerRig(t *testing.T) (*P2PHandler, *models.User, *models.P2PAd) {
	t.Helper()
	db := setupP2PTestDB(t)
	reg := services.NewP2PEscrowRegistryFromEntries(map[int64]services.P2PRegistryEntry{
		97: {Escrow: newMockEscrow(), ChainKey: "bsc", Purpose: config.P2PPurposePool, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 18}},
	}, "2")
	trade := services.NewP2PTradeService(db, reg, &mockRates{}, &mockBus{}, 146)
	trade.SetPMService(services.NewPMService(db))
	h := NewP2PHandler(db, trade, nil, nil, nil)

	owner := &models.User{ID: uuid.New(), VerificationStatus: "verified"}
	chain := int64(97)
	lp := &models.PaymentMerchant{ID: uuid.New(), UserID: owner.ID, Name: "Ada Obi", Kind: models.PaymentMerchantKindLP, PoolChainID: &chain, Status: models.PaymentMerchantStatusActive}
	// This DDL predates some PaymentMerchant columns, so insert only the ones the LP lookup needs.
	if err := db.Select("ID", "UserID", "Name", "Kind", "PoolChainID", "Status").Create(lp).Error; err != nil {
		t.Fatalf("seed lp: %v", err)
	}
	ad := &models.P2PAd{ID: uuid.New(), SellerID: owner.ID, Side: "sell", Token: "USDT", ChainID: 97, Amount: "1", Remaining: "1",
		Reserved: "0", FiatCurrency: "NGN", Rate: "0", RateType: "fixed", FloatAnchor: "bybit_sell", Status: "active", PMID: &lp.ID,
		VoucherSignature: "0xsecret"}
	if err := db.Create(ad).Error; err != nil {
		t.Fatalf("seed ad: %v", err)
	}
	return h, owner, ad
}

func servePoolAdRoute(u *models.User, route, url string, handle gin.HandlerFunc) (int, string) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", u); c.Next() })
	r.GET(route, handle)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", url, nil)
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func stranger() *models.User { return &models.User{ID: uuid.New(), VerificationStatus: "verified"} }

// Review Focus 1, through the route the app actually calls.
func TestGetAdHandler_PoolAdIs404ForOthers(t *testing.T) {
	h, owner, ad := poolAdHandlerRig(t)
	get := func(u *models.User) (int, string) {
		return servePoolAdRoute(u, "/p2p/ads/:id", "/p2p/ads/"+ad.ID.String(), h.GetAd)
	}
	code, body := get(stranger())
	if code != http.StatusNotFound {
		t.Fatalf("stranger = %d, want 404", code)
	}
	if strings.Contains(body, "0xsecret") {
		t.Fatal("the voucher signature reached a stranger")
	}
	code, body = get(owner)
	if code != http.StatusOK {
		t.Fatalf("owner = %d, want 200", code)
	}
	// Nobody pays into a stock ad, so it carries no PM pay-to card.
	if strings.Contains(body, `"isPm":true`) || strings.Contains(body, "pmPayTo") {
		t.Fatalf("owner's stock ad carries the PM pay-to enrichment: %s", body)
	}
}

// Review Focus 1: the browse and seller-profile routes miss the stock ad for
// everyone, its owner included; only My Ads lists it, to its owner.
func TestListAdsHandlers_PoolAdOnlyOnOwnersMyAds(t *testing.T) {
	h, owner, ad := poolAdHandlerRig(t)
	for name, u := range map[string]*models.User{"stranger": stranger(), "owner": owner} {
		code, body := servePoolAdRoute(u, "/p2p/ads", "/p2p/ads?sellerId="+owner.ID.String()+"&chainId=97", h.ListAds)
		if code != http.StatusOK || strings.Contains(body, ad.ID.String()) {
			t.Fatalf("browse for %s = %d, lists the stock ad: %v", name, code, strings.Contains(body, ad.ID.String()))
		}
	}
	code, body := servePoolAdRoute(stranger(), "/p2p/ads/my", "/p2p/ads/my", h.ListMyAds)
	if code != http.StatusOK || strings.Contains(body, ad.ID.String()) {
		t.Fatalf("a stranger's My Ads = %d, lists the stock ad", code)
	}
	code, body = servePoolAdRoute(owner, "/p2p/ads/my", "/p2p/ads/my", h.ListMyAds)
	if code != http.StatusOK || !strings.Contains(body, ad.ID.String()) {
		t.Fatalf("owner's My Ads = %d, missing the stock ad (renew or reclaim needs it)", code)
	}
}
