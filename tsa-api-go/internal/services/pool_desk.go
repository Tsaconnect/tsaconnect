package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PoolDeskConfig is everything the pool's order desk is built from at boot
// (plan C1 P4). Every chain-facing part is the pool chain's.
type PoolDeskConfig struct {
	DB          *gorm.DB
	PM          *PMService
	Trades      *P2PTradeService
	Stock       *PoolStock
	Token       PoolToken
	Escrow      P2PEscrowAdapter
	EscrowAddr  string
	Treasury    *Treasury // the conversion treasury; its Token.ChainKey lane opens slices
	Gates       PoolGate
	Probe       TreasuryTxProbe     // receipts depth, nonces (one client, P10)
	Generation  func() uint64       // the pool chain client's EndpointGeneration (S43)
	Nonces      *TreasuryNonceWatch // the lane's stuck-nonce watch (F24)
	BuyerUserID uuid.UUID           // IT_PM_ID's user: the slices' DB buyer (S10)
	ChainName   string              // "BNB Smart Chain": the card's chain label
	// Settle side (plan C2). Boot requires all three before the desk serves
	// orders (Task 12); AdvancePoolBuy refuses without them.
	Sender   ChainSender                  // the release key (S8)
	Verifier ERC20SourcedTransferVerifier // release proofs: escrow → treasury per receipt
	Sale     *CompanyWallet               // the conversion treasury's pool-chain lane as a sale wallet (S20)
	LogChunk uint64                       // PoolChainConfig.LogChunkBlocks (the F22 scan)
	Now      func() time.Time
	// What the release key and the treasury lane on the pool chain already owe
	// in-flight orders (nil = counted from DB). NewPoolDesk registers both as
	// their lanes' reservations, so no other send on either key (a gas top-up,
	// a direct P2P release, an IT op, a slice open) spends them.
	ReleaseCommitments  *LaneCommitments
	TreasuryCommitments *LaneCommitments
}

// PoolDesk is the TSA Liquidity pool's order desk: free stock, allocation,
// (Task 10) the open, and plan C2's settle side. One per process; ITService holds it like the company
// wallet.
type PoolDesk struct {
	c           PoolDeskConfig
	cached      *cachedReader[[]poolCandidate]
	proveWindow time.Duration // poolProveWindow; tests shorten it
	breaker     poolOpenBreaker
}

const (
	poolTotalsTTL         = 5 * time.Second    // Σ top-N free on public paths (§9.1)
	poolVoucherMargin     = 70 * time.Minute   // payment window + 10 min (§9.1)
	poolUnconfirmedWindow = 15 * time.Minute   // DB reservations not yet locked (§9.1)
	poolFillWindow        = 7 * 24 * time.Hour // fill share (§9.3)
	poolFreshReadTimeout  = 3 * time.Second    // per chosen LP (§9.1 step 1)
)

var errPoolDeskIncomplete = errors.New("liquidity pool: desk is missing a part")

// NewPoolDesk refuses an incomplete desk; boot then leaves pool orders
// dormant while Plan B's LP onboarding (which never uses the desk) keeps working.
func NewPoolDesk(c PoolDeskConfig) (*PoolDesk, error) {
	switch {
	case c.DB == nil, c.PM == nil, c.Trades == nil, c.Stock == nil, c.Escrow == nil, c.EscrowAddr == "",
		c.Treasury == nil, c.Gates == nil, c.Probe == nil, c.Generation == nil, c.Nonces == nil,
		c.BuyerUserID == uuid.Nil, c.Token.Decimals < StableAssetDecimals:
		return nil, errPoolDeskIncomplete
	}
	if c.Treasury.Lane(c.Token.ChainKey) == nil {
		return nil, fmt.Errorf("%w: no treasury lane on %s", errPoolDeskIncomplete, c.Token.ChainKey)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.ReleaseCommitments == nil {
		c.ReleaseCommitments = NewPoolReleaseCommitments(c.DB, c.Token.ChainID)
	}
	if c.TreasuryCommitments == nil {
		c.TreasuryCommitments = NewPoolTreasuryCommitments(c.DB)
	}
	c.Treasury.Lane(c.Token.ChainKey).nonceLane().AddReservation("pool_treasury_owed", c.TreasuryCommitments.Reservation)
	if crs, ok := c.Sender.(*ChainReleaseSigner); ok {
		crs.Lane(c.Token.ChainID).AddReservation("pool_releases", c.ReleaseCommitments.Reservation)
	}
	return &PoolDesk{c: c, cached: newCachedReader[[]poolCandidate](poolTotalsTTL, poolBalanceFailTTL), proveWindow: poolProveWindow}, nil
}

func (s *ITService) SetPoolDesk(d *PoolDesk) { s.poolDesk = d }

// HasPool: a pool desk is wired (never a typed nil).
func (s *ITService) HasPool() bool { return s != nil && s.poolDesk != nil }

func (d *PoolDesk) ChainKey() string    { return d.c.Token.ChainKey }
func (d *PoolDesk) TokenSymbol() string { return d.c.Token.Symbol }
func (d *PoolDesk) now() time.Time      { return d.c.Now().UTC() }

// cachedCandidates is the eligible set on cached balances, cached 5 s on top
// of the 30 s balance cache (public paths only; never a money decision).
// Callers must not mutate what it returns.
func (d *PoolDesk) cachedCandidates(ctx context.Context, set PoolSettings) ([]poolCandidate, error) {
	key := fmt.Sprintf("min%s", set.MinSliceUnits)
	now := d.now()
	return d.cached.Get(key, now, func() ([]poolCandidate, error) {
		cs, err := d.candidates(ctx, d.c.DB, set, nil, nil, false)
		d.cached.Put(key, now, cs, err)
		return cs, err
	})
}

// FreeTopN is Σ of the top pool_slice_cap LPs' free stock: what the pool can
// promise right now (quote feasibility §9.3 step 4, the card's maximum).
func (d *PoolDesk) FreeTopN(ctx context.Context, set PoolSettings) (*big.Int, error) {
	cs, err := d.cachedCandidates(ctx, set)
	if err != nil {
		return nil, err
	}
	return sumTopN(cs, set.SliceCap), nil
}

// QuoteSliceUnits is the slice sizes an order of units would open as now:
// allocatePool on the cached eligible set, the open's own allocator (§9.3).
// A quote prices its per-slice costs on it; the open re-plans on fresh stock.
// ErrPoolCannotFill when no allocation fits.
func (d *PoolDesk) QuoteSliceUnits(ctx context.Context, units *big.Int, set PoolSettings) ([]*big.Int, error) {
	cs, err := d.cachedCandidates(ctx, set)
	if err != nil {
		return nil, err
	}
	plan, err := allocatePool(cs, units, set.SliceCap, set.MinSliceUnits)
	if err != nil {
		return nil, err
	}
	out := make([]*big.Int, len(plan))
	for i := range plan {
		out[i] = plan[i].Units
	}
	return out, nil
}
