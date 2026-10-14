package tron

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"time"
)

type TronGrid struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewTronGrid(baseURL, apiKey string) *TronGrid {
	return &TronGrid{baseURL: baseURL, apiKey: apiKey, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

type Account struct {
	Active     bool
	BalanceSun int64
	TRC20      map[string]string // token contract → integer string
}

type RawTx struct {
	TxID    string          `json:"txID"`
	RawData json.RawMessage `json:"raw_data"`
	RawHex  string          `json:"raw_data_hex"`
	Visible bool            `json:"visible"`
}

type TxInfo struct {
	ID          string
	BlockNumber int64
	Confirmed   bool
	Success     bool
}

func (t *TronGrid) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if t.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", t.apiKey)
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("trongrid %s: status %d: %s", path, resp.StatusCode, string(raw))
	}
	return json.Unmarshal(raw, out)
}

func (t *TronGrid) GetAccount(ctx context.Context, addr string) (*Account, error) {
	var envelope struct {
		Data []struct {
			Balance int64               `json:"balance"`
			TRC20   []map[string]string `json:"trc20"`
		} `json:"data"`
	}
	if err := t.do(ctx, http.MethodGet, "/v1/accounts/"+addr, nil, &envelope); err != nil {
		return nil, err
	}
	acc := &Account{TRC20: map[string]string{}}
	if len(envelope.Data) == 0 {
		return acc, nil // inactive account: Active=false, zero balances
	}
	acc.Active = true
	acc.BalanceSun = envelope.Data[0].Balance
	for _, m := range envelope.Data[0].TRC20 {
		for k, v := range m {
			acc.TRC20[k] = v
		}
	}
	return acc, nil
}

func (t *TronGrid) CreateTransfer(ctx context.Context, from, to string, amountSun int64) (*RawTx, error) {
	body := map[string]any{"owner_address": from, "to_address": to, "amount": amountSun, "visible": true}
	var rt RawTx
	if err := t.do(ctx, http.MethodPost, "/wallet/createtransaction", body, &rt); err != nil {
		return nil, err
	}
	if rt.TxID == "" {
		return nil, fmt.Errorf("trongrid createtransaction returned no txID")
	}
	return &rt, nil
}

// abiAddressWord ABI-encodes a base58 Tron address as one 32-byte word: the
// 20-byte EVM-style body (the 21-byte form minus its 0x41 prefix), left-padded.
func abiAddressWord(addr string) (string, error) {
	h, err := Base58ToHex(addr)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%064s", h[2:]), nil
}

// contractCallBody is the request shared by triggersmartcontract (build a call)
// and triggerconstantcontract (run one read-only).
func contractCallBody(from, contract, selector, param string) map[string]any {
	return map[string]any{
		"owner_address":     from,
		"contract_address":  contract,
		"function_selector": selector,
		"parameter":         param,
		"visible":           true,
	}
}

// triggerSmartContract builds an unsigned call of selector(param) on contract,
// sent by from with no TRX attached.
func (t *TronGrid) triggerSmartContract(ctx context.Context, from, contract, selector, param string, feeLimit int64) (*RawTx, error) {
	body := contractCallBody(from, contract, selector, param)
	body["fee_limit"] = feeLimit
	body["call_value"] = 0
	var wrap struct {
		Transaction RawTx `json:"transaction"`
		Result      struct {
			Result  bool   `json:"result"`
			Message string `json:"message"`
		} `json:"result"`
	}
	if err := t.do(ctx, http.MethodPost, "/wallet/triggersmartcontract", body, &wrap); err != nil {
		return nil, err
	}
	if wrap.Transaction.TxID == "" {
		return nil, fmt.Errorf("triggersmartcontract failed: %s", wrap.Result.Message)
	}
	return &wrap.Transaction, nil
}

// TriggerTransfer builds a TRC-20 transfer(to, amount).
func (t *TronGrid) TriggerTransfer(ctx context.Context, from, tokenAddr, to string, amount *big.Int, feeLimit int64) (*RawTx, error) {
	toWord, err := abiAddressWord(to)
	if err != nil {
		return nil, err
	}
	param := toWord + fmt.Sprintf("%064s", toHexNoPrefix(amount))
	return t.triggerSmartContract(ctx, from, tokenAddr, "transfer(address,uint256)", param, feeLimit)
}

// TriggerApprove builds a TRC-20 approve(spender, amount).
func (t *TronGrid) TriggerApprove(ctx context.Context, from, tokenAddr, spender string, amount *big.Int, feeLimit int64) (*RawTx, error) {
	spenderWord, err := abiAddressWord(spender)
	if err != nil {
		return nil, err
	}
	param := spenderWord + fmt.Sprintf("%064s", toHexNoPrefix(amount))
	return t.triggerSmartContract(ctx, from, tokenAddr, "approve(address,uint256)", param, feeLimit)
}

// Allowance reads a TRC-20 allowance(owner, spender) with a constant call.
func (t *TronGrid) Allowance(ctx context.Context, tokenAddr, owner, spender string) (*big.Int, error) {
	ownerWord, err := abiAddressWord(owner)
	if err != nil {
		return nil, err
	}
	spenderWord, err := abiAddressWord(spender)
	if err != nil {
		return nil, err
	}
	body := contractCallBody(owner, tokenAddr, "allowance(address,address)", ownerWord+spenderWord)
	var res struct {
		ConstantResult []string `json:"constant_result"`
		Result         struct {
			Result  bool   `json:"result"`
			Message string `json:"message"`
		} `json:"result"`
	}
	if err := t.do(ctx, http.MethodPost, "/wallet/triggerconstantcontract", body, &res); err != nil {
		return nil, err
	}
	if !res.Result.Result || len(res.ConstantResult) == 0 {
		return nil, fmt.Errorf("allowance read failed: %s", res.Result.Message)
	}
	v, ok := new(big.Int).SetString(res.ConstantResult[0], 16)
	if !ok {
		return nil, fmt.Errorf("allowance read returned %q", res.ConstantResult[0])
	}
	return v, nil
}

func (t *TronGrid) Broadcast(ctx context.Context, raw *RawTx, sigHex string) (string, error) {
	body := map[string]any{
		"txID":         raw.TxID,
		"raw_data":     raw.RawData,
		"raw_data_hex": raw.RawHex,
		"signature":    []string{sigHex},
		"visible":      true,
	}
	var res struct {
		Result  bool   `json:"result"`
		Txid    string `json:"txid"`
		Message string `json:"message"`
	}
	if err := t.do(ctx, http.MethodPost, "/wallet/broadcasttransaction", body, &res); err != nil {
		return "", err
	}
	if !res.Result {
		return "", fmt.Errorf("broadcast rejected: %s", res.Message)
	}
	return raw.TxID, nil
}

// BroadcastHex sends a signed protocol.Transaction serialized as hex. Used for
// aggregator-built transactions, where the protobuf bytes are what was
// validated and signed and no raw_data JSON of ours exists to re-encode.
func (t *TronGrid) BroadcastHex(ctx context.Context, txHex string) error {
	var res struct {
		Result  bool   `json:"result"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := t.do(ctx, http.MethodPost, "/wallet/broadcasthex", map[string]any{"transaction": txHex}, &res); err != nil {
		return err
	}
	if !res.Result {
		return fmt.Errorf("broadcast rejected: %s %s", res.Code, decodeNodeMessage(res.Message))
	}
	return nil
}

// decodeNodeMessage returns a node error message as text: java-tron hex-encodes
// it on the broadcast endpoints.
func decodeNodeMessage(m string) string {
	if b, err := hex.DecodeString(m); err == nil && len(b) > 0 {
		return string(b)
	}
	return m
}

// HistoryItem is one normalized transfer from the TronGrid history endpoints.
// Native (TRX) rows carry hex 41-prefixed addresses; TRC-20 rows carry base58.
type HistoryItem struct {
	Hash      string
	From      string
	To        string
	AmountRaw string
	Symbol    string
	Decimals  int
	BlockTime int64 // ms since epoch
	Success   bool
	IsTRC20   bool
}

// GetTRXTransfers returns recent native-TRX transfers for an address.
func (t *TronGrid) GetTRXTransfers(ctx context.Context, addr string, limit int) ([]HistoryItem, error) {
	var env struct {
		Data []struct {
			TxID           string `json:"txID"`
			BlockTimestamp int64  `json:"block_timestamp"`
			Ret            []struct {
				ContractRet string `json:"contractRet"`
			} `json:"ret"`
			RawData struct {
				Contract []struct {
					Type      string `json:"type"`
					Parameter struct {
						Value struct {
							Amount       int64  `json:"amount"`
							OwnerAddress string `json:"owner_address"`
							ToAddress    string `json:"to_address"`
						} `json:"value"`
					} `json:"parameter"`
				} `json:"contract"`
			} `json:"raw_data"`
		} `json:"data"`
	}
	path := fmt.Sprintf("/v1/accounts/%s/transactions?limit=%d&only_confirmed=true&order_by=block_timestamp,desc", addr, limit)
	if err := t.do(ctx, http.MethodGet, path, nil, &env); err != nil {
		return nil, err
	}
	out := make([]HistoryItem, 0, len(env.Data))
	for _, tx := range env.Data {
		if len(tx.RawData.Contract) == 0 || tx.RawData.Contract[0].Type != "TransferContract" {
			continue // only plain TRX transfers (skip contract calls, freezes, etc.)
		}
		v := tx.RawData.Contract[0].Parameter.Value
		success := len(tx.Ret) == 0 || tx.Ret[0].ContractRet == "" || tx.Ret[0].ContractRet == "SUCCESS"
		out = append(out, HistoryItem{
			Hash: tx.TxID, From: v.OwnerAddress, To: v.ToAddress,
			AmountRaw: strconv.FormatInt(v.Amount, 10), Symbol: "TRX", Decimals: 6,
			BlockTime: tx.BlockTimestamp, Success: success, IsTRC20: false,
		})
	}
	return out, nil
}

// GetTRC20Transfers returns recent TRC-20 transfers for an address (base58
// from/to, token symbol + decimals from token_info).
func (t *TronGrid) GetTRC20Transfers(ctx context.Context, addr string, limit int) ([]HistoryItem, error) {
	var env struct {
		Data []struct {
			TransactionID  string `json:"transaction_id"`
			BlockTimestamp int64  `json:"block_timestamp"`
			From           string `json:"from"`
			To             string `json:"to"`
			Value          string `json:"value"`
			TokenInfo      struct {
				Symbol   string `json:"symbol"`
				Decimals int    `json:"decimals"`
				Address  string `json:"address"`
			} `json:"token_info"`
		} `json:"data"`
	}
	path := fmt.Sprintf("/v1/accounts/%s/transactions/trc20?limit=%d&only_confirmed=true&order_by=block_timestamp,desc", addr, limit)
	if err := t.do(ctx, http.MethodGet, path, nil, &env); err != nil {
		return nil, err
	}
	out := make([]HistoryItem, 0, len(env.Data))
	for _, tx := range env.Data {
		if tx.Value == "" || tx.TokenInfo.Symbol == "" {
			continue
		}
		out = append(out, HistoryItem{
			Hash: tx.TransactionID, From: tx.From, To: tx.To,
			AmountRaw: tx.Value, Symbol: tx.TokenInfo.Symbol, Decimals: tx.TokenInfo.Decimals,
			BlockTime: tx.BlockTimestamp, Success: true, IsTRC20: true,
		})
	}
	return out, nil
}

func (t *TronGrid) GetTxInfo(ctx context.Context, txID string) (*TxInfo, error) {
	body := map[string]any{"value": txID}
	var info struct {
		ID          string `json:"id"`
		BlockNumber int64  `json:"blockNumber"`
		Receipt     struct {
			Result string `json:"result"`
		} `json:"receipt"`
	}
	if err := t.do(ctx, http.MethodPost, "/wallet/gettransactioninfobyid", body, &info); err != nil {
		return nil, err
	}
	if info.ID == "" {
		return &TxInfo{Confirmed: false}, nil
	}
	// A native TRX transfer has no receipt.result; treat mined-with-no-failure as success.
	success := info.Receipt.Result == "" || info.Receipt.Result == "SUCCESS"
	return &TxInfo{ID: info.ID, BlockNumber: info.BlockNumber, Confirmed: info.BlockNumber > 0, Success: success}, nil
}

func toHexNoPrefix(n *big.Int) string {
	return fmt.Sprintf("%x", n)
}
