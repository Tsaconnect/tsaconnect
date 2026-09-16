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
)

var (
	// ErrTreasuryKilled maps the lane guard's own Killed() to a caller-facing
	// sentinel. The guard stays the single source of truth for the kill
	// switch; this is only how that state is reported.
	ErrTreasuryKilled = errors.New("conversions are paused")
	// ErrTreasuryCapExceeded is returned when an order or a chain's in-flight
	// exposure would cross the value caps configured in TreasuryCaps. The
	// floor reuses the guard's own ErrRelayerUnderfunded — no third sentinel.
	ErrTreasuryCapExceeded = errors.New("this order is above the conversion limit right now")
)

// TreasuryLane is a SponsorLane plus what a token custodian needs: the
// numeric chain id LiFi quotes want, and the value caps the guard cannot
// express (the guard's DailyGasBudget is denominated in gas units, not USD).
type TreasuryLane struct {
	*SponsorLane // ChainKey, Chain, Builder, Native, Signer, Guard, Tokens, send()
	ChainID      int64
}

// TreasuryCaps bound the treasury's value exposure. A treasury caps VALUE,
// not gas units, so this lives outside SponsorLimits rather than overloading
// DailyGasBudget for a unit it was never meant to hold.
type TreasuryCaps struct {
	MaxInFlightUSDPerOrder float64
	MaxInFlightUSDPerChain float64
}

// Treasury holds and moves user funds in transit for a converted buy: one
// lane per chain, each a SponsorLane with the treasury's own SponsorGuard
// (kill switch + native floor) and this treasury's value caps.
type Treasury struct {
	lanes map[string]*TreasuryLane
	caps  TreasuryCaps
	now   func() time.Time
}

func NewTreasury(lanes map[string]*TreasuryLane, caps TreasuryCaps, now func() time.Time) *Treasury {
	if now == nil {
		now = time.Now
	}
	if lanes == nil {
		lanes = map[string]*TreasuryLane{}
	}
	return &Treasury{lanes: lanes, caps: caps, now: now}
}

// Address is the treasury's own EOA — the same key signs on every chain, so
// any lane's signer answers it.
func (t *Treasury) Address() string {
	for _, lane := range t.lanes {
		return lane.Signer.Address()
	}
	return ""
}

func (t *Treasury) Lane(chainKey string) *TreasuryLane {
	return t.lanes[strings.ToLower(strings.TrimSpace(chainKey))]
}

// SetKillSwitch fans out to every lane's Guard — there is no second,
// treasury-level switch to keep in sync.
func (t *Treasury) SetKillSwitch(on bool) {
	for _, lane := range t.lanes {
		lane.Guard.SetKillSwitch(on)
	}
}

// Killed reports the treasury as paused once any lane's guard is killed:
// SetKillSwitch always sets every lane together, so in normal operation this
// is "all" or "none" — checking "any" just fails safe if that ever drifts.
func (t *Treasury) Killed() bool {
	for _, lane := range t.lanes {
		if lane.Guard.Killed() {
			return true
		}
	}
	return false
}

// CheckCaps is the gate every caller runs BEFORE reserving a leg: kill switch
// first, then the per-order cap, then this chain's total in-flight exposure.
// The treasury holds user funds, so the caps are what bounds a bad day. The
// kill switch is the lane guard's, not a second one.
func (t *Treasury) CheckCaps(chainKey string, orderUSD, inFlightUSD float64) error {
	lane := t.Lane(chainKey)
	if lane == nil {
		return ErrSponsorNotAvailable
	}
	if lane.Guard.Killed() {
		return ErrTreasuryKilled
	}
	if t.caps.MaxInFlightUSDPerOrder > 0 && orderUSD > t.caps.MaxInFlightUSDPerOrder {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_order_cap_total=1 chain=%s order_usd=%.2f cap=%.2f",
			chainKey, orderUSD, t.caps.MaxInFlightUSDPerOrder)
		return ErrTreasuryCapExceeded
	}
	if t.caps.MaxInFlightUSDPerChain > 0 && inFlightUSD+orderUSD > t.caps.MaxInFlightUSDPerChain {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_chain_cap_total=1 chain=%s in_flight=%.2f order=%.2f cap=%.2f",
			chainKey, inFlightUSD, orderUSD, t.caps.MaxInFlightUSDPerChain)
		return ErrTreasuryCapExceeded
	}
	return nil
}

// checkFloor reuses the guard's own floor check — the same one the sponsor
// lanes use — rather than a second copy of "read balance, compare, alert". A
// half-sent conversion is worse than one that never started.
func (t *Treasury) checkFloor(ctx context.Context, lane *TreasuryLane) error {
	bal, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(lane.Signer.Address()), nil)
	if err != nil {
		return fmt.Errorf("treasury: read native balance: %w", err)
	}
	return lane.Guard.CheckFloor(bal) // logs its own ALERT, returns ErrRelayerUnderfunded
}

// ExecuteRoute approves the route's spender if needed, then signs LiFi's
// transactionRequest AS GIVEN — never rebuilt from parts, the same invariant
// the BTC/PSBT path holds. broadcastAttempted separates a build failure
// (nothing left this process) from a broadcast whose outcome is unknown — the
// caller must keep the leg on that path, exactly as Phase 1's lane does.
func (t *Treasury) ExecuteRoute(ctx context.Context, lane *TreasuryLane, token string, amount *big.Int, route *LiFiRoute) (string, bool, error) {
	if lane.Guard.Killed() {
		return "", false, ErrTreasuryKilled
	}
	if err := t.checkFloor(ctx, lane); err != nil {
		return "", false, err
	}
	tokenAddr := lane.tokenAddress(token) // SponsorLane's own lookup
	if tokenAddr == "" || route == nil || route.To == "" || route.Data == "" {
		return "", false, ErrSponsorNotAvailable
	}

	if route.ApprovalAddress != "" {
		// route.ApprovalToken names the exact token LiFi's router will pull
		// (action.fromToken.address). Prefer it over the symbol lookup above:
		// the symbol map is keyed by our own SYMBOL -> address convention and
		// could point at a different representation of the same asset (e.g. a
		// bridged variant) than the one the quoted route actually spends.
		approvalToken := tokenAddr
		if route.ApprovalToken != "" {
			approvalToken = route.ApprovalToken
		}
		allowance, err := lane.Native.GetTokenAllowance(approvalToken, lane.Signer.Address(), route.ApprovalAddress)
		if err != nil {
			return "", false, fmt.Errorf("treasury: read allowance: %w", err)
		}
		if allowance.Cmp(amount) < 0 {
			// Exact-amount approval, never MaxUint256: the spender is a
			// third-party router that changes weekly. lane.send carries the
			// nonce lock and the broadcastAttempted contract.
			approveHash, attempted, aerr := lane.send(ctx, func() ([]byte, error) {
				return lane.Native.PrepareERC20Approve(approvalToken, lane.Signer.Address(), route.ApprovalAddress, amount)
			})
			if aerr != nil {
				return "", attempted, fmt.Errorf("treasury: approve: %w", aerr)
			}
			if _, rerr := lane.Native.WaitForReceiptContext(ctx, approveHash, 60*time.Second); rerr != nil {
				return "", true, fmt.Errorf("treasury: approve unconfirmed: %w", rerr)
			}
		}
	}

	value := new(big.Int)
	if v := strings.TrimSpace(route.Value); v != "" && v != "0x0" {
		if parsed, ok := new(big.Int).SetString(strings.TrimPrefix(v, "0x"), 16); ok {
			value = parsed
		}
	}
	// Task 4's generalized builder, reached through the lane's existing
	// Builder — the same interface the sponsor already holds. A zero
	// route.GasLimit means "estimate", and the builder itself fails closed if
	// estimation fails (e.g. a malformed gasLimit from LiFi parsed to 0).
	txHash, attempted, err := lane.send(ctx, func() ([]byte, error) {
		return lane.Builder.BuildUnsignedCall(lane.Signer.Address(), route.To, common.FromHex(route.Data), value, route.GasLimit)
	})
	if err != nil {
		if attempted {
			log.Printf("[ramp-conversion] ALERT ramp_treasury_broadcast_uncertain_total=1 chain=%s tool=%s err=%v",
				lane.ChainKey, route.Tool, err)
		}
		return "", attempted, fmt.Errorf("treasury: broadcast route: %w", err)
	}
	return txHash, true, nil
}

// TransferToken moves a token out of the treasury — a shortfall top-up or a
// refund. Same uncertain-broadcast contract as ExecuteRoute.
func (t *Treasury) TransferToken(ctx context.Context, lane *TreasuryLane, token, to string, amount *big.Int) (string, bool, error) {
	if lane.Guard.Killed() {
		return "", false, ErrTreasuryKilled
	}
	if err := t.checkFloor(ctx, lane); err != nil {
		return "", false, err
	}
	tokenAddr := lane.tokenAddress(token)
	if tokenAddr == "" {
		return "", false, ErrSponsorNotAvailable
	}
	txHash, attempted, err := lane.send(ctx, func() ([]byte, error) {
		return lane.Native.PrepareERC20Transfer(tokenAddr, lane.Signer.Address(), to, amount)
	})
	if err != nil {
		if attempted {
			log.Printf("[ramp-conversion] ALERT ramp_treasury_broadcast_uncertain_total=1 chain=%s kind=transfer err=%v", lane.ChainKey, err)
		}
		return "", attempted, fmt.Errorf("treasury: broadcast transfer: %w", err)
	}
	return txHash, true, nil
}

// Enabled mirrors RampSponsorship.Enabled(): a treasury with no lanes is
// dormant, not broken.
func (t *Treasury) Enabled() bool { return t != nil && len(t.lanes) > 0 }

// FloatBalances returns the same []SponsorFloat shape jobs.GasFloatMonitor
// already consumes, so a later task can register that existing monitor a
// second time for the treasury instead of writing a new one. Copied from
// RampSponsorship.FloatBalances's loop: per lane, errors.Join, carry on past
// a bad RPC rather than hiding every other chain's reading behind one.
func (t *Treasury) FloatBalances(ctx context.Context) ([]SponsorFloat, error) {
	var out []SponsorFloat
	var errs []error
	for chain, lane := range t.lanes {
		addr := lane.Signer.Address()
		bal, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(addr), nil)
		if err != nil {
			errs = append(errs, fmt.Errorf("treasury float %s: %w", chain, err))
			continue
		}
		out = append(out, SponsorFloat{Chain: chain, Address: addr, BalanceWei: bal, FloorWei: lane.Guard.Floor()})
	}
	return out, errors.Join(errs...)
}
