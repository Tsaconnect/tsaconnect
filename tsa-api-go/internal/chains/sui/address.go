package sui

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// ed25519Flag is the Sui signature-scheme flag byte for ed25519 keys. It
// prefixes the public key when deriving an address and the signature when
// serializing it for the RPC.
const ed25519Flag = 0x00

var suiAddrRe = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

// ValidateAddress accepts the full-form Sui address only: "0x" + exactly 64 hex
// chars (32 bytes), case-insensitive. This is deliberately stricter than the
// SDK's short-form normalization (which left-pads e.g. "0x2") — for a payment
// target, rejecting a truncated paste is safer than silently padding it into a
// different, valid-looking address. The app's isValidSuiAddress uses the same
// rule so the two validators agree.
func ValidateAddress(addr string) error {
	if !suiAddrRe.MatchString(addr) {
		return fmt.Errorf("invalid sui address: %s", addr)
	}
	return nil
}

// normalizeAddress lowercases a validated address so equality checks (e.g. the
// pubkey→sender match at broadcast) are case-insensitive.
func normalizeAddress(addr string) string { return strings.ToLower(addr) }

// AddressFromPubkey derives the Sui address for a 32-byte ed25519 public key:
// "0x" + hex(blake2b256(0x00 || pubkey)). This is the address-derivation rule
// itself (the app derives the same way); the interop tests use it to assert the
// derivation against the @mysten/sui vectors. Note the signer↔sender binding at
// submit time is enforced by the handler's user-ownership check plus the node,
// not by calling this in AssembleAndBroadcast (which has no sender to compare).
func AddressFromPubkey(pub []byte) (string, error) {
	if len(pub) != 32 {
		return "", fmt.Errorf("ed25519 public key must be 32 bytes, got %d", len(pub))
	}
	flagged := make([]byte, 1+len(pub))
	flagged[0] = ed25519Flag
	copy(flagged[1:], pub)
	sum := blake2b.Sum256(flagged)
	return "0x" + hex.EncodeToString(sum[:]), nil
}
