package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

var (
	ErrStableNoDepositAddress = errors.New("it stable: no Flutterwave deposit address configured")
	ErrStableQuoteExpired     = errors.New("it stable: quote has expired")
	ErrStableNoDestination    = errors.New("it stable: a payout destination is required")
)

// stableDepositChain is where every sell deposit is routed. Flutterwave accepts
// inbound stablecoins on Polygon and Ethereum only; Polygon is the cheaper and
// is Flutterwave's own documented default. Defined in terms of feeChainPolygon
// (fee_quote_service.go) rather than a second "polygon" literal, uppercased
// because the rest of the stable ramp (SourceChain, deliveryMatrix) keys chains
// upper-case.
var stableDepositChain = strings.ToUpper(feeChainPolygon)

// StableDepositInstructions tells the app where to route the user's funds.
//
// Address is FLUTTERWAVE'S address, never one of ours: the user's stablecoin
// goes straight from their wallet to the provider, so the platform is never in
// the custody path.
type StableDepositInstructions struct {
	Chain           string   `json:"chain"`
	Address         string   `json:"address"`
	Asset           string   `json:"asset"`
	AmountBaseUnits *big.Int `json:"-"`
	AmountDisplay   string   `json:"amount"`
}

// SetStableDepositAddress records Flutterwave's own deposit address for a
// chain. Flutterwave exposes it on the dashboard only — there is no API to
// fetch it — so it is copied in by configuration.
func (s *ITService) SetStableDepositAddress(chain, address string) {
	if s.stableDepositAddrs == nil {
		s.stableDepositAddrs = map[string]string{}
	}
	s.stableDepositAddrs[strings.ToUpper(strings.TrimSpace(chain))] = strings.TrimSpace(address)
}

func (s *ITService) stableDepositAddress(chain string) (string, error) {
	addr := s.stableDepositAddrs[strings.ToUpper(strings.TrimSpace(chain))]
	if addr == "" {
		return "", fmt.Errorf("%w: %s", ErrStableNoDepositAddress, chain)
	}
	if err := ValidateDeliveryAddress(chain, addr); err != nil {
		return "", fmt.Errorf("configured deposit address is invalid: %w", err)
	}
	return addr, nil
}

// CreateStableSellIntent records a priced sell and returns where the user must
// send their stablecoin.
//
// Nothing is paid out here and nothing is reserved: the payout is funded
// entirely by the user's own funds once they provably arrive at Flutterwave
// (see the watcher). The intent is the ledger of that promise, not a claim on
// any balance of ours.
func (s *ITService) CreateStableSellIntent(
	ctx context.Context,
	userID uuid.UUID,
	q *StableSellQuote,
	sourceChain string,
	bank *ITSellBank,
) (*models.ITIntent, *StableDepositInstructions, error) {
	if q == nil {
		return nil, nil, errors.New("it stable: nil quote")
	}
	if time.Now().After(q.ExpiresAt) {
		return nil, nil, ErrStableQuoteExpired
	}
	if bank == nil || (strings.TrimSpace(bank.Number) == "" && strings.TrimSpace(bank.MobileMsisdn) == "") {
		return nil, nil, ErrStableNoDestination
	}
	addr, err := s.stableDepositAddress(stableDepositChain)
	if err != nil {
		return nil, nil, err
	}

	expires := q.ExpiresAt
	intent := &models.ITIntent{
		ID:            uuid.New(),
		UserID:        userID,
		Direction:     models.ITDirectionSell,
		Status:        models.ITStatusPendingDeposit,
		Asset:         q.Asset,
		AssetAmount:   q.AssetAmount.String(),
		Currency:      q.Currency,
		SourceChain:   strings.ToUpper(strings.TrimSpace(sourceChain)),
		NgnKobo:       q.GrossMinor.String(),
		NetNgnKobo:    q.NetMinor.String(),
		RateKobo:      q.RatePerUnitMinor.String(),
		LockedRate:    q.RatePerUnitMinor.String(),
		RateExpiresAt: &expires,

		BankCode:            strings.TrimSpace(bank.Code),
		AccountNumber:       strings.TrimSpace(bank.Number),
		AccountName:         strings.TrimSpace(bank.Name),
		DestinationCurrency: strings.TrimSpace(bank.DestinationCurrency),
		RailType:            strings.TrimSpace(bank.RailType),
		MobileNetwork:       strings.TrimSpace(bank.MobileNetwork),
		MobileCountry:       strings.TrimSpace(bank.MobileCountry),
		MobileMsisdn:        strings.TrimSpace(bank.MobileMsisdn),
	}
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, nil, err
	}

	return intent, &StableDepositInstructions{
		Chain:           stableDepositChain,
		Address:         addr,
		Asset:           q.Asset,
		AmountBaseUnits: q.AssetAmount,
		AmountDisplay:   fmt.Sprintf("%.6f", StableFromBaseUnits(q.AssetAmount)),
	}, nil
}
