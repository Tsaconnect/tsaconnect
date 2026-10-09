package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ErrPoolClosed: the pool's boot gates are not all passing (spec §6.2), so no
// new LP stock moves in. Exits never check it.
var ErrPoolClosed = errors.New("p2p: the liquidity pool is closed right now; try again later")

const (
	LiquidityAboveMaxCode = "above_liquidity_max"
	LiquidityBelowMinCode = "below_liquidity_min"
)

// LiquidityLimitError refuses an LP deposit outside the pool limits (spec §8.3,
// decision 10). Headroom is how much more the LP may deposit, in base units.
type LiquidityLimitError struct {
	Code     string
	Headroom *big.Int
	Decimals int
}

func (e *LiquidityLimitError) Error() string {
	if e.Code == LiquidityBelowMinCode {
		return "p2p: your first deposit is below the liquidity minimum"
	}
	return "p2p: this deposit would take your liquidity above the maximum"
}

// SetPoolGate wires the pool's gates. Called once at boot; nil keeps the LP
// route closed.
func (s *P2PTradeService) SetPoolGate(g PoolOpener) { s.poolGate = g }

// OnPoolBalanceMoved registers what to tell when a confirmed deposit or
// withdrawal moves a seller's escrow balance (the LP balance cache, review I4).
func (s *P2PTradeService) OnPoolBalanceMoved(f func(chainID int64, wallet, token string)) {
	s.balanceMoved = f
}

func (s *P2PTradeService) poolBalanceMoved(chainID int64, wallet, token string) {
	if s.balanceMoved != nil {
		s.balanceMoved(chainID, wallet, strings.ToUpper(token))
	}
}

func (s *P2PTradeService) poolOpen() bool { return s.poolGate != nil && s.poolGate.Open() }

// lookupActiveLP is the caller's active LP row; (nil, nil) when there is none
// or the PM feature is dormant.
func (s *P2PTradeService) lookupActiveLP(ctx context.Context, userID uuid.UUID) (*models.PaymentMerchant, error) {
	if s.pm == nil {
		return nil, nil
	}
	lp, err := s.pm.GetActiveByUserIDKind(ctx, userID, models.PaymentMerchantKindLP)
	if errors.Is(err, ErrPMNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lp lookup: %w", err)
	}
	return lp, nil
}

// IsActiveLP lets the ad handler admit an LP that is not a marketplace merchant.
func (s *P2PTradeService) IsActiveLP(ctx context.Context, userID uuid.UUID) bool {
	lp, err := s.lookupActiveLP(ctx, userID)
	return err == nil && lp != nil
}

// activeLPOn is the caller's active LP row when it is registered on chainID;
// nil otherwise.
func (s *P2PTradeService) activeLPOn(ctx context.Context, userID uuid.UUID, chainID int64) (*models.PaymentMerchant, error) {
	lp, err := s.lookupActiveLP(ctx, userID)
	if err != nil || lp == nil || lp.PoolChainID == nil || *lp.PoolChainID != chainID {
		return nil, err
	}
	return lp, nil
}

// poolLPFor is the ONLY route onto the pool chain (spec S5, §8; plan B P6): a
// pool token, the caller's active LP row on this chain, and the pool open.
func (s *P2PTradeService) poolLPFor(ctx context.Context, userID uuid.UUID, chainID int64, token string) (*models.PaymentMerchant, error) {
	pool, ok := s.escrows.PoolChain()
	if !ok || pool.ChainID != chainID || !pool.HasAsset(token) {
		return nil, fmt.Errorf("%w: %s on chain %d", ErrAssetNotTradeable, strings.ToUpper(token), chainID)
	}
	lp, err := s.activeLPOn(ctx, userID, chainID)
	if err != nil {
		return nil, err
	}
	if lp == nil {
		return nil, fmt.Errorf("%w: only an active liquidity provider can add stock on chain %d", ErrAssetNotTradeable, chainID)
	}
	if !s.poolOpen() {
		return nil, ErrPoolClosed
	}
	return lp, nil
}

// openableFor is requireOpenable made pool-aware: on an ordinary chain it is
// exactly requireOpenable and returns no LP; on the pool chain it returns the
// caller's LP row or refuses. Exit paths never call it.
func (s *P2PTradeService) openableFor(ctx context.Context, userID uuid.UUID, chainID int64, token string) (*models.PaymentMerchant, error) {
	if s.escrows.Purpose(chainID) != config.P2PPurposePool {
		return nil, s.requireOpenable(chainID, token)
	}
	return s.poolLPFor(ctx, userID, chainID, token)
}

// checkLPDeposit applies the pool limits to an LP deposit on a FRESH chain read
// (spec §8.3, plan B P7): available + locked + amount ≤ max, and a first
// deposit (nothing held) ≥ min. Only the LP's registered wallet may deposit.
// A deposit made outside the app cannot be refused; the stake cap bounds it.
// Accepted (review M16): two concurrent prepares can each pass and together
// exceed the max; the stake cap (§9.1) is what bounds the pool, as the spec says.
func (s *P2PTradeService) checkLPDeposit(ctx context.Context, lp *models.PaymentMerchant, escrow P2PEscrowAdapter, sellerAddress, tokenAddr string, decimals int, amount *big.Int) error {
	if common.HexToAddress(sellerAddress) != common.HexToAddress(lp.OperatorWalletAddress) {
		return fmt.Errorf("%w: deposit from your registered liquidity wallet", ErrInvalidParams)
	}
	min, max, err := PoolLPLimitUnits(decimals)
	if err != nil {
		return err
	}
	// Never the PoolStock cache: a stale reading could let a top-up through
	// after a slice lock moved the balance.
	bal, err := readBalanceWithin(ctx, escrow, sellerAddress, tokenAddr, poolBalanceTimeout)
	if err != nil {
		return err
	}
	held := new(big.Int).Add(bal.Available, bal.Locked)
	headroom := new(big.Int).Sub(max, held)
	if headroom.Sign() < 0 {
		headroom.SetInt64(0)
	}
	if amount.Cmp(headroom) > 0 {
		return &LiquidityLimitError{Code: LiquidityAboveMaxCode, Headroom: headroom, Decimals: decimals}
	}
	if held.Sign() == 0 && amount.Cmp(min) < 0 {
		return &LiquidityLimitError{Code: LiquidityBelowMinCode, Headroom: headroom, Decimals: decimals}
	}
	return nil
}

// ChainsFor is EnabledChains plus the pool chain (purpose "pool", with
// decimals) for an LP row bound to it, whatever its status (spec §5): exits are
// never gated (review I1), so a paused LP must still reach its balance to
// withdraw. New money stays active-only (openableFor).
func (s *P2PTradeService) ChainsFor(ctx context.Context, userID uuid.UUID) []P2PChainInfo {
	chains := s.withLiveFees(ctx, s.EnabledChains())
	pool, ok := s.escrows.PoolChain()
	if !ok || !s.lpBoundTo(ctx, userID, pool.ChainID) {
		return chains
	}
	return append(chains, P2PChainInfo{ChainID: pool.ChainID, Assets: pool.Assets, Purpose: config.P2PPurposePool, Decimals: pool.Decimals})
}

// lpBoundTo reports whether the user's PM row, any status, is an LP on chainID.
func (s *P2PTradeService) lpBoundTo(ctx context.Context, userID uuid.UUID, chainID int64) bool {
	if s.pm == nil {
		return false
	}
	row, err := s.pm.GetByUserID(ctx, userID)
	return err == nil && row.IsLP() && row.PoolChainID != nil && *row.PoolChainID == chainID
}
