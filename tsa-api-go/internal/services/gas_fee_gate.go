package services

import (
	"context"
	"fmt"
	"log"
	"math/big"
)

// Floors are alert-only (owner decision 2026-10-13). A hot wallet's fixed
// native-balance floor (sponsor, relayer, pool release key, treasury) only
// raises the refill ALERT; whether a transaction goes out is decided by one
// question — can the wallet pay THIS transaction: its gas units at the live
// gas price, times gasFeeBuffer, plus any value it sends (a gas top-up). The
// treasury's requireGas / CanPayForGas asked that question first; every other
// lane now asks it through its key's KeyLane, the one fee gate every send
// passes (KeyLane.requireFee).

// gasFeeBuffer is the headroom on a transaction's own fee, so a price move
// between the check and the broadcast does not leave the send unpayable.
const gasFeeBuffer = 2

// feeNeed is what a wallet must hold to send one transaction: units of gas at
// price, times gasFeeBuffer, plus value (nil = none).
func feeNeed(units uint64, price, value *big.Int) *big.Int {
	return withFeeBuffer(new(big.Int).Mul(new(big.Int).SetUint64(units), price), value)
}

// withFeeBuffer is cost (units × price, already computed) times gasFeeBuffer,
// plus value (nil = none).
func withFeeBuffer(cost, value *big.Int) *big.Int {
	need := new(big.Int).Mul(cost, big.NewInt(gasFeeBuffer))
	if value != nil {
		need.Add(need, value)
	}
	return need
}

// canPay asks the key's lane fee gate (KeyLane.CanPay — the one gate every
// send passes) whether the signer can pay `units` of gas at today's price plus
// value plus the lane's reservation, before anything is built: a quote-time
// check, or a refusal before a slot is claimed. Below the floor it raises the
// refill ALERT and still passes. ErrRelayerUnderfunded when it cannot pay.
func (l *SponsorLane) canPay(ctx context.Context, units uint64, value *big.Int) error {
	price, err := l.Native.SuggestGasPrice()
	if err != nil {
		return fmt.Errorf("sponsor: gas price on %s: %w", l.ChainKey, err)
	}
	bal, err := l.nonceLane().canPayAs(ctx, l.Signer.Address(), units, price, value)
	if bal != nil {
		l.Guard.AlertFloor(bal)
	}
	return err
}

// AlertFloor logs the refill ALERT when balance is below the guard's floor and
// reports whether it was. It never refuses anything.
func (g *SponsorGuard) AlertFloor(balance *big.Int) bool {
	if g.limits.BalanceFloor == nil || balance == nil || balance.Cmp(g.limits.BalanceFloor) >= 0 {
		return false
	}
	log.Printf("[%s] ALERT %s_underfunded_total=1 balance=%s floor=%s — below the refill floor; sends continue while the balance pays each transaction's gas",
		g.name, g.name, balance, g.limits.BalanceFloor)
	return true
}
