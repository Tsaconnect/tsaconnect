package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

const (
	sellCapSigner     = "0x00000000000000000000000000000000000000Aa"
	sellCapRegistered = "0x00000000000000000000000000000000000000Bb"
)

// fakeOTCPositions records which wallet was asked about and how fresh.
type fakeOTCPositions struct {
	sellable map[string]*big.Int // lower-cased wallet -> sellable
	err      error
	asked    []string
	maxAges  []time.Duration
}

func (f *fakeOTCPositions) Position(_ context.Context, wallet string, maxAge time.Duration) (*services.OTCPosition, error) {
	f.asked = append(f.asked, wallet)
	f.maxAges = append(f.maxAges, maxAge)
	if f.err != nil {
		return nil, f.err
	}
	s := f.sellable[strings.ToLower(wallet)]
	if s == nil {
		s = new(big.Int)
	}
	return &services.OTCPosition{
		Wallet: wallet, Bought: s, Sold: new(big.Int), NetPurchased: s,
		WalletBalance: s, Sellable: s, AsOfBlock: 42,
	}, nil
}

func mcgpWei(whole int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(whole), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

func newSellCapHandler(t *testing.T, db *gorm.DB, positions otcPositionReader) *SwapHandler {
	t.Helper()
	cfg := &config.Config{OTCMarketplaceAddress: "0x00000000000000000000000000000000000000c0"}
	// No chain client: anything that passes the cap then fails at pricing,
	// which is how the tests tell "refused by the cap" from "let through".
	h := NewSwapHandler(cfg, services.NewOTCService(nil, cfg), services.NewBlockchainService(cfg), db, nil)
	h.positions = positions
	return h
}

func setOTCSellEnabled(t *testing.T, db *gorm.DB, enabled bool) {
	t.Helper()
	row, err := loadOrSeedOTCSettings(db)
	if err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := db.Model(row).Updates(map[string]interface{}{"sell_enabled": enabled}).Error; err != nil {
		t.Fatalf("set sell_enabled: %v", err)
	}
}

func prepareSell(t *testing.T, h *SwapHandler, body map[string]interface{}) (*httptest.ResponseRecorder, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/swap/prepare", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", &models.User{ID: uuid.New(), WalletAddress: sellCapRegistered})
	h.PrepareSwap(c)
	return w, w.Body.String()
}

func TestPrepareSwapSell_RefusesAboveOTCPurchases(t *testing.T) {
	db := setupOTCSettingsTestDB(t)
	positions := &fakeOTCPositions{sellable: map[string]*big.Int{strings.ToLower(sellCapSigner): mcgpWei(70)}}
	h := newSellCapHandler(t, db, positions)

	w, body := prepareSell(t, h, map[string]interface{}{
		"direction": "sell", "mcgpAmount": mcgpWei(71).String(), "fromAddress": sellCapSigner,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s, want 422", w.Code, body)
	}
	if !strings.Contains(body, "up to 70 MCGP") {
		t.Fatalf("message should name the sellable amount: %s", body)
	}
	// The cap is the SIGNING wallet's, read fresh — never the registered one.
	if len(positions.asked) != 1 || positions.asked[0] != sellCapSigner || positions.maxAges[0] != 0 {
		t.Fatalf("asked=%v maxAges=%v, want one fresh read of the signer", positions.asked, positions.maxAges)
	}
}

func TestPrepareSwapSell_ZeroPurchasesExplainsWhy(t *testing.T) {
	db := setupOTCSettingsTestDB(t)
	h := newSellCapHandler(t, db, &fakeOTCPositions{})

	w, body := prepareSell(t, h, map[string]interface{}{
		"direction": "sell", "mcgpAmount": "1", "fromAddress": sellCapSigner,
	})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "no MCGP bought on the OTC desk") {
		t.Fatalf("status=%d body=%s, want 422 no-purchases message", w.Code, body)
	}
}

func TestPrepareSwapSell_WithinCapPassesTheGate(t *testing.T) {
	db := setupOTCSettingsTestDB(t)
	positions := &fakeOTCPositions{sellable: map[string]*big.Int{strings.ToLower(sellCapRegistered): mcgpWei(70)}}
	h := newSellCapHandler(t, db, positions)

	// No fromAddress: the registered wallet signs, so its cap applies.
	w, body := prepareSell(t, h, map[string]interface{}{
		"direction": "sell", "mcgpAmount": mcgpWei(70).String(),
	})
	// Past the cap the handler goes on to price the sell, which fails here
	// only because the test has no chain client.
	if w.Code != http.StatusBadGateway || !strings.Contains(body, "failed to calculate sell price") {
		t.Fatalf("status=%d body=%s, want the sell to pass the cap and reach pricing", w.Code, body)
	}
	if len(positions.asked) != 1 || positions.asked[0] != sellCapRegistered {
		t.Fatalf("asked=%v, want the registered wallet", positions.asked)
	}
}

func TestPrepareSwapSell_KillSwitchRefusesServerSide(t *testing.T) {
	db := setupOTCSettingsTestDB(t)
	setOTCSellEnabled(t, db, false)
	positions := &fakeOTCPositions{sellable: map[string]*big.Int{strings.ToLower(sellCapSigner): mcgpWei(70)}}
	h := newSellCapHandler(t, db, positions)

	w, body := prepareSell(t, h, map[string]interface{}{
		"direction": "sell", "mcgpAmount": "1", "fromAddress": sellCapSigner,
	})
	if w.Code != http.StatusForbidden || !strings.Contains(body, "Selling is closed") {
		t.Fatalf("status=%d body=%s, want 403 selling closed", w.Code, body)
	}
	if len(positions.asked) != 0 {
		t.Fatalf("a paused desk should not read positions, asked=%v", positions.asked)
	}
}

func TestPrepareSwapSell_FailsClosed(t *testing.T) {
	cases := map[string]struct {
		db        func(t *testing.T) *gorm.DB
		positions otcPositionReader
	}{
		"logs unreadable": {
			db:        setupOTCSettingsTestDB,
			positions: &fakeOTCPositions{err: fmt.Errorf("%w: eth_getLogs: timeout", services.ErrOTCPositionUnavailable)},
		},
		"cap not configured": {
			db:        setupOTCSettingsTestDB,
			positions: nil,
		},
		"settings unreadable": {
			db:        func(*testing.T) *gorm.DB { return nil },
			positions: &fakeOTCPositions{sellable: map[string]*big.Int{strings.ToLower(sellCapSigner): mcgpWei(70)}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newSellCapHandler(t, tc.db(t), tc.positions)
			w, body := prepareSell(t, h, map[string]interface{}{
				"direction": "sell", "mcgpAmount": "1", "fromAddress": sellCapSigner,
			})
			if w.Code != http.StatusServiceUnavailable || !strings.Contains(body, "try again") {
				t.Fatalf("status=%d body=%s, want 503 try-again", w.Code, body)
			}
		})
	}
}

func TestPrepareSwapBuy_NotGatedBySellCap(t *testing.T) {
	db := setupOTCSettingsTestDB(t)
	setOTCSellEnabled(t, db, false)
	positions := &fakeOTCPositions{err: errors.New("must not be called")}
	h := newSellCapHandler(t, db, positions)

	w, body := prepareSell(t, h, map[string]interface{}{"direction": "buy", "mcgpAmount": "1"})
	if w.Code == http.StatusForbidden || w.Code == http.StatusUnprocessableEntity || len(positions.asked) != 0 {
		t.Fatalf("buy hit the sell cap: status=%d body=%s asked=%v", w.Code, body, positions.asked)
	}
}

func TestGetOTCPosition_ReturnsSellable(t *testing.T) {
	newCallerWalletTestDB(t) // callerWallet reconciles against config.DB
	positions := &fakeOTCPositions{sellable: map[string]*big.Int{strings.ToLower(sellCapSigner): new(big.Int).Add(mcgpWei(5), big.NewInt(2e17))}}
	h := newSellCapHandler(t, nil, positions)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/otc/position?walletAddress="+sellCapSigner, nil)
	c.Set("user", &models.User{ID: uuid.New(), WalletAddress: sellCapSigner})
	h.GetOTCPosition(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data["sellable"] != "5200000000000000000" || resp.Data["sellableFormatted"] != "5.2" {
		t.Fatalf("data=%v, want sellable 5.2 MCGP", resp.Data)
	}
	if positions.maxAges[0] != otcPositionDisplayMaxAge {
		t.Fatalf("display read maxAge=%v, want %v", positions.maxAges[0], otcPositionDisplayMaxAge)
	}
}

func TestGetOTCPosition_UnreadableIs503(t *testing.T) {
	newCallerWalletTestDB(t)
	h := newSellCapHandler(t, nil, &fakeOTCPositions{err: services.ErrOTCPositionUnavailable})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/otc/position?walletAddress="+sellCapSigner, nil)
	c.Set("user", &models.User{ID: uuid.New(), WalletAddress: sellCapSigner})
	h.GetOTCPosition(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", w.Code, w.Body.String())
	}
}
