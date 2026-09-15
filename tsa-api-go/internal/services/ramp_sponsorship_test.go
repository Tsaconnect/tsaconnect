package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func newTestRampSponsorship(r *sponsorRig) *RampSponsorship {
	return NewRampSponsorship(r.orders, r.store, r.lanes(), 3, time.Second, func() time.Time { return sponsorTestNow })
}

func TestRampSponsorship_OfferAuthorizationCarriesVerifiedDomainAndExactValue(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	offer, err := newTestRampSponsorship(r).Offer(context.Background(), topUpUser(r), orderID)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	if offer.Mode != SponsorModeAuthorization || offer.Value != "36421800" || offer.Decimals != 6 {
		t.Fatalf("offer = %+v", offer)
	}
	if offer.To != common.HexToAddress(sponsorTestDeposit).Hex() || offer.From != common.HexToAddress(sponsorTestUser).Hex() {
		t.Fatalf("from/to = %s/%s", offer.From, offer.To)
	}
	if offer.Domain == nil || offer.Domain.Name != "USD Coin" || offer.Domain.Version != "2" || offer.Domain.ChainID != "8453" {
		t.Fatalf("domain = %+v", offer.Domain)
	}
	// Order's ValidUntil is far in the future: validBefore is capped at now+25m, not ValidUntil-30s.
	if want := uint64(sponsorTestNow.Add(25 * time.Minute).Unix()); offer.ValidBefore != want {
		t.Fatalf("validBefore = %d, want %d", offer.ValidBefore, want)
	}
}

func TestRampSponsorship_OfferNoneWhenDomainUnverifiable(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC") // no extra selectors: name() fails
	offer, err := newTestRampSponsorship(r).Offer(context.Background(), topUpUser(r), orderID)
	if err != nil || offer.Mode != SponsorModeNone {
		t.Fatalf("offer = %+v, err = %v; want mode none", offer, err)
	}
}

func TestRampSponsorship_OfferNoneWhenTooLittleTimeToSign(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	soon := sponsorTestNow.Add(90 * time.Second)
	r.orders.orders[orderID].ValidUntil = &soon
	offer, err := newTestRampSponsorship(r).Offer(context.Background(), topUpUser(r), orderID)
	if err != nil || offer.Mode != SponsorModeNone {
		t.Fatalf("offer = %+v, err = %v; want mode none", offer, err)
	}
}

func TestRampSponsorship_OfferTopUpOnlyForVerifiedUsers(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	svc := newTestRampSponsorship(r)
	offer, err := svc.Offer(context.Background(), topUpUser(r), orderID)
	if err != nil || offer.Mode != SponsorModeTopUp {
		t.Fatalf("verified: offer = %+v, err = %v", offer, err)
	}
	u := topUpUser(r)
	u.VerificationStatus = models.VerificationStatusPending
	offer, err = svc.Offer(context.Background(), u, orderID)
	if err != nil || offer.Mode != SponsorModeNone {
		t.Fatalf("unverified: offer = %+v, err = %v; want none", offer, err)
	}
}

// An EIP-7702 wallet signing an authorization would only earn a revert, so the
// offer switches it to a top-up (KYC-gated like any other top-up).
func TestRampSponsorship_OfferTopUpForWalletWithCode(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	r.chain.code = map[string][]byte{strings.ToLower(sponsorTestUser): eip7702Code}
	svc := newTestRampSponsorship(r)
	offer, err := svc.Offer(context.Background(), topUpUser(r), orderID)
	if err != nil || offer.Mode != SponsorModeTopUp || offer.Domain != nil {
		t.Fatalf("verified: offer = %+v, err = %v; want top-up", offer, err)
	}
	u := topUpUser(r)
	u.VerificationStatus = models.VerificationStatusPending
	offer, err = svc.Offer(context.Background(), u, orderID)
	if err != nil || offer.Mode != SponsorModeNone {
		t.Fatalf("unverified: offer = %+v, err = %v; want none", offer, err)
	}
}

// An unreadable wallet keeps authorization: the relay simulates before it
// broadcasts, so a wrong guess costs a refused request, not gas.
func TestRampSponsorship_OfferKeepsAuthorizationWhenCodeUnreadable(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	r.chain.codeErr = errors.New("rpc down")
	offer, err := newTestRampSponsorship(r).Offer(context.Background(), topUpUser(r), orderID)
	if err != nil || offer.Mode != SponsorModeAuthorization {
		t.Fatalf("offer = %+v, err = %v; want authorization", offer, err)
	}
}

func TestRampSponsorship_RoutesHideTopUpForUnverifiedAndKilledLanes(t *testing.T) {
	r, _ := newSponsorRig("bsc", "USDT")
	svc := newTestRampSponsorship(r)
	if got := svc.Routes(topUpUser(r)); len(got) != 2 {
		t.Fatalf("verified routes = %+v, want bsc USDC + USDT", got)
	}
	u := topUpUser(r)
	u.VerificationStatus = models.VerificationStatusPending
	if got := svc.Routes(u); len(got) != 0 {
		t.Fatalf("unverified routes = %+v, want none (bsc is top-up only)", got)
	}
	r.lane.Guard.SetKillSwitch(true)
	if got := svc.Routes(topUpUser(r)); len(got) != 0 {
		t.Fatalf("killed lane routes = %+v, want none", got)
	}
}

func TestRampSponsorship_FloatBalancesReportsFloor(t *testing.T) {
	r, _ := newSponsorRig("bsc", "USDT")
	r.chain.balances[sponsorTestSigner] = big.NewInt(500)
	floats, err := newTestRampSponsorship(r).FloatBalances(context.Background())
	if err != nil || len(floats) != 1 {
		t.Fatalf("floats = %+v, err = %v", floats, err)
	}
	if floats[0].BalanceWei.Cmp(big.NewInt(500)) != 0 || floats[0].FloorWei.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("float = %+v", floats[0])
	}
}
