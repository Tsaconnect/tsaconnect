package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Task 15 review: what ConfirmDelivery answers for the receipts a REAL
// LogTransferVerifier produces, and what Create refuses before she signs.

// The real verifier never answers "credited zero": no matching Transfer log is
// ErrTransferNotFound, and a reverted destination transaction is
// ErrTransferReverted (see LogTransferVerifier.VerifyTransfer).
func TestSellIntent_ConfirmDeliveryTurnsTheRealVerifiersRefusalsIntoVerdicts(t *testing.T) {
	ctx := context.Background()
	t.Run("a reverted destination transaction is a mismatch at once", func(t *testing.T) {
		r, v, fees := newDeliveryRig(t)
		in := r.createDelivery(t)
		r.status.st = r.deliveredStatus(in)
		v.amount, v.err = nil, fmt.Errorf("%w: %s", ErrTransferReverted, deliveryDestTx)
		logs := captureLog(t)
		if _, err := r.svc.ConfirmDelivery(ctx, in.ID); !errors.Is(err, ErrSwapMismatch) {
			t.Fatalf("err = %v, want ErrSwapMismatch", err)
		}
		if !strings.Contains(logs.String(), "ALERT ramp_sell_delivery_reverted_total=1 intent="+in.ID.String()) {
			t.Fatalf("no ALERT in %q", logs.String())
		}
		if len(fees.events) != 0 {
			t.Fatal("a fee was booked for a delivery that did not happen")
		}
	})
	t.Run("no credit is retried inside the RPC-lag grace, then a mismatch", func(t *testing.T) {
		r, v, fees := newDeliveryRig(t)
		in := r.createDelivery(t)
		st := r.deliveredStatus(in)
		r.status.st = st
		v.amount, v.err = nil, fmt.Errorf("%w: token=%s to=%s tx=%s", ErrTransferNotFound, deliveryToken, deliveryMerch, deliveryDestTx)
		at := func(d time.Duration) time.Time { return sponsorTestNow.Add(d) }
		logs := captureLog(t)

		r.svc.now = func() time.Time { return at(0) } // first seen COMPLETED
		_, err := r.svc.ConfirmDelivery(ctx, in.ID)
		var nl *SwapNotLandedError
		if !errors.Is(err, ErrSwapNotLanded) || errors.Is(err, ErrSwapMismatch) || !errors.As(err, &nl) || nl.Status != st {
			t.Fatalf("first sight: err = %#v, want a retryable not-yet carrying the completed status", err)
		}
		r.svc.now = func() time.Time { return at(deliveryReceiptGrace - time.Second) }
		if _, err := r.svc.ConfirmDelivery(ctx, in.ID); !errors.Is(err, ErrSwapNotLanded) || errors.Is(err, ErrSwapMismatch) {
			t.Fatalf("one second inside the grace: err = %v", err)
		}
		r.svc.now = func() time.Time { return at(deliveryReceiptGrace) }
		if _, err := r.svc.ConfirmDelivery(ctx, in.ID); !errors.Is(err, ErrSwapMismatch) {
			t.Fatalf("at the grace: err = %v, want ErrSwapMismatch", err)
		}
		if !strings.Contains(logs.String(), "ALERT ramp_sell_delivery_no_credit_total=1 intent="+in.ID.String()) {
			t.Fatalf("no ALERT in %q", logs.String())
		}
		if len(fees.events) != 0 {
			t.Fatal("a fee was booked for a delivery that did not happen")
		}
	})
	t.Run("a credit that shows up inside the grace delivers", func(t *testing.T) {
		r, v, _ := newDeliveryRig(t)
		in := r.createDelivery(t)
		r.status.st = r.deliveredStatus(in)
		v.amount, v.err = nil, ErrTransferNotFound
		r.svc.now = func() time.Time { return sponsorTestNow }
		if _, err := r.svc.ConfirmDelivery(ctx, in.ID); !errors.Is(err, ErrSwapNotLanded) {
			t.Fatalf("err = %v", err)
		}
		v.amount, v.err = mustBig(t, deliveryOwed), nil
		r.svc.now = func() time.Time { return sponsorTestNow.Add(time.Minute) }
		if tx, err := r.svc.ConfirmDelivery(ctx, in.ID); err != nil || tx != deliveryDestTx {
			t.Fatalf("tx %q err %v", tx, err)
		}
	})
	t.Run("an unreadable chain is still a retry, never a verdict", func(t *testing.T) {
		r, v, _ := newDeliveryRig(t)
		in := r.createDelivery(t)
		r.status.st = r.deliveredStatus(in)
		v.amount, v.err = nil, errors.New("verify: not found")
		r.svc.now = func() time.Time { return sponsorTestNow.Add(48 * time.Hour) }
		if _, err := r.svc.ConfirmDelivery(ctx, in.ID); err == nil || errors.Is(err, ErrSwapMismatch) {
			t.Fatalf("err = %v", err)
		}
	})
}

// One destination transaction can prove one sale: a batched relayer transaction
// that credits the same merchant for two customers must not satisfy both.
func TestSellIntent_ADestinationTransactionProvesOnlyOneDelivery(t *testing.T) {
	ctx := context.Background()
	r, _, fees := newDeliveryRig(t)
	first := r.createDelivery(t)
	r.status.st = r.deliveredStatus(first)
	if _, err := r.svc.ConfirmDelivery(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	second, _, err := r.svc.Create(ctx, r.user, deliveryInput(r))
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, second.ID, testSwapHash2)
	r.status.st = r.deliveredStatus(second) // the very same destination transaction
	logs := captureLog(t)
	if _, err := r.svc.ConfirmDelivery(ctx, second.ID); !errors.Is(err, ErrSwapMismatch) {
		t.Fatalf("err = %v, want ErrSwapMismatch", err)
	}
	if !strings.Contains(logs.String(), "ALERT ramp_sell_delivery_tx_reused_total=1 intent="+second.ID.String()) {
		t.Fatalf("no ALERT in %q", logs.String())
	}
	if got := r.reload(t, second.ID); got.Status != models.SellIntentSwapped || len(fees.events) != 1 {
		t.Fatalf("second = %+v fees %d: the reused transaction delivered a second sale", got, len(fees.events))
	}
	// Re-proving the first is unaffected.
	r.status.st = r.deliveredStatus(first)
	if tx, err := r.svc.ConfirmDelivery(ctx, first.ID); err != nil || tx != deliveryDestTx {
		t.Fatalf("tx %q err %v", tx, err)
	}
}

// ---- I4: fail closed before she signs ----

func TestSellIntent_CreateDeliveryRefusesWhatCouldNeverBeProvenOrCoveredBeforeAnySwap(t *testing.T) {
	owedBig := func(t *testing.T) string { return deliveryOwed }
	for name, tc := range map[string]struct {
		setup func(t *testing.T, r *sellIntentRig)
		// quoted: whether the refusal may come after the quote (the route is what is wrong).
		quoted bool
	}{
		"no verifier for the chain": {func(t *testing.T, r *sellIntentRig) {
			r.svc.SetDestVerifiers(map[string]ERC20TransferVerifier{}, deliveryChainName)
		}, false},
		"verifiers never wired": {func(t *testing.T, r *sellIntentRig) { r.svc.SetDestVerifiers(nil, nil) }, false},
		"the chain resolves to no key": {func(t *testing.T, r *sellIntentRig) {
			r.svc.SetDestVerifiers(map[string]ERC20TransferVerifier{deliveryChainKy: &recordingVerifier{}}, func(int64) (string, bool) { return "", false })
		}, false},
		"the floor is one base unit under what is owed": {func(t *testing.T, r *sellIntentRig) {
			r.quoter.quote.Route.ToAmountMin = new(big.Int).Sub(mustBig(t, owedBig(t)), big.NewInt(1))
		}, true},
		"the route has no floor at all": {func(t *testing.T, r *sellIntentRig) { r.quoter.quote.Route.ToAmountMin = nil }, true},
		"the route carries no transaction id": {func(t *testing.T, r *sellIntentRig) {
			r.quoter.quote.Route.TransactionID = ""
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			r, _, _ := newDeliveryRig(t)
			tc.setup(t, r)
			if _, _, err := r.svc.Create(context.Background(), r.user, deliveryInput(r)); !errors.Is(err, ErrSellUnavailable) {
				t.Fatalf("err = %v, want ErrSellUnavailable", err)
			}
			if !tc.quoted && r.quoter.calls != 0 {
				t.Fatal("quoted a route nothing could confirm")
			}
			if open, _ := r.svc.Open(context.Background(), r.user.ID); open != nil {
				t.Fatalf("a refused sale left an intent behind: %+v", open)
			}
		})
	}
	t.Run("a floor exactly at what is owed is accepted", func(t *testing.T) {
		r, _, _ := newDeliveryRig(t)
		r.quoter.quote.Route.ToAmountMin = mustBig(t, owedBig(t))
		if _, _, err := r.svc.Create(context.Background(), r.user, deliveryInput(r)); err != nil {
			t.Fatal(err)
		}
	})
}

// An ordinary sale keeps its own rules: no transaction id is tolerated there.
func TestSellIntent_TheDeliveryChecksDoNotTouchAnOrdinarySale(t *testing.T) {
	r := newSellIntentRig(t)
	r.quoter.quote.Route.TransactionID = ""
	if _, _, err := r.svc.Create(context.Background(), r.user, CreateSellIntentInput{
		SourceChainID: 56, SourceToken: evmNativeAddress, SourceAmount: "16000000000000000",
		SourceAddress: r.user.WalletAddress, FiatCode: "NGN", BankCode: "044", AccountNumber: "0123456789"}); err != nil {
		t.Fatal(err)
	}
}

// ---- S3: our price against the route's own ----

func TestSellIntent_CreateDeliveryCrossChecksThePriceItSizedTheAmountFrom(t *testing.T) {
	for name, tc := range map[string]struct {
		routeUSD, expected float64
		ok                 bool
	}{
		"agree":                     {33.5, 33.5, true},
		"inside the band":           {33.5, 34.9, true}, // 4.2% apart
		"our price is far too low":  {33.5, 20, false},
		"our price is far too high": {33.5, 60, false},
		"the route names no value":  {0, 33.5, false},
		"no expectation given":      {33.5, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			r, _, _ := newDeliveryRig(t)
			r.quoter.quote.Route.ToAmountUSD = tc.routeUSD
			in := deliveryInput(r)
			in.ExpectedToUSD = tc.expected
			_, _, err := r.svc.Create(context.Background(), r.user, in)
			if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrSellUnavailable)) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
