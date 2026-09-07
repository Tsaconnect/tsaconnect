package sui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// RPC is a thin JSON-RPC 2.0 client over the configured Sui fullnode. Only the
// methods the ChainClient needs are exposed. All Sui reads/tx-builds/broadcasts
// funnel through here so the backend stays the single upstream gateway (mirrors
// Tron's trongrid.go and Solana's rpc.go — no third-party Go SDK).
type RPC struct {
	endpoint string
	hc       *http.Client
}

func NewRPC(endpoint string) *RPC {
	return &RPC{endpoint: endpoint, hc: &http.Client{Timeout: 20 * time.Second}}
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// call performs a single JSON-RPC request and unmarshals result into out.
func (r *RPC) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.hc.Do(req)
	if err != nil {
		return fmt.Errorf("sui rpc %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sui rpc %s: http %d: %s", method, resp.StatusCode, string(raw))
	}
	var parsed rpcResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("sui rpc %s: decode: %w", method, err)
	}
	if parsed.Error != nil {
		return fmt.Errorf("sui rpc %s: %s (code %d)", method, parsed.Error.Message, parsed.Error.Code)
	}
	if out != nil {
		if err := json.Unmarshal(parsed.Result, out); err != nil {
			return fmt.Errorf("sui rpc %s: decode result: %w", method, err)
		}
	}
	return nil
}

// --- read models ---

// SuiBalance is one coin-type aggregate from suix_getBalance/getAllBalances.
type SuiBalance struct {
	CoinType        string `json:"coinType"`
	CoinObjectCount int    `json:"coinObjectCount"`
	TotalBalance    string `json:"totalBalance"` // u128 as string
}

// SuiCoin is one owned Coin<T> object from suix_getCoins.
type SuiCoin struct {
	CoinType     string `json:"coinType"`
	CoinObjectID string `json:"coinObjectId"`
	Version      string `json:"version"`
	Digest       string `json:"digest"`
	Balance      string `json:"balance"` // u64 as string
}

type coinPage struct {
	Data        []SuiCoin `json:"data"`
	NextCursor  *string   `json:"nextCursor"`
	HasNextPage bool      `json:"hasNextPage"`
}

// SuiCoinMetadata is the on-chain registered metadata for a coin type.
type SuiCoinMetadata struct {
	Decimals uint8  `json:"decimals"`
	Name     string `json:"name"`
	Symbol   string `json:"symbol"`
}

type txBytesResult struct {
	TxBytes string `json:"txBytes"` // base64 BCS TransactionData
}

// SuiExecEffects is the subset of transaction effects we read for status.
type SuiExecEffects struct {
	Status struct {
		Status string `json:"status"` // "success" | "failure"
		Error  string `json:"error"`
	} `json:"status"`
}

// SuiBalanceChange is one per-owner, per-coin-type net delta on a tx (amount is
// a signed integer string) — the basis for history direction/amount.
type SuiBalanceChange struct {
	Owner    json.RawMessage `json:"owner"` // {"AddressOwner":"0x…"} | {"ObjectOwner":…} | "Immutable"
	CoinType string          `json:"coinType"`
	Amount   string          `json:"amount"`
}

// AddressOwner extracts the AddressOwner field, or "" for non-address owners.
func (b SuiBalanceChange) AddressOwner() string {
	var wrap struct {
		AddressOwner string `json:"AddressOwner"`
	}
	_ = json.Unmarshal(b.Owner, &wrap)
	return wrap.AddressOwner
}

// SuiTxResponse is the subset of a transaction-block response we consume.
type SuiTxResponse struct {
	Digest         string             `json:"digest"`
	Effects        *SuiExecEffects    `json:"effects"`
	TimestampMs    *string            `json:"timestampMs"`
	Checkpoint     *string            `json:"checkpoint"`
	BalanceChanges []SuiBalanceChange `json:"balanceChanges"`
}

// GetAllBalances returns one aggregate per coin type the owner holds.
func (r *RPC) GetAllBalances(ctx context.Context, owner string) ([]SuiBalance, error) {
	var out []SuiBalance
	if err := r.call(ctx, "suix_getAllBalances", []interface{}{owner}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetBalance returns the aggregate for a single coin type (empty coinType →
// native SUI).
func (r *RPC) GetBalance(ctx context.Context, owner, coinType string) (SuiBalance, error) {
	var out SuiBalance
	params := []interface{}{owner}
	if coinType != "" {
		params = append(params, coinType)
	}
	err := r.call(ctx, "suix_getBalance", params, &out)
	return out, err
}

// GetCoins pages through the owner's Coin<T> objects for a coin type (empty →
// SUI), gathering up to maxCoins across pages. truncated is true when the owner
// holds MORE coins than were returned (so a caller that came up short knows the
// shortfall may be coin fragmentation, not a genuine lack of funds — distinct
// user-facing errors). Bounded to avoid unbounded paging on dust-flooded wallets.
func (r *RPC) GetCoins(ctx context.Context, owner, coinType string, maxCoins int) (coins []SuiCoin, truncated bool, err error) {
	var cursor *string
	for {
		params := []interface{}{owner}
		if coinType != "" {
			params = append(params, coinType)
		} else {
			params = append(params, nil)
		}
		params = append(params, cursor, 50)
		var page coinPage
		if err := r.call(ctx, "suix_getCoins", params, &page); err != nil {
			return nil, false, err
		}
		coins = append(coins, page.Data...)
		if len(coins) >= maxCoins {
			// Hit the gather cap; report truncation only if more actually remain.
			return coins, page.HasNextPage, nil
		}
		if !page.HasNextPage || page.NextCursor == nil {
			return coins, false, nil
		}
		cursor = page.NextCursor
	}
}

// ReferenceGasPrice returns the current epoch reference gas price (MIST/unit).
// Reserved for the dry-run-based gas sizing follow-up (the MVP uses a fixed
// conservative budget); kept here so that work is a client.go-local change.
func (r *RPC) ReferenceGasPrice(ctx context.Context) (string, error) {
	var out string
	if err := r.call(ctx, "suix_getReferenceGasPrice", []interface{}{}, &out); err != nil {
		return "", err
	}
	return out, nil
}

// GetCoinMetadata returns the registered metadata (decimals/symbol) for a coin
// type; ok is false when the type has no metadata object.
func (r *RPC) GetCoinMetadata(ctx context.Context, coinType string) (SuiCoinMetadata, bool, error) {
	var out *SuiCoinMetadata
	if err := r.call(ctx, "suix_getCoinMetadata", []interface{}{coinType}, &out); err != nil {
		return SuiCoinMetadata{}, false, err
	}
	if out == nil {
		return SuiCoinMetadata{}, false, nil
	}
	return *out, true, nil
}

// PaySui builds an unsigned native-SUI transfer via unsafe_paySui, using the
// given input coins for both the transfer and gas. Returns base64 TransactionData.
func (r *RPC) PaySui(ctx context.Context, signer string, inputCoins, recipients, amounts []string, gasBudget string) (string, error) {
	var out txBytesResult
	err := r.call(ctx, "unsafe_paySui",
		[]interface{}{signer, inputCoins, recipients, amounts, gasBudget}, &out)
	if err != nil {
		return "", err
	}
	return out.TxBytes, nil
}

// Pay builds an unsigned Coin<T> transfer via unsafe_pay. gas is nil so the node
// auto-selects a SUI gas coin distinct from the input coins. Returns base64
// TransactionData.
func (r *RPC) Pay(ctx context.Context, signer string, inputCoins, recipients, amounts []string, gasBudget string) (string, error) {
	var out txBytesResult
	err := r.call(ctx, "unsafe_pay",
		[]interface{}{signer, inputCoins, recipients, amounts, nil, gasBudget}, &out)
	if err != nil {
		return "", err
	}
	return out.TxBytes, nil
}

// ExecuteTransactionBlock submits a signed tx (base64 tx bytes + serialized
// signatures) and returns the response (digest + effects).
func (r *RPC) ExecuteTransactionBlock(ctx context.Context, txBytesB64 string, sigsB64 []string) (SuiTxResponse, error) {
	var out SuiTxResponse
	opts := map[string]interface{}{"showEffects": true}
	err := r.call(ctx, "sui_executeTransactionBlock",
		[]interface{}{txBytesB64, sigsB64, opts, "WaitForEffectsCert"}, &out)
	return out, err
}

// DryRunBalanceChange is one address's net movement of one coin type, as the
// node computes it by simulating a transaction.
type DryRunBalanceChange struct {
	Owner    json.RawMessage `json:"owner"`
	CoinType string          `json:"coinType"`
	// Amount is a SIGNED decimal string in base units: negative leaves the
	// address, positive arrives.
	Amount string `json:"amount"`
}

// DryRunGasUsed is the simulation's own gas accounting, in MIST.
type DryRunGasUsed struct {
	ComputationCost string `json:"computationCost"`
	StorageCost     string `json:"storageCost"`
	StorageRebate   string `json:"storageRebate"`
}

// NetGas is computationCost + storageCost - storageRebate: what the sender
// actually pays, and exactly the amount the node subtracts from their SUI
// balance change. (Confirmed against mainnet: a transaction that did nothing
// but pay gas reports a sender balance change of precisely -netGas.) That
// makes it the allowance the spend check must grant SUI on top of the swap,
// or every Sui swap is refused for spending its own gas.
//
// A storage rebate larger than the cost makes the total negative — a refund,
// which arrives as an inflow rather than an outflow — so it floors at zero.
func (g DryRunGasUsed) NetGas() *big.Int {
	total := new(big.Int)
	for _, v := range []string{g.ComputationCost, g.StorageCost} {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(v), 10); ok {
			total.Add(total, n)
		}
	}
	if n, ok := new(big.Int).SetString(strings.TrimSpace(g.StorageRebate), 10); ok {
		total.Sub(total, n)
	}
	if total.Sign() < 0 {
		return new(big.Int)
	}
	return total
}

// DryRunResult is the part of a simulation we act on: whether it would succeed,
// what it would move, and what it would cost to run.
type DryRunResult struct {
	Effects struct {
		Status struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"status"`
		GasUsed DryRunGasUsed `json:"gasUsed"`
	} `json:"effects"`
	BalanceChanges []DryRunBalanceChange `json:"balanceChanges"`
}

// DryRun simulates an unsigned transaction without submitting it.
//
// This is what makes it safe to sign a transaction somebody else built: the
// node reports exactly what it would move, so an aggregator's opaque payload
// can be checked against what the user actually agreed to before a signing
// digest is handed out.
func (r *RPC) DryRun(ctx context.Context, txBytesB64 string) (*DryRunResult, error) {
	var out *DryRunResult
	if err := r.call(ctx, "sui_dryRunTransactionBlock", []interface{}{txBytesB64}, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("sui: empty dry-run response")
	}
	return out, nil
}

// GetTransactionBlock reads a transaction by digest (with effects) for status.
func (r *RPC) GetTransactionBlock(ctx context.Context, digest string) (*SuiTxResponse, error) {
	var out *SuiTxResponse
	opts := map[string]interface{}{"showEffects": true, "showInput": false}
	if err := r.call(ctx, "sui_getTransactionBlock", []interface{}{digest, opts}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// QueryTransactionBlocks returns recent tx digests for an address filter
// ("FromAddress" or "ToAddress"), newest first, with effects+timestamp for
// history rows.
func (r *RPC) QueryTransactionBlocks(ctx context.Context, filterKey, addr string, limit int) ([]SuiTxResponse, error) {
	query := map[string]interface{}{
		"filter":  map[string]interface{}{filterKey: addr},
		"options": map[string]interface{}{"showEffects": true, "showBalanceChanges": true, "showInput": false},
	}
	var out struct {
		Data []SuiTxResponse `json:"data"`
	}
	err := r.call(ctx, "suix_queryTransactionBlocks", []interface{}{query, nil, limit, true}, &out)
	if err != nil {
		return nil, err
	}
	return out.Data, nil
}
