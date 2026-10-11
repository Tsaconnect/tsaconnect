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

// fakeP2PGasNeed answers the state check; every request is needed unless err.
type fakeP2PGasNeed struct {
	err   error
	calls int
	last  P2PGasRequest
}

func (f *fakeP2PGasNeed) RequireP2PGasNeed(_ context.Context, _ *models.User, _ string, req P2PGasRequest) error {
	f.calls++
	f.last = req
	return f.err
}

type p2pGasRig struct {
	*sponsorRig
	g     *P2PGasSponsor
	need  *fakeP2PGasNeed
	clock *time.Time
}

func newP2PGasRig(t *testing.T, perDay int) *p2pGasRig {
	t.Helper()
	r, _ := newSponsorRig("sonic", "USDT")
	clock := sponsorTestNow
	now := func() time.Time { return clock }
	sp := NewRampSponsorship(nil, r.store, r.lanes(), 2, time.Second, now)
	need := &fakeP2PGasNeed{}
	return &p2pGasRig{sponsorRig: r, g: NewP2PGasSponsor(sp, need, perDay, now), need: need, clock: &clock}
}

func (r *p2pGasRig) topUp(a P2PGasAction) (GasTopUpResult, error) {
	return r.g.TopUp(context.Background(), topUpUser(r.sponsorRig), sponsorTestUser, P2PGasRequest{Action: a})
}

func (r *p2pGasRig) setUserBalance(wei int64) {
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(wei)
}

func p2pActionGasForTest(a P2PGasAction) int64 { return int64(p2pActionGas[a]) }

// what the action needs at the rig's 1 gwei, with the 2x buffer
func p2pNeedForTest(a P2PGasAction) int64 { return 2 * p2pActionGasForTest(a) * 1_000_000_000 }

func TestP2PGasTopUp_SendsShortfallForTheAction(t *testing.T) {
	r := newP2PGasRig(t, 6)
	r.setUserBalance(0)
	got, err := r.topUp(P2PGasMarkPaid)
	want := new(big.Int).Mul(big.NewInt(2*p2pActionGasForTest(P2PGasMarkPaid)), big.NewInt(1_000_000_000)) // units x 1 gwei x buffer 2
	if err != nil || got.Skipped || got.TxHash != "0xsponsored" || got.AmountWei != want.String() {
		t.Fatalf("got %+v err %v want %s", got, err, want)
	}
	if !strings.EqualFold(r.native.sentTo, sponsorTestUser) {
		t.Fatalf("sent to %s", r.native.sentTo)
	}
	if r.need.calls != 1 || r.need.last.Action != P2PGasMarkPaid {
		t.Fatalf("need check = %d %+v", r.need.calls, r.need.last)
	}
}

func TestP2PGasTopUp_SkipsWhenWalletCovers(t *testing.T) {
	r := newP2PGasRig(t, 6)
	r.setUserBalance(1_000_000_000_000_000)
	if got, err := r.topUp(P2PGasRelease); err != nil || !got.Skipped || r.signer.broadcastCalls != 0 {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestP2PGasTopUp_DailyCapPerUser(t *testing.T) {
	r := newP2PGasRig(t, 1)
	r.setUserBalance(0)
	if _, err := r.topUp(P2PGasOpenTrade); err != nil {
		t.Fatal(err)
	}
	if _, err := r.topUp(P2PGasMarkPaid); !errors.Is(err, ErrTopUpDailyCap) {
		t.Fatalf("err = %v", err)
	}
}

// A skip or a refusal of ours sends nothing, so it must not spend one of her
// daily slots.
func TestP2PGasTopUp_SkipDoesNotSpendASlot(t *testing.T) {
	r := newP2PGasRig(t, 1)
	r.setUserBalance(1_000_000_000_000_000)
	if got, err := r.topUp(P2PGasApprove); err != nil || !got.Skipped {
		t.Fatalf("got %+v err %v", got, err)
	}
	r.setUserBalance(0)
	if got, err := r.topUp(P2PGasDeposit); err != nil || got.Skipped {
		t.Fatalf("got %+v err %v", got, err)
	}
}

// The float under its floor is ours, not hers: it reads as "gas unavailable",
// and the slot she was admitted on is handed back.
func TestP2PGasTopUp_UnderfundedSponsorIsUnavailableAndFreesTheSlot(t *testing.T) {
	r := newP2PGasRig(t, 1)
	r.setUserBalance(0)
	r.chain.balances[strings.ToLower(sponsorTestSigner)] = big.NewInt(1)
	if _, err := r.topUp(P2PGasRelease); !errors.Is(err, ErrP2PGasUnavailable) || !errors.Is(gasUnavailableCause(err), ErrRelayerUnderfunded) {
		t.Fatalf("err = %v", err)
	}
	delete(r.chain.balances, strings.ToLower(sponsorTestSigner))
	if got, err := r.topUp(P2PGasRelease); err != nil || got.Skipped {
		t.Fatalf("slot not freed: %+v %v", got, err)
	}
}

func TestP2PGasTopUp_Refusals(t *testing.T) {
	r := newP2PGasRig(t, 6)
	u := topUpUser(r.sponsorRig)
	u.VerificationStatus = models.VerificationStatusPending
	if _, err := r.g.TopUp(context.Background(), u, sponsorTestUser, P2PGasRequest{Action: P2PGasMarkPaid}); !errors.Is(err, ErrKYCRequired) {
		t.Fatalf("kyc: %v", err)
	}
	if _, err := r.g.TopUp(context.Background(), topUpUser(r.sponsorRig), "0x000000000000000000000000000000000000dEaD", P2PGasRequest{Action: P2PGasMarkPaid}); !errors.Is(err, ErrSponsorWallet) {
		t.Fatalf("foreign wallet: %v", err)
	}
	if _, err := r.topUp(P2PGasAction("mint")); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("unknown action: %v", err)
	}
	if r.need.calls != 0 {
		t.Fatalf("state checked before the cheap gates: %d", r.need.calls)
	}
	var none *P2PGasSponsor = NewP2PGasSponsor(nil, &fakeP2PGasNeed{}, 6, nil)
	if _, err := none.TopUp(context.Background(), topUpUser(r.sponsorRig), sponsorTestUser, P2PGasRequest{Action: P2PGasMarkPaid}); !errors.Is(err, ErrP2PGasUnavailable) {
		t.Fatalf("dormant: %v", err)
	}
	if NewP2PGasSponsor(NewRampSponsorship(nil, r.store, r.lanes(), 2, time.Second, nil), nil, 6, nil) != nil {
		t.Fatal("no state check must leave the sponsor dormant, never sponsoring blind")
	}
	r.lane.Guard.SetKillSwitch(true)
	if _, err := r.topUp(P2PGasMarkPaid); !errors.Is(err, ErrP2PGasUnavailable) {
		t.Fatalf("killed lane: %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatalf("a refusal broadcast %d txs", r.signer.broadcastCalls)
	}
}

// The state check's answer decides: not her turn is gas_not_needed, a bad id
// is invalid, and a read that failed is ours (unavailable). Nothing is sent.
func TestP2PGasTopUp_StateCheckDecides(t *testing.T) {
	r := newP2PGasRig(t, 6)
	r.setUserBalance(0)
	for _, tc := range []struct {
		err  error
		want error
	}{
		{ErrP2PGasNotNeeded, ErrP2PGasNotNeeded},
		{ErrInvalidParams, ErrInvalidParams},
		{errors.New("db down"), ErrP2PGasUnavailable},
	} {
		r.need.err = tc.err
		if _, err := r.topUp(P2PGasRelease); !errors.Is(err, tc.want) {
			t.Fatalf("need %v -> %v, want %v", tc.err, err, tc.want)
		}
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatalf("a refused state check sent %d txs", r.signer.broadcastCalls)
	}
}

// A drip she has not spent blocks the next one for an hour: her balance is
// still at what the last drip brought it to, so no tx has been paid since.
func TestP2PGasTopUp_UnusedDripBlocksTheNext(t *testing.T) {
	r := newP2PGasRig(t, 6)
	r.setUserBalance(0)
	if _, err := r.topUp(P2PGasMarkPaid); err != nil {
		t.Fatal(err)
	}
	r.setUserBalance(p2pNeedForTest(P2PGasMarkPaid)) // the drip landed; nothing spent
	if _, err := r.topUp(P2PGasOpenTrade); !errors.Is(err, ErrP2PGasUnused) {
		t.Fatalf("unspent drip: %v", err)
	}
	// What she already holds is still a skip, not a refusal.
	if got, err := r.topUp(P2PGasMarkPaid); err != nil || !got.Skipped {
		t.Fatalf("covered action: %+v %v", got, err)
	}
	r.setUserBalance(p2pNeedForTest(P2PGasMarkPaid) - 47_000_000_000_000) // she paid a markPaid
	if got, err := r.topUp(P2PGasOpenTrade); err != nil || got.Skipped {
		t.Fatalf("spent drip must allow the next: %+v %v", got, err)
	}
}

func TestP2PGasTopUp_UnusedDripExpiresAfterAnHour(t *testing.T) {
	r := newP2PGasRig(t, 6)
	r.setUserBalance(0)
	if _, err := r.topUp(P2PGasMarkPaid); err != nil {
		t.Fatal(err)
	}
	r.setUserBalance(p2pNeedForTest(P2PGasMarkPaid))
	*r.clock = r.clock.Add(time.Hour)
	if got, err := r.topUp(P2PGasOpenTrade); err != nil || got.Skipped {
		t.Fatalf("after an hour: %+v %v", got, err)
	}
}

// One request in flight per user: a second one while the first holds the
// slot is "already sending", and the slot is free once it returns.
func TestP2PGasTopUp_OneInFlightPerUser(t *testing.T) {
	r := newP2PGasRig(t, 6)
	r.setUserBalance(0)
	u := topUpUser(r.sponsorRig)
	if !r.g.enter(u.ID) {
		t.Fatal("first enter refused")
	}
	if _, err := r.topUp(P2PGasMarkPaid); !errors.Is(err, ErrSponsorshipInProgress) {
		t.Fatalf("concurrent: %v", err)
	}
	r.g.leave(u.ID)
	if _, err := r.topUp(P2PGasMarkPaid); err != nil {
		t.Fatalf("after leave: %v", err)
	}
	if _, busy := r.g.inFlight[u.ID]; busy {
		t.Fatal("TopUp must release its in-flight slot")
	}
}

// Our failures all read as gas_unavailable; an unconfirmed send keeps its hash.
func TestP2PGasTopUp_FailuresAreUnavailable(t *testing.T) {
	r := newP2PGasRig(t, 6)
	r.setUserBalance(0)
	r.native.receiptErr = errors.New("receipt timeout")
	got, err := r.topUp(P2PGasMarkPaid)
	if !errors.Is(err, ErrP2PGasUnavailable) || !errors.Is(gasUnavailableCause(err), ErrTopUpUnconfirmed) || got.TxHash != "0xsponsored" {
		t.Fatalf("unconfirmed: %+v %v", got, err)
	}

	r = newP2PGasRig(t, 6)
	r.setUserBalance(0)
	r.native.receiptStatus = 0
	if _, err := r.topUp(P2PGasMarkPaid); !errors.Is(err, ErrP2PGasUnavailable) || !errors.Is(gasUnavailableCause(err), ErrTopUpFailed) {
		t.Fatalf("reverted: %v", err)
	}

	r = newP2PGasRig(t, 6)
	r.chain.balanceErr = errors.New("rpc down")
	if _, err := r.topUp(P2PGasMarkPaid); !errors.Is(err, ErrP2PGasUnavailable) {
		t.Fatalf("balance read: %v", err)
	}
}

func gasUnavailableCause(err error) error {
	var u gasUnavailableError
	if errors.As(err, &u) {
		return u.cause
	}
	return nil
}

// An unavailable error keeps its cause for the log but never matches it: a
// failed read whose cause is a caller-refusal sentinel must still read as
// ours (gas_unavailable), whatever order the handler checks in.
func TestP2PGasTopUp_UnavailableNeverMatchesItsCause(t *testing.T) {
	for _, cause := range []error{ErrKYCRequired, ErrSponsorWallet, ErrTopUpDailyCap, ErrP2PGasUnused} {
		r := newP2PGasRig(t, 6)
		r.setUserBalance(0)
		r.need.err = fmt.Errorf("lookup: %w", cause)
		_, err := r.topUp(P2PGasRelease)
		if !errors.Is(err, ErrP2PGasUnavailable) || errors.Is(err, cause) {
			t.Fatalf("cause %v -> %v: want unavailable only", cause, err)
		}
		if !errors.Is(gasUnavailableCause(err), cause) || !strings.Contains(err.Error(), cause.Error()) {
			t.Fatalf("cause %v lost from %v", cause, err)
		}
	}
}

func TestParseP2PGasAction(t *testing.T) {
	for _, s := range []string{"approve", "deposit", "withdraw", "cancel_ad", "open_trade", "mark_paid", "release", "cancel_trade", "dispute"} {
		a, ok := ParseP2PGasAction(s)
		if !ok || string(a) != s || p2pActionGas[a] == 0 {
			t.Fatalf("%q -> %q %v", s, a, ok)
		}
	}
	if _, ok := ParseP2PGasAction(" MARK_PAID "); !ok {
		t.Fatal("case and spaces must not matter")
	}
	if _, ok := ParseP2PGasAction("mint"); ok {
		t.Fatal("unknown action accepted")
	}
}
