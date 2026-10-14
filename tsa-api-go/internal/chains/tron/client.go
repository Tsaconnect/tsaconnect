package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// Fee constants (conservative; refine against Nile testnet during execution).
const (
	trc20FeeLimitSun  = 100_000_000 // 100 TRX cap for TRC-20 transfer
	activationCostSun = 1_100_000   // ~1.1 TRX to activate a new account
	nativeFeeEstSun   = 300_000     // ~0.3 TRX bandwidth headroom
	// trc20FeeEstSun is what a contract call typically burns; the fee limit is
	// only a cap, and the preflight asks for this, not for the cap.
	trc20FeeEstSun = trc20FeeLimitSun / 10
)

type Client struct {
	cfg config.NonEVMChainConfig
	tg  *TronGrid
	now func() time.Time // aggregator expiry checks; replaced in tests
}

func NewClient(cfg config.NonEVMChainConfig, tg *TronGrid) *Client {
	return &Client{cfg: cfg, tg: tg, now: time.Now}
}

func (c *Client) Family() string { return "tron" }

func (c *Client) ValidateAddress(addr string) error {
	if !IsValidBase58(addr) {
		return fmt.Errorf("invalid tron address: %s", addr)
	}
	return nil
}

func (c *Client) GetBalances(ctx context.Context, addr string) ([]chains.Balance, error) {
	acc, err := c.tg.GetAccount(ctx, addr)
	if err != nil {
		return nil, err
	}
	out := []chains.Balance{{
		Symbol: "TRX", Raw: strconv.FormatInt(acc.BalanceSun, 10), Decimals: 6, IsNative: true,
	}}
	for sym, tokenAddr := range c.cfg.Tokens {
		out = append(out, chains.Balance{
			Symbol: sym, Raw: firstNonEmpty(acc.TRC20[tokenAddr], "0"),
			Decimals: 6, TokenAddress: tokenAddr, Listed: true,
		})
	}
	return out, nil
}

func (c *Client) EstimateFee(ctx context.Context, req chains.TransferRequest) (chains.FeeQuote, error) {
	// Addresses are interpolated into TronGrid URL paths; nothing unchecked
	// goes there.
	if !IsValidBase58(req.From) {
		return chains.FeeQuote{}, chains.Invalid("invalid from address")
	}
	contractCall := req.TronRawHex != "" || req.Approve
	if !contractCall && !IsValidBase58(req.To) {
		return chains.FeeQuote{}, chains.Invalid("invalid tron address: %s", req.To)
	}
	sender, err := c.tg.GetAccount(ctx, req.From)
	if err != nil {
		return chains.FeeQuote{}, err
	}
	// A route's transaction and an approval are contract calls, priced like a
	// TRC-20 transfer. Their target is a contract, so there is no recipient to
	// activate. The balance is held to the typical burn, not to the route's
	// fee_limit: that is a ceiling the call rarely reaches, and a wallet with
	// staked energy may burn no TRX at all — demanding the full ceiling would
	// refuse swaps that go through.
	if contractCall {
		// A TRX-source route spends the amount AND the fee from the same
		// balance, so it must hold both; a token source or an approval only
		// needs the fee in TRX.
		required := big.NewInt(trc20FeeEstSun)
		note := "You need TRX in this wallet to pay the Tron network fee."
		if req.TronRawHex != "" && strings.TrimSpace(req.TokenAddr) == "" {
			amount, ok := new(big.Int).SetString(strings.TrimSpace(req.Amount), 10)
			if !ok || amount.Sign() <= 0 {
				return chains.FeeQuote{}, chains.Invalid("bad amount")
			}
			required.Add(required, amount)
			note = "Not enough TRX to cover the amount plus the Tron network fee."
		}
		quote := chains.FeeQuote{
			GasSymbol: "TRX", EstFeeRaw: strconv.FormatInt(trc20FeeEstSun, 10), GasDecimals: 6,
			HasEnoughGas: big.NewInt(sender.BalanceSun).Cmp(required) >= 0,
		}
		if !quote.HasEnoughGas {
			quote.Note = note
		}
		return quote, nil
	}
	recipient, err := c.tg.GetAccount(ctx, req.To)
	if err != nil {
		return chains.FeeQuote{}, err
	}
	estFee := int64(nativeFeeEstSun)
	if req.TokenAddr != "" {
		estFee = trc20FeeEstSun
	}
	needsActivation := !recipient.Active
	if needsActivation {
		estFee += activationCostSun
	}
	note := ""
	if sender.BalanceSun < estFee {
		if req.TokenAddr != "" {
			note = "You need TRX in this wallet to send " + req.Symbol + "."
		} else {
			note = "Not enough TRX to cover the network fee."
		}
	}
	return chains.FeeQuote{
		GasSymbol: "TRX", EstFeeRaw: strconv.FormatInt(estFee, 10), GasDecimals: 6,
		NeedsActivation: needsActivation, HasEnoughGas: sender.BalanceSun >= estFee, Note: note,
	}, nil
}

func (c *Client) PrepareTransfer(ctx context.Context, req chains.TransferRequest) (chains.PreparedTx, error) {
	if req.MemoHex != "" {
		return chains.PreparedTx{}, chains.Invalid("tron transfers cannot carry a bridge memo")
	}
	if req.PSBTHex != "" {
		return chains.PreparedTx{}, chains.Invalid("a PSBT is a Bitcoin transaction; it cannot be signed on tron")
	}
	if req.TxBytesB64 != "" {
		return chains.PreparedTx{}, chains.Invalid("a Sui transaction cannot be signed on tron")
	}
	if req.TronRawHex != "" && req.Approve {
		return chains.PreparedTx{}, chains.Invalid("an approval and a route transaction are separate requests")
	}
	// An aggregator-built transaction is signed as given, once it has been
	// decoded and held to what the user approved (see prepareFromRawHex).
	if req.TronRawHex != "" {
		return c.prepareFromRawHex(req)
	}
	if req.Approve {
		return c.prepareApprove(ctx, req)
	}
	if err := c.ValidateAddress(req.To); err != nil {
		return chains.PreparedTx{}, err
	}
	if !IsValidBase58(req.From) {
		return chains.PreparedTx{}, chains.Invalid("invalid from address")
	}
	var raw *RawTx
	var err error
	if req.TokenAddr == "" {
		amt, ok := new(big.Int).SetString(req.Amount, 10)
		if !ok {
			return chains.PreparedTx{}, fmt.Errorf("bad amount")
		}
		raw, err = c.tg.CreateTransfer(ctx, req.From, req.To, amt.Int64())
	} else {
		amt, ok := new(big.Int).SetString(req.Amount, 10)
		if !ok {
			return chains.PreparedTx{}, fmt.Errorf("bad amount")
		}
		raw, err = c.tg.TriggerTransfer(ctx, req.From, req.TokenAddr, req.To, amt, trc20FeeLimitSun)
	}
	if err != nil {
		return chains.PreparedTx{}, err
	}
	rawJSON, _ := json.Marshal(raw)
	return chains.PreparedTx{
		Family: "tron",
		Raw:    string(rawJSON),
		// The Tron txID IS the sha256 digest of raw_data — sign it directly.
		Payloads: []chains.SigningPayload{{Index: 0, HashHex: raw.TxID, Curve: chains.CurveSecp256k1}},
	}, nil
}

func (c *Client) AssembleAndBroadcast(ctx context.Context, prepared chains.PreparedTx, sigs []chains.Signature) (string, error) {
	if len(sigs) != 1 {
		return "", fmt.Errorf("tron expects exactly one signature, got %d", len(sigs))
	}
	var raw RawTx
	if err := json.Unmarshal([]byte(prepared.Raw), &raw); err != nil {
		return "", err
	}
	if isAggregatorRaw(&raw) {
		return c.broadcastAggregatorRaw(ctx, &raw, sigs[0].SigHex)
	}
	return c.tg.Broadcast(ctx, &raw, sigs[0].SigHex)
}

// prepareApprove takes the wallet one step toward an allowance of exactly
// Amount for spender To on TokenAddr. The allowance is never left unlimited:
// the token amount a route pulls sits inside calldata we cannot read, so the
// approval is the only bound on it.
//
// Contract with the app — loop prepare → sign → submit → wait for the tx,
// until prepare answers with no payloads (at most 3 rounds):
//   - allowance ≥ Amount: no payloads, nothing to sign. Already approved.
//   - allowance == 0: approve(To, Amount).
//   - 0 < allowance < Amount: approve(To, 0) first. USDT-style tokens refuse
//     to move a non-zero allowance to another non-zero value, and resetting
//     first also closes the classic allowance-change race. The next round then
//     sees 0 and approves Amount.
func (c *Client) prepareApprove(ctx context.Context, req chains.TransferRequest) (chains.PreparedTx, error) {
	if !IsValidBase58(req.From) {
		return chains.PreparedTx{}, chains.Invalid("invalid from address")
	}
	if !IsValidBase58(req.TokenAddr) {
		return chains.PreparedTx{}, chains.Invalid("an approval needs a valid token address")
	}
	if err := c.requireRouter(req.To); err != nil {
		return chains.PreparedTx{}, err
	}
	needed, ok := new(big.Int).SetString(strings.TrimSpace(req.Amount), 10)
	if !ok || needed.Sign() <= 0 {
		return chains.PreparedTx{}, chains.Invalid("bad amount")
	}
	current, err := c.tg.Allowance(ctx, req.TokenAddr, req.From, req.To)
	if err != nil {
		return chains.PreparedTx{}, err
	}
	if current.Cmp(needed) >= 0 {
		return chains.PreparedTx{Family: "tron", Payloads: []chains.SigningPayload{}}, nil
	}
	target := needed
	if current.Sign() > 0 {
		target = new(big.Int)
	}
	raw, err := c.tg.TriggerApprove(ctx, req.From, req.TokenAddr, req.To, target, trc20FeeLimitSun)
	if err != nil {
		return chains.PreparedTx{}, err
	}
	if err := verifyBuiltApprove(raw, req.From, req.TokenAddr, req.To, target, c.now()); err != nil {
		return chains.PreparedTx{}, err
	}
	// Only the hex was verified, so only the hex is kept: without raw_data the
	// broadcast goes through broadcasthex with these exact bytes, never through
	// a JSON copy we did not check.
	rawJSON, _ := json.Marshal(RawTx{TxID: raw.TxID, RawHex: raw.RawHex})
	return chains.PreparedTx{
		Family:   "tron",
		Raw:      string(rawJSON),
		Payloads: []chains.SigningPayload{{Index: 0, HashHex: raw.TxID, Curve: chains.CurveSecp256k1}},
	}, nil
}

func (c *Client) GetTxStatus(ctx context.Context, txHash string) (chains.TxStatus, error) {
	info, err := c.tg.GetTxInfo(ctx, txHash)
	if err != nil {
		return chains.TxStatus{}, err
	}
	return chains.TxStatus{
		Found: info.ID != "", Confirmed: info.Confirmed, Success: info.Success,
		Confirmations: boolToConf(info.Confirmed),
	}, nil
}

func (c *Client) History(ctx context.Context, addr string, cursor string) ([]chains.Activity, string, error) {
	// cursor is reserved: this returns a bounded window of the most recent
	// transfers (perKind native + perKind token). Fingerprint-based pagination
	// is deferred; the app fetches this once, on the first activity page. The
	// returned "next" is therefore always empty.
	const perKind = 30

	// TRX and TRC-20 live on separate TronGrid endpoints — fetch concurrently.
	var trx, trc []HistoryItem
	var trxErr, trcErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); trx, trxErr = c.tg.GetTRXTransfers(ctx, addr, perKind) }()
	go func() { defer wg.Done(); trc, trcErr = c.tg.GetTRC20Transfers(ctx, addr, perKind) }()
	wg.Wait()
	if trxErr != nil {
		return nil, "", trxErr
	}
	if trcErr != nil {
		return nil, "", trcErr
	}

	// TRX rows carry hex (41..) addresses; TRC-20 rows carry base58. Compare
	// each row's sender against the owner in its own encoding to set direction.
	addrHex, _ := Base58ToHex(addr)

	merged := make([]HistoryItem, 0, len(trx)+len(trc))
	merged = append(merged, trx...)
	merged = append(merged, trc...)

	items := make([]chains.Activity, 0, len(merged))
	for _, it := range merged {
		fromIsMe := (it.IsTRC20 && strings.EqualFold(it.From, addr)) ||
			(!it.IsTRC20 && strings.EqualFold(it.From, addrHex))
		direction, counter := "in", it.From
		if fromIsMe {
			direction, counter = "out", it.To
		}
		if !it.IsTRC20 {
			counter = HexToBase58(counter) // pretty base58 for display
		}
		status := "confirmed"
		if !it.Success {
			status = "failed"
		}
		items = append(items, chains.Activity{
			Hash: it.Hash, Direction: direction, AmountRaw: it.AmountRaw, Decimals: it.Decimals,
			Symbol: it.Symbol, Counter: counter, Status: status,
			BlockTime: it.BlockTime / 1000, // ms → s
		})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].BlockTime > items[j].BlockTime })
	return items, "", nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func boolToConf(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
