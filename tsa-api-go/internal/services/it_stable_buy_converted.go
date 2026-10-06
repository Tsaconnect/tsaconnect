package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ITConversionRefPrefix marks the conversion record of an IT-settled
// converted buy; the rest of the reference is the intent's id (spec §15.4).
const ITConversionRefPrefix = "ITC-"

const (
	FeeProviderLiFi     = "LiFi"
	ConversionLiFiLabel = "Conversion (LiFi)"
)

var ErrStableBuyAbandonCapReached = errors.New("too many unpaid purchases were cancelled or expired today")

func ITConversionRef(intentID uuid.UUID) string { return ITConversionRefPrefix + intentID.String() }

func IsITConversion(o *models.PaycrestOrderRecord) bool {
	return o != nil && strings.HasPrefix(o.Reference, ITConversionRefPrefix)
}

// ConvertedTreasury is the slice of the conversion *Treasury an open needs.
type ConvertedTreasury interface {
	Lane(chainKey string) *TreasuryLane
	CheckCaps(chainKey string, orderUSD, inFlightUSD float64) error
}

// ConvertedBuys is the conversion treasury as converted OPENS and the
// operator's attach-hash see it. Empty: no converted buy opens.
type ConvertedBuys struct {
	TreasuryAddress string
	Treasury        ConvertedTreasury
	Verifiers       map[string]ERC20TransferVerifier // by chain key: the attach-hash pre-check
	Probes          map[string]TreasuryTxProbe       // by chain key: its depth check
}

func (s *ITService) SetConvertedBuys(c ConvertedBuys) {
	c.TreasuryAddress = strings.TrimSpace(c.TreasuryAddress)
	s.converted = c
}

// ConvertedOpen is what a converted open needs beyond the leg's quote: the
// venue the pricer gave for her target at this D, and her own wallet.
type ConvertedOpen struct {
	Venue         PaycrestVenue
	WalletAddress string
}

// convertedTreasuryFor is the conversion treasury's address for a leg on
// chain, once its lane exists and its advisory exposure caps take D (the
// orchestrator runs the authoritative check before it moves money).
func (s *ITService) convertedTreasuryFor(chain string, q *StableBuyQuote, v PaycrestVenue) (string, error) {
	c := s.converted
	chain = strings.ToLower(strings.TrimSpace(chain))
	if c.TreasuryAddress == "" || c.Treasury == nil || c.Treasury.Lane(chain) == nil {
		return "", fmt.Errorf("%w: no conversion treasury on %s", ErrConversionUnavailable, chain)
	}
	// T must be what this D owes at the venue's budget, and something: a
	// budget outside [0, 100%) would open a buy that owes her nothing or more
	// than was bought.
	if v.Kind != models.RouteKindConverted || !strings.EqualFold(v.ChainKey, chain) || !strings.EqualFold(v.Token, q.Asset) ||
		v.MarkupBps < 0 || v.MarkupBps >= 10_000 || ConversionOwed(q.AssetUnits, v.MarkupBps).Sign() <= 0 ||
		v.TokenAmountBaseUnits != ConversionOwed(q.AssetUnits, v.MarkupBps).String() {
		return "", fmt.Errorf("%w: the converted venue was not priced on this settlement leg", ErrConversionUnavailable)
	}
	if err := c.Treasury.CheckCaps(chain, StableFromBaseUnits(q.AssetUnits), 0); err != nil {
		return "", fmt.Errorf("%w: %s", ErrConversionUnavailable, RedactAlertText(err.Error()))
	}
	return c.TreasuryAddress, nil
}

// newConversionRecord is the row the orchestrator drives once the leg lands.
func newConversionRecord(intent *models.ITIntent, in ConvertedOpen, holder string) *models.PaycrestOrderRecord {
	v := in.Venue
	chain := strings.ToLower(intent.DestChain)
	return &models.PaycrestOrderRecord{
		UserID:                 intent.UserID,
		Direction:              models.PaycrestDirectionBuy,
		Status:                 models.PaycrestStatusAwaitingPayment,
		Reference:              ITConversionRef(intent.ID),
		FiatCode:               intent.Currency,
		FiatAmount:             FormatMinor(intent.AmountMinor),
		Token:                  intent.Asset,
		ChainKey:               chain,
		Network:                chain,
		Rate:                   v.Rate,
		TokenAmount:            FormatStableUnits(intent.AssetAmount), // D, a decimal like Paycrest's own rows
		WalletAddress:          in.WalletAddress,
		RouteKind:              models.RouteKindConverted,
		RequestedChain:         v.RequestedChain,
		RequestedToken:         v.RequestedToken,
		RequestedChainID:       v.RequestedChainID,
		RequestedTokenAddress:  v.RequestedAddress,
		RequestedTokenDecimals: v.RequestedDecimals,
		DestinationAddress:     firstNonEmpty(v.RequestedDestination, in.WalletAddress),
		MarkupBps:              v.MarkupBps,
		QuotedAmount:           v.TokenAmountBaseUnits, // T, checked against this D by convertedTreasuryFor
		EstimatedAmount:        v.EstimatedAmount,
		PromisedAmount:         v.PromisedAmount,
		Holder:                 holder,
		PayAmount:              FormatMinor(intent.AmountMinor),
		ValidUntil:             intent.RateExpiresAt,
	}
}

// OpenTreasuryConvertedBuy opens a route-1 converted buy (spec §15): the
// company wallet's purchase delivering USDC to the conversion treasury —
// every limit of OpenTreasuryBuy, the stock taken under the lock — and its
// conversion record, in the SAME transaction (Decision C4).
func (s *ITService) OpenTreasuryConvertedBuy(ctx context.Context, userID uuid.UUID, q *StableBuyQuote, chain string, in ConvertedOpen) (*models.ITIntent, *StableBuyInstructions, *models.PaycrestOrderRecord, error) {
	if q == nil {
		return nil, nil, nil, errors.New("it treasury converted buy: nil quote")
	}
	// Her own wallet is where the conversion delivers by default: refuse an
	// unusable one before anything is written.
	in.WalletAddress = strings.TrimSpace(in.WalletAddress)
	if !common.IsHexAddress(in.WalletAddress) {
		return nil, nil, nil, fmt.Errorf("%w: a converted buy needs her EVM wallet", ErrBadDeliveryAddress)
	}
	treasury, err := s.convertedTreasuryFor(chain, q, in.Venue)
	if err != nil {
		return nil, nil, nil, err
	}
	var rec *models.PaycrestOrderRecord
	intent, payInto, err := s.openTreasuryBuy(ctx, userID, q, chain, treasury, func(tx *gorm.DB, intent *models.ITIntent) error {
		rec = newConversionRecord(intent, in, models.HolderCompanyWallet)
		return tx.Create(rec).Error
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return intent, payInto, rec, nil
}

// ConversionFor is the conversion record a buy feeds, or nil. It reads the
// database only: the record existing is what makes a delivery feed a
// conversion, never runtime wiring (N1).
func (s *ITService) ConversionFor(ctx context.Context, intent *models.ITIntent) (*models.PaycrestOrderRecord, error) {
	if intent == nil || intent.Direction != models.ITDirectionBuy || !IsRoutedStableBuy(intent) {
		return nil, nil
	}
	return conversionRecordOn(ctx, s.db, intent.ID)
}

func conversionRecordOn(ctx context.Context, db *gorm.DB, intentID uuid.UUID) (*models.PaycrestOrderRecord, error) {
	var rows []models.PaycrestOrderRecord
	if err := db.WithContext(ctx).Where("reference = ?", ITConversionRef(intentID)).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// markConversion moves the intent's record off one of `from` in one guarded
// write. No record (a direct buy) is a no-op.
func markConversion(ctx context.Context, db *gorm.DB, intentID uuid.UUID, from []string, updates map[string]any) error {
	return db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Where("reference = ? AND status IN ?", ITConversionRef(intentID), from).Updates(updates).Error
}

// ConvertedTerms are a converted venue's economics on a delivered leg (spec
// §15.7): T, the all-in rate, and the fee lines — the leg's own plus the
// conversion budget as "Conversion (LiFi)".
type ConvertedTerms struct {
	OwedUnits          *big.Int
	EffectiveRateMinor *big.Int
	Fees               []StableBuyFee
}

func NewConvertedTerms(q *StableBuyQuote, v *PaycrestVenue) (*ConvertedTerms, error) {
	if q == nil || v == nil || q.AssetUnits == nil || q.FiatMinor == nil {
		return nil, errors.New("converted terms: missing quote or venue")
	}
	owed := ConversionOwed(q.AssetUnits, v.MarkupBps)
	if owed.Sign() <= 0 {
		return nil, fmt.Errorf("converted terms: nothing owed at a %d bps budget", v.MarkupBps)
	}
	fees := append(append([]StableBuyFee{}, q.Fees...), conversionFee(q.AssetUnits, owed, displayRateOf(q)))
	return &ConvertedTerms{OwedUnits: owed, EffectiveRateMinor: ratePerDeliveredUnit(q.FiatMinor, owed), Fees: fees}, nil
}

// ConversionFeeLine is the same line for a stored order: D − T at the
// intent's display rate.
func ConversionFeeLine(intent *models.ITIntent, rec *models.PaycrestOrderRecord) *StableBuyFee {
	d, okD := new(big.Int).SetString(strings.TrimSpace(intent.AssetAmount), 10)
	rate, okR := new(big.Int).SetString(strings.TrimSpace(StableBuyDisplayRate(intent)), 10)
	if !okD || !okR || rec == nil {
		return nil
	}
	f := conversionFee(d, ConversionOwed(d, rec.MarkupBps), rate)
	return &f
}

func conversionFee(delivered, owed, displayRate *big.Int) StableBuyFee {
	units := new(big.Int).Sub(delivered, owed)
	ngn := new(big.Int).Mul(units, displayRate)
	ngn.Quo(ngn, stableUnitsPerToken())
	return StableBuyFee{Kind: FeeKindConversion, Label: ConversionLiFiLabel, Provider: FeeProviderLiFi,
		Units: units.String(), NgnMinor: ngn.String()}
}

func displayRateOf(q *StableBuyQuote) *big.Int {
	if q.DisplayRateMinor != nil {
		return q.DisplayRateMinor
	}
	return q.RatePerUnitMinor
}

// checkRoutedAbandonCap refuses after too many unpaid routed buys in 24h —
// enforced here so every entry point gets it (N2), not only the adapters.
func (s *ITService) checkRoutedAbandonCap(ctx context.Context, userID uuid.UUID) error {
	limit := s.RoutedAbandonCap()
	if limit <= 0 {
		return nil
	}
	n, err := s.CountAbandonedRoutedStableBuys(ctx, userID, time.Now().Add(-24*time.Hour))
	if err != nil {
		return err
	}
	if n >= int64(limit) {
		log.Printf("[it stable buy] METRIC ramp_abandon_cap_hit at=service user=%s abandoned=%d cap=%d", userID, n, limit)
		return ErrStableBuyAbandonCapReached
	}
	return nil
}
