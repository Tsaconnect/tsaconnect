package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
)

// ErrLiFiNoRoute is returned when LiFi has no executable route for a quote
// request (a 404/422 upstream, or a 200 that omits the fields a route needs).
var ErrLiFiNoRoute = errors.New("no conversion route available")

// ErrLiFiFeeUnconfigured is returned when LiFi rejects the request because our
// integrator has no fee wallet set up for the chain — distinct from
// ErrLiFiNoRoute, which is a routing failure rather than a portal-config gap.
var ErrLiFiFeeUnconfigured = errors.New("lifi: integrator fee not configured on this chain")

// LiFiRoute is one executable quote. The treasury signs transactionRequest as
// given: a route is never reconstructed from its parts.
type LiFiRoute struct {
	Tool string
	// FromAmount is what the quote spends (action.fromAmount, base units).
	// For an exact-output quote it is the answer, not the ask.
	FromAmount      string
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
	// FromAmountUSD, ToAmountUSD and GasCostUSD price the route in dollars, so
	// its cost can be compared without knowing either token's own price.
	FromAmountUSD float64
	ToAmountUSD   float64
	GasCostUSD    float64
	// GasCostWei is the same gas cost in the source chain's native units —
	// what a sell's top-up would actually have to send, and therefore what a
	// wallet's native balance has to be compared against. The USD figure
	// cannot answer that question: it prices the gas, not the coin.
	GasCostWei *big.Int
	// TxGasPriceWei is the price the transaction is signed at (maxFeePerGas,
	// else gasPrice). With GasLimit it is what a node reserves from her
	// balance before accepting the tx — more than GasCostWei, an estimate of
	// what the tx will use. nil when LiFi sent neither.
	TxGasPriceWei *big.Int
	// ToTokenDecimals and ToTokenSymbol come from LiFi's own action.toToken,
	// never the caller: the server must not trust the client for either.
	ToTokenDecimals int
	ToTokenSymbol   string
	// TransactionID is the calldata's bytes32 transactionId (0x-hex), when
	// decodable. LiFi's status API reports the same id, which is how a user's
	// swap is bound back to the quote that produced it.
	TransactionID string
	// Raw is the whole quote response body, kept for fields a later task needs
	// without growing this struct field-by-field again.
	Raw json.RawMessage
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
	// ToAmount asks for an EXACT output instead (base units of ToToken):
	// LiFi then works out what has to be spent. Exactly one of FromAmount and
	// ToAmount may be set.
	ToAmount string
	// FeeBps is the integrator fee in basis points, sent to LiFi as a fraction
	// (e.g. 25 -> "0.0025"). 0 omits the fee param entirely.
	FeeBps int
	// Slippage is the max acceptable slippage as a fraction (e.g. 0.005 for
	// 0.5%). 0 defaults to 0.001, the stable-to-stable-safe value Quote used
	// to hardcode.
	Slippage float64
	// AllowDestinationCall opts into routes that hand funds to a contract call
	// on arrival. False (the default) is sent explicitly as
	// allowDestinationCall=false, since a destination call can forward funds
	// anywhere the checked receiver does not name.
	AllowDestinationCall bool
	// Order is LiFi's route-selection strategy ("FASTEST" or "CHEAPEST").
	// Empty defaults to FASTEST. A caller that executes immediately with no
	// human signing delay (the treasury's own bridging) can ask for CHEAPEST
	// without risking a route going stale before it is used; a caller whose
	// route is handed to a human to sign later should leave this unset.
	Order string
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
		FromAmountUSD     string `json:"fromAmountUSD"`
		ToAmountUSD       string `json:"toAmountUSD"`
		GasCosts          []struct {
			Amount    string `json:"amount"`
			AmountUSD string `json:"amountUSD"`
		} `json:"gasCosts"`
	} `json:"estimate"`
	Action struct {
		FromAmount string `json:"fromAmount"`
		FromToken  struct {
			Address string `json:"address"`
		} `json:"fromToken"`
		ToToken struct {
			Address  string `json:"address"`
			Decimals int    `json:"decimals"`
			Symbol   string `json:"symbol"`
		} `json:"toToken"`
	} `json:"action"`
	TransactionRequest struct {
		To           string `json:"to"`
		Data         string `json:"data"`
		Value        string `json:"value"`
		GasLimit     string `json:"gasLimit"`
		GasPrice     string `json:"gasPrice"`
		MaxFeePerGas string `json:"maxFeePerGas"`
	} `json:"transactionRequest"`
}

// Quote asks LiFi for one route, priced for immediate execution. order
// defaults to FASTEST — paired with a tight slippage, that keeps
// stable-to-stable conversions inside the promise for a route a human signs
// later. A caller with no such delay may set req.Order to CHEAPEST instead. A
// route is never cached, because providers and pools come and go daily.
//
// With req.ToAmount set the ask is exact-output (GET /quote/toAmount): the
// route delivers that much and reports what it costs in route.FromAmount.
func (c *LiFiQuoteClient) Quote(ctx context.Context, req LiFiQuoteRequest) (*LiFiRoute, error) {
	if req.FromAmount != "" && req.ToAmount != "" {
		return nil, errors.New("lifi quote: fromAmount and toAmount are mutually exclusive")
	}
	path := "/quote"
	q := url.Values{}
	q.Set("fromChain", strconv.FormatInt(req.FromChainID, 10))
	q.Set("toChain", strconv.FormatInt(req.ToChainID, 10))
	q.Set("fromToken", req.FromToken)
	q.Set("toToken", req.ToToken)
	q.Set("fromAddress", req.FromAddress)
	q.Set("toAddress", req.ToAddress)
	if req.ToAmount != "" {
		path = "/quote/toAmount"
		q.Set("toAmount", req.ToAmount)
	} else {
		q.Set("fromAmount", req.FromAmount)
	}
	order := req.Order
	if order == "" {
		order = "FASTEST"
	}
	q.Set("order", order)
	slippage := req.Slippage
	if slippage <= 0 {
		slippage = 0.001
	}
	q.Set("slippage", strconv.FormatFloat(slippage, 'f', -1, 64))
	if !req.AllowDestinationCall {
		q.Set("allowDestinationCall", "false")
	}
	if req.FeeBps > 0 {
		q.Set("fee", strconv.FormatFloat(float64(req.FeeBps)/10_000, 'f', -1, 64))
	}
	q.Set("integrator", c.integrator)

	body, status, err := c.get(ctx, path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("lifi quote: %w", err)
	}
	if status != http.StatusOK {
		if IsIntegratorFeeUnconfigured(body) {
			return nil, ErrLiFiFeeUnconfigured
		}
		if status == http.StatusNotFound || status == http.StatusUnprocessableEntity {
			return nil, ErrLiFiNoRoute
		}
		return nil, fmt.Errorf("lifi quote: status %d", status)
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

	route := &LiFiRoute{
		Tool:            tool,
		FromAmount:      parsed.Action.FromAmount,
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
	}
	route.Raw = json.RawMessage(body)
	route.TxGasPriceWei = lifiTxGasPrice(parsed.TransactionRequest.MaxFeePerGas, parsed.TransactionRequest.GasPrice)
	route.ToTokenDecimals = parsed.Action.ToToken.Decimals
	route.ToTokenSymbol = parsed.Action.ToToken.Symbol
	// Every USD figure prices a budget, so one that is not a finite number
	// ("NaN", "Inf" — both parse) makes the route unpriceable: NaN slips past
	// every "<= 0" guard and would price the route as free.
	var finite bool
	if route.FromAmountUSD, finite = lifiUSD(parsed.Estimate.FromAmountUSD); !finite {
		return nil, fmt.Errorf("%w: fromAmountUSD %q is not a number", ErrLiFiNoRoute, parsed.Estimate.FromAmountUSD)
	}
	if route.ToAmountUSD, finite = lifiUSD(parsed.Estimate.ToAmountUSD); !finite {
		return nil, fmt.Errorf("%w: toAmountUSD %q is not a number", ErrLiFiNoRoute, parsed.Estimate.ToAmountUSD)
	}
	for _, g := range parsed.Estimate.GasCosts {
		v, finite := lifiUSD(g.AmountUSD)
		if !finite {
			return nil, fmt.Errorf("%w: gas amountUSD %q is not a number", ErrLiFiNoRoute, g.AmountUSD)
		}
		route.GasCostUSD += v
		if wei, ok := new(big.Int).SetString(strings.TrimSpace(g.Amount), 10); ok {
			if route.GasCostWei == nil {
				route.GasCostWei = new(big.Int)
			}
			route.GasCostWei.Add(route.GasCostWei, wei)
		}
	}
	if data, derr := hexutil.Decode(route.Data); derr == nil {
		if id, ok := lifiTransactionID(data); ok {
			route.TransactionID = id
		}
	}
	return route, nil
}

// get is the one authenticated GET this client makes: the body (capped at
// 1 MiB) and the status, for the caller to interpret.
func (c *LiFiQuoteClient) get(ctx context.Context, pathWithQuery string) ([]byte, int, error) {
	req, err := NewLiFiGetRequest(ctx, c.baseURL, c.apiKey, pathWithQuery)
	if err != nil {
		return nil, 0, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, 0, err
	}
	return body, res.StatusCode, nil
}

// TokenPriceUSD is LiFi's own USD price for one token, as an exact rational,
// with the token's decimals. It sizes what a merchant is owed in a token, so a
// price that is missing, unparsable or not positive is an error: a zero would
// price the token as free and owe the merchant nothing (or, inverted, an
// unbounded amount).
func (c *LiFiQuoteClient) TokenPriceUSD(ctx context.Context, chainID int64, token string) (*big.Rat, int, error) {
	q := url.Values{}
	q.Set("chain", strconv.FormatInt(chainID, 10))
	q.Set("token", token)
	body, status, err := c.get(ctx, "/token?"+q.Encode())
	if err != nil {
		return nil, 0, fmt.Errorf("lifi token: %w", err)
	}
	if status != http.StatusOK {
		return nil, 0, fmt.Errorf("lifi token: status %d", status)
	}
	var parsed struct {
		PriceUSD string `json:"priceUSD"`
		Decimals int    `json:"decimals"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, 0, fmt.Errorf("lifi token: %w", err)
	}
	price, ok := new(big.Rat).SetString(strings.TrimSpace(parsed.PriceUSD))
	if !ok || price.Sign() <= 0 {
		return nil, 0, fmt.Errorf("lifi token: unusable price %q", parsed.PriceUSD)
	}
	if parsed.Decimals <= 0 || parsed.Decimals > 36 {
		return nil, 0, fmt.Errorf("lifi token: unusable decimals %d", parsed.Decimals)
	}
	return price, parsed.Decimals, nil
}

// lifiUSD reads one of LiFi's USD strings. An empty or unparseable one reads
// as 0, as it always has — LiFiCostBps already treats a zero input or output
// as an unknown cost. finite is false for a value that parsed to NaN or ±Inf,
// or that overflowed float64 — ParseFloat returns (±Inf, ErrRange) for the
// latter, not a plain parse failure, so it must not fall through to the
// unparseable-string default of 0: a gas figure this large would otherwise
// price the treasury's gas as free.
func lifiUSD(s string) (v float64, finite bool) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return 0, false
		}
		return 0, true
	}
	return v, !math.IsNaN(v) && !math.IsInf(v, 0)
}

// IsIntegratorFeeUnconfigured spots LiFi's "not configured for collecting fees
// on chain X" rejection, which is about our portal setup rather than the
// route. Exported so both this client and SwapLiFiHandler's raw /quote proxy
// — which must keep behaving exactly as before — share one detection.
func IsIntegratorFeeUnconfigured(body []byte) bool {
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	msg := strings.ToLower(parsed.Message)
	return strings.Contains(msg, "not configured for collecting fees")
}

// lifiTxGasPrice is the first positive hex price of maxFee, gasPrice, or nil.
func lifiTxGasPrice(maxFee, gasPrice string) *big.Int {
	for _, v := range []string{maxFee, gasPrice} {
		if p, err := hexutil.DecodeBig(strings.TrimSpace(v)); err == nil && p.Sign() > 0 {
			return p
		}
	}
	return nil
}
