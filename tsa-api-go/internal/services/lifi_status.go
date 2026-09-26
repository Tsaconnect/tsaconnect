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

// LiFi substatuses. DONE is not on its own a statement that the recipient was
// paid in full: LiFi also reports DONE for a partial fill and for a transfer
// it bounced back to the sender. Only COMPLETED means "delivered as quoted".
const (
	LiFiSubstatusCompleted = "COMPLETED"
	LiFiSubstatusPartial   = "PARTIAL"
	LiFiSubstatusRefunded  = "REFUNDED"
)

// LiFiTransferStatus is the outcome of one bridged transfer.
//
// ReceivedBaseUnits is what actually landed at the destination — a bridge
// delivers less than was sent, and the payout is computed from this figure, not
// from the quoted amount. Treat it as a lead, not as proof: the destination tx
// is verified independently on-chain before any money moves.
type LiFiTransferStatus struct {
	Status string
	// Substatus qualifies Status. A DONE carries COMPLETED, PARTIAL or
	// REFUNDED, and only COMPLETED means the recipient got what was quoted.
	Substatus         string
	DestTxHash        string
	DestChainID       int64
	ReceivedBaseUnits *big.Int
	ToAddress         string
	// ReceivingToken is the token address actually delivered. A native coin
	// leaves no ERC-20 Transfer log to read, so on that one path this is the
	// only thing that says the recipient got the asset they asked for rather
	// than something the bridge substituted.
	ReceivingToken string
	// The sending side: what left, from where. Not used by the converted-buy
	// landing check (which only cares what arrived) — these bind a user's own
	// swap back to the transaction that funded it.
	SendingChainID int64
	SendingToken   string
	// SendingAmount is nil when LiFi omitted it or wrote something
	// unparseable: "nothing was sent" is a claim, and there is no evidence
	// for it here.
	SendingAmount *big.Int
	FromAddress   string
	TransactionID string
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
	Status        string `json:"status"`
	Substatus     string `json:"substatus"`
	TransactionID string `json:"transactionId"`
	FromAddress   string `json:"fromAddress"`
	// ToAddress is where LiFi actually reports the recipient. Observed live
	// on 2026-09-23 across lifiIntents, mayan, relaydepository, nordstern and
	// across: receiving.toAddress came back NULL on every one of them, and
	// this top-level field carried the address. Reading only the nested one
	// left every landing looking like "recipient unknown".
	ToAddress string `json:"toAddress"`
	Sending   struct {
		ChainID int64  `json:"chainId"`
		Amount  string `json:"amount"`
		Token   struct {
			Address string `json:"address"`
		} `json:"token"`
	} `json:"sending"`
	Receiving struct {
		TxHash    string `json:"txHash"`
		ChainID   int64  `json:"chainId"`
		Amount    string `json:"amount"`
		ToAddress string `json:"toAddress"`
		Token     struct {
			Address string `json:"address"`
		} `json:"token"`
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
		Status:         parsed.Status,
		Substatus:      parsed.Substatus,
		DestTxHash:     parsed.Receiving.TxHash,
		DestChainID:    parsed.Receiving.ChainID,
		ToAddress:      firstNonEmpty(parsed.Receiving.ToAddress, parsed.ToAddress),
		ReceivingToken: parsed.Receiving.Token.Address,
		SendingChainID: parsed.Sending.ChainID,
		SendingToken:   parsed.Sending.Token.Address,
		FromAddress:    parsed.FromAddress,
		TransactionID:  parsed.TransactionID,
	}
	if parsed.Receiving.Amount != "" {
		if v, ok := new(big.Int).SetString(parsed.Receiving.Amount, 10); ok {
			out.ReceivedBaseUnits = v
		}
	}
	if parsed.Sending.Amount != "" {
		if v, ok := new(big.Int).SetString(parsed.Sending.Amount, 10); ok {
			out.SendingAmount = v
		}
	}
	return out, nil
}
