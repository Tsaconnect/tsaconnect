package handlers

import (
	"context"
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

const (
	lifiDefaultSlippage = "0.005"

	// lifiChainTypes is the VM families we expose to the app: EVM, Solana (SVM),
	// Bitcoin (UTXO) and Sui (MVM). LiFi defaults to EVM only, so every chain
	// and token call has to name them explicitly or the non-EVM chains silently
	// vanish from the picker. Widen this only alongside an app-side signer for
	// the family — a chain the app cannot sign for is a dead route in the UI.
	lifiChainTypes = "EVM,SVM,UTXO,MVM"

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
	// CoinKey is LiFi's id for the same asset across chains ("USDC" on Base
	// and on BSC), so the app can show one coin with several networks.
	CoinKey string `json:"coinKey,omitempty"`
	// VerificationStatus is LiFi's "verified" | "unverified". "flagged" tokens
	// (security-flagged) are dropped before they get here.
	VerificationStatus string `json:"verificationStatus,omitempty"`
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
	// FeeConfigService supplies the admin-managed rate for the feature being
	// booked. Nil-safe: the env constant stands in when it is absent.
	FeeConfigService *services.FeeConfigService
	// StatusClient follows a swap to the destination chain. Nil-safe: the status
	// endpoint reports "not configured" rather than pretending a swap landed.
	StatusClient *services.LiFiStatusClient

	// quoteClient is the shared typed LiFi /quote client (internal/services).
	// Not used by this handler's own raw pass-through endpoints (GetQuote,
	// quoteWithoutFee, liveRequeryQuoteUSD all need untyped/param-level control
	// LiFiQuoteClient.Quote does not expose — see its callers for why) — held
	// here so a converted-buy treasury flow can share the same client rather
	// than building a second one.
	quoteClient *services.LiFiQuoteClient

	// requeryQuoteUSD re-queries LiFi for the supplied route. Split out as a
	// field so tests can inject a deterministic / unreachable LiFi without HTTP.
	requeryQuoteUSD func(p settleQuoteParams) requeryResult
}

// NewSwapLiFiHandler creates a production SwapLiFiHandler.
//
// `baseURL` is resolved through config.ResolveLiFiBaseURL — an empty string
// (the common case: callers pass cfg.LiFiBaseURL, which is already resolved,
// but tests often pass "") defaults to LiFi's public API. This is the one
// place the handler's base URL is decided; lifiGet and quoteClient both
// share it.
//
// `apiKey` is forwarded as the `x-lifi-api-key` header on every upstream
// call (empty = anonymous tier, rate-limited).
//
// `integratorID` + `integratorFee` are only injected on /quote when BOTH
// are non-empty. Collecting an integrator fee requires the matching name
// to be registered on portal.li.fi with a fee wallet configured; quoting
// with an unregistered integrator + fee returns a 400 from LiFi.
func NewSwapLiFiHandler(baseURL, apiKey, integratorID, integratorFee string) *SwapLiFiHandler {
	resolvedBaseURL := config.ResolveLiFiBaseURL(baseURL)
	h := &SwapLiFiHandler{
		tokenCache:    make(map[string]tokenCacheEntry),
		lifiBaseURL:   resolvedBaseURL,
		apiKey:        apiKey,
		integratorID:  integratorID,
		integratorFee: integratorFee,
	}
	h.quoteClient = services.NewLiFiQuoteClient(resolvedBaseURL, apiKey, integratorID)
	h.requeryQuoteUSD = h.liveRequeryQuoteUSD
	return h
}

// lifiGet performs a GET against the LiFi base URL and forwards the
// configured API key when one is set. Keeps the per-endpoint code free of
// header-plumbing noise. The request itself is built by
// services.NewLiFiGetRequest — the same helper the shared LiFiQuoteClient
// uses — so the base-URL/api-key plumbing lives in exactly one place rather
// than being duplicated between the handler and the services client.
func (h *SwapLiFiHandler) lifiGet(client *http.Client, path string) (*http.Response, error) {
	req, err := services.NewLiFiGetRequest(context.Background(), h.lifiBaseURL, h.apiKey, path)
	if err != nil {
		return nil, err
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
// Proxies the LiFi chain list (EVM + Solana + Bitcoin + Sui) with a 24-hour
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

	// Encoded, not spliced: a raw "&…" in either value would add parameters to
	// our request to LiFi.
	resp, err := h.lifiGet(liFiHTTPClient, "/token?"+url.Values{"chain": {chain}, "token": {token}}.Encode())
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

	// A pasted address is how a scam token gets in: never hand back one LiFi
	// has security-flagged, same rule as the all-chains list.
	var status struct {
		VerificationStatus string `json:"verificationStatus"`
	}
	if json.Unmarshal(body, &status) == nil && strings.EqualFold(status.VerificationStatus, "flagged") {
		utils.ErrorResponse(c, http.StatusNotFound, "This token is flagged as unsafe")
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
//   - coinKeys (optional, comma-separated LiFi coinKeys, e.g. USDC,USDT,ETH):
//     only VERIFIED tokens with one of these keys — the picker's default list
//     of major coins in a single request
//   - tokens (optional, comma-separated chainId:address pairs, max 200): only
//     those exact tokens — which of the coins a wallet holds LiFi can swap,
//     answered in one request
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

	var coinKeys map[string]struct{}
	if raw := strings.TrimSpace(c.Query("coinKeys")); raw != "" {
		coinKeys = make(map[string]struct{})
		for _, k := range strings.Split(raw, ",") {
			if k = strings.ToUpper(strings.TrimSpace(k)); k != "" {
				coinKeys[k] = struct{}{}
			}
		}
	}

	pairs := parseTokenPairs(c.Query("tokens"))

	flat, err := h.getOrFetchAllTokens()
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, err.Error())
		return
	}

	// Filter
	filtered := flat
	if chainsFilter != nil || q != "" || coinKeys != nil || pairs != nil {
		filtered = make([]tokenWithChain, 0, 1024)
		for i := range flat {
			t := &flat[i]
			if chainsFilter != nil {
				if _, ok := chainsFilter[t.ChainID]; !ok {
					continue
				}
			}
			if coinKeys != nil {
				if _, ok := coinKeys[strings.ToUpper(t.CoinKey)]; !ok || t.VerificationStatus != "verified" {
					continue
				}
			}
			if pairs != nil {
				if _, ok := pairs[tokenPairKey(t.ChainID, t.Address)]; !ok {
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

	// A text search ranks the real thing first, BEFORE the page is cut: an
	// exact symbol match, then verified, then tokens LiFi groups under a coin.
	// Otherwise a broad query ("usd") fills the page alphabetically with
	// lookalikes and the real coins never make it in.
	if q != "" {
		score := func(t tokenWithChain) int {
			s := 0
			if strings.EqualFold(t.Symbol, q) {
				s += 4
			}
			if t.VerificationStatus == "verified" {
				s += 2
			}
			if t.CoinKey != "" {
				s++
			}
			return s
		}
		sort.SliceStable(filtered, func(i, j int) bool { return score(filtered[i]) > score(filtered[j]) })
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

const maxTokenPairs = 200

func tokenPairKey(chainID int, address string) string {
	return strconv.Itoa(chainID) + ":" + strings.ToLower(address)
}

// parseTokenPairs reads "chainId:address,…" into a set; nil when the param is
// absent. Malformed entries are skipped, and reading stops at maxTokenPairs,
// so a huge parameter is never split whole.
func parseTokenPairs(raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	pairs := make(map[string]struct{})
	for rest := raw; rest != "" && len(pairs) < maxTokenPairs; {
		var entry string
		entry, rest, _ = strings.Cut(rest, ",")
		chain, address, ok := strings.Cut(strings.TrimSpace(entry), ":")
		id, err := strconv.Atoi(chain)
		if !ok || err != nil || id <= 0 || address == "" {
			continue
		}
		pairs[tokenPairKey(id, address)] = struct{}{}
	}
	return pairs
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
			CoinKey  string          `json:"coinKey"`
			Status   string          `json:"verificationStatus"`
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
			// Security-flagged tokens never reach a picker, search included.
			if strings.EqualFold(t.Status, "flagged") {
				continue
			}
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
				CoinKey:  t.CoinKey,
				// Absent on older payloads: treat as unverified, never verified.
				VerificationStatus: strings.ToLower(t.Status),
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

	// Fee wallets are configured per chain at portal.li.fi. A chain we have not
	// set one up for — a newly added one, typically — makes LiFi reject the
	// whole quote, which would take swapping on that chain down entirely rather
	// than merely forgo the fee. Retry once without the fee so the user can
	// still swap, and make the lost revenue loud rather than silent.
	if resp.StatusCode != http.StatusOK && services.IsIntegratorFeeUnconfigured(body) {
		log.Printf("[allswap] ALERT allswap_fee_unconfigured_total=1 integrator %q has no fee wallet for "+
			"chain %s — quoting WITHOUT a fee (this swap earns nothing); configure it at portal.li.fi",
			h.integratorID, params.Get("fromChain"))
		params.Del("fee")
		params.Del("integrator")
		if status, rbody, ok := h.quoteWithoutFee(params); ok {
			c.Data(status, "application/json", rbody)
			return
		}
	}

	c.Data(resp.StatusCode, "application/json", body)
}

// quoteWithoutFee re-asks LiFi for the same route with no integrator fee. Its
// own function so the response body is closed here rather than held open for
// the rest of the request. ok=false leaves the caller to return what it had.
func (h *SwapLiFiHandler) quoteWithoutFee(params url.Values) (status int, body []byte, ok bool) {
	resp, err := h.lifiGet(liFiHTTPClient, "/quote?"+params.Encode())
	if err != nil {
		return 0, nil, false
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, false
	}
	return resp.StatusCode, body, true
}

// GetSwapStatus handles GET /api/swap/lifi/status?txHash=0x…
//
// A cross-chain swap is not finished when its source transaction confirms — the
// bridge still has to deliver on the far side, which takes minutes and can
// fail. Without this the app can only ever say "broadcast" and call it success,
// which is how a user ends up believing a swap completed while the funds are
// still in flight (or refunded).
func (h *SwapLiFiHandler) GetSwapStatus(c *gin.Context) {
	txHash := strings.TrimSpace(c.Query("txHash"))
	if txHash == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "txHash is required")
		return
	}
	if h.StatusClient == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "swap status is not configured")
		return
	}

	// Status reports the outcome in st.Status; its error is transport or decode
	// only. LiFi does not know a transaction for the first seconds after a
	// broadcast, and that is indistinguishable from a transient failure here —
	// so any error reads as "not yet", never as a failed swap.
	st, err := h.StatusClient.Status(c.Request.Context(), txHash)
	if err != nil {
		utils.SuccessResponse(c, http.StatusOK, "pending", gin.H{
			"status": services.LiFiStatusPending, "txHash": txHash,
		})
		return
	}

	out := gin.H{"status": st.Status, "txHash": txHash}
	// DONE alone is not delivery: only COMPLETED is; REFUNDED and PARTIAL
	// are ways a swap ends short, and the app words each one differently.
	if st.Substatus != "" {
		out["substatus"] = st.Substatus
	}
	if st.DestTxHash != "" {
		out["destTxHash"] = st.DestTxHash
	}
	if st.DestChainID != 0 {
		out["destChainId"] = st.DestChainID
	}
	if st.ReceivedBaseUnits != nil {
		out["receivedAmount"] = st.ReceivedBaseUnits.String()
	}
	utils.SuccessResponse(c, http.StatusOK, "ok", out)
}

// ── AllSwap server-verified settle (design §8, I8) ──────────────────────────

// settleQuoteParams is the route the client claims it swapped on, used to
// re-query LiFi server-side.
// requeryResult is what a settle-time re-query of LiFi established.
//
// The three states are genuinely different and collapsing any two of them
// misbooks money:
//
//	Reachable && FeeCollectable   — the route's USD value, book the rate on it
//	!Reachable                    — we learned nothing; price the basis ourselves
//	Reachable && !FeeCollectable  — LiFi answered that it collects NO fee on this
//	                                chain (no fee wallet configured for it), so
//	                                the swap earned nothing and booking a rate
//	                                would invent income
type requeryResult struct {
	FromAmountUSD  float64
	Reachable      bool
	FeeCollectable bool
}

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

// nominalBpsFor is the rate this swap's fee is booked at.
//
// fee_config is the source of truth under the v2 engine — it is what admins
// edit and what the FeeManager reconciler keeps honest — so the env constant is
// only a fallback for when the config service is absent or has no row. Reading
// the env value first is how the ledger silently keeps booking 3% after someone
// changes the rate in the admin console.
func (h *SwapLiFiHandler) nominalBpsFor(feature, tokenSymbol string, chainID int64) int {
	if h.FeeConfigService != nil {
		// Rates are configurable per chain as well as per token — passing nil
		// here would silently ignore a chain-specific rate an admin had set,
		// which is the very drift this function exists to remove.
		var chain *int64
		if chainID != 0 {
			chain = &chainID
		}
		if bps, _, exempt, found := h.FeeConfigService.RateFor(feature, tokenSymbol, chain); found {
			if exempt {
				return 0
			}
			if bps > 0 {
				return bps
			}
		}
	}
	return h.Config.AllSwapNominalFeeBpsOrDefault()
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
	requery := h.requeryQuoteUSD(settleQuoteParams{
		FromChain:   strconv.FormatInt(req.ChainID, 10),
		ToChain:     strconv.FormatInt(orChain(req.ToChainID, req.ChainID), 10),
		FromToken:   req.FromToken,
		ToToken:     req.ToToken,
		FromAmount:  req.FromAmount,
		FromAddress: req.FromAddress,
		ToAddress:   req.ToAddress,
	})

	feature := settleFeature(req.Feature)
	nominalBps := h.nominalBpsFor(feature, settlementSymbol(req), req.ChainID)

	// LiFi collects per chain, and a chain with no fee wallet configured earns
	// nothing — GetQuote re-quotes those fee-less so the swap still works. There
	// is no fee to mirror, so booking the rate anyway would invent income, which
	// is the whole failure this endpoint exists to avoid. The debt is cleared:
	// nothing is owed, and the sweeper must not revisit it.
	if requery.Reachable && !requery.FeeCollectable {
		log.Printf("[allswap] ALERT allswap_fee_uncollected_total=1 quote=%s feature=%s chain=%d — "+
			"LiFi collects no fee on this chain, so NOTHING was earned and nothing is booked; "+
			"configure a fee wallet at portal.li.fi to start earning here",
			req.QuoteID, feature, req.ChainID)
		markPendingSwapFeeSettled(req.QuoteID)
		utils.SuccessResponse(c, http.StatusOK, "No fee collected on this route", gin.H{
			"quoteId":        req.QuoteID,
			"verifiedFeeUsd": 0.0,
			"lifiReachable":  true,
			"feeCollected":   false,
		})
		return
	}

	var verifiedFeeUSD float64
	if requery.Reachable {
		// Verified fee = fromAmountUSD × the configured rate for this feature.
		feeRate := float64(nominalBps) / 10000.0
		verifiedFeeUSD = requery.FromAmountUSD * feeRate

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
		// swap into a $35,000,000 fee — so the charge is applied to a USD basis
		// priced here, from our own price service.
		//
		// Same rate as the verified path, deliberately. This accrual MIRRORS what
		// LiFi already collected into the fee wallet — a fixed percentage it takes
		// whether or not we can re-query it — so booking a higher "conservative"
		// rate would record fee income that was never received, and fund TP and
		// cashback entitlements out of nothing. The uncertainty here is in the
		// basis, not the rate, and biasing the rate does not fix a basis.
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
		verifiedFeeUSD = basisUSD * float64(nominalBps) / 10000.0
		log.Printf("[allswap] ALERT allswap_lifi_unreachable_total=1 LiFi unreachable for quote %s — "+
			"booking %d bps of a priced $%.2f basis (=$%.6f), NOT the client value",
			req.QuoteID, nominalBps, basisUSD, verifiedFeeUSD)
	}

	// Idempotency key (allswap, quoteId). The quoteId is a string; derive a
	// deterministic UUID so a repeat settle for the same quote is a guard-row
	// no-op in the engine.
	sourceID := deterministicUUID("allswap:" + req.QuoteID)

	// Accrue the VERIFIED fee (never the client value).
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

	// The debt recorded at submit time is now paid; clear it so the sweeper
	// does not revisit it. (The fee engine's guard makes a double settle a
	// no-op anyway — this just keeps the queue honest.)
	markPendingSwapFeeSettled(req.QuoteID)

	utils.SuccessResponse(c, http.StatusOK, "Swap fee settled", gin.H{
		"quoteId":        req.QuoteID,
		"verifiedFeeUsd": verifiedFeeUSD,
		"lifiReachable":  requery.Reachable,
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
//
// This keeps its own request + parse rather than calling h.quoteClient.Quote:
// the shared client cannot serve it. It needs (a) the conditional fee/integrator
// params so a non-200 can be classified via services.IsIntegratorFeeUnconfigured — a
// distinction Quote's plain error does not preserve — and (b) the handler's own
// default slippage (0.005, matching GetQuote) rather than Quote's fixed 0.001.
// Both are load-bearing for the FeeCollectable branch below; unifying onto
// Quote would silently disable it.
func (h *SwapLiFiHandler) liveRequeryQuoteUSD(p settleQuoteParams) requeryResult {
	// fromAddress is REQUIRED by LiFi ("querystring must have required property
	// 'fromAddress'"), and a UTXO route additionally needs it to hold coins,
	// since the quote is built from that address's UTXOs. Omitting it made every
	// re-query a 400 — i.e. every settle silently took the unreachable path.
	// Every value is encoded: these come from the settle request, and a token
	// field carrying "&fromAmount=1" would otherwise price a different amount.
	params := url.Values{
		"fromChain": {p.FromChain}, "toChain": {p.ToChain},
		"fromToken": {p.FromToken}, "toToken": {p.ToToken},
		"fromAmount": {p.FromAmount}, "slippage": {lifiDefaultSlippage},
	}
	if p.FromAddress != "" {
		params.Set("fromAddress", p.FromAddress)
	}
	if p.ToAddress != "" {
		params.Set("toAddress", p.ToAddress)
	}
	if h.integratorID != "" && h.integratorFee != "" {
		params.Set("fee", h.integratorFee)
		params.Set("integrator", h.integratorID)
	}

	resp, err := h.lifiGet(liFiHTTPClient, "/quote?"+params.Encode())
	if err != nil {
		return requeryResult{}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return requeryResult{}
	}
	if resp.StatusCode != http.StatusOK {
		// LiFi answering "no fee wallet for this chain" is not a failure to
		// reach it — it is a definite answer that this swap earned nothing,
		// because GetQuote will have re-quoted it fee-less for the same reason.
		// Booking a rate here would record income that was never collected.
		if services.IsIntegratorFeeUnconfigured(body) {
			return requeryResult{Reachable: true, FeeCollectable: false}
		}
		return requeryResult{}
	}
	var parsed struct {
		Estimate struct {
			FromAmountUSD string `json:"fromAmountUSD"`
		} `json:"estimate"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return requeryResult{}
	}
	usd := parseFloatOr0(parsed.Estimate.FromAmountUSD)
	// A 200 with a malformed/empty/zero fromAmountUSD must NOT count as
	// reachable: that would route a verifiedFeeUSD=0 down the verified path and
	// silently accrue nothing. Report it as unknown so the caller prices the
	// basis itself (design §8, I8 — never a silent $0).
	if usd <= 0 {
		return requeryResult{}
	}
	return requeryResult{FromAmountUSD: usd, Reachable: true, FeeCollectable: true}
}
