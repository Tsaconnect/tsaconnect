package handlers

import (
	"context"
	"encoding/json"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// otcPositionReader is what the handler needs from services.OTCPositionService.
type otcPositionReader interface {
	Position(ctx context.Context, wallet string, maxAge time.Duration) (*services.OTCPosition, error)
}

// otcPositionDisplayMaxAge is how stale the index may be when only SHOWING a
// position. Enforcing a sell always refreshes.
const otcPositionDisplayMaxAge = 10 * time.Second

// mcgpDecimals is MCGP's ERC-20 decimals, for messages.
const mcgpDecimals = 18

// SwapHandler handles OTC swap price queries and transaction preparation.
type SwapHandler struct {
	cfg               *config.Config
	otcService        *services.OTCService
	blockchainService *services.BlockchainService
	// db holds the OTC settings row whose SellEnabled is the server-side kill
	// switch; positions caps a sell at the wallet's own OTC purchases. Either
	// missing refuses every sell — the cap fails closed.
	db        *gorm.DB
	positions otcPositionReader
}

// NewSwapHandler creates a new SwapHandler. positions may be nil (the OTC
// desk unconfigured), which refuses every sell.
func NewSwapHandler(cfg *config.Config, otcService *services.OTCService, blockchainService *services.BlockchainService, db *gorm.DB, positions *services.OTCPositionService) *SwapHandler {
	h := &SwapHandler{
		cfg:               cfg,
		otcService:        otcService,
		blockchainService: blockchainService,
		db:                db,
	}
	// Assigned only when set: a nil pointer in the interface would not
	// compare equal to nil below.
	if positions != nil {
		h.positions = positions
	}
	return h
}

// GetOTCPosition handles GET /api/otc/position?walletAddress=0x… (auth): what
// the caller's device wallet may sell back on the OTC desk — the MCGP it
// bought there, net of what it sold, capped at what it still holds. Amounts
// are MCGP wei strings. Read-only: walletAddress must be the account's own
// stored wallet; a GET never links a wallet.
func (h *SwapHandler) GetOTCPosition(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "authentication required")
		return
	}
	wallet, ok := requireOwnWallet(c, user, c.Query("walletAddress"))
	if !ok {
		return
	}
	pos, err := h.readOTCPosition(c.Request.Context(), wallet, otcPositionDisplayMaxAge)
	if err != nil {
		log.Printf("[otc-position] read failed wallet=%s: %v", wallet, err)
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "We couldn't check your OTC purchases right now. Please try again in a moment.")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "OTC position", gin.H{
		"walletAddress":     pos.Wallet,
		"bought":            pos.Bought.String(),
		"sold":              pos.Sold.String(),
		"netPurchased":      pos.NetPurchased.String(),
		"walletBalance":     pos.WalletBalance.String(),
		"sellable":          pos.Sellable.String(),
		"sellableFormatted": formatTokenBalance(pos.Sellable, mcgpDecimals),
		"decimals":          mcgpDecimals,
		"asOfBlock":         pos.AsOfBlock,
	})
}

func (h *SwapHandler) readOTCPosition(ctx context.Context, wallet string, maxAge time.Duration) (*services.OTCPosition, error) {
	if h.positions == nil {
		return nil, services.ErrOTCPositionUnavailable
	}
	return h.positions.Position(ctx, wallet, maxAge)
}

// enforceOTCSellCap refuses a sell the owner's rule does not allow: the admin
// has paused selling, or the amount is more than the wallet bought on the
// desk. The wallet is the SIGNING wallet, since that is where the tokens leave
// from. Anything it cannot verify is refused too. It writes the error response
// and returns false when the sell must stop.
func (h *SwapHandler) enforceOTCSellCap(c *gin.Context, wallet string, mcgpAmount *big.Int) bool {
	settings, err := loadOrSeedOTCSettings(h.db)
	if err != nil {
		log.Printf("[otc-sell] settings unreadable: %v", err)
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "OTC selling is unavailable right now. Please try again in a moment.")
		return false
	}
	if !settings.SellEnabled {
		utils.ErrorResponse(c, http.StatusForbidden, "Selling is closed on the OTC marketplace.")
		return false
	}

	pos, err := h.readOTCPosition(c.Request.Context(), wallet, 0)
	if err != nil {
		log.Printf("[otc-sell] position unreadable wallet=%s: %v", wallet, err)
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "We couldn't confirm how much MCGP you bought on the OTC desk. Please try again in a moment.")
		return false
	}
	if mcgpAmount.Cmp(pos.Sellable) > 0 {
		if pos.Sellable.Sign() == 0 {
			utils.ErrorResponse(c, http.StatusUnprocessableEntity,
				"This wallet has no MCGP bought on the OTC desk to sell. Only MCGP purchased on the OTC desk can be sold back here.")
		} else {
			utils.ErrorResponse(c, http.StatusUnprocessableEntity,
				"You can sell up to "+formatTokenBalance(pos.Sellable, mcgpDecimals)+
					" MCGP from this wallet. Only MCGP purchased on the OTC desk can be sold back here.")
		}
		return false
	}
	return true
}

// GetSwapPrice handles GET /api/swap/price
//
// Query params:
//   - direction: "buy" or "sell" (required)
//   - mcgpAmount: MCGP amount in wei (optional, used for sell or buy-by-mcgp)
//   - usdcAmount: USDC amount in wei (optional, used for buy-by-usdc)
//
// For buy: pass either mcgpAmount (get USDC cost) or usdcAmount (get MCGP you'll receive)
// For sell: pass mcgpAmount (get USDC you'll receive)
func (h *SwapHandler) GetSwapPrice(c *gin.Context) {
	direction := c.Query("direction")
	if direction != "buy" && direction != "sell" {
		utils.ErrorResponse(c, http.StatusBadRequest, "direction must be 'buy' or 'sell'")
		return
	}

	mcgpAmountStr := c.Query("mcgpAmount")
	usdcAmountStr := c.Query("usdcAmount")

	// Get price per 1 MCGP for the active direction
	var pricePerMCGP *big.Int
	var err error
	if direction == "buy" {
		pricePerMCGP, err = h.otcService.GetBuyPricePerToken()
	} else {
		pricePerMCGP, err = h.otcService.GetSellPricePerToken()
	}
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "failed to fetch price: "+err.Error())
		return
	}

	var mcgpAmount, usdcAmount *big.Int

	if direction == "buy" && usdcAmountStr != "" {
		// Buy mode: user specified USDC amount → calculate MCGP they'll receive
		usdcAmount, _ = new(big.Int).SetString(usdcAmountStr, 10)
		if usdcAmount == nil || usdcAmount.Sign() <= 0 {
			utils.ErrorResponse(c, http.StatusBadRequest, "usdcAmount must be a positive integer (wei)")
			return
		}
		// mcgpAmount = (usdcAmount * 1e18) / pricePerMCGP
		mcgpAmount = new(big.Int).Mul(usdcAmount, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
		mcgpAmount.Div(mcgpAmount, pricePerMCGP)

		// Verify with contract
		verifiedUsdc, verr := h.otcService.GetBuyPrice(mcgpAmount)
		if verr == nil && verifiedUsdc != nil {
			usdcAmount = verifiedUsdc
		}
	} else {
		// mcgpAmount provided (or default 1 token)
		if mcgpAmountStr == "" {
			mcgpAmount = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
		} else {
			mcgpAmount, _ = new(big.Int).SetString(mcgpAmountStr, 10)
			if mcgpAmount == nil || mcgpAmount.Sign() <= 0 {
				utils.ErrorResponse(c, http.StatusBadRequest, "mcgpAmount must be a positive integer (wei)")
				return
			}
		}

		switch direction {
		case "buy":
			usdcAmount, err = h.otcService.GetBuyPrice(mcgpAmount)
		case "sell":
			usdcAmount, err = h.otcService.GetSellPrice(mcgpAmount)
		}
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadGateway, "failed to fetch price: "+err.Error())
			return
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "price fetched successfully", gin.H{
		"direction":    direction,
		"mcgpAmount":   mcgpAmount.String(),
		"usdcAmount":   usdcAmount.String(),
		"pricePerMCGP": pricePerMCGP.String(),
	})
}

// prepareSwapRequest is the request body for POST /api/swap/prepare.
type prepareSwapRequest struct {
	Direction   string `json:"direction"`
	McgpAmount  string `json:"mcgpAmount"` // Required for sell; optional for buy
	UsdcAmount  string `json:"usdcAmount"` // Optional for buy (alternative to mcgpAmount)
	SlippageBps int    `json:"slippageBps"`
	// FromAddress lets the app swap from its ACTIVE device wallet (nonce/gas
	// computed for it; the submit guard requires the signature to match).
	// Empty = the registered wallet.
	FromAddress string `json:"fromAddress,omitempty"`
}

// PrepareSwap handles POST /api/swap/prepare (auth required).
func (h *SwapHandler) PrepareSwap(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "authentication required")
		return
	}

	var req prepareSwapRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Spend from the declared active device wallet; registered wallet otherwise.
	userWallet := strings.TrimSpace(req.FromAddress)
	if userWallet != "" && !isValidEthAddress(userWallet) {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid fromAddress format")
		return
	}
	if userWallet == "" {
		userWallet = user.WalletAddress
	}
	if userWallet == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "user wallet address not set")
		return
	}

	if req.Direction != "buy" && req.Direction != "sell" {
		utils.ErrorResponse(c, http.StatusBadRequest, "direction must be 'buy' or 'sell'")
		return
	}

	slippageBps := req.SlippageBps
	if slippageBps <= 0 {
		slippageBps = 50
	}

	// Resolve MCGP amount
	var mcgpAmount *big.Int
	var ok bool

	if req.Direction == "buy" && req.UsdcAmount != "" && req.McgpAmount == "" {
		// Buy by USDC amount: calculate MCGP
		usdcInput, uok := new(big.Int).SetString(req.UsdcAmount, 10)
		if !uok || usdcInput.Sign() <= 0 {
			utils.ErrorResponse(c, http.StatusBadRequest, "usdcAmount must be a positive integer (wei)")
			return
		}
		pricePerMCGP, err := h.otcService.GetBuyPricePerToken()
		if err != nil || pricePerMCGP.Sign() == 0 {
			utils.ErrorResponse(c, http.StatusBadGateway, "failed to get MCGP price")
			return
		}
		mcgpAmount = new(big.Int).Mul(usdcInput, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
		mcgpAmount.Div(mcgpAmount, pricePerMCGP)
	} else {
		mcgpAmount, ok = new(big.Int).SetString(req.McgpAmount, 10)
		if !ok || mcgpAmount.Sign() <= 0 {
			utils.ErrorResponse(c, http.StatusBadRequest, "mcgpAmount must be a positive integer (wei)")
			return
		}
	}

	if req.Direction == "sell" && !h.enforceOTCSellCap(c, userWallet, mcgpAmount) {
		return
	}

	// OTC contract is on Sonic mainnet
	network := services.OTCNetwork
	otcAddress := h.cfg.OTCMarketplaceAddress
	sonicClient := h.blockchainService.ClientForChain("sonic")

	var approveTxBytes, swapTxBytes []byte
	var usdcAmount *big.Int
	needsApprove := true

	switch req.Direction {
	case "buy":
		var err error
		usdcAmount, err = h.otcService.GetBuyPrice(mcgpAmount)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadGateway, "failed to calculate buy price: "+err.Error())
			return
		}

		maxUsdcAmount := applySlippageUp(usdcAmount, slippageBps)

		usdcAddr := h.blockchainService.TokenAddressForNetwork(network, "sonic", "USDC")
		if usdcAddr == "" {
			utils.ErrorResponse(c, http.StatusInternalServerError, "USDC token address not configured")
			return
		}

		if sonicClient != nil {
			if allowance, allowanceErr := sonicClient.GetTokenAllowance(usdcAddr, userWallet, otcAddress); allowanceErr == nil {
				needsApprove = allowance.Cmp(maxUsdcAmount) < 0
			}
			if needsApprove {
				approveTxBytes, err = sonicClient.PrepareERC20Approve(usdcAddr, userWallet, otcAddress, maxUsdcAmount)
				if err != nil {
					utils.ErrorResponse(c, http.StatusBadGateway, "failed to prepare USDC approve tx: "+err.Error())
					return
				}
			}
		}

		if needsApprove {
			swapTxBytes, err = h.otcService.PrepareBuyWithEstimateFallback(userWallet, mcgpAmount, maxUsdcAmount)
		} else {
			swapTxBytes, err = h.otcService.PrepareBuy(userWallet, mcgpAmount, maxUsdcAmount)
		}
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadGateway, "failed to prepare buy tx: "+err.Error())
			return
		}

	case "sell":
		var err error
		usdcAmount, err = h.otcService.GetSellPrice(mcgpAmount)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadGateway, "failed to calculate sell price: "+err.Error())
			return
		}

		minUsdcAmount := applySlippageDown(usdcAmount, slippageBps)

		mcgpAddr := h.blockchainService.TokenAddressForNetwork(network, "sonic", "MCGP")
		if mcgpAddr == "" {
			utils.ErrorResponse(c, http.StatusInternalServerError, "MCGP token address not configured")
			return
		}

		if sonicClient != nil {
			if allowance, allowanceErr := sonicClient.GetTokenAllowance(mcgpAddr, userWallet, otcAddress); allowanceErr == nil {
				needsApprove = allowance.Cmp(mcgpAmount) < 0
			}
			if needsApprove {
				approveTxBytes, err = sonicClient.PrepareERC20Approve(mcgpAddr, userWallet, otcAddress, mcgpAmount)
				if err != nil {
					utils.ErrorResponse(c, http.StatusBadGateway, "failed to prepare MCGP approve tx: "+err.Error())
					return
				}
			}
		}

		if needsApprove {
			swapTxBytes, err = h.otcService.PrepareSellWithEstimateFallback(userWallet, mcgpAmount, minUsdcAmount)
		} else {
			swapTxBytes, err = h.otcService.PrepareSell(userWallet, mcgpAmount, minUsdcAmount)
		}
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadGateway, "failed to prepare sell tx: "+err.Error())
			return
		}
	}

	approveTx, swapTx, txErr := normalizePreparedSwapTransactions(approveTxBytes, swapTxBytes)
	if txErr != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "failed to decode prepared swap transactions")
		return
	}

	usdcStr := "0"
	if usdcAmount != nil {
		usdcStr = usdcAmount.String()
	}

	utils.SuccessResponse(c, http.StatusOK, "swap transactions prepared", gin.H{
		"direction":  req.Direction,
		"mcgpAmount": mcgpAmount.String(),
		"usdcAmount": usdcStr,
		"approveTx":  approveTx,
		"swapTx":     swapTx,
	})
}

func normalizePreparedSwapTransactions(approveTxBytes, swapTxBytes []byte) (*blockchain.UnsignedTx, *blockchain.UnsignedTx, error) {
	var approveTx *blockchain.UnsignedTx
	var swapTx *blockchain.UnsignedTx

	if len(approveTxBytes) > 0 {
		approveTx = &blockchain.UnsignedTx{}
		if err := json.Unmarshal(approveTxBytes, approveTx); err != nil {
			return nil, nil, err
		}
	}

	if len(swapTxBytes) > 0 {
		swapTx = &blockchain.UnsignedTx{}
		if err := json.Unmarshal(swapTxBytes, swapTx); err != nil {
			return nil, nil, err
		}
	}

	if approveTx != nil && swapTx != nil && swapTx.Nonce <= approveTx.Nonce {
		swapTx.Nonce = approveTx.Nonce + 1
	}

	return approveTx, swapTx, nil
}

func applySlippageUp(amount *big.Int, slippageBps int) *big.Int {
	numerator := new(big.Int).Mul(amount, big.NewInt(int64(10000+slippageBps)))
	return new(big.Int).Div(numerator, big.NewInt(10000))
}

func applySlippageDown(amount *big.Int, slippageBps int) *big.Int {
	numerator := new(big.Int).Mul(amount, big.NewInt(int64(10000-slippageBps)))
	return new(big.Int).Div(numerator, big.NewInt(10000))
}
