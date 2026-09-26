package solana

import (
	"context"
	"fmt"
	"math/big"

	solanago "github.com/gagliardetto/solana-go"
	solanarpc "github.com/gagliardetto/solana-go/rpc"
)

// landing.go — what a transaction actually delivered to an address.
//
// A bridge tells us WHICH transaction paid the user; only the chain says how
// much. On Solana there is no transfer log to read: the proof is the
// transaction's own balance changes, which is exactly what history already
// reads for the activity feed — the same two deltas (lamports at the owner's
// account index, and the owner's per-mint token balances), asked a different
// question.

// ReceivedBy reports what owner was credited by txHash, in base units: lamports
// when mint is empty (or the System Program id, which is how LiFi spells native
// SOL), and the mint's own units otherwise.
//
// Being credited nothing is an answer, not an error: it returns zero. Only a
// transaction that cannot be read at all — bad input, an RPC failure, a
// signature the cluster does not know, or one that failed on chain — is an
// error, because "nothing landed" and "we could not look" must never be the
// same value to a caller deciding whether a user has been paid.
func (c *Client) ReceivedBy(ctx context.Context, txHash, owner, mint string) (*big.Int, error) {
	sig, err := solanago.SignatureFromBase58(txHash)
	if err != nil {
		return nil, fmt.Errorf("invalid transaction signature %q: %w", txHash, err)
	}
	pk, err := solanago.PublicKeyFromBase58(owner)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}
	if !isNativeMint(mint) {
		if _, err := solanago.PublicKeyFromBase58(mint); err != nil {
			return nil, fmt.Errorf("invalid mint %q: %w", mint, err)
		}
	}
	res, err := c.rpc.Transaction(ctx, sig)
	if err != nil {
		return nil, err
	}
	if res == nil || res.Meta == nil {
		return nil, fmt.Errorf("transaction %s not found", txHash)
	}
	if res.Meta.Err != nil {
		// A failed transaction moved nothing, but reporting that as a zero
		// delivery would read as "the bridge short-paid her" rather than
		// "this is not the transaction that paid her".
		return nil, fmt.Errorf("transaction %s failed on chain", txHash)
	}
	return receivedFrom(res, pk, mint), nil
}

// isNativeMint reports whether mint names native SOL rather than an SPL token.
// The empty string is the in-process spelling; the System Program id is what
// LiFi and our own order rows carry, since a native coin has no mint account.
func isNativeMint(mint string) bool {
	return mint == "" || mint == solanago.SystemProgramID.String()
}

// receivedFrom is the pure half of ReceivedBy: the owner's CREDIT in one
// transaction, never a debit. A negative delta means the owner paid rather than
// was paid, and is reported as zero — a landing proof answers "how much did
// this address receive", and a sender receiving nothing is the honest answer.
func receivedFrom(res *solanarpc.GetTransactionResult, owner solanago.PublicKey, mint string) *big.Int {
	if res == nil || res.Meta == nil {
		return new(big.Int)
	}
	if isNativeMint(mint) {
		return credit(ownerSOLDelta(owner, res))
	}
	pre := ownerTokenAmounts(owner, res.Meta.PreTokenBalances)
	post := ownerTokenAmounts(owner, res.Meta.PostTokenBalances)
	before, after := new(big.Int), new(big.Int)
	if v, ok := pre[mint]; ok {
		before = v.amount
	}
	if v, ok := post[mint]; ok {
		after = v.amount
	}
	return credit(new(big.Int).Sub(after, before))
}

// credit clamps a signed delta to what was received: zero for nil (the owner
// is not in the transaction at all) and for any debit.
func credit(delta *big.Int) *big.Int {
	if delta == nil || delta.Sign() < 0 {
		return new(big.Int)
	}
	return delta
}

// ownerSOLDelta is the owner's SIGNED lamport change in a transaction — post
// minus pre at the owner's own account index — or nil when the transaction
// cannot be decoded or the owner is not one of its account keys.
//
// The base fee is deliberately NOT deducted here. What the fee payer is out of
// pocket is a presentation choice for the activity feed (see activityFor);
// what the chain credited an address is what a landing proof needs, and the two
// must not be the same number.
func ownerSOLDelta(owner solanago.PublicKey, res *solanarpc.GetTransactionResult) *big.Int {
	delta, _ := ownerLamportDelta(owner, res)
	return delta
}

// ownerLamportDelta is ownerSOLDelta plus the owner's account index, which is
// the only thing that says whether the owner paid the transaction's fee (index
// 0 is always the fee payer). Both callers need the delta; only the activity
// feed needs the index, and splitting it out this way keeps the transaction
// decode to one per call.
func ownerLamportDelta(owner solanago.PublicKey, res *solanarpc.GetTransactionResult) (*big.Int, int) {
	if res == nil || res.Meta == nil {
		return nil, -1
	}
	tx, err := res.Transaction.GetTransaction()
	if err != nil || tx == nil {
		return nil, -1
	}
	for idx, k := range accountKeys(tx, res.Meta) {
		if !k.Equals(owner) {
			continue
		}
		if idx >= len(res.Meta.PreBalances) || idx >= len(res.Meta.PostBalances) {
			return nil, -1
		}
		return new(big.Int).Sub(
			new(big.Int).SetUint64(res.Meta.PostBalances[idx]),
			new(big.Int).SetUint64(res.Meta.PreBalances[idx]),
		), idx
	}
	return nil, -1
}

// accountKeys is every account a transaction touches, in the order the balance
// arrays are indexed by.
//
// A version-0 transaction names only some of its accounts in the message; the
// rest are loaded from an address lookup table and come back in
// meta.loadedAddresses, writable first, then read-only. Message.AccountKeys
// holds the static ones alone until the lookups are resolved, and we ask for
// them with maxSupportedTransactionVersion 0, so v0 transactions do arrive
// here. Reading only the static keys does not mis-index anything (the static
// keys come first) — it simply cannot find an owner that was loaded, which
// reads as "credited nothing" for an address that was in fact paid.
//
// Nothing is fetched: the loaded addresses are already in the response the
// caller has. The message is not mutated either, since the same decode is
// shared with whatever else reads this transaction.
func accountKeys(tx *solanago.Transaction, meta *solanarpc.TransactionMeta) []solanago.PublicKey {
	static := tx.Message.AccountKeys
	loaded := len(meta.LoadedAddresses.Writable) + len(meta.LoadedAddresses.ReadOnly)
	if loaded == 0 || tx.Message.IsResolved() {
		// IsResolved means the lookups were already appended to AccountKeys;
		// appending them again would index the same account twice.
		return static
	}
	all := make([]solanago.PublicKey, 0, len(static)+loaded)
	all = append(all, static...)
	all = append(all, meta.LoadedAddresses.Writable...)
	all = append(all, meta.LoadedAddresses.ReadOnly...)
	return all
}
