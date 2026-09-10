package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Stablecoin buys as a source of inbound naira claims (central-account design
// §6.2).
//
// Unlike the P2P source there is no second party here: the claim's user is the
// buyer, the buyer is who gets the stablecoin, and the money they send is the
// money that funds their own purchase. The interesting part is entirely in the
// key change — attribution moves from a Flutterwave charge id, which the
// provider echoed back to us, to a NIBSS session id we can only observe.

// ITStableClaimSource implements ClaimSource for stablecoin purchases awaiting
// a bank transfer.
type ITStableClaimSource struct {
	it *ITService
}

// NewITStableClaimSource wires the stablecoin buy product into the matcher.
func NewITStableClaimSource(it *ITService) *ITStableClaimSource {
	return &ITStableClaimSource{it: it}
}

// itStableClaimRow is the projection OpenClaims scans.
type itStableClaimRow struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	NgnKobo   string
	CreatedAt time.Time
}

// OpenClaims returns every stablecoin purchase currently awaiting payment.
//
// Bounded by stableBuyClaimWindow, the same 24h bound settleStableBuyFromCharge
// uses. Offering an older intent would let a credit settle a purchase the settle
// path itself would refuse — the matcher would bind it, Accept would decline it,
// and the credit would look attributed while nothing happened.
func (s *ITStableClaimSource) OpenClaims(ctx context.Context, currency string) ([]InboundClaim, error) {
	cur := normaliseCurrency(currency)
	if cur == "" {
		return nil, errors.New("it stable claims: empty currency")
	}
	if s.it == nil || s.it.db == nil {
		return nil, errors.New("it stable claims: service not wired")
	}

	var rows []itStableClaimRow
	if err := s.it.db.WithContext(ctx).
		Table("it_intents").
		Select("id, user_id, ngn_kobo, created_at").
		Where("direction = ? AND status = ? AND asset IN ? AND currency = ? AND created_at >= ?",
			models.ITDirectionBuy, models.ITStatusPendingPayment,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT}, cur,
			time.Now().Add(-stableBuyClaimWindow)).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	claims := make([]InboundClaim, 0, len(rows))
	for _, r := range rows {
		kobo, ok := new(big.Int).SetString(strings.TrimSpace(r.NgnKobo), 10)
		if !ok || kobo.Sign() <= 0 || !kobo.IsInt64() {
			log.Printf("[inbound it stable] intent=%s has unusable ngn_kobo %q; not offered as a claim", r.ID, r.NgnKobo)
			continue
		}
		claims = append(claims, InboundClaim{
			Type:   InboundClaimTypeITStable,
			ID:     r.ID,
			UserID: r.UserID,
			// Rounded with the SAME helper the matcher rounds the credit with,
			// so the two sides cannot round in opposite directions. Central
			// mode already mints every ask at whole naira, so this only bites
			// on intents created before the cutover — and rounding them keeps
			// them visible to the matcher rather than silently dropping a
			// purchase somebody has already paid for.
			AmountKobo: roundKoboToNaira(kobo.Int64()),
			OpenedAt:   r.CreatedAt,
		})
	}
	return claims, nil
}

// Accept funds the purchase the matcher bound this credit to.
//
// It drives the EXISTING settle path — price at the live rate on what actually
// landed, claim in one status-guarded write — with the NIBSS session id
// standing in for the Flutterwave charge id. Nothing downstream changes:
// ConvertStableBuy and DeliverStableBuy read payment_ref without caring which
// kind of reference is in it.
//
// Idempotent on credit.ProviderRef through payment_ref, which carries a partial
// unique index. A replayed credit finds its own intent and finishes any pricing
// the first attempt was interrupted before completing.
func (s *ITStableClaimSource) Accept(ctx context.Context, claimID uuid.UUID, credit *models.InboundCredit) error {
	if credit == nil {
		return errors.New("it stable accept: nil credit")
	}
	ref := strings.TrimSpace(credit.ProviderRef)
	if ref == "" {
		return errors.New("it stable accept: credit has no provider ref")
	}
	if s.it == nil {
		return ErrITNotConfigured
	}

	replayed, _, err := s.it.finishStableBuyReplay(ctx, ref, false)
	if err != nil {
		return err
	}
	if replayed {
		return nil
	}

	var intent models.ITIntent
	if err := s.it.db.WithContext(ctx).First(&intent, "id = ?", claimID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrStableIntentNotFound
		}
		return err
	}
	if intent.Direction != models.ITDirectionBuy || !IsStableAsset(intent.Asset) {
		// The matcher handed us a claim id this source never produced.
		return ErrITBadState
	}
	if intent.Status != models.ITStatusPendingPayment {
		// Advanced between the matcher's read and ours — most plausibly a
		// concurrent poller tick that already settled it. The replay check
		// above found no intent carrying THIS ref, so this credit is not the
		// one that funded it; refusing lets the matcher hold it for an operator
		// rather than silently attributing money to an already-funded purchase.
		return ErrITBadState
	}

	// The credit's GROSS is what the buyer actually sent — AmountKobo is net of
	// any provider charge that was netted out of it. Settling on the net would
	// quietly short every buyer by the funding fee.
	gross := credit.GrossKobo
	if gross <= 0 {
		gross = credit.AmountKobo
	}
	if gross <= 0 {
		return fmt.Errorf("it stable accept: credit %s has no usable amount", ref)
	}

	// parkOnBadRate=false. A rate outage must NOT park this intent for an
	// operator: the credit has a durable inbound_credits row and the poller
	// re-reads it every 30 seconds, which is a strictly better retry than the
	// webhook path ever had. Parking would turn a transient provider blip into
	// a manual recovery, and a parked intent is one an operator has to resume
	// by hand before the buyer sees anything at all.
	claimed, err := s.it.claimAndPriceStableBuy(ctx, &intent, ref, fmt.Sprintf("%d", gross), false)
	if err != nil {
		return err
	}
	if !claimed {
		// Lost the claim race and the winner did not use this ref. Same
		// reasoning as the status check above: hold rather than attribute.
		return ErrITBadState
	}
	return nil
}

// Compile-time proof that this satisfies the matcher's seam — see the same
// assertion in inbound_claim_p2p.go.
var _ ClaimSource = (*ITStableClaimSource)(nil)
