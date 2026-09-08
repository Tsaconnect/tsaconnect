package sui

// Signing an aggregator-built Sui transaction.
//
// A LiFi Sui route returns transactionRequest.data and nothing else: a whole
// BCS-serialized TransactionData the aggregator built, base64-encoded. Unlike
// Bitcoin's PSBT — where the outputs can be read directly and checked one by
// one — a Sui transaction is a programmable block whose effects are not
// apparent from its bytes. Parsing it well enough to know what it spends would
// mean reimplementing a chunk of the VM.
//
// The node will tell us instead. sui_dryRunTransactionBlock simulates the
// transaction and reports the balance changes it would cause, which is a
// stronger check than any parse: it is the chain's own answer to "what does
// this actually move". So the rule here is simple and enforced before a signing
// digest is ever handed out:
//
//   - the simulation must succeed (a transaction that would abort is not signed)
//   - the coin being swapped may leave by no more than the amount approved
//   - SUI may additionally leave to pay for gas, by no more than the gas the
//     simulation itself reported — Sui charges gas to the sender's SUI coin, so
//     it shows up in the balance changes like any other outflow
//   - no other coin type may leave at all: a swap the user asked for in one
//     coin has no business touching a second one
//   - nothing may leave any address that is not ours — we are the only signer,
//     but a transaction that moves someone else's funds is not one we want to
//     put our name on either
//   - no OBJECT of ours may end up owned by another address. Balance changes
//     cover coins and nothing else, so an NFT, an LP position, a staked-SUI
//     receipt or a DeepBook capability can leave the wallet without moving a
//     unit of any coin — a transaction that transfers one simulates as a
//     success whose only balance change is its own gas.
//
// What survives is handed to the same digest/broadcast path a normal Sui send
// uses, so signing, assembly and submission are one code path in both cases.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/chains"
)

// errUnattributableOwner marks a balance change owned by something other than
// an address (a shared or wrapped object), which cannot be tied to a user.
var errUnattributableOwner = errors.New("sui: balance change has no address owner")

// prepareFromTxBytes validates an aggregator-supplied transaction against what
// the user approved (req.From spending at most req.Amount of req.Symbol) and
// returns the digest to sign.
func (c *Client) prepareFromTxBytes(ctx context.Context, req chains.TransferRequest) (chains.PreparedTx, error) {
	if err := ValidateAddress(req.From); err != nil {
		return chains.PreparedTx{}, chains.Invalid("invalid from address")
	}
	txB64 := strings.TrimSpace(req.TxBytesB64)
	raw, err := base64.StdEncoding.DecodeString(txB64)
	if err != nil || len(raw) == 0 {
		return chains.PreparedTx{}, chains.Invalid("the route's transaction is not valid base64")
	}

	sim, err := c.rpc.DryRun(ctx, txB64)
	if err != nil {
		// A transaction we cannot simulate is one we cannot vouch for. Refusing
		// costs the user a swap; signing blind can cost them the wallet.
		//
		// The detail stays in the log rather than the response: a failed call
		// wraps net/http's error, which quotes the endpoint URL — and for a
		// keyed provider that URL is a credential. It is also not the caller's
		// mistake, so it must not come back as a 400.
		log.Printf("sui: dry-run failed for %s: %v", req.From, err)
		return chains.PreparedTx{}, fmt.Errorf("the route could not be checked against the network")
	}
	if !strings.EqualFold(sim.Effects.Status.Status, "success") {
		return chains.PreparedTx{}, chains.Invalid("the route's transaction would fail on-chain: %s",
			firstNonEmpty(sim.Effects.Status.Error, sim.Effects.Status.Status))
	}

	approved, ok := new(big.Int).SetString(strings.TrimSpace(req.Amount), 10)
	if !ok || approved.Sign() <= 0 {
		return chains.PreparedTx{}, chains.Invalid("bad amount")
	}
	// An empty TokenAddr means the swap is denominated in the native coin, the
	// same convention the normal send path uses (see isNativeSend).
	fromCoin := strings.TrimSpace(req.TokenAddr)
	if fromCoin == "" {
		fromCoin = nativeCoinType
	}
	if err := checkBalanceChanges(sim.BalanceChanges, req.From, fromCoin, approved, sim.Effects.GasUsed.NetGas()); err != nil {
		return chains.PreparedTx{}, err
	}
	if err := checkObjectChanges(sim.ObjectChanges, req.From); err != nil {
		return chains.PreparedTx{}, err
	}

	digest := signingDigest(raw)
	return chains.PreparedTx{
		Family: "sui",
		Raw:    txB64,
		Payloads: []chains.SigningPayload{{
			Index:   0,
			HashHex: hex.EncodeToString(digest[:]),
			Curve:   chains.CurveEd25519,
		}},
	}, nil
}

// checkBalanceChanges enforces that the simulated transaction only takes from
// `owner`, and takes no more than `approved` of any single coin type.
//
// The per-coin-type allowance is what stops a route quoted as "swap 1 SUI" from
// also sweeping an unrelated coin: each outflow is measured against what that
// particular coin is allowed to lose, rather than against one shared ceiling
// that a net total could hide a second withdrawal inside.
func checkBalanceChanges(changes []DryRunBalanceChange, owner, fromCoinType string, approved, gasAllowance *big.Int) error {
	ownerLower := strings.ToLower(strings.TrimSpace(owner))
	fromCoin := normalizeCoinType(strings.TrimSpace(fromCoinType))
	for _, ch := range changes {
		amount, ok := new(big.Int).SetString(strings.TrimSpace(ch.Amount), 10)
		if !ok {
			return chains.Invalid("the network reported a balance change we cannot read")
		}
		if amount.Sign() >= 0 {
			continue // funds arriving are never the risk
		}
		addr, err := balanceChangeOwner(ch.Owner)
		if err != nil {
			return chains.Invalid("the network reported a balance change we cannot attribute")
		}
		if !strings.EqualFold(addr, ownerLower) {
			return chains.Invalid("the route moves funds out of an address that is not yours")
		}
		coin := normalizeCoinType(strings.TrimSpace(ch.CoinType))
		allowance := outflowAllowance(coin, fromCoin, approved, gasAllowance)
		outflow := new(big.Int).Neg(amount)
		if outflow.Cmp(allowance) <= 0 {
			continue
		}
		switch {
		case allowance.Sign() == 0:
			return chains.Invalid("the route would move %s out of your wallet, which this swap does not involve",
				shortTypeName(ch.CoinType))
		case coin != fromCoin:
			// Native SUI leaving a swap it is not the subject of is gas, and
			// the only gas we vouch for is the amount the node just quoted.
			return chains.Invalid("the route would take %s MIST for gas but the network estimated only %s",
				outflow, allowance)
		default:
			return chains.Invalid("the route would spend %s of %s but only %s was approved",
				outflow, shortTypeName(ch.CoinType), allowance)
		}
	}
	return nil
}

// outflowAllowance is how much of one coin type may leave the user's address:
// what they approved for the coin being swapped, plus the simulation's own gas
// for SUI (both at once when the swap is itself denominated in SUI), and
// nothing whatsoever for any other coin.
func outflowAllowance(coin, fromCoin string, approved, gasAllowance *big.Int) *big.Int {
	allowance := new(big.Int)
	if coin == fromCoin {
		allowance.Add(allowance, approved)
	}
	if isNativeCoinType(coin) {
		allowance.Add(allowance, gasAllowance)
	}
	return allowance
}

// nativeCoinPackage is the Sui framework's address in the padded form the RPC
// returns; "0x2" is the same package written short.
const nativeCoinPackage = "0x0000000000000000000000000000000000000000000000000000000000000002"

// checkObjectChanges enforces that no object of ours ends up in somebody else's
// wallet.
//
// This is the other half of the spend check, and the half a coin ledger cannot
// give us: on Sui most value that is not a coin is an object, and transferring
// one moves no balance at all. Without this, a route could hand back a
// transaction that simulates as a clean success — a single balance change for
// its own gas — while assigning an NFT or a trading capability to an address
// the user has never seen.
func checkObjectChanges(changes []DryRunObjectChange, owner string) error {
	ownerLower := strings.ToLower(strings.TrimSpace(owner))
	for _, oc := range changes {
		// A coin's value is already bounded by checkBalanceChanges, and a
		// cross-chain route legitimately pays one to a bridge's address —
		// refusing that here would break every Sui-to-elsewhere swap.
		if isCoinType(oc.ObjectType) {
			continue
		}
		// Anything that is not owned by a plain address is not in a wallet:
		// shared, immutable, wrapped inside another object, or destroyed. None
		// of those is a theft, and all of them are ordinary DeFi bookkeeping.
		addr, ok := addressOwnerOf(oc.Owner)
		if !ok {
			continue
		}
		if !strings.EqualFold(addr, ownerLower) {
			return chains.Invalid("the route would move %s out of your wallet",
				shortTypeName(oc.ObjectType))
		}
	}
	return nil
}

// isCoinType reports whether a Move type is the framework's own Coin<T>, in
// either the short or the padded form of its package address. The package is
// checked rather than the module name alone: a type is only a coin because
// 0x2 says so, and a package of somebody else's that happens to call its
// module "coin" must not inherit the exemption above.
func isCoinType(objectType string) bool {
	t := strings.TrimSpace(objectType)
	return strings.HasPrefix(t, "0x2::coin::Coin<") ||
		strings.HasPrefix(t, nativeCoinPackage+"::coin::Coin<")
}

// addressOwnerOf returns the address an object ends up owned by, and false when
// it lands somewhere that is not a wallet at all. Only AddressOwner counts:
// ObjectOwner means it went inside another object, Shared and Immutable mean it
// belongs to nobody, and an absent or unreadable owner belongs to nobody either.
func addressOwnerOf(rawOwner json.RawMessage) (string, bool) {
	var asObject map[string]json.RawMessage
	if err := json.Unmarshal(rawOwner, &asObject); err != nil {
		return "", false
	}
	raw, ok := asObject["AddressOwner"]
	if !ok {
		return "", false
	}
	var addr string
	if err := json.Unmarshal(raw, &addr); err != nil {
		return "", false
	}
	return strings.ToLower(addr), true
}

// balanceChangeOwner pulls the address out of Sui's Owner enum, which is a
// string ("Immutable") or an object ({"AddressOwner": "0x…"}, {"ObjectOwner":
// …}, {"Shared": …}). Only an address-owned change is attributable to a user.
func balanceChangeOwner(rawOwner json.RawMessage) (string, error) {
	var asObject map[string]json.RawMessage
	if err := json.Unmarshal(rawOwner, &asObject); err != nil {
		return "", err
	}
	for _, key := range []string{"AddressOwner", "ObjectOwner"} {
		if v, ok := asObject[key]; ok {
			var addr string
			if err := json.Unmarshal(v, &addr); err != nil {
				return "", err
			}
			return strings.ToLower(addr), nil
		}
	}
	return "", errUnattributableOwner
}

// shortTypeName trims a fully-qualified Move type to its last segment for error
// messages ("0x2::sui::SUI" → "SUI", "0x…::balance_manager::TradeCap" →
// "TradeCap").
func shortTypeName(coinType string) string {
	if i := strings.LastIndex(coinType, "::"); i >= 0 && i+2 < len(coinType) {
		return coinType[i+2:]
	}
	if coinType == "" {
		return "a coin"
	}
	return coinType
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return "unknown"
}
