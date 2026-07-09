package tron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
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

func (t *TronGrid) TriggerTransfer(ctx context.Context, from, tokenAddr, to string, amount *big.Int, feeLimit int64) (*RawTx, error) {
	// ABI-encode transfer(address,uint256): 32-byte padded recipient (hex, no 41 prefix) + 32-byte amount.
	toHex, err := Base58ToHex(to)
	if err != nil {
		return nil, err
	}
	param := fmt.Sprintf("%064s%064s", toHex[2:], toHexNoPrefix(amount))
	body := map[string]any{
		"owner_address":     from,
		"contract_address":  tokenAddr,
		"function_selector": "transfer(address,uint256)",
		"parameter":         param,
		"fee_limit":         feeLimit,
		"call_value":        0,
		"visible":           true,
	}
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
