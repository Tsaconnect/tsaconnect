package services

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// RecoverSigner recovers the signer address from a 65-byte signature over a
// 32-byte digest (an EIP-712 hash, or an EIP-191 personal_sign hash). Accepts
// both 0/1 and 27/28 conventions for v. High-S signatures are rejected —
// go-ethereum's SigToPub does not reject malleable sigs, so we mirror the
// on-chain ECDSA.recover behaviour here.
func RecoverSigner(digest [32]byte, sig []byte) (common.Address, error) {
	if len(sig) != 65 {
		return common.Address{}, fmt.Errorf("signature must be 65 bytes, got %d", len(sig))
	}
	// secp256k1n / 2 — sigs with s > this are rejected on chain.
	halfN := new(big.Int).Rsh(crypto.S256().Params().N, 1)
	s := new(big.Int).SetBytes(sig[32:64])
	if s.Cmp(halfN) > 0 {
		return common.Address{}, fmt.Errorf("non-canonical signature s value")
	}
	// Normalise v from 27/28 → 0/1 for crypto.SigToPub.
	v := sig[64]
	if v >= 27 {
		v -= 27
	}
	if v != 0 && v != 1 {
		return common.Address{}, fmt.Errorf("invalid v: %d", sig[64])
	}
	normalised := make([]byte, 65)
	copy(normalised, sig[:64])
	normalised[64] = v
	pub, err := crypto.SigToPub(digest[:], normalised)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// RecoverPersonalSigner recovers who signed `message` with EIP-191
// personal_sign (ethers' Wallet.signMessage). sigHex is the 0x-prefixed
// 65-byte signature.
func RecoverPersonalSigner(message, sigHex string) (common.Address, error) {
	sig, err := decodeHexSig(sigHex)
	if err != nil {
		return common.Address{}, err
	}
	var digest [32]byte
	copy(digest[:], accounts.TextHash([]byte(message)))
	return RecoverSigner(digest, sig)
}
