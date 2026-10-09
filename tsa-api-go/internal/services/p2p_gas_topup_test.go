package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func newP2PGasRig(t *testing.T, perDay int) (*sponsorRig, *P2PGasSponsor) {
	t.Helper()
	r, _ := newSponsorRig("sonic", "USDT")
	now := func() time.Time { return sponsorTestNow }
	sp := NewRampSponsorship(nil, r.store, r.lanes(), 2, time.Second, now)
	return r, NewP2PGasSponsor(sp, perDay, now)
}

func p2pActionGasForTest(a P2PGasAction) int64 { return int64(p2pActionGas[a]) }

func TestP2PGasTopUp_SendsShortfallForTheAction(t *testing.T) {
	r, g := newP2PGasRig(t, 6)
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	got, err := g.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasMarkPaid)
	want := new(big.Int).Mul(big.NewInt(2*p2pActionGasForTest(P2PGasMarkPaid)), big.NewInt(1_000_000_000)) // units x 1 gwei x buffer 2
	if err != nil || got.Skipped || got.TxHash != "0xsponsored" || got.AmountWei != want.String() {
		t.Fatalf("got %+v err %v want %s", got, err, want)
	}
	if !strings.EqualFold(r.native.sentTo, sponsorTestUser) {
		t.Fatalf("sent to %s", r.native.sentTo)
	}
}

func TestP2PGasTopUp_SkipsWhenWalletCovers(t *testing.T) {
	r, g := newP2PGasRig(t, 6)
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(1_000_000_000_000_000)
	if got, err := g.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasRelease); err != nil || !got.Skipped || r.signer.broadcastCalls != 0 {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestP2PGasTopUp_DailyCapPerUser(t *testing.T) {
	r, g := newP2PGasRig(t, 1)
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	if _, err := g.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasOpenTrade); err != nil {
		t.Fatal(err)
	}
	if _, err := g.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasMarkPaid); !errors.Is(err, ErrTopUpDailyCap) {
		t.Fatalf("err = %v", err)
	}
}

// A skip or a refusal of ours sends nothing, so it must not spend one of her
// daily slots.
func TestP2PGasTopUp_SkipDoesNotSpendASlot(t *testing.T) {
	r, g := newP2PGasRig(t, 1)
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(1_000_000_000_000_000)
	if got, err := g.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasApprove); err != nil || !got.Skipped {
		t.Fatalf("got %+v err %v", got, err)
	}
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	if got, err := g.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasDeposit); err != nil || got.Skipped {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestP2PGasTopUp_Refusals(t *testing.T) {
	r, g := newP2PGasRig(t, 6)
	u := topUpUser(r)
	u.VerificationStatus = models.VerificationStatusPending
	if _, err := g.TopUp(context.Background(), u, sponsorTestUser, P2PGasMarkPaid); !errors.Is(err, ErrKYCRequired) {
		t.Fatalf("kyc: %v", err)
	}
	if _, err := g.TopUp(context.Background(), topUpUser(r), "0x000000000000000000000000000000000000dEaD", P2PGasMarkPaid); !errors.Is(err, ErrSponsorWallet) {
		t.Fatalf("foreign wallet: %v", err)
	}
	if _, err := g.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasAction("mint")); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("unknown action: %v", err)
	}
	var none *P2PGasSponsor = NewP2PGasSponsor(nil, 6, nil)
	if _, err := none.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasMarkPaid); !errors.Is(err, ErrP2PGasUnavailable) {
		t.Fatalf("dormant: %v", err)
	}
	r.lane.Guard.SetKillSwitch(true)
	if _, err := g.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasMarkPaid); !errors.Is(err, ErrP2PGasUnavailable) {
		t.Fatalf("killed lane: %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatalf("a refusal broadcast %d txs", r.signer.broadcastCalls)
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

// The float under its floor is ours, not hers: it reads as "gas unavailable",
// and the slot she was admitted on is handed back.
func TestP2PGasTopUp_UnderfundedSponsorIsUnavailableAndFreesTheSlot(t *testing.T) {
	r, g := newP2PGasRig(t, 1)
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	r.chain.balances[strings.ToLower(sponsorTestSigner)] = big.NewInt(1)
	if _, err := g.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasRelease); !errors.Is(err, ErrP2PGasUnavailable) || !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("err = %v", err)
	}
	delete(r.chain.balances, strings.ToLower(sponsorTestSigner))
	if got, err := g.TopUp(context.Background(), topUpUser(r), sponsorTestUser, P2PGasRelease); err != nil || got.Skipped {
		t.Fatalf("slot not freed: %+v %v", got, err)
	}
}
