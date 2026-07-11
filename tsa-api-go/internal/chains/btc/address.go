package btc

import (
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

// netParams maps our network naming to btcd chain params.
func netParams(network string) *chaincfg.Params {
	if network == "testnet" {
		return &chaincfg.TestNet3Params
	}
	return &chaincfg.MainNetParams
}

// decodeAddress parses any standard BTC address (bech32/bech32m/base58) and
// rejects addresses from the wrong network (e.g. a tb1… address on mainnet).
func decodeAddress(addr string, params *chaincfg.Params) (btcutil.Address, error) {
	a, err := btcutil.DecodeAddress(addr, params)
	if err != nil {
		return nil, fmt.Errorf("invalid btc address: %v", err)
	}
	if !a.IsForNet(params) {
		return nil, fmt.Errorf("btc address is for a different network")
	}
	return a, nil
}

// pkScriptFor builds the output script paying to an address.
func pkScriptFor(addr string, params *chaincfg.Params) ([]byte, error) {
	a, err := decodeAddress(addr, params)
	if err != nil {
		return nil, err
	}
	return txscript.PayToAddrScript(a)
}

// senderWitnessAddr enforces that our own spending address is native SegWit
// (P2WPKH) — the only script the prepare/sign flow builds witnesses for. The
// app derives BIP-84 addresses, so anything else here is a caller bug.
func senderWitnessAddr(addr string, params *chaincfg.Params) (*btcutil.AddressWitnessPubKeyHash, error) {
	a, err := decodeAddress(addr, params)
	if err != nil {
		return nil, err
	}
	wpkh, ok := a.(*btcutil.AddressWitnessPubKeyHash)
	if !ok {
		return nil, fmt.Errorf("sender must be a native segwit (bc1…/tb1…) address")
	}
	return wpkh, nil
}
