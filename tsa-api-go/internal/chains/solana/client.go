package solana

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	bin "github.com/gagliardetto/binary"
	solanago "github.com/gagliardetto/solana-go"
	ataprog "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/programs/token"
	solanarpc "github.com/gagliardetto/solana-go/rpc"
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

const (
	solDecimals = 9

	// lamportsPerSig is the fixed Solana base fee per signature (5000 lamports).
	lamportsPerSig = 5000

	// ataRentLamports is the rent-exempt minimum for a 165-byte SPL token
	// account — the cost of creating the recipient's associated token account
	// when it doesn't exist yet. The sender pays it as part of an SPL send.
	ataRentLamports = 2_039_280

	// historyWindow bounds how many recent signatures we enrich into activity.
	// Each enriched entry costs one getTransaction call, so this is deliberately
	// small on the shared free-tier gateway. Cursor pagination is deferred
	// (mirrors the Tron history impl).
	historyWindow = 12

	// historyConcurrency caps how many getTransaction calls run at once so a
	// single history view can't burst the shared free-tier RPC's qps budget.
	historyConcurrency = 4

	// historyTimeout bounds total wall-clock for enriching a history page.
	historyTimeout = 10 * time.Second

	// maxPreparedRawHex bounds the client-supplied prepared message we hex+borsh
	// decode at submit — a legitimate Solana message is well under this. The
	// signature is verified on-chain, not here, so an oversized blob would only
	// waste server CPU/memory.
	maxPreparedRawHex = 8192
)

// Client is the Solana ChainClient. Tx construction, RPC, and message
// serialization use solana-go server-side; the app signs the serialized message
// with ed25519 (see the design's library decision, §7).
type Client struct {
	cfg      config.NonEVMChainConfig
	rpc      *RPC
	mintName map[string]string // mint address → configured symbol (reverse of cfg.Tokens)
}

func NewClient(cfg config.NonEVMChainConfig, rpc *RPC) *Client {
	mintName := make(map[string]string, len(cfg.Tokens))
	for sym, mint := range cfg.Tokens {
		mintName[mint] = sym
	}
	return &Client{cfg: cfg, rpc: rpc, mintName: mintName}
}

func (c *Client) Family() string { return "solana" }

func (c *Client) ValidateAddress(addr string) error { return ValidateAddress(addr) }

// symbolFor returns the configured symbol for a mint, or a short abbreviation of
// the mint address for auto-discovered tokens the registry doesn't name.
func (c *Client) symbolFor(mint string) string {
	if s, ok := c.mintName[mint]; ok {
		return s
	}
	if len(mint) > 8 {
		return mint[:4] + "…" + mint[len(mint)-4:]
	}
	return mint
}

func (c *Client) GetBalances(ctx context.Context, addr string) ([]chains.Balance, error) {
	owner, err := solanago.PublicKeyFromBase58(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}
	lamports, err := c.rpc.SolBalance(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := []chains.Balance{{
		Symbol: c.cfg.NativeSymbol, Raw: strconv.FormatUint(lamports, 10), Decimals: solDecimals, IsNative: true,
	}}

	held, err := c.rpc.HeldTokens(ctx, owner)
	if err != nil {
		return nil, err
	}
	amountByMint := make(map[string]uint64, len(held))
	for _, h := range held {
		amountByMint[h.Mint.String()] += h.Amount
	}

	// Fetch decimals only for the mints we'll display: configured tokens plus
	// held tokens with a non-zero balance. Skipping zero-balance spam accounts
	// keeps the getMultipleAccounts batch small on spam-flooded wallets.
	mintSet := map[string]struct{}{}
	for _, mint := range c.cfg.Tokens {
		mintSet[mint] = struct{}{}
	}
	for mint, amt := range amountByMint {
		if amt > 0 {
			mintSet[mint] = struct{}{}
		}
	}
	mints := make([]solanago.PublicKey, 0, len(mintSet))
	for m := range mintSet {
		if pk, err := solanago.PublicKeyFromBase58(m); err == nil {
			mints = append(mints, pk)
		}
	}
	decs, err := c.rpc.MintDecimals(ctx, mints)
	if err != nil {
		return nil, err
	}

	// emit appends a token row, but drops a HELD token whose decimals we couldn't
	// resolve — emitting Decimals:0 for real holdings renders a ~10^n-inflated
	// balance/USD on a financial screen. A zero-balance token with unknown
	// decimals is harmless (0 scales to 0), so configured tokens still show at 0.
	emit := func(mint string, listed bool) {
		dec, ok := decs[mint]
		if !ok && amountByMint[mint] > 0 {
			return
		}
		out = append(out, chains.Balance{
			Symbol: c.symbolFor(mint), Raw: strconv.FormatUint(amountByMint[mint], 10),
			Decimals: int(dec), TokenAddress: mint, Listed: listed,
		})
	}

	// Configured tokens are always shown (even at zero), like Tron's TRC-20s.
	emitted := map[string]struct{}{}
	for _, mint := range c.cfg.Tokens {
		emit(mint, true)
		emitted[mint] = struct{}{}
	}
	// Auto-discovered tokens (not in the registry) are shown only when held.
	discovered := make([]string, 0)
	for mint, amt := range amountByMint {
		if _, done := emitted[mint]; done || amt == 0 {
			continue
		}
		discovered = append(discovered, mint)
	}
	sort.Strings(discovered) // stable ordering across refreshes
	for _, mint := range discovered {
		emit(mint, false)
	}
	return out, nil
}

func (c *Client) EstimateFee(ctx context.Context, req chains.TransferRequest) (chains.FeeQuote, error) {
	from, err := solanago.PublicKeyFromBase58(req.From)
	if err != nil {
		return chains.FeeQuote{}, fmt.Errorf("invalid from address: %w", err)
	}
	if err := ValidateAddress(req.To); err != nil {
		return chains.FeeQuote{}, err
	}
	sol, err := c.rpc.SolBalance(ctx, from)
	if err != nil {
		return chains.FeeQuote{}, err
	}

	needsATA := false
	if req.TokenAddr != "" {
		to, err := solanago.PublicKeyFromBase58(req.To)
		if err != nil {
			return chains.FeeQuote{}, fmt.Errorf("invalid to address: %w", err)
		}
		mint, err := solanago.PublicKeyFromBase58(req.TokenAddr)
		if err != nil {
			return chains.FeeQuote{}, fmt.Errorf("invalid mint: %w", err)
		}
		dstATA, _, err := solanago.FindAssociatedTokenAddress(to, mint)
		if err != nil {
			return chains.FeeQuote{}, err
		}
		exists, err := c.rpc.AccountExists(ctx, dstATA)
		if err != nil {
			return chains.FeeQuote{}, err
		}
		needsATA = !exists
	}
	// For a native SOL send the balance must also cover the amount itself, not
	// just the fee — otherwise "send max" passes preflight then fails at broadcast.
	var nativeAmount uint64
	if req.TokenAddr == "" {
		if amt, ok := new(big.Int).SetString(req.Amount, 10); ok && amt.IsUint64() {
			nativeAmount = amt.Uint64()
		}
	}
	return buildFeeQuote(sol, req.TokenAddr != "", needsATA, nativeAmount, req.Symbol, c.cfg.NativeSymbol), nil
}

// buildFeeQuote assembles the fee quote from the pre-fetched SOL balance and the
// facts that drive cost: whether this is a token send, whether the recipient's
// ATA must be created, and (for native sends) the amount that must sit alongside
// the fee. Pure so the preflight logic is unit-tested without a live RPC.
func buildFeeQuote(solBalance uint64, isToken, needsATA bool, nativeAmount uint64, symbol, native string) chains.FeeQuote {
	est := uint64(lamportsPerSig)
	if needsATA {
		est += ataRentLamports
	}
	// A token send only needs the SOL fee; a native send needs amount + fee.
	required := est
	if !isToken {
		required += nativeAmount
	}
	note := ""
	if solBalance < required {
		if isToken {
			rent := ""
			if needsATA {
				rent = " + account rent"
			}
			note = "You need " + native + " in this wallet to send " + symbol + " (network fee" + rent + ")."
		} else {
			note = "Not enough " + native + " to cover the amount plus the network fee."
		}
	}
	return chains.FeeQuote{
		GasSymbol: native, EstFeeRaw: strconv.FormatUint(est, 10), GasDecimals: solDecimals,
		NeedsActivation: needsATA, HasEnoughGas: solBalance >= required, Note: note,
	}
}

func (c *Client) PrepareTransfer(ctx context.Context, req chains.TransferRequest) (chains.PreparedTx, error) {
	if req.MemoHex != "" {
		return chains.PreparedTx{}, chains.Invalid("solana transfers cannot carry a bridge memo")
	}
	if req.PSBTHex != "" {
		return chains.PreparedTx{}, chains.Invalid("a PSBT is a Bitcoin transaction; it cannot be signed on solana")
	}
	if req.TxBytesB64 != "" {
		return chains.PreparedTx{}, chains.Invalid("a Sui transaction cannot be signed on solana")
	}
	if err := ValidateAddress(req.To); err != nil {
		return chains.PreparedTx{}, err
	}
	from, err := solanago.PublicKeyFromBase58(req.From)
	if err != nil {
		return chains.PreparedTx{}, fmt.Errorf("invalid from address: %w", err)
	}
	to, err := solanago.PublicKeyFromBase58(req.To)
	if err != nil {
		return chains.PreparedTx{}, fmt.Errorf("invalid to address: %w", err)
	}
	amountBI, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amountBI.Sign() < 0 || !amountBI.IsUint64() {
		return chains.PreparedTx{}, fmt.Errorf("bad amount")
	}
	amount := amountBI.Uint64()

	blockhash, err := c.rpc.LatestBlockhash(ctx)
	if err != nil {
		return chains.PreparedTx{}, err
	}

	var insts []solanago.Instruction
	if req.TokenAddr == "" {
		insts = []solanago.Instruction{system.NewTransferInstruction(amount, from, to).Build()}
	} else {
		mint, err := solanago.PublicKeyFromBase58(req.TokenAddr)
		if err != nil {
			return chains.PreparedTx{}, fmt.Errorf("invalid mint: %w", err)
		}
		decs, err := c.rpc.MintDecimals(ctx, []solanago.PublicKey{mint})
		if err != nil {
			return chains.PreparedTx{}, err
		}
		dec, ok := decs[mint.String()]
		if !ok {
			return chains.PreparedTx{}, fmt.Errorf("mint not found on-chain: %s", mint)
		}
		srcATA, _, err := solanago.FindAssociatedTokenAddress(from, mint)
		if err != nil {
			return chains.PreparedTx{}, err
		}
		dstATA, _, err := solanago.FindAssociatedTokenAddress(to, mint)
		if err != nil {
			return chains.PreparedTx{}, err
		}
		insts = []solanago.Instruction{
			// Idempotent: a no-op (no rent charged) when the ATA already exists,
			// so it's safe to always include and closes the check→prepare race.
			ataprog.NewCreateIdempotentInstruction(from, to, mint).Build(),
			token.NewTransferCheckedInstruction(amount, dec, srcATA, mint, dstATA, from, nil).Build(),
		}
	}

	tx, err := solanago.NewTransaction(insts, blockhash, solanago.TransactionPayer(from))
	if err != nil {
		return chains.PreparedTx{}, err
	}
	msgBytes, err := tx.Message.MarshalBinary()
	if err != nil {
		return chains.PreparedTx{}, err
	}
	msgHex := hex.EncodeToString(msgBytes)
	return chains.PreparedTx{
		Family: "solana",
		Raw:    msgHex,
		// ed25519 signs the FULL serialized message, not a 32-byte digest — the
		// HashHex field carries the whole message (opaque hex, like Tron's txID).
		Payloads: []chains.SigningPayload{{Index: 0, HashHex: msgHex, Curve: chains.CurveEd25519}},
	}, nil
}

func (c *Client) AssembleAndBroadcast(ctx context.Context, prepared chains.PreparedTx, sigs []chains.Signature) (string, error) {
	if len(sigs) != 1 {
		return "", fmt.Errorf("solana expects exactly one signature, got %d", len(sigs))
	}
	if len(prepared.Raw) > maxPreparedRawHex {
		return "", fmt.Errorf("prepared message too large")
	}
	msgBytes, err := hex.DecodeString(prepared.Raw)
	if err != nil {
		return "", fmt.Errorf("bad prepared message: %w", err)
	}
	var msg solanago.Message
	if err := msg.UnmarshalWithDecoder(bin.NewBinDecoder(msgBytes)); err != nil {
		return "", fmt.Errorf("decode message: %w", err)
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(sigs[0].SigHex, "0x"))
	if err != nil {
		return "", fmt.Errorf("bad signature hex: %w", err)
	}
	if len(sigBytes) != 64 {
		return "", fmt.Errorf("solana signature must be 64 bytes, got %d", len(sigBytes))
	}
	var sig solanago.Signature
	copy(sig[:], sigBytes)

	tx := &solanago.Transaction{Message: msg, Signatures: []solanago.Signature{sig}}
	return c.rpc.SendTx(ctx, tx)
}

func (c *Client) GetTxStatus(ctx context.Context, txHash string) (chains.TxStatus, error) {
	sig, err := solanago.SignatureFromBase58(txHash)
	if err != nil {
		return chains.TxStatus{}, fmt.Errorf("bad signature: %w", err)
	}
	st, err := c.rpc.SignatureStatus(ctx, sig)
	if err != nil {
		return chains.TxStatus{}, err
	}
	if st == nil {
		return chains.TxStatus{Found: false}, nil
	}
	// "confirmed" or "finalized" both count as confirmed for our purposes.
	confirmed := st.ConfirmationStatus == "confirmed" || st.ConfirmationStatus == "finalized"
	var confs int64
	if st.Confirmations != nil {
		confs = int64(*st.Confirmations)
	} else if confirmed {
		confs = 1 // finalized reports null confirmations
	}
	return chains.TxStatus{
		Found:         true,
		Confirmed:     confirmed,
		Success:       st.Err == nil, // a landed-but-failed tx must not read as success
		Confirmations: confs,
	}, nil
}

func (c *Client) History(ctx context.Context, addr string, cursor string) ([]chains.Activity, string, error) {
	owner, err := solanago.PublicKeyFromBase58(addr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid address: %w", err)
	}
	sigs, err := c.rpc.Signatures(ctx, owner, historyWindow)
	if err != nil {
		return nil, "", err
	}

	// Each signature costs one getTransaction. Bound both the concurrency (so a
	// single history view can't burst the shared free-tier RPC — see the spec's
	// gateway budget) and the total wall-clock with a deadline.
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	results := make([][]chains.Activity, len(sigs))
	sem := make(chan struct{}, historyConcurrency)
	var wg sync.WaitGroup
	for i := range sigs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = c.activityFor(ctx, owner, sigs[i])
		}(i)
	}
	wg.Wait()

	items := make([]chains.Activity, 0, len(sigs))
	for _, r := range results {
		items = append(items, r...)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].BlockTime > items[j].BlockTime })
	return items, "", nil // cursor pagination deferred (matches Tron)
}

// activityFor turns one signature into zero or more Activity rows by reading the
// owner's SOL and SPL balance deltas from the transaction meta. Best-effort: a
// tx we can't fetch/parse yields no rows rather than failing the whole history.
func (c *Client) activityFor(ctx context.Context, owner solanago.PublicKey, ts *solanarpc.TransactionSignature) []chains.Activity {
	res, err := c.rpc.Transaction(ctx, ts.Signature)
	if err != nil || res == nil || res.Meta == nil {
		return nil
	}
	blockTime := int64(0)
	if res.BlockTime != nil {
		blockTime = int64(*res.BlockTime)
	}
	status := "confirmed"
	if res.Meta.Err != nil {
		status = "failed"
	}
	hash := ts.Signature.String()
	var out []chains.Activity

	// SOL delta at the owner's account index.
	if tx, err := res.Transaction.GetTransaction(); err == nil {
		keys := tx.Message.AccountKeys
		for idx, k := range keys {
			if !k.Equals(owner) {
				continue
			}
			if idx < len(res.Meta.PreBalances) && idx < len(res.Meta.PostBalances) {
				delta := int64(res.Meta.PostBalances[idx]) - int64(res.Meta.PreBalances[idx])
				amount := delta
				dir := "in"
				if delta < 0 {
					dir = "out"
					// The fee payer (index 0) pays the base fee; exclude it so the
					// row shows the amount actually sent, not sent+fee.
					amount = -delta
					if idx == 0 {
						amount -= int64(res.Meta.Fee)
					}
				}
				if amount > 0 {
					out = append(out, chains.Activity{
						Hash: hash, Direction: dir, AmountRaw: strconv.FormatInt(amount, 10),
						Decimals: solDecimals, Symbol: c.cfg.NativeSymbol, Status: status, BlockTime: blockTime,
					})
				}
			}
			break
		}
	}

	// SPL deltas: compare owner's pre/post token balances per mint.
	pre := ownerTokenAmounts(owner, res.Meta.PreTokenBalances)
	post := ownerTokenAmounts(owner, res.Meta.PostTokenBalances)
	mints := map[string]uint8{}
	for m, d := range pre {
		mints[m] = d.decimals
	}
	for m, d := range post {
		mints[m] = d.decimals
	}
	for mint, dec := range mints {
		before := big.NewInt(0)
		if v, ok := pre[mint]; ok {
			before = v.amount
		}
		after := big.NewInt(0)
		if v, ok := post[mint]; ok {
			after = v.amount
		}
		delta := new(big.Int).Sub(after, before)
		if delta.Sign() == 0 {
			continue
		}
		dir := "in"
		amt := delta
		if delta.Sign() < 0 {
			dir = "out"
			amt = new(big.Int).Neg(delta)
		}
		out = append(out, chains.Activity{
			Hash: hash, Direction: dir, AmountRaw: amt.String(), Decimals: int(dec),
			Symbol: c.symbolFor(mint), Status: status, BlockTime: blockTime,
		})
	}
	return out
}

type tokenAmt struct {
	amount   *big.Int
	decimals uint8
}

// ownerTokenAmounts extracts the owner's token balances (mint → amount+decimals)
// from a meta token-balance list.
func ownerTokenAmounts(owner solanago.PublicKey, bals []solanarpc.TokenBalance) map[string]tokenAmt {
	out := map[string]tokenAmt{}
	for _, b := range bals {
		if b.Owner == nil || !b.Owner.Equals(owner) || b.UiTokenAmount == nil {
			continue
		}
		amt, ok := new(big.Int).SetString(b.UiTokenAmount.Amount, 10)
		if !ok {
			continue
		}
		out[b.Mint.String()] = tokenAmt{amount: amt, decimals: b.UiTokenAmount.Decimals}
	}
	return out
}

// ResolveMint returns the decimals for a mint and whether it is a real SPL mint,
// backing the manual token-import path.
func (c *Client) ResolveMint(ctx context.Context, mint string) (decimals uint8, ok bool, err error) {
	pk, err := solanago.PublicKeyFromBase58(mint)
	if err != nil {
		return 0, false, fmt.Errorf("invalid mint address")
	}
	return c.rpc.MintInfo(ctx, pk)
}
