package tron

// Signing an aggregator-built Tron transaction.
//
// A LiFi route whose source chain is Tron hands back a whole transaction:
// transactionRequest.data is the hex of a serialized protocol.TransactionRaw,
// and its sha256 is the txID the wallet signs. Like a PSBT (and unlike Sui's
// opaque programmable block) that protobuf can be read field by field, so the
// rule is to decode it and refuse anything we would not have built ourselves
// before a signing digest is ever handed out:
//
//   - exactly one contract, and only a TriggerSmartContract (a call) or a
//     TransferContract (a plain TRX send). Every other contract type is a
//     different kind of authority over the account — AccountPermissionUpdate
//     in particular hands the account's keys to someone else, and a route has
//     no reason to carry one.
//   - the owner is the wallet asking to sign, under its owner permission (id
//     0). A different owner is not ours to sign for; a non-zero permission
//     names a multisig key set we know nothing about.
//   - the call targets the route's own contract (req.To) and is not a direct
//     token move: transfer/approve/transferFrom on any contract bypasses the
//     amount the user approved, so those selectors are refused outright.
//   - native TRX leaves by no more than the approved amount (call_value or the
//     TransferContract amount), and not at all when the source is a token. No
//     TRC-10 value may move (call_token_value / token_id).
//   - fee_limit is bounded (maxAggregatorFeeLimitSun) and the transaction has
//     not expired.
//
// What survives is signed as given; AssembleAndBroadcast then sends the exact
// bytes through /wallet/broadcasthex, so the signed and the sent transaction
// cannot drift apart through a JSON re-encoding we never inspected.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/ojimcy/tsa-api-go/internal/chains"
)

// maxAggregatorFeeLimitSun caps the fee_limit we will sign on a route. fee_limit
// is the most TRX the network may burn for energy if the call runs long, so a
// route is free to set it as high as the protocol allows (15,000 TRX) and let a
// looping contract burn the wallet's TRX. LiFi quotes 150 TRX for a SunSwap
// trade (2026-10-14); 200 TRX leaves headroom for a heavier route without
// leaving the ceiling to the aggregator.
const maxAggregatorFeeLimitSun = 200_000_000

// Tron contract types (protocol.Transaction.Contract.ContractType) we accept.
const (
	contractTypeTransfer             = 1
	contractTypeTriggerSmartContract = 31
)

const typeURLPrefix = "type.googleapis.com/protocol."

var acceptedContractTypes = map[uint64]string{
	contractTypeTransfer:             typeURLPrefix + "TransferContract",
	contractTypeTriggerSmartContract: typeURLPrefix + "TriggerSmartContract",
}

// tokenMovingSelectors are the ERC-20/TRC-20 calls that move or expose a token
// balance directly. A route calls the aggregator's contract, which pulls only
// what the approval allows; a route that calls one of these instead is spending
// the wallet outside the amount the user agreed to.
var tokenMovingSelectors = map[string]string{
	"a9059cbb": "transfer",
	"095ea7b3": "approve",
	"23b872dd": "transferFrom",
	"39509351": "increaseAllowance",
	"a22cb465": "setApprovalForAll",
}

// rawContract is the one contract of a decoded TransactionRaw, flattened across
// the two parameter shapes we accept.
type rawContract struct {
	Type         uint64
	TypeURL      string
	PermissionID uint64
	Owner        []byte // 21 bytes, 0x41-prefixed
	Target       []byte // TriggerSmartContract.contract_address or TransferContract.to_address
	Value        int64  // call_value (trigger) or amount (transfer), sun
	Data         []byte // trigger calldata
	TokenValue   int64  // call_token_value (TRC-10)
	TokenID      int64
}

type decodedRaw struct {
	ExpirationMs int64
	FeeLimit     int64
	HasFeeLimit  bool
	Contracts    []rawContract
}

// decodeTransactionRaw parses a protocol.TransactionRaw. A field we do not know
// is refused rather than skipped: the bytes are signed whole, so anything in
// them we cannot interpret is something we would be signing blind.
func decodeTransactionRaw(b []byte) (*decodedRaw, error) {
	out := &decodedRaw{}
	err := walkFields(b, func(num protowire.Number, typ protowire.Type, v uint64, bs []byte) error {
		switch num {
		case 1, 3, 4: // ref_block_bytes, ref_block_num, ref_block_hash
		case 10: // data: a memo, carried but never executed
		case 14: // timestamp
		case 8:
			if typ != protowire.VarintType {
				return fmt.Errorf("expiration has the wrong wire type")
			}
			out.ExpirationMs = int64(v)
		case 18:
			if typ != protowire.VarintType {
				return fmt.Errorf("fee_limit has the wrong wire type")
			}
			out.FeeLimit, out.HasFeeLimit = int64(v), true
		case 11:
			if typ != protowire.BytesType {
				return fmt.Errorf("contract has the wrong wire type")
			}
			c, err := decodeContract(bs)
			if err != nil {
				return err
			}
			out.Contracts = append(out.Contracts, c)
		default:
			// 9 auths and 12 scripts included: neither belongs in a swap.
			return fmt.Errorf("unexpected transaction field %d", num)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func decodeContract(b []byte) (rawContract, error) {
	var c rawContract
	var param []byte
	err := walkFields(b, func(num protowire.Number, typ protowire.Type, v uint64, bs []byte) error {
		switch num {
		case 1:
			c.Type = v
		case 2:
			param = bs
		case 3, 4: // provider, ContractName: labels with no effect on execution
		case 5:
			c.PermissionID = v
		default:
			return fmt.Errorf("unexpected contract field %d", num)
		}
		return nil
	})
	if err != nil {
		return c, err
	}
	var value []byte
	err = walkFields(param, func(num protowire.Number, typ protowire.Type, v uint64, bs []byte) error {
		switch num {
		case 1:
			c.TypeURL = string(bs)
		case 2:
			value = bs
		default:
			return fmt.Errorf("unexpected parameter field %d", num)
		}
		return nil
	})
	if err != nil {
		return c, err
	}
	switch c.Type {
	case contractTypeTriggerSmartContract:
		err = walkFields(value, func(num protowire.Number, typ protowire.Type, v uint64, bs []byte) error {
			switch num {
			case 1:
				c.Owner = bs
			case 2:
				c.Target = bs
			case 3:
				c.Value = int64(v)
			case 4:
				c.Data = bs
			case 5:
				c.TokenValue = int64(v)
			case 6:
				c.TokenID = int64(v)
			default:
				return fmt.Errorf("unexpected TriggerSmartContract field %d", num)
			}
			return nil
		})
	case contractTypeTransfer:
		err = walkFields(value, func(num protowire.Number, typ protowire.Type, v uint64, bs []byte) error {
			switch num {
			case 1:
				c.Owner = bs
			case 2:
				c.Target = bs
			case 3:
				c.Value = int64(v)
			default:
				return fmt.Errorf("unexpected TransferContract field %d", num)
			}
			return nil
		})
	}
	// Any other type is left undecoded here and refused by the validator, which
	// can say which type it was.
	return c, err
}

// walkFields calls fn for every field of one protobuf message. Varints arrive
// in v, length-delimited fields in bs; the two fixed-width types are refused
// because none of the messages we accept uses them.
func walkFields(b []byte, fn func(num protowire.Number, typ protowire.Type, v uint64, bs []byte) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fmt.Errorf("malformed protobuf tag")
		}
		b = b[n:]
		var v uint64
		var bs []byte
		switch typ {
		case protowire.VarintType:
			v, n = protowire.ConsumeVarint(b)
		case protowire.BytesType:
			bs, n = protowire.ConsumeBytes(b)
		default:
			return fmt.Errorf("unexpected protobuf wire type %d in field %d", typ, num)
		}
		if n < 0 {
			return fmt.Errorf("malformed protobuf field %d", num)
		}
		b = b[n:]
		if err := fn(num, typ, v, bs); err != nil {
			return err
		}
	}
	return nil
}

// validateAggregatorRaw holds a decoded aggregator transaction to what the user
// approved. Every refusal is a chains.Invalid: the route is what is wrong, and
// the app should say so rather than "prepare failed".
func validateAggregatorRaw(d *decodedRaw, req chains.TransferRequest, now time.Time) error {
	if len(d.Contracts) != 1 {
		return chains.Invalid("the route's transaction must contain exactly one action, found %d", len(d.Contracts))
	}
	c := d.Contracts[0]
	wantURL, ok := acceptedContractTypes[c.Type]
	if !ok {
		return chains.Invalid("the route's transaction is not a transfer or a contract call (type %d); it cannot be signed", c.Type)
	}
	if c.TypeURL != wantURL {
		return chains.Invalid("the route's transaction is malformed: its parameter does not match its type")
	}
	if c.PermissionID != 0 {
		return chains.Invalid("the route's transaction signs under a non-owner permission")
	}
	ownerHex, err := Base58ToHex(req.From)
	if err != nil || !IsValidBase58(req.From) {
		return chains.Invalid("invalid from address")
	}
	if hex.EncodeToString(c.Owner) != ownerHex {
		return chains.Invalid("the route's transaction spends from an address that is not yours")
	}
	targetHex, err := Base58ToHex(req.To)
	if err != nil || !IsValidBase58(req.To) {
		return chains.Invalid("the route's contract address is missing or invalid")
	}
	if hex.EncodeToString(c.Target) != targetHex {
		return chains.Invalid("the route's transaction calls a different contract than the route names")
	}
	if c.TokenValue != 0 || c.TokenID != 0 {
		return chains.Invalid("the route's transaction moves a TRC-10 token")
	}
	approved, ok := new(big.Int).SetString(strings.TrimSpace(req.Amount), 10)
	if !ok || approved.Sign() <= 0 {
		return chains.Invalid("bad amount")
	}
	if c.Value < 0 {
		return chains.Invalid("the route's transaction carries a negative TRX value")
	}
	value := big.NewInt(c.Value)
	tokenSource := strings.TrimSpace(req.TokenAddr) != ""

	switch c.Type {
	case contractTypeTriggerSmartContract:
		if tokenSource {
			// The token leaves through the aggregator's transferFrom, bounded
			// by the approval; TRX riding along would be a second, unapproved
			// spend.
			if value.Sign() != 0 {
				return chains.Invalid("the route's transaction also sends TRX on a token swap")
			}
			tokenHex, err := Base58ToHex(req.TokenAddr)
			if err != nil || !IsValidBase58(req.TokenAddr) {
				return chains.Invalid("invalid token address")
			}
			if tokenHex == targetHex {
				return chains.Invalid("the route's transaction calls the token directly")
			}
		} else if value.Cmp(approved) > 0 {
			return chains.Invalid("the route's transaction sends more TRX than you approved")
		}
		if len(c.Data) >= 4 {
			if name, bad := tokenMovingSelectors[hex.EncodeToString(c.Data[:4])]; bad {
				return chains.Invalid("the route's transaction is a direct token %s, not a swap", name)
			}
		}
		// A contract call without a fee_limit lets the node apply its own
		// default, which is not a ceiling we chose.
		if !d.HasFeeLimit {
			return chains.Invalid("the route's transaction has no fee limit")
		}
	case contractTypeTransfer:
		if tokenSource {
			return chains.Invalid("the route's transaction sends TRX on a token swap")
		}
		if value.Cmp(approved) > 0 {
			return chains.Invalid("the route's transaction sends more TRX than you approved")
		}
	}
	if d.HasFeeLimit && (d.FeeLimit < 0 || d.FeeLimit > maxAggregatorFeeLimitSun) {
		return chains.Invalid("the route's fee limit of %s TRX is above the %d TRX we allow",
			strconv.FormatFloat(float64(d.FeeLimit)/1e6, 'f', -1, 64), maxAggregatorFeeLimitSun/1_000_000)
	}
	if d.ExpirationMs <= now.UnixMilli() {
		return chains.Invalid("the route's transaction has expired; get a new quote")
	}
	return nil
}

// decodeRawHex accepts the hex with or without 0x (LiFi's transactionRequest.data
// carries it, customData.tronTransaction.raw_data_hex does not).
func decodeRawHex(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "0x"), "0X")
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) == 0 {
		return nil, chains.Invalid("the route's transaction is not valid hex")
	}
	return raw, nil
}

// prepareFromRawHex validates an aggregator-supplied transaction against what
// the user approved and returns its txID to sign.
func (c *Client) prepareFromRawHex(req chains.TransferRequest) (chains.PreparedTx, error) {
	raw, err := decodeRawHex(req.TronRawHex)
	if err != nil {
		return chains.PreparedTx{}, err
	}
	decoded, err := decodeTransactionRaw(raw)
	if err != nil {
		return chains.PreparedTx{}, chains.Invalid("the route's transaction could not be read: %v", err)
	}
	if err := validateAggregatorRaw(decoded, req, c.now()); err != nil {
		return chains.PreparedTx{}, err
	}
	digest := sha256.Sum256(raw)
	txID := hex.EncodeToString(digest[:])
	// No raw_data JSON: its absence is what routes AssembleAndBroadcast to the
	// broadcasthex path, which sends exactly these bytes.
	rawJSON, _ := json.Marshal(RawTx{TxID: txID, RawHex: hex.EncodeToString(raw)})
	return chains.PreparedTx{
		Family:   "tron",
		Raw:      string(rawJSON),
		Payloads: []chains.SigningPayload{{Index: 0, HashHex: txID, Curve: chains.CurveSecp256k1}},
	}, nil
}

// isAggregatorRaw reports whether a prepared RawTx came from prepareFromRawHex.
func isAggregatorRaw(raw *RawTx) bool {
	return len(bytes.TrimSpace(raw.RawData)) == 0 || string(bytes.TrimSpace(raw.RawData)) == "null"
}

// broadcastAggregatorRaw assembles protocol.Transaction{raw_data: 1, signature: 2}
// from the exact bytes that were validated and signed, and broadcasts it.
func (c *Client) broadcastAggregatorRaw(ctx context.Context, raw *RawTx, sigHex string) (string, error) {
	rawBytes, err := decodeRawHex(raw.RawHex)
	if err != nil {
		return "", err
	}
	// The prepared tx makes a round trip through the client; the signature is
	// over TxID, so the bytes we send must still be the bytes it names.
	digest := sha256.Sum256(rawBytes)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), raw.TxID) {
		return "", chains.Invalid("the signed transaction does not match its txID")
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(sigHex), "0x"))
	if err != nil || len(sig) != 65 {
		return "", chains.Invalid("a tron signature is 65 bytes of hex")
	}
	tx := protowire.AppendTag(nil, 1, protowire.BytesType)
	tx = protowire.AppendBytes(tx, rawBytes)
	tx = protowire.AppendTag(tx, 2, protowire.BytesType)
	tx = protowire.AppendBytes(tx, sig)
	if err := c.tg.BroadcastHex(ctx, hex.EncodeToString(tx)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest[:]), nil
}
