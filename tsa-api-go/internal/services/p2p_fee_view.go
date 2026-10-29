package services

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	p2pFeeViewTTL = 60 * time.Second
	// A failed read is retried after this, not after the full TTL: one RPC
	// blip must not hide the fee from every screen for a minute.
	p2pFeeViewFailTTL = 10 * time.Second
	// The shared read is detached from the first caller's request (whose
	// cancel would fail every waiter and cache the failure) and bounded here.
	p2pFeeViewReadTimeout = 10 * time.Second
	// An ad screen waits at most this long for fees; a slower read leaves the
	// field unset (it still lands in the cache for the next request).
	p2pAdFeeBudget = 2 * time.Second
	// Contract ceilings (P2PTradeEscrow MAX_*_FEE_BPS). A rate above them is one
	// the escrow reverts on at open, so it is never shown as the fee.
	p2pMaxPlatformFeeBps = 300
	p2pMaxGasFeeBps      = 300
	p2pMaxTotalFeeBps    = 500
)

// P2P fee features in FeeConfig (P2PTradeEscrow.FEATURE_PLATFORM / FEATURE_GAS).
const (
	p2pFeatureP2PPlatform = "p2p_platform"
	p2pFeatureP2PGas      = "p2p_gas"
)

// p2pRateReader reads one FeeConfig row (active or not) for a chain's core set.
type p2pRateReader func(ctx context.Context, chainID int64, feature string, token common.Address) (FeeConfigRate, error)

// P2PFeeView answers "what fee would a P2P trade of this token on this chain
// snapshot right now", straight from the chain's FeeConfig, so the app never
// hardcodes a rate and follows the owner's setRates without a release. It mirrors
// P2PTradeEscrow._storeTrade; the fee comes out of the crypto receiver.
type P2PFeeView struct {
	rate      p2pRateReader
	tokenAddr func(chainID int64, symbol string) string
	cache     *cachedReader[int]
	now       func() time.Time
}

// NewP2PFeeView builds the view over a FeeConfig row reader and a (chain, symbol) → token address
// resolver.
func NewP2PFeeView(rate p2pRateReader, tokenAddr func(int64, string) string) *P2PFeeView {
	return &P2PFeeView{
		rate:      rate,
		tokenAddr: tokenAddr,
		cache:     newCachedReader[int](p2pFeeViewTTL, p2pFeeViewFailTTL),
		now:       time.Now,
	}
}

// NewP2PFeeViewForCore reads each chain's rows from its core set's FeeConfig. A chain whose set
// has not passed its gates has no fee to show.
func NewP2PFeeViewForCore(core *CoreRegistry, tokenAddr func(int64, string) string) *P2PFeeView {
	return NewP2PFeeView(
		func(ctx context.Context, chainID int64, feature string, token common.Address) (FeeConfigRate, error) {
			set, ok := core.Ready(chainID)
			if !ok {
				return FeeConfigRate{}, fmt.Errorf("%w on chain %d", ErrCoreSetNotReady, chainID)
			}
			return core.Fees().RawRate(ctx, chainID, set.FeeConfig, feature, token)
		},
		tokenAddr,
	)
}

// LiveFeeBps is the total (platform + gas) fee bps a trade of token on chainID
// would snapshot. An error means the chain could not be read, or a trade could not open (a row
// missing, inactive, carrying a flat fee or above the ceilings): callers omit the field rather
// than guess.
func (v *P2PFeeView) LiveFeeBps(ctx context.Context, chainID int64, token string) (int, error) {
	if v == nil {
		return 0, fmt.Errorf("fee view not configured")
	}
	sym := strings.ToUpper(strings.TrimSpace(token))
	key := fmt.Sprintf("%d|%s", chainID, sym)
	now := v.now()
	return v.cache.Get(key, now, func() (int, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p2pFeeViewReadTimeout)
		defer cancel()
		bps, err := v.read(rctx, chainID, sym)
		v.cache.Put(key, v.now(), bps, err)
		return bps, err
	})
}

func (v *P2PFeeView) read(ctx context.Context, chainID int64, sym string) (int, error) {
	tokenAddr := v.tokenAddr(chainID, sym)
	if !common.IsHexAddress(tokenAddr) {
		return 0, fmt.Errorf("%w: %s", ErrUnsupportedToken, sym)
	}
	token := common.HexToAddress(tokenAddr)
	var bps [2]int
	for i, feature := range []string{p2pFeatureP2PPlatform, p2pFeatureP2PGas} {
		r, err := v.rate(ctx, chainID, feature, token)
		if err != nil {
			return 0, err
		}
		// The escrow's rateFor reverts on an inactive row and _storeTrade on a flat fee: no trade opens.
		if !r.Active {
			return 0, fmt.Errorf("%s has no active %s rate on chain %d", sym, feature, chainID)
		}
		if r.FlatFee != nil && r.FlatFee.Sign() != 0 {
			return 0, fmt.Errorf("%s %s rate on chain %d carries a flat fee", sym, feature, chainID)
		}
		bps[i] = int(r.BPS)
	}
	if bps[0] > p2pMaxPlatformFeeBps || bps[1] > p2pMaxGasFeeBps || bps[0]+bps[1] > p2pMaxTotalFeeBps {
		return 0, fmt.Errorf("fee rate above the contract ceiling")
	}
	return bps[0] + bps[1], nil
}

// SplitFee is P2PTradeEscrow._payOut's split: fee = floor(amount·bps/10000), the
// receiver nets the rest. Amounts are token base units.
func SplitFee(amountBase string, bps int) (fee, net string, err error) {
	amt, ok := new(big.Int).SetString(strings.TrimSpace(amountBase), 10)
	if !ok || amt.Sign() < 0 {
		return "", "", fmt.Errorf("invalid token amount %q", amountBase)
	}
	f := new(big.Int).Mul(amt, big.NewInt(int64(bps)))
	f.Quo(f, big.NewInt(10000))
	return f.String(), new(big.Int).Sub(amt, f).String(), nil
}

// fillTradeFee derives the virtual fee amounts from the trade's recorded
// snapshot. A trade with no snapshot (open not recorded, or the read failed)
// carries no fee fields.
func fillTradeFee(t *models.P2PTrade) {
	if t == nil || t.FeeBps == nil {
		return
	}
	fee, net, err := SplitFee(t.TokenAmount, *t.FeeBps)
	if err != nil {
		return
	}
	t.FeeAmount, t.BuyerNetAmount = fee, net
}

// feeKey is one (chain, token) live-fee read.
type feeKey struct {
	chainID int64
	token   string
}

// liveFees reads each key's live fee in parallel (a cold read is a few RPC
// calls) and returns what finished within budget; failed or late keys are
// absent. A late read keeps running detached and fills the cache.
func (s *P2PTradeService) liveFees(ctx context.Context, keys []feeKey, budget time.Duration) map[feeKey]int {
	out := map[feeKey]int{}
	if s.feeView == nil || len(keys) == 0 {
		return out
	}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		done = make(chan struct{})
	)
	for _, k := range keys {
		wg.Add(1)
		go func(k feeKey) {
			defer wg.Done()
			if bps, err := s.feeView.LiveFeeBps(ctx, k.chainID, k.token); err == nil {
				mu.Lock()
				out[k] = bps
				mu.Unlock()
			}
		}(k)
	}
	go func() { wg.Wait(); close(done) }()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
	mu.Lock()
	defer mu.Unlock()
	finished := make(map[feeKey]int, len(out))
	for k, v := range out {
		finished[k] = v
	}
	return finished
}

// FillAdFees sets each ad's live fee for an ad screen: one read per distinct
// (chain, token), bounded by adFeeBudget. Plain ad reads (GetAd, ListAds) never
// touch the chain; only the browse, detail and seller-profile handlers call
// this. A fee that could not be read in time stays nil, never guessed.
func (s *P2PTradeService) FillAdFees(ctx context.Context, ads ...*models.P2PAd) {
	if s.feeView == nil {
		return
	}
	seen := map[feeKey]bool{}
	var keys []feeKey
	for _, ad := range ads {
		if ad == nil {
			continue
		}
		k := adFeeKey(ad)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	fees := s.liveFees(ctx, keys, s.adFeeBudget)
	for _, ad := range ads {
		if ad == nil {
			continue
		}
		if bps, ok := fees[adFeeKey(ad)]; ok {
			ad.FeeBps = &bps
		}
	}
}

func adFeeKey(ad *models.P2PAd) feeKey {
	return feeKey{chainID: ad.ChainID, token: strings.ToUpper(strings.TrimSpace(ad.Token))}
}

// withLiveFees adds each ordinary chain's per-asset live fee. The pool chain is
// not in this list.
func (s *P2PTradeService) withLiveFees(ctx context.Context, chains []P2PChainInfo) []P2PChainInfo {
	if s.feeView == nil {
		return chains
	}
	var keys []feeKey
	for _, c := range chains {
		for _, sym := range c.Assets {
			keys = append(keys, feeKey{chainID: c.ChainID, token: sym})
		}
	}
	// Each read is already capped at p2pFeeViewReadTimeout, so this waits for all.
	fees := s.liveFees(ctx, keys, p2pFeeViewReadTimeout)
	for i := range chains {
		chains[i].FeeBps = nil
		for _, sym := range chains[i].Assets {
			if bps, ok := fees[feeKey{chainID: chains[i].ChainID, token: sym}]; ok {
				if chains[i].FeeBps == nil {
					chains[i].FeeBps = map[string]int{}
				}
				chains[i].FeeBps[sym] = bps
			}
		}
	}
	return chains
}
