package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func attachHashServer(h *ITHandler, admin *models.User) *gin.Engine {
	r := gin.New()
	if admin != nil {
		r.Use(func(c *gin.Context) { c.Set("user", admin) })
	}
	r.POST("/stable/buy/:id/conversion-hash", h.AdminAttachConversionHash)
	return r
}

func TestAdminAttachConversionHash_Refusals(t *testing.T) {
	h, db, _ := newITBuyHandler(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	admin := &models.User{ID: uuid.New()}
	expires := time.Now().Add(15 * time.Minute)
	direct := &models.ITIntent{ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionBuy,
		Status: models.ITStatusSettled, Currency: "NGN", Provider: models.ITProviderFlutterwave,
		Asset: "USDC", AssetAmount: "94537500", DestChain: "POLYGON", DestAddress: "0x000000000000000000000000000000000000dEaD",
		NgnKobo: "16502500", AmountMinor: "16502500", LockedRate: "174561", DisplayRate: "169257", RateKobo: "174561",
		McgpAmount: "0", RateExpiresAt: &expires}
	if err := db.Create(direct).Error; err != nil {
		t.Fatal(err)
	}
	hash := "0x" + strings.Repeat("cd", 32)
	ok := map[string]string{"txHash": hash, "note": "from FW support"}
	for _, tc := range []struct {
		name   string
		admin  *models.User
		id     string
		body   any
		status int
		code   string
	}{
		{"anonymous", nil, direct.ID.String(), ok, 401, ""},
		{"bad id", admin, "not-a-uuid", ok, 404, "not_found"},
		{"no note", admin, direct.ID.String(), map[string]string{"txHash": hash}, 400, "note_required"},
		{"no hash", admin, direct.ID.String(), map[string]string{"note": "n"}, 400, "tx_hash_required"},
		{"unknown order", admin, uuid.NewString(), ok, 404, "not_found"},
		{"no conversion", admin, direct.ID.String(), ok, 409, "bad_state"},
	} {
		w := walletDo(attachHashServer(h, tc.admin), "POST", "/stable/buy/"+tc.id+"/conversion-hash", tc.body)
		var env struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if w.Code != tc.status || env.Code != tc.code {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
	}
}
