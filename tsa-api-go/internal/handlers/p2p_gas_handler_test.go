package handlers

import (
	"context"
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
	action services.P2PGasAction
}

func (f *fakeP2PGas) TopUp(_ context.Context, _ *models.User, w string, a services.P2PGasAction) (services.GasTopUpResult, error) {
	f.calls++
	f.wallet, f.action = w, a
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
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "mark_paid"})
	if rec.Code != http.StatusOK || decode(t, rec)["data"].(map[string]any)["txHash"] != "0xgas" ||
		f.wallet != r.user.WalletAddress || f.action != services.P2PGasMarkPaid {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "mint"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action = %d", rec.Code)
	}
	f.err = services.ErrP2PGasUnavailable
	rec = doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "release"})
	if rec.Code != http.StatusServiceUnavailable || decode(t, rec)["code"] != "gas_unavailable" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	f.err = services.ErrTopUpDailyCap
	if rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "release"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("cap = %d", rec.Code)
	}
	f.err = services.ErrSponsorshipInProgress
	rec = doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "release"})
	if rec.Code != http.StatusConflict || decode(t, rec)["code"] != sponsorshipInProgressCode {
		t.Fatalf("in progress = %d %s", rec.Code, rec.Body.String())
	}
	f.err = services.ErrKYCRequired
	rec = doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "release"})
	if rec.Code != http.StatusForbidden || decode(t, rec)["code"] != services.KYCVerificationRequiredCode {
		t.Fatalf("kyc = %d %s", rec.Code, rec.Body.String())
	}
}

func TestGasTopUp_RequiresProvenWallet(t *testing.T) {
	r, f := gasRig(t, false)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "mark_paid"})
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
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/gas/top-up", map[string]any{"action": "mark_paid"})
	if rec.Code != http.StatusServiceUnavailable || decode(t, rec)["code"] != "gas_unavailable" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
