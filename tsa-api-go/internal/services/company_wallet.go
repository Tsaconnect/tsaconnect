package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"golang.org/x/sync/singleflight"
)

var (
	// ErrReceiptsUnreadable: the RPC answers block reads but not receipts —
	// the 2026-10-01 publicnode failure. The company wallet proves every
	// delivery from a receipt, so a lane without them is never built.
	ErrReceiptsUnreadable = errors.New("company wallet: this RPC does not serve transaction receipts")
	// ErrTreasuryStockUnreadable: a balance read failed. Reads as "closed" on
	// public paths and as nothing-sent at open.
	ErrTreasuryStockUnreadable = errors.New("company wallet: stock could not be read")
)

// RawRPCCaller is a JSON-RPC client; *rpc.Client (ethclient.Client.Client())
// satisfies it.
type RawRPCCaller interface {
	CallContext(ctx context.Context, result any, method string, args ...any) error
}

// ProbeReceipts proves the RPC serves transaction receipts (spec 2.3 #8): it
// takes the last transaction of the newest block that has one (looking back at
// most 5 blocks) and asks for its receipt. Raw JSON, hashes only, so an
// OP-stack deposit transaction can never fail decoding (Decision D53).
func ProbeReceipts(ctx context.Context, rpc RawRPCCaller) error {
	tag := "latest"
	for i := 0; i < 5; i++ {
		var blk struct {
			Number       string   `json:"number"`
			Transactions []string `json:"transactions"`
		}
		if err := rpc.CallContext(ctx, &blk, "eth_getBlockByNumber", tag, false); err != nil {
			return fmt.Errorf("%w: read block %s: %v", ErrReceiptsUnreadable, tag, err)
		}
		if n := len(blk.Transactions); n > 0 {
			hash := blk.Transactions[n-1]
			var receipt json.RawMessage
			if err := rpc.CallContext(ctx, &receipt, "eth_getTransactionReceipt", hash); err != nil {
				return fmt.Errorf("%w: receipt of %s: %v", ErrReceiptsUnreadable, hash, err)
			}
			if len(receipt) == 0 || string(receipt) == "null" {
				return fmt.Errorf("%w: no receipt for mined transaction %s", ErrReceiptsUnreadable, hash)
			}
			return nil
		}
		num, err := hexutil.DecodeUint64(blk.Number)
		if err != nil || num == 0 {
			return fmt.Errorf("%w: block %s has no transactions and no parent to try", ErrReceiptsUnreadable, tag)
		}
		tag = hexutil.EncodeUint64(num - 1)
	}
	return fmt.Errorf("%w: no transaction in the last 5 blocks", ErrReceiptsUnreadable)
}

// TreasuryTxProbe is what the dropped-transaction check asks a chain (spec
// §5.4). *ethclient.Client satisfies it.
type TreasuryTxProbe interface {
	TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, bool, error)
	PendingNonceAt(ctx context.Context, account common.Address) (uint64, error)
	NonceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (uint64, error)
	// Task 9: the head (scan bounds, confirmation depth), a sale's receipt
	// block, and the chunked Transfer scan behind an operator re-send.
	BlockNumber(ctx context.Context) (uint64, error)
	TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error)
	// HeaderByNumber: a receipt block's time (the conversion attach-hash
	// check that a transfer was mined after the delivery was sent).
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
	// EndpointGeneration counts the client's RPC endpoint changes
	// (blockchain.ProbeClient). A decision built from several reads takes it
	// before and after (endpointSteady) and decides nothing if it moved.
	EndpointGeneration() uint64
}

// errRPCEndpointChanged: the client failed over to another RPC endpoint while
// a decision was reading the chain, so its answers may come from nodes at
// different heights — a lagging one returns "no logs" or "not found" for
// blocks it has not seen. Nothing is decided; the next tick reads again.
var errRPCEndpointChanged = errors.New("the RPC endpoint changed while the chain was read, so the answers may come from different nodes — not decided; the next tick reads again")

// endpointSteady is nil while probe is still on the endpoint it was on at gen.
func endpointSteady(probe TreasuryTxProbe, gen uint64) error {
	if probe.EndpointGeneration() != gen {
		return errRPCEndpointChanged
	}
	return nil
}

// CompanyWalletCaps are the company wallet lane's value caps: none. Its
// maximum (RAMP_TREASURY_MAX_USD) is enforced where orders are priced — quote,
// open and live pricing — so an operator's audited "deliver anyway" above it
// is not refused at send time forever (review C2). Free stock is the real
// per-token limit; the kill switch still stops every send.
func CompanyWalletCaps() TreasuryCaps { return TreasuryCaps{} }

// CompanyWalletTokenDecimals reads each stablecoin's decimals on a lane once,
// at boot. A token whose decimals cannot be read is dropped (spec §4.1).
func CompanyWalletTokenDecimals(lane *TreasuryLane) (map[string]int, []string) {
	decimals := map[string]int{}
	var dropped []string
	for symbol, addr := range lane.Tokens {
		if !IsStableAsset(symbol) {
			continue
		}
		d, err := lane.Native.GetTokenDecimals(addr)
		if err != nil || d <= 0 || d > 36 {
			dropped = append(dropped, strings.ToUpper(symbol))
			continue
		}
		decimals[strings.ToUpper(symbol)] = d
	}
	sort.Strings(dropped)
	return decimals, dropped
}

// CompanyWalletConfig is everything the company wallet is built from at boot.
type CompanyWalletConfig struct {
	Treasury  *Treasury
	Decimals  map[string]map[string]int // chain -> TOKEN -> decimals
	Verifiers map[string]ERC20SourcedTransferVerifier
	Probes    map[string]TreasuryTxProbe
	// ReceiptRPCs are re-probed by ReprobeReceipts; ReceiptsOK is the boot
	// probe's verdict per chain (nil: every chain readable).
	ReceiptRPCs map[string]RawRPCCaller
	ReceiptsOK  map[string]bool
	// ConversionTreasury is derived from RAMP_TREASURY_PRIVATE_KEY at boot; a
	// sale to it needs a conversion record (spec §15.5 N1).
	ConversionTreasury string
}

const (
	// companyWalletFreeTTL bounds the public quote path's free-stock reads
	// (balance cache + one reservation SUM) to one per 5s per token.
	companyWalletFreeTTL = 5 * time.Second

	companyWalletStockTTL     = 30 * time.Second
	companyWalletStockFailTTL = 5 * time.Second
	companyWalletReadTimeout  = 3 * time.Second
)

type stockReading struct {
	units *big.Int
	err   error
	at    time.Time
}

// CompanyWallet is the company's own stablecoin stock (Decision D45): a
// Treasury with its own key, the tokens it offers, a cached per-token balance,
// and what delivery needs per chain.
type CompanyWallet struct {
	treasury  *Treasury
	decimals  map[string]map[string]int
	verifiers map[string]ERC20SourcedTransferVerifier
	probes    map[string]TreasuryTxProbe
	now       func() time.Time

	receiptRPCs        map[string]RawRPCCaller
	conversionTreasury string

	mu         sync.Mutex
	stock      map[string]stockReading
	free       map[string]stockReading // the 5s free-stock cache (TreasuryFreeUnits)
	receiptsOK map[string]bool
	flight     singleflight.Group
}

// NewCompanyWallet returns nil unless some lane offers some token.
func NewCompanyWallet(c CompanyWalletConfig) *CompanyWallet {
	if c.Treasury == nil || !c.Treasury.Enabled() {
		return nil
	}
	decimals := map[string]map[string]int{}
	for chain, toks := range c.Decimals {
		chain = strings.ToLower(strings.TrimSpace(chain))
		lane := c.Treasury.Lane(chain)
		if lane == nil || lane.SponsorLane == nil {
			continue
		}
		for tok, d := range toks {
			tok = strings.ToUpper(strings.TrimSpace(tok))
			if lane.tokenAddress(tok) == "" || d <= 0 {
				continue
			}
			if decimals[chain] == nil {
				decimals[chain] = map[string]int{}
			}
			decimals[chain][tok] = d
		}
	}
	if len(decimals) == 0 {
		return nil
	}
	return &CompanyWallet{
		treasury: c.Treasury, decimals: decimals, verifiers: lowerChainKeys(c.Verifiers), probes: lowerChainKeys(c.Probes),
		now: time.Now, stock: map[string]stockReading{}, free: map[string]stockReading{},
		receiptRPCs: lowerChainKeys(c.ReceiptRPCs), receiptsOK: receiptsVerdict(decimals, lowerChainKeys(c.ReceiptsOK)),
		conversionTreasury: strings.TrimSpace(c.ConversionTreasury),
	}
}

func (w *CompanyWallet) Address() string     { return w.treasury.Address() }
func (w *CompanyWallet) Treasury() *Treasury { return w.treasury }

// IsConversionTreasury: addr is the conversion treasury — a sale there is a
// converted buy's settlement leg and needs its record (N1).
func (w *CompanyWallet) IsConversionTreasury(addr string) bool {
	return w != nil && w.conversionTreasury != "" && strings.EqualFold(strings.TrimSpace(addr), w.conversionTreasury)
}

// Chains are the chains with at least one offered token, sorted.
func (w *CompanyWallet) Chains() []string {
	out := make([]string, 0, len(w.decimals))
	for c := range w.decimals {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Tokens are the tokens offered on chain, sorted.
func (w *CompanyWallet) Tokens(chain string) []string {
	m := w.decimals[strings.ToLower(strings.TrimSpace(chain))]
	out := make([]string, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func (w *CompanyWallet) Offers(chain, token string) bool {
	_, ok := w.decimals[strings.ToLower(strings.TrimSpace(chain))][strings.ToUpper(strings.TrimSpace(token))]
	return ok
}

func (w *CompanyWallet) Lane(chain string) *TreasuryLane { return w.treasury.Lane(chain) }

// lowerChainKeys normalises per-chain config keys the way Decimals is, so a
// "Base" key cannot silently miss a lookup by "base".
func lowerChainKeys[V any](in map[string]V) map[string]V {
	if in == nil {
		return nil
	}
	out := make(map[string]V, len(in))
	for k, v := range in {
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out
}

// receiptsVerdict is the boot probe's verdict per offered chain; nil means
// every chain is readable.
func receiptsVerdict(decimals map[string]map[string]int, ok map[string]bool) map[string]bool {
	out := make(map[string]bool, len(decimals))
	for chain := range decimals {
		out[chain] = ok == nil || ok[chain]
	}
	return out
}

// ReceiptsReadable: whether chain's RPC last proved it serves receipts. A chain
// that cannot prove a delivery is not quoted or opened on; its open orders
// still settle and send (review I1).
func (w *CompanyWallet) ReceiptsReadable(chain string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.receiptsOK[strings.ToLower(strings.TrimSpace(chain))]
}

// QuotableChains are the chains new orders may be quoted and opened on.
func (w *CompanyWallet) QuotableChains() []string {
	var out []string
	for _, c := range w.Chains() {
		if w.ReceiptsReadable(c) {
			out = append(out, c)
		}
	}
	return out
}

// ReprobeReceipts re-runs the receipt probe on every chain that has an RPC
// to probe, reopening a chain that passes and closing one that fails. Run by
// jobs.ReceiptProbe every 5 minutes.
func (w *CompanyWallet) ReprobeReceipts(ctx context.Context) {
	for chain, rpc := range w.receiptRPCs {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := ProbeReceipts(pctx, rpc)
		cancel()
		w.mu.Lock()
		was := w.receiptsOK[chain]
		w.receiptsOK[chain] = err == nil
		w.mu.Unlock()
		switch {
		case err != nil:
			log.Printf("[ramp-company-wallet] ALERT ramp_company_wallet_receipts_unreadable=1 chain=%s err=%v — not quoted until a re-probe passes", chain, RedactAlertText(err.Error()))
		case !was:
			log.Printf("[ramp-company-wallet] METRIC ramp_company_wallet_receipts_restored chain=%s — quoting again", chain)
		}
	}
}

// cachedFree / storeFree are TreasuryFreeUnits' 5s cache.
func (w *CompanyWallet) cachedFree(chain, token string) (*big.Int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.free[strings.ToLower(chain)+"|"+strings.ToUpper(token)]
	if !ok || w.now().Sub(r.at) >= companyWalletFreeTTL {
		return nil, false
	}
	return new(big.Int).Set(r.units), true
}

func (w *CompanyWallet) storeFree(chain, token string, units *big.Int) {
	w.mu.Lock()
	w.free[strings.ToLower(chain)+"|"+strings.ToUpper(token)] = stockReading{units: new(big.Int).Set(units), at: w.now()}
	w.mu.Unlock()
}

func (w *CompanyWallet) TokenAddress(chain, token string) string {
	lane := w.Lane(chain)
	if lane == nil || lane.SponsorLane == nil {
		return ""
	}
	return lane.tokenAddress(token)
}

// CanPayForTransfer: whether chain's lane can pay one ERC-20 transfer's gas
// (cached 30s by the Treasury).
func (w *CompanyWallet) CanPayForTransfer(ctx context.Context, chain string) bool {
	return w.treasury.CanPayForGas(ctx, strings.ToLower(chain), treasuryERC20Gas)
}

func (w *CompanyWallet) Verifier(chain string) ERC20SourcedTransferVerifier {
	return w.verifiers[strings.ToLower(strings.TrimSpace(chain))]
}

func (w *CompanyWallet) Probe(chain string) TreasuryTxProbe {
	return w.probes[strings.ToLower(strings.TrimSpace(chain))]
}

// ToTokenUnits scales 6-dp units to the token's own decimals exactly; an
// amount that does not divide is refused, never rounded (Decision D54).
func (w *CompanyWallet) ToTokenUnits(chain, token string, units *big.Int) (*big.Int, error) {
	d, ok := w.decimals[strings.ToLower(chain)][strings.ToUpper(token)]
	if !ok {
		return nil, fmt.Errorf("%w: %s on %s", ErrUnsupportedChainAsset, token, chain)
	}
	if d >= StableAssetDecimals {
		return new(big.Int).Mul(units, pow10(d-StableAssetDecimals)), nil
	}
	q, r := new(big.Int).QuoRem(units, pow10(StableAssetDecimals-d), new(big.Int))
	if r.Sign() != 0 {
		return nil, fmt.Errorf("company wallet: %s units cannot be sent exactly in %d-decimal %s", units, d, token)
	}
	return q, nil
}

// fromTokenUnits scales a raw balance to 6-dp units, flooring. (pow10 is the
// package's own helper in conversion_orchestrator.go.)
func fromTokenUnits(raw *big.Int, decimals int) *big.Int {
	if decimals >= StableAssetDecimals {
		return new(big.Int).Quo(raw, pow10(decimals-StableAssetDecimals))
	}
	return new(big.Int).Mul(raw, pow10(StableAssetDecimals-decimals))
}

// Balance is the wallet's on-chain balance of token on chain, in 6-dp units.
// fresh=false reads through a 30s single-flighted cache (failures 5s), for
// quotes, legs and the monitor; fresh=true reads now, within 3s, for open,
// claim and send (Decision D52).
func (w *CompanyWallet) Balance(ctx context.Context, chain, token string, fresh bool) (*big.Int, error) {
	chain = strings.ToLower(strings.TrimSpace(chain))
	token = strings.ToUpper(strings.TrimSpace(token))
	if !w.Offers(chain, token) {
		return nil, fmt.Errorf("%w: %s on %s", ErrUnsupportedChainAsset, token, chain)
	}
	if fresh {
		return w.read(ctx, chain, token)
	}
	key := chain + "|" + token
	w.mu.Lock()
	r, ok := w.stock[key]
	w.mu.Unlock()
	if ok {
		age := w.now().Sub(r.at)
		if r.err == nil && age < companyWalletStockTTL {
			return new(big.Int).Set(r.units), nil
		}
		if r.err != nil && age < companyWalletStockFailTTL {
			return nil, r.err
		}
	}
	v, err, _ := w.flight.Do(key, func() (any, error) { return w.read(ctx, chain, token) })
	if err != nil {
		return nil, err
	}
	return new(big.Int).Set(v.(*big.Int)), nil
}

// read asks the chain, bounded by companyWalletReadTimeout, and refreshes the
// cache. GetTokenBalance takes no context, so the read runs in a goroutine
// (the RPC client bounds it at 10s; the caller stops waiting at 3s).
func (w *CompanyWallet) read(ctx context.Context, chain, token string) (*big.Int, error) {
	type result struct {
		units *big.Int
		err   error
	}
	ch := make(chan result, 1)
	lane := w.Lane(chain)
	// Scaled with the decimals read at boot, the same figure ToTokenUnits
	// sends with, so a balance and a send can never disagree on precision.
	dec := w.decimals[chain][token]
	go func() {
		raw, _, err := laneTokenBalance(lane, token)
		if err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{units: fromTokenUnits(raw, dec)}
	}()
	rctx, cancel := context.WithTimeout(ctx, companyWalletReadTimeout)
	defer cancel()
	var r result
	select {
	case r = <-ch:
	case <-rctx.Done():
		r = result{err: fmt.Errorf("read timed out: %w", rctx.Err())}
	}
	if r.err != nil {
		// Redacted here, where it is made: the transport error carries the RPC
		// URL, key in its path, and this error is logged by the router and the
		// inbound matcher, stored as a parked order's failure_reason and shown
		// in the console.
		r.err = fmt.Errorf("%w: %s on %s: %s", ErrTreasuryStockUnreadable, token, chain, RedactAlertText(r.err.Error()))
	}
	w.mu.Lock()
	w.stock[chain+"|"+token] = stockReading{units: r.units, err: r.err, at: w.now()}
	w.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return new(big.Int).Set(r.units), nil
}
