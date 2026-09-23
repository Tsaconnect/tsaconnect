package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// gas_topup_intent_test.go — Task 14: a source-chain gas top-up for a sale.
//
// The property that matters most: the gas sent is the gas the QUOTE budgeted
// (RampSellIntent.QuotedTopUpWei), never a figure re-priced off a live gas
// price minutes later — the difference would come out of the round trip the
// user was priced against.

type fakeIntentTopUps struct {
	toppedToday, unused int64
	claimed             map[uuid.UUID]bool
	cleared             int
	// marks counts every MarkToppedUp call. The claim map alone cannot tell
	// "never claimed" from "claimed, then cleared", so a skip or refusal
	// path that claims first and releases after would still look clean.
	marks       int
	toppedSince time.Time
	unusedSince time.Time
}

func (f *fakeIntentTopUps) CountToppedUpSince(_ context.Context, _ uuid.UUID, since time.Time) (int64, error) {
	f.toppedSince = since
	return f.toppedToday, nil
}
func (f *fakeIntentTopUps) CountUnusedSince(_ context.Context, _ uuid.UUID, since time.Time) (int64, error) {
	f.unusedSince = since
	return f.unused, nil
}
func (f *fakeIntentTopUps) MarkToppedUp(_ context.Context, id uuid.UUID, _ time.Time) (bool, error) {
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
func (f *fakeIntentTopUps) ClearToppedUp(_ context.Context, id uuid.UUID) error {
	f.cleared++
	delete(f.claimed, id)
	return nil
}

// intentQuotedTopUpWei is what the primary-path fixtures say the quote
// budgeted. Deliberately NOT what the rig's live gas price would compute for
// QuotedGasLimit (300k+80k gas x 1 gwei x 2 = 760_000 gwei), so a send of the
// live figure cannot pass for a send of the quoted one.
const intentQuotedTopUpWei = "123456789000000"

// intentOn is an open sale on chainID whose quote budgeted a top-up in wei —
// the normal case, and the one every primary-path test exercises.
func intentOn(chainID int64, user *models.User) *models.RampSellIntent {
	return &models.RampSellIntent{ID: uuid.New(), UserID: user.ID, WalletAddress: user.WalletAddress,
		SourceAddress: user.WalletAddress, SourceChainID: chainID, SourceTokenAddress: sponsorTestUSDC,
		Status: models.SellIntentSwapping, QuotedGasLimit: 300_000, QuotedTopUpWei: intentQuotedTopUpWei,
		TopUpBudgeted: true}
}

// intentWithoutQuotedWei is a sale whose route quoted no gas in wei: the
// "unknown, not zero" case, which falls back to pricing QuotedGasLimit live.
func intentWithoutQuotedWei(chainID int64, user *models.User) *models.RampSellIntent {
	in := intentOn(chainID, user)
	in.QuotedTopUpWei = ""
	return in
}

func newIntentTopUpRig(t *testing.T) (*GasTopUp, *models.User) {
	g, user, _ := newIntentTopUpRigWith(t)
	return g, user
}

func newIntentTopUpRigWith(t *testing.T) (*GasTopUp, *models.User, *sponsorRig) {
	t.Helper()
	rig, _ := newSponsorRig("base", "USDC")
	// Same construction gas_topup_test.go uses for its subject; the order
	// reader and sponsorship store are unused on the intent path.
	g := NewGasTopUp(rig.orders, rig.store, map[string]*SponsorLane{"base": rig.lane}, 2, time.Second,
		func() time.Time { return sponsorTestNow })
	g.SetChainRegistry(testChainRegistry()) // base 8453; Sonic (146) deliberately absent
	user := &models.User{ID: uuid.New(), WalletAddress: sponsorTestUser, VerificationStatus: models.VerificationStatusVerified}
	rig.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	return g, user, rig
}

func TestTopUpForIntent_OncePerIntent(t *testing.T) {
	g, user := newIntentTopUpRig(t)
	store := &fakeIntentTopUps{}
	in := intentOn(8453, user)
	if res, err := g.TopUpForIntent(context.Background(), user, in, store); err != nil || res.Skipped || res.TxHash == "" {
		t.Fatalf("first top-up: %+v %v", res, err)
	}
	if _, err := g.TopUpForIntent(context.Background(), user, in, store); !errors.Is(err, ErrTopUpUnusedCap) {
		t.Fatalf("a second top-up for the same intent: err = %v", err)
	}
}

func TestTopUpForIntent_DailyAndUnusedCaps(t *testing.T) {
	g, user := newIntentTopUpRig(t)
	daily := &fakeIntentTopUps{toppedToday: 3}
	if _, err := g.TopUpForIntent(context.Background(), user, intentOn(8453, user), daily); !errors.Is(err, ErrTopUpDailyCap) {
		t.Fatalf("4th in a day: err = %v", err)
	}
	// The daily cap is a UTC calendar day; the unused cap a rolling 24h.
	if want := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC); !daily.toppedSince.Equal(want) {
		t.Fatalf("daily cap counted since %v, want the start of the UTC day %v", daily.toppedSince, want)
	}
	unused := &fakeIntentTopUps{unused: 2}
	if _, err := g.TopUpForIntent(context.Background(), user, intentOn(8453, user), unused); !errors.Is(err, ErrTopUpUnusedCap) {
		t.Fatalf("unused cap: err = %v", err)
	}
	if want := sponsorTestNow.Add(-24 * time.Hour); !unused.unusedSince.Equal(want) {
		t.Fatalf("unused cap counted since %v, want %v", unused.unusedSince, want)
	}
	if daily.marks+unused.marks != 0 || daily.cleared+unused.cleared != 0 {
		t.Fatalf("claim attempts: daily %d unused %d; a capped request must never try to claim the intent",
			daily.marks, unused.marks)
	}
}

func TestTopUpForIntent_RefusesUnverifiedUsersAndChainsWithoutALane(t *testing.T) {
	g, user := newIntentTopUpRig(t)
	unverified := *user
	unverified.VerificationStatus = models.VerificationStatusPending
	if _, err := g.TopUpForIntent(context.Background(), &unverified, intentOn(8453, user), &fakeIntentTopUps{}); !errors.Is(err, ErrKYCRequired) {
		t.Fatalf("err = %v, want ErrKYCRequired", err)
	}
	if _, err := g.TopUpForIntent(context.Background(), user, intentOn(146, user), &fakeIntentTopUps{}); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("no sponsor lane on Sonic: err = %v", err)
	}
	// A chain id the registry does not know at all is the same answer, not a
	// panic on a nil lookup.
	if _, err := g.TopUpForIntent(context.Background(), user, intentOn(999_999, user), &fakeIntentTopUps{}); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("unknown chain: err = %v", err)
	}
}

func TestTopUpForIntent_RefusesWithoutAChainRegistry(t *testing.T) {
	g, user := newIntentTopUpRig(t)
	g.SetChainRegistry(nil)
	if _, err := g.TopUpForIntent(context.Background(), user, intentOn(8453, user), &fakeIntentTopUps{}); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("err = %v, want ErrSponsorNotAvailable", err)
	}
}

func TestTopUpForIntent_RefusesAKilledLane(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	rig.lane.Guard.SetKillSwitch(true)
	if _, err := g.TopUpForIntent(context.Background(), user, intentOn(8453, user), &fakeIntentTopUps{}); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("err = %v, want ErrSponsorNotAvailable", err)
	}
}

func TestTopUpForIntent_OnlyAnOwnedSaleNotYetSwapped(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	foreign := intentOn(8453, user)
	foreign.UserID = uuid.New()
	if _, err := g.TopUpForIntent(context.Background(), user, foreign, &fakeIntentTopUps{}); !errors.Is(err, ErrSellIntentNotFound) {
		t.Fatalf("someone else's sale: err = %v", err)
	}
	hash := testSwapHash
	for name, mutate := range map[string]func(*models.RampSellIntent){
		"swapped":   func(in *models.RampSellIntent) { in.Status = models.SellIntentSwapped },
		"failed":    func(in *models.RampSellIntent) { in.Status = models.SellIntentFailed },
		"swap sent": func(in *models.RampSellIntent) { in.SwapTxHash = &hash },
	} {
		in := intentOn(8453, user)
		mutate(in)
		if _, err := g.TopUpForIntent(context.Background(), user, in, &fakeIntentTopUps{}); !errors.Is(err, ErrSellIntentState) {
			t.Fatalf("%s: err = %v, want ErrSellIntentState", name, err)
		}
	}
	if rig.signer.calls() != 0 {
		t.Fatal("nothing may be sent for a sale that is not hers or is past its swap")
	}
}

// Gas goes only to her own reconciled wallet, the address the swap is sent
// from — never to another address the sale names.
func TestTopUpForIntent_OnlyToHerOwnWallet(t *testing.T) {
	g, user := newIntentTopUpRig(t)
	in := intentOn(8453, user)
	in.SourceAddress = "0x000000000000000000000000000000000000bEEF"
	if _, err := g.TopUpForIntent(context.Background(), user, in, &fakeIntentTopUps{}); !errors.Is(err, ErrSponsorWallet) {
		t.Fatalf("err = %v, want ErrSponsorWallet", err)
	}
}

// Selling the chain's own coin pays its gas out of the coin (§5.4).
func TestTopUpForIntent_NotForANativeCoinSale(t *testing.T) {
	g, user := newIntentTopUpRig(t)
	in := intentOn(8453, user)
	in.SourceTokenAddress = evmNativeAddress
	if _, err := g.TopUpForIntent(context.Background(), user, in, &fakeIntentTopUps{}); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("err = %v, want ErrSponsorNotAvailable", err)
	}
}

func TestTopUpForIntent_SkipsWhenHerBalanceCovers(t *testing.T) {
	g, user := newIntentTopUpRig(t)
	g.lanes["base"].Chain.(*fakeSponsorChain).balances[strings.ToLower(user.WalletAddress)] = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	store := &fakeIntentTopUps{}
	in := intentOn(8453, user)
	res, err := g.TopUpForIntent(context.Background(), user, in, store)
	if err != nil || !res.Skipped || store.marks != 0 || store.cleared != 0 {
		t.Fatalf("res = %+v err = %v claim attempts = %d; want skipped with no claim ever tried", res, err, store.marks)
	}
}

// ─── The amount: what the quote budgeted, never a live re-price ────────────

// The primary path: the quote stored its top-up in wei, and that exact figure
// is sent — even when the live gas price has moved so far that re-pricing
// would cost five hundred times as much (and be refused as too expensive).
func TestTopUpForIntent_SendsExactlyTheQuotedTopUp(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	rig.native.gasPrice = big.NewInt(500_000_000_000) // 500 gwei: live pricing would be 0.38 native
	store := &fakeIntentTopUps{}
	in := intentOn(8453, user)
	res, err := g.TopUpForIntent(context.Background(), user, in, store)
	if err != nil {
		t.Fatalf("TopUpForIntent: %v", err)
	}
	if res.AmountWei != intentQuotedTopUpWei || rig.native.sentWei.String() != intentQuotedTopUpWei {
		t.Fatalf("sent %s (result %s), want exactly the quoted %s", rig.native.sentWei, res.AmountWei, intentQuotedTopUpWei)
	}
	if !strings.EqualFold(rig.native.sentTo, sponsorTestUser) {
		t.Fatalf("gas sent to %s, want her wallet", rig.native.sentTo)
	}
	if !store.claimed[in.ID] {
		t.Fatal("a sent top-up must stay claimed")
	}
}

// She already holds part of it: only the shortfall is sent, so what she ends
// up holding is the quoted figure and we never spend beyond it.
func TestTopUpForIntent_SendsOnlyTheShortfallOfTheQuotedTopUp(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	rig.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(23_456_789_000_000)
	res, err := g.TopUpForIntent(context.Background(), user, intentOn(8453, user), &fakeIntentTopUps{})
	if err != nil || res.AmountWei != "100000000000000" {
		t.Fatalf("res = %+v err = %v; want the 100_000 gwei shortfall", res, err)
	}
}

// The fallback: the route quoted no gas in wei, so there is nothing quoted to
// honour — the quote's own gas LIMIT is priced at today's gas price, with the
// approve and the buffer, exactly as the plan describes.
func TestTopUpForIntent_FallsBackToTheQuotedGasLimitWhenNoWeiWasQuoted(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	res, err := g.TopUpForIntent(context.Background(), user, intentWithoutQuotedWei(8453, user), &fakeIntentTopUps{})
	if err != nil {
		t.Fatalf("TopUpForIntent: %v", err)
	}
	// (300_000 + treasuryERC20Gas) gas x 1 gwei x topUpGasBuffer.
	want := new(big.Int).SetUint64((300_000 + treasuryERC20Gas) * 1_000_000_000 * topUpGasBuffer)
	if res.AmountWei != want.String() || rig.native.sentWei.Cmp(want) != 0 {
		t.Fatalf("sent %s, want %s", rig.native.sentWei, want)
	}
}

// Either path, a single top-up above the lane's ceiling is refused, not sent,
// and does not use up the sale's one top-up.
func TestTopUpForIntent_RefusesAboveTheLaneCeiling(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	rig.lane.TopUpMaxWei = big.NewInt(100_000_000_000_000) // below the quoted figure
	store := &fakeIntentTopUps{}
	in := intentOn(8453, user)
	if _, err := g.TopUpForIntent(context.Background(), user, in, store); !errors.Is(err, ErrTopUpTooExpensive) {
		t.Fatalf("quoted path: err = %v, want ErrTopUpTooExpensive", err)
	}
	rig.native.gasPrice = big.NewInt(500_000_000_000)
	if _, err := g.TopUpForIntent(context.Background(), user, intentWithoutQuotedWei(8453, user), store); !errors.Is(err, ErrTopUpTooExpensive) {
		t.Fatalf("fallback path: err = %v, want ErrTopUpTooExpensive", err)
	}
	if rig.signer.calls() != 0 || store.marks != 0 || store.cleared != 0 {
		t.Fatalf("sends = %d claim attempts = %d; a refused top-up must not send or even try to claim",
			rig.signer.calls(), store.marks)
	}
}

// A stored figure that is not a positive integer is corrupt, not "unknown":
// refused rather than silently re-priced live.
func TestTopUpForIntent_RefusesACorruptQuotedFigure(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	for _, bad := range []string{"not-a-number", "0", "-5", "1.5"} {
		in := intentOn(8453, user)
		in.QuotedTopUpWei = bad
		if _, err := g.TopUpForIntent(context.Background(), user, in, &fakeIntentTopUps{}); !errors.Is(err, ErrSponsorNotAvailable) {
			t.Fatalf("%q: err = %v, want ErrSponsorNotAvailable", bad, err)
		}
	}
	if rig.signer.calls() != 0 {
		t.Fatal("nothing may be sent for a corrupt quote")
	}
}

// ─── The claim, and when it is given back ──────────────────────────────────

// A tx that provably never left (the node refused it before broadcast) frees
// the sale's one top-up again.
func TestTopUpForIntent_ReleasesTheClaimWhenNothingWasSent(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	rig.signer.err = fmt.Errorf("insufficient funds: %w", ErrTxNotBroadcast)
	store := &fakeIntentTopUps{}
	in := intentOn(8453, user)
	if _, err := g.TopUpForIntent(context.Background(), user, in, store); err == nil {
		t.Fatal("want an error")
	}
	if store.claimed[in.ID] || store.cleared != 1 {
		t.Fatalf("claimed = %v cleared = %d; a never-sent top-up must be released", store.claimed[in.ID], store.cleared)
	}
	// And a build failure is the same: nothing reached the chain.
	rig.signer.err = nil
	rig.native.prepareErr = errors.New("rpc down")
	if _, err := g.TopUpForIntent(context.Background(), user, in, store); err == nil || store.claimed[in.ID] {
		t.Fatalf("build failure: err = %v claimed = %v", err, store.claimed[in.ID])
	}
}

// A broadcast whose outcome is unknown may have landed: the claim stays, so a
// retry cannot send her a second top-up, and it counts against the caps.
func TestTopUpForIntent_KeepsTheClaimWhenTheBroadcastIsUncertain(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	rig.signer.err = errors.New("rpc timeout")
	store := &fakeIntentTopUps{}
	in := intentOn(8453, user)
	if _, err := g.TopUpForIntent(context.Background(), user, in, store); !errors.Is(err, ErrSponsorshipInProgress) {
		t.Fatalf("err = %v, want ErrSponsorshipInProgress", err)
	}
	if !store.claimed[in.ID] || store.cleared != 0 {
		t.Fatal("an uncertain broadcast must keep the claim")
	}
}

// Sent but not yet confirmed is a real spend: it keeps the claim too.
func TestTopUpForIntent_KeepsTheClaimWhenTheReceiptIsSlow(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	rig.native.receiptErr = errors.New("timeout")
	store := &fakeIntentTopUps{}
	in := intentOn(8453, user)
	if _, err := g.TopUpForIntent(context.Background(), user, in, store); !errors.Is(err, ErrTopUpUnconfirmed) {
		t.Fatalf("err = %v, want ErrTopUpUnconfirmed", err)
	}
	if !store.claimed[in.ID] || store.cleared != 0 {
		t.Fatal("an unconfirmed top-up must keep the claim")
	}
}

// ─── The store: SellIntentService's four queries ───────────────────────────

func TestSellIntentTopUpStore(t *testing.T) {
	r := newSellIntentRig(t)
	ctx := context.Background()
	db := r.svc.db
	now := sponsorTestNow

	open, _, err := r.svc.Create(ctx, r.user, CreateSellIntentInput{SourceChainID: 8453, SourceToken: sponsorTestUSDC,
		SourceAmount: "1", SourceAddress: r.user.WalletAddress, FiatCode: "NGN", BankCode: "044", AccountNumber: "0123456789"})
	if err != nil {
		t.Fatal(err)
	}

	// MarkToppedUp is the claim: exactly one caller wins it.
	if ok, err := r.svc.MarkToppedUp(ctx, open.ID, now); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, err := r.svc.MarkToppedUp(ctx, open.ID, now); err != nil || ok {
		t.Fatalf("second claim: %v %v; want lost", ok, err)
	}
	if n, _ := r.svc.CountToppedUpSince(ctx, r.user.ID, now.Add(-time.Minute)); n != 1 {
		t.Fatalf("topped up since = %d, want 1", n)
	}
	if n, _ := r.svc.CountToppedUpSince(ctx, r.user.ID, now.Add(time.Minute)); n != 0 {
		t.Fatalf("topped up since after = %d, want 0", n)
	}
	if n, _ := r.svc.CountToppedUpSince(ctx, uuid.New(), now.Add(-time.Minute)); n != 0 {
		t.Fatalf("someone else's count = %d, want 0", n)
	}

	// ClearToppedUp gives the claim back.
	if err := r.svc.ClearToppedUp(ctx, open.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.svc.MarkToppedUp(ctx, open.ID, now); !ok {
		t.Fatal("a cleared claim must be claimable again")
	}

	// Unused: failed, never swapped, topped up, recently.
	hash := testSwapHash2
	earlier := now.Add(-2 * time.Hour)
	seed := func(status string, swap *string, toppedUp *time.Time, updated time.Time) {
		t.Helper()
		row := &models.RampSellIntent{UserID: r.user.ID, SourceAddress: r.user.WalletAddress, WalletAddress: r.user.WalletAddress,
			SourceChainID: 8453, SourceTokenAddress: sponsorTestUSDC, SourceAmount: "1", FiatCode: "NGN",
			BankCode: "044", AccountNumber: "0123456789", Status: status, SwapTxHash: swap, ToppedUpAt: toppedUp}
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`UPDATE ramp_sell_intents SET updated_at = ? WHERE id = ?`, updated, row.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed(models.SellIntentFailed, nil, &earlier, earlier)                // unused
	seed(models.SellIntentFailed, &hash, &earlier, earlier)              // swapped, then failed: used
	seed(models.SellIntentFailed, nil, nil, earlier)                     // never topped up
	seed(models.SellIntentFailed, nil, &earlier, now.Add(-30*time.Hour)) // too long ago
	if n, err := r.svc.CountUnusedSince(ctx, r.user.ID, now.Add(-24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("unused = %d %v, want 1", n, err)
	}
	// The open, topped-up sale is not unused: it is not decided yet.
	if n, _ := r.svc.CountToppedUpSince(ctx, r.user.ID, now.Add(-3*time.Hour)); n != 4 {
		t.Fatalf("topped up since = %d, want 4 (open + three seeded)", n)
	}

	// Get reads by id AND owner.
	if got, err := r.svc.Get(ctx, r.user.ID, open.ID); err != nil || got.ID != open.ID {
		t.Fatalf("Get own: %v %v", got, err)
	}
	if _, err := r.svc.Get(ctx, uuid.New(), open.ID); !errors.Is(err, ErrSellIntentNotFound) {
		t.Fatalf("Get foreign: %v, want ErrSellIntentNotFound", err)
	}
}

// RampSponsorship is what the handler holds: its chain registry must reach the
// top-up, or every intent top-up answers "not available".
func TestRampSponsorship_TopUpForIntentUsesItsChainRegistry(t *testing.T) {
	rig, _ := newSponsorRig("base", "USDC")
	rig.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	s := NewRampSponsorship(rig.orders, rig.store, rig.lanes(), 2, time.Second, func() time.Time { return sponsorTestNow })
	user := &models.User{ID: uuid.New(), WalletAddress: sponsorTestUser, VerificationStatus: models.VerificationStatusVerified}
	if _, err := s.TopUpForIntent(context.Background(), user, intentOn(8453, user), &fakeIntentTopUps{}); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("no registry: err = %v, want ErrSponsorNotAvailable", err)
	}
	s.SetChainRegistry(testChainRegistry())
	res, err := s.TopUpForIntent(context.Background(), user, intentOn(8453, user), &fakeIntentTopUps{})
	if err != nil || res.AmountWei != intentQuotedTopUpWei {
		t.Fatalf("res = %+v err = %v; want the quoted top-up sent", res, err)
	}
}

// The claim is made against the row as it is AT CLAIM TIME, not the copy the
// handler loaded: a sale that was swapped or abandoned in between must not
// win the claim, and must not be sent gas.
func TestTopUpForIntent_NoClaimForASaleThatMovedOnAfterItWasLoaded(t *testing.T) {
	for name, moveOn := range map[string]func(*sellIntentRig, *models.RampSellIntent) error{
		"swapped": func(r *sellIntentRig, in *models.RampSellIntent) error {
			return r.svc.RecordSwap(context.Background(), in.UserID, in.ID, testSwapHash)
		},
		"abandoned": func(r *sellIntentRig, in *models.RampSellIntent) error {
			return r.svc.Abandon(context.Background(), in.UserID, in.ID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			g, user, rig := newIntentTopUpRigWith(t)
			store := newSellIntentRig(t)
			stale := intentOn(8453, user) // what the handler loaded: still swapping
			row := *stale
			row.SourceAmount, row.FiatCode, row.BankCode, row.AccountNumber = "1", "NGN", "044", "0123456789"
			if err := store.svc.db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			if err := moveOn(store, stale); err != nil {
				t.Fatal(err)
			}
			if _, err := g.TopUpForIntent(context.Background(), user, stale, store.svc); err == nil {
				t.Fatal("a top-up for a sale that has moved on must fail")
			}
			if rig.signer.calls() != 0 {
				t.Fatalf("sends = %d; no gas may go to a sale that has moved on", rig.signer.calls())
			}
			var after models.RampSellIntent
			if err := store.svc.db.First(&after, "id = ?", stale.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.ToppedUpAt != nil {
				t.Fatal("the claim must not land on a sale that has moved on")
			}
		})
	}
}

// ─── Whole-branch fix: only gas the quote paid for ─────────────────────────

// A sale whose quote judged her funded budgeted no top-up, so none may be
// sent — on either path, whatever the row's wei column holds, and before any
// claim is even attempted. Before the fix the intent stored a top-up figure
// from the route whether or not the quote had counted one, and this call sent
// it out of a budget that never included it.
func TestTopUpForIntent_RefusesASaleWhoseQuoteBudgetedNoTopUp(t *testing.T) {
	for name, in := range map[string]func(*models.User) *models.RampSellIntent{
		"a stored wei figure": func(u *models.User) *models.RampSellIntent { return intentOn(8453, u) },
		"no wei (live path)":  func(u *models.User) *models.RampSellIntent { return intentWithoutQuotedWei(8453, u) },
	} {
		t.Run(name, func(t *testing.T) {
			g, user, rig := newIntentTopUpRigWith(t)
			intent := in(user)
			intent.TopUpBudgeted = false
			store := &fakeIntentTopUps{}
			if _, err := g.TopUpForIntent(context.Background(), user, intent, store); !errors.Is(err, ErrSponsorNotAvailable) {
				t.Fatalf("err = %v, want ErrSponsorNotAvailable", err)
			}
			if rig.signer.calls() != 0 || rig.native.sentWei != nil || store.marks != 0 {
				t.Fatalf("sends = %d sent = %v claims = %d; an unbudgeted top-up must send nothing",
					rig.signer.calls(), rig.native.sentWei, store.marks)
			}
		})
	}
}

// The seam, end to end: quote → admission → the stored row → the send. The
// fee-carrying route quotes different gas from the fee-free one; whatever is
// sent has to be the top-up the ADMITTED route priced, and a sale priced with
// no top-up must never send one.
func TestSellSeam_TheTopUpSentIsTheOneTheQuoteAdmitted(t *testing.T) {
	routes := func(t *testing.T) (*LiFiRoute, *LiFiRoute) {
		r1, r2 := topUpFixture(t)
		r1.Data = testBridgeCalldata(t, orderTestUserWallet, rampSettlementChainID, false)
		r2.Data = testBridgeCalldata(t, orderTestUserWallet, rampSettlementChainID, false)
		r2.GasCostWei = big.NewInt(2_000) // r1 says 1,000: a different answer for the route she signs
		return r1, r2
	}
	run := func(t *testing.T, balance *big.Int) (*models.RampSellIntent, *SellSwapQuote, *sponsorRig, error) {
		r1, r2 := routes(t)
		quoter, _ := newSellQuoterWithFakes(t, r1, r2)
		quoter.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return balance, true })
		sale := newSellIntentRig(t)
		sale.svc.quoter = quoter
		in, q, err := sale.svc.Create(context.Background(), sale.user, CreateSellIntentInput{
			SourceChainID: 56, SourceToken: bscUSDT, SourceAmount: "100000000000000000000",
			SourceAddress: sale.user.WalletAddress, FiatCode: "NGN", BankCode: "044", AccountNumber: "0123456789"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		rig, _ := newSponsorRig("bsc", "USDT")
		rig.chain.balances[strings.ToLower(sale.user.WalletAddress)] = big.NewInt(0) // what she holds at send time
		g := NewGasTopUp(rig.orders, rig.store, rig.lanes(), 2, time.Second, func() time.Time { return sponsorTestNow })
		g.SetChainRegistry(testChainRegistry())
		_, err = g.TopUpForIntent(context.Background(), sale.user, in, &fakeIntentTopUps{})
		return in, q, rig, err
	}

	t.Run("priced a top-up: sends exactly the signed route's", func(t *testing.T) {
		in, q, rig, err := run(t, big.NewInt(0))
		if err != nil {
			t.Fatalf("TopUpForIntent: %v", err)
		}
		_, r2 := routes(t)
		want := quotedTopUpWei(r2) // 2,000 × 1.08 × 2 = 4,320 — not r1's 2,160
		if !q.GasTopUp || q.TopUpWei != want || !in.TopUpBudgeted || in.QuotedTopUpWei != want {
			t.Fatalf("quote %v/%q, row %v/%q; want the admitted route's %s", q.GasTopUp, q.TopUpWei, in.TopUpBudgeted, in.QuotedTopUpWei, want)
		}
		if rig.native.sentWei == nil || rig.native.sentWei.String() != want {
			t.Fatalf("sent %v, want %s", rig.native.sentWei, want)
		}
		// And that top-up is inside the g the sale was admitted on.
		if q.GasBps != USDGasBps(0.02+TopUpUSDForRoute(r2), r2.FromAmountUSD) {
			t.Fatalf("gas = %d bps, not the signed route's own g", q.GasBps)
		}
	})
	t.Run("priced none: sends nothing", func(t *testing.T) {
		in, q, rig, err := run(t, big.NewInt(1_000_000_000))
		if !errors.Is(err, ErrSponsorNotAvailable) {
			t.Fatalf("err = %v, want ErrSponsorNotAvailable", err)
		}
		if q.GasTopUp || in.TopUpBudgeted || in.QuotedTopUpWei != "" || rig.native.sentWei != nil {
			t.Fatalf("quote %v, row %v/%q, sent %v; nothing was budgeted", q.GasTopUp, in.TopUpBudgeted, in.QuotedTopUpWei, rig.native.sentWei)
		}
	})
}
