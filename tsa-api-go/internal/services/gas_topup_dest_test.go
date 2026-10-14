package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// gas_topup_dest_test.go — Sonic gas for a marketplace checkout whose funding
// swap has landed: she holds the order token but none of the S her approve +
// createOrder need. Same gates as a sale's source-chain top-up, on the
// destination chain, and a refusal of ours fails closed with a sentence.

type fakeDestTopUps struct {
	today    int64
	claimed  map[uuid.UUID]bool
	marks    int
	cleared  int
	recorded map[uuid.UUID][2]string
}

func (f *fakeDestTopUps) CountDestToppedUpSince(context.Context, uuid.UUID, time.Time) (int64, error) {
	return f.today, nil
}
func (f *fakeDestTopUps) MarkDestToppedUp(_ context.Context, id uuid.UUID, _ time.Time) (bool, error) {
	f.marks++
	if f.claimed == nil {
		f.claimed = map[uuid.UUID]bool{}
	}
	if f.claimed[id] {
		return false, nil
	}
	f.claimed[id] = true
	return true, nil
}
func (f *fakeDestTopUps) ClearDestToppedUp(_ context.Context, id uuid.UUID) error {
	f.cleared++
	delete(f.claimed, id)
	return nil
}
func (f *fakeDestTopUps) RecordDestTopUp(_ context.Context, id uuid.UUID, tx, wei string) error {
	if f.recorded == nil {
		f.recorded = map[uuid.UUID][2]string{}
	}
	f.recorded[id] = [2]string{tx, wei}
	return nil
}

func (f *fakeDestTopUps) ClearCancelledDestTopUp(_ context.Context, id uuid.UUID, txHash string) (bool, error) {
	if f.recorded[id][0] != txHash {
		return false, nil
	}
	delete(f.recorded, id)
	delete(f.claimed, id)
	f.cleared++
	return true, nil
}

const destNeedWei = "400000000000000" // 0.0004 S, under the rig lane's 0.001 ceiling

func newDestTopUpRig(t *testing.T) (*GasTopUp, *models.User, *sponsorRig) {
	t.Helper()
	rig, _ := newSponsorRig("sonic", "USDC")
	g := NewGasTopUp(rig.orders, rig.store, map[string]*SponsorLane{"sonic": rig.lane}, 2, time.Second,
		func() time.Time { return sponsorTestNow })
	g.SetChainRegistry(fundingChainRegistry())
	user := &models.User{ID: uuid.New(), WalletAddress: sponsorTestUser, VerificationStatus: models.VerificationStatusVerified}
	rig.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	return g, user, rig
}

func deliveredFunding(user *models.User) *models.RampSellIntent {
	return &models.RampSellIntent{ID: uuid.New(), UserID: user.ID, Purpose: models.SellIntentPurposeMarketplace,
		WalletAddress: user.WalletAddress, DestAddress: user.WalletAddress, DestChainID: 146,
		DestTokenAddress: fundingTestSonicUSDC, Status: models.SellIntentDelivered,
		DestTopUpBudgeted: true, DestGasNeedWei: destNeedWei, DestTopUpMaxWei: destNeedWei}
}

func TestTopUpDest_SendsTheShortfallOnce(t *testing.T) {
	g, user, rig := newDestTopUpRig(t)
	store := &fakeDestTopUps{}
	in := deliveredFunding(user)
	rig.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(100_000_000_000_000) // holds 0.0001
	res, err := g.TopUpDestForIntent(context.Background(), user, in, store)
	if err != nil || res.Skipped || res.TxHash == "" {
		t.Fatalf("top-up: %+v %v", res, err)
	}
	if res.AmountWei != "300000000000000" || store.recorded[in.ID] != [2]string{res.TxHash, "300000000000000"} {
		t.Fatalf("sent %s (recorded %v), want only the 0.0003 S shortfall", res.AmountWei, store.recorded[in.ID])
	}
	if _, err := g.TopUpDestForIntent(context.Background(), user, in, store); !errors.Is(err, ErrSponsorshipInProgress) {
		t.Fatalf("second top-up for one checkout: err = %v", err)
	}
}

func TestTopUpDest_SkipsWhenSheHoldsEnough(t *testing.T) {
	g, user, rig := newDestTopUpRig(t)
	rig.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(1_000_000_000_000_000)
	store := &fakeDestTopUps{}
	res, err := g.TopUpDestForIntent(context.Background(), user, deliveredFunding(user), store)
	if err != nil || !res.Skipped || store.marks != 0 {
		t.Fatalf("res=%+v err=%v marks=%d — a funded wallet costs us nothing and claims nothing", res, err, store.marks)
	}
}

func TestTopUpDest_Gates(t *testing.T) {
	g, user, _ := newDestTopUpRig(t)
	for name, tc := range map[string]struct {
		user   func() *models.User
		intent func(*models.RampSellIntent)
		want   error
	}{
		"KYC":             {user: func() *models.User { u := *user; u.VerificationStatus = "pending"; return &u }, want: ErrKYCRequired},
		"before delivery": {intent: func(in *models.RampSellIntent) { in.Status = models.SellIntentSwapped }, want: ErrSellIntentState},
		"not a checkout":  {intent: func(in *models.RampSellIntent) { in.Purpose = "" }, want: ErrSellIntentState},
		"another wallet":  {intent: func(in *models.RampSellIntent) { in.DestAddress = sellOtherAddress }, want: ErrSponsorWallet},
		"not budgeted":    {intent: func(in *models.RampSellIntent) { in.DestTopUpBudgeted = false }, want: ErrSponsorNotAvailable},
		"no lane":         {intent: func(in *models.RampSellIntent) { in.DestChainID = 56 }, want: ErrDestGasUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			u := user
			if tc.user != nil {
				u = tc.user()
			}
			in := deliveredFunding(user)
			if tc.intent != nil {
				tc.intent(in)
			}
			store := &fakeDestTopUps{}
			if _, err := g.TopUpDestForIntent(context.Background(), u, in, store); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if store.marks != 0 {
				t.Fatal("a refused top-up must not take the checkout's one claim")
			}
		})
	}
}

// The treasury's S float under its floor, or her daily cap, fails CLOSED with
// the sentence that tells her what to do — her funds are in her wallet either
// way — and gives the claim back.
func TestTopUpDest_FailsClosedWithASentence(t *testing.T) {
	t.Run("float under floor", func(t *testing.T) {
		g, user, rig := newDestTopUpRig(t)
		rig.chain.balances[strings.ToLower(sponsorTestSigner)] = big.NewInt(10) // floor is 1000
		store := &fakeDestTopUps{}
		_, err := g.TopUpDestForIntent(context.Background(), user, deliveredFunding(user), store)
		if !errors.Is(err, ErrDestGasUnavailable) || !errors.Is(err, ErrRelayerUnderfunded) {
			t.Fatalf("err = %v, want ErrDestGasUnavailable (underfunded)", err)
		}
		if len(store.claimed) != 0 {
			t.Fatal("nothing was sent, so the claim must be free for a retry")
		}
	})
	t.Run("daily cap", func(t *testing.T) {
		g, user, _ := newDestTopUpRig(t)
		store := &fakeDestTopUps{today: intentTopUpDailyCap}
		_, err := g.TopUpDestForIntent(context.Background(), user, deliveredFunding(user), store)
		if !errors.Is(err, ErrDestGasUnavailable) || !errors.Is(err, ErrTopUpDailyCap) {
			t.Fatalf("err = %v, want ErrDestGasUnavailable (daily cap)", err)
		}
	})
	t.Run("over the lane's ceiling", func(t *testing.T) {
		g, user, _ := newDestTopUpRig(t)
		in := deliveredFunding(user)
		in.DestGasNeedWei, in.DestTopUpMaxWei = "5000000000000000", "5000000000000000" // 0.005 > 0.001 ceiling
		if _, err := g.TopUpDestForIntent(context.Background(), user, in, &fakeDestTopUps{}); !errors.Is(err, ErrDestGasUnavailable) {
			t.Fatalf("err = %v, want ErrDestGasUnavailable", err)
		}
	})
	t.Run("sponsorship off", func(t *testing.T) {
		var off *RampSponsorship
		user := &models.User{ID: uuid.New(), WalletAddress: sponsorTestUser, VerificationStatus: models.VerificationStatusVerified}
		if _, err := off.TopUpDestForIntent(context.Background(), user, deliveredFunding(user), &fakeDestTopUps{}); !errors.Is(err, ErrDestGasUnavailable) {
			t.Fatalf("err = %v", err)
		}
		if off.CanSponsorChain(146) {
			t.Fatal("no sponsorship can sponsor nothing")
		}
	})
}

// What is sent is never more than the shortfall the quote priced: a balance
// she spent down since is not ours to refill.
func TestTopUpDest_SendsAtMostThePricedShortfall(t *testing.T) {
	g, user, rig := newDestTopUpRig(t)
	in := deliveredFunding(user)
	in.DestTopUpMaxWei = "250000000000000"                               // priced when she held 0.00015 S
	rig.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0) // spent it since
	res, err := g.TopUpDestForIntent(context.Background(), user, in, &fakeDestTopUps{})
	if err != nil || res.AmountWei != "250000000000000" {
		t.Fatalf("sent %s (%v), want the priced 0.00025 S, not the full 0.0004 now missing", res.AmountWei, err)
	}
	in2 := deliveredFunding(user)
	in2.DestTopUpMaxWei = ""
	if _, err := g.TopUpDestForIntent(context.Background(), user, in2, &fakeDestTopUps{}); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("no priced figure: err = %v, want refused", err)
	}
}

// CheckTopUp answers, without sending or reserving anything, what the send
// would be refused for: no lane, over the ceiling, float under its floor.
func TestTopUpDest_CheckTopUpBeforeSigning(t *testing.T) {
	g, _, rig := newDestTopUpRig(t)
	ctx := context.Background()
	if err := g.CheckTopUp(ctx, 146, big.NewInt(400_000_000_000_000)); err != nil {
		t.Fatalf("within limits: %v", err)
	}
	if err := g.CheckTopUp(ctx, 146, big.NewInt(2_000_000_000_000_000)); !errors.Is(err, ErrTopUpTooExpensive) {
		t.Fatalf("over the lane ceiling: %v", err)
	}
	if err := g.CheckTopUp(ctx, 56, big.NewInt(1)); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("no lane: %v", err)
	}
	rig.chain.balances[strings.ToLower(sponsorTestSigner)] = big.NewInt(10)
	if err := g.CheckTopUp(ctx, 146, big.NewInt(1)); !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("float under floor: %v", err)
	}
	if rig.signer.broadcastCalls != 0 {
		t.Fatal("a check must never send")
	}
}
