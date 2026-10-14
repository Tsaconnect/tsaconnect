package btc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

const (
	// dustSat is the conservative (legacy) dust threshold; change below this
	// folds into the fee, and sends below it are rejected as non-standard.
	dustSat = 546

	// rbfSequence opts every input into replace-by-fee (BIP-125).
	rbfSequence = wire.MaxTxInSequenceNum - 2

	// vB estimates for a P2WPKH spend: 272 WU input (outpoint+seq + sig+pubkey
	// witness), 31 vB change output, ~11 vB tx overhead incl. segwit marker.
	inputVB    = 68
	changeVB   = 31
	overheadVB = 11

	// Bounds on client-supplied prepared txs at submit time. Digest computation
	// and script-engine verification run per input BEFORE signatures are
	// checked, so an unbounded Raw would let one request burn CPU/memory at
	// will. 500 P2WPKH inputs (~34k vB) is far above any tx we prepare.
	maxTxInputs  = 500
	maxTxOutputs = 100

	// maxMemoBytes is the standard relay limit for a nulldata (OP_RETURN)
	// payload. Bridge memos (THORChain-style swap instructions, which is how
	// LiFi's Bitcoin routes identify a deposit) sit well inside it; anything
	// larger would be non-standard and never relayed, so reject it up front
	// rather than build a tx no node will accept.
	maxMemoBytes = 80

	// maxFeeSat rejects a broadcast whose implied fee (inputs - outputs)
	// exceeds 0.01 BTC. The user signs their own tx so only their funds are at
	// stake, but a buggy client must not hand the balance to miners.
	maxFeeSat = 1_000_000
)

type Client struct {
	cfg config.NonEVMChainConfig
	esp *Esplora
}

func NewClient(cfg config.NonEVMChainConfig, esp *Esplora) *Client { return &Client{cfg: cfg, esp: esp} }

func (c *Client) Family() string { return "btc" }

func (c *Client) ValidateAddress(addr string) error {
	_, err := decodeAddress(addr, netParams(c.cfg.Network))
	return err
}

func (c *Client) GetBalances(ctx context.Context, addr string) ([]chains.Balance, error) {
	stats, err := c.esp.GetAddressStats(ctx, addr)
	if err != nil {
		return nil, err
	}
	return []chains.Balance{{
		Symbol: "BTC", Raw: strconv.FormatInt(stats.BalanceSat(), 10), Decimals: 8, IsNative: true,
	}}, nil
}

// rawTx is the family-specific PreparedTx.Raw payload: everything needed to
// rebuild the unsigned wire tx and its per-input BIP-143 digests at assembly.
type rawTx struct {
	Version        int32       `json:"version"`
	LockTime       uint32      `json:"lockTime"`
	Inputs         []rawInput  `json:"inputs"`
	Outputs        []rawOutput `json:"outputs"`
	SenderPkScript string      `json:"senderPkScript"` // hex; all inputs are the sender's P2WPKH
}

type rawInput struct {
	Txid     string `json:"txid"`
	Vout     uint32 `json:"vout"`
	ValueSat int64  `json:"valueSat"`
	// Sequence carries an aggregator-supplied nSequence through to the digest.
	// Zero means "unset" — the txs we build ourselves all opt into RBF, and a
	// prepared tx from before this field existed has to keep hashing the same.
	Sequence uint32 `json:"sequence,omitempty"`
}

// sequenceOr returns the input's nSequence, defaulting to RBF.
func (in rawInput) sequenceOr() uint32 {
	if in.Sequence == 0 {
		return rbfSequence
	}
	return in.Sequence
}

type rawOutput struct {
	PkScript string `json:"pkScript"` // hex
	ValueSat int64  `json:"valueSat"`
}

func (c *Client) EstimateFee(ctx context.Context, req chains.TransferRequest) (chains.FeeQuote, error) {
	quote := chains.FeeQuote{GasSymbol: "BTC", GasDecimals: 8}
	if req.TokenAddr != "" {
		return quote, fmt.Errorf("btc has no token transfers")
	}
	// An aggregator-built spend already chose its inputs and its fee; there is
	// nothing to estimate, only to validate.
	if req.PSBTHex != "" {
		spend, err := c.prepareFromPSBT(req)
		if err != nil {
			return quote, err
		}
		quote.EstFeeRaw = strconv.FormatInt(spend.feeSat, 10)
		quote.HasEnoughGas = true
		return quote, nil
	}
	amountSat, err := parseSat(req.Amount)
	if err != nil {
		return quote, err
	}
	sel, _, err := c.selectCoins(ctx, req)
	if err != nil {
		return quote, err
	}
	quote.EstFeeRaw = strconv.FormatInt(sel.feeSat, 10)
	quote.HasEnoughGas = sel.funded
	if !sel.funded {
		quote.Note = "Not enough BTC to cover the amount plus the network fee."
	} else if amountSat < dustSat {
		quote.HasEnoughGas = false
		quote.Note = "Amount is below the Bitcoin dust limit."
	}
	return quote, nil
}

func (c *Client) PrepareTransfer(ctx context.Context, req chains.TransferRequest) (chains.PreparedTx, error) {
	params := netParams(c.cfg.Network)
	if req.TokenAddr != "" {
		return chains.PreparedTx{}, fmt.Errorf("btc has no token transfers")
	}
	if req.TxBytesB64 != "" {
		return chains.PreparedTx{}, chains.Invalid("a Sui transaction cannot be signed on bitcoin")
	}
	if req.TronRawHex != "" {
		return chains.PreparedTx{}, chains.Invalid("a Tron transaction cannot be signed on bitcoin")
	}
	if req.Approve {
		return chains.PreparedTx{}, chains.Invalid("bitcoin has no token approvals")
	}
	// An aggregator-built spend is signed as given, once it has been proven to
	// be ours to sign and to match the quote (see prepareFromPSBT).
	if req.PSBTHex != "" {
		spend, err := c.prepareFromPSBT(req)
		if err != nil {
			return chains.PreparedTx{}, err
		}
		return preparedFromRaw(spend.raw)
	}
	amountSat, err := parseSat(req.Amount)
	if err != nil {
		return chains.PreparedTx{}, err
	}
	if amountSat < dustSat {
		return chains.PreparedTx{}, fmt.Errorf("amount is below the dust limit (%d sats)", dustSat)
	}
	sender, err := senderWitnessAddr(req.From, params)
	if err != nil {
		return chains.PreparedTx{}, err
	}
	recipientScript, err := pkScriptFor(req.To, params)
	if err != nil {
		return chains.PreparedTx{}, err
	}
	senderScript, err := txscript.PayToAddrScript(sender)
	if err != nil {
		return chains.PreparedTx{}, err
	}

	sel, _, err := c.selectCoins(ctx, req)
	if err != nil {
		return chains.PreparedTx{}, err
	}
	if !sel.funded {
		return chains.PreparedTx{}, fmt.Errorf("insufficient BTC: balance does not cover amount plus network fee")
	}

	memoScript, err := memoPkScript(req.MemoHex)
	if err != nil {
		return chains.PreparedTx{}, err
	}

	raw := rawTx{
		Version:        2,
		SenderPkScript: hex.EncodeToString(senderScript),
		Outputs:        []rawOutput{{PkScript: hex.EncodeToString(recipientScript), ValueSat: amountSat}},
	}
	// Memo before change: bridges scan for the nulldata output, and keeping
	// change last matches what every other wallet produces.
	if len(memoScript) > 0 {
		raw.Outputs = append(raw.Outputs, rawOutput{PkScript: hex.EncodeToString(memoScript), ValueSat: 0})
	}
	for _, u := range sel.inputs {
		raw.Inputs = append(raw.Inputs, rawInput{Txid: u.Txid, Vout: u.Vout, ValueSat: u.Value})
	}
	if sel.changeSat > 0 {
		raw.Outputs = append(raw.Outputs, rawOutput{PkScript: hex.EncodeToString(senderScript), ValueSat: sel.changeSat})
	}

	return preparedFromRaw(raw)
}

// preparedFromRaw turns an unsigned tx into the digests the app signs. Shared
// by both ways one is arrived at: coin selection here, or an aggregator's PSBT.
func preparedFromRaw(raw rawTx) (chains.PreparedTx, error) {
	digests, _, err := computeDigests(raw)
	if err != nil {
		return chains.PreparedTx{}, err
	}
	payloads := make([]chains.SigningPayload, len(digests))
	for i, d := range digests {
		payloads[i] = chains.SigningPayload{Index: i, HashHex: hex.EncodeToString(d), Curve: chains.CurveSecp256k1}
	}
	rawJSON, err := json.Marshal(raw)
	if err != nil {
		return chains.PreparedTx{}, err
	}
	return chains.PreparedTx{Family: "btc", Raw: string(rawJSON), Payloads: payloads}, nil
}

// AssembleAndBroadcast rebuilds the unsigned tx, recovers the signer pubkey
// from each 65-byte r||s||v signature (the same compact format the Tron flow
// uses), enforces that it hashes to the sender's witness program, attaches
// DER witnesses, validates every input with the script engine, and broadcasts.
func (c *Client) AssembleAndBroadcast(ctx context.Context, prepared chains.PreparedTx, sigs []chains.Signature) (string, error) {
	var raw rawTx
	if err := json.Unmarshal([]byte(prepared.Raw), &raw); err != nil {
		return "", fmt.Errorf("bad prepared tx: %v", err)
	}
	// Raw is client-supplied: bound it before any per-input work (digests +
	// script-engine runs) so one request can't burn arbitrary CPU/memory.
	if len(raw.Inputs) > maxTxInputs || len(raw.Outputs) > maxTxOutputs {
		return "", fmt.Errorf("prepared tx too large (%d inputs / %d outputs)", len(raw.Inputs), len(raw.Outputs))
	}
	if len(sigs) != len(raw.Inputs) {
		return "", fmt.Errorf("btc expects %d signatures (one per input), got %d", len(raw.Inputs), len(sigs))
	}
	sigByIndex := make(map[int]string, len(sigs))
	for _, s := range sigs {
		sigByIndex[s.Index] = s.SigHex
	}

	digests, tx, err := computeDigests(raw)
	if err != nil {
		return "", err
	}
	senderScript, err := hex.DecodeString(raw.SenderPkScript)
	if err != nil || len(senderScript) != 22 {
		return "", fmt.Errorf("bad sender script")
	}
	witnessProgram := senderScript[2:] // 0x00 0x14 <20-byte hash160>

	for i := range raw.Inputs {
		sigHex, ok := sigByIndex[i]
		if !ok {
			return "", fmt.Errorf("missing signature for input %d", i)
		}
		witness, err := buildWitness(digests[i], sigHex, witnessProgram)
		if err != nil {
			return "", fmt.Errorf("input %d: %v", i, err)
		}
		tx.TxIn[i].Witness = witness
	}

	if err := verifyTx(tx, raw, senderScript); err != nil {
		return "", fmt.Errorf("signature verification failed: %v", err)
	}

	// Fee sanity: the network only enforces outputs <= inputs, not an upper
	// bound. Catch a corrupted Raw before it donates the balance to miners.
	var inSum, outSum int64
	for _, in := range raw.Inputs {
		inSum += in.ValueSat
	}
	for _, out := range raw.Outputs {
		outSum += out.ValueSat
	}
	if fee := inSum - outSum; fee < 0 || fee > maxFeeSat {
		return "", fmt.Errorf("implied fee %d sats is outside the sane range [0, %d]", fee, maxFeeSat)
	}

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return "", err
	}
	return c.esp.Broadcast(ctx, hex.EncodeToString(buf.Bytes()))
}

func (c *Client) GetTxStatus(ctx context.Context, txHash string) (chains.TxStatus, error) {
	st, err := c.esp.GetTxStatus(ctx, txHash)
	if err != nil {
		return chains.TxStatus{}, err
	}
	out := chains.TxStatus{Found: st.Found, Confirmed: st.Confirmed, Success: st.Found}
	if st.Confirmed {
		if tip, err := c.esp.TipHeight(ctx); err == nil && tip >= st.BlockHeight {
			out.Confirmations = tip - st.BlockHeight + 1
		} else {
			out.Confirmations = 1
		}
	}
	return out, nil
}

// History maps recent address txs to wallet activity rows. Each tx nets the
// address's inputs against its outputs: net > 0 is a receive; net < 0 is a
// send whose display amount is what actually left to other addresses
// (excluding change; the fee is not shown as part of the amount).
func (c *Client) History(ctx context.Context, addr string, cursor string) ([]chains.Activity, string, error) {
	txs, err := c.esp.GetAddressTxs(ctx, addr)
	if err != nil {
		return nil, "", err
	}
	items := make([]chains.Activity, 0, len(txs))
	for _, tx := range txs {
		var inSum, outSum, toOthers int64
		counterIn, counterOut := "", ""
		recipients := 0
		for _, vin := range tx.Vin {
			if vin.Prevout.Address == addr {
				inSum += vin.Prevout.Value
			} else if counterIn == "" && vin.Prevout.Address != "" {
				counterIn = vin.Prevout.Address
			}
		}
		for _, vout := range tx.Vout {
			if vout.Address == addr {
				outSum += vout.Value
			} else {
				toOthers += vout.Value
				if vout.Address != "" {
					recipients++
					if counterOut == "" {
						counterOut = vout.Address
					}
				}
			}
		}
		net := outSum - inSum
		direction, amount, counter := "in", net, counterIn
		if net < 0 {
			direction, counter = "out", counterOut
			amount = toOthers
			if amount == 0 { // self-transfer / consolidation: show the fee spent
				amount = -net
			}
			// A batch send (e.g. from another BIP-84 wallet on the same seed)
			// pays several recipients in one tx; the amount above is the total,
			// so say so rather than misattributing it all to the first address.
			if recipients > 1 {
				counter = fmt.Sprintf("%s (+%d more)", counterOut, recipients-1)
			}
		}
		status := "pending"
		blockTime := int64(0)
		if tx.Status.Confirmed {
			status = "confirmed"
			blockTime = tx.Status.BlockTime
		}
		items = append(items, chains.Activity{
			Hash: tx.Txid, Direction: direction, AmountRaw: strconv.FormatInt(amount, 10),
			Decimals: 8, Symbol: "BTC", Counter: counter, Status: status, BlockTime: blockTime,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		// Pending (blockTime 0) first, then newest confirmed first.
		if (items[i].BlockTime == 0) != (items[j].BlockTime == 0) {
			return items[i].BlockTime == 0
		}
		return items[i].BlockTime > items[j].BlockTime
	})
	return items, "", nil
}

// --- coin selection ---

type coinSelection struct {
	inputs    []UTXO
	feeSat    int64
	changeSat int64
	funded    bool
}

// selectCoins greedily accumulates UTXOs (confirmed first, largest first)
// until they cover amount + fee, where the fee grows with each input added.
// Change below the dust limit folds into the fee instead of creating an
// unspendable output.
func (c *Client) selectCoins(ctx context.Context, req chains.TransferRequest) (coinSelection, []UTXO, error) {
	params := netParams(c.cfg.Network)
	amountSat, err := parseSat(req.Amount)
	if err != nil {
		return coinSelection{}, nil, err
	}
	recipientScript, err := pkScriptFor(req.To, params)
	if err != nil {
		return coinSelection{}, nil, err
	}
	// Validate the memo before touching the network: a request we are going to
	// reject should not cost two upstream calls first.
	memoScript, err := memoPkScript(req.MemoHex)
	if err != nil {
		return coinSelection{}, nil, err
	}
	utxos, err := c.esp.GetUTXOs(ctx, req.From)
	if err != nil {
		return coinSelection{}, nil, err
	}
	feeRate, err := c.esp.FeeRateSatVB(ctx)
	if err != nil {
		return coinSelection{}, nil, err
	}

	// The memo rides along as a second output, so its bytes must be in the fee
	// estimate — otherwise a memo send underpays and stalls in the mempool.
	fixedOutVB := outputVB(recipientScript)
	if len(memoScript) > 0 {
		fixedOutVB += outputVB(memoScript)
	}
	feeFor := func(nIn int, withChange bool) int64 {
		vb := overheadVB + inputVB*nIn + fixedOutVB
		if withChange {
			vb += changeVB
		}
		return int64(math.Ceil(float64(vb) * feeRate))
	}

	var total int64
	for i, u := range utxos {
		total += u.Value
		nIn := i + 1
		if fee := feeFor(nIn, true); total >= amountSat+fee+dustSat {
			return coinSelection{inputs: utxos[:nIn], feeSat: fee, changeSat: total - amountSat - fee, funded: true}, utxos, nil
		}
		if fee := feeFor(nIn, false); total >= amountSat+fee {
			// covers amount but change would be dust — surplus goes to the fee
			return coinSelection{inputs: utxos[:nIn], feeSat: total - amountSat, funded: true}, utxos, nil
		}
	}
	// Underfunded: report the fee a full sweep would cost so the 402 note is honest.
	n := len(utxos)
	if n == 0 {
		n = 1
	}
	return coinSelection{feeSat: feeFor(n, false)}, utxos, nil
}

// --- signing plumbing ---

// computeDigests rebuilds the unsigned wire tx from the raw payload and
// returns each input's BIP-143 SIGHASH_ALL digest.
func computeDigests(raw rawTx) ([][]byte, *wire.MsgTx, error) {
	senderScript, err := hex.DecodeString(raw.SenderPkScript)
	if err != nil {
		return nil, nil, fmt.Errorf("bad sender script: %v", err)
	}
	if len(raw.Inputs) == 0 || len(raw.Outputs) == 0 {
		return nil, nil, fmt.Errorf("prepared tx has no inputs or outputs")
	}
	tx := wire.NewMsgTx(raw.Version)
	tx.LockTime = raw.LockTime
	prevOuts := make(map[wire.OutPoint]*wire.TxOut, len(raw.Inputs))
	for _, in := range raw.Inputs {
		h, err := chainhash.NewHashFromStr(in.Txid)
		if err != nil {
			return nil, nil, fmt.Errorf("bad input txid: %v", err)
		}
		op := wire.OutPoint{Hash: *h, Index: in.Vout}
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: op, Sequence: in.sequenceOr()})
		prevOuts[op] = wire.NewTxOut(in.ValueSat, senderScript)
	}
	for _, out := range raw.Outputs {
		script, err := hex.DecodeString(out.PkScript)
		if err != nil {
			return nil, nil, fmt.Errorf("bad output script: %v", err)
		}
		tx.AddTxOut(wire.NewTxOut(out.ValueSat, script))
	}
	fetcher := txscript.NewMultiPrevOutFetcher(prevOuts)
	sigHashes := txscript.NewTxSigHashes(tx, fetcher)
	digests := make([][]byte, len(raw.Inputs))
	for i, in := range raw.Inputs {
		d, err := txscript.CalcWitnessSigHash(senderScript, sigHashes, txscript.SigHashAll, tx, i, in.ValueSat)
		if err != nil {
			return nil, nil, err
		}
		digests[i] = d
	}
	return digests, tx, nil
}

// buildWitness turns a 65-byte r||s||v compact signature over the digest into
// a P2WPKH witness stack [DER+SIGHASH_ALL, compressedPubkey]. The pubkey is
// recovered from the signature itself and must hash to the sender's witness
// program, which both authenticates the signer and supplies the witness
// pubkey without a registry lookup.
func buildWitness(digest []byte, sigHex string, witnessProgram []byte) (wire.TxWitness, error) {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != 65 {
		return nil, fmt.Errorf("expected 65-byte r||s||v signature hex")
	}
	normalizeLowS(sig)
	pub, err := ethcrypto.SigToPub(digest, sig)
	if err != nil {
		return nil, fmt.Errorf("pubkey recovery failed: %v", err)
	}
	compressed := ethcrypto.CompressPubkey(pub)
	if !bytes.Equal(btcutil.Hash160(compressed), witnessProgram) {
		return nil, fmt.Errorf("signature does not belong to the sender address")
	}

	var r, s btcec.ModNScalar
	if overflow := r.SetByteSlice(sig[:32]); overflow {
		return nil, fmt.Errorf("invalid signature r")
	}
	if overflow := s.SetByteSlice(sig[32:64]); overflow {
		return nil, fmt.Errorf("invalid signature s")
	}
	der := btcecdsa.NewSignature(&r, &s).Serialize()
	return wire.TxWitness{append(der, byte(txscript.SigHashAll)), compressed}, nil
}

// normalizeLowS rewrites a high-S compact signature to its low-S twin (and
// flips the recovery bit) in place. Bitcoin nodes reject high-S as
// non-standard (BIP-62); the app's signer is low-S already, so this is
// defense in depth for other callers.
func normalizeLowS(sig []byte) {
	var s btcec.ModNScalar
	s.SetByteSlice(sig[32:64])
	if s.IsOverHalfOrder() {
		s.Negate()
		b := s.Bytes()
		copy(sig[32:64], b[:])
		sig[64] ^= 1
	}
}

// verifyTx runs the full script engine over every input before broadcast so a
// bad signature surfaces as a clear local error, not an opaque node reject.
func verifyTx(tx *wire.MsgTx, raw rawTx, senderScript []byte) error {
	prevOuts := make(map[wire.OutPoint]*wire.TxOut, len(raw.Inputs))
	for i, in := range raw.Inputs {
		prevOuts[tx.TxIn[i].PreviousOutPoint] = wire.NewTxOut(in.ValueSat, senderScript)
	}
	fetcher := txscript.NewMultiPrevOutFetcher(prevOuts)
	sigHashes := txscript.NewTxSigHashes(tx, fetcher)
	for i, in := range raw.Inputs {
		vm, err := txscript.NewEngine(senderScript, tx, i, txscript.StandardVerifyFlags, nil, sigHashes, in.ValueSat, fetcher)
		if err != nil {
			return err
		}
		if err := vm.Execute(); err != nil {
			return fmt.Errorf("input %d: %v", i, err)
		}
	}
	return nil
}

// memoPkScript builds the zero-value OP_RETURN output that carries a bridge
// memo, returning nil when there is no memo. The memo is what a Bitcoin bridge
// reads to know which swap a deposit belongs to, so a malformed one is a hard
// error: dropping it silently would send real BTC to the bridge with nothing
// tying it to the user's route.
func memoPkScript(memoHex string) ([]byte, error) {
	memoHex = strings.TrimPrefix(strings.TrimSpace(memoHex), "0x")
	if memoHex == "" {
		return nil, nil
	}
	// A whole transaction arriving in the memo field means the caller branched
	// wrongly on the route's payload. Say so, rather than reporting it as an
	// oversized memo and sending the reader looking for the wrong bug.
	if IsPSBT(memoHex) {
		return nil, chains.Invalid("this is a transaction to sign, not a memo — send it as psbtHex")
	}
	data, err := hex.DecodeString(memoHex)
	if err != nil {
		return nil, chains.Invalid("memo is not valid hex")
	}
	if len(data) == 0 {
		return nil, nil
	}
	if len(data) > maxMemoBytes {
		return nil, chains.Invalid("memo is %d bytes; the OP_RETURN standardness limit is %d", len(data), maxMemoBytes)
	}
	script, err := txscript.NewScriptBuilder().AddOp(txscript.OP_RETURN).AddData(data).Script()
	if err != nil {
		return nil, chains.Invalid("memo script: %v", err)
	}
	return script, nil
}

// outputVB is the serialized size of one output: 8-byte value + 1-byte script
// length (every script we build is far under 253 bytes) + the script itself.
func outputVB(script []byte) int { return 9 + len(script) }

func parseSat(amount string) (int64, error) {
	v, err := strconv.ParseInt(amount, 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("bad amount")
	}
	return v, nil
}
