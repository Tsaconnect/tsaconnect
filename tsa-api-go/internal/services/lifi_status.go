package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"time"
)

// LiFi transfer statuses.
const (
	LiFiStatusPending = "PENDING"
	LiFiStatusDone    = "DONE"
	LiFiStatusFailed  = "FAILED"
)

// LiFiTransferStatus is the outcome of one bridged transfer.
//
// ReceivedBaseUnits is what actually landed at the destination — a bridge
// delivers less than was sent, and the payout is computed from this figure, not
// from the quoted amount. Treat it as a lead, not as proof: the destination tx
// is verified independently on-chain before any money moves.
type LiFiTransferStatus struct {
	Status            string
	DestTxHash        string
	DestChainID       int64
	ReceivedBaseUnits *big.Int
	ToAddress         string
}

var (
	ErrLiFiPending = errors.New("lifi: transfer still in flight")
	ErrLiFiFailed  = errors.New("lifi: transfer failed")
)

type LiFiStatusClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewLiFiStatusClient(baseURL, apiKey string) *LiFiStatusClient {
	return &LiFiStatusClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

type lifiStatusResponse struct {
	Status    string `json:"status"`
	Receiving struct {
		TxHash    string `json:"txHash"`
		ChainID   int64  `json:"chainId"`
		Amount    string `json:"amount"`
		ToAddress string `json:"toAddress"`
	} `json:"receiving"`
}

// Status reports what became of the source transaction the user broadcast.
func (c *LiFiStatusClient) Status(ctx context.Context, srcTxHash string) (*LiFiTransferStatus, error) {
	u := fmt.Sprintf("%s/status?txHash=%s", c.baseURL, url.QueryEscape(srcTxHash))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("x-lifi-api-key", c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lifi status: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("lifi status: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("lifi status: http %d: %s", resp.StatusCode, string(body))
	}
	var parsed lifiStatusResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("lifi status: %w", err)
	}

	out := &LiFiTransferStatus{
		Status:      parsed.Status,
		DestTxHash:  parsed.Receiving.TxHash,
		DestChainID: parsed.Receiving.ChainID,
		ToAddress:   parsed.Receiving.ToAddress,
	}
	if parsed.Receiving.Amount != "" {
		if v, ok := new(big.Int).SetString(parsed.Receiving.Amount, 10); ok {
			out.ReceivedBaseUnits = v
		}
	}
	return out, nil
}
