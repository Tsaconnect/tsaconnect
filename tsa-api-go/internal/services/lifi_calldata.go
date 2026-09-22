package services

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// A LiFi route's calldata decides who receives the funds the diamond pulls
// from the treasury. The router allowlist pins WHICH contract is called, not
// where that contract then sends the money, so the calldata itself is decoded
// and its receiver checked before anything is signed. LiFi's status API says
// the same thing only after the funds have left.
//
// Two layouts cover every route LiFi returns for a treasury conversion
// (verified against live calldata in testdata/lifi_calldata):
//
//   - cross-chain: every bridge facet takes ILiFi.BridgeData as its first
//     argument, and that struct names the receiver and destination chain.
//   - same-chain: the GenericSwapFacet(V3) functions take the receiver as
//     their fourth argument and have no BridgeData at all.
//
// The selector picks the layout, never a guess from the bytes: a generic swap
// can put an attacker-chosen offset in its first word (transactionId) that
// points at a fake BridgeData naming the user while its real receiver is
// someone else. Anything that is not a known swap selector must decode as
// BridgeData or it is refused.

// lifiBridgeData mirrors ILiFi.BridgeData. Field names follow the ABI
// component names so abi.ConvertType can fill it.
type lifiBridgeData struct {
	TransactionId      [32]byte
	Bridge             string
	Integrator         string
	Referrer           common.Address
	SendingAssetId     common.Address
	Receiver           common.Address
	MinAmount          *big.Int
	DestinationChainId *big.Int
	HasSourceSwaps     bool
	HasDestinationCall bool
}

var lifiBridgeDataType = func() abi.Type {
	t, err := abi.NewType("tuple", "", []abi.ArgumentMarshaling{
		{Name: "transactionId", Type: "bytes32"},
		{Name: "bridge", Type: "string"},
		{Name: "integrator", Type: "string"},
		{Name: "referrer", Type: "address"},
		{Name: "sendingAssetId", Type: "address"},
		{Name: "receiver", Type: "address"},
		{Name: "minAmount", Type: "uint256"},
		{Name: "destinationChainId", Type: "uint256"},
		{Name: "hasSourceSwaps", Type: "bool"},
		{Name: "hasDestinationCall", Type: "bool"},
	})
	if err != nil {
		panic(err)
	}
	return t
}()

// lifiGenericSwapHead is the leading, shared part of every GenericSwapFacet
// function: (transactionId, integrator, referrer, receiver, minAmountOut).
// Only these are unpacked; the swap data that follows does not affect who is
// paid.
var lifiGenericSwapHead = func() abi.Arguments {
	var args abi.Arguments
	for _, typ := range []string{"bytes32", "string", "string", "address", "uint256"} {
		t, err := abi.NewType(typ, "", nil)
		if err != nil {
			panic(err)
		}
		args = append(args, abi.Argument{Type: t})
	}
	return args
}()

// lifiGenericSwapSelectors are the GenericSwapFacet and GenericSwapFacetV3
// entry points. Derived from their signatures rather than pasted as hex, so
// a typo cannot silently route a swap through the BridgeData decoder.
var lifiGenericSwapSelectors = func() map[[4]byte]string {
	const swapData = "(address,address,address,address,uint256,bytes,bool)"
	const head = "bytes32,string,string,address,uint256,"
	out := map[[4]byte]string{}
	for _, sig := range []string{
		"swapTokensGeneric(" + head + swapData + "[])",
		"swapTokensSingleV3ERC20ToERC20(" + head + swapData + ")",
		"swapTokensSingleV3ERC20ToNative(" + head + swapData + ")",
		"swapTokensSingleV3NativeToERC20(" + head + swapData + ")",
		"swapTokensMultipleV3ERC20ToERC20(" + head + swapData + "[])",
		"swapTokensMultipleV3ERC20ToNative(" + head + swapData + "[])",
		"swapTokensMultipleV3NativeToERC20(" + head + swapData + "[])",
	} {
		var sel [4]byte
		copy(sel[:], crypto.Keccak256([]byte(sig))[:4])
		out[sel] = sig
	}
	return out
}()

// lifiDelivery is where a LiFi diamond call sends what it pulls.
type lifiDelivery struct {
	Receiver           common.Address
	DestinationChainID int64
	// HasDestinationCall means the funds are handed to a contract call on
	// arrival, which can forward them anywhere: Receiver is then not the
	// final recipient.
	HasDestinationCall bool
}

var errLiFiCalldata = errors.New("unrecognised LiFi calldata")

// decodeLiFiDelivery reads the receiver and destination out of a LiFi diamond
// call. sourceChainID is the destination of a same-chain swap, which carries
// no chain id of its own.
func decodeLiFiDelivery(data []byte, sourceChainID int64) (lifiDelivery, error) {
	if len(data) < 4 {
		return lifiDelivery{}, fmt.Errorf("%w: %d bytes", errLiFiCalldata, len(data))
	}
	var sel [4]byte
	copy(sel[:], data[:4])
	args := data[4:]

	if _, ok := lifiGenericSwapSelectors[sel]; ok {
		vals, err := lifiGenericSwapHead.Unpack(args)
		if err != nil {
			return lifiDelivery{}, fmt.Errorf("%w: generic swap: %v", errLiFiCalldata, err)
		}
		return lifiDelivery{Receiver: vals[3].(common.Address), DestinationChainID: sourceChainID}, nil
	}

	vals, err := abi.Arguments{{Type: lifiBridgeDataType}}.Unpack(args)
	if err != nil {
		return lifiDelivery{}, fmt.Errorf("%w: selector %x: %v", errLiFiCalldata, sel, err)
	}
	bd, ok := abi.ConvertType(vals[0], new(lifiBridgeData)).(*lifiBridgeData)
	if !ok || bd.DestinationChainId == nil || !bd.DestinationChainId.IsInt64() {
		return lifiDelivery{}, fmt.Errorf("%w: selector %x: malformed BridgeData", errLiFiCalldata, sel)
	}
	return lifiDelivery{
		Receiver:           bd.Receiver,
		DestinationChainID: bd.DestinationChainId.Int64(),
		HasDestinationCall: bd.HasDestinationCall,
	}, nil
}

// lifiTransactionID returns the bytes32 transactionId LiFi stamps on every
// diamond call — the first argument of the generic-swap functions and the
// first field of BridgeData — as 0x-hex. LiFi's status API reports the same
// id, which is how a swap is tied back to the quote that produced it.
func lifiTransactionID(data []byte) (string, bool) {
	if len(data) < 4+32 {
		return "", false
	}
	var sel [4]byte
	copy(sel[:], data[:4])
	if _, ok := lifiGenericSwapSelectors[sel]; ok {
		return hexutil.Encode(data[4 : 4+32]), true
	}
	vals, err := abi.Arguments{{Type: lifiBridgeDataType}}.Unpack(data[4:])
	if err != nil {
		return "", false
	}
	bd, ok := abi.ConvertType(vals[0], new(lifiBridgeData)).(*lifiBridgeData)
	if !ok {
		return "", false
	}
	return hexutil.Encode(bd.TransactionId[:]), true
}

// checkLiFiDelivery refuses a route whose calldata would deliver anywhere but
// receiver on destChainID — or hand the funds to a destination call. Every
// refusal is ErrTreasuryInvalidRoute: the route is untrusted input that
// failed a safety check, the same class as an off-allowlist `to`.
func checkLiFiDelivery(data string, sourceChainID, destChainID int64, receiver string) error {
	if !common.IsHexAddress(receiver) {
		return fmt.Errorf("%w: order wallet %q is not an address", ErrTreasuryInvalidRoute, receiver)
	}
	raw, err := hexutil.Decode(data)
	if err != nil {
		return fmt.Errorf("%w: route data is not valid hex: %v", ErrTreasuryInvalidRoute, err)
	}
	got, err := decodeLiFiDelivery(raw, sourceChainID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTreasuryInvalidRoute, err)
	}
	if got.HasDestinationCall {
		return fmt.Errorf("%w: route hands the funds to a destination call", ErrTreasuryInvalidRoute)
	}
	if got.Receiver != common.HexToAddress(receiver) {
		return fmt.Errorf("%w: route pays %s, not the order's wallet", ErrTreasuryInvalidRoute, got.Receiver.Hex())
	}
	if got.DestinationChainID != destChainID {
		return fmt.Errorf("%w: route delivers on chain %d, not %d", ErrTreasuryInvalidRoute, got.DestinationChainID, destChainID)
	}
	return nil
}
