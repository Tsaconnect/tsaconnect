package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

const defaultLiFiBaseURL = "https://li.quest/v1"

var liFiHTTPClient = &http.Client{Timeout: 10 * time.Second}

type tokenCacheEntry struct {
	data      []byte
	fetchedAt time.Time
}

// SwapLiFiHandler proxies LiFi aggregator endpoints for token lists and swap quotes.
type SwapLiFiHandler struct {
	mu          sync.Mutex
	tokenCache  map[string]tokenCacheEntry
	lifiBaseURL string
}

// NewSwapLiFiHandler creates a production SwapLiFiHandler.
func NewSwapLiFiHandler() *SwapLiFiHandler {
	return &SwapLiFiHandler{
		tokenCache:  make(map[string]tokenCacheEntry),
		lifiBaseURL: defaultLiFiBaseURL,
	}
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

	resp, err := liFiHTTPClient.Get(fmt.Sprintf("%s/tokens?chains=%s", h.lifiBaseURL, chainId))
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

// GetQuote handles GET /api/swap/lifi/quote
// Forwards all query params to LiFi and returns the response unmodified.
func (h *SwapLiFiHandler) GetQuote(c *gin.Context) {
	params := c.Request.URL.Query()
	if params.Get("slippage") == "" {
		params.Set("slippage", "0.005")
	}

	resp, err := liFiHTTPClient.Get(fmt.Sprintf("%s/quote?%s", h.lifiBaseURL, params.Encode()))
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
