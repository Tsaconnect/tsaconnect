// Tests for reconciling the DEVICE's wallet against the account's registered
// one on the MCGP entitlement endpoints.
//
// The bug these pin down is not a crash — it is a screen that lies. Before
// this, a device holding no wallet (or a different one) still got the
// registered address's premium balance back, so the app rendered MCGP the user
// had no key to spend. That reads as the platform custodying their MCGP, which
// is exactly what it is not. The prepare endpoints had the matching failure:
// they would build a tx for an address the device cannot sign for, which dies
// later at broadcast naming neither address.

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	registeredWallet = "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"
	otherWallet      = "0x09124a9E3c1B5AcF4207E33828195D00846E5EA8"
)

type walletRig struct {
	router *gin.Engine
	user   *models.User
}

func newWalletRig(t *testing.T, registered string) *walletRig {
	t.Helper()
	gin.SetMode(gin.TestMode)

	user := &models.User{
		ID:            uuid.New(),
		Username:      "holder",
		Name:          "Holder",
		WalletAddress: registered,
	}
	h := &MCGPSwapHandler{swap: &fakeSwapPort{}}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", user); c.Next() })
	r.GET("/balance", h.GetBalance)
	r.POST("/buy/prepare", h.PrepareBuy)
	r.POST("/sell/prepare", h.PrepareSell)

	return &walletRig{router: r, user: user}
}

func (wr *walletRig) get(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	wr.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (wr *walletRig) postJSON(t *testing.T, path string, body any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wr.router.ServeHTTP(w, req)
	return w.Code
}

// The whole point: a device that says which wallet it holds gets that wallet's
// balance, and the response names the address it read so the app can never
// display a number against the wrong wallet.
func TestGetBalance_MatchingDeviceWalletIsServed(t *testing.T) {
	wr := newWalletRig(t, registeredWallet)

	code, resp := wr.get(t, "/balance?address="+registeredWallet)

	if code != http.StatusOK {
		t.Fatalf("expected 200 for the registered wallet, got %d (%v)", code, resp)
	}
	data, _ := resp["data"].(map[string]any)
	if data["walletAddress"] != registeredWallet {
		t.Fatalf("response must name the address it read, got %v", data["walletAddress"])
	}
}

// The custody-looking bug. A device holding a different wallet must be told so,
// not silently handed the registered wallet's entitlement.
func TestGetBalance_DifferentDeviceWalletIsRejected(t *testing.T) {
	wr := newWalletRig(t, registeredWallet)

	code, resp := wr.get(t, "/balance?address="+otherWallet)

	if code != http.StatusConflict {
		t.Fatalf("a device on another wallet must get 409, got %d (%v)", code, resp)
	}
	if msg, _ := resp["message"].(string); !strings.Contains(msg, "different wallet") {
		t.Fatalf("message must explain the wallet conflict, got %q", msg)
	}
}

// Addresses arrive checksummed from some call sites and lowercased from others;
// that must never be mistaken for a different wallet.
func TestGetBalance_AddressComparisonIgnoresCasing(t *testing.T) {
	wr := newWalletRig(t, registeredWallet)

	code, _ := wr.get(t, "/balance?address="+strings.ToLower(registeredWallet))

	if code != http.StatusOK {
		t.Fatalf("same wallet in different casing must be accepted, got %d", code)
	}
}

func TestGetBalance_GarbageAddressIsRejected(t *testing.T) {
	wr := newWalletRig(t, registeredWallet)

	if code, _ := wr.get(t, "/balance?address=not-an-address"); code != http.StatusBadRequest {
		t.Fatalf("malformed address must be 400, got %d", code)
	}
}

// Older app builds send no address at all. They must keep working through the
// rollout rather than losing their balance screen mid-update.
func TestGetBalance_OmittedAddressFallsBackToTheRegisteredWallet(t *testing.T) {
	wr := newWalletRig(t, registeredWallet)

	code, resp := wr.get(t, "/balance")

	if code != http.StatusOK {
		t.Fatalf("a client that sends no address must still be served, got %d", code)
	}
	data, _ := resp["data"].(map[string]any)
	if data["walletAddress"] != registeredWallet {
		t.Fatalf("expected the registered wallet, got %v", data["walletAddress"])
	}
}

func TestGetBalance_AccountWithNoWalletIsRejected(t *testing.T) {
	wr := newWalletRig(t, "")

	if code, _ := wr.get(t, "/balance"); code != http.StatusBadRequest {
		t.Fatalf("an account with no registered wallet must be 400, got %d", code)
	}
}

// A prepare must never build a tx for an address the device has no key for —
// that tx can only fail at broadcast, long after the user has committed.
func TestPrepare_RefusesToBuildForAWalletTheDeviceDoesNotHold(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body map[string]any
	}{
		{"buy", "/buy/prepare", map[string]any{"mcgpAmount": "1000", "maxUsdc": "20000"}},
		{"sell", "/sell/prepare", map[string]any{"mcgpAmount": "1000", "minUsdc": "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wr := newWalletRig(t, registeredWallet)
			tc.body["fromAddress"] = otherWallet

			if code := wr.postJSON(t, tc.path, tc.body); code != http.StatusConflict {
				t.Fatalf("expected 409 before any tx is built, got %d", code)
			}
		})
	}
}

func TestPrepare_BuildsForTheRegisteredWallet(t *testing.T) {
	wr := newWalletRig(t, registeredWallet)

	code := wr.postJSON(t, "/buy/prepare", map[string]any{
		"mcgpAmount": "1000", "maxUsdc": "20000", "fromAddress": registeredWallet,
	})

	if code != http.StatusOK {
		t.Fatalf("the registered wallet must be able to prepare, got %d", code)
	}
}
