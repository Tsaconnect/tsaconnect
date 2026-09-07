package btc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/ojimcy/tsa-api-go/internal/chains"
)

// Captured from a live LiFi quote on 2026-09-07 (BTC → USDC on Arbitrum,
// 200,000 sats, tool: Layerswap). It is the shape the whole Bitcoin-source path
// is built around, so the fixture is verbatim rather than synthesised:
//
//	spends  797160d6…:2   45,246,762 sats  (the sender's only UTXO)
//	pays    199,500 P2TR (the bridge deposit)
//	              0 OP_RETURN "ee7e48;=|lifi=…" (the swap's tag)
//	     45,046,440 P2WPKH back to the sender (change)
//	            500 P2WPKH (Layerswap's own output)
//	fee         322 sats
//
// Note 199,500 + 500 = the 200,000 quoted: the deposit alone is NOT the amount.
const (
	fixturePSBT  = "70736274ff0100b80200000001450e2079d46908fb35671806e18796442c0647748d5a84897b7976b6d66071790200000000fdffffff044c0b030000000000225120f9ba507bc8114718a0e16d70a22059162f24a7996ac4d1db1f26330c91c761de0000000000000000136a116565376534383b3d7c6c6966693db95cc2a85aaf0200000000160014f7bdcf26141c1feb8dea93f0c0a1f14ca61ae23ef4010000000000001600149ab105ce5f65cc9d3e51e4b2403eef1831eb5bfd000000000001011f2a69b20200000000160014f7bdcf26141c1feb8dea93f0c0a1f14ca61ae23e0000000000"
	fixtureFrom  = "bc1q777u7fs5rs07hr02j0cvpg03fjnp4c37gzhpes"
	fixtureTo    = "bc1plxa9q77gz9r33g8pd4c2ygzezchjffuedtzdrkclyceseyw8v80qasmquf"
	fixtureSpend = "200000"
)

func psbtReq(from, to, amount, psbtHex string) chains.TransferRequest {
	return chains.TransferRequest{From: from, To: to, Amount: amount, Symbol: "BTC", PSBTHex: psbtHex}
}

func TestPSBT_AcceptsALiveAggregatorRoute(t *testing.T) {
	c := newTestClient("http://unused", "mainnet")
	spend, err := c.prepareFromPSBT(psbtReq(fixtureFrom, fixtureTo, fixtureSpend, fixturePSBT))
	if err != nil {
		t.Fatalf("prepareFromPSBT: %v", err)
	}
	if spend.feeSat != 322 {
		t.Errorf("fee = %d, want 322", spend.feeSat)
	}
	if spend.spendSat != 200_000 {
		t.Errorf("spend = %d, want 200000", spend.spendSat)
	}
	if len(spend.raw.Inputs) != 1 || len(spend.raw.Outputs) != 4 {
		t.Fatalf("raw shape: %d inputs / %d outputs", len(spend.raw.Inputs), len(spend.raw.Outputs))
	}
	// The aggregator's nSequence has to survive into the digest, or we would be
	// signing a different transaction than the one it built.
	if got := spend.raw.Inputs[0].Sequence; got != 0xfffffffd {
		t.Errorf("sequence = %x, want fffffffd (RBF)", got)
	}
	// One signable digest per input.
	prepared, err := preparedFromRaw(spend.raw)
	if err != nil {
		t.Fatalf("preparedFromRaw: %v", err)
	}
	if len(prepared.Payloads) != 1 || prepared.Payloads[0].Curve != chains.CurveSecp256k1 {
		t.Fatalf("payloads: %+v", prepared.Payloads)
	}
	// EstimateFee reports the PSBT's own fee rather than pricing a new spend.
	quote, err := c.EstimateFee(context.Background(), psbtReq(fixtureFrom, fixtureTo, fixtureSpend, fixturePSBT))
	if err != nil {
		t.Fatalf("EstimateFee: %v", err)
	}
	if quote.EstFeeRaw != "322" || !quote.HasEnoughGas {
		t.Errorf("fee quote = %+v", quote)
	}
}

func TestPSBT_RefusesWhatItCannotProve(t *testing.T) {
	c := newTestClient("http://unused", "mainnet")
	_, otherWallet := testKey(t) // a different wallet's address

	cases := []struct {
		name, from, to, amount, psbtHex, wantErr string
	}{
		{
			// The inputs belong to whoever the aggregator was quoting for. Signing
			// them for a different wallet is signing someone else's coins away.
			name: "inputs are not ours", from: otherWallet, to: fixtureTo,
			amount: fixtureSpend, psbtHex: fixturePSBT, wantErr: "not this wallet's to spend",
		},
		{
			// The user approved 200,000 sats. A route that moves more must not be
			// signed just because it pays the right deposit address.
			name: "spends more than quoted", from: fixtureFrom, to: fixtureTo,
			amount: "199500", psbtHex: fixturePSBT, wantErr: "but 199500 was quoted",
		},
		{
			// Pays out the right total, but not to the address the user was shown.
			name: "deposit address not paid", from: fixtureFrom,
			to:     "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu",
			amount: fixtureSpend, psbtHex: fixturePSBT, wantErr: "does not pay the deposit address",
		},
		{
			name: "not hex", from: fixtureFrom, to: fixtureTo, amount: fixtureSpend,
			psbtHex: "zzzz", wantErr: "not valid hex",
		},
		{
			name: "not a psbt", from: fixtureFrom, to: fixtureTo, amount: fixtureSpend,
			psbtHex: "deadbeef", wantErr: "could not be read",
		},
		{
			name: "empty", from: fixtureFrom, to: fixtureTo, amount: fixtureSpend,
			psbtHex: "", wantErr: "could not be read",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.prepareFromPSBT(psbtReq(tc.from, tc.to, tc.amount, tc.psbtHex))
			if err == nil {
				t.Fatal("want an error, got none")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
			// Every one of these is the caller's to fix, so the reason must
			// survive the prepare handler instead of becoming "fee estimate failed".
			var invalid *chains.InvalidRequestError
			if !errors.As(err, &invalid) {
				t.Fatalf("want chains.InvalidRequestError, got %T", err)
			}
		})
	}
}

// buildTestPSBT constructs a PSBT spending one P2WPKH UTXO owned by `owner`,
// paying `deposit` and returning the rest as change.
func buildTestPSBT(t *testing.T, owner string, inputSat int64, outs []*wire.TxOut) string {
	t.Helper()
	ownerScript, err := pkScriptFor(owner, netParams("mainnet"))
	if err != nil {
		t.Fatalf("owner script: %v", err)
	}
	prev, _ := chainhash.NewHashFromStr(utxoTxid)
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: *prev, Index: 0},
		Sequence:         rbfSequence,
	})
	for _, o := range outs {
		tx.AddTxOut(o)
	}
	packet, err := psbt.NewFromUnsignedTx(tx)
	if err != nil {
		t.Fatalf("psbt: %v", err)
	}
	packet.Inputs[0].WitnessUtxo = wire.NewTxOut(inputSat, ownerScript)
	var buf bytes.Buffer
	if err := packet.Serialize(&buf); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return hex.EncodeToString(buf.Bytes())
}

// End to end: an aggregator PSBT, signed with the app's key format, assembled
// and broadcast through the same path a normal send uses. This is what proves
// the digests we hand out actually satisfy the script engine — the fixture
// above can be validated but never signed, since we do not hold its key.
func TestPSBT_SignsAndBroadcastsEndToEnd(t *testing.T) {
	priv, sender := testKey(t)
	var broadcast string
	srv := mockEsplora(t, sender, nil, func(rawHex string) { broadcast = rawHex })
	defer srv.Close()

	senderScript, _ := pkScriptFor(sender, netParams("mainnet"))
	depositAddr := "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu"
	depositScript, _ := pkScriptFor(depositAddr, netParams("mainnet"))
	tagScript, err := txscript.NullDataScript([]byte("=:swap:tag"))
	if err != nil {
		t.Fatalf("tag: %v", err)
	}

	const inputSat = 500_000
	psbtHex := buildTestPSBT(t, sender, inputSat, []*wire.TxOut{
		wire.NewTxOut(199_500, depositScript), // the deposit
		wire.NewTxOut(0, tagScript),           // the swap tag
		wire.NewTxOut(500, depositScript),     // an operator output
		wire.NewTxOut(299_678, senderScript),  // change (fee: 322)
	})

	c := newTestClient(srv.URL, "mainnet")
	req := psbtReq(sender, depositAddr, "200000", psbtHex)

	prepared, err := c.PrepareTransfer(context.Background(), req)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	sigs := make([]chains.Signature, 0, len(prepared.Payloads))
	for _, p := range prepared.Payloads {
		sigs = append(sigs, chains.Signature{Index: p.Index, SigHex: signAppStyle(t, priv, p.HashHex)})
	}
	if _, err := c.AssembleAndBroadcast(context.Background(), prepared, sigs); err != nil {
		t.Fatalf("AssembleAndBroadcast: %v", err)
	}

	rawBytes, err := hex.DecodeString(broadcast)
	if err != nil {
		t.Fatalf("broadcast hex: %v", err)
	}
	var signed wire.MsgTx
	if err := signed.Deserialize(bytes.NewReader(rawBytes)); err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	// The broadcast transaction must be the aggregator's, output for output —
	// dropping any of them is what strands a bridge deposit.
	if len(signed.TxOut) != 4 {
		t.Fatalf("broadcast has %d outputs, want the PSBT's 4", len(signed.TxOut))
	}
	if signed.TxOut[0].Value != 199_500 || !bytes.Equal(signed.TxOut[0].PkScript, depositScript) {
		t.Errorf("deposit output altered: %+v", signed.TxOut[0])
	}
	if !bytes.Equal(signed.TxOut[1].PkScript, tagScript) {
		t.Errorf("swap tag lost: %x", signed.TxOut[1].PkScript)
	}
	if signed.TxIn[0].Sequence != rbfSequence {
		t.Errorf("sequence changed: %x", signed.TxIn[0].Sequence)
	}
	if len(signed.TxIn[0].Witness) != 2 {
		t.Errorf("input not witnessed: %v", signed.TxIn[0].Witness)
	}
}

// A PSBT that pays itself more than it funds, or leaves an absurd fee, is
// refused before any signing.
func TestPSBT_RefusesInsaneFees(t *testing.T) {
	_, sender := testKey(t)
	senderScript, _ := pkScriptFor(sender, netParams("mainnet"))
	deposit := "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu"
	depositScript, _ := pkScriptFor(deposit, netParams("mainnet"))
	c := newTestClient("http://unused", "mainnet")

	// Outputs exceed inputs.
	overspend := buildTestPSBT(t, sender, 100_000, []*wire.TxOut{
		wire.NewTxOut(200_000, depositScript),
	})
	if _, err := c.prepareFromPSBT(psbtReq(sender, deposit, "200000", overspend)); err == nil ||
		!strings.Contains(err.Error(), "spends more than it funds") {
		t.Fatalf("want an overspend error, got %v", err)
	}

	// Funds a tiny deposit from a huge input and burns the rest as fee.
	burn := buildTestPSBT(t, sender, 100_000_000, []*wire.TxOut{
		wire.NewTxOut(200_000, depositScript),
		wire.NewTxOut(1_000, senderScript),
	})
	if _, err := c.prepareFromPSBT(psbtReq(sender, deposit, "200000", burn)); err == nil ||
		!strings.Contains(err.Error(), "above the") {
		t.Fatalf("want a fee-ceiling error, got %v", err)
	}
}

// A signed input means someone else already committed to this transaction;
// co-signing it is not a flow this wallet has.
func TestPSBT_RefusesPreSignedInputs(t *testing.T) {
	priv, sender := testKey(t)
	depositScript, _ := pkScriptFor("bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu", netParams("mainnet"))
	psbtHex := buildTestPSBT(t, sender, 500_000, []*wire.TxOut{wire.NewTxOut(200_000, depositScript)})

	packet, err := decodePSBT(psbtHex)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A well-formed signature from a real key — the point is that SOMEONE has
	// already committed to this transaction, not that the bytes are junk.
	digest := make([]byte, 32)
	sig := btcecdsa.Sign(priv, digest)
	packet.Inputs[0].PartialSigs = []*psbt.PartialSig{{
		PubKey:    priv.PubKey().SerializeCompressed(),
		Signature: append(sig.Serialize(), byte(txscript.SigHashAll)),
	}}
	var buf bytes.Buffer
	if err := packet.Serialize(&buf); err != nil {
		t.Fatalf("serialize: %v", err)
	}

	c := newTestClient("http://unused", "mainnet")
	_, err = c.prepareFromPSBT(psbtReq(sender, "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu",
		"200000", hex.EncodeToString(buf.Bytes())))
	if err == nil || !strings.Contains(err.Error(), "already signed") {
		t.Fatalf("want an already-signed error, got %v", err)
	}
}

// The prepared payload has to round-trip as JSON with the sequence intact, or
// the digests recomputed at submit time will not match the ones just signed.
func TestPSBT_RawSurvivesJSONRoundTrip(t *testing.T) {
	c := newTestClient("http://unused", "mainnet")
	spend, err := c.prepareFromPSBT(psbtReq(fixtureFrom, fixtureTo, fixtureSpend, fixturePSBT))
	if err != nil {
		t.Fatalf("prepareFromPSBT: %v", err)
	}
	prepared, err := preparedFromRaw(spend.raw)
	if err != nil {
		t.Fatalf("preparedFromRaw: %v", err)
	}
	var reloaded rawTx
	if err := json.Unmarshal([]byte(prepared.Raw), &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	again, _, err := computeDigests(reloaded)
	if err != nil {
		t.Fatalf("computeDigests: %v", err)
	}
	if hex.EncodeToString(again[0]) != prepared.Payloads[0].HashHex {
		t.Fatal("digest changed across the JSON round trip")
	}
}

// A transaction we build ourselves has no sequence recorded; it must keep
// hashing as RBF so prepared txs from before the field existed still assemble.
func TestSequenceDefaultsToRBF(t *testing.T) {
	if got := (rawInput{}).sequenceOr(); got != rbfSequence {
		t.Errorf("unset sequence = %x, want %x", got, rbfSequence)
	}
	if got := (rawInput{Sequence: 0xfffffffe}).sequenceOr(); got != 0xfffffffe {
		t.Errorf("explicit sequence not preserved: %x", got)
	}
}
