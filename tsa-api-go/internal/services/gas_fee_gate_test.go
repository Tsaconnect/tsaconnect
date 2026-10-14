package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// Floors are alert-only (owner 2026-10-13). Each sponsor path below sits under
// a 0.1-native floor: one that can still pay its own transaction goes through
// (and alerts); one that cannot is refused with ErrRelayerUnderfunded.

func withSponsorFloor(r *sponsorRig, floor int64) {
	r.lane.Guard = NewSponsorGuard(r.lane.Guard.name, SponsorLimits{
		DailyGasBudget: big.NewInt(10_000_000), BalanceFloor: big.NewInt(floor),
	}, func() time.Time { return sponsorTestNow })
}

// The top-up sends 75_000 gwei (7.5e13) and its own fee is 21k gas × 1 gwei × 2
// (4.2e13): the sponsor needs 1.17e14.
func TestGasTopUp_BelowFloorButAbleToPaySends(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	withSponsorFloor(r, 1e17)
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(25_000_000_000_000)
	r.chain.balances[strings.ToLower(sponsorTestSigner)] = big.NewInt(2e14)
	got, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID)
	if err != nil || got.TxHash != "0xsponsored" {
		t.Fatalf("below the floor but able to pay: %+v, %v", got, err)
	}
}

func TestGasTopUp_RefusedWhenTheSponsorCannotPayAmountPlusFee(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	withSponsorFloor(r, 1e17)
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(25_000_000_000_000)
	// Covers the 7.5e13 sent, not the fee on top of it.
	r.chain.balances[strings.ToLower(sponsorTestSigner)] = big.NewInt(1e14)
	if _, err := newTestTopUp(r).TopUp(context.Background(), topUpUser(r), orderID); !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("want ErrRelayerUnderfunded, got %v", err)
	}
	if r.signer.calls() != 0 || r.store.count() != 0 {
		t.Fatalf("broadcasts=%d rows=%d, want nothing sent and the slot freed", r.signer.calls(), r.store.count())
	}
}

// CheckTopUp (asked at quote time) applies the same rule.
func TestGasTopUp_CheckTopUpGatesOnAmountPlusFeeNotTheFloor(t *testing.T) {
	r, _ := newSponsorRig("bsc", "USDT")
	withSponsorFloor(r, 1e17)
	g := newTestTopUp(r)
	g.SetChainRegistry(testChainRegistry())
	r.chain.balances[strings.ToLower(sponsorTestSigner)] = big.NewInt(2e14)
	if err := g.CheckTopUp(context.Background(), 56, big.NewInt(75_000_000_000_000)); err != nil {
		t.Fatalf("below the floor but able to pay: %v", err)
	}
	r.chain.balances[strings.ToLower(sponsorTestSigner)] = big.NewInt(1e14)
	if err := g.CheckTopUp(context.Background(), 56, big.NewInt(75_000_000_000_000)); !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("unable to pay: want ErrRelayerUnderfunded, got %v", err)
	}
}

// A transferWithAuthorization relay costs 120k gas × 1 gwei × 2 = 2.4e14.
func TestAuthorizationRelayer_BelowFloorButAbleToPayRelays(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	withSponsorFloor(r, 1e17)
	r.chain.balances[strings.ToLower(sponsorTestSigner)] = big.NewInt(5e14)
	if _, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth()); err != nil {
		t.Fatalf("below the floor but able to pay: %v", err)
	}
}

func TestAuthorizationRelayer_RefusedWhenItCannotPayTheRelaysFee(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	withSponsorFloor(r, 1e17)
	r.chain.balances[strings.ToLower(sponsorTestSigner)] = big.NewInt(1e14)
	if _, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth()); !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("want ErrRelayerUnderfunded, got %v", err)
	}
	if r.signer.calls() != 0 {
		t.Fatal("nothing may be broadcast")
	}
}

func TestFeeNeed_IsUnitsTimesPriceTimesBufferPlusValue(t *testing.T) {
	if got := feeNeed(21_000, big.NewInt(1e9), big.NewInt(5)); got.Cmp(big.NewInt(42_000_000_000_005)) != 0 {
		t.Fatalf("feeNeed = %s", got)
	}
}
