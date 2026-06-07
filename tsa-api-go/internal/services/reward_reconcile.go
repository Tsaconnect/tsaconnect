package services

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// reconcileLog emits a structured, greppable reconciler line (the keyed
// `*_total=1` tokens mirror the p2p_reconciler_drift_total log-metric
// convention — there is no Prometheus in this codebase yet).
func reconcileLog(format string, args ...interface{}) {
	log.Printf("[reward-reconciler] "+format, args...)
}

// erc20BalanceOfABI is the minimal balanceOf-only ERC-20 ABI used to read the
// distributor's MCGP balance (built once).
var erc20BalanceOfABI = func() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(`[{"constant":true,"inputs":[{"name":"account","type":"address"}],"name":"balanceOf","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"}]`))
	if err != nil {
		panic(fmt.Sprintf("reward reconcile: parse balanceOf ABI: %v", err))
	}
	return parsed
}()

// erc20BalanceOfCalldata packs balanceOf(holder).
func erc20BalanceOfCalldata(holder string) ([]byte, error) {
	data, err := erc20BalanceOfABI.Pack("balanceOf", common.HexToAddress(holder))
	if err != nil {
		return nil, fmt.Errorf("pack balanceOf: %w", err)
	}
	return data, nil
}

// SP6 reconciler + treasury monitor (design §5 SP6 bullet + §9 money-safety
// invariants). Mirrors the P2P chain reconciler / p2p_reconciler_drift_total
// convention: it compares the off-chain published claimable ledger against
// on-chain truth and a structured, greppable log-metric records any drift so a
// dropped claim-confirm or a treasury shortfall is visible to ops rather than
// silent.
//
// Two checks per pass (both read-only — no tx, no key):
//
//  1. DRIFT: for every account in the latest published epoch, read on-chain
//     claimed[account]. The published cumulative MUST be >= claimed[account]
//     (the contract enforces claim() only pays cumulative − claimed, so the
//     monotonic clamp guarantees published >= claimed). A published cumulative
//     BELOW on-chain claimed means the off-chain ledger regressed under what the
//     chain already paid — a correctness violation (e.g. the exclusion-gap clamp
//     bug) that bricks the account's future claims. Emitted as
//     reward_reconciler_drift_total.
//
//  2. TREASURY: outstanding = Σ(published cumulative) − Σ(on-chain claimed) over
//     the latest published epoch's accounts. If the distributor's MCGP balance
//     is below outstanding, queued claims cannot all be paid — emitted as
//     reward_treasury_short_total with the shortfall so ops can top up the
//     treasury BEFORE users hit a clean-but-confusing revert. Never
//     partial-silent (design §9).

// RewardChainReader is the read-only on-chain surface the reconciler needs.
// Kept narrow + injectable so the job is unit-testable without a live RPC. The
// production implementation (NewRewardChainReader) wraps an EVMClient.
type RewardChainReader interface {
	// ClaimedOf returns claimed[account] (base units) on the distributor.
	ClaimedOf(ctx context.Context, distributor, account string) (*big.Int, error)
	// MCGPBalanceOf returns the MCGP balanceOf(distributor) (base units).
	MCGPBalanceOf(ctx context.Context, distributor string) (*big.Int, error)
}

// RewardReconcileResult is one reconcile pass's outcome (for logging/tests).
type RewardReconcileResult struct {
	// Epoch is the published epoch reconciled (0 when none is published yet —
	// the pass is then a no-op).
	Epoch int64
	// Accounts is how many distinct accounts were checked.
	Accounts int
	// DriftCount is how many accounts had published cumulative < on-chain claimed
	// (a ledger regression below already-paid funds). Non-zero ⇒ a bug to fix.
	DriftCount int
	// OutstandingBaseUnits is Σ(published cumulative − on-chain claimed) — the
	// MCGP the treasury still owes against the live root.
	OutstandingBaseUnits *big.Int
	// TreasuryBaseUnits is the distributor's on-chain MCGP balance.
	TreasuryBaseUnits *big.Int
	// TreasuryShort is true when TreasuryBaseUnits < OutstandingBaseUnits.
	TreasuryShort bool
	// ShortfallBaseUnits is Outstanding − Treasury when short (else 0).
	ShortfallBaseUnits *big.Int
}

// Reconcile performs one read-only reconciliation pass against the latest
// PUBLISHED epoch (the root live on-chain). It detects ledger-vs-chain drift and
// treasury insufficiency, logging structured metrics, and returns the result.
//
// No-op (clean) when the feature is dormant, no reader is wired, or no epoch is
// published yet. Idempotent + side-effect-free on-chain — safe to run on a tick.
func (s *RewardMerkleService) Reconcile(ctx context.Context, reader RewardChainReader, mcgpToken string) (*RewardReconcileResult, error) {
	res := &RewardReconcileResult{
		OutstandingBaseUnits: big.NewInt(0),
		TreasuryBaseUnits:    big.NewInt(0),
		ShortfallBaseUnits:   big.NewInt(0),
	}
	if s == nil || s.db == nil || !s.IsConfigured() || reader == nil {
		return res, nil // dormant / not wired — nothing to reconcile
	}

	published, err := s.latestPublishedDistribution(ctx)
	if err != nil {
		return nil, err
	}
	if published == nil {
		return res, nil // no live root yet
	}
	res.Epoch = published.Epoch

	var entries []models.RewardDistributionEntry
	if err := s.db.WithContext(ctx).
		Where("distribution_id = ?", published.ID).
		Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("load published epoch %d entries: %w", published.Epoch, err)
	}
	res.Accounts = len(entries)

	outstanding := new(big.Int)
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pubCum, ok := new(big.Int).SetString(e.CumulativeBaseUnits, 10)
		if !ok {
			return nil, fmt.Errorf("parse published cumulative %q for user %s", e.CumulativeBaseUnits, e.UserID)
		}
		claimed, err := reader.ClaimedOf(ctx, s.distributorAddress, e.Account)
		if err != nil {
			// A single RPC read failing shouldn't abort the whole pass — log and
			// skip this account; the next tick retries. (Matches the P2P
			// reconciler's per-item continue-on-error.)
			reconcileLog("reward_reconciler_read_error_total=1 epoch=%d account=%s err=%v", published.Epoch, e.Account, err)
			continue
		}
		// Invariant: published cumulative >= on-chain claimed. A violation means
		// the off-chain ledger published LESS than the chain already paid — the
		// exclusion-gap regression bug or a bad correction. Loud + greppable.
		if pubCum.Cmp(claimed) < 0 {
			res.DriftCount++
			reconcileLog("reward_reconciler_drift_total=1 epoch=%d account=%s publishedCumulative=%s onchainClaimed=%s (LEDGER BELOW ON-CHAIN CLAIMED — claims will brick)",
				published.Epoch, e.Account, pubCum.String(), claimed.String())
		}
		// Outstanding = Σ max(published − claimed, 0). Clamp at 0 so a drifted
		// (negative) account doesn't understate the treasury need.
		owed := new(big.Int).Sub(pubCum, claimed)
		if owed.Sign() > 0 {
			outstanding.Add(outstanding, owed)
		}
	}
	res.OutstandingBaseUnits = outstanding

	// Treasury monitor: the distributor must hold >= outstanding MCGP or some
	// queued claims revert (clean, but invisible without this). Alert + (the job
	// re-runs each tick, which IS the queue) when short — never partial-silent.
	bal, err := reader.MCGPBalanceOf(ctx, s.distributorAddress)
	if err != nil {
		// Couldn't read the treasury — surface it but don't fail the whole pass
		// (drift detection above still ran).
		reconcileLog("reward_reconciler_read_error_total=1 epoch=%d kind=treasury_balance err=%v", published.Epoch, err)
		return res, nil
	}
	res.TreasuryBaseUnits = bal
	if bal.Cmp(outstanding) < 0 {
		res.TreasuryShort = true
		res.ShortfallBaseUnits = new(big.Int).Sub(outstanding, bal)
		reconcileLog("reward_treasury_short_total=1 ALERT epoch=%d distributor=%s token=%s treasury=%s outstanding=%s shortfall=%s (fund the distributor — claims will revert underfunded)",
			published.Epoch, s.distributorAddress, mcgpToken, bal.String(), outstanding.String(), res.ShortfallBaseUnits.String())
	}
	return res, nil
}

// ─── EVMClient-backed chain reader ───────────────────────────────────────────

// rpcCaller is the read-only RPC surface the reward chain reader needs (the
// *ethclient.Client embedded in blockchain.EVMClient satisfies it). An interface
// keeps NewRewardChainReader injectable for tests.
type rpcCaller interface {
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// evmRewardChainReader reads the distributor's claimed[]/MCGP balanceOf via
// eth_call. mcgpToken is the MCGP ERC-20 the distributor pays out.
type evmRewardChainReader struct {
	caller    rpcCaller
	mcgpToken string
}

// NewRewardChainReader wraps a CallContract-capable RPC client (an EVMClient's
// embedded *ethclient.Client) into a RewardChainReader. Returns nil when caller
// or mcgpToken is missing so callers leave the reconciler dormant.
func NewRewardChainReader(caller rpcCaller, mcgpToken string) RewardChainReader {
	if caller == nil || mcgpToken == "" {
		return nil
	}
	return &evmRewardChainReader{caller: caller, mcgpToken: mcgpToken}
}

func (r *evmRewardChainReader) ClaimedOf(ctx context.Context, distributor, account string) (*big.Int, error) {
	data, err := parsedRewardDistributorABI.Pack("claimed", common.HexToAddress(account))
	if err != nil {
		return nil, fmt.Errorf("pack claimed: %w", err)
	}
	out, err := r.call(ctx, distributor, data)
	if err != nil {
		return nil, fmt.Errorf("claimed(%s) call: %w", account, err)
	}
	vals, err := parsedRewardDistributorABI.Unpack("claimed", out)
	if err != nil || len(vals) == 0 {
		return nil, fmt.Errorf("decode claimed: %w", err)
	}
	v, ok := vals[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("claimed returned unexpected type %T", vals[0])
	}
	return v, nil
}

func (r *evmRewardChainReader) MCGPBalanceOf(ctx context.Context, distributor string) (*big.Int, error) {
	data, err := erc20BalanceOfCalldata(distributor)
	if err != nil {
		return nil, err
	}
	out, err := r.call(ctx, r.mcgpToken, data)
	if err != nil {
		return nil, fmt.Errorf("MCGP balanceOf(%s) call: %w", distributor, err)
	}
	if len(out) == 0 {
		return big.NewInt(0), nil
	}
	vals, err := erc20BalanceOfABI.Unpack("balanceOf", out)
	if err != nil || len(vals) == 0 {
		return nil, fmt.Errorf("decode balanceOf: %w", err)
	}
	v, ok := vals[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("balanceOf returned unexpected type %T", vals[0])
	}
	return v, nil
}

// call performs a read-only eth_call against `to` with a short timeout.
func (r *evmRewardChainReader) call(ctx context.Context, to string, data []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addr := common.HexToAddress(to)
	return r.caller.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: data}, nil)
}
