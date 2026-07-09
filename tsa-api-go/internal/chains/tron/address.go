package tron

import (
	"crypto/sha256"
	"errors"
	"math/big"
)

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

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
