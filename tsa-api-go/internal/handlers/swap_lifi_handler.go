package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

const defaultLiFiBaseURL = "https://li.quest/v1"

const (
	lifiDefaultSlippage = "0.005"

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
	return &SwapLiFiHandler{
		tokenCache:    make(map[string]tokenCacheEntry),
		lifiBaseURL:   defaultLiFiBaseURL,
		apiKey:        apiKey,
		integratorID:  integratorID,
		integratorFee: integratorFee,
	}
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

	resp, err := h.lifiGet(liFiHTTPClient, fmt.Sprintf("/tokens?chains=%s", chainId))
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
// Proxies LiFi EVM chain list with a 24-hour in-memory cache.
func (h *SwapLiFiHandler) GetChains(c *gin.Context) {
	h.mu.Lock()
	if h.chainsCache != nil && time.Since(h.chainsCache.fetchedAt) < 24*time.Hour {
		data := h.chainsCache.data
		h.mu.Unlock()
		c.Data(http.StatusOK, "application/json", data)
		return
	}
	h.mu.Unlock()

	resp, err := h.lifiGet(liFiHTTPClient, "/chains?chainTypes=EVM")
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
	resp, err := h.lifiGet(liFiBulkHTTPClient, "/tokens")
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
