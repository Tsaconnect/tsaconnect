package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

const defaultLiFiBaseURL = "https://li.quest/v1"

const (
	lifiDefaultSlippage = "0.005"

	// lifiChainTypes is the VM families we expose to the app: EVM, Solana
	// (SVM) and Bitcoin (UTXO). LiFi defaults to EVM only, so every chain and
	// token call has to name them explicitly or Bitcoin/Solana silently
	// vanish from the picker. Widen this only alongside an app-side signer for
	// the family — a chain the app cannot sign for is a dead route in the UI.
	lifiChainTypes = "EVM,SVM,UTXO"

	allTokensDefaultLimit = 100
	allTokensMaxLimit     = 500
)

// HTTP client for token-list calls. The all-chains LiFi blob is several MB
// and can take ~30s on a cold upstream cache, so we give it a generous
// timeout independent of the standard 10s client.
var liFiHTTPClient = &http.Client{Timeout: 10 * time.Second}
var liFiBulkHTTPClient = &http.Client{Timeout: 60 * time.Second}

type tokenCacheEntry struct {
	data      []byte
	fetchedAt time.Time
}

type chainsCacheEntry struct {
	data      []byte
	fetchedAt time.Time
}

// tokenWithChain is the flat row we hand the FE — every LiFi token with an
// explicit chainId stamped on it so the client never has to walk a nested
// `tokens[chainId]` map.
type tokenWithChain struct {
	Address  string `json:"address"`
	Symbol   string `json:"symbol"`
	Decimals int    `json:"decimals"`
	Name     string `json:"name"`
	LogoURI  string `json:"logoURI,omitempty"`
	PriceUSD string `json:"priceUSD,omitempty"`
	ChainID  int    `json:"chainId"`
}

type allTokensCacheEntry struct {
	flat      []tokenWithChain
	fetchedAt time.Time
}

// SwapLiFiHandler proxies LiFi aggregator endpoints for token lists and swap quotes.
type SwapLiFiHandler struct {
	mu              sync.Mutex
	tokenCache      map[string]tokenCacheEntry
	chainsCache     *chainsCacheEntry
	allTokensCache  *allTokensCacheEntry
	allTokensInFlt  bool
	allTokensWaitCh chan struct{}
	lifiBaseURL     string
	apiKey          string
	integratorID    string
	integratorFee   string

	// Config + FeeService wire the server-verified AllSwap settle (design §8, I8).
	// Optional/nil-safe: when Config is nil the settle endpoint returns "not
	// configured"; the accrual runs whenever FeeService is wired.
	Config     *config.Config
	FeeService *services.FeeDistributionService
	// PriceService values the from-amount when LiFi cannot be re-queried. Without
	// it that path has no USD basis and the fee is deferred rather than guessed.
	PriceService *services.PriceService

	// requeryQuoteUSD re-queries LiFi for the supplied route and returns the
	// from-amount USD value LiFi reports (estimate.fromAmountUSD). Split out as a
	// field so tests can inject a deterministic / unreachable LiFi without HTTP.
	// reachable=false means LiFi could not be reached (→ conservative default).
	requeryQuoteUSD func(p settleQuoteParams) (fromAmountUSD float64, reachable bool)
}

// NewSwapLiFiHandler creates a production SwapLiFiHandler.
//
// `apiKey` is forwarded as the `x-lifi-api-key` header on every upstream
// call (empty = anonymous tier, rate-limited).
//
// `integratorID` + `integratorFee` are only injected on /quote when BOTH
// are non-empty. Collecting an integrator fee requires the matching name
// to be registered on portal.li.fi with a fee wallet configured; quoting
// with an unregistered integrator + fee returns a 400 from LiFi.
func NewSwapLiFiHandler(apiKey, integratorID, integratorFee string) *SwapLiFiHandler {
	h := &SwapLiFiHandler{
		tokenCache:    make(map[string]tokenCacheEntry),
		lifiBaseURL:   defaultLiFiBaseURL,
		apiKey:        apiKey,
		integratorID:  integratorID,
		integratorFee: integratorFee,
	}
	h.requeryQuoteUSD = h.liveRequeryQuoteUSD
	return h
}

// lifiGet performs a GET against the LiFi base URL and forwards the
// configured API key when one is set. Keeps the per-endpoint code free of
// header-plumbing noise.
func (h *SwapLiFiHandler) lifiGet(client *http.Client, path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, h.lifiBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if h.apiKey != "" {
		req.Header.Set("x-lifi-api-key", h.apiKey)
	}
	return client.Do(req)
}

// GetTokens handles GET /api/swap/lifi/tokens
// Proxies LiFi token list with a 24-hour in-memory cache per chainId.
func (h *SwapLiFiHandler) GetTokens(c *gin.Context) {
	chainId := c.Query("chainId")
	if chainId == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "chainId is required")
		return
	}
	if _, err := strconv.ParseUint(chainId, 10, 64); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "chainId must be a positive numeric chain ID")
		return
	}

	h.mu.Lock()
	entry, ok := h.tokenCache[chainId]
	if ok && time.Since(entry.fetchedAt) < 24*time.Hour {
		data := entry.data
		h.mu.Unlock()
		c.Data(http.StatusOK, "application/json", data)
		return
	}
	h.mu.Unlock()

	// Bulk client: a cold-cache per-chain list (Ethereum especially) is several
	// MB and routinely overruns the 10s client, surfacing as "failed to reach
	// LiFi" → the app's "Failed to fetch token list".
	resp, err := h.lifiGet(liFiBulkHTTPClient, fmt.Sprintf("/tokens?chains=%s", chainId))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "failed to reach LiFi")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "failed to read LiFi response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		c.Data(resp.StatusCode, "application/json", body)
		return
	}

	h.mu.Lock()
	h.tokenCache[chainId] = tokenCacheEntry{data: body, fetchedAt: time.Now()}
	h.mu.Unlock()

	c.Data(http.StatusOK, "application/json", body)
}

// GetChains handles GET /api/swap/lifi/chains
// Proxies the LiFi chain list (EVM + Solana + Bitcoin) with a 24-hour
// in-memory cache.
func (h *SwapLiFiHandler) GetChains(c *gin.Context) {
	h.mu.Lock()
	if h.chainsCache != nil && time.Since(h.chainsCache.fetchedAt) < 24*time.Hour {
		data := h.chainsCache.data
		h.mu.Unlock()
		c.Data(http.StatusOK, "application/json", data)
		return
	}
	h.mu.Unlock()

	resp, err := h.lifiGet(liFiHTTPClient, "/chains?chainTypes="+lifiChainTypes)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "failed to reach LiFi")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "failed to read LiFi response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		c.Data(resp.StatusCode, "application/json", body)
		return
	}

	h.mu.Lock()
	h.chainsCache = &chainsCacheEntry{data: body, fetchedAt: time.Now()}
	h.mu.Unlock()

	c.Data(http.StatusOK, "application/json", body)
}

// GetToken handles GET /api/swap/lifi/token?chain=56&token=0xabc
// Looks up a single token by contract address on a given chain. No caching —
// used for custom address entry so results must be fresh.
func (h *SwapLiFiHandler) GetToken(c *gin.Context) {
	chain := c.Query("chain")
	token := c.Query("token")
	if chain == "" || token == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "chain and token are required")
		return
	}

	resp, err := h.lifiGet(liFiHTTPClient, fmt.Sprintf("/token?chain=%s&token=%s", chain, token))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "failed to reach LiFi")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "failed to read LiFi response")
		return
	}

	c.Data(resp.StatusCode, "application/json", body)
}

// GetAllChainTokens handles GET /api/swap/lifi/tokens/all
//
// Returns a paginated, optionally-filtered slice of every token LiFi indexes
// across every supported chain. The full LiFi token blob (~5 MB nested JSON)
// is fetched once and cached in flattened form for 24h; subsequent requests
// are pure in-memory slices.
//
// Query params:
//   - limit  (default 100, max 500)
//   - offset (default 0)
//   - q      (optional, case-insensitive substring match on symbol/name/address)
//   - chains (optional, comma-separated chainId allowlist)
//
// Response: {"tokens": [...], "total": N, "offset": O, "limit": L}
func (h *SwapLiFiHandler) GetAllChainTokens(c *gin.Context) {
	limit := parsePositiveInt(c.Query("limit"), allTokensDefaultLimit)
	if limit > allTokensMaxLimit {
		limit = allTokensMaxLimit
	}
	offset := parsePositiveInt(c.Query("offset"), 0)
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))

	chainsParam := strings.TrimSpace(c.Query("chains"))
	var chainsFilter map[int]struct{}
	if chainsParam != "" {
		chainsFilter = make(map[int]struct{})
		for _, raw := range strings.Split(chainsParam, ",") {
			id, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || id <= 0 {
				continue
			}
			chainsFilter[id] = struct{}{}
		}
	}

	flat, err := h.getOrFetchAllTokens()
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, err.Error())
		return
	}

	// Filter
	filtered := flat
	if chainsFilter != nil || q != "" {
		filtered = make([]tokenWithChain, 0, 1024)
		for i := range flat {
			t := &flat[i]
			if chainsFilter != nil {
				if _, ok := chainsFilter[t.ChainID]; !ok {
					continue
				}
			}
			if q != "" {
				if !strings.Contains(strings.ToLower(t.Symbol), q) &&
					!strings.Contains(strings.ToLower(t.Name), q) &&
					!strings.Contains(strings.ToLower(t.Address), q) {
					continue
				}
			}
			filtered = append(filtered, *t)
		}
	}

	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := filtered[offset:end]

	c.JSON(http.StatusOK, gin.H{
		"tokens": page,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

func parsePositiveInt(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// getOrFetchAllTokens returns the cached flat token list, refreshing from
// LiFi if the cache is empty or older than 24h. A single in-flight guard
// prevents a thundering herd of concurrent upstream fetches when many
// clients hit a cold cache simultaneously.
func (h *SwapLiFiHandler) getOrFetchAllTokens() ([]tokenWithChain, error) {
	h.mu.Lock()
	if h.allTokensCache != nil && time.Since(h.allTokensCache.fetchedAt) < 24*time.Hour {
		flat := h.allTokensCache.flat
		h.mu.Unlock()
		return flat, nil
	}
	// Coalesce concurrent cold-cache refreshes: only one goroutine fetches,
	// the rest wait on the close of allTokensWaitCh.
	if h.allTokensInFlt {
		wait := h.allTokensWaitCh
		h.mu.Unlock()
		<-wait
		h.mu.Lock()
		flat := []tokenWithChain(nil)
		if h.allTokensCache != nil {
			flat = h.allTokensCache.flat
		}
		h.mu.Unlock()
		if flat == nil {
			return nil, fmt.Errorf("upstream fetch failed")
		}
		return flat, nil
	}
	h.allTokensInFlt = true
	h.allTokensWaitCh = make(chan struct{})
	wait := h.allTokensWaitCh
	h.mu.Unlock()

	// Always close the wait channel + clear inflight flag so blocked callers wake.
	defer func() {
		h.mu.Lock()
		h.allTokensInFlt = false
		close(wait)
		h.mu.Unlock()
	}()

	flat, err := h.fetchAndFlattenAllTokens()
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	h.allTokensCache = &allTokensCacheEntry{flat: flat, fetchedAt: time.Now()}
	h.mu.Unlock()
	return flat, nil
}

// fetchAndFlattenAllTokens hits LiFi's /tokens (no `chains` param = every
// supported chain) and flattens the nested map into a sorted []tokenWithChain.
// Sort by symbol then chainId so paginated results are deterministic across
// requests.
func (h *SwapLiFiHandler) fetchAndFlattenAllTokens() ([]tokenWithChain, error) {
	// LiFi's /tokens defaults to chainTypes=EVM — Solana's SPL tokens and
	// Bitcoin only arrive when SVM/UTXO are requested explicitly, so the
	// all-chains search can surface them.
	resp, err := h.lifiGet(liFiBulkHTTPClient, "/tokens?chainTypes="+lifiChainTypes)
	if err != nil {
		return nil, fmt.Errorf("failed to reach LiFi")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read LiFi response")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LiFi returned status %d", resp.StatusCode)
	}

	var parsed struct {
		Tokens map[string][]struct {
			Address  string          `json:"address"`
			Symbol   string          `json:"symbol"`
			Decimals int             `json:"decimals"`
			Name     string          `json:"name"`
			LogoURI  string          `json:"logoURI"`
			PriceUSD json.RawMessage `json:"priceUSD"`
			ChainID  int             `json:"chainId"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse LiFi response")
	}

	flat := make([]tokenWithChain, 0, 8192)
	for chainKey, list := range parsed.Tokens {
		chainID, err := strconv.Atoi(chainKey)
		if err != nil {
			continue
		}
		for _, t := range list {
			id := t.ChainID
			if id == 0 {
				id = chainID
			}
			priceUSD := ""
			if len(t.PriceUSD) > 0 {
				// LiFi sometimes ships priceUSD as a JSON string, sometimes as a
				// number; normalize to string for the FE.
				s := string(t.PriceUSD)
				s = strings.TrimSpace(s)
				s = strings.Trim(s, "\"")
				if s != "null" {
					priceUSD = s
				}
			}
			flat = append(flat, tokenWithChain{
				Address:  t.Address,
				Symbol:   t.Symbol,
				Decimals: t.Decimals,
				Name:     t.Name,
				LogoURI:  t.LogoURI,
				PriceUSD: priceUSD,
				ChainID:  id,
			})
		}
	}

	sort.SliceStable(flat, func(i, j int) bool {
		if flat[i].Symbol != flat[j].Symbol {
			return strings.ToLower(flat[i].Symbol) < strings.ToLower(flat[j].Symbol)
		}
		return flat[i].ChainID < flat[j].ChainID
	})
	return flat, nil
}

// GetQuote handles GET /api/swap/lifi/quote
//
// Forwards query params to LiFi, defaulting slippage to 0.005 if not
// supplied. The integrator/fee combo is *only* injected when both
// LIFI_INTEGRATOR_ID and LIFI_INTEGRATOR_FEE are configured — sending
// either against an unregistered integrator at portal.li.fi makes LiFi
// return a 400, so leaving both empty is the correct path when fee
// collection isn't set up yet.
func (h *SwapLiFiHandler) GetQuote(c *gin.Context) {
	params := c.Request.URL.Query()
	if params.Get("slippage") == "" {
		params.Set("slippage", lifiDefaultSlippage)
	}
	if h.integratorID != "" && h.integratorFee != "" {
		params.Set("fee", h.integratorFee)
		params.Set("integrator", h.integratorID)
	} else {
		// Strip any caller-supplied versions so we never leak an
		// unregistered integrator string into the upstream request.
		params.Del("fee")
		params.Del("integrator")
	}

	resp, err := h.lifiGet(liFiHTTPClient, "/quote?"+params.Encode())
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "failed to reach LiFi")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "failed to read LiFi response")
		return
	}

	c.Data(resp.StatusCode, "application/json", body)
}

// ── AllSwap server-verified settle (design §8, I8) ──────────────────────────

// settleQuoteParams is the route the client claims it swapped on, used to
// re-query LiFi server-side.
type settleQuoteParams struct {
	FromChain   string
	ToChain     string
	FromToken   string
	ToToken     string
	FromAmount  string
	FromAddress string
	ToAddress   string
}

// allSwapSettleRequest is the body of POST /api/swap/lifi/settle. The client
// supplies the route + amounts + (optionally) the fee it believes it paid; the
// backend NEVER trusts ClientFeeUSD — it re-queries LiFi and accrues the VERIFIED
// fee, rejecting a forged ClientFeeUSD that deviates too far.
type allSwapSettleRequest struct {
	QuoteID      string  `json:"quoteId" binding:"required"`
	FromToken    string  `json:"fromToken" binding:"required"`
	ToToken      string  `json:"toToken" binding:"required"`
	FromAmount   string  `json:"fromAmount" binding:"required"`
	ToAmount     string  `json:"toAmount"`
	ChainID      int64   `json:"chainId"`
	ToChainID    int64   `json:"toChainId"`
	ClientFeeUSD float64 `json:"clientFeeUsd"`
	// FromSymbol labels the fee event's settlement token. FromToken is a
	// contract address (what LiFi resolves most reliably on the re-query), and
	// an address in this field would reach SP5 payout resolution as the token
	// symbol. Optional: falls back to FromToken when absent.
	FromSymbol string `json:"fromSymbol"`
	// Feature is the fee bucket to book against: "allswap" (cross-chain, the
	// default) or "spot" (the same-chain trading desk). Both are configured at
	// the same rate, but they are reported separately, so a spot trade booked
	// as allswap is a wrong line in the P&L, not a rounding difference.
	Feature string `json:"feature"`
	// FromAddress/ToAddress are the route's endpoints. LiFi REQUIRES fromAddress
	// on /quote — without it the re-query is a 400, which used to mean every
	// settle silently took the unreachable path — and UTXO routes need it to be
	// an address holding coins, since the quote is built from its UTXOs.
	FromAddress string `json:"fromAddress"`
	ToAddress   string `json:"toAddress"`
	// FromDecimals converts FromAmount (base units) to whole tokens for the
	// price-service fallback. Without it there is no way back to a USD figure.
	FromDecimals int `json:"fromDecimals"`
}

// settleFeature validates the requested fee bucket. Only the two swap features
// may be booked here; anything else (a typo, or a client reaching for a bucket
// this endpoint has no business writing to) falls back to allswap.
func settleFeature(requested string) string {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case models.FeatureSpot:
		return models.FeatureSpot
	default:
		return models.FeatureAllswap
	}
}

// fromAmountUSDFromPrices values the from-amount in dollars using our own
// price service, for when LiFi cannot be re-queried. Reports false when the
// amount cannot be converted to whole tokens or the symbol has no price — a
// missing basis has to stay missing, because the only alternative is inventing
// one and every downstream number (fee splits, TP, rewards) inherits it.
func (h *SwapLiFiHandler) fromAmountUSDFromPrices(req allSwapSettleRequest) (float64, bool) {
	if h.PriceService == nil || req.FromDecimals <= 0 {
		return 0, false
	}
	symbol := settlementSymbol(req)
	if symbol == "" || strings.HasPrefix(symbol, "0X") {
		return 0, false // an address is not a symbol the price service knows
	}
	base := parseFloatOr0(req.FromAmount)
	if base <= 0 {
		return 0, false
	}
	whole := base / math.Pow(10, float64(req.FromDecimals))
	prices, err := h.PriceService.GetPrices([]string{symbol})
	if err != nil {
		return 0, false
	}
	price, ok := prices[symbol]
	if !ok || price.USD <= 0 {
		return 0, false
	}
	return whole * price.USD, true
}

// settlementSymbol is the token label for the fee event: the caller's symbol
// when it sent one, else whatever it used to name the token.
func settlementSymbol(req allSwapSettleRequest) string {
	if s := strings.TrimSpace(req.FromSymbol); s != "" {
		return strings.ToUpper(s)
	}
	return strings.ToUpper(req.FromToken)
}

// SettleAllSwap handles POST /api/swap/lifi/settle (authenticated user — NOT
// super-admin). The backend RE-QUERIES LiFi for the supplied route, recomputes
// the integrator-fee USD, then (design §8, I8):
//
//   - 422 if the client-supplied fee deviates by more than the configured
//     threshold (default 10%) from the re-queried fee (anti-forgery — a client
//     must never be able to shrink the fee routed to the v2 pool).
//   - if LiFi is unreachable, it does NOT charge 0 and does NOT trust the client:
//     it charges a conservative configured default (default 3.5% vs nominal 3%)
//     and emits a warning metric.
//   - accrues a FeeEvent{Feature: allswap, SourceType: "allswap", SourceID:
//     quoteId} with the VERIFIED fee (never the client value), gated on the flag.
//     Idempotent on (allswap, quoteId).
func (h *SwapLiFiHandler) SettleAllSwap(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if h.Config == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "AllSwap settle not configured")
		return
	}

	var req allSwapSettleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "quoteId, fromToken, toToken and fromAmount are required")
		return
	}

	// Re-query LiFi for the from-amount USD on the supplied route. The integrator
	// fee is a percentage of the from-amount value; the verified fee is
	// fromAmountUSD × the integrator-fee rate.
	fromAmountUSD, reachable := h.requeryQuoteUSD(settleQuoteParams{
		FromChain:   strconv.FormatInt(req.ChainID, 10),
		ToChain:     strconv.FormatInt(orChain(req.ToChainID, req.ChainID), 10),
		FromToken:   req.FromToken,
		ToToken:     req.ToToken,
		FromAmount:  req.FromAmount,
		FromAddress: req.FromAddress,
		ToAddress:   req.ToAddress,
	})

	var verifiedFeeUSD float64
	if reachable {
		// Verified fee = fromAmountUSD × nominal integrator rate (bps).
		feeRate := float64(h.Config.AllSwapNominalFeeBpsOrDefault()) / 10000.0
		verifiedFeeUSD = fromAmountUSD * feeRate

		// Anti-forgery: reject a client-supplied fee that deviates beyond the
		// threshold from the verified figure. A zero/absent ClientFeeUSD is the
		// "client made no claim" case — we still accrue the verified fee, no reject.
		if req.ClientFeeUSD > 0 && verifiedFeeUSD > 0 {
			dev := math.Abs(req.ClientFeeUSD-verifiedFeeUSD) / verifiedFeeUSD
			if dev > h.Config.AllSwapFeeDeviationMaxOrDefault() {
				utils.ErrorResponse(c, http.StatusUnprocessableEntity, fmt.Sprintf(
					"fee deviation %.1f%% exceeds the %.1f%% threshold (client=%.6f verified=%.6f)",
					dev*100, h.Config.AllSwapFeeDeviationMaxOrDefault()*100, req.ClientFeeUSD, verifiedFeeUSD))
				return
			}
		}
	} else {
		// LiFi unreachable: do NOT trust the client, and do NOT invent a number.
		// FromAmount is in BASE UNITS — reading it as dollars turns a 1,000 USDC
		// swap into a $35,000,000 fee — so the conservative charge is applied to
		// a USD basis priced here, from our own price service.
		consRate := float64(h.Config.AllSwapConservativeFeeBpsOrDefault()) / 10000.0
		basisUSD, priced := h.fromAmountUSDFromPrices(req)
		if !priced {
			// No trustworthy basis exists. Accruing a guess would corrupt the fee
			// ledger, the TP pool and every reward computed from them, so the fee
			// is left unrecorded and flagged for reconciliation instead.
			log.Printf("[allswap] ALERT allswap_fee_unpriced_total=1 quote=%s feature=%s token=%s "+
				"amount=%s decimals=%d — LiFi unreachable AND no price; NO fee accrued, reconcile manually",
				req.QuoteID, settleFeature(req.Feature), settlementSymbol(req), req.FromAmount, req.FromDecimals)
			utils.SuccessResponse(c, http.StatusOK, "Swap fee deferred", gin.H{
				"quoteId":        req.QuoteID,
				"verifiedFeeUsd": 0.0,
				"lifiReachable":  false,
				"deferred":       true,
			})
			return
		}
		verifiedFeeUSD = basisUSD * consRate
		log.Printf("[allswap] ALERT allswap_lifi_unreachable_total=1 LiFi unreachable for quote %s — "+
			"charging conservative %d bps of a priced $%.2f basis (=$%.6f), NOT the client value",
			req.QuoteID, h.Config.AllSwapConservativeFeeBpsOrDefault(), basisUSD, verifiedFeeUSD)
	}

	// Idempotency key (allswap, quoteId). The quoteId is a string; derive a
	// deterministic UUID so a repeat settle for the same quote is a guard-row
	// no-op in the engine.
	sourceID := deterministicUUID("allswap:" + req.QuoteID)

	// Accrue the VERIFIED fee (never the client value).
	feature := settleFeature(req.Feature)
	if h.FeeService != nil && verifiedFeeUSD > 0 {
		h.FeeService.EnqueueFeeSettled(services.FeeEvent{
			Feature:    feature,
			Token:      settlementSymbol(req),
			FeeUSD:     verifiedFeeUSD,
			UserID:     user.ID,
			SourceType: "allswap",
			SourceID:   sourceID,
		})
	}

	utils.SuccessResponse(c, http.StatusOK, "Swap fee settled", gin.H{
		"quoteId":        req.QuoteID,
		"verifiedFeeUsd": verifiedFeeUSD,
		"lifiReachable":  reachable,
		"feature":        feature,
	})
}

// orChain returns primary when non-zero, else fallback (same-chain swaps omit
// toChainId).
func orChain(primary, fallback int64) int64 {
	if primary != 0 {
		return primary
	}
	return fallback
}

// parseFloatOr0 parses a decimal string, returning 0 on error.
func parseFloatOr0(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// deterministicUUID derives a stable UUIDv5 from a key so (allswap, quoteId)
// maps to the same SourceID across retries — the engine's idempotency guard.
func deterministicUUID(key string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(key))
}

// liveRequeryQuoteUSD re-queries LiFi /quote for the supplied route and returns
// the from-amount USD (estimate.fromAmountUSD). reachable=false on any transport
// or decode failure so the caller falls back to the conservative default.
func (h *SwapLiFiHandler) liveRequeryQuoteUSD(p settleQuoteParams) (float64, bool) {
	// fromAddress is REQUIRED by LiFi ("querystring must have required property
	// 'fromAddress'"), and a UTXO route additionally needs it to hold coins,
	// since the quote is built from that address's UTXOs. Omitting it made every
	// re-query a 400 — i.e. every settle silently took the unreachable path.
	q := fmt.Sprintf("/quote?fromChain=%s&toChain=%s&fromToken=%s&toToken=%s&fromAmount=%s&slippage=%s",
		p.FromChain, p.ToChain, p.FromToken, p.ToToken, p.FromAmount, lifiDefaultSlippage)
	if p.FromAddress != "" {
		q += "&fromAddress=" + url.QueryEscape(p.FromAddress)
	}
	if p.ToAddress != "" {
		q += "&toAddress=" + url.QueryEscape(p.ToAddress)
	}
	if h.integratorID != "" && h.integratorFee != "" {
		q += "&fee=" + h.integratorFee + "&integrator=" + h.integratorID
	}

	resp, err := h.lifiGet(liFiHTTPClient, q)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, false
	}
	var parsed struct {
		Estimate struct {
			FromAmountUSD string `json:"fromAmountUSD"`
		} `json:"estimate"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, false
	}
	usd := parseFloatOr0(parsed.Estimate.FromAmountUSD)
	// A 200 with a malformed/empty/zero fromAmountUSD must NOT yield (0, true):
	// that would silently route a verifiedFeeUSD=0 down the "reachable" path and
	// accrue a $0 fee. Treat it as unreachable so the caller applies the
	// conservative configured default instead (design §8, I8 — never silent $0).
	if usd <= 0 {
		return 0, false
	}
	return usd, true
}
