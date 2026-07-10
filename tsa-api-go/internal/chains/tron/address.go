package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
)

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// HexToBase58 converts a 41-prefixed 21-byte hex Tron address (as returned in
// raw_data by TronGrid) into its base58check T-address for display. Returns the
// input unchanged if it doesn't look like a Tron hex address.
func HexToBase58(hexAddr string) string {
	raw, err := hex.DecodeString(strings.TrimPrefix(hexAddr, "0x"))
	if err != nil || len(raw) != 21 || raw[0] != 0x41 {
		return hexAddr
	}
	h1 := sha256.Sum256(raw)
	h2 := sha256.Sum256(h1[:])
	full := append(raw, h2[:4]...)
	return b58Encode(full)
}

func b58Encode(input []byte) string {
	zeros := 0
	for zeros < len(input) && input[zeros] == 0 {
		zeros++
	}
	x := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	mod := new(big.Int)
	var out []byte
	for x.Sign() > 0 {
		x.DivMod(x, base, mod)
		out = append(out, b58Alphabet[mod.Int64()])
	}
	for i := 0; i < zeros; i++ {
		out = append(out, b58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// IsValidBase58 checks a Tron mainnet address: base58check, 25 bytes, 0x41 prefix.
func IsValidBase58(addr string) bool {
	dec, ok := b58Decode(addr)
	if !ok || len(dec) != 25 || dec[0] != 0x41 {
		return false
	}
	body, checksum := dec[:21], dec[21:]
	h1 := sha256.Sum256(body)
	h2 := sha256.Sum256(h1[:])
	for i := 0; i < 4; i++ {
		if checksum[i] != h2[i] {
			return false
		}
	}
	return true
}

// Base58ToHex returns the 21-byte hex form (41-prefixed) TronGrid expects when visible=false.
func Base58ToHex(addr string) (string, error) {
	dec, ok := b58Decode(addr)
	if !ok || len(dec) != 25 {
		return "", errors.New("invalid tron address")
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 42)
	for i, b := range dec[:21] {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out), nil
}

func b58Decode(s string) ([]byte, bool) {
	x := big.NewInt(0)
	base := big.NewInt(58)
	for _, r := range s {
		idx := -1
		for i, c := range b58Alphabet {
			if c == r {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, false
		}
		x.Mul(x, base)
		x.Add(x, big.NewInt(int64(idx)))
	}
	dec := x.Bytes()
	// restore leading zero bytes (encoded as leading '1's)
	zeros := 0
	for _, r := range s {
		if r == '1' {
			zeros++
		} else {
			break
		}
	}
	return append(make([]byte, zeros), dec...), true
}
