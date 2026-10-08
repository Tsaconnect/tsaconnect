package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	poolBalanceTTL     = 30 * time.Second // public paths (spec §9.1)
	poolBalanceFailTTL = 5 * time.Second
	poolBalanceTimeout = 3 * time.Second
)

// ErrNotPoolLP: the row is not an LP on the configured pool chain.
var ErrNotPoolLP = errors.New("liquidity: not a liquidity provider on the pool chain")

// PoolToken is the pool chain's token as LP flows show it.
type PoolToken struct {
	ChainID  int64
	ChainKey string
	Symbol   string
	Address  string
	Decimals int
}

// LPPosition is one LP's stock on the pool chain, in chain base units.
type LPPosition struct {
	Token     PoolToken
	Wallet    string
	Available *big.Int
	Locked    *big.Int
	Stake     *big.Int // min(available + locked, max): what the pool counts (§9.1)
	AboveMax  *big.Int // available + locked above max: earns nothing (§8.3)
	Min, Max  *big.Int // pool_lp_min / pool_lp_max in base units
}

// PoolStock reads LP stock from the pool escrow (spec §9.1, read side). Free
// stock (minus unconfirmed slice reservations) is Plan C1's.
type PoolStock struct {
	escrows   *P2PEscrowRegistry
	tokenAddr func(chainID int64, symbol string) string
	balances  *cachedReader[*OnChainBalance]
	timeout   time.Duration // per balance read; poolBalanceTimeout
	now       func() time.Time
}

func NewPoolStock(escrows *P2PEscrowRegistry, tokenAddr func(chainID int64, symbol string) string) *PoolStock {
	return &PoolStock{escrows: escrows, tokenAddr: tokenAddr,
		balances: newCachedReader[*OnChainBalance](poolBalanceTTL, poolBalanceFailTTL), timeout: poolBalanceTimeout, now: time.Now}
}

// Token is the pool's (first) token: USDT on BSC first.
func (p *PoolStock) Token() (PoolToken, bool) {
	pool, ok := p.escrows.PoolChain()
	if !ok || len(pool.Assets) == 0 {
		return PoolToken{}, false
	}
	sym := strings.ToUpper(pool.Assets[0])
	dec, ok := pool.Decimals[sym]
	addr := p.tokenAddr(pool.ChainID, sym)
	if !ok || dec < StableAssetDecimals || addr == "" {
		return PoolToken{}, false
	}
	return PoolToken{ChainID: pool.ChainID, ChainKey: pool.ChainKey, Symbol: sym, Address: addr, Decimals: dec}, true
}

// Position reads lp's balance (cached unless fresh) and applies the limits.
func (p *PoolStock) Position(ctx context.Context, lp *models.PaymentMerchant, fresh bool) (*LPPosition, error) {
	tok, ok := p.Token()
	if !ok || !lp.IsLP() || lp.PoolChainID == nil || *lp.PoolChainID != tok.ChainID {
		return nil, ErrNotPoolLP
	}
	escrow, err := p.escrows.For(tok.ChainID)
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(lp.OperatorWalletAddress) + "|" + tok.Symbol
	read := func() (*OnChainBalance, error) {
		b, err := readBalanceWithin(ctx, escrow, lp.OperatorWalletAddress, tok.Address, p.timeout)
		p.balances.Put(key, p.now(), b, err)
		return b, err
	}
	var bal *OnChainBalance
	if fresh {
		bal, err = read()
	} else {
		bal, err = p.balances.Get(key, p.now(), read)
	}
	if err != nil {
		return nil, err
	}
	min, max, err := PoolLPLimitUnits(tok.Decimals)
	if err != nil {
		return nil, err
	}
	stake, above := lpStake(bal.Available, bal.Locked, max)
	return &LPPosition{Token: tok, Wallet: lp.OperatorWalletAddress,
		Available: new(big.Int).Set(bal.Available), Locked: new(big.Int).Set(bal.Locked),
		Stake: stake, AboveMax: above, Min: min, Max: max}, nil
}

// lpStake is min(available + locked, max) and the part above max.
func lpStake(available, locked, max *big.Int) (stake, aboveMax *big.Int) {
	held := new(big.Int).Add(available, locked)
	if held.Cmp(max) <= 0 {
		return held, new(big.Int)
	}
	return new(big.Int).Set(max), new(big.Int).Sub(held, max)
}

// readBalanceWithin bounds ReadBalance (which takes no context) and redacts the
// RPC error, which can carry a keyed URL. It follows CompanyWallet.read's
// goroutine-plus-timeout shape; that one reads a treasury lane, not an escrow,
// so only the cache (spec §9.1) is extracted and shared (review M8).
func readBalanceWithin(ctx context.Context, escrow P2PEscrowAdapter, wallet, token string, d time.Duration) (*OnChainBalance, error) {
	type result struct {
		b   *OnChainBalance
		err error
	}
	ch := make(chan result, 1)
	go func() { b, err := escrow.ReadBalance(wallet, token); ch <- result{b, err} }()
	rctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("%w: %s", ErrChainUnavailable, RedactAlertText(r.err.Error()))
		}
		return r.b, nil
	case <-rctx.Done():
		return nil, fmt.Errorf("%w: pool balance read timed out", ErrChainUnavailable)
	}
}
