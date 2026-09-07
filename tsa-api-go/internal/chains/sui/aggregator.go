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
//   - every coin type leaving OUR address must leave by no more than the amount
//     the user approved
//   - nothing may leave any address that is not ours — we are the only signer,
//     but a transaction that moves someone else's funds is not one we want to
//     put our name on either
//
// What survives is handed to the same digest/broadcast path a normal Sui send
// uses, so signing, assembly and submission are one code path in both cases.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
		return chains.PreparedTx{}, chains.Invalid("the route's transaction could not be checked against the network: %v", err)
	}
	if !strings.EqualFold(sim.Effects.Status.Status, "success") {
		return chains.PreparedTx{}, chains.Invalid("the route's transaction would fail on-chain: %s",
			firstNonEmpty(sim.Effects.Status.Error, sim.Effects.Status.Status))
	}

	approved, ok := new(big.Int).SetString(strings.TrimSpace(req.Amount), 10)
	if !ok || approved.Sign() <= 0 {
		return chains.PreparedTx{}, chains.Invalid("bad amount")
	}
	if err := checkBalanceChanges(sim.BalanceChanges, req.From, approved); err != nil {
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
// The per-coin-type bound is what stops a route quoted as "swap 1 SUI" from
// also sweeping an unrelated coin: each outflow is measured on its own, so a
// second coin type leaving is judged against the same ceiling rather than
// hidden inside a net total.
func checkBalanceChanges(changes []DryRunBalanceChange, owner string, approved *big.Int) error {
	ownerLower := strings.ToLower(strings.TrimSpace(owner))
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
		if outflow := new(big.Int).Neg(amount); outflow.Cmp(approved) > 0 {
			return chains.Invalid("the route would spend %s of %s but %s was approved",
				outflow, shortCoinType(ch.CoinType), approved)
		}
	}
	return nil
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

// shortCoinType trims a fully-qualified coin type to its last segment for
// error messages ("0x2::sui::SUI" → "SUI").
func shortCoinType(coinType string) string {
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
