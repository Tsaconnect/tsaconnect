package services

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// A signature produced by ethers v6 `Wallet.signMessage` (the app's signer),
// so the Go verifier is pinned to what the app actually sends. The key is the
// well-known ethers docs test key — not a real wallet.
func TestRecoverPersonalSigner_MatchesEthersSignMessage(t *testing.T) {
	const (
		addr = "0x2c7536E3605D9C16a7a3D7b1898e529396a65c23"
		msg  = "TSA Connect: link wallet to account\n\nWallet: 0x2c7536E3605D9C16a7a3D7b1898e529396a65c23\nNonce: abc"
		sig  = "0x6b3b0c45384cf8e232d71af39bf7bb1a74fc00e4df561e1f9e240b2f7a10538b1a0d6914408426211a1ddbb500567a7709ffd872943893a016788d8c530a191c1c"
	)
	got, err := RecoverPersonalSigner(msg, sig)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got.Hex() != addr {
		t.Fatalf("recovered %s, want %s", got.Hex(), addr)
	}
	if other, err := RecoverPersonalSigner(msg+" ", sig); err == nil && other.Hex() == addr {
		t.Fatal("a different message must not recover the same signer")
	}
	if _, err := RecoverPersonalSigner(msg, "0x1234"); err == nil {
		t.Fatal("a short signature must be rejected")
	}
}

// v may arrive as 27/28 (ethers) or 0/1 (raw secp256k1); both recover the
// same signer. A high-s twin of a valid signature is malleable and refused,
// as ECDSA.recover refuses it on chain.
func TestRecoverPersonalSigner_VFormsAndHighS(t *testing.T) {
	const (
		addr = "0x2c7536E3605D9C16a7a3D7b1898e529396a65c23"
		msg  = "TSA Connect: link wallet to account\n\nWallet: 0x2c7536E3605D9C16a7a3D7b1898e529396a65c23\nNonce: abc"
		sig  = "0x6b3b0c45384cf8e232d71af39bf7bb1a74fc00e4df561e1f9e240b2f7a10538b1a0d6914408426211a1ddbb500567a7709ffd872943893a016788d8c530a191c1c"
	)
	raw := common.FromHex(sig)

	zeroOne := append([]byte(nil), raw...)
	zeroOne[64] -= 27
	if got, err := RecoverPersonalSigner(msg, hexutil.Encode(zeroOne)); err != nil || got.Hex() != addr {
		t.Fatalf("v in {0,1}: got %s, %v", got.Hex(), err)
	}

	n := crypto.S256().Params().N
	highS := new(big.Int).Sub(n, new(big.Int).SetBytes(raw[32:64]))
	malleable := append([]byte(nil), raw...)
	copy(malleable[32:64], common.LeftPadBytes(highS.Bytes(), 32))
	malleable[64] ^= 1 // 27 <-> 28: the flipped-parity twin recovers the same key
	if _, err := RecoverPersonalSigner(msg, hexutil.Encode(malleable)); err == nil ||
		!strings.Contains(err.Error(), "non-canonical") {
		t.Fatalf("high-s signature: err=%v, want non-canonical refusal", err)
	}

	badV := append([]byte(nil), raw...)
	badV[64] = 5
	if _, err := RecoverPersonalSigner(msg, hexutil.Encode(badV)); err == nil {
		t.Fatal("v outside {0,1,27,28} must be refused")
	}
}
