package services

import (
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
// lane now asks it through feeNeed.

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

// requireFee refuses (ErrRelayerUnderfunded, the existing class every caller
// maps to "gas unavailable") only when the lane's signer cannot pay for `units`
// of gas at today's price plus value. Below the floor it raises the refill
// ALERT and still sends. balance is the signer's native balance, already read.
func (l *SponsorLane) requireFee(balance *big.Int, units uint64, value *big.Int) error {
	l.Guard.AlertFloor(balance)
	cost, err := l.gasCost(units) // units × SuggestGasPrice
	if err != nil {
		return fmt.Errorf("sponsor: %w", err)
	}
	return l.Guard.RequireFee(balance, withFeeBuffer(cost, value))
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

// RequireFee refuses with ErrRelayerUnderfunded when balance cannot cover need
// (one transaction's buffered fee plus value). It is the only balance gate.
func (g *SponsorGuard) RequireFee(balance, need *big.Int) error {
	if balance == nil || need == nil || balance.Cmp(need) >= 0 {
		return nil
	}
	log.Printf("[%s] ALERT %s_cannot_pay_total=1 balance=%s needed=%s", g.name, g.name, balance, need)
	return fmt.Errorf("%w: balance %s wei, this transaction needs %s", ErrRelayerUnderfunded, balance, need)
}
