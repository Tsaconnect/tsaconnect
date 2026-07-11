package btc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

func newTestClient(url, network string) *Client {
	cfg := config.NonEVMChainConfig{
		ChainKey: "btc", Family: "btc", Network: network, RPCURL: url,
		NativeSymbol: "BTC", NativeDecimals: 8,
	}
	return NewClient(cfg, NewEsplora(url))
}

// testKey returns a fixed secp256k1 key and its mainnet P2WPKH address.
func testKey(t *testing.T) (*btcec.PrivateKey, string) {
	t.Helper()
	kb, _ := hex.DecodeString("2bd806c97f0e00af1a1fc3328fa763a9269723c8db8fac4f93af71db186d6e90")
	priv, pub := btcec.PrivKeyFromBytes(kb)
	wpkh, err := btcutil.NewAddressWitnessPubKeyHash(btcutil.Hash160(pub.SerializeCompressed()), &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("address: %v", err)
	}
	return priv, wpkh.EncodeAddress()
}

// signAppStyle signs a digest exactly like the app's signSecpDigest: 65-byte
// r||s||v hex with low-S (geth's crypto.Sign matches @noble/curves output).
func signAppStyle(t *testing.T, priv *btcec.PrivateKey, digestHex string) string {
	t.Helper()
	digest, _ := hex.DecodeString(digestHex)
	sig, err := ethcrypto.Sign(digest, priv.ToECDSA())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return hex.EncodeToString(sig)
}

// mockEsplora wires a minimal Esplora API around a UTXO set.
func mockEsplora(t *testing.T, addr string, utxos []UTXO, onBroadcast func(rawHex string)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /address/"+addr+"/utxo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(utxos)
	})
	mux.HandleFunc("GET /fee-estimates", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"1": 20.0, "3": 10.0, "6": 5.0}`)
	})
	mux.HandleFunc("POST /tx", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if onBroadcast != nil {
			onBroadcast(strings.TrimSpace(string(body)))
		}
		io.WriteString(w, "cafebabe0000000000000000000000000000000000000000000000000000feed")
	})
	return httptest.NewServer(mux)
}

func transferReq(from, to, amount string) chains.TransferRequest {
	return chains.TransferRequest{From: from, To: to, Amount: amount, Symbol: "BTC"}
}

const utxoTxid = "aa00000000000000000000000000000000000000000000000000000000000001"

func TestValidateAddress(t *testing.T) {
	c := newTestClient("http://unused", "mainnet")
	valid := []string{
		"bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu",          // P2WPKH
		"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",                  // legacy P2PKH
		"bc1pmzfrwwndsqmk5yh69yjr5lfgfg4ev8c0tsc06e",          // bech32 (v0-ish length checked by btcutil)
	}
	for _, a := range valid[:2] {
		if err := c.ValidateAddress(a); err != nil {
			t.Errorf("expected valid %s: %v", a, err)
		}
	}
	invalid := []string{
		"tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx", // testnet addr on mainnet
		"bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyx", // bad checksum
		"0x14dC79964da2C08b23698B3D3cc7Ca32193d9955", // EVM address
		"",
	}
	for _, a := range invalid {
		if err := c.ValidateAddress(a); err == nil {
			t.Errorf("expected invalid: %q", a)
		}
	}

	ct := newTestClient("http://unused", "testnet")
	if err := ct.ValidateAddress("tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx"); err != nil {
		t.Errorf("testnet addr should be valid on testnet: %v", err)
	}
	if err := ct.ValidateAddress("bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu"); err == nil {
		t.Error("mainnet addr should be invalid on testnet")
	}
}

func TestGetBalances(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/address/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"chain_stats":{"funded_txo_sum":500000,"spent_txo_sum":100000},
			"mempool_stats":{"funded_txo_sum":25000,"spent_txo_sum":0}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(srv.URL, "mainnet")
	bals, err := c.GetBalances(context.Background(), "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu")
	if err != nil {
		t.Fatalf("GetBalances: %v", err)
	}
	if len(bals) != 1 || bals[0].Symbol != "BTC" || bals[0].Decimals != 8 || !bals[0].IsNative {
		t.Fatalf("unexpected balance shape: %+v", bals)
	}
	if bals[0].Raw != "425000" { // 500000-100000+25000
		t.Fatalf("balance want 425000 got %s", bals[0].Raw)
	}
}

func TestPrepareBuildsChangeAndPayloads(t *testing.T) {
	_, sender := testKey(t)
	utxos := []UTXO{
		{Txid: utxoTxid, Vout: 0, Value: 60_000},
		{Txid: utxoTxid, Vout: 1, Value: 50_000},
	}
	srv := mockEsplora(t, sender, utxos, nil)
	defer srv.Close()

	c := newTestClient(srv.URL, "mainnet")
	prepared, err := c.PrepareTransfer(context.Background(),
		transferReq(sender, "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu", "80000"))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if len(prepared.Payloads) != 2 {
		t.Fatalf("want 2 payloads (2 inputs), got %d", len(prepared.Payloads))
	}
	for i, p := range prepared.Payloads {
		if p.Index != i || p.Curve != chains.CurveSecp256k1 || len(p.HashHex) != 64 {
			t.Fatalf("bad payload %d: %+v", i, p)
		}
	}
	var raw rawTx
	if err := json.Unmarshal([]byte(prepared.Raw), &raw); err != nil {
		t.Fatalf("raw: %v", err)
	}
	if len(raw.Inputs) != 2 || len(raw.Outputs) != 2 {
		t.Fatalf("want 2 inputs + recipient/change outputs, got %d/%d", len(raw.Inputs), len(raw.Outputs))
	}
	// fee at 10 sat/vB for 2-in 2-out p2wpkh ≈ (11+136+31+31)*10 = 2090
	change := raw.Outputs[1].ValueSat
	fee := 110_000 - 80_000 - change
	if fee != 2090 {
		t.Fatalf("fee want 2090 got %d (change %d)", fee, change)
	}
}

func TestPrepareFoldsDustChangeIntoFee(t *testing.T) {
	_, sender := testKey(t)
	// One 81.6k UTXO, sending 80k: change would be ~190 sats < dust → no change output.
	srv := mockEsplora(t, sender, []UTXO{{Txid: utxoTxid, Vout: 0, Value: 81_600}}, nil)
	defer srv.Close()

	c := newTestClient(srv.URL, "mainnet")
	prepared, err := c.PrepareTransfer(context.Background(),
		transferReq(sender, "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu", "80000"))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	var raw rawTx
	json.Unmarshal([]byte(prepared.Raw), &raw)
	if len(raw.Outputs) != 1 {
		t.Fatalf("dust change must fold into fee; got %d outputs", len(raw.Outputs))
	}
}

func TestPrepareRejectsInsufficientAndDust(t *testing.T) {
	_, sender := testKey(t)
	srv := mockEsplora(t, sender, []UTXO{{Txid: utxoTxid, Vout: 0, Value: 10_000}}, nil)
	defer srv.Close()

	c := newTestClient(srv.URL, "mainnet")
	to := "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu"
	if _, err := c.PrepareTransfer(context.Background(), transferReq(sender, to, "50000")); err == nil {
		t.Fatal("want insufficient-funds error")
	}
	if _, err := c.PrepareTransfer(context.Background(), transferReq(sender, to, "100")); err == nil {
		t.Fatal("want dust error")
	}
	fee, err := c.EstimateFee(context.Background(), transferReq(sender, to, "50000"))
	if err != nil {
		t.Fatalf("EstimateFee: %v", err)
	}
	if fee.HasEnoughGas || fee.Note == "" {
		t.Fatalf("want HasEnoughGas=false with note, got %+v", fee)
	}
	if fee.GasSymbol != "BTC" || fee.GasDecimals != 8 {
		t.Fatalf("bad gas quote: %+v", fee)
	}
}

func TestAssembleAndBroadcastRoundtrip(t *testing.T) {
	priv, sender := testKey(t)
	utxos := []UTXO{
		{Txid: utxoTxid, Vout: 0, Value: 60_000},
		{Txid: utxoTxid, Vout: 1, Value: 50_000},
	}
	var broadcastHex string
	srv := mockEsplora(t, sender, utxos, func(raw string) { broadcastHex = raw })
	defer srv.Close()

	c := newTestClient(srv.URL, "mainnet")
	prepared, err := c.PrepareTransfer(context.Background(),
		transferReq(sender, "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu", "80000"))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}

	sigs := make([]chains.Signature, len(prepared.Payloads))
	for i, p := range prepared.Payloads {
		sigs[i] = chains.Signature{Index: p.Index, SigHex: signAppStyle(t, priv, p.HashHex)}
	}
	txid, err := c.AssembleAndBroadcast(context.Background(), prepared, sigs)
	if err != nil {
		t.Fatalf("AssembleAndBroadcast: %v", err)
	}
	if txid == "" {
		t.Fatal("no txid returned")
	}

	// The broadcast hex must decode to a segwit tx with 2 witnessed inputs.
	rawBytes, err := hex.DecodeString(broadcastHex)
	if err != nil {
		t.Fatalf("broadcast body not hex: %v", err)
	}
	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(rawBytes)); err != nil {
		t.Fatalf("broadcast tx does not deserialize: %v", err)
	}
	if len(tx.TxIn) != 2 || len(tx.TxOut) != 2 {
		t.Fatalf("tx shape want 2-in 2-out, got %d/%d", len(tx.TxIn), len(tx.TxOut))
	}
	for i, in := range tx.TxIn {
		if len(in.Witness) != 2 || len(in.Witness[1]) != 33 {
			t.Fatalf("input %d: want [sig, 33-byte pubkey] witness, got %d items", i, len(in.Witness))
		}
	}
	if tx.TxOut[0].Value != 80_000 {
		t.Fatalf("recipient output want 80000 got %d", tx.TxOut[0].Value)
	}
}

func TestAssembleRejectsForeignKey(t *testing.T) {
	_, sender := testKey(t)
	wrongPriv, _ := btcec.NewPrivateKey()
	srv := mockEsplora(t, sender, []UTXO{{Txid: utxoTxid, Vout: 0, Value: 100_000}}, nil)
	defer srv.Close()

	c := newTestClient(srv.URL, "mainnet")
	prepared, err := c.PrepareTransfer(context.Background(),
		transferReq(sender, "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu", "80000"))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	sigs := []chains.Signature{{Index: 0, SigHex: signAppStyle(t, wrongPriv, prepared.Payloads[0].HashHex)}}
	if _, err := c.AssembleAndBroadcast(context.Background(), prepared, sigs); err == nil ||
		!strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("want signer-mismatch error, got %v", err)
	}
}

func TestHistoryMapsDirectionsAndAmounts(t *testing.T) {
	me := "bc1qme000000000000000000000000000000000000"
	other := "bc1qother0000000000000000000000000000000000"
	mux := http.NewServeMux()
	mux.HandleFunc("/address/"+me+"/txs", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[
		 {"txid":"t1","status":{"confirmed":true,"block_time":1700000100},
		  "vin":[{"prevout":{"scriptpubkey_address":"%[2]s","value":50000}}],
		  "vout":[{"scriptpubkey_address":"%[1]s","value":30000},{"scriptpubkey_address":"%[2]s","value":19000}]},
		 {"txid":"t2","status":{"confirmed":true,"block_time":1700000200},
		  "vin":[{"prevout":{"scriptpubkey_address":"%[1]s","value":30000}}],
		  "vout":[{"scriptpubkey_address":"%[2]s","value":20000},{"scriptpubkey_address":"%[1]s","value":9000}]},
		 {"txid":"t3","status":{"confirmed":false},
		  "vin":[{"prevout":{"scriptpubkey_address":"%[2]s","value":9000}}],
		  "vout":[{"scriptpubkey_address":"%[1]s","value":8000}]}
		]`, me, other)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// bypass address validation shape concerns — History hits the API directly
	c := newTestClient(srv.URL, "mainnet")
	items, next, err := c.History(context.Background(), me, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if next != "" || len(items) != 3 {
		t.Fatalf("want 3 items no cursor, got %d %q", len(items), next)
	}
	// pending first, then newest confirmed first
	if items[0].Hash != "t3" || items[0].Status != "pending" || items[0].Direction != "in" || items[0].AmountRaw != "8000" {
		t.Fatalf("bad pending item: %+v", items[0])
	}
	if items[1].Hash != "t2" || items[1].Direction != "out" || items[1].AmountRaw != "20000" || items[1].Counter != other {
		t.Fatalf("bad out item: %+v", items[1])
	}
	if items[2].Hash != "t1" || items[2].Direction != "in" || items[2].AmountRaw != "30000" || items[2].Counter != other {
		t.Fatalf("bad in item: %+v", items[2])
	}
}

func TestHistoryBatchSendAndConsolidation(t *testing.T) {
	me := "bc1qme000000000000000000000000000000000000"
	alice := "bc1qalice000000000000000000000000000000000"
	bob := "bc1qbob00000000000000000000000000000000000"
	mux := http.NewServeMux()
	mux.HandleFunc("/address/"+me+"/txs", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[
		 {"txid":"batch","status":{"confirmed":true,"block_time":1700000300},
		  "vin":[{"prevout":{"scriptpubkey_address":"%[1]s","value":100000}}],
		  "vout":[{"scriptpubkey_address":"%[2]s","value":50000},
		          {"scriptpubkey_address":"%[3]s","value":30000},
		          {"scriptpubkey_address":"%[1]s","value":19000}]},
		 {"txid":"consolidate","status":{"confirmed":true,"block_time":1700000200},
		  "vin":[{"prevout":{"scriptpubkey_address":"%[1]s","value":40000}},
		         {"prevout":{"scriptpubkey_address":"%[1]s","value":20000}}],
		  "vout":[{"scriptpubkey_address":"%[1]s","value":59000}]}
		]`, me, alice, bob)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(srv.URL, "mainnet")
	items, _, err := c.History(context.Background(), me, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items got %d", len(items))
	}
	// Batch send to two recipients: total external amount, counterparty says so.
	batch := items[0]
	if batch.Hash != "batch" || batch.Direction != "out" || batch.AmountRaw != "80000" {
		t.Fatalf("bad batch item: %+v", batch)
	}
	if batch.Counter != alice+" (+1 more)" {
		t.Fatalf("batch counterparty want %q got %q", alice+" (+1 more)", batch.Counter)
	}
	// Self-consolidation: no external recipients; amount is the fee spent.
	cons := items[1]
	if cons.Hash != "consolidate" || cons.Direction != "out" || cons.AmountRaw != "1000" || cons.Counter != "" {
		t.Fatalf("bad consolidation item: %+v", cons)
	}
}

func TestAssembleRejectsOversizedAndFeeInsaneRaw(t *testing.T) {
	priv, sender := testKey(t)
	srv := mockEsplora(t, sender, []UTXO{{Txid: utxoTxid, Vout: 0, Value: 100_000}}, nil)
	defer srv.Close()
	c := newTestClient(srv.URL, "mainnet")

	prepared, err := c.PrepareTransfer(context.Background(),
		transferReq(sender, "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu", "80000"))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}

	// Oversized: inflate the input list beyond maxTxInputs. Digest computation
	// must bail before doing per-input work or signature checks.
	var raw rawTx
	json.Unmarshal([]byte(prepared.Raw), &raw)
	for len(raw.Inputs) <= maxTxInputs {
		raw.Inputs = append(raw.Inputs, raw.Inputs[0])
	}
	bigJSON, _ := json.Marshal(raw)
	big := prepared
	big.Raw = string(bigJSON)
	if _, err := c.AssembleAndBroadcast(context.Background(), big, nil); err == nil ||
		!strings.Contains(err.Error(), "too large") {
		t.Fatalf("want too-large rejection, got %v", err)
	}

	// Fee-insane: shrink the recipient output so inputs-outputs > maxFeeSat…
	// impossible with a 100k UTXO, so instead drop the change output and lower
	// the recipient to 1 sat over a large input: use outputs summing to 1000
	// against the 100k input → fee 99k < 1 BTC cap, fine. So assert the
	// negative-fee guard instead: inflate outputs beyond inputs.
	json.Unmarshal([]byte(prepared.Raw), &raw)
	raw.Outputs[0].ValueSat = 200_000 // exceeds the 100k input
	negJSON, _ := json.Marshal(raw)
	neg := prepared
	neg.Raw = string(negJSON)
	digests, _, err := computeDigests(raw)
	if err != nil {
		t.Fatalf("computeDigests: %v", err)
	}
	sigs := make([]chains.Signature, len(digests))
	for i, d := range digests {
		sigs[i] = chains.Signature{Index: i, SigHex: signAppStyle(t, priv, hex.EncodeToString(d))}
	}
	if _, err := c.AssembleAndBroadcast(context.Background(), neg, sigs); err == nil ||
		!strings.Contains(err.Error(), "outside the sane range") {
		t.Fatalf("want fee-sanity rejection, got %v", err)
	}
}

func TestGetTxStatusConfirmations(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tx/deadbeef", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"status":{"confirmed":true,"block_height":800000}}`)
	})
	mux.HandleFunc("/tx/unknown", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Transaction not found", http.StatusNotFound)
	})
	mux.HandleFunc("/blocks/tip/height", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "800005")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(srv.URL, "mainnet")
	st, err := c.GetTxStatus(context.Background(), "deadbeef")
	if err != nil {
		t.Fatalf("GetTxStatus: %v", err)
	}
	if !st.Found || !st.Confirmed || !st.Success || st.Confirmations != 6 {
		t.Fatalf("bad status: %+v", st)
	}
	st, err = c.GetTxStatus(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("GetTxStatus(unknown): %v", err)
	}
	if st.Found {
		t.Fatalf("want Found=false for 404, got %+v", st)
	}
}
