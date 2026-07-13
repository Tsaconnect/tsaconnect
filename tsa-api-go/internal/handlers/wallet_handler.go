package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// ethAddressRegex validates Ethereum-style wallet addresses.
var ethAddressRegex = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// registerWalletRequest is the request body for RegisterWalletAddress.
type registerWalletRequest struct {
	WalletAddress string `json:"walletAddress" binding:"required"`
}

// prepareTxRequest is the request body for PrepareSendTransaction.
//
// TokenAddress + Decimals are optional overrides: when set, the handler
// uses them directly instead of resolving the token from the configured
// supported_tokens map. This is the path for sending auto-discovered
// ERC-20s (tokens the user holds but the operator hasn't curated).
type prepareTxRequest struct {
	TokenSymbol  string `json:"tokenSymbol" binding:"required"`
	ToAddress    string `json:"toAddress" binding:"required"`
	Amount       string `json:"amount" binding:"required"`
	ChainID      int64  `json:"chainId" binding:"required"`
	TokenAddress string `json:"tokenAddress,omitempty"`
	Decimals     *int   `json:"decimals,omitempty"`
	// FromAddress lets the app spend from its ACTIVE device wallet (nonce/gas
	// are computed for this address). Empty = the registered wallet.
	// Receiving stays on the registered wallet (username resolution).
	FromAddress string `json:"fromAddress,omitempty"`
}

// submitTxRequest is the request body for SubmitTransaction.
type submitTxRequest struct {
	SignedTx    string  `json:"signedTx" binding:"required"`
	TxType      string  `json:"txType" binding:"required"`
	TokenSymbol string  `json:"tokenSymbol" binding:"required"`
	ToAddress   string  `json:"toAddress" binding:"required"`
	Amount      string  `json:"amount" binding:"required"`
	ChainID     int64   `json:"chainId" binding:"required"`
	FeeUSD      float64 `json:"feeUSD"` // optional: system fee in USD for TP distribution
	// FromAddress is the wallet the tx was PREPARED for (the app's active
	// wallet). The signature must recover to it. Empty = the registered wallet.
	FromAddress string `json:"fromAddress,omitempty"`
}

// isValidEthAddress checks if the given string is a valid Ethereum address.
func isValidEthAddress(addr string) bool {
	return ethAddressRegex.MatchString(addr)
}

// isPositiveAmount checks if the given string represents a positive numeric amount.
func isPositiveAmount(amount string) bool {
	val, err := strconv.ParseFloat(amount, 64)
	if err != nil {
		return false
	}
	return val > 0
}

// formatTokenBalance converts a raw big.Int balance to a human-readable string using token decimals.
func formatTokenBalance(balance *big.Int, decimals int) string {
	if balance == nil || balance.Sign() == 0 {
		return "0"
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole := new(big.Int).Div(balance, divisor)
	remainder := new(big.Int).Mod(balance, divisor)

	if remainder.Sign() == 0 {
		return whole.String()
	}

	format := fmt.Sprintf("%%s.%%0%ds", decimals)
	result := fmt.Sprintf(format, whole.String(), remainder)
	result = strings.TrimRight(result, "0")
	return result
}

func parseTokenAmount(amount string, decimals int) (*big.Int, bool) {
	f, ok := new(big.Float).SetString(amount)
	if !ok {
		return nil, false
	}
	multiplier := new(big.Float).SetInt(
		new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil),
	)
	f.Mul(f, multiplier)
	result, _ := f.Int(nil)
	if result.Sign() <= 0 {
		return nil, false
	}
	return result, true
}

// RegisterWalletAddress handles POST /api/wallet/register.
// Associates a blockchain wallet address with the authenticated user.
func (h *Handlers) RegisterWalletAddress(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var req registerWalletRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "walletAddress is required")
		return
	}

	if !isValidEthAddress(req.WalletAddress) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid wallet address format. Must be a valid Ethereum address (0x followed by 40 hex characters)")
		return
	}

	// Check if user already has a wallet address set.
	if user.WalletAddress != "" {
		// If the new address is the same, no-op.
		if user.WalletAddress == req.WalletAddress {
			utils.SuccessResponse(c, http.StatusOK, "Wallet address already registered", gin.H{
				"walletAddress": user.WalletAddress,
			})
			return
		}
		// Allow update to a different address; log the change for audit.
		log.Printf("User %s changed wallet from %s to %s", user.ID, user.WalletAddress, req.WalletAddress)
	}

	// Check if the address is already taken by another user.
	var existingUser models.User
	if err := config.DB.Where("wallet_address = ?", req.WalletAddress).First(&existingUser).Error; err == nil {
		utils.ErrorResponse(c, http.StatusConflict, "Wallet address already in use by another account")
		return
	}

	// Update user's wallet address.
	if err := config.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("wallet_address", req.WalletAddress).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to register wallet address")
		return
	}

	// Create Wallet record if one doesn't exist yet.
	var wallet models.Wallet
	if err := config.DB.Where("user_id = ?", user.ID).First(&wallet).Error; err != nil {
		now := time.Now()
		wallet = models.Wallet{
			ID:               uuid.New(),
			UserID:           user.ID,
			TotalBalance:     0,
			TotalUSDValue:    0,
			TransactionLimit: models.DefaultTransactionLimit,
			DailyLimit:       models.DefaultDailyLimit,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if createErr := config.DB.Create(&wallet).Error; createErr != nil {
			log.Printf("Failed to create wallet record for user %s: %v", user.ID, createErr)
		}
	}

	user.WalletAddress = req.WalletAddress
	utils.SuccessResponse(c, http.StatusOK, "Wallet address registered successfully", gin.H{
		"walletAddress": user.WalletAddress,
	})
}

// resolveNetwork reads the "network" query param and returns "mainnet" or "testnet".
func resolveNetwork(c *gin.Context) string {
	n := c.DefaultQuery("network", "mainnet")
	if n == "testnet" {
		return "testnet"
	}
	return "mainnet"
}

// GetWalletBalances handles GET /api/wallet/balances.
// Query params: ?network=mainnet|testnet (default mainnet), ?chainId=<numeric> (optional),
// ?address=<hex> (optional — overrides the user's registered wallet for multi-wallet clients).
func (h *Handlers) GetWalletBalances(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	address := strings.TrimSpace(c.Query("address"))
	if address == "" {
		address = user.WalletAddress
	}
	if address == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}
	if !ethAddressRegex.MatchString(address) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid wallet address")
		return
	}

	network := resolveNetwork(c)
	netCfg, ok := h.Config.Networks[network]
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid network")
		return
	}

	chainIDParam := c.Query("chainId")

	type chainQuery struct {
		name string
		cfg  config.ChainConfig
	}
	var chains []chainQuery

	if chainIDParam != "" {
		cid, err := strconv.ParseInt(chainIDParam, 10, 64)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid chainId")
			return
		}
		for name, cfg := range netCfg.Chains {
			if cfg.ChainID == cid {
				chains = append(chains, chainQuery{name, cfg})
				break
			}
		}
		if len(chains) == 0 {
			utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain ID")
			return
		}
	} else {
		for name, cfg := range netCfg.Chains {
			chains = append(chains, chainQuery{name, cfg})
		}
	}

	var supportedTokens []models.SupportedToken
	config.DB.Where("is_active = ?", true).Find(&supportedTokens)

	type tokenBalance struct {
		Balance  string  `json:"balance"`
		UsdPrice float64 `json:"usdPrice"`
		UsdValue float64 `json:"usdValue"`
	}
	result := make(map[string]map[string]tokenBalance)

	// Collect all token symbols to fetch prices
	symbolSet := make(map[string]bool)
	for _, cq := range chains {
		symbolSet[cq.cfg.NativeCurrency] = true
	}
	for _, tok := range supportedTokens {
		symbolSet[tok.Symbol] = true
	}
	var priceSymbols []string
	for s := range symbolSet {
		priceSymbols = append(priceSymbols, s)
	}

	// Fetch USD prices from CoinGecko
	prices := make(map[string]float64)
	if h.PriceService != nil && len(priceSymbols) > 0 {
		if priceData, err := h.PriceService.GetPrices(priceSymbols); err == nil {
			for sym, pd := range priceData {
				prices[sym] = pd.USD
			}
		}
	}

	// Override MCGP price with OTC contract buy price
	if h.OTCService != nil {
		if priceWei, err := h.OTCService.GetBuyPricePerToken(); err == nil && priceWei.Sign() > 0 {
			// priceWei is USDC (6 decimals) per 1 MCGP — convert to float USD
			prices["MCGP"] = float64(priceWei.Int64()) / 1e6
		}
	}

	for _, cq := range chains {
		client := h.BlockchainService.ClientForNetwork(network, cq.name)
		chainBalances := make(map[string]tokenBalance)

		if client != nil {
			if nativeBal, err := client.GetBalance(address); err == nil {
				balStr := formatTokenBalance(nativeBal, 18)
				balFloat, _ := strconv.ParseFloat(balStr, 64)
				price := prices[cq.cfg.NativeCurrency]
				chainBalances[cq.cfg.NativeCurrency] = tokenBalance{
					Balance:  balStr,
					UsdPrice: price,
					UsdValue: balFloat * price,
				}
			}
		}

		for _, tok := range supportedTokens {
			var tokenChains []string
			json.Unmarshal(tok.Chains, &tokenChains)
			for _, tc := range tokenChains {
				if tc == cq.name {
					tokenAddr := h.BlockchainService.TokenAddressForNetwork(network, cq.name, tok.Symbol)
					if tokenAddr != "" && client != nil {
						if bal, err := client.GetTokenBalance(tokenAddr, address); err == nil {
							// Decimals are per-contract (USDT is 6 on
							// Ethereum/Polygon but 18 on BSC). Query the
							// contract directly; fall back to the DB row only
							// if the RPC fails.
							decimals := tok.Decimals
							if onChainDec, derr := client.GetTokenDecimals(tokenAddr); derr == nil {
								decimals = onChainDec
							} else {
								log.Printf("[wallet] decimals() failed for %s on %s (%s); falling back to DB value %d: %v",
									tok.Symbol, cq.name, tokenAddr, tok.Decimals, derr)
							}
							balStr := formatTokenBalance(bal, decimals)
							balFloat, _ := strconv.ParseFloat(balStr, 64)
							price := prices[tok.Symbol]
							chainBalances[tok.Symbol] = tokenBalance{
								Balance:  balStr,
								UsdPrice: price,
								UsdValue: balFloat * price,
							}
						} else {
							chainBalances[tok.Symbol] = tokenBalance{Balance: "0"}
						}
					} else {
						chainBalances[tok.Symbol] = tokenBalance{Balance: "0"}
					}
				}
			}
		}

		result[cq.name] = chainBalances
	}

	utils.SuccessResponse(c, http.StatusOK, "Wallet balances retrieved", gin.H{
		"walletAddress": address,
		"network":       network,
		"balances":      result,
	})
}

// GetDiscoveredTokens handles GET /api/wallet/discovered-tokens.
//
// Returns every ERC-20 token an address holds, across all chains we have
// indexer coverage for. This is the auto-discovery counterpart to
// GetWalletBalances, which only knows about tokens registered in the
// supported_tokens table. Supports an optional ?chains=ethereum,polygon
// filter so the FE can fan out per-chain spinners if needed.
//
// The endpoint always returns 200 with an empty list when no indexer
// keys are configured; the response carries `providerAvailable: false` so
// the FE can render an explanatory empty state instead of "you hold no
// tokens" (which would be misleading).
func (h *Handlers) GetDiscoveredTokens(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	address := strings.TrimSpace(c.Query("address"))
	if address == "" {
		address = user.WalletAddress
	}
	if address == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}
	if !ethAddressRegex.MatchString(address) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid wallet address")
		return
	}

	// Optional comma-separated chain filter; empty = query everything.
	var chainKeys []string
	if raw := strings.TrimSpace(c.Query("chains")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if p := strings.TrimSpace(strings.ToLower(part)); p != "" {
				chainKeys = append(chainKeys, p)
			}
		}
	}

	if h.TokenDiscoveryService == nil || !h.TokenDiscoveryService.HasAnyProvider() {
		utils.SuccessResponse(c, http.StatusOK, "Token discovery is not configured", gin.H{
			"walletAddress":     address,
			"providerAvailable": false,
			"tokens":            []services.DiscoveredToken{},
		})
		return
	}

	// 25s budget for the whole multi-chain fan-out. Per-chain timeout is
	// 10s inside the discovery service, so even a single slow indexer
	// call won't blow this; the budget mainly protects us against an
	// unexpected hang in net/http itself.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	tokens := h.TokenDiscoveryService.DiscoverTokens(ctx, address, chainKeys)

	utils.SuccessResponse(c, http.StatusOK, "Discovered tokens retrieved", gin.H{
		"walletAddress":     address,
		"providerAvailable": true,
		"tokens":            tokens,
	})
}

// PrepareSendTransaction handles POST /api/wallet/prepare-tx.
// Prepares an unsigned transaction for the mobile app to sign.
func (h *Handlers) PrepareSendTransaction(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	var req prepareTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "tokenSymbol, toAddress, amount, and chainId are required")
		return
	}

	tokenUpper := strings.ToUpper(req.TokenSymbol)
	if !isValidEthAddress(req.ToAddress) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid destination address format")
		return
	}
	if !isPositiveAmount(req.Amount) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Amount must be a positive number")
		return
	}

	// Sender: the app's active device wallet when declared, else the
	// registered wallet. The nonce/gas below are computed for THIS address,
	// so the submit guard later requires the signature to match it.
	fromAddress := strings.TrimSpace(req.FromAddress)
	if fromAddress != "" && !isValidEthAddress(fromAddress) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid fromAddress format")
		return
	}
	if fromAddress == "" {
		fromAddress = user.WalletAddress
	}

	client, chainName := h.BlockchainService.ClientForChainID(req.ChainID)
	if client == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain ID")
		return
	}

	// Resolve which network this chainId belongs to
	network, _, _ := h.BlockchainService.NetworkForChainID(req.ChainID)
	if network == "" {
		network = "mainnet"
	}

	// Decimals: caller override takes precedence (auto-discovered tokens
	// can have any decimals — 6, 8, 9, 18 — and we won't have curated
	// metadata for them). Otherwise fall back to the historical defaults.
	decimals := 18
	if req.Decimals != nil && *req.Decimals >= 0 && *req.Decimals <= 36 {
		decimals = *req.Decimals
	} else if tokenUpper == "USDT" || tokenUpper == "USDC" {
		decimals = 6
	}
	amountWei, ok := parseTokenAmount(req.Amount, decimals)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid amount format")
		return
	}

	var txBytes []byte
	var err error

	if tokenUpper == "S" || tokenUpper == "TBNB" || tokenUpper == "BNB" {
		txBytes, err = client.PrepareNativeTransfer(fromAddress, req.ToAddress, amountWei)
	} else {
		// Resolve token contract: explicit override (discovered tokens)
		// wins over the supported_tokens lookup.
		tokenAddr := strings.TrimSpace(req.TokenAddress)
		if tokenAddr != "" {
			if !isValidEthAddress(tokenAddr) {
				utils.ErrorResponse(c, http.StatusBadRequest, "Invalid token contract address")
				return
			}
		} else {
			tokenAddr = h.BlockchainService.TokenAddressForNetwork(network, chainName, tokenUpper)
			if tokenAddr == "" {
				utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Token %s not configured on %s", tokenUpper, chainName))
				return
			}
		}
		txBytes, err = client.PrepareERC20Transfer(tokenAddr, fromAddress, req.ToAddress, amountWei)
	}

	if err != nil {
		log.Printf("Failed to prepare tx: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare transaction")
		return
	}

	var txData map[string]interface{}
	json.Unmarshal(txBytes, &txData)

	utils.SuccessResponse(c, http.StatusOK, "Transaction prepared", gin.H{
		"transaction": txData,
	})
}

// SubmitTransaction handles POST /api/wallet/submit-tx.
// Submits a signed transaction to the blockchain and records it in the database.
func (h *Handlers) SubmitTransaction(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	var req submitTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "signedTx, txType, tokenSymbol, toAddress, amount, and chainId are required")
		return
	}

	validTxTypes := map[string]bool{
		models.TxTypeSend: true, models.TxTypeReceive: true,
		models.TxTypeApprove: true, models.TxTypeEscrow: true,
		models.TxTypeSwap: true,
	}
	if !validTxTypes[req.TxType] {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid transaction type")
		return
	}

	tokenUpper := strings.ToUpper(req.TokenSymbol)
	if !isValidEthAddress(req.ToAddress) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid destination address format")
		return
	}
	if !isPositiveAmount(req.Amount) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Amount must be a positive number")
		return
	}

	client, chainName := h.BlockchainService.ClientForChainID(req.ChainID)
	if client == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain ID")
		return
	}

	signedTxBytes, err := hexutil.Decode(req.SignedTx)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid signed transaction format")
		return
	}

	// The chain credits a tx to whoever SIGNED it — not to the account the
	// prepare endpoints computed the nonce for. If those differ, the nonce
	// belongs to the wrong account and nodes accept the tx as a future-nonce
	// orphan that queues in the mempool forever: the app reports "sent",
	// nothing ever mines, and a retry rebuilds the byte-identical tx
	// (2026-07-08 Instant Pay incident). The expected signer is the address
	// the tx was prepared for: the declared active wallet (fromAddress) when
	// present, else the registered wallet. Receiving stays on the registered
	// wallet — this only governs who spends.
	expectedSigner := strings.TrimSpace(req.FromAddress)
	if expectedSigner != "" && !isValidEthAddress(expectedSigner) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid fromAddress format")
		return
	}
	if expectedSigner == "" {
		expectedSigner = user.WalletAddress
	}
	var parsedTx types.Transaction
	if err := parsedTx.UnmarshalBinary(signedTxBytes); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid signed transaction encoding")
		return
	}
	signerAddr, err := types.Sender(types.LatestSignerForChainID(parsedTx.ChainId()), &parsedTx)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Could not verify the transaction signature")
		return
	}
	if !strings.EqualFold(signerAddr.Hex(), expectedSigner) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf(
			"This transaction is signed by wallet %s, but it was prepared for wallet %s. Switch the app to that wallet (or re-register your current wallet) and try again.",
			signerAddr.Hex(), expectedSigner))
		return
	}

	txHash, err := client.SendRawTransaction(signedTxBytes)
	if err != nil {
		log.Printf("Failed to submit tx to %s: %v", chainName, err)
		// Surface the underlying RPC error to the client so the UI can show
		// something actionable ("nonce too low", "insufficient funds for
		// gas", "underpriced", "execution reverted: ...") instead of a
		// generic message. The raw error already excludes user secrets;
		// what's left (RPC method names, reason strings) is fine to leak.
		utils.ErrorResponse(c, http.StatusInternalServerError, fmt.Sprintf("Failed to submit transaction to blockchain on %s: %v", chainName, err))
		return
	}

	walletTx := models.WalletTransaction{
		UserID:      user.ID,
		TxHash:      txHash,
		TokenSymbol: tokenUpper,
		TxType:      req.TxType,
		// Record the true on-chain sender (may be the active device wallet,
		// not the registered one).
		FromAddress: signerAddr.Hex(),
		ToAddress:   req.ToAddress,
		Amount:      req.Amount,
		Status:      models.TxStatusPending,
		Chain:       chainName,
		ChainID:     req.ChainID,
	}

	if err := config.DB.Create(&walletTx).Error; err != nil {
		log.Printf("tx submitted (hash=%s) but failed to record in DB: %v", txHash, err)
		utils.SuccessResponse(c, http.StatusCreated, "Transaction submitted but failed to record", gin.H{
			"txHash": txHash,
		})
		return
	}

	// Distribute TP for swap and send transaction types.
	// Escrow TP is handled separately by the checkout handler on order resolution.
	if req.TxType == models.TxTypeSwap || req.TxType == models.TxTypeSend {
		systemFeeUSD := req.FeeUSD
		if systemFeeUSD <= 0 {
			// Fallback: estimate system fee from transaction amount
			amountFloat, _ := strconv.ParseFloat(req.Amount, 64)
			switch req.TxType {
			case models.TxTypeSwap:
				// System earns ~1% of swap amount (remainder of 2% platform fee after cashback+splits)
				systemFeeUSD = amountFloat * 0.01
			case models.TxTypeSend:
				// Nominal fee proxy for P2P sends (gas cost equivalent)
				systemFeeUSD = 0.01
			}
		}
		if systemFeeUSD > 0 {
			// Fee settlement (see settleFeeEarnings): idempotent on
			// req.TxType/walletTx.ID. Feature maps the wallet tx type to a fee
			// feature: a raw send is a withdrawal (70/30, earns TP); a swap is the
			// internal spot exchange (FeatureSpot, 70/30).
			feeFeature := models.FeatureWithdrawal
			if req.TxType == models.TxTypeSwap {
				feeFeature = models.FeatureSpot
			}
			if err := settleFeeEarnings(h.FeeService, services.FeeEvent{
				Feature:    feeFeature,
				Token:      tokenUpper,
				FeeUSD:     systemFeeUSD,
				UserID:     user.ID,
				SourceType: req.TxType,
				SourceID:   walletTx.ID,
			}); err != nil {
				log.Printf("TP distribution failed for %s tx %s: %v", req.TxType, walletTx.ID, err)
			}

		}
	}

	utils.SuccessResponse(c, http.StatusCreated, "Transaction submitted", gin.H{
		"transaction": walletTx,
	})

	// Poll the blockchain in the background until the tx is mined, then update status.
	go awaitAndConfirmTx(h.BlockchainService, txHash, walletTx.ID.String(), req.ChainID)
}

// BackfillPendingTransactions is called once at server startup. It finds all
// WalletTransactions with status "pending" and a non-empty txHash (submitted
// before receipt-polling was added) and checks each one against the chain.
func BackfillPendingTransactions(bs *services.BlockchainService) {
	var txns []models.WalletTransaction
	if err := config.DB.
		Where("status = ? AND tx_hash != ''", models.TxStatusPending).
		Find(&txns).Error; err != nil {
		log.Printf("BackfillPendingTransactions: query failed: %v", err)
		return
	}
	log.Printf("BackfillPendingTransactions: checking %d pending transactions", len(txns))
	for _, tx := range txns {
		client, _ := bs.ClientForChainID(tx.ChainID)
		if client == nil {
			continue
		}
		receipt, err := client.GetTransactionReceipt(tx.TxHash)
		if err != nil || receipt == nil {
			continue
		}
		newStatus := models.TxStatusConfirmed
		if receipt.Status == 0 {
			newStatus = models.TxStatusFailed
		}
		config.DB.Model(&tx).Update("status", newStatus)
	}
	log.Printf("BackfillPendingTransactions: done")
}

// awaitAndConfirmTx polls eth_getTransactionReceipt every 5 s for up to 5 min
// and updates the WalletTransaction status once the tx is mined.
func awaitAndConfirmTx(bs *services.BlockchainService, txHash, txID string, chainID int64) {
	client, _ := bs.ClientForChainID(chainID)
	if client == nil {
		return
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		receipt, err := client.GetTransactionReceipt(txHash)
		if err != nil || receipt == nil {
			// Not yet mined — keep polling.
			continue
		}
		newStatus := models.TxStatusConfirmed
		if receipt.Status == 0 {
			newStatus = models.TxStatusFailed
		}
		if dbErr := config.DB.Model(&models.WalletTransaction{}).
			Where("id = ?", txID).
			Update("status", newStatus).Error; dbErr != nil {
			log.Printf("awaitAndConfirmTx: db update failed for %s: %v", txID, dbErr)
		}
		return
	}
}

// GetTransactionHistory handles GET /api/wallet/transactions.
// Returns paginated transaction history for the authenticated user.
func (h *Handlers) GetTransactionHistory(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	// Parse pagination parameters.
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	txType := c.Query("type")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	// Build query.
	query := config.DB.Where("user_id = ?", user.ID)
	countQuery := config.DB.Model(&models.WalletTransaction{}).Where("user_id = ?", user.ID)

	if txType != "" {
		query = query.Where("tx_type = ?", txType)
		countQuery = countQuery.Where("tx_type = ?", txType)
	}

	chainIDFilter := c.Query("chainId")
	if chainIDFilter != "" {
		cid, _ := strconv.ParseInt(chainIDFilter, 10, 64)
		if cid > 0 {
			query = query.Where("chain_id = ?", cid)
			countQuery = countQuery.Where("chain_id = ?", cid)
		}
	}

	// Get total count.
	var total int64
	if err := countQuery.Count(&total).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to count transactions")
		return
	}

	// Get transactions.
	var transactions []models.WalletTransaction
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&transactions).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch transactions")
		return
	}

	totalPages := int(total) / limit
	if int(total)%limit != 0 {
		totalPages++
	}

	utils.SuccessResponse(c, http.StatusOK, "Transactions retrieved", gin.H{
		"transactions": transactions,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}

// GetSupportedTokens handles GET /api/wallet/supported-tokens.
// Returns the list of active tokens with their chain availability.
// If no tokens exist in the database yet, seeds default tokens and returns them.
func (h *Handlers) GetSupportedTokens(c *gin.Context) {
	var tokens []models.SupportedToken
	if err := config.DB.Where("is_active = ?", true).Order("symbol ASC").Find(&tokens).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch supported tokens")
		return
	}

	// Seed defaults if table is empty (first-time setup).
	if len(tokens) == 0 {
		tokens = seedDefaultTokens()
	}

	// Map to the response shape the frontend expects.
	result := make([]gin.H, 0, len(tokens))
	for _, t := range tokens {
		// Parse chains from JSON.
		var chains []string
		if err := json.Unmarshal(t.Chains, &chains); err != nil {
			chains = []string{}
		}
		result = append(result, gin.H{
			"symbol":    t.Symbol,
			"name":      t.Name,
			"decimals":  t.Decimals,
			"iconColor": t.IconColor,
			"chains":    chains,
		})
	}

	utils.SuccessResponse(c, http.StatusOK, "Supported tokens retrieved", result)
}

// seedDefaultTokens inserts the default token set into the database.
func seedDefaultTokens() []models.SupportedToken {
	type tokenDef struct {
		Symbol    string
		Name      string
		Decimals  int
		IconColor string
		Chains    []string
	}

	defaults := []tokenDef{
		{"USDT", "Tether", 6, "#26A17B", []string{"sonic", "bsc"}},
		{"USDC", "USD Coin", 6, "#2775CA", []string{"sonic", "bsc"}},
		{"MCGP", "MCG Protocol", 18, "#FFD700", []string{"sonic"}},
		// Robinhood Chain's canonical stablecoin (Paxos Global Dollar). Decimals
		// are read on-chain at balance time; 6 here is the fallback.
		{"USDG", "Global Dollar", 6, "#00C805", []string{"robinhood"}},
	}

	var created []models.SupportedToken
	for _, d := range defaults {
		chainsJSON, _ := json.Marshal(d.Chains)
		token := models.SupportedToken{
			ID:        uuid.New(),
			Symbol:    d.Symbol,
			Name:      d.Name,
			Decimals:  d.Decimals,
			IconColor: d.IconColor,
			Chains:    chainsJSON,
			IsActive:  true,
		}
		if err := config.DB.Create(&token).Error; err != nil {
			log.Printf("Failed to seed token %s: %v", d.Symbol, err)
			continue
		}
		created = append(created, token)
	}
	return created
}

// ConfirmSeedPhraseBackup handles POST /api/wallet/seed-phrase-backed-up.
// Marks the user's seed phrase as backed up.
func (h *Handlers) ConfirmSeedPhraseBackup(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if err := config.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("seed_phrase_backed_up", true).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update seed phrase backup status")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Seed phrase backup confirmed", gin.H{
		"seedPhraseBackedUp": true,
	})
}

// ResolveUsername handles GET /api/wallet/resolve/:username
// Looks up a user by username and returns their public wallet address.
func (h *Handlers) ResolveUsername(c *gin.Context) {
	caller := getUserFromContext(c)
	if caller == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	username := strings.TrimSpace(c.Param("username"))
	if username == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Username is required")
		return
	}

	// Prevent sending to self
	if strings.EqualFold(username, caller.Username) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot send to yourself")
		return
	}

	var target models.User
	if err := config.DB.Where("LOWER(username) = LOWER(?)", username).First(&target).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "User not found")
		return
	}

	if target.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "This user has not set up a wallet yet")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "User resolved", gin.H{
		"username":           target.Username,
		"name":               target.Name,
		"walletAddress":      target.WalletAddress,
		"verificationStatus": target.VerificationStatus,
	})
}

// --- non-EVM (family) wallet endpoints ---

type registerNonEVMRequest struct {
	WalletID string `json:"walletId" binding:"required"`
	ChainKey string `json:"chainKey" binding:"required"`
	Address  string `json:"address" binding:"required"`
}

// RegisterNonEVMAddress handles POST /api/wallet/nonevm/register.
// Associates a non-EVM chain address (e.g. Tron) with the authenticated user's wallet.
func (h *Handlers) RegisterNonEVMAddress(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req registerNonEVMRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if h.ChainRegistry == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	client, ok := h.ChainRegistry.ForKey(resolveNetwork(c), req.ChainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	if err := client.ValidateAddress(req.Address); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	rec := models.WalletAddress{
		UserID: user.ID, WalletID: req.WalletID, ChainKey: req.ChainKey,
		Family: client.Family(), Address: req.Address,
	}
	// upsert on (wallet_id, chain_key, user_id) — scope to owner so a caller
	// cannot overwrite another user's registered address by passing their walletId.
	if err := config.DB.Where("wallet_id = ? AND chain_key = ? AND user_id = ?", req.WalletID, req.ChainKey, user.ID).
		Assign(models.WalletAddress{Address: req.Address, Family: client.Family()}).
		FirstOrCreate(&rec).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "failed to save address")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "registered", gin.H{"address": rec.Address})
}

// GetNonEVMBalances handles GET /api/wallet/nonevm/balances.
// Returns balances for a non-EVM address (e.g. Tron TRX + TRC-20s).
// Query params: ?chain=tron&address=T...
func (h *Handlers) GetNonEVMBalances(c *gin.Context) {
	chainKey := c.Query("chain")
	network := resolveNetwork(c)
	addr := c.Query("address")
	if h.ChainRegistry == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	client, ok := h.ChainRegistry.ForKey(network, chainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	if err := client.ValidateAddress(addr); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid address")
		return
	}
	bals, err := client.GetBalances(c.Request.Context(), addr)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "balance read failed")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "ok", gin.H{"balances": enrichBalancesWithUSD(h.PriceService, bals)})
}

// enrichBalancesWithUSD attaches a per-token USD price + value to non-EVM
// balances using the shared CoinGecko price service (same source as EVM). It's
// best-effort: when the price service is unset or a symbol has no price, the
// balance is returned with usdPrice/usdValue = 0 rather than failing the read.
func enrichBalancesWithUSD(ps *services.PriceService, bals []chains.Balance) []balanceWithUSD {
	prices := map[string]services.PriceData{}
	if ps != nil && len(bals) > 0 {
		symbols := make([]string, 0, len(bals))
		for _, b := range bals {
			symbols = append(symbols, b.Symbol)
		}
		if pd, err := ps.GetPrices(symbols); err == nil {
			prices = pd
		}
	}
	out := make([]balanceWithUSD, 0, len(bals))
	for _, b := range bals {
		price := prices[strings.ToUpper(b.Symbol)].USD
		out = append(out, balanceWithUSD{
			Balance:  b,
			UsdPrice: price,
			UsdValue: rawToFloat(b.Raw, b.Decimals) * price,
		})
	}
	return out
}

type balanceWithUSD struct {
	chains.Balance
	UsdPrice float64 `json:"usdPrice"`
	UsdValue float64 `json:"usdValue"`
}

// rawToFloat converts an integer base-unit string to a human float given the
// token's decimals (e.g. "2000000000", 6 → 2000).
func rawToFloat(raw string, decimals int) float64 {
	bi, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return 0
	}
	div := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(bi), div).Float64()
	return f
}

// PrepareNonEVMTx handles POST /api/wallet/nonevm/prepare-tx.
// Estimates fee and builds an unsigned tx for a non-EVM send.
// Query param: ?chain=tron
func (h *Handlers) PrepareNonEVMTx(c *gin.Context) {
	var req chains.TransferRequest
	chainKey := c.Query("chain")
	network := resolveNetwork(c)
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if h.ChainRegistry == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	client, ok := h.ChainRegistry.ForKey(network, chainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	fee, err := client.EstimateFee(c.Request.Context(), req)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "fee estimate failed")
		return
	}
	if !fee.HasEnoughGas {
		utils.ErrorResponse(c, http.StatusPaymentRequired, fee.Note)
		return
	}
	prepared, err := client.PrepareTransfer(c.Request.Context(), req)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "prepared", gin.H{"prepared": prepared, "fee": fee})
}

type submitNonEVMRequest struct {
	Prepared chains.PreparedTx  `json:"prepared" binding:"required"`
	Sigs     []chains.Signature `json:"sigs" binding:"required"`
	From     string             `json:"from"`
	Amount   string             `json:"amount"`
	Symbol   string             `json:"symbol"`
}

// SubmitNonEVMTx handles POST /api/wallet/nonevm/submit-tx.
// Assembles a signed tx from payloads + signatures and broadcasts it.
// Query param: ?chain=tron
func (h *Handlers) SubmitNonEVMTx(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	chainKey := c.Query("chain")
	network := resolveNetwork(c)
	var req submitNonEVMRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if h.ChainRegistry == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	client, ok := h.ChainRegistry.ForKey(network, chainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	// The audit row's Address comes from the client, so verify req.From is an
	// address THIS user registered for this chain before we broadcast + record.
	// Without this a caller could broadcast their own tx while logging another
	// user's address, poisoning the activity/audit trail (Tampering/Repudiation).
	// Registration is idempotent and runs on every wallet load, so a legitimate
	// sender is always registered by the time they send.
	var owned int64
	if err := config.DB.Model(&models.WalletAddress{}).
		Where("user_id = ? AND chain_key = ? AND address = ?", user.ID, chainKey, req.From).
		Count(&owned).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "failed to verify sender")
		return
	}
	if owned == 0 {
		utils.ErrorResponse(c, http.StatusForbidden, "sender address is not registered to this account")
		return
	}

	hash, err := client.AssembleAndBroadcast(c.Request.Context(), req.Prepared, req.Sigs)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, err.Error())
		return
	}

	// Record the send as a pending activity row so the non-EVM reconciler can
	// finalize it against on-chain truth (and it becomes an audit trail). The
	// broadcast already succeeded, so a failed insert must not fail the request.
	amount := req.Amount
	if amount == "" {
		amount = "0"
	}
	activity := models.WalletActivity{
		ChainKey:  chainKey,
		Network:   network,
		Address:   req.From, // verified above to be a user-registered address
		Hash:      hash,
		Direction: "out",
		AmountRaw: amount,
		Symbol:    req.Symbol,
		Status:    "pending",
	}
	if err := config.DB.Create(&activity).Error; err != nil {
		log.Printf("nonevm: failed to record activity for tx %s: %v", hash, err)
	}

	utils.SuccessResponse(c, http.StatusOK, "broadcast", gin.H{"txHash": hash})
}

// nonEVMTxHashRe matches Tron and BTC txids (32-byte hex). Validated before
// the hash is interpolated into an upstream provider URL path.
var nonEVMTxHashRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// solanaSigRe matches a base58-encoded 64-byte Solana signature (~87–88 chars).
// Solana txids are base58, not hex, so they need their own gate.
var solanaSigRe = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{80,90}$`)

// validNonEVMTxHash validates a tx hash per family before it is used in a status
// lookup. Tron/BTC interpolate the hash into an upstream provider URL path (hex
// only); Solana passes a base58 signature to solana-go.
func validNonEVMTxHash(family, hash string) bool {
	if family == "solana" {
		return solanaSigRe.MatchString(hash)
	}
	return nonEVMTxHashRe.MatchString(hash)
}

// GetNonEVMTxStatus handles GET /api/wallet/nonevm/tx-status.
// Returns the status of a non-EVM transaction by hash.
// Query params: ?chain=tron&hash=<txHash>
func (h *Handlers) GetNonEVMTxStatus(c *gin.Context) {
	chainKey := c.Query("chain")
	network := resolveNetwork(c)
	hash := c.Query("hash")
	if h.ChainRegistry == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	client, ok := h.ChainRegistry.ForKey(network, chainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	// Validate per family AFTER resolving the client (hex for Tron/BTC, base58 for Solana).
	if !validNonEVMTxHash(client.Family(), hash) {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid tx hash")
		return
	}
	st, err := client.GetTxStatus(c.Request.Context(), hash)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "status lookup failed")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "ok", gin.H{"status": st})
}

// GetNonEVMHistory handles GET /api/wallet/nonevm/history.
// Returns recent on-chain transfers (native + tokens) for a non-EVM address,
// covering both sends and receives. Query params: ?chain=tron&address=...&cursor=
func (h *Handlers) GetNonEVMHistory(c *gin.Context) {
	chainKey := c.Query("chain")
	network := resolveNetwork(c)
	addr := c.Query("address")
	cursor := c.Query("cursor")
	if h.ChainRegistry == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	client, ok := h.ChainRegistry.ForKey(network, chainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	if err := client.ValidateAddress(addr); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid address")
		return
	}
	items, next, err := client.History(c.Request.Context(), addr, cursor)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "history lookup failed")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "ok", gin.H{"items": items, "next": next})
}

// mintResolver is the optional capability a ChainClient exposes to resolve a
// token contract/mint into its on-chain metadata (currently Solana). Chains
// without it (Tron, BTC) simply don't implement it and the route 400s.
type mintResolver interface {
	ResolveMint(ctx context.Context, mint string) (decimals uint8, ok bool, err error)
}

// GetNonEVMTokenInfo handles GET /api/wallet/nonevm/token-info.
// Resolves a user-supplied token mint/contract into its decimals so the app can
// import an arbitrary token (e.g. any SPL token on Solana).
// Query params: ?chain=solana&mint=<mint address>
func (h *Handlers) GetNonEVMTokenInfo(c *gin.Context) {
	chainKey := c.Query("chain")
	network := resolveNetwork(c)
	mint := c.Query("mint")
	if h.ChainRegistry == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	client, ok := h.ChainRegistry.ForKey(network, chainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	resolver, ok := client.(mintResolver)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "token import not supported on this chain")
		return
	}
	decimals, found, err := resolver.ResolveMint(c.Request.Context(), mint)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if !found {
		utils.ErrorResponse(c, http.StatusNotFound, "no token found at that address")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "ok", gin.H{
		"chain": chainKey, "mint": mint, "decimals": decimals, "tokenAddress": mint,
	})
}
