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

func topUpUser(r *sponsorRig) *models.User {
	return &models.User{ID: r.userID, WalletAddress: sponsorTestUser, VerificationStatus: models.VerificationStatusVerified}
}

func newTestTopUp(r *sponsorRig) *GasTopUp {
	return NewGasTopUp(r.orders, r.store, r.lanes(), 2, time.Second, func() time.Time { return sponsorTestNow })
}

func TestGasTopUp_SendsShortfallAtTwiceEstimate(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	// estimate 50_000 gas x 1 gwei x 2 = 100_000 gwei needed; user holds 25_000 gwei.
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(25_000_000_000_000)
	got, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID)
	if err != nil {
		t.Fatalf("TopUp: %v", err)
	}
	if got.Skipped || got.TxHash != "0xsponsored" || got.AmountWei != "75000000000000" {
		t.Fatalf("result = %+v; want 75_000 gwei sent", got)
	}
	if !strings.EqualFold(r.native.sentTo, sponsorTestUser) {
		t.Fatalf("gas sent to %s, want the order wallet", r.native.sentTo)
	}
}

func TestGasTopUp_SkipsWhenUserAlreadyHasGas(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(1_000_000_000_000_000)
	got, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID)
	if err != nil || !got.Skipped {
		t.Fatalf("result = %+v, err = %v; want Skipped", got, err)
	}
	if r.signer.broadcastCalls != 0 || r.store.count() != 0 {
		t.Fatal("a skipped top-up must not broadcast or consume the order's slot")
	}
}

func TestGasTopUp_RequiresKYC(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	u := topUpUser(r)
	u.VerificationStatus = models.VerificationStatusPending
	if _, err := newTestTopUp(r).TopUp(context.Background(), u, orderID); !errors.Is(err, ErrKYCRequired) {
		t.Fatalf("want ErrKYCRequired, got %v", err)
	}
}

func TestGasTopUp_RejectsAuthorizationModeRoute(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	if _, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("want ErrSponsorNotAvailable, got %v", err)
	}
}

// A wallet with code (an EIP-7702 delegation) cannot use EIP-3009, so on an
// authorization token it gets gas and sends the token itself.
func TestGasTopUp_WalletWithCodeGetsGasOnAuthorizationToken(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	r.chain.code = map[string][]byte{strings.ToLower(sponsorTestUser): eip7702Code}
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	got, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID)
	if err != nil || got.Skipped || got.TxHash != "0xsponsored" {
		t.Fatalf("result = %+v, err = %v; want gas sent", got, err)
	}
	if !strings.EqualFold(r.native.sentTo, sponsorTestUser) {
		t.Fatalf("gas sent to %s, want the order wallet", r.native.sentTo)
	}
}

func TestGasTopUp_SecondCallReturnsFirstTransfer(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	svc := newTestTopUp(r)
	first, err := svc.TopUp(context.Background(), topUpUser(r), orderID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.TopUp(context.Background(), topUpUser(r), orderID)
	if err != nil || second.TxHash != first.TxHash {
		t.Fatalf("second = %+v, %v; want the first tx %s", second, err, first.TxHash)
	}
	if r.signer.broadcastCalls != 1 {
		t.Fatalf("broadcasts = %d, want 1", r.signer.broadcastCalls)
	}
}

func TestTopUpUnused(t *testing.T) {
	cases := []struct {
		status, amountPaid string
		want               bool
	}{
		{models.PaycrestStatusExpired, "", true},
		{models.PaycrestStatusExpired, "0", true},
		{models.PaycrestStatusExpired, "0.000000", true},
		{models.PaycrestStatusCancelled, "", true},
		{models.PaycrestStatusCancelled, "0", true},
		{models.PaycrestStatusCancelled, "0.000000", true},
		{models.PaycrestStatusFailed, "", true},
		{models.PaycrestStatusFailed, "0", true},
		{models.PaycrestStatusFailed, "0.000000", true},
		{models.PaycrestStatusExpired, "1.5", false},
		{models.PaycrestStatusSettled, "", false},
		{models.PaycrestStatusRefunded, "", false},
		{models.PaycrestStatusPending, "", false},
		{models.PaycrestStatusSettling, "", false},
		{models.PaycrestStatusUnmatched, "", false},
		{models.PaycrestStatusAwaitingDeposit, "", false},
	}
	for _, c := range cases {
		if got := topUpUnused(c.status, c.amountPaid); got != c.want {
			t.Errorf("topUpUnused(%q, %q) = %v, want %v", c.status, c.amountPaid, got, c.want)
		}
	}
}

// seedPriorTopUp gives the rig's user another sell order with the given
// outcome, and a sent top-up on it.
func seedPriorTopUp(t *testing.T, r *sponsorRig, status, amountPaid string) {
	t.Helper()
	orderID := uuid.New()
	r.orders.orders[orderID] = &models.PaycrestOrderRecord{
		ID: orderID, UserID: r.userID, Direction: models.PaycrestDirectionSell,
		Status: status, AmountPaid: amountPaid, ChainKey: "bsc", Token: "USDT", WalletAddress: sponsorTestUser,
	}
	row := &models.GasSponsorship{UserID: r.userID, OrderID: orderID, Kind: models.GasSponsorshipKindTopUp}
	if existing, err := r.store.Reserve(context.Background(), row); err != nil || existing != nil {
		t.Fatalf("reserve: existing=%v err=%v", existing, err)
	}
	_ = r.store.SetTxHash(context.Background(), row.ID, "0xprior", "1")
}

func TestGasTopUp_UnusedCapRefusesAfterAbandonedTopUps(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	for i := 0; i < 2; i++ {
		seedPriorTopUp(t, r, models.PaycrestStatusExpired, "0")
	}
	if _, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID); !errors.Is(err, ErrTopUpUnusedCap) {
		t.Fatalf("want ErrTopUpUnusedCap, got %v", err)
	}
	if r.signer.calls() != 0 {
		t.Fatalf("broadcasts = %d past the unused cap, want 0", r.signer.calls())
	}
	if r.store.count() != 2 {
		t.Fatalf("rows = %d, want 2; the refused order's own row must be released", r.store.count())
	}
}

func TestGasTopUp_CompletedSalesNeverCap(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	for i := 0; i < 5; i++ {
		seedPriorTopUp(t, r, models.PaycrestStatusSettled, "36.4218")
	}
	if _, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID); err != nil {
		t.Fatalf("TopUp: %v; completed sales must never cap a user", err)
	}
	if r.signer.calls() != 1 {
		t.Fatalf("broadcasts = %d, want 1", r.signer.calls())
	}
}

// A user at the unused cap who already holds enough gas needs no top-up. The
// cap must not refuse them, or the app aborts a sale that needed nothing from us.
func TestGasTopUp_UnusedCapSkipsWhenGasAlreadyHeld(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	for i := 0; i < 2; i++ {
		seedPriorTopUp(t, r, models.PaycrestStatusExpired, "0")
	}
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(1_000_000_000_000_000)
	got, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID)
	if err != nil || !got.Skipped {
		t.Fatalf("result = %+v, err = %v; want Skipped with no error", got, err)
	}
	if r.signer.calls() != 0 {
		t.Fatalf("broadcasts = %d, want 0", r.signer.calls())
	}
	if r.store.count() != 2 {
		t.Fatalf("rows = %d, want 2; the skipped order's row must be released", r.store.count())
	}
}

func TestGasTopUp_RefusesAboveLaneMax(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	r.native.gasPrice = big.NewInt(100_000_000_000) // 100 gwei spike -> 0.01 native > 0.001 max
	if _, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID); !errors.Is(err, ErrTopUpTooExpensive) {
		t.Fatalf("want ErrTopUpTooExpensive, got %v", err)
	}
	if r.store.count() != 0 {
		t.Fatal("refused top-up still holds the order slot")
	}
}

// A broadcast error may hide an accepted tx; keeping the slot stops a retry
// from sending the user a second top-up.
func TestGasTopUp_UncertainBroadcastKeepsSlot(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	r.signer.err = errors.New("i/o timeout")
	svc := newTestTopUp(r)
	if _, err := svc.TopUp(context.Background(), topUpUser(r), orderID); !errors.Is(err, ErrSponsorshipInProgress) {
		t.Fatalf("want ErrSponsorshipInProgress, got %v", err)
	}
	if r.store.count() != 1 || r.signer.calls() != 1 {
		t.Fatalf("rows=%d broadcasts=%d; want the slot kept after one broadcast", r.store.count(), r.signer.calls())
	}
	if _, err := svc.TopUp(context.Background(), topUpUser(r), orderID); !errors.Is(err, ErrSponsorshipInProgress) {
		t.Fatalf("retry: want ErrSponsorshipInProgress, got %v", err)
	}
	if r.signer.calls() != 1 {
		t.Fatalf("broadcasts = %d after retry, want 1", r.signer.calls())
	}
}

func TestGasTopUp_BuildFailureFreesSlot(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	r.native.prepareErr = errors.New("nonce read failed")
	if _, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID); err == nil {
		t.Fatal("want build error")
	}
	if r.store.count() != 0 {
		t.Fatal("a top-up that was never broadcast still holds the order slot; the user could never retry")
	}
}

func TestGasTopUp_RecordsHashWhenRequestContextCancelled(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)

	store := &ctxCheckingStore{fakeSponsorStore: r.store}
	svc := NewGasTopUp(r.orders, store, r.lanes(), 3, time.Second, func() time.Time { return sponsorTestNow })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // dead before TopUp even starts

	got, err := svc.TopUp(ctx, topUpUser(r), orderID)
	// The receipt wait follows the request ctx, so a dead request stops waiting
	// and reports unconfirmed; the hash must still be recorded.
	if !errors.Is(err, ErrTopUpUnconfirmed) {
		t.Fatalf("TopUp: want ErrTopUpUnconfirmed from the cancelled receipt wait, got %v", err)
	}
	if r.signer.broadcastCalls != 1 {
		t.Fatalf("broadcasts = %d, want 1", r.signer.broadcastCalls)
	}
	row := r.store.onlyRow()
	if row == nil || row.TxHash != got.TxHash || row.TxHash != "0xsponsored" || row.AmountWei == "" {
		t.Fatalf("row = %+v; want TxHash and AmountWei recorded despite the cancelled request ctx", row)
	}
}

func TestGasTopUp_UnconfirmedReturnsHashWithError(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	r.native.receiptErr = errors.New("archive requests require a personal token")
	got, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID)
	if !errors.Is(err, ErrTopUpUnconfirmed) || got.TxHash != "0xsponsored" {
		t.Fatalf("result = %+v, err = %v; want the hash with ErrTopUpUnconfirmed", got, err)
	}
	// Why the receipt could not be read must survive: an RPC refusing every
	// receipt (2026-10-01, publicnode) otherwise looks like a slow chain.
	if !strings.Contains(err.Error(), "archive requests require a personal token") {
		t.Fatalf("err = %v; want the receipt read's own cause kept", err)
	}
}

// reserveInFlightTopUp leaves the rig's order holding a topup row with no tx
// hash, as an uncertain broadcast does.
func reserveInFlightTopUp(t *testing.T, r *sponsorRig, orderID uuid.UUID) {
	t.Helper()
	row := &models.GasSponsorship{UserID: r.userID, OrderID: orderID, Kind: models.GasSponsorshipKindTopUp}
	if existing, err := r.store.Reserve(context.Background(), row); err != nil || existing != nil {
		t.Fatalf("reserve: existing=%v err=%v", existing, err)
	}
}

// An uncertain broadcast usually did land: once the gas is there, the user
// must be able to make their own transfer instead of being stuck in-progress.
func TestGasTopUp_InFlightRowSkipsWhenGasArrived(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	reserveInFlightTopUp(t, r, orderID)
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(1_000_000_000_000_000)
	got, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID)
	if err != nil || !got.Skipped {
		t.Fatalf("result = %+v, err = %v; want Skipped", got, err)
	}
	if r.signer.calls() != 0 {
		t.Fatalf("broadcasts = %d, want 0", r.signer.calls())
	}
	if r.store.count() != 1 {
		t.Fatalf("rows = %d, want the in-flight row kept", r.store.count())
	}
}

func TestGasTopUp_InFlightRowStaysInProgressWithoutGas(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	reserveInFlightTopUp(t, r, orderID)
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	if _, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID); !errors.Is(err, ErrSponsorshipInProgress) {
		t.Fatalf("want ErrSponsorshipInProgress, got %v", err)
	}
	if r.signer.calls() != 0 {
		t.Fatalf("broadcasts = %d, want 0", r.signer.calls())
	}
}
