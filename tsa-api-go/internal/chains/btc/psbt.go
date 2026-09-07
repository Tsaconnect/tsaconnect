package btc

// Signing an aggregator-built Bitcoin spend.
//
// A LiFi UTXO route is not "pay this address": the aggregator picks the user's
// UTXOs and hands back a complete PSBT whose output set is what makes the
// deposit creditable — the bridge deposit, a nulldata tag identifying the swap,
// the operator's own output, and change. Rebuilding that from a destination and
// an amount would drop outputs the bridge is looking for and strand the funds,
// so the transaction is signed as given.
//
// "As given" is not "as trusted". The PSBT arrives through the app from a third
// party, and signing it spends the user's coins, so everything below has to
// hold before a single signing digest is handed out:
//
//   - every input is one of OUR P2WPKH outputs, with the value the signature
//     commits to supplied (a lie about that value is how a segwit signature is
//     tricked into authorising a larger spend than it appears to)
//   - nothing is pre-signed or pre-finalized
//   - the deposit address the user was quoted is actually paid
//   - everything leaving the wallet totals exactly the quoted amount, so no
//     extra output can siphon off the balance
//   - the fee left over is inside the same sane range a normal send uses
//
// What survives all that is converted into the ordinary rawTx the coin-selection
// path produces, so digests, witnesses, script-engine verification and broadcast
// are the same code in both cases.

import (
	"bytes"
	"encoding/hex"
	"strings"

	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/ojimcy/tsa-api-go/internal/chains"
)

// psbtSpend is a validated aggregator PSBT, ready to sign.
type psbtSpend struct {
	raw    rawTx
	feeSat int64
	// spendSat is everything leaving the wallet (deposit + tag + any operator
	// output), i.e. outputs that are not change.
	spendSat int64
}

// prepareFromPSBT decodes and validates req.PSBTHex against what the user was
// quoted: req.To must be paid, and req.Amount must be the total leaving the
// wallet.
func (c *Client) prepareFromPSBT(req chains.TransferRequest) (*psbtSpend, error) {
	params := netParams(c.cfg.Network)

	sender, err := senderWitnessAddr(req.From, params)
	if err != nil {
		return nil, err
	}
	senderScript, err := txscript.PayToAddrScript(sender)
	if err != nil {
		return nil, err
	}
	quotedSat, err := parseSat(req.Amount)
	if err != nil {
		return nil, chains.Invalid("bad amount")
	}
	depositScript, err := pkScriptFor(req.To, params)
	if err != nil {
		return nil, chains.Invalid("%v", err)
	}

	packet, err := decodePSBT(req.PSBTHex)
	if err != nil {
		return nil, err
	}
	tx := packet.UnsignedTx
	if tx == nil || len(tx.TxIn) == 0 || len(tx.TxOut) == 0 {
		return nil, chains.Invalid("the route's transaction has no inputs or outputs")
	}
	if len(tx.TxIn) > maxTxInputs || len(tx.TxOut) > maxTxOutputs {
		return nil, chains.Invalid("the route's transaction is too large (%d inputs / %d outputs)",
			len(tx.TxIn), len(tx.TxOut))
	}
	if len(packet.Inputs) != len(tx.TxIn) {
		return nil, chains.Invalid("the route's transaction is malformed")
	}

	raw := rawTx{
		Version:        tx.Version,
		LockTime:       tx.LockTime,
		SenderPkScript: hex.EncodeToString(senderScript),
	}

	// Inputs: ours, unsigned, and each with the value its signature commits to.
	var inSum int64
	for i, in := range packet.Inputs {
		if len(in.PartialSigs) > 0 || len(in.FinalScriptWitness) > 0 || len(in.FinalScriptSig) > 0 {
			return nil, chains.Invalid("input %d is already signed", i)
		}
		if in.WitnessUtxo == nil {
			return nil, chains.Invalid("input %d does not say what it spends", i)
		}
		if !bytes.Equal(in.WitnessUtxo.PkScript, senderScript) {
			return nil, chains.Invalid("input %d is not this wallet's to spend", i)
		}
		if in.WitnessUtxo.Value <= 0 {
			return nil, chains.Invalid("input %d has no value", i)
		}
		inSum += in.WitnessUtxo.Value
		op := tx.TxIn[i].PreviousOutPoint
		raw.Inputs = append(raw.Inputs, rawInput{
			Txid:     op.Hash.String(),
			Vout:     op.Index,
			ValueSat: in.WitnessUtxo.Value,
			Sequence: tx.TxIn[i].Sequence,
		})
	}

	// Outputs: separate change (back to us) from everything leaving the wallet,
	// and confirm the quoted deposit is among the latter.
	var outSum, spendSat, depositSat int64
	for _, out := range tx.TxOut {
		if out.Value < 0 {
			return nil, chains.Invalid("the route's transaction has a negative output")
		}
		outSum += out.Value
		if !bytes.Equal(out.PkScript, senderScript) {
			spendSat += out.Value
		}
		if bytes.Equal(out.PkScript, depositScript) {
			depositSat += out.Value
		}
		raw.Outputs = append(raw.Outputs, rawOutput{
			PkScript: hex.EncodeToString(out.PkScript),
			ValueSat: out.Value,
		})
	}
	if depositSat <= 0 {
		return nil, chains.Invalid("the route does not pay the deposit address it quoted (%s)", req.To)
	}
	// Everything leaving the wallet must be exactly what the user approved. The
	// deposit itself is usually less than that — the tag output carries 0 and
	// the bridge takes its own cut in a separate output — so the total is what
	// can be checked, and it is also what stops an extra output from quietly
	// draining the rest of the balance.
	if spendSat != quotedSat {
		return nil, chains.Invalid("the route spends %d sats but %d was quoted", spendSat, quotedSat)
	}

	feeSat := inSum - outSum
	if feeSat < 0 {
		return nil, chains.Invalid("the route's transaction spends more than it funds")
	}
	if feeSat > maxFeeSat {
		return nil, chains.Invalid("the route's network fee (%d sats) is above the %d sat ceiling", feeSat, maxFeeSat)
	}

	return &psbtSpend{raw: raw, feeSat: feeSat, spendSat: spendSat}, nil
}

// decodePSBT parses the hex payload. Anything unreadable is a caller-fixable
// error: we would rather refuse a route than sign a guess at what it meant.
func decodePSBT(psbtHex string) (*psbt.Packet, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(psbtHex), "0x")
	raw, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, chains.Invalid("the route's transaction is not valid hex")
	}
	packet, err := psbt.NewFromRawBytes(bytes.NewReader(raw), false)
	if err != nil {
		return nil, chains.Invalid("the route's transaction could not be read: %v", err)
	}
	return packet, nil
}

// IsPSBT reports whether a payload carries the BIP-174 magic. Aggregators vary
// by tool: some hand back a whole PSBT to sign, others just a memo to attach to
// a deposit we build ourselves, and both arrive in the same field.
func IsPSBT(payloadHex string) bool {
	trimmed := strings.TrimPrefix(strings.TrimSpace(payloadHex), "0x")
	if len(trimmed) < 10 {
		return false
	}
	prefix, err := hex.DecodeString(trimmed[:10])
	if err != nil {
		return false
	}
	return bytes.Equal(prefix, []byte{0x70, 0x73, 0x62, 0x74, 0xff}) // "psbt\xff"
}
