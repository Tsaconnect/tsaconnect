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
//
// Defence in depth only: the router allowlist (NonEVMChainConfig.Routers) is
// what keeps a route on the aggregator's own contract.
var tokenMovingSelectors = map[string]string{
	"a9059cbb": "transfer",
	"095ea7b3": "approve",
	"23b872dd": "transferFrom",
	"39509351": "increaseAllowance",
	"d73dd623": "increaseApproval",
	"cae9ca51": "approveAndCall",
	"4000aea0": "transferAndCall",
	"1296ee62": "transferAndCall",
	"d505accf": "permit",
	"a22cb465": "setApprovalForAll",
	"42842e0e": "safeTransferFrom",
	"b88d4fde": "safeTransferFrom",
	"9bd9bbc6": "send",
}

// maxAggregatorExpiry bounds how far ahead a route's transaction may expire.
// LiFi sets timestamp + 10 minutes; a far-off expiry turns a signed swap into
// one that can be held back and broadcast long after its price is gone.
const maxAggregatorExpiry = 15 * time.Minute

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

// fieldSpec is what one field of a message must look like on the wire.
type fieldSpec struct {
	typ      protowire.Type
	repeated bool
}

type messageSchema struct {
	name   string
	fields map[protowire.Number]fieldSpec
}

var (
	varintField   = fieldSpec{typ: protowire.VarintType}
	bytesField    = fieldSpec{typ: protowire.BytesType}
	repeatedBytes = fieldSpec{typ: protowire.BytesType, repeated: true}

	// protocol.Transaction.raw. 9 auths and 12 scripts are deliberately absent:
	// neither belongs in a swap, so they are refused like any unknown field.
	transactionRawSchema = messageSchema{"transaction", map[protowire.Number]fieldSpec{
		1: bytesField, 3: varintField, 4: bytesField, 8: varintField, 10: bytesField,
		11: repeatedBytes, 14: varintField, 18: varintField,
	}}
	contractSchema = messageSchema{"contract", map[protowire.Number]fieldSpec{
		1: varintField, 2: bytesField, 3: bytesField, 4: bytesField, 5: varintField,
	}}
	anySchema           = messageSchema{"parameter", map[protowire.Number]fieldSpec{1: bytesField, 2: bytesField}}
	triggerSchema       = messageSchema{"TriggerSmartContract", map[protowire.Number]fieldSpec{1: bytesField, 2: bytesField, 3: varintField, 4: bytesField, 5: varintField, 6: varintField}}
	transferValueSchema = messageSchema{"TransferContract", map[protowire.Number]fieldSpec{1: bytesField, 2: bytesField, 3: varintField}}
)

// decodeTransactionRaw parses a protocol.TransactionRaw. A field we do not know
// is refused rather than skipped: the bytes are signed whole, so anything in
// them we cannot interpret is something we would be signing blind.
func decodeTransactionRaw(b []byte) (*decodedRaw, error) {
	out := &decodedRaw{}
	err := walkFields(b, transactionRawSchema, func(num protowire.Number, v uint64, bs []byte) error {
		switch num {
		case 8:
			out.ExpirationMs = int64(v)
		case 18:
			out.FeeLimit, out.HasFeeLimit = int64(v), true
		case 11:
			c, err := decodeContract(bs)
			if err != nil {
				return err
			}
			out.Contracts = append(out.Contracts, c)
		}
		// 1, 3, 4 (ref block), 10 (memo) and 14 (timestamp) affect nothing we
		// check; they are still held to the schema above.
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
	err := walkFields(b, contractSchema, func(num protowire.Number, v uint64, bs []byte) error {
		switch num {
		case 1:
			c.Type = v
		case 2:
			param = bs
		case 5:
			c.PermissionID = v
		}
		// 3 provider, 4 ContractName: labels with no effect on execution.
		return nil
	})
	if err != nil {
		return c, err
	}
	var value []byte
	err = walkFields(param, anySchema, func(num protowire.Number, v uint64, bs []byte) error {
		if num == 1 {
			c.TypeURL = string(bs)
		} else {
			value = bs
		}
		return nil
	})
	if err != nil {
		return c, err
	}
	switch c.Type {
	case contractTypeTriggerSmartContract:
		err = walkFields(value, triggerSchema, func(num protowire.Number, v uint64, bs []byte) error {
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
			}
			return nil
		})
	case contractTypeTransfer:
		err = walkFields(value, transferValueSchema, func(num protowire.Number, v uint64, bs []byte) error {
			switch num {
			case 1:
				c.Owner = bs
			case 2:
				c.Target = bs
			case 3:
				c.Value = int64(v)
			}
			return nil
		})
	}
	// Any other type is left undecoded here and refused by the validator, which
	// can say which type it was.
	return c, err
}

// walkFields calls fn for every field of one message, after holding the bytes
// to the schema AND to the one encoding java-tron itself produces.
//
// The network does not hash the bytes it receives: it parses them and
// re-serializes. Parsing is forgiving — a field repeated, or sent under a
// different wire type, silently replaces or hides the value we read — so a
// lenient decoder can be shown call_value 0 while the node executes 5 TRX.
// Requiring the canonical form (known fields only, the declared wire type,
// ascending field order, each non-repeated field at most once, minimal
// varints, and no explicit proto3 default, which java never writes) means
// there is exactly one reading of these bytes, and it is ours. It is the same
// guarantee as re-encoding and comparing, without a second encoder to keep
// in step.
func walkFields(b []byte, sch messageSchema, fn func(num protowire.Number, v uint64, bs []byte) error) error {
	var last protowire.Number
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fmt.Errorf("malformed protobuf tag in %s", sch.name)
		}
		if n != protowire.SizeTag(num) {
			return fmt.Errorf("non-canonical tag for %s field %d", sch.name, num)
		}
		b = b[n:]
		spec, known := sch.fields[num]
		if !known {
			return fmt.Errorf("unexpected %s field %d", sch.name, num)
		}
		if typ != spec.typ {
			return fmt.Errorf("%s field %d has the wrong wire type", sch.name, num)
		}
		if num < last || (num == last && !spec.repeated) {
			return fmt.Errorf("%s field %d is repeated or out of order", sch.name, num)
		}
		last = num
		var v uint64
		var bs []byte
		if typ == protowire.VarintType {
			v, n = protowire.ConsumeVarint(b)
			if n >= 0 && (n != protowire.SizeVarint(v) || v == 0) {
				return fmt.Errorf("non-canonical value for %s field %d", sch.name, num)
			}
		} else {
			bs, n = protowire.ConsumeBytes(b)
			if n >= 0 && (n != protowire.SizeBytes(len(bs)) || len(bs) == 0) {
				return fmt.Errorf("non-canonical value for %s field %d", sch.name, num)
			}
		}
		if n < 0 {
			return fmt.Errorf("malformed protobuf field %d in %s", num, sch.name)
		}
		b = b[n:]
		if err := fn(num, v, bs); err != nil {
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
	if d.ExpirationMs > now.Add(maxAggregatorExpiry).UnixMilli() {
		return chains.Invalid("the route's transaction expires too far in the future; get a new quote")
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
	if err := c.requireRouter(req.To); err != nil {
		return chains.PreparedTx{}, err
	}
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

// isAggregatorRaw reports whether a prepared RawTx carries only verified hex
// (an aggregator route, or an approval we decoded), which is broadcast as
// exactly those bytes.
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

// requireRouter refuses a route target or approval spender that is not one of
// this network's configured aggregator contracts. The quote names both itself,
// so this list — not the quote — is what decides who may be called or
// approved. An empty list signs nothing.
func (c *Client) requireRouter(addr string) error {
	for _, r := range c.cfg.Routers {
		if r == addr {
			return nil
		}
	}
	return chains.Invalid("%q is not a swap contract we sign for on tron", addr)
}

// verifyBuiltApprove checks the approve TronGrid built before its txID is
// handed out: TronGrid is an upstream like any other, and the request we sent
// it is only what we asked for, not what came back.
func verifyBuiltApprove(raw *RawTx, from, token, spender string, amount *big.Int, now time.Time) error {
	bad := func(why string) error { return fmt.Errorf("the node built an unexpected approval: %s", why) }
	rawBytes, err := decodeRawHex(raw.RawHex)
	if err != nil {
		return bad("raw_data_hex is not hex")
	}
	digest := sha256.Sum256(rawBytes)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), raw.TxID) {
		return bad("txID does not match its bytes")
	}
	d, err := decodeTransactionRaw(rawBytes)
	if err != nil {
		return bad(err.Error())
	}
	if len(d.Contracts) != 1 || d.Contracts[0].Type != contractTypeTriggerSmartContract {
		return bad("not a single contract call")
	}
	c := d.Contracts[0]
	ownerHex, _ := Base58ToHex(from)
	tokenHex, _ := Base58ToHex(token)
	if hex.EncodeToString(c.Owner) != ownerHex || hex.EncodeToString(c.Target) != tokenHex {
		return bad("wrong owner or token")
	}
	if c.TypeURL != acceptedContractTypes[contractTypeTriggerSmartContract] || c.PermissionID != 0 ||
		c.Value != 0 || c.TokenValue != 0 || c.TokenID != 0 {
		return bad("unexpected value, permission or type")
	}
	spenderWord, err := abiAddressWord(spender)
	if err != nil {
		return bad("bad spender")
	}
	want := approveSelector + spenderWord + fmt.Sprintf("%064s", toHexNoPrefix(amount))
	if hex.EncodeToString(c.Data) != want {
		return bad("calldata is not approve(spender, amount)")
	}
	if !d.HasFeeLimit || d.FeeLimit > trc20FeeLimitSun || d.ExpirationMs <= now.UnixMilli() {
		return bad("fee limit or expiration out of range")
	}
	return nil
}
