package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
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

// M9: the intents list carries a converted buy's requested asset, so a list
// row shows what she receives rather than the USDC leg.
func TestListIntents_CarriesAConvertedBuysRequestedAsset(t *testing.T) {
	h, db, _ := newITBuyHandler(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	user := &models.User{ID: uuid.New()}
	expires := time.Now().Add(15 * time.Minute)
	newBuy := func() *models.ITIntent {
		i := &models.ITIntent{ID: uuid.New(), UserID: user.ID, Direction: models.ITDirectionBuy,
			Status: models.ITStatusSettled, Currency: "NGN", Provider: models.ITProviderTreasury,
			Asset: "USDC", AssetAmount: "7232937", DestChain: "BASE", DestAddress: "0x7777777777777777777777777777777777777777",
			NgnKobo: "1000000", AmountMinor: "1000000", LockedRate: "138257", DisplayRate: "138257", RateKobo: "138257",
			McgpAmount: "0", RateExpiresAt: &expires}
		if err := db.Create(i).Error; err != nil {
			t.Fatal(err)
		}
		return i
	}
	converted, direct := newBuy(), newBuy()
	if err := db.Create(&models.PaycrestOrderRecord{UserID: user.ID, Direction: models.PaycrestDirectionBuy,
		Status: models.PaycrestStatusConverting, Reference: services.ITConversionRef(converted.ID),
		RequestedToken: "USDT", RequestedChain: "optimism"}).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", user) })
	r.GET("/intents", h.ListIntents)
	w := walletDo(r, "GET", "/intents", nil)
	var env struct {
		Data struct {
			Intents []map[string]any `json:"intents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || w.Code != 200 || len(env.Data.Intents) != 2 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, row := range env.Data.Intents {
		switch row["id"] {
		case converted.ID.String():
			if row["requestedToken"] != "USDT" || row["requestedChain"] != "optimism" || row["asset"] != "USDC" ||
				row["conversionStatus"] != models.PaycrestStatusConverting {
				t.Fatalf("converted row = %v", row)
			}
		case direct.ID.String():
			if _, ok := row["requestedToken"]; ok {
				t.Fatalf("direct row = %v", row)
			}
		default:
			t.Fatalf("row = %v", row)
		}
	}
}
