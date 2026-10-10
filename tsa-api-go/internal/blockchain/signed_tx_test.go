package blockchain

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestDecodeSignedTx_RecoversTheSignerOfLegacyAndTypedTxs(t *testing.T) {
	key, _ := crypto.GenerateKey()
	want := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x55d398326f99059fF775485246999027B3197955")
	for name, unsigned := range map[string]*types.Transaction{
		"legacy":      types.NewTx(&types.LegacyTx{Nonce: 3, To: &to, Value: big.NewInt(0), Gas: 60000, GasPrice: big.NewInt(1)}),
		"dynamic fee": types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(56), Nonce: 3, To: &to, Gas: 60000, GasFeeCap: big.NewInt(2), GasTipCap: big.NewInt(1)}),
	} {
		signed, err := types.SignTx(unsigned, types.LatestSignerForChainID(big.NewInt(56)), key)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := signed.MarshalBinary()
		tx, signer, err := DecodeSignedTx(hexutil.Encode(raw))
		if err != nil || signer != want || tx.Hash() != signed.Hash() || tx.Nonce() != 3 {
			t.Errorf("%s: signer %s err %v", name, signer.Hex(), err)
		}
	}
}

// The caller learns which step failed, so /wallet/submit-tx keeps its messages.
func TestDecodeSignedTx_SaysWhichStepFailed(t *testing.T) {
	unsigned, _ := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(56), Gas: 21000, GasFeeCap: big.NewInt(1), GasTipCap: big.NewInt(1)}).MarshalBinary()
	for name, tc := range map[string]struct {
		raw  string
		want error
	}{
		"not hex":       {"not-valid-hex", ErrSignedTxHex},
		"hex, not a tx": {"0xdeadbeef", ErrSignedTxEncoding},
		"never signed":  {hexutil.Encode(unsigned), ErrSignedTxSignature},
	} {
		if _, _, err := DecodeSignedTx(tc.raw); !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v, want %v", name, err, tc.want)
		}
	}
}

func TestERC20TransferData_IsTheTransferSelectorAndTwoWords(t *testing.T) {
	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	amount := big.NewInt(12_500_000)
	data, err := ERC20TransferData(to, amount)
	want := append(append(common.FromHex("0xa9059cbb"), common.LeftPadBytes(to.Bytes(), 32)...), common.LeftPadBytes(amount.Bytes(), 32)...)
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("data %x err %v", data, err)
	}
}

func TestDecodeSignedTx_RefusesAPreEIP155TxInsteadOfPanicking(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	unsigned := types.NewTx(&types.LegacyTx{Nonce: 1, GasPrice: big.NewInt(1), Gas: 21000, To: &common.Address{1}})
	signed, err := types.SignTx(unsigned, types.HomesteadSigner{}, key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := signed.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = DecodeSignedTx(hexutil.Encode(b))
	if !errors.Is(err, ErrSignedTxSignature) {
		t.Fatalf("got %v, want ErrSignedTxSignature", err)
	}
}
