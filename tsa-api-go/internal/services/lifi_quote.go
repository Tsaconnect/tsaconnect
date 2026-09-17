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
	"strconv"
	"strings"
	"time"
)

// ErrLiFiNoRoute is returned when LiFi has no executable route for a quote
// request (a 404/422 upstream, or a 200 that omits the fields a route needs).
var ErrLiFiNoRoute = errors.New("no conversion route available")

// LiFiRoute is one executable quote. The treasury signs transactionRequest as
// given: a route is never reconstructed from its parts.
type LiFiRoute struct {
	Tool            string
	ToAmount        *big.Int
	ToAmountMin     *big.Int
	ApprovalAddress string
	// ApprovalToken is the token contract the caller must grant ApprovalAddress
	// an allowance on (action.fromToken.address) — distinct from ApprovalAddress
	// itself, which is the spender. Not in the original brief's field list;
	// added because a converted buy cannot approve a spend without knowing
	// which token to approve.
	ApprovalToken string
	To            string
	Data          string
	Value         string
	GasLimit      uint64
	ETASeconds    int
	// ValidUntil is the unix-seconds deadline (estimate.validUntil) after which
	// this quote is stale and must be re-fetched before signing. Also added
	// beyond the brief's list, for the same reason: a treasury that signs a
	// stale route risks executing at a rate it never agreed to.
	ValidUntil int64
}

// LiFiQuoteRequest is one /quote ask: convert FromAmount of FromToken (on
// FromChainID) into ToToken (on ToChainID), sent by FromAddress and delivered
// to ToAddress.
type LiFiQuoteRequest struct {
	FromChainID int64
	ToChainID   int64
	FromToken   string
	ToToken     string
	FromAddress string
	ToAddress   string
	FromAmount  string // base units
}

// LiFiQuoteClient is the one shared client for LiFi's /quote endpoint. Both
// the AllSwap proxy handler and the converted-buy treasury flow build routes
// through it rather than each hand-rolling their own LiFi HTTP plumbing.
type LiFiQuoteClient struct {
	baseURL    string
	apiKey     string
	integrator string
	http       *http.Client
}

// NewLiFiQuoteClient creates a LiFi quote client. apiKey is forwarded as the
// x-lifi-api-key header on every request (empty = anonymous, rate-limited
// tier); integrator names the caller to LiFi for its own attribution/fee
// accounting.
func NewLiFiQuoteClient(baseURL, apiKey, integrator string) *LiFiQuoteClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://li.quest/v1"
	}
	if strings.TrimSpace(integrator) == "" {
		integrator = "tsa-connect"
	}
	return &LiFiQuoteClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		integrator: integrator,
		http:       &http.Client{Timeout: 20 * time.Second},
	}
}

// NewLiFiGetRequest builds a GET request against a LiFi-compatible base URL,
// attaching the x-lifi-api-key header when apiKey is non-empty. This is the
// one place that builds an authenticated LiFi request: LiFiQuoteClient.Quote
// uses it, and SwapLiFiHandler's raw pass-through endpoints (chains, tokens,
// token, the /quote proxy) delegate to it too, so the header/base-URL
// plumbing that used to be duplicated in the handler exists exactly once.
func NewLiFiGetRequest(ctx context.Context, baseURL, apiKey, pathWithQuery string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+pathWithQuery, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("x-lifi-api-key", apiKey)
	}
	return req, nil
}

// lifiQuoteResponse mirrors LiFi's /quote shape. Field mapping ported from the
// app's getLiFiQuote (tsa-app/services/lifi.ts), which is already correct in
// production: toAmount/toAmountMin/approvalAddress live under estimate, the
// tool name prefers toolDetails.name over the top-level tool, and the
// approval token lives under action.fromToken.address.
type lifiQuoteResponse struct {
	Tool     string `json:"tool"`
	ToolName struct {
		Name string `json:"name"`
	} `json:"toolDetails"`
	Estimate struct {
		ToAmount          string `json:"toAmount"`
		ToAmountMin       string `json:"toAmountMin"`
		ApprovalAddress   string `json:"approvalAddress"`
		ExecutionDuration int    `json:"executionDuration"`
		ValidUntil        int64  `json:"validUntil"`
	} `json:"estimate"`
	Action struct {
		FromToken struct {
			Address string `json:"address"`
		} `json:"fromToken"`
	} `json:"action"`
	TransactionRequest struct {
		To       string `json:"to"`
		Data     string `json:"data"`
		Value    string `json:"value"`
		GasLimit string `json:"gasLimit"`
	} `json:"transactionRequest"`
}

// Quote asks LiFi for one route, priced for immediate execution. order=FASTEST
// and a tight slippage keep stable-to-stable conversions inside the promise; a
// route is never cached, because providers and pools come and go daily.
func (c *LiFiQuoteClient) Quote(ctx context.Context, req LiFiQuoteRequest) (*LiFiRoute, error) {
	q := url.Values{}
	q.Set("fromChain", strconv.FormatInt(req.FromChainID, 10))
	q.Set("toChain", strconv.FormatInt(req.ToChainID, 10))
	q.Set("fromToken", req.FromToken)
	q.Set("toToken", req.ToToken)
	q.Set("fromAddress", req.FromAddress)
	q.Set("toAddress", req.ToAddress)
	q.Set("fromAmount", req.FromAmount)
	q.Set("order", "FASTEST")
	q.Set("slippage", "0.001")
	q.Set("integrator", c.integrator)

	httpReq, err := NewLiFiGetRequest(ctx, c.baseURL, c.apiKey, "/quote?"+q.Encode())
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("lifi quote: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("lifi quote: %w", err)
	}
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusUnprocessableEntity {
		return nil, ErrLiFiNoRoute
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lifi quote: status %d", res.StatusCode)
	}
	var parsed lifiQuoteResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("lifi quote: %w", err)
	}
	toAmount, ok1 := new(big.Int).SetString(parsed.Estimate.ToAmount, 10)
	toMin, ok2 := new(big.Int).SetString(parsed.Estimate.ToAmountMin, 10)
	if !ok1 || !ok2 || parsed.TransactionRequest.To == "" || parsed.TransactionRequest.Data == "" {
		return nil, ErrLiFiNoRoute
	}
	gasLimit, _ := strconv.ParseUint(strings.TrimPrefix(parsed.TransactionRequest.GasLimit, "0x"), 16, 64)

	tool := parsed.ToolName.Name
	if tool == "" {
		tool = parsed.Tool
	}
	if tool == "" {
		tool = "LiFi"
	}

	return &LiFiRoute{
		Tool:            tool,
		ToAmount:        toAmount,
		ToAmountMin:     toMin,
		ApprovalAddress: parsed.Estimate.ApprovalAddress,
		ApprovalToken:   parsed.Action.FromToken.Address,
		To:              parsed.TransactionRequest.To,
		Data:            parsed.TransactionRequest.Data,
		Value:           parsed.TransactionRequest.Value,
		GasLimit:        gasLimit,
		ETASeconds:      parsed.Estimate.ExecutionDuration,
		ValidUntil:      parsed.Estimate.ValidUntil,
	}, nil
}
