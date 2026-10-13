package services

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// PoolLegOpen: the pool can take a naira buy now — wired, gates open, the
// release key able to pay an order's releases, central account active, not
// stopped (§11.1 RampLegs).
func (s *ITService) PoolLegOpen(ctx context.Context) bool {
	if !s.HasPool() || s.payInChannelNow() != PayInCentral || CurrentPoolSettings().OrdersStopped {
		return false
	}
	d := s.poolDesk
	return d.c.Gates.Open() && d.c.Gates.ReleaseGasOK(ctx, CurrentPoolSettings().SliceCap)
}

// PoolVenue is the pool's one direct venue: its chain key and token.
func (s *ITService) PoolVenue() (string, string) {
	if !s.HasPool() {
		return "", ""
	}
	return s.poolDesk.ChainKey(), s.poolDesk.TokenSymbol()
}

// PoolSettingsNow is the settings snapshot a quote or open uses.
func (s *ITService) PoolSettingsNow() PoolSettings { return CurrentPoolSettings() }

// PoolBandBps is pool_band (§11.2).
func (s *ITService) PoolBandBps() int64 { return CurrentPoolSettings().BandBps }

// PoolCanFill is §9.3 step 4 on cached stock: Σ top-N free ≥ units.
func (s *ITService) PoolCanFill(ctx context.Context, units *big.Int, set PoolSettings) bool {
	if !s.HasPool() {
		return false
	}
	free, err := s.poolDesk.FreeTopN(ctx, set)
	return err == nil && free.Cmp(units) >= 0
}

// OpenPoolConvertedBuy opens a converted pool order (§11.1): the pool chain's
// token delivered to the conversion treasury, and its record (holder tsa_pool,
// the leg's own decimals) in the open's transaction. Custody (S22): until the
// settle engine hands off with the first release's hash, takeCustody waits.
func (s *ITService) OpenPoolConvertedBuy(ctx context.Context, userID uuid.UUID, p *PoolPrice, in ConvertedOpen) (*models.ITIntent, *StableBuyInstructions, *models.PaycrestOrderRecord, error) {
	if !s.HasPool() || p == nil {
		return nil, nil, nil, ErrPoolClosed
	}
	in.WalletAddress = strings.TrimSpace(in.WalletAddress)
	if !common.IsHexAddress(in.WalletAddress) {
		return nil, nil, nil, fmt.Errorf("%w: a converted buy needs her EVM wallet", ErrBadDeliveryAddress)
	}
	treasury, err := s.convertedTreasuryFor(s.poolDesk.ChainKey(), p.Quote, in.Venue)
	if err != nil {
		return nil, nil, nil, err
	}
	var rec *models.PaycrestOrderRecord
	intent, payInto, err := s.openPoolBuy(ctx, userID, p, treasury, func(tx *gorm.DB, intent *models.ITIntent) error {
		rec = newConversionRecord(intent, in, models.HolderPool)
		return tx.Create(rec).Error
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return intent, payInto, rec, nil
}

// PoolCard is the pinned P2P card (§15, decision 12): rate and limits without
// an amount. Enabled is false while the pool is dormant (P17).
type PoolCard struct {
	Enabled           bool           `json:"enabled"`
	Name              string         `json:"name,omitempty"`
	Badge             string         `json:"badge,omitempty"`
	Available         bool           `json:"available"`
	Rate              string         `json:"rate,omitempty"` // display rate, naira per token
	FundingFeePercent string         `json:"fundingFeePercent,omitempty"`
	MinNgn            string         `json:"minNgn,omitempty"`
	MaxNgn            string         `json:"maxNgn,omitempty"`
	Token             *PoolCardToken `json:"token,omitempty"`
	Chain             *PoolCardChain `json:"chain,omitempty"`
}

type PoolCardToken struct {
	Symbol   string `json:"symbol"`
	Address  string `json:"address"`
	Decimals int    `json:"decimals"`
}

type PoolCardChain struct {
	ID   int64  `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

func (s *ITService) PoolCard(ctx context.Context) (*PoolCard, error) {
	if !s.HasPool() {
		return &PoolCard{Enabled: false}, nil
	}
	d := s.poolDesk
	card := &PoolCard{Enabled: true, Name: "TSA Connect", Badge: "Recommended",
		Token: &PoolCardToken{Symbol: d.c.Token.Symbol, Address: d.c.Token.Address, Decimals: d.c.Token.Decimals},
		Chain: &PoolCardChain{ID: d.c.Token.ChainID, Key: d.c.Token.ChainKey, Name: d.c.ChainName}}
	set := CurrentPoolSettings()
	if !s.PoolLegOpen(ctx) || s.treasuryRates == nil {
		return card, nil
	}
	mid, err := s.treasuryRates.BuyRate(ctx, "NGN")
	if err != nil {
		return card, nil
	}
	funding, err := s.fundingCost(PayInCentral)
	if err != nil {
		return card, nil
	}
	display := inHouseDisplayRate(poolBaseRate(mid, set), set.TakeBps) // the quote's own rate (review M-2)
	free, err := d.FreeTopN(ctx, set)
	if err != nil || free.Cmp(set.MinSliceUnits) < 0 {
		return card, nil
	}
	maxUnits := free
	if set.OrderMaxUnits.Cmp(maxUnits) < 0 {
		maxUnits = set.OrderMaxUnits
	}
	card.Available = true
	card.Rate = FormatMinor(display.String())
	card.FundingFeePercent = formatBps(funding.bps)
	// The smallest order is one effective minimum slice plus its flat costs
	// (gas and one payout fee), which come off after funding.
	lpRate := floorRatMinor(poolBaseRate(mid, set))
	minSlice := poolEffectiveMinSlice(set.MinSliceUnits, lpRate)
	minLeft := new(big.Int).Add(minSlice, poolNetworkFeeUnits(1, set))
	minLeft.Add(minLeft, poolPayoutFeeUnits([]*big.Int{minSlice}, lpRate, display))
	card.MinNgn = FormatMinor(fiatForNet(minLeft, display, funding.bps, true).String())
	card.MaxNgn = FormatMinor(fiatForNet(maxUnits, display, funding.bps, false).String())
	return card, nil
}

// fiatForNet is the whole-naira amount (in kobo) that leaves about `net`
// units after funding: net ÷ (1 − f) tokens at the display rate. Rounded up for a
// minimum (the quote then clears it), down for a maximum. The quote is the
// authority; the card only frames it.
func fiatForNet(net, display *big.Int, fundingBps int64, up bool) *big.Int {
	left := new(big.Rat).SetFrac(new(big.Int).Mul(net, big.NewInt(10_000)), big.NewInt(10_000-fundingBps))
	kobo := new(big.Rat).Quo(new(big.Rat).Mul(left, new(big.Rat).SetInt(display)), new(big.Rat).SetInt(stableUnitsPerToken()))
	naira := new(big.Rat).Quo(kobo, big.NewRat(100, 1))
	q, r := new(big.Int).QuoRem(naira.Num(), naira.Denom(), new(big.Int))
	if up && r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Mul(q, big.NewInt(100))
}
