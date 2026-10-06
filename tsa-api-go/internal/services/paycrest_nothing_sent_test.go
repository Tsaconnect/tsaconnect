package services

import (
	"context"
	"errors"
	"testing"
)

// A bank-directory failure happens before any order request leaves us, so it
// is marked "nothing sent" — with its text unchanged for /api/paycrest/*.
func TestNothingSent_BankDirectoryFailure(t *testing.T) {
	svc := newSeamService(t, stubPaycrestOrderer{}, "buy", "1390.64")
	svc.dir = NewPaycrestDirectory(&stubInstitutions{err: errors.New("directory 503")}, &stubBankDir{})
	_, err := svc.CreateBuy(context.Background(), seamBuyRequest(seedVerifiedUser(t, svc.db)))
	if !errors.Is(err, ErrPaycrestNothingSent) || errors.Is(err, ErrOrderRequestSent) {
		t.Fatalf("err = %v, want nothing-sent and not request-sent", err)
	}
	if err.Error() != "paycrest directory: directory 503" {
		t.Fatalf("text changed: %q", err.Error())
	}
}

// A /v2/rates transport failure in VenueFor is the same: nothing sent.
func TestNothingSent_VenueForTransportFailure(t *testing.T) {
	base, _ := newPaycrestOrderTestService(t)
	quotes := NewPaycrestQuoteService(&stubQuoter{
		market:    1370.15,
		rates:     map[string]*PaycrestRates{"base|USDC": buy("1390.64")},
		transient: map[string]bool{"base|USDC": true},
	}, []string{"USDC"}, []string{"base"})
	svc := NewPaycrestOrderService(base.db, stubPaycrestOrderer{}, quotes, base.dir, "0.5", "")
	_, err := svc.CreateBuy(context.Background(), seamBuyRequest(seedVerifiedUser(t, svc.db)))
	if err == nil || !errors.Is(err, ErrPaycrestNothingSent) || errors.Is(err, ErrOrderRequestSent) {
		t.Fatalf("err = %v, want nothing-sent", err)
	}
}

// The marker never hides a sentinel the handler maps, and never stacks.
func TestNothingSent_KeepsSentinelsAndText(t *testing.T) {
	cause := errors.New("x")
	wrapped := nothingSent(errors.Join(ErrPaycrestLegClosed, cause))
	if !errors.Is(wrapped, ErrPaycrestLegClosed) || !errors.Is(wrapped, ErrPaycrestNothingSent) {
		t.Fatalf("lost a sentinel: %v", wrapped)
	}
	if again := nothingSent(wrapped); again != wrapped {
		t.Fatal("wrapped twice")
	}
	if nothingSent(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}
