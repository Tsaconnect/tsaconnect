package services

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/mr-tron/base58"
)

// A LiFi route's calldata decides who receives the funds the diamond pulls
// from the treasury. The router allowlist pins WHICH contract is called, not
// where that contract then sends the money, so the calldata itself is decoded
// and its receiver checked before anything is signed. LiFi's status API says
// the same thing only after the funds have left.
//
// Three layouts cover every route LiFi returns for a treasury conversion
// (verified against live calldata in testdata/lifi_calldata):
//
//   - cross-chain: every bridge facet takes ILiFi.BridgeData as its first
//     argument, and that struct names the receiver and destination chain.
//   - same-chain: the GenericSwapFacet(V3) functions take the receiver as
//     their fourth argument and have no BridgeData at all.
//   - non-EVM destination: BridgeData.receiver holds only a placeholder and
//     the real receiver is 32 bytes inside the tool's own facet struct, at a
//     position that differs per facet (nonEVMReceiverLayouts).
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

// solanaLiFiChainID is the pseudo chain id LiFi gives Solana — far outside
// the EVM range, which is how a non-EVM destination is recognised at all.
const solanaLiFiChainID int64 = 1151111081099710

// lifiNonEVMAddress is what LiFi writes into BridgeData.receiver when the real
// recipient is not an EVM address. Seeing it means the receiver has to come
// out of the facet's own data instead; seeing anything else on a non-EVM
// destination means the calldata is not the shape this decoder was written
// against.
const lifiNonEVMAddress = "0x11f111f111f111F111f111f111F111f111f111F1"

// nonEVMReceiverLayout locates a facet's real 32-byte receiver: argIndex is
// the top-level argument holding the facet's own struct, wordOffset the word
// within that struct's encoding. Both are read off live calldata, never
// inferred — see scripts/lifi-nonevm-calldata-probe.sh.
type nonEVMReceiverLayout struct{ argIndex, wordOffset int }

// nonEVMReceiverLayouts is the allowlist of tools whose Solana calldata has
// actually been captured and decoded (testdata/lifi_calldata). A route on any
// other selector is refused rather than guessed at: reading the wrong word
// would compare the order's wallet against an unrelated 32 bytes, and the two
// facets here already disagree about where the receiver sits.
//
// Selectors are derived from the signatures in lifinance/contracts
// (src/Facets/<Facet>.sol) the same way lifiGenericSwapSelectors are, so a
// mistyped hex constant cannot quietly admit the wrong function.
//
// Only the swapAndStartBridgeTokensVia* entry points are listed: those are
// what LiFi quoted (hasSourceSwaps = true). The plain startBridgeTokensVia*
// variants take the facet struct as their second argument, but no live
// capture of one exists, so they stay refused until the probe returns one.
var nonEVMReceiverLayouts = func() map[[4]byte]nonEVMReceiverLayout {
	const bridgeData = "(bytes32,string,string,address,address,address,uint256,uint256,bool,bool)"
	const swapData = "(address,address,address,address,uint256,bytes,bool)[]"
	out := map[[4]byte]nonEVMReceiverLayout{}
	for sig, layout := range map[string]nonEVMReceiverLayout{
		// NEARIntentsData.nonEVMReceiver is the struct's first word.
		"swapAndStartBridgeTokensViaNEARIntents(" + bridgeData + "," + swapData +
			",(bytes32,address,bytes32,uint256,uint256,address,bytes))": {argIndex: 2, wordOffset: 0},
		// PolymerCCTPData.nonEVMReceiver sits after polymerTokenFee and
		// maxCCTPFee. The word after it is solanaReceiverATA — the token
		// account derived from this owner, not a second recipient.
		"swapAndStartBridgeTokensViaPolymerCCTP(" + bridgeData + "," + swapData +
			",(uint256,uint256,bytes32,bytes32,uint32,address,bytes))": {argIndex: 2, wordOffset: 2},
	} {
		var sel [4]byte
		copy(sel[:], crypto.Keccak256([]byte(sig))[:4])
		out[sel] = layout
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
	return checkLiFiDeliveryTo(data, sourceChainID, destChainID, receiver, "evm")
}

// checkLiFiDeliveryTo is checkLiFiDelivery for a receiver of any address
// family ("evm", "solana"). The family decides how the receiver is read out of
// the calldata, and a family with no decoder is refused rather than checked
// with the EVM one — on a non-EVM route that would compare the order's wallet
// against LiFi's placeholder and pass anything.
func checkLiFiDeliveryTo(data string, sourceChainID, destChainID int64, receiver, family string) error {
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
	if got.DestinationChainID != destChainID {
		return fmt.Errorf("%w: route delivers on chain %d, not %d", ErrTreasuryInvalidRoute, got.DestinationChainID, destChainID)
	}
	switch family {
	case "evm":
		if !common.IsHexAddress(receiver) {
			return fmt.Errorf("%w: order wallet %q is not an address", ErrTreasuryInvalidRoute, receiver)
		}
		if got.Receiver != common.HexToAddress(receiver) {
			return fmt.Errorf("%w: route pays %s, not the order's wallet", ErrTreasuryInvalidRoute, got.Receiver.Hex())
		}
		return nil
	case "solana":
		return checkLiFiSolanaReceiver(raw, destChainID, got.Receiver, receiver)
	default:
		return fmt.Errorf("%w: no receiver decoder for the %q address family", ErrTreasuryInvalidRoute, family)
	}
}

// checkLiFiSolanaReceiver checks the 32-byte Solana address the facet carries,
// which is the only place a Solana route names its recipient.
func checkLiFiSolanaReceiver(raw []byte, destChainID int64, bridgeReceiver common.Address, receiver string) error {
	if destChainID != solanaLiFiChainID {
		return fmt.Errorf("%w: chain %d is not Solana", ErrTreasuryInvalidRoute, destChainID)
	}
	// Without the placeholder the route is not one of the non-EVM shapes this
	// decoder was written against — a generic swap, say, whose receiver is a
	// real EVM address that no Solana wallet could ever match.
	if bridgeReceiver != common.HexToAddress(lifiNonEVMAddress) {
		return fmt.Errorf("%w: route names %s, not LiFi's non-EVM receiver", ErrTreasuryInvalidRoute, bridgeReceiver.Hex())
	}
	var sel [4]byte
	copy(sel[:], raw[:4])
	layout, ok := nonEVMReceiverLayouts[sel]
	if !ok {
		return fmt.Errorf("%w: no captured Solana receiver layout for selector %x", ErrTreasuryInvalidRoute, sel)
	}
	word, err := lifiArgumentWord(raw[4:], layout)
	if err != nil {
		return fmt.Errorf("%w: selector %x: %v", ErrTreasuryInvalidRoute, sel, err)
	}
	if got := base58.Encode(word); got != receiver {
		return fmt.Errorf("%w: route pays %s, not the order's wallet", ErrTreasuryInvalidRoute, got)
	}
	return nil
}

// lifiArgumentWord reads one 32-byte word out of a dynamic top-level argument:
// the argument's head word is an offset into args, and wordOffset counts words
// from there. Every facet struct holding a non-EVM receiver is dynamic (each
// ends in a `bytes` field), so the head is always an offset, never a value.
func lifiArgumentWord(args []byte, layout nonEVMReceiverLayout) ([]byte, error) {
	head := layout.argIndex * 32
	if len(args) < head+32 {
		return nil, fmt.Errorf("calldata ends before argument %d", layout.argIndex)
	}
	offset := new(big.Int).SetBytes(args[head : head+32])
	if !offset.IsInt64() {
		return nil, fmt.Errorf("argument %d has an unreadable offset", layout.argIndex)
	}
	start := offset.Int64() + int64(layout.wordOffset)*32
	if start < 0 || start+32 > int64(len(args)) {
		return nil, fmt.Errorf("argument %d word %d is out of bounds", layout.argIndex, layout.wordOffset)
	}
	return args[start : start+32], nil
}
