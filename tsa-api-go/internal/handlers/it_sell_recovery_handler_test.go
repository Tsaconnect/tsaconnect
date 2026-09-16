package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// The sell response must say what actually happened to the user's MCGP: a
// payout the provider refused is not "Sell submitted".
func TestSellFailureResponse(t *testing.T) {
	cases := []struct {
		err      error
		status   int
		code     string
		msgHas   string
		accepted bool
	}{
		{fmt.Errorf("%w: HTTP 400", services.ErrITSellPayoutRejected), http.StatusUnprocessableEntity, "it_sell_payout_rejected", "returned", false},
		{fmt.Errorf("%w: receipt timeout", services.ErrITSellDebitPending), http.StatusAccepted, "it_sell_processing", "processing", true},
		{errors.New("insufficient spendable premium"), http.StatusBadRequest, "", "insufficient", false},
	}
	for _, tc := range cases {
		r := sellFailureResponse(tc.err, &models.ITIntent{Status: models.ITStatusReversed})
		if r.status != tc.status || r.code != tc.code || r.accepted != tc.accepted ||
			!strings.Contains(strings.ToLower(r.message), tc.msgHas) {
			t.Errorf("%v → %+v, want status=%d code=%q accepted=%v message containing %q", tc.err, r, tc.status, tc.code, tc.accepted, tc.msgHas)
		}
		if strings.Contains(r.message, "HTTP 400") || strings.Contains(r.message, "receipt timeout") {
			t.Errorf("%v leaks provider/chain detail to the user: %q", tc.err, r.message)
		}
	}
}

func TestAdminSellActionStatus(t *testing.T) {
	cases := map[error]int{
		fmt.Errorf("%w: transfer SUCCESSFUL", services.ErrITSellPayoutConflict): http.StatusConflict,
		fmt.Errorf("%w: HTTP 503", services.ErrITSellPayoutUnverified):          http.StatusServiceUnavailable,
		fmt.Errorf("%w: not an MCGP sell", services.ErrITBadState):              http.StatusBadRequest,
		fmt.Errorf("%w: HTTP 400", services.ErrITSellPayoutRejected):            http.StatusUnprocessableEntity,
	}
	for err, want := range cases {
		if got := adminSellActionStatus(err); got != want {
			t.Errorf("%v → %d, want %d", err, got, want)
		}
	}
}

// When the immediate reverse did not complete, the user must not be told the
// MCGP is already back.
func TestSellFailureResponse_ReturnInProgress(t *testing.T) {
	err := fmt.Errorf("%w: HTTP 400", services.ErrITSellPayoutRejected)
	pending := sellFailureResponse(err, &models.ITIntent{Status: models.ITStatusDebited})
	if strings.Contains(strings.ToLower(pending.message), "has been returned") || !strings.Contains(strings.ToLower(pending.message), "being returned") {
		t.Fatalf("message = %q, want a return-in-progress message", pending.message)
	}
	done := sellFailureResponse(err, &models.ITIntent{Status: models.ITStatusReversed})
	if !strings.Contains(strings.ToLower(done.message), "has been returned") {
		t.Fatalf("message = %q, want the completed-return message", done.message)
	}
}
