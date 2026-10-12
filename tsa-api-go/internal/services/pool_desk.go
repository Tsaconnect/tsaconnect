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
}

// PoolDesk is the TSA Liquidity pool's order desk: free stock, allocation,
// (Task 10) the open, and plan C2's settle side. One per process; ITService holds it like the company
// wallet.
type PoolDesk struct {
	c           PoolDeskConfig
	totals      *cachedReader[*big.Int]
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
	return &PoolDesk{c: c, totals: newCachedReader[*big.Int](poolTotalsTTL, poolBalanceFailTTL), proveWindow: poolProveWindow}, nil
}

func (s *ITService) SetPoolDesk(d *PoolDesk) { s.poolDesk = d }

// HasPool: a pool desk is wired (never a typed nil).
func (s *ITService) HasPool() bool { return s != nil && s.poolDesk != nil }

func (d *PoolDesk) ChainKey() string    { return d.c.Token.ChainKey }
func (d *PoolDesk) TokenSymbol() string { return d.c.Token.Symbol }
func (d *PoolDesk) now() time.Time      { return d.c.Now().UTC() }

// FreeTopN is Σ of the top pool_slice_cap LPs' free stock: what the pool can
// promise right now (quote feasibility §9.3 step 4, the card's maximum).
// Cached 5 s on top of the 30 s balance cache; never a money decision.
func (d *PoolDesk) FreeTopN(ctx context.Context, set PoolSettings) (*big.Int, error) {
	key := fmt.Sprintf("top%d", set.SliceCap)
	now := d.now()
	return d.totals.Get(key, now, func() (*big.Int, error) {
		cs, err := d.candidates(ctx, d.c.DB, set, nil, nil, false)
		if err == nil {
			sum := sumTopN(cs, set.SliceCap)
			d.totals.Put(key, now, sum, nil)
			return sum, nil
		}
		d.totals.Put(key, now, nil, err)
		return nil, err
	})
}
