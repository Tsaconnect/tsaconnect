package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeP2PGas struct {
	res    services.GasTopUpResult
	err    error
	calls  int
	wallet string
	req    services.P2PGasRequest
}

func (f *fakeP2PGas) TopUp(_ context.Context, _ *models.User, w string, req services.P2PGasRequest) (services.GasTopUpResult, error) {
	f.calls++
	f.wallet, f.req = w, req
	return f.res, f.err
}

func gasRig(t *testing.T, proven bool) (*rig, *fakeP2PGas) {
	r := newRig(t)
	owner := r.user.ID
	if !proven {
		owner = uuid.New()
	}
	withProvenWalletDB(t, owner, r.user.WalletAddress)
	f := &fakeP2PGas{res: services.GasTopUpResult{TxHash: "0xgas", AmountWei: "160000000000000"}}
	r.handler.SetGasSponsor(f)
	r.router.POST("/api/p2p/gas/top-up", r.handler.GasTopUp)
	return r, f
}

func TestGasTopUp_Handler(t *testing.T) {
	r, f := gasRig(t, true)
	tradeID := uuid.New()
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "mark_paid", "tradeId": tradeID.String()})
	if rec.Code != http.StatusOK || decode(t, rec)["data"].(map[string]any)["txHash"] != "0xgas" ||
		f.wallet != r.user.WalletAddress || f.req.Action != services.P2PGasMarkPaid || f.req.TradeID != tradeID || f.req.AdID != uuid.Nil {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f.req)
	}
	adID := uuid.New()
	if rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "open_trade", "adId": adID.String()}); rec.Code != http.StatusOK || f.req.AdID != adID {
		t.Fatalf("adId: %d %+v", rec.Code, f.req)
	}
	calls := f.calls
	if rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "mint"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action = %d", rec.Code)
	}
	if rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "release", "tradeId": "not-a-uuid"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad tradeId = %d", rec.Code)
	}
	if rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "cancel_ad", "adId": "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad adId = %d", rec.Code)
	}
	if f.calls != calls {
		t.Fatal("a malformed request reached the sponsor")
	}
}

// Every refusal carries the contract's status and code.
func TestGasTopUp_ErrorContract(t *testing.T) {
	r, f := gasRig(t, true)
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unavailable", services.ErrP2PGasUnavailable, http.StatusServiceUnavailable, "gas_unavailable"},
		{"wrapped lane refusal", fmt.Errorf("%w (%w)", services.ErrP2PGasUnavailable, services.ErrRelayerUnderfunded), http.StatusServiceUnavailable, "gas_unavailable"},
		{"rpc failure", errors.New("sponsor: read user balance: dial tcp"), http.StatusServiceUnavailable, "gas_unavailable"},
		{"reverted", fmt.Errorf("%w (%w)", services.ErrP2PGasUnavailable, services.ErrTopUpFailed), http.StatusServiceUnavailable, "gas_unavailable"},
		{"not needed", services.ErrP2PGasNotNeeded, http.StatusConflict, "gas_not_needed"},
		{"unused", services.ErrP2PGasUnused, http.StatusConflict, "gas_topup_unused"},
		{"in progress", services.ErrSponsorshipInProgress, http.StatusConflict, sponsorshipInProgressCode},
		{"kyc", services.ErrKYCRequired, http.StatusForbidden, services.KYCVerificationRequiredCode},
		{"daily cap", services.ErrTopUpDailyCap, http.StatusTooManyRequests, ""},
		{"bad id", fmt.Errorf("%w: unknown tradeId", services.ErrInvalidParams), http.StatusBadRequest, ""},
	} {
		f.res, f.err = services.GasTopUpResult{}, tc.err
		rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "release", "tradeId": uuid.NewString()})
		body := decode(t, rec)
		code, _ := body["code"].(string)
		if rec.Code != tc.status || code != tc.code {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if body := decode(t, doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "release", "tradeId": uuid.NewString()})); body["message"] == "Already sending. Check the order in a moment." {
		t.Fatal("order wording on a P2P route")
	}

	// Sent but unconfirmed: still gas_unavailable, but with the hash to watch.
	f.res = services.GasTopUpResult{TxHash: "0xslow", AmountWei: "1"}
	f.err = fmt.Errorf("%w (%w)", services.ErrP2PGasUnavailable, services.ErrTopUpUnconfirmed)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "release", "tradeId": uuid.NewString()})
	body := decode(t, rec)
	data, _ := body["data"].(map[string]any)
	if rec.Code != http.StatusServiceUnavailable || body["code"] != "gas_unavailable" || data["txHash"] != "0xslow" {
		t.Fatalf("unconfirmed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGasTopUp_RequiresProvenWallet(t *testing.T) {
	r, f := gasRig(t, false)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "approve"})
	if rec.Code != http.StatusForbidden || decode(t, rec)["code"] != WalletProofRequiredCode || f.calls != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Without a sponsor (no key, or refused in production) the route still
// answers, with the code the app keys its "add a little S" copy on.
func TestGasTopUp_NoSponsorIsGasUnavailable(t *testing.T) {
	r := newRig(t)
	withProvenWalletDB(t, r.user.ID, r.user.WalletAddress)
	r.router.POST("/api/p2p/gas/top-up", r.handler.GasTopUp)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "approve"})
	if rec.Code != http.StatusServiceUnavailable || decode(t, rec)["code"] != "gas_unavailable" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
