package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"gorm.io/gorm"
)

// PoolToken is the shared pool's token on its chain (USDT on BSC first).
type PoolToken struct {
	ChainID  int64
	ChainKey string
	Symbol   string
	Address  string
	Decimals int
}

// PoolDeskConfig is everything the shared pool's order desk (spec
// 2026-10-14 §4) is built from at boot. Every chain-facing part is the pool
// chain's.
type PoolDeskConfig struct {
	DB         *gorm.DB
	Token      PoolToken
	Vault      VaultChain
	Treasury   *Treasury // the conversion treasury: receives fills, delivers
	Gates      PoolGate
	Probe      TreasuryTxProbe     // fill receipts and depth
	Generation func() uint64       // the pool chain client's EndpointGeneration
	Nonces     *TreasuryNonceWatch // the treasury lane's stuck-nonce watch
	ChainName  string              // "BNB Smart Chain": the card's chain label
	Sender     ChainSender         // the OPERATOR: fills
	Sale       *CompanyWallet      // the treasury's pool-chain lane as a sale wallet
	Now        func() time.Time
	// What the operator and the treasury lane already owe in-flight orders
	// (nil = counted from DB), registered as their lanes' reservations so no
	// other send on either key spends them.
	ReleaseCommitments  *LaneCommitments
	TreasuryCommitments *LaneCommitments
}

// VaultChain is what the pool desk asks of the shared-pool vault;
// *LiquidityVault is the chain's, tests use a fake.
type VaultChain interface {
	Address() string
	State(ctx context.Context) (*VaultState, error)
	FillCaps(ctx context.Context) (*VaultFillCaps, error)
	Roles(ctx context.Context) (*VaultRoles, error)
	FillUsed(ctx context.Context, ref [32]byte) (bool, error)
	PrepareFill(from, to string, amount, spread *big.Int, ref [32]byte) ([]byte, error)
	PendingWithdrawal(ctx context.Context, lp string) (*big.Int, error)
	PreparePayPending(from, lp string) ([]byte, error)
	FilledIn(r *types.Receipt, ref [32]byte) *VaultEvent
}

var _ VaultChain = (*LiquidityVault)(nil)

// PoolDesk is the shared pool's order desk. One per process; ITService holds
// it like the company wallet.
type PoolDesk struct {
	c          PoolDeskConfig
	vaultCache *cachedReader[*big.Int] // the vault's free liquidity, public paths only
}

const (
	poolTotalsTTL        = 5 * time.Second // public reads of the vault's free liquidity
	poolReadFailTTL      = 5 * time.Second
	poolFreshReadTimeout = 3 * time.Second // a read under the pool lock
)

var errPoolDeskIncomplete = errors.New("liquidity pool: desk is missing a part")

// NewPoolDesk refuses an incomplete desk: one that could take naira it cannot
// fill or deliver is never built.
func NewPoolDesk(c PoolDeskConfig) (*PoolDesk, error) {
	switch {
	case c.DB == nil, c.Vault == nil, c.Treasury == nil, c.Gates == nil, c.Probe == nil, c.Generation == nil, c.Nonces == nil,
		c.Sender == nil, c.Sale == nil, c.Token.Decimals < StableAssetDecimals:
		return nil, errPoolDeskIncomplete
	}
	if c.Treasury.Lane(c.Token.ChainKey) == nil {
		return nil, fmt.Errorf("%w: no treasury lane on %s", errPoolDeskIncomplete, c.Token.ChainKey)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.ReleaseCommitments == nil {
		c.ReleaseCommitments = NewPoolFillCommitments(c.DB)
	}
	if c.TreasuryCommitments == nil {
		c.TreasuryCommitments = NewPoolTreasuryCommitments(c.DB)
	}
	c.Treasury.Lane(c.Token.ChainKey).nonceLane().AddReservation("pool_treasury_owed", c.TreasuryCommitments.Units)
	if crs, ok := c.Sender.(*ChainReleaseSigner); ok {
		crs.Lane(c.Token.ChainID).AddReservation("pool_fills", c.ReleaseCommitments.Units)
	}
	return &PoolDesk{c: c, vaultCache: newCachedReader[*big.Int](poolTotalsTTL, poolReadFailTTL)}, nil
}

func (s *ITService) SetPoolDesk(d *PoolDesk) { s.poolDesk = d }

// HasPool: a pool desk is wired (never a typed nil).
func (s *ITService) HasPool() bool { return s != nil && s.poolDesk != nil }

func (d *PoolDesk) ChainKey() string    { return d.c.Token.ChainKey }
func (d *PoolDesk) TokenSymbol() string { return d.c.Token.Symbol }
func (d *PoolDesk) now() time.Time      { return d.c.Now().UTC() }

// FreeUnits is the largest order (canonical units) the vault can take now
// (liquid less what open orders reserve): the quote's feasibility and the
// card's maximum. Cached; an open re-reads.
func (d *PoolDesk) FreeUnits(ctx context.Context, set PoolSettings) (*big.Int, error) {
	return d.vaultDeliverable(ctx, set)
}
