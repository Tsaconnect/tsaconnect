package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

const preconditionProof = "https://res.cloudinary.com/demo/image/upload/proof.png"

// gateRefused: the Prepare* call refused on the caller or the trade/ad state
// (its gate), as opposed to succeeding or failing later on a tx build.
func gateRefused(err error) bool {
	for _, s := range []error{ErrForbidden, ErrInvalidTransition, ErrAdNotActive, ErrSelfTrade, ErrAdHasOpenTrades,
		ErrNotApplicableForBuyAd, ErrAssetNotTradeable, ErrKYCVerificationRequired} {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

// Every trade action's gas need says yes exactly when its Prepare* call gets
// past its gate, over one table of status × caller × expiry.
func TestP2PGasNeed_TradeActionsMatchPrepareGates(t *testing.T) {
	stranger := uuid.New()
	statuses := []string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid, models.P2PTradeStatusReleasing,
		models.P2PTradeStatusReleased, models.P2PTradeStatusCancelled, models.P2PTradeStatusDisputed}
	prepare := map[P2PGasAction]func(f *gasNeedFixture, id, caller uuid.UUID) error{
		P2PGasMarkPaid: func(f *gasNeedFixture, id, caller uuid.UUID) error {
			_, _, err := f.svc.PrepareMarkPaid(context.Background(), id, caller, gasNeedWallet, preconditionProof)
			return err
		},
		P2PGasRelease: func(f *gasNeedFixture, id, caller uuid.UUID) error {
			_, err := f.svc.PrepareRelease(context.Background(), id, caller, gasNeedWallet)
			return err
		},
		P2PGasCancelTrade: func(f *gasNeedFixture, id, caller uuid.UUID) error {
			_, err := f.svc.PrepareCancel(context.Background(), id, caller, gasNeedWallet)
			return err
		},
		P2PGasDispute: func(f *gasNeedFixture, id, caller uuid.UUID) error {
			_, err := f.svc.PrepareRaiseDispute(context.Background(), id, caller, gasNeedWallet, "not paid", nil)
			return err
		},
	}
	for action, prep := range prepare {
		for _, status := range statuses {
			for _, expiry := range []time.Duration{30 * time.Minute, -time.Minute} {
				for _, who := range []string{"buyer", "seller", "stranger"} {
					f := newGasNeedFixture(t)
					caller := map[string]uuid.UUID{"buyer": f.buyer, "seller": f.seller, "stranger": stranger}[who]
					// Fresh trades: a Prepare* may move the one it is given.
					gasID := f.seedTrade(t, status, expiry, 146)
					prepID := f.seedTrade(t, status, expiry, 146)
					name := fmt.Sprintf("%s %s %s expiry=%v", action, who, status, expiry)

					gasErr := f.check(caller, P2PGasRequest{Action: action, TradeID: gasID})
					if gasErr != nil && !errors.Is(gasErr, ErrP2PGasNotNeeded) {
						t.Fatalf("%s: gas need err = %v", name, gasErr)
					}
					prepErr := prep(f, prepID, caller)
					if gasOK, prepOK := gasErr == nil, !gateRefused(prepErr); gasOK != prepOK {
						t.Fatalf("%s: gas need ok=%v but Prepare gate ok=%v (prepare err %v)", name, gasOK, prepOK, prepErr)
					}
				}
			}
		}
	}
}

// Mark-paid and dispute also need the open on chain, as their Prepare* do.
func TestP2PGasNeed_NeverOpenedTradeIsNotNeeded(t *testing.T) {
	f := newGasNeedFixture(t)
	id := f.seedTrade(t, models.P2PTradeStatusPendingPayment, 30*time.Minute, 146)
	if err := f.db.Model(&models.P2PTrade{}).Where("id = ?", id).Update("open_tx_hash", "").Error; err != nil {
		t.Fatal(err)
	}
	wantGasNeed(t, "mark_paid", f.check(f.buyer, P2PGasRequest{Action: P2PGasMarkPaid, TradeID: id}), ErrP2PGasNotNeeded)
	wantGasNeed(t, "dispute", f.check(f.buyer, P2PGasRequest{Action: P2PGasDispute, TradeID: id}), ErrP2PGasNotNeeded)

	f.esc.readTradeErr = errors.New("rpc down")
	if err := f.check(f.buyer, P2PGasRequest{Action: P2PGasMarkPaid, TradeID: id}); err == nil || errors.Is(err, ErrP2PGasNotNeeded) {
		t.Fatalf("a failed chain read must surface as ours, got %v", err)
	}
}

// A verified buyer with no legal name cannot open a sell-ad trade (the
// money-in tier), so no gas is sent for it; a buy-ad open needs only the
// verified status.
func TestP2PGasNeed_OpenTradeAppliesOpenTradeKYCTier(t *testing.T) {
	f := newGasNeedFixture(t)
	nameless := seedVerifiedWithoutLegalName(t, f.db, "")
	buyAd := f.seedAd(t, f.seller, models.P2PAdSideBuy, models.P2PAdStatusActive, 146)

	wantGasNeed(t, "sell ad, no legal name", f.check(nameless, P2PGasRequest{Action: P2PGasOpenTrade, AdID: f.adID}), ErrP2PGasNotNeeded)
	wantGasNeed(t, "buy ad, no legal name", f.check(nameless, P2PGasRequest{Action: P2PGasOpenTrade, AdID: buyAd}), nil)
	wantGasNeed(t, "sell ad, named buyer", f.check(f.buyer, P2PGasRequest{Action: P2PGasOpenTrade, AdID: f.adID}), nil)

	pending := seedUserAt(t, f.db, uuid.New(), models.VerificationStatusPending)
	wantGasNeed(t, "buy ad, unverified", f.check(pending, P2PGasRequest{Action: P2PGasOpenTrade, AdID: buyAd}), ErrP2PGasNotNeeded)
}

// open_trade and cancel_ad say yes exactly when OpenTrade / the cancel call
// for that ad's side gets past its gate.
func TestP2PGasNeed_AdActionsMatchPrepareGates(t *testing.T) {
	f := newGasNeedFixture(t)
	nameless := seedVerifiedWithoutLegalName(t, f.db, "")
	var ads []uuid.UUID
	for _, side := range []string{models.P2PAdSideSell, models.P2PAdSideBuy} {
		for _, status := range []string{models.P2PAdStatusActive, models.P2PAdStatusPaused, models.P2PAdStatusCancelled} {
			ads = append(ads, f.seedAd(t, f.seller, side, status, 146))
		}
	}
	busy := f.seedAd(t, f.seller, models.P2PAdSideSell, models.P2PAdStatusActive, 146)
	f.adID = busy
	f.seedTrade(t, models.P2PTradeStatusPaid, 30*time.Minute, 146)
	ads = append(ads, busy)
	// Signed ads, so each Prepare* gets past its gate to the tx build.
	deadline := f.now.Add(24 * time.Hour)
	if err := f.db.Model(&models.P2PAd{}).Where("id IN ?", ads).Updates(map[string]any{
		"seller_address": "0x000000000000000000000000000000000000d00d", "voucher_nonce": "0x" + strings.Repeat("ab", 32),
		"voucher_deadline": deadline, "voucher_signature": "0x" + strings.Repeat("11", 65),
	}).Error; err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for _, adID := range ads {
		var ad models.P2PAd
		f.db.First(&ad, "id = ?", adID)
		for name, caller := range map[string]uuid.UUID{"seller": f.seller, "buyer": f.buyer, "nameless": nameless} {
			label := fmt.Sprintf("%s ad %s/%s", name, ad.Side, ad.Status)

			gasErr := f.check(caller, P2PGasRequest{Action: P2PGasOpenTrade, AdID: adID})
			_, _, openErr := f.svc.OpenTrade(ctx, adID, caller, gasNeedWallet, OpenTradeParams{FiatAmount: "1500"})
			if gasOK, prepOK := gasErr == nil, !gateRefused(openErr); gasOK != prepOK {
				t.Fatalf("open_trade %s: gas ok=%v, OpenTrade gate ok=%v (%v / %v)", label, gasOK, prepOK, gasErr, openErr)
			}

			gasErr = f.check(caller, P2PGasRequest{Action: P2PGasCancelAd, AdID: adID})
			// A buy-ad cancel has no tx (CancelBuyAd is DB-only), so there is
			// never gas for it: compare against the on-chain cancel only.
			_, cancelErr := f.svc.PrepareCancelAd(ctx, adID, caller, gasNeedWallet)
			if gasOK, prepOK := gasErr == nil, !gateRefused(cancelErr); gasOK != prepOK {
				t.Fatalf("cancel_ad %s: gas ok=%v, PrepareCancelAd gate ok=%v (%v / %v)", label, gasOK, prepOK, gasErr, cancelErr)
			}
		}
	}
}
