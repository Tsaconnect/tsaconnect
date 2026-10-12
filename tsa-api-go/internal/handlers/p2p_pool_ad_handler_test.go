package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type openGate struct{}

func (openGate) Open() bool { return true }

// Review I1: an LP (role user) may publish its stock ad on the pool chain and
// nothing on an ordinary chain.
func TestCreateAdHandler_LPAdmittedOnlyOnPoolChain(t *testing.T) {
	db := setupP2PTestDB(t)
	reg := services.NewP2PEscrowRegistryFromEntries(map[int64]services.P2PRegistryEntry{
		146: {Escrow: newMockEscrow(), ChainKey: "sonic", Purpose: config.P2PPurposeP2P, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 6}},
		97:  {Escrow: newMockEscrow(), ChainKey: "bsc", Purpose: config.P2PPurposePool, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 18}},
	}, "2")
	trade := services.NewP2PTradeService(db, reg, &mockRates{}, &mockBus{}, 146)
	trade.SetPMService(services.NewPMService(db))
	trade.SetPoolGate(openGate{})
	trade.SetTokenResolver(func(chainID int64, sym string) string {
		return map[int64]string{146: "0x29219dd400f2Bf60E5a23d13Be72B486D4038894", 97: "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7"}[chainID]
	})
	h := NewP2PHandler(db, trade, nil, nil, nil)
	wallet := "0x52908400098527886E0F7030069857D2E4169EE7"
	user := &models.User{ID: uuid.New(), Role: models.RoleUser, VerificationStatus: "verified"}
	chain := int64(97)
	// This DDL predates some PaymentMerchant columns, so insert only the ones the LP lookup needs.
	if err := db.Select("ID", "UserID", "Name", "Kind", "PoolChainID", "Status", "OperatorWalletAddress").
		Create(&models.PaymentMerchant{ID: uuid.New(), UserID: user.ID, Name: "Ada", Kind: models.PaymentMerchantKindLP, PoolChainID: &chain,
			Status: models.PaymentMerchantStatusActive, OperatorWalletAddress: wallet}).Error; err != nil {
		t.Fatalf("seed lp: %v", err)
	}

	post := func(chainID int64) int {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("user", user); c.Next() })
		r.POST("/p2p/ads", h.CreateAd)
		b, _ := json.Marshal(map[string]any{"token": "USDT", "tokenAmount": "1", "fiatCurrency": "NGN", "rate": "1500",
			"sellerAddress": wallet, "chainId": chainID, "paymentMethodIds": []string{}})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/p2p/ads", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code
	}
	// Off the pool chain the merchant/LP rule no longer applies (any verified
	// user may post); this payload has no payment method, so it is refused by
	// validation (400), not by role (403).
	if code := post(146); code == http.StatusForbidden {
		t.Fatalf("ordinary Sonic ad refused by role = %d, want no role gate", code)
	}
	if code := post(0); code == http.StatusForbidden {
		t.Fatalf("ad on the default chain refused by role = %d, want no role gate", code)
	}
	// A verified non-merchant, non-LP user stays refused on the pool chain.
	user.Role = models.RoleUser
	if err := db.Exec("UPDATE payment_merchants SET status = 'suspended'").Error; err != nil {
		t.Fatal(err)
	}
	if code := post(97); code != http.StatusForbidden {
		t.Fatalf("non-LP user on the pool chain = %d, want 403", code)
	}
	if err := db.Exec("UPDATE payment_merchants SET status = ?", models.PaymentMerchantStatusActive).Error; err != nil {
		t.Fatal(err)
	}
	if code := post(97); code != http.StatusCreated {
		t.Fatalf("LP stock ad on the pool chain = %d, want 201", code)
	}
}

func TestMapServiceError_PoolStockAdExistsIs409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	mapServiceError(c, services.ErrPoolStockAdExists)
	if w.Code != http.StatusConflict {
		t.Fatalf("stock ad exists = %d, want 409", w.Code)
	}
}
