package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// Adapters that let the relayer talk to the real chain while keeping its own
// dependencies narrow enough to fake in tests.

// EVMRelayChain adapts *blockchain.EVMClient to relayChain.
type EVMRelayChain struct{ Client *blockchain.EVMClient }

func (a EVMRelayChain) CallContract(ctx context.Context, msg ethereum.CallMsg, block *big.Int) ([]byte, error) {
	if a.Client == nil || a.Client.Client == nil {
		return nil, fmt.Errorf("relayer: no chain client")
	}
	return a.Client.Client.CallContract(ctx, msg, block)
}

func (a EVMRelayChain) BalanceAt(ctx context.Context, account common.Address, block *big.Int) (*big.Int, error) {
	if a.Client == nil || a.Client.Client == nil {
		return nil, fmt.Errorf("relayer: no chain client")
	}
	return a.Client.Client.BalanceAt(ctx, account, block)
}

func (a EVMRelayChain) CodeAt(ctx context.Context, account common.Address, block *big.Int) ([]byte, error) {
	if a.Client == nil || a.Client.Client == nil {
		return nil, fmt.Errorf("relayer: no chain client")
	}
	return a.Client.Client.CodeAt(ctx, account, block)
}

func (a EVMRelayChain) ChainID() *big.Int {
	if a.Client == nil {
		return new(big.Int)
	}
	return a.Client.ChainID()
}

// EVMRelayTxBuilder builds the unsigned tx that carries forwarder.execute.
//
// Unlike MCGPSwapService's builder this takes an explicit `to`, because the
// relayer's transaction targets the FORWARDER, not the swap contract.
type EVMRelayTxBuilder struct{ Client *blockchain.EVMClient }

// BuildUnsignedCall builds an unsigned call to an arbitrary contract. value
// and gasLimit exist for LiFi routes, whose transactionRequest we sign AS
// GIVEN: rebuilding a route from its parts drops fields the bridge looks for
// and strands the funds (the same rule the BTC PSBT path follows). A zero
// gasLimit means estimate — a cross-chain call routinely estimates low, so a
// non-zero gasLimit is LiFi's own limit and is honoured as given, not just as
// a floor.
func (b EVMRelayTxBuilder) BuildUnsignedCall(from, to string, callData []byte, value *big.Int, gasLimit uint64) ([]byte, error) {
	if b.Client == nil {
		return nil, fmt.Errorf("relayer: no chain client")
	}
	if value == nil {
		value = big.NewInt(0)
	}
	fromAddr := common.HexToAddress(from)
	toAddr := common.HexToAddress(to)

	nonce, err := b.Client.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("relayer: nonce: %w", err)
	}
	gasPrice, err := b.Client.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("relayer: gas price: %w", err)
	}
	if gasLimit == 0 {
		// The request carries its own inner gas; this bounds the OUTER transaction.
		gasLimit, err = b.Client.EstimateGas(ethereum.CallMsg{From: fromAddr, To: &toAddr, Data: callData, Value: value})
		if err != nil {
			return nil, fmt.Errorf("relayer: gas estimation failed (would revert): %w", err)
		}
	}

	tx := blockchain.UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       to,
		Value:    value.String(),
		Data:     common.Bytes2Hex(callData),
		ChainID:  b.Client.ChainID().String(),
	}
	return json.Marshal(tx)
}

// BuildUnsignedTx keeps the signature the sponsor lane and the relayer already
// use: a zero-value call with an estimated limit.
func (b EVMRelayTxBuilder) BuildUnsignedTx(from, to string, callData []byte) ([]byte, error) {
	return b.BuildUnsignedCall(from, to, callData, nil, 0)
}

// SwapRoleReader adapts MCGPSwapService to relayRoleReader for the boot-time
// check that the relayer key holds no privileged role.
type SwapRoleReader struct{ Swap *MCGPSwapService }

func (s SwapRoleReader) Owner(ctx context.Context) (string, error) {
	return s.Swap.callAddress(ctx, "owner")
}

func (s SwapRoleReader) IsOperator(ctx context.Context, addr string) (bool, error) {
	return s.Swap.callBool(ctx, "operators", common.HexToAddress(addr))
}

func (s SwapRoleReader) IsGuardian(ctx context.Context, addr string) (bool, error) {
	return s.Swap.callBool(ctx, "guardians", common.HexToAddress(addr))
}
