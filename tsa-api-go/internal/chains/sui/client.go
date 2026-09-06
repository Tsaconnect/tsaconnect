package sui

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/blake2b"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

const (
	suiDecimals = 9

	// nativeCoinType is the fully-qualified type of the native SUI coin. Sui also
	// reports the short form "0x2::sui::SUI"; both normalize to this.
	nativeCoinType      = "0x0000000000000000000000000000000000000000000000000000000000000002::sui::SUI"
	nativeCoinTypeShort = "0x2::sui::SUI"

	// defaultGasBudgetMist is the gas budget (a CAP, not a charge — only actual
	// gas is spent) used for both the fee preflight and the unsafe_* build. 0.01
	// SUI comfortably covers a simple transfer (~0.002-0.003 SUI actual) with
	// headroom, so we avoid a live dry-run dependency for the MVP. Dry-run-based
	// sizing is a documented follow-up.
	defaultGasBudgetMist = 10_000_000

	// maxCoinsPerSend bounds how many Coin<T> objects we include as inputs in one
	// send (largest-first, to minimize count), so a dust-flooded wallet can't
	// build an unboundedly large/expensive tx.
	maxCoinsPerSend = 50

	// coinGatherCap bounds how many coin objects we read to assess coverage. We
	// read up to this many (more than maxCoinsPerSend) so a shortfall within the
	// top maxCoinsPerSend can be distinguished from a genuine lack of funds.
	coinGatherCap = 500

	// metaConcurrency bounds parallel coin-metadata reads on a balance fetch so a
	// token-heavy wallet doesn't fan out unboundedly against the shared fullnode.
	metaConcurrency = 6

	// maxPreparedRawBytes bounds the client-supplied prepared tx we base64-decode
	// at submit. A legitimate transfer's TransactionData is well under this; the
	// signature is re-verified here and again on-chain, so an oversized blob only
	// wastes server memory.
	maxPreparedRawBytes = 128 * 1024

	// historyWindow bounds how many recent txs we pull per direction.
	historyWindow = 20

	// maxDiscoveredTokens caps how many auto-discovered (unregistered) coin types
	// we resolve metadata for on a balance read, protecting the shared RPC from a
	// spam-flooded wallet.
	maxDiscoveredTokens = 25
)

// Client is the Sui ChainClient. Tx construction happens server-side via the
// fullnode's unsafe_* RPC (returning BCS TransactionData); the app signs the
// intent-prefixed blake2b digest with ed25519 and hands back its pubkey so the
// backend can serialize the on-chain signature.
type Client struct {
	cfg      config.NonEVMChainConfig
	rpc      *RPC
	typeName map[string]string // coinType → configured symbol (reverse of cfg.Tokens)
}

func NewClient(cfg config.NonEVMChainConfig, rpc *RPC) *Client {
	typeName := make(map[string]string, len(cfg.Tokens))
	for sym, coinType := range cfg.Tokens {
		typeName[normalizeCoinType(coinType)] = sym
	}
	return &Client{cfg: cfg, rpc: rpc, typeName: typeName}
}

func (c *Client) Family() string { return "sui" }

func (c *Client) ValidateAddress(addr string) error { return ValidateAddress(addr) }

// normalizeCoinType canonicalizes the native SUI short form so registry lookups
// and native detection match regardless of which form the RPC returns.
func normalizeCoinType(t string) string {
	if t == nativeCoinTypeShort {
		return nativeCoinType
	}
	return t
}

func isNativeCoinType(t string) bool {
	return normalizeCoinType(t) == nativeCoinType
}

// symbolFor returns the configured symbol for a coin type, else the on-chain
// metadata symbol (untrusted — spoofable), else a short abbreviation of the type.
func (c *Client) symbolFor(coinType, metaSymbol string) string {
	if s, ok := c.typeName[normalizeCoinType(coinType)]; ok {
		return s
	}
	if metaSymbol != "" {
		return metaSymbol
	}
	if len(coinType) > 12 {
		return coinType[:6] + "…" + coinType[len(coinType)-4:]
	}
	return coinType
}

func (c *Client) GetBalances(ctx context.Context, addr string) ([]chains.Balance, error) {
	if err := ValidateAddress(addr); err != nil {
		return nil, err
	}
	all, err := c.rpc.GetAllBalances(ctx, addr)
	if err != nil {
		return nil, err
	}
	amountByType := make(map[string]string, len(all))
	for _, b := range all {
		amountByType[normalizeCoinType(b.CoinType)] = b.TotalBalance
	}

	// Native SUI is always shown (even at 0), like the other chains' native rows.
	out := []chains.Balance{{
		Symbol:   c.cfg.NativeSymbol,
		Raw:      orZero(amountByType[nativeCoinType]),
		Decimals: suiDecimals,
		IsNative: true,
	}}

	// Build the display set: configured tokens (always shown) + auto-discovered
	// coin types with a non-zero balance (bounded).
	seen := map[string]struct{}{nativeCoinType: {}}
	display := make([]string, 0)
	for _, coinType := range c.cfg.Tokens {
		nt := normalizeCoinType(coinType)
		if _, done := seen[nt]; done {
			continue
		}
		seen[nt] = struct{}{}
		display = append(display, nt)
	}
	discovered := make([]string, 0)
	for nt, amt := range amountByType {
		if _, done := seen[nt]; done || isZero(amt) {
			continue
		}
		discovered = append(discovered, nt)
	}
	sort.Strings(discovered) // stable ordering across refreshes
	if len(discovered) > maxDiscoveredTokens {
		discovered = discovered[:maxDiscoveredTokens]
	}
	display = append(display, discovered...)

	metas, err := c.resolveMetas(ctx, display)
	if err != nil {
		return nil, err
	}
	for _, coinType := range display {
		meta := metas[coinType]
		amt := orZero(amountByType[coinType])
		// N4: drop a HELD coin whose decimals we can't resolve — emitting
		// Decimals:0 for real holdings renders a 10^n-inflated balance/USD on a
		// financial screen. A zero-balance configured token is harmless.
		if !meta.ok && !isZero(amt) {
			continue
		}
		out = append(out, chains.Balance{
			Symbol:       c.symbolFor(coinType, meta.symbol),
			Raw:          amt,
			Decimals:     meta.decimals,
			TokenAddress: coinType,
		})
	}
	return out, nil
}

// coinMeta is a resolved coin type's display metadata. ok is false when the type
// has no on-chain metadata object (decimals then unknown).
type coinMeta struct {
	decimals int
	symbol   string
	ok       bool
}

// resolveMetas reads coin-type metadata concurrently (bounded), returning one
// entry per input type. A transient RPC error on any type fails the whole read
// (matches Solana's batch-or-fail semantics — better than showing a partial,
// possibly mis-scaled balance sheet).
func (c *Client) resolveMetas(ctx context.Context, types []string) (map[string]coinMeta, error) {
	out := make(map[string]coinMeta, len(types))
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, metaConcurrency)
	var wg sync.WaitGroup
	for _, t := range types {
		wg.Add(1)
		sem <- struct{}{}
		go func(t string) {
			defer wg.Done()
			defer func() { <-sem }()
			meta, ok, err := c.rpc.GetCoinMetadata(ctx, t)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			out[t] = coinMeta{decimals: int(meta.Decimals), symbol: meta.Symbol, ok: ok}
		}(t)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func (c *Client) EstimateFee(ctx context.Context, req chains.TransferRequest) (chains.FeeQuote, error) {
	if err := ValidateAddress(req.To); err != nil {
		return chains.FeeQuote{}, err
	}
	if err := ValidateAddress(req.From); err != nil {
		return chains.FeeQuote{}, fmt.Errorf("invalid from address: %w", err)
	}
	nativeBal, err := c.rpc.GetBalance(ctx, req.From, "")
	if err != nil {
		return chains.FeeQuote{}, err
	}
	sui, ok := new(big.Int).SetString(orZero(nativeBal.TotalBalance), 10)
	if !ok {
		return chains.FeeQuote{}, fmt.Errorf("bad native balance")
	}
	var nativeAmount uint64
	if isNativeSend(req) {
		if amt, ok := new(big.Int).SetString(req.Amount, 10); ok && amt.IsUint64() {
			nativeAmount = amt.Uint64()
		}
	}
	return buildFeeQuote(sui, isNativeSend(req), nativeAmount, req.Symbol, c.cfg.NativeSymbol), nil
}

func isNativeSend(req chains.TransferRequest) bool {
	return req.TokenAddr == "" || isNativeCoinType(req.TokenAddr)
}

// buildFeeQuote is pure so the preflight is unit-tested without a live RPC. Gas
// is always paid in SUI: a token send needs only the budget in SUI, a native
// send needs amount + budget (so send-max on a single coin fails preflight with
// a clear message rather than at broadcast).
func buildFeeQuote(suiBalance *big.Int, isNative bool, nativeAmount uint64, symbol, native string) chains.FeeQuote {
	budget := big.NewInt(defaultGasBudgetMist)
	required := new(big.Int).Set(budget)
	if isNative {
		required.Add(required, new(big.Int).SetUint64(nativeAmount))
	}
	hasEnough := suiBalance.Cmp(required) >= 0
	note := ""
	if !hasEnough {
		if isNative {
			note = "Not enough " + native + " to cover the amount plus the network fee."
		} else {
			note = "You need " + native + " in this wallet to send " + symbol + " (network fee)."
		}
	}
	return chains.FeeQuote{
		GasSymbol:    native,
		EstFeeRaw:    budget.String(), // the SUI you must hold; only actual gas is charged
		GasDecimals:  suiDecimals,
		HasEnoughGas: hasEnough,
		Note:         note,
	}
}

func (c *Client) PrepareTransfer(ctx context.Context, req chains.TransferRequest) (chains.PreparedTx, error) {
	if req.MemoHex != "" {
		return chains.PreparedTx{}, fmt.Errorf("sui transfers cannot carry a bridge memo")
	}
	if err := ValidateAddress(req.To); err != nil {
		return chains.PreparedTx{}, err
	}
	if err := ValidateAddress(req.From); err != nil {
		return chains.PreparedTx{}, fmt.Errorf("invalid from address: %w", err)
	}
	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 || !amount.IsUint64() {
		return chains.PreparedTx{}, fmt.Errorf("bad amount")
	}
	from := normalizeAddress(req.From)
	to := normalizeAddress(req.To)
	budget := strconv.FormatInt(defaultGasBudgetMist, 10)

	var txB64 string
	if isNativeSend(req) {
		// Native SUI: the input coins also pay gas, so gather enough for amount+budget.
		target := new(big.Int).Add(amount, big.NewInt(defaultGasBudgetMist))
		coinIDs, err := c.selectCoins(ctx, from, "", target)
		if err != nil {
			return chains.PreparedTx{}, err
		}
		txB64, err = c.rpc.PaySui(ctx, from, coinIDs, []string{to}, []string{amount.String()}, budget)
		if err != nil {
			return chains.PreparedTx{}, err
		}
	} else {
		// isNativeSend already routed the native coin (incl. the short 0x2::sui::SUI
		// form) to the PaySui branch, so this branch only ever sees a real token.
		coinType := normalizeCoinType(req.TokenAddr)
		coinIDs, err := c.selectCoins(ctx, from, coinType, amount)
		if err != nil {
			return chains.PreparedTx{}, err
		}
		// unsafe_pay auto-selects a SUI gas coin distinct from these inputs.
		txB64, err = c.rpc.Pay(ctx, from, coinIDs, []string{to}, []string{amount.String()}, budget)
		if err != nil {
			return chains.PreparedTx{}, err
		}
	}

	raw, err := base64.StdEncoding.DecodeString(txB64)
	if err != nil {
		return chains.PreparedTx{}, fmt.Errorf("bad tx bytes from node: %w", err)
	}
	digest := signingDigest(raw)
	return chains.PreparedTx{
		Family: "sui",
		Raw:    txB64, // base64 TransactionData (NOT hex — see AssembleAndBroadcast)
		Payloads: []chains.SigningPayload{{
			Index:   0,
			HashHex: hex.EncodeToString(digest[:]), // real 32-byte intent digest
			Curve:   chains.CurveEd25519,
		}},
	}, nil
}

// selectCoins picks Coin<T> object IDs (largest first, to minimize count) whose
// sum covers target, using at most maxCoinsPerSend inputs. It distinguishes a
// genuine shortfall ("insufficient balance") from coin fragmentation — enough
// total funds, but spread across more small coins than one tx can include —
// which needs consolidation first, so the user gets an actionable message.
func (c *Client) selectCoins(ctx context.Context, owner, coinType string, target *big.Int) ([]string, error) {
	coins, truncated, err := c.rpc.GetCoins(ctx, owner, coinType, coinGatherCap)
	if err != nil {
		return nil, err
	}
	return pickCoins(coins, truncated, target)
}

// pickCoins is the pure selection logic (largest-first, ≤ maxCoinsPerSend inputs)
// split out for unit testing. truncated means the owner holds more coins than
// `coins` — so a shortfall there is fragmentation, not a genuine lack of funds.
func pickCoins(coins []SuiCoin, truncated bool, target *big.Int) ([]string, error) {
	sort.Slice(coins, func(i, j int) bool {
		return coinBal(coins[i]).Cmp(coinBal(coins[j])) > 0
	})

	// Sum of the top maxCoinsPerSend coins we could actually include as inputs.
	pickable := big.NewInt(0)
	ids := make([]string, 0, maxCoinsPerSend)
	for _, cn := range coins {
		if len(ids) >= maxCoinsPerSend {
			break
		}
		ids = append(ids, cn.CoinObjectID)
		pickable.Add(pickable, coinBal(cn))
		if pickable.Cmp(target) >= 0 {
			return ids, nil // minimal largest-first set that covers target
		}
	}
	// Couldn't cover with the top maxCoinsPerSend. If more coins exist beyond
	// what we gathered/could include, it's fragmentation, not a real shortfall.
	if truncated || len(coins) > maxCoinsPerSend {
		return nil, fmt.Errorf("your balance is split across too many small coins — consolidate them first")
	}
	return nil, fmt.Errorf("insufficient balance")
}

func coinBal(c SuiCoin) *big.Int {
	v, ok := new(big.Int).SetString(orZero(c.Balance), 10)
	if !ok {
		return big.NewInt(0)
	}
	return v
}

// signingDigest is what Sui signs: blake2b256 of the TransactionData intent
// message ([scope=0, version=0, appId=0] || bcs).
func signingDigest(txBytes []byte) [32]byte {
	msg := make([]byte, 3+len(txBytes))
	// intent prefix is [0,0,0]; leave as zero-initialized
	copy(msg[3:], txBytes)
	return blake2b.Sum256(msg)
}

// parsePackedSig splits the app-supplied hex payload (pubkey32 || sig64, 96
// bytes) into its ed25519 public key and signature.
func parsePackedSig(sigHex string) (pub, sig []byte, err error) {
	packed, err := hex.DecodeString(strings.TrimPrefix(sigHex, "0x"))
	if err != nil {
		return nil, nil, fmt.Errorf("bad signature hex: %w", err)
	}
	if len(packed) != 96 {
		return nil, nil, fmt.Errorf("sui signature payload must be 96 bytes (pubkey32||sig64), got %d", len(packed))
	}
	return packed[:32], packed[32:], nil
}

// serializeSignature builds Sui's on-chain signature encoding:
// base64(flag=0x00 || sig64 || pubkey32).
func serializeSignature(pub, sig []byte) string {
	serialized := make([]byte, 1+64+32)
	serialized[0] = ed25519Flag
	copy(serialized[1:], sig)
	copy(serialized[65:], pub)
	return base64.StdEncoding.EncodeToString(serialized)
}

func (c *Client) AssembleAndBroadcast(ctx context.Context, prepared chains.PreparedTx, sigs []chains.Signature) (string, error) {
	if len(sigs) != 1 {
		return "", fmt.Errorf("sui expects exactly one signature, got %d", len(sigs))
	}
	// Bound the encoded length BEFORE decoding so a client can't force a large
	// allocation with an oversized blob (mirrors Solana's pre-decode cap).
	if len(prepared.Raw) > base64.StdEncoding.EncodedLen(maxPreparedRawBytes) {
		return "", fmt.Errorf("prepared tx too large")
	}
	txBytes, err := base64.StdEncoding.DecodeString(prepared.Raw)
	if err != nil {
		return "", fmt.Errorf("bad prepared tx: %w", err)
	}
	// sigHex = pubkey32 || sig64 (96 bytes) — the app has no key recovery on
	// ed25519, so it hands us its pubkey packed ahead of the signature.
	pub, sig, err := parsePackedSig(sigs[0].SigHex)
	if err != nil {
		return "", err
	}

	// Defense-in-depth (review C2): re-derive the digest from the exact tx bytes
	// we're about to broadcast and verify the supplied signature over it under the
	// supplied pubkey. This guarantees the signature, pubkey, and broadcast bytes
	// are mutually consistent — a malformed/mismatched signature fails fast here
	// instead of wasting a broadcast. It does NOT by itself prove the tx spends
	// only the sender's funds: prepared.Raw is client-supplied, so the authority
	// checks that matter are (a) the handler verifying `from` is a wallet address
	// owned by the authenticated user, and (b) the node rejecting any tx whose
	// sender ≠ the signing pubkey. Together those bind the signer to the sender.
	digest := signingDigest(txBytes)
	if !ed25519.Verify(ed25519.PublicKey(pub), digest[:], sig) {
		return "", fmt.Errorf("signature does not verify against the prepared transaction")
	}

	resp, err := c.rpc.ExecuteTransactionBlock(ctx, prepared.Raw, []string{serializeSignature(pub, sig)})
	if err != nil {
		return "", err
	}
	if resp.Digest == "" {
		return "", fmt.Errorf("execute returned no digest")
	}
	return resp.Digest, nil
}

func (c *Client) GetTxStatus(ctx context.Context, txHash string) (chains.TxStatus, error) {
	resp, err := c.rpc.GetTransactionBlock(ctx, txHash)
	if err != nil {
		// A not-yet-indexed digest surfaces as an RPC error; treat as not-found
		// so the reconciler simply retries rather than marking the tx failed.
		return chains.TxStatus{Found: false}, nil
	}
	if resp == nil {
		return chains.TxStatus{Found: false}, nil
	}
	// Once a tx is readable with certified effects it is final on Sui. Success is
	// driven independently by the effects status, so a landed-but-failed tx
	// (MoveAbort, deny-listed coin, gas exhaustion) reads Confirmed but not Success.
	success := resp.Effects != nil && resp.Effects.Status.Status == "success"
	confirmed := resp.Effects != nil
	var confs int64
	if confirmed {
		confs = 1
	}
	return chains.TxStatus{
		Found:         true,
		Confirmed:     confirmed,
		Success:       success,
		Confirmations: confs,
	}, nil
}

func (c *Client) History(ctx context.Context, addr string, cursor string) ([]chains.Activity, string, error) {
	if err := ValidateAddress(addr); err != nil {
		return nil, "", err
	}
	self := normalizeAddress(addr)
	from, err := c.rpc.QueryTransactionBlocks(ctx, "FromAddress", self, historyWindow)
	if err != nil {
		return nil, "", err
	}
	to, err := c.rpc.QueryTransactionBlocks(ctx, "ToAddress", self, historyWindow)
	if err != nil {
		return nil, "", err
	}

	byDigest := make(map[string]SuiTxResponse)
	for _, t := range append(from, to...) {
		byDigest[t.Digest] = t
	}

	metaCache := map[string]coinMeta{nativeCoinType: {decimals: suiDecimals, symbol: c.cfg.NativeSymbol, ok: true}}
	items := make([]chains.Activity, 0)
	for _, tx := range byDigest {
		status := "confirmed"
		if tx.Effects != nil && tx.Effects.Status.Status != "success" {
			status = "failed"
		}
		var blockTime int64
		if tx.TimestampMs != nil {
			if ms, err := strconv.ParseInt(*tx.TimestampMs, 10, 64); err == nil {
				blockTime = ms / 1000
			}
		}
		for _, ch := range tx.BalanceChanges {
			if normalizeAddress(ch.AddressOwner()) != self {
				continue
			}
			delta, ok := new(big.Int).SetString(ch.Amount, 10)
			if !ok || delta.Sign() == 0 {
				continue
			}
			nt := normalizeCoinType(ch.CoinType)
			meta := c.metaFor(ctx, nt, metaCache)
			if !meta.ok {
				continue // unresolved decimals → skip rather than render a wrong amount
			}
			dir := "in"
			amt := delta
			if delta.Sign() < 0 {
				dir = "out"
				amt = new(big.Int).Neg(delta)
			}
			items = append(items, chains.Activity{
				Hash:      tx.Digest,
				Direction: dir,
				AmountRaw: amt.String(),
				Decimals:  meta.decimals,
				Symbol:    c.symbolFor(nt, meta.symbol), // use the resolved metadata symbol
				Status:    status,
				BlockTime: blockTime,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].BlockTime > items[j].BlockTime })
	return items, "", nil // cursor pagination deferred (matches the other non-EVM chains)
}

// metaFor resolves a coin type's decimals + symbol, caching per call. Native is
// known; others come from on-chain metadata. An unresolved type returns ok=false.
func (c *Client) metaFor(ctx context.Context, coinType string, cache map[string]coinMeta) coinMeta {
	if m, ok := cache[coinType]; ok {
		return m
	}
	meta, ok, err := c.rpc.GetCoinMetadata(ctx, coinType)
	if err != nil || !ok {
		return coinMeta{ok: false}
	}
	m := coinMeta{decimals: int(meta.Decimals), symbol: meta.Symbol, ok: true}
	cache[coinType] = m
	return m
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func isZero(s string) bool {
	return s == "" || s == "0"
}
