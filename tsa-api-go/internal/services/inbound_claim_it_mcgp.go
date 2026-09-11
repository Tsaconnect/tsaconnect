package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ITMCGPClaimSource makes MCGP buy orders visible to the inbound matcher
// (central-account design §6.1).
//
// It is the whole of what the matcher knows about MCGP: which orders are
// waiting for money, and how to hand one of them a credit. Every rule about
// what an MCGP buy IS — the 24h claim window, the minted-reference guard, live
// repricing, fee deduction, the on-chain broadcast — stays inside ITService,
// where it already lives and is already tested.
type ITMCGPClaimSource struct {
	db *gorm.DB
	it *ITService
}

// NewITMCGPClaimSource wires the MCGP buy leg into inbound attribution.
func NewITMCGPClaimSource(db *gorm.DB, it *ITService) *ITMCGPClaimSource {
	return &ITMCGPClaimSource{db: db, it: it}
}

// OpenClaims returns every MCGP buy order currently awaiting a transfer.
//
// The filters are the same three AutoCreditFromVA already applies when it looks
// for an order to settle into, and each one is load-bearing:
//
//   - MCGP only (or the pre-Asset-column legacy rows). A stablecoin purchase is
//     a different product with its own claim source; matching one here would
//     credit MCGP for a USDC order.
//   - Still carrying its MINTED payment_ref. Settlement overwrites payment_ref
//     with the provider's identifier the moment money is attributed, so a row
//     without its minted ref already has money landing against it and must
//     never be offered as open. This is also what stops the poller re-offering
//     an order it settled seconds ago but whose broadcast is still in flight.
//   - Inside buyOrderClaimWindow. Nothing else expires an MCGP order, so an
//     abandoned one would otherwise lie in wait forever and swallow the user's
//     next unrelated transfer — which under central-account matching is far
//     worse than under VAs, because here it also occupies an amount slot and
//     can turn someone else's clean match into a contested hold.
func (s *ITMCGPClaimSource) OpenClaims(ctx context.Context, currency string) ([]InboundClaim, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("it mcgp claims: not configured")
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = itDepositCurrency
	}
	// MCGP buys are collected in naira only. Answering an empty list for any
	// other currency is not a shortcut — offering an NGN-priced order against,
	// say, a GHS credit would compare two different units of money.
	if currency != itDepositCurrency {
		return nil, nil
	}

	var rows []models.ITIntent
	if err := s.db.WithContext(ctx).
		Where("direction = ? AND status = ? AND (asset IS NULL OR asset = '' OR asset = ?) "+
			"AND payment_ref LIKE ? AND currency = ? AND created_at >= ?",
			models.ITDirectionBuy, models.ITStatusPendingPayment, models.ITAssetMCGP,
			mintedRefPrefix+"%", currency, time.Now().Add(-buyOrderClaimWindow)).
		Order("created_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	claims := make([]InboundClaim, 0, len(rows))
	for i := range rows {
		kobo, err := strconv.ParseInt(strings.TrimSpace(rows[i].NgnKobo), 10, 64)
		if err != nil || kobo <= 0 {
			// An order with no readable price cannot be matched on amount, and
			// silently offering it as a zero-naira claim would let it match a
			// zero-naira credit that will never exist. Log and skip: the row is
			// still visible to the operator in the admin hub.
			log.Printf("[inbound-claim it_mcgp] WARNING intent=%s has unusable ngn_kobo %q — not offered as a claim",
				rows[i].ID, rows[i].NgnKobo)
			continue
		}
		claims = append(claims, InboundClaim{
			Type:   InboundClaimTypeITMCGP,
			ID:     rows[i].ID,
			UserID: rows[i].UserID,
			// Rounded with the SAME helper the matcher rounds the credit with,
			// exactly as both sibling sources do. Central mode mints every ask
			// at whole naira, so this only bites on orders opened before the
			// cutover — but an order offered at, say, 123456 kobo can never
			// equal the 123500 the matcher compares against, so it would hold
			// forever with the buyer's money already sent. It is also what
			// keeps the InboundClaim contract's AmountKobo%100 == 0 true.
			AmountKobo: RoundKoboToNaira(kobo),
			// The order's OWN currency, not the argument echoed back: echoing
			// it would make the matcher's assertion tautological and re-hide
			// the mismatch it exists to catch.
			Currency: normaliseCurrency(rows[i].Currency),
			OpenedAt: rows[i].CreatedAt,
		})
	}
	return claims, nil
}

// Accept settles the credit into the named buy order.
//
// The amount handed over is the credit's gross snapped to whole naira, which is
// the same figure the matcher compared against the order. Passing the raw gross
// instead would credit a fraction of a naira more or less than the user was
// quoted whenever the provider's fee reconstruction lands a kobo out — a
// difference nobody can explain from the receipt.
//
// Idempotency on credit.ProviderRef — the ClaimSource contract's one hard
// requirement — is CreditBuyOrderFromInbound's, all the way down to the
// contract's single-use ref. Nothing is added here on top of it, so a manual
// bind from the admin queue and an automatic one settle through exactly the
// same guards.
func (s *ITMCGPClaimSource) Accept(ctx context.Context, claimID uuid.UUID, credit *models.InboundCredit) error {
	if s == nil || s.it == nil || s.db == nil {
		return errors.New("it mcgp claims: not configured")
	}
	if credit == nil {
		return errors.New("it mcgp claims: nil credit")
	}

	// Re-read the order's currency rather than trusting that OpenClaims filtered
	// it. The manual-assign path names a claim id directly and never runs that
	// filter, so this is the only place the two units of money are ever
	// compared before MCGP is issued against them.
	var order models.ITIntent
	if err := s.db.WithContext(ctx).Select("currency").First(&order, "id = ?", claimID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("it mcgp claims: intent %s not found", claimID)
		}
		return err
	}
	if err := assertClaimCurrency("it mcgp", claimID, order.Currency, credit); err != nil {
		return err
	}

	paid := big.NewInt(RoundKoboToNaira(credit.GrossKobo))
	return s.it.CreditBuyOrderFromInbound(ctx, claimID, credit.ProviderRef, paid)
}

// assertClaimCurrency refuses to move money between two different units of it.
//
// Every source filters by currency in OpenClaims, but the whole bind — the
// matcher's arbitration and the operator's manual assign — sits between that
// filter and the settle. The manual path never runs the filter at all, so
// without this a hand-typed claim id is all it takes to fund a naira order out
// of a cedi transfer. An empty code on either side is a refusal too: unknown is
// not the same as equal, and guessing here spends real money.
//
// Shared by all three claim sources; it is the same check for the same reason
// in each.
func assertClaimCurrency(source string, claimID uuid.UUID, claimCurrency string, credit *models.InboundCredit) error {
	claim := normaliseCurrency(claimCurrency)
	got := normaliseCurrency(credit.Currency)
	if claim == "" || got == "" {
		return fmt.Errorf("%s accept: claim %s currency %q and credit %s currency %q — one of them is unknown, so they cannot be compared",
			source, claimID, claimCurrency, credit.ProviderRef, credit.Currency)
	}
	if claim != got {
		return fmt.Errorf("%s accept: claim %s is priced in %s but credit %s is %s",
			source, claimID, claim, credit.ProviderRef, got)
	}
	return nil
}
