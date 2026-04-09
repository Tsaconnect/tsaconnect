package handlers

import (
	"encoding/json"
	"math/big"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// SwapHandler handles OTC swap price queries and transaction preparation.
type SwapHandler struct {
	cfg               *config.Config
	otcService        *services.OTCService
	blockchainService *services.BlockchainService
}

// NewSwapHandler creates a new SwapHandler.
func NewSwapHandler(cfg *config.Config, otcService *services.OTCService, blockchainService *services.BlockchainService) *SwapHandler {
	return &SwapHandler{
		cfg:               cfg,
		otcService:        otcService,
		blockchainService: blockchainService,
	}
}

// GetSwapPrice handles GET /api/swap/price?direction=buy|sell&mcgpAmount=<wei>
// Public endpoint — no auth required.
func (h *SwapHandler) GetSwapPrice(c *gin.Context) {
	direction := c.Query("direction")
	if direction != "buy" && direction != "sell" {
		utils.ErrorResponse(c, http.StatusBadRequest, "direction must be 'buy' or 'sell'")
		return
	}

	mcgpAmountStr := c.Query("mcgpAmount")
	var mcgpAmount *big.Int
	if mcgpAmountStr == "" {
		// Default to 1e18 (1 token in wei)
		mcgpAmount = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
		mcgpAmountStr = mcgpAmount.String()
	} else {
		var ok bool
		mcgpAmount, ok = new(big.Int).SetString(mcgpAmountStr, 10)
		if !ok || mcgpAmount.Sign() <= 0 {
			utils.ErrorResponse(c, http.StatusBadRequest, "mcgpAmount must be a positive integer (wei)")
			return
		}
	}

	var usdcAmount *big.Int
	var err error

	switch direction {
	case "buy":
		usdcAmount, err = h.otcService.GetBuyPrice(mcgpAmount)
	case "sell":
		usdcAmount, err = h.otcService.GetSellPrice(mcgpAmount)
	}

	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "failed to fetch price from contract: "+err.Error())
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "price fetched successfully", gin.H{
		"direction":   direction,
		"mcgpAmount":  mcgpAmountStr,
		"usdcAmount":  usdcAmount.String(),
	})
}

// prepareSwapRequest is the request body for POST /api/swap/prepare.
type prepareSwapRequest struct {
	Direction   string `json:"direction"`
	McgpAmount  string `json:"mcgpAmount"`
	SlippageBps int    `json:"slippageBps"`
}

// PrepareSwap handles POST /api/swap/prepare (auth required).
func (h *SwapHandler) PrepareSwap(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "authentication required")
		return
	}

	userWallet := user.WalletAddress
	if userWallet == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "user wallet address not set")
		return
	}

	var req prepareSwapRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.Direction != "buy" && req.Direction != "sell" {
		utils.ErrorResponse(c, http.StatusBadRequest, "direction must be 'buy' or 'sell'")
		return
	}

	mcgpAmount, ok := new(big.Int).SetString(req.McgpAmount, 10)
	if !ok || mcgpAmount.Sign() <= 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "mcgpAmount must be a positive integer (wei)")
		return
	}

	// Default slippage: 50 bps = 0.5%
	slippageBps := req.SlippageBps
	if slippageBps <= 0 {
		slippageBps = 50
	}

	// OTC contract is on Sonic mainnet
	network := "mainnet"

	otcAddress := h.cfg.OTCMarketplaceAddress
	sonicClient := h.blockchainService.ClientForChain("sonic")

	var approveTxBytes, swapTxBytes []byte

	switch req.Direction {
	case "buy":
		// Get USDC cost from contract
		usdcAmount, err := h.otcService.GetBuyPrice(mcgpAmount)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadGateway, "failed to calculate buy price: "+err.Error())
			return
		}

		// Apply slippage upward (buyer pays at most usdcAmount * (1 + slippageBps/10000))
		maxUsdcAmount := applySlippageUp(usdcAmount, slippageBps)

		// Prepare approve USDC tx
		usdcAddr := h.blockchainService.TokenAddressForNetwork(network, "sonic", "USDC")
		if usdcAddr == "" {
			utils.ErrorResponse(c, http.StatusInternalServerError, "USDC token address not configured for network: "+network)
			return
		}

		if sonicClient != nil {
			approveTxBytes, err = sonicClient.PrepareERC20Approve(usdcAddr, userWallet, otcAddress, maxUsdcAmount)
			if err != nil {
				utils.ErrorResponse(c, http.StatusBadGateway, "failed to prepare USDC approve tx: "+err.Error())
				return
			}
		}

		// Prepare buy tx
		swapTxBytes, err = h.otcService.PrepareBuy(userWallet, mcgpAmount, maxUsdcAmount)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadGateway, "failed to prepare buy tx: "+err.Error())
			return
		}

	case "sell":
		// Get USDC proceeds from contract
		usdcAmount, err := h.otcService.GetSellPrice(mcgpAmount)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadGateway, "failed to calculate sell price: "+err.Error())
			return
		}

		// Apply slippage downward (seller accepts at least usdcAmount * (1 - slippageBps/10000))
		minUsdcAmount := applySlippageDown(usdcAmount, slippageBps)

		// Prepare approve MCGP tx
		mcgpAddr := h.blockchainService.TokenAddressForNetwork(network, "sonic", "MCGP")
		if mcgpAddr == "" {
			utils.ErrorResponse(c, http.StatusInternalServerError, "MCGP token address not configured for network: "+network)
			return
		}

		if sonicClient != nil {
			approveTxBytes, err = sonicClient.PrepareERC20Approve(mcgpAddr, userWallet, otcAddress, mcgpAmount)
			if err != nil {
				utils.ErrorResponse(c, http.StatusBadGateway, "failed to prepare MCGP approve tx: "+err.Error())
				return
			}
		}

		// Prepare sell tx
		swapTxBytes, err = h.otcService.PrepareSell(userWallet, mcgpAmount, minUsdcAmount)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadGateway, "failed to prepare sell tx: "+err.Error())
			return
		}
	}

	// Unmarshal tx bytes into raw JSON objects for the response
	var approveTx, swapTx json.RawMessage
	if approveTxBytes != nil {
		approveTx = json.RawMessage(approveTxBytes)
	}
	if swapTxBytes != nil {
		swapTx = json.RawMessage(swapTxBytes)
	}

	utils.SuccessResponse(c, http.StatusOK, "swap transactions prepared", gin.H{
		"direction":  req.Direction,
		"mcgpAmount": req.McgpAmount,
		"approveTx":  approveTx,
		"swapTx":     swapTx,
	})
}

// applySlippageUp increases amount by slippageBps basis points (for buy maxUsdcAmount).
// result = amount * (10000 + slippageBps) / 10000
func applySlippageUp(amount *big.Int, slippageBps int) *big.Int {
	numerator := new(big.Int).Mul(amount, big.NewInt(int64(10000+slippageBps)))
	return new(big.Int).Div(numerator, big.NewInt(10000))
}

// applySlippageDown decreases amount by slippageBps basis points (for sell minUsdcAmount).
// result = amount * (10000 - slippageBps) / 10000
func applySlippageDown(amount *big.Int, slippageBps int) *big.Int {
	numerator := new(big.Int).Mul(amount, big.NewInt(int64(10000-slippageBps)))
	return new(big.Int).Div(numerator, big.NewInt(10000))
}
