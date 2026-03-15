# Wallet System Completion — Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the wallet system by generalizing the blockchain client to multi-chain, wiring up real transaction preparation/submission, adding chain awareness to the API, and writing comprehensive tests.

**Architecture:** Rename `SonicClient` → `EVMClient` as a chain-agnostic Ethereum RPC wrapper. `BlockchainService` manages a registry of `EVMClient` instances keyed by chain name. Handlers accept a `chainId` parameter to route requests to the correct client. `WalletTransaction` gains a `chain` column for multi-chain tx history.

**Tech Stack:** Go 1.24, go-ethereum/ethclient, Gin, GORM/PostgreSQL, standard `testing` package

**Working directory:** All file paths are relative to `tsa-dev/tsa-api-go/`. All `cd tsa-api-go` commands assume cwd is `tsa-dev/`. All `git` commands run from `tsa-dev/` (the git root).

---

## File Structure

**Modify:**
- `internal/blockchain/client.go` — Rename `SonicClient` → `EVMClient`, make constructor chain-agnostic
- `internal/blockchain/token.go` — Update receiver type from `SonicClient` → `EVMClient`, add native transfer prep
- `internal/blockchain/client_test.go` — Update to use `EVMClient`
- `internal/blockchain/token_test.go` — Update to use `EVMClient`
- `internal/services/blockchain_service.go` — Multi-client registry, chain lookup by chainId
- `internal/config/config.go` — Add BSC config fields + `ChainConfigs` struct
- `internal/handlers/wallet_handler.go` — Wire real blockchain calls, accept `chainId` param
- `internal/models/wallet_transaction.go` — Add `Chain` field

**Create:**
- `internal/handlers/wallet_handler_test.go` — Handler unit tests with mocked blockchain

---

## Chunk 1: Generalize Blockchain Client

### Task 1: Rename SonicClient → EVMClient

**Files:**
- Modify: `internal/blockchain/client.go`

- [ ] **Step 1: Rename struct and constructor**

Replace `SonicClient` struct with `EVMClient`. Constructor takes `rpcURL string` and `chainID int64` directly instead of the full `*config.Config`:

```go
type EVMClient struct {
	Client  *ethclient.Client
	chainID *big.Int
}

func NewEVMClient(rpcURL string, chainID int64) (*EVMClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RPC at %s: %w", rpcURL, err)
	}

	return &EVMClient{
		Client:  client,
		chainID: big.NewInt(chainID),
	}, nil
}
```

Update all method receivers from `(sc *SonicClient)` to `(c *EVMClient)`. Remove the `Config` field and the `config` import.

- [ ] **Step 2: Build to verify**

Run: `cd tsa-api-go && go build ./...`
Expected: Compile errors in files that reference `SonicClient` — that's expected, we fix those next.

- [ ] **Step 3: Commit**

```bash
git add internal/blockchain/client.go
git commit -m "Rename SonicClient to EVMClient for multi-chain support"
```

### Task 2: Update token.go for EVMClient

**Files:**
- Modify: `internal/blockchain/token.go`

- [ ] **Step 1: Update receiver types**

Change all `(sc *SonicClient)` receivers to `(c *EVMClient)`. Remove `GetSupportedTokens()` method (token config is now DB-driven via `supported_tokens` table, not hardcoded in the client). Remove the `Config` field references.

**Keep `GetAllBalances` for now** — it is called by `wallet_handler.go:GetWalletBalances`. It will be removed in Task 9 when the handler is rewritten for multi-chain. Update its signature to accept a `[]TokenInfo` parameter instead of calling the removed `GetSupportedTokens()`:

```go
func (c *EVMClient) GetAllBalances(walletAddress string, tokens []TokenInfo) (map[string]*big.Int, error) {
	balances := make(map[string]*big.Int)
	for _, token := range tokens {
		balance, err := c.GetTokenBalance(token.Address, walletAddress)
		if err != nil {
			balances[token.Symbol] = big.NewInt(0)
			continue
		}
		balances[token.Symbol] = balance
	}
	return balances, nil
}
```

- [ ] **Step 2: Add PrepareNativeTransfer method**

Add a method to build unsigned native token (S, tBNB) transfers:

```go
func (c *EVMClient) PrepareNativeTransfer(from, to string, amountWei *big.Int) ([]byte, error) {
	fromAddr := common.HexToAddress(from)
	toAddr := common.HexToAddress(to)

	nonce, err := c.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

	gasPrice, err := c.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}

	gasLimit, err := c.EstimateGas(ethereum.CallMsg{
		From:  fromAddr,
		To:    &toAddr,
		Value: amountWei,
	})
	if err != nil {
		gasLimit = 21000 // standard ETH transfer
	}

	tx := UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       to,
		Value:    amountWei.String(),
		Data:     "",
		ChainID:  c.chainID.String(),
	}

	return json.Marshal(tx)
}
```

- [ ] **Step 3: Build to verify**

Run: `cd tsa-api-go && go build ./internal/blockchain/...`
Expected: PASS (blockchain package compiles)

- [ ] **Step 4: Commit**

```bash
git add internal/blockchain/token.go
git commit -m "Update token.go to EVMClient, add native transfer support"
```

### Task 3: Update blockchain tests

**Files:**
- Modify: `internal/blockchain/client_test.go`
- Modify: `internal/blockchain/token_test.go`

- [ ] **Step 1: Update client_test.go**

Replace `NewSonicClient(cfg)` calls with `NewEVMClient(rpcURL, chainID)`. Remove `testConfig()` helper, use direct values:

```go
const (
	testRPCURL  = "https://rpc.testnet.soniclabs.com"
	testChainID = 14601
	testMCGP    = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21"
	testSystem  = "0xaF326D5D242C9A55590540f14658adDDd3586A8d"
)

func TestNewEVMClient(t *testing.T) {
	client, err := NewEVMClient(testRPCURL, testChainID)
	if err != nil {
		t.Fatalf("NewEVMClient failed: %v", err)
	}
	if client.ChainID().Int64() != testChainID {
		t.Errorf("expected chain ID %d, got %d", testChainID, client.ChainID().Int64())
	}
}
```

Update `TestGetBalance`, `TestGetNonce`, `TestSuggestGasPrice` similarly.

- [ ] **Step 2: Update token_test.go**

Replace `NewSonicClient` with `NewEVMClient`. Update `TestGetSupportedTokens` → remove (method deleted). Keep `TestGetTokenBalance`, `TestGetAllBalances` but pass token list explicitly (or remove `TestGetAllBalances` since `GetAllBalances` is removed — it depended on `GetSupportedTokens`).

Since `GetAllBalances` and `GetSupportedTokens` are removed from `EVMClient`, remove their tests. Keep `TestGetTokenBalance`, `TestPrepareERC20Transfer`, `TestPrepareERC20Approve`. Add `TestPrepareNativeTransfer`.

- [ ] **Step 3: Run tests**

Run: `cd tsa-api-go && go test ./internal/blockchain/ -v -count=1`
Expected: All tests pass (these hit real testnet RPC)

- [ ] **Step 4: Commit**

```bash
git add internal/blockchain/client_test.go internal/blockchain/token_test.go
git commit -m "Update blockchain tests for EVMClient"
```

### Task 4: Add chain config to config.go

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add ChainConfig struct and BSC fields**

```go
type ChainConfig struct {
	Name           string
	RPCURL         string
	ChainID        int64
	NativeCurrency string // "S", "tBNB", etc.
}

type Config struct {
	// ... existing fields ...

	// Chain configurations (keyed by chain name)
	Chains map[string]ChainConfig

	// Token contract addresses per chain (chain:symbol → address)
	// e.g. "sonic:MCGP" → "0x517..."
	TokenAddresses map[string]string

	// Sonic blockchain (kept for backward compat during transition)
	SonicRPCURL         string
	SonicChainID        int64
	MCGPTokenAddress    string
	USDTTokenAddress    string
	USDCTokenAddress    string
	SystemWalletAddress string

	// BSC blockchain
	BSCRPCURL  string
	BSCChainID int64
}
```

In `Load()`, populate `Chains` map and `TokenAddresses`:

```go
func Load() *Config {
	cfg := &Config{
		// ... existing fields ...

		// BSC
		BSCRPCURL:  getEnv("BSC_RPC_URL", "https://data-seed-prebsc-1-s1.binance.org:8545"),
		BSCChainID: getEnvInt64("BSC_CHAIN_ID", 97),
	}

	cfg.Chains = map[string]ChainConfig{
		"sonic": {Name: "Sonic Network", RPCURL: cfg.SonicRPCURL, ChainID: cfg.SonicChainID, NativeCurrency: "S"},
		"bsc":   {Name: "BNB Smart Chain", RPCURL: cfg.BSCRPCURL, ChainID: cfg.BSCChainID, NativeCurrency: "tBNB"},
	}

	cfg.TokenAddresses = map[string]string{}
	if cfg.MCGPTokenAddress != "" {
		cfg.TokenAddresses["sonic:MCGP"] = cfg.MCGPTokenAddress
	}
	if cfg.USDTTokenAddress != "" {
		cfg.TokenAddresses["sonic:USDT"] = cfg.USDTTokenAddress
	}
	if cfg.USDCTokenAddress != "" {
		cfg.TokenAddresses["sonic:USDC"] = cfg.USDCTokenAddress
	}
	// BSC token addresses
	bscUSDT := getEnv("BSC_USDT_ADDRESS", "")
	if bscUSDT != "" {
		cfg.TokenAddresses["bsc:USDT"] = bscUSDT
	}
	bscUSDC := getEnv("BSC_USDC_ADDRESS", "")
	if bscUSDC != "" {
		cfg.TokenAddresses["bsc:USDC"] = bscUSDC
	}

	return cfg
}
```

- [ ] **Step 2: Build to verify**

Run: `cd tsa-api-go && go build ./...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "Add multi-chain config with BSC support"
```

### Task 5: Update BlockchainService to multi-client registry

**Files:**
- Modify: `internal/services/blockchain_service.go`

- [ ] **Step 1: Replace single client with registry**

```go
type BlockchainService struct {
	clients map[string]*blockchain.EVMClient // keyed by chain name: "sonic", "bsc"
	cfg     *config.Config
}

func NewBlockchainService(cfg *config.Config) *BlockchainService {
	bs := &BlockchainService{
		clients: make(map[string]*blockchain.EVMClient),
		cfg:     cfg,
	}

	for name, chain := range cfg.Chains {
		client, err := blockchain.NewEVMClient(chain.RPCURL, chain.ChainID)
		if err != nil {
			log.Printf("Warning: failed to connect to %s (chain ID %d): %v", name, chain.ChainID, err)
			continue
		}
		bs.clients[name] = client
		log.Printf("%s blockchain client connected (chain ID: %d, RPC: %s)", name, chain.ChainID, chain.RPCURL)
	}

	return bs
}

// ClientForChain returns the EVMClient for the given chain name.
func (bs *BlockchainService) ClientForChain(chain string) *blockchain.EVMClient {
	return bs.clients[chain]
}

// ClientForChainID returns the EVMClient matching the given numeric chain ID.
func (bs *BlockchainService) ClientForChainID(chainID int64) (*blockchain.EVMClient, string) {
	for name, chain := range bs.cfg.Chains {
		if chain.ChainID == chainID {
			return bs.clients[name], name
		}
	}
	return nil, ""
}

// Client returns the Sonic client for backward compatibility.
func (bs *BlockchainService) Client() *blockchain.EVMClient {
	return bs.clients["sonic"]
}

// TokenAddress returns the contract address for a token on a chain.
func (bs *BlockchainService) TokenAddress(chain, symbol string) string {
	return bs.cfg.TokenAddresses[chain+":"+symbol]
}
```

Update `GetTransactionReceipt`, `GetGasPrices`, `VerifyTransaction` to accept chain and route to the correct client via `ClientForChain`.

- [ ] **Step 2: Build to verify**

Run: `cd tsa-api-go && go build ./...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/services/blockchain_service.go
git commit -m "Update BlockchainService to multi-chain client registry"
```

---

## Chunk 2: Wire Real Blockchain Calls in Handlers

### Task 6: Add chain field to WalletTransaction model

**Files:**
- Modify: `internal/models/wallet_transaction.go`

- [ ] **Step 1: Add Chain and ChainID fields**

```go
type WalletTransaction struct {
	// ... existing fields ...
	Chain   string `gorm:"not null;default:'sonic'" json:"chain"`   // sonic, bsc
	ChainID int64  `gorm:"not null;default:14601" json:"chainId"`
	// ... rest unchanged ...
}
```

- [ ] **Step 2: Build to verify**

Run: `cd tsa-api-go && go build ./...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/models/wallet_transaction.go
git commit -m "Add chain fields to WalletTransaction model"
```

### Task 7: Wire real PrepareSendTransaction

**Files:**
- Modify: `internal/handlers/wallet_handler.go`

- [ ] **Step 1: Add chainId to request structs**

```go
type prepareTxRequest struct {
	TokenSymbol string `json:"tokenSymbol" binding:"required"`
	ToAddress   string `json:"toAddress" binding:"required"`
	Amount      string `json:"amount" binding:"required"`
	ChainID     int64  `json:"chainId" binding:"required"`
}

type submitTxRequest struct {
	SignedTx    string `json:"signedTx" binding:"required"`
	TxType      string `json:"txType" binding:"required"`
	TokenSymbol string `json:"tokenSymbol" binding:"required"`
	ToAddress   string `json:"toAddress" binding:"required"`
	Amount      string `json:"amount" binding:"required"`
	ChainID     int64  `json:"chainId" binding:"required"`
}
```

- [ ] **Step 2: Replace stub in PrepareSendTransaction with real blockchain call**

```go
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

	// Look up chain client
	client, chainName := h.BlockchainService.ClientForChainID(req.ChainID)
	if client == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain ID")
		return
	}

	// Parse amount to big.Int based on token decimals
	decimals := 18
	if tokenUpper == "USDT" || tokenUpper == "USDC" {
		decimals = 6
	}
	amountWei, ok := parseTokenAmount(req.Amount, decimals)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid amount format")
		return
	}

	var txBytes []byte
	var err error

	// Native token transfer (S, tBNB) vs ERC-20
	if tokenUpper == "S" || tokenUpper == "TBNB" {
		txBytes, err = client.PrepareNativeTransfer(user.WalletAddress, req.ToAddress, amountWei)
	} else {
		tokenAddr := h.BlockchainService.TokenAddress(chainName, tokenUpper)
		if tokenAddr == "" {
			utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Token %s not configured on %s", tokenUpper, chainName))
			return
		}
		txBytes, err = client.PrepareERC20Transfer(tokenAddr, user.WalletAddress, req.ToAddress, amountWei)
	}

	if err != nil {
		log.Printf("Failed to prepare tx: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare transaction")
		return
	}

	// Return the unsigned tx as JSON
	var txData map[string]interface{}
	json.Unmarshal(txBytes, &txData)

	utils.SuccessResponse(c, http.StatusOK, "Transaction prepared", gin.H{
		"transaction": txData,
	})
}
```

- [ ] **Step 3: Add parseTokenAmount helper**

```go
func parseTokenAmount(amount string, decimals int) (*big.Int, bool) {
	// Parse decimal string to big.Int with proper scaling
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
```

- [ ] **Step 4: Build to verify**

Run: `cd tsa-api-go && go build ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/wallet_handler.go
git commit -m "Wire real blockchain calls in PrepareSendTransaction"
```

### Task 8: Wire real SubmitTransaction

**Files:**
- Modify: `internal/handlers/wallet_handler.go`

- [ ] **Step 1: Replace mock submission with real blockchain call**

```go
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

	// Look up chain client
	client, chainName := h.BlockchainService.ClientForChainID(req.ChainID)
	if client == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain ID")
		return
	}

	// Decode hex-encoded signed tx and submit to chain
	signedTxBytes, err := hexutil.Decode(req.SignedTx)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid signed transaction format")
		return
	}

	txHash, err := client.SendRawTransaction(signedTxBytes)
	if err != nil {
		log.Printf("Failed to submit tx to %s: %v", chainName, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to submit transaction to blockchain")
		return
	}

	walletTx := models.WalletTransaction{
		UserID:      user.ID,
		TxHash:      txHash,
		TokenSymbol: tokenUpper,
		TxType:      req.TxType,
		FromAddress: user.WalletAddress,
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

	utils.SuccessResponse(c, http.StatusCreated, "Transaction submitted", gin.H{
		"transaction": walletTx,
	})
}
```

This requires adding the `hexutil` import: `"github.com/ethereum/go-ethereum/common/hexutil"`

- [ ] **Step 2: Build to verify**

Run: `cd tsa-api-go && go build ./...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/handlers/wallet_handler.go
git commit -m "Wire real blockchain submission in SubmitTransaction"
```

### Task 9: Update GetWalletBalances for multi-chain

**Files:**
- Modify: `internal/handlers/wallet_handler.go`

- [ ] **Step 1: Accept optional chainId query param, query all chains**

```go
func (h *Handlers) GetWalletBalances(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	chainIDParam := c.Query("chainId")

	// Determine which chains to query
	type chainQuery struct {
		name   string
		cfg    config.ChainConfig
	}
	var chains []chainQuery

	if chainIDParam != "" {
		cid, err := strconv.ParseInt(chainIDParam, 10, 64)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid chainId")
			return
		}
		for name, cfg := range h.Config.Chains {
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
		for name, cfg := range h.Config.Chains {
			chains = append(chains, chainQuery{name, cfg})
		}
	}

	// Fetch supported tokens from DB to know which tokens exist on which chains
	var supportedTokens []models.SupportedToken
	config.DB.Where("is_active = ?", true).Find(&supportedTokens)

	result := make(map[string]map[string]string) // chain → symbol → balance

	for _, cq := range chains {
		client := h.BlockchainService.ClientForChain(cq.name)
		chainBalances := make(map[string]string)

		// Native balance
		if client != nil {
			if nativeBal, err := client.GetBalance(user.WalletAddress); err == nil {
				chainBalances[cq.cfg.NativeCurrency] = formatTokenBalance(nativeBal, 18)
			}
		}

		// ERC-20 token balances for tokens on this chain
		for _, tok := range supportedTokens {
			var tokenChains []string
			json.Unmarshal(tok.Chains, &tokenChains)
			for _, tc := range tokenChains {
				if tc == cq.name {
					tokenAddr := h.BlockchainService.TokenAddress(cq.name, tok.Symbol)
					if tokenAddr != "" && client != nil {
						if bal, err := client.GetTokenBalance(tokenAddr, user.WalletAddress); err == nil {
							chainBalances[tok.Symbol] = formatTokenBalance(bal, tok.Decimals)
						} else {
							chainBalances[tok.Symbol] = "0"
						}
					} else {
						chainBalances[tok.Symbol] = "0"
					}
				}
			}
		}

		result[cq.name] = chainBalances
	}

	utils.SuccessResponse(c, http.StatusOK, "Wallet balances retrieved", gin.H{
		"walletAddress": user.WalletAddress,
		"balances":      result,
	})
}
```

Note: `NativeCurrency` was already added to `ChainConfig` in Task 4.

Also, in Task 9, **remove `GetAllBalances` from `token.go`** — it is no longer needed since this handler iterates tokens itself. Remove the method and its test in `token_test.go`.

- [ ] **Step 2: Update GetTransactionHistory to filter by chain**

Add chain filter to the query:

```go
chainID := c.Query("chainId")
if chainID != "" {
	cid, _ := strconv.ParseInt(chainID, 10, 64)
	if cid > 0 {
		query = query.Where("chain_id = ?", cid)
		countQuery = countQuery.Where("chain_id = ?", cid)
	}
}
```

- [ ] **Step 3: Remove hardcoded SupportedTokens map usage**

Replace `models.SupportedTokens[tokenUpper]` checks in PrepareSendTransaction and SubmitTransaction with a DB query or just allow any symbol (the token address lookup will fail if the token doesn't exist on the chain). For the validation, query the `supported_tokens` table:

```go
func isTokenSupported(symbol string) bool {
	var count int64
	config.DB.Model(&models.SupportedToken{}).Where("symbol = ? AND is_active = ?", symbol, true).Count(&count)
	return count > 0
}
```

Use `isTokenSupported(tokenUpper)` instead of `models.SupportedTokens[tokenUpper]`. Native tokens (S, tBNB) should bypass this check.

- [ ] **Step 4: Build to verify**

Run: `cd tsa-api-go && go build ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/wallet_handler.go internal/config/config.go
git commit -m "Update balances and tx history for multi-chain support"
```

---

## Chunk 3: Handler Tests

### Task 10: Write wallet handler unit tests

**Files:**
- Create: `internal/handlers/wallet_handler_test.go` (no existing test file for handlers)

These tests use httptest + gin test mode with a SQLite in-memory database. The `BlockchainService` is initialized with no connected clients (nil clients map), so handler tests exercise validation paths. Blockchain integration is tested separately in `internal/blockchain/` tests that hit real testnet RPCs.

- [ ] **Step 1: Create test setup**

```go
package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	db.AutoMigrate(&models.User{}, &models.Wallet{}, &models.WalletTransaction{}, &models.SupportedToken{})
	config.DB = db
	return db
}

func setupTestHandlers(t *testing.T) *Handlers {
	t.Helper()
	return &Handlers{
		BlockchainService: &services.BlockchainService{},
		Config:            config.Load(),
	}
}

func createTestUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	user := &models.User{
		ID:       uuid.New(),
		Name:     "Test User",
		Username: "testuser",
		Email:    "test@example.com",
		Password: "$2a$10$fakehash",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return user
}

// withUser sets a user in the gin context (simulating auth middleware)
func withUser(c *gin.Context, user *models.User) {
	c.Set("user", user)
}
```

Note: This requires adding `gorm.io/driver/sqlite` as a test dependency:
```bash
cd tsa-api-go && go get gorm.io/driver/sqlite
```

- [ ] **Step 2: Write RegisterWalletAddress tests**

```go
func TestRegisterWalletAddress_Valid(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user)

	body, _ := json.Marshal(registerWalletRequest{
		WalletAddress: "0x0000000000000000000000000000000000000001",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.RegisterWalletAddress(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterWalletAddress_InvalidFormat(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user)

	body, _ := json.Marshal(registerWalletRequest{WalletAddress: "not-an-address"})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.RegisterWalletAddress(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestRegisterWalletAddress_Duplicate(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	addr := "0x0000000000000000000000000000000000000001"

	// Create first user with wallet
	user1 := createTestUser(t, db)
	db.Model(&models.User{}).Where("id = ?", user1.ID).Update("wallet_address", addr)

	// Create second user trying same address
	user2 := &models.User{
		ID: uuid.New(), Name: "User 2", Username: "user2",
		Email: "user2@example.com", Password: "$2a$10$fakehash",
	}
	db.Create(user2)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user2)

	body, _ := json.Marshal(registerWalletRequest{WalletAddress: addr})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.RegisterWalletAddress(c)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}
```

- [ ] **Step 3: Write PrepareSendTransaction validation tests**

```go
func TestPrepareTx_InvalidAddress(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("wallet_address", "0x0000000000000000000000000000000000000001")
	user.WalletAddress = "0x0000000000000000000000000000000000000001"

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user)

	body, _ := json.Marshal(map[string]interface{}{
		"tokenSymbol": "USDT",
		"toAddress":   "invalid",
		"amount":      "10",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PrepareSendTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestPrepareTx_InvalidAmount(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("wallet_address", "0x0000000000000000000000000000000000000001")
	user.WalletAddress = "0x0000000000000000000000000000000000000001"

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user)

	body, _ := json.Marshal(map[string]interface{}{
		"tokenSymbol": "USDT",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "-5",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PrepareSendTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestPrepareTx_NoWallet(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db) // no wallet address

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user)

	body, _ := json.Marshal(map[string]interface{}{
		"tokenSymbol": "USDT",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "10",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PrepareSendTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
```

- [ ] **Step 4: Write ConfirmSeedPhraseBackup test**

```go
func TestConfirmSeedPhraseBackup(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user)
	c.Request, _ = http.NewRequest("POST", "/", nil)

	h.ConfirmSeedPhraseBackup(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Verify DB updated
	var updated models.User
	db.First(&updated, "id = ?", user.ID)
	if !updated.SeedPhraseBackedUp {
		t.Error("expected SeedPhraseBackedUp to be true")
	}
}
```

- [ ] **Step 5: Write GetTransactionHistory test**

```go
func TestGetTransactionHistory(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)

	// Create test transactions
	for i := 0; i < 3; i++ {
		db.Create(&models.WalletTransaction{
			UserID:      user.ID,
			TxHash:      fmt.Sprintf("0x%040d", i),
			TokenSymbol: "USDT",
			TxType:      models.TxTypeSend,
			FromAddress: "0x0000000000000000000000000000000000000001",
			ToAddress:   "0x0000000000000000000000000000000000000002",
			Amount:      "10",
			Status:      models.TxStatusPending,
			Chain:       "sonic",
			ChainID:     14601,
		})
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user)
	c.Request, _ = http.NewRequest("GET", "/?page=1&limit=10", nil)

	h.GetTransactionHistory(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	txs := data["transactions"].([]interface{})
	if len(txs) != 3 {
		t.Errorf("expected 3 transactions, got %d", len(txs))
	}
}
```

- [ ] **Step 6: Write GetSupportedTokens test**

```go
func TestGetSupportedTokens_AutoSeed(t *testing.T) {
	setupTestDB(t)
	h := setupTestHandlers(t)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("GET", "/", nil)

	h.GetSupportedTokens(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].([]interface{})
	if len(data) < 3 {
		t.Errorf("expected at least 3 seeded tokens, got %d", len(data))
	}
}
```

- [ ] **Step 7: Write SubmitTransaction validation tests**

```go
func TestSubmitTx_InvalidSignedTx(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("wallet_address", "0x0000000000000000000000000000000000000001")
	user.WalletAddress = "0x0000000000000000000000000000000000000001"

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user)

	body, _ := json.Marshal(map[string]interface{}{
		"signedTx":    "not-valid-hex",
		"txType":      "send",
		"tokenSymbol": "USDT",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "10",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid hex, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitTx_InvalidTxType(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("wallet_address", "0x0000000000000000000000000000000000000001")
	user.WalletAddress = "0x0000000000000000000000000000000000000001"

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user)

	body, _ := json.Marshal(map[string]interface{}{
		"signedTx":    "0xdeadbeef",
		"txType":      "invalid_type",
		"tokenSymbol": "USDT",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "10",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid tx type, got %d", w.Code)
	}
}

func TestSubmitTx_UnsupportedChain(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("wallet_address", "0x0000000000000000000000000000000000000001")
	user.WalletAddress = "0x0000000000000000000000000000000000000001"

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withUser(c, user)

	body, _ := json.Marshal(map[string]interface{}{
		"signedTx":    "0xdeadbeef",
		"txType":      "send",
		"tokenSymbol": "USDT",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "10",
		"chainId":     99999,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unsupported chain, got %d", w.Code)
	}
}
```

- [ ] **Step 8: Add sqlite dependency and run tests**

Run:
```bash
cd tsa-api-go && go get gorm.io/driver/sqlite && go mod tidy
go test ./internal/handlers/ -v -count=1
```
Expected: All PASS

- [ ] **Step 9: Commit**

```bash
git add internal/handlers/wallet_handler_test.go go.mod go.sum
git commit -m "Add wallet handler unit tests"
```

---

## Chunk 4: Integration Test + Cleanup

### Task 11: Write parseTokenAmount unit tests

**Files:**
- Modify: `internal/handlers/wallet_handler_test.go`

- [ ] **Step 1: Add tests for the amount parser**

```go
func TestParseTokenAmount(t *testing.T) {
	tests := []struct {
		amount   string
		decimals int
		wantStr  string
		wantOK   bool
	}{
		{"1", 18, "1000000000000000000", true},
		{"0.5", 18, "500000000000000000", true},
		{"10.5", 6, "10500000", true},
		{"0", 18, "", false},     // zero not positive
		{"-1", 18, "", false},    // negative
		{"abc", 18, "", false},   // not a number
	}

	for _, tt := range tests {
		result, ok := parseTokenAmount(tt.amount, tt.decimals)
		if ok != tt.wantOK {
			t.Errorf("parseTokenAmount(%q, %d): ok=%v, want %v", tt.amount, tt.decimals, ok, tt.wantOK)
			continue
		}
		if ok && result.String() != tt.wantStr {
			t.Errorf("parseTokenAmount(%q, %d) = %s, want %s", tt.amount, tt.decimals, result.String(), tt.wantStr)
		}
	}
}
```

- [ ] **Step 2: Run all handler tests**

Run: `cd tsa-api-go && go test ./internal/handlers/ -v -count=1`
Expected: All PASS

- [ ] **Step 3: Commit**

```bash
git add internal/handlers/wallet_handler_test.go
git commit -m "Add parseTokenAmount unit tests"
```

### Task 12: Run full build and existing tests

- [ ] **Step 1: Build entire project**

Run: `cd tsa-api-go && go build ./...`
Expected: PASS

- [ ] **Step 2: Run all tests**

Run: `cd tsa-api-go && go test ./... -v -count=1`
Expected: All PASS

- [ ] **Step 3: Final commit with any fixes**

If any fixes were needed:
```bash
git add -A && git commit -m "Fix issues from full test run"
```

### Task 13: Update CLAUDE.md

**Files:**
- Modify: `tsa-api-go/CLAUDE.md`

- [ ] **Step 1: Update architecture section**

Update the blockchain section to reflect multi-chain:
- `internal/blockchain/client.go` — `EVMClient` wraps go-ethereum ethclient for any EVM chain
- `internal/blockchain/token.go` — ERC-20 queries + native transfers
- `internal/services/blockchain_service.go` — Multi-chain client registry

Add test command:
```bash
go test ./... -v -count=1     # Run all tests
go test ./internal/handlers/ -v  # Handler tests only (uses SQLite)
go test ./internal/blockchain/ -v  # Blockchain tests (hits testnet RPC)
```

Update environment variables to include BSC:
- `BSC_RPC_URL` — BSC RPC endpoint (defaults to BSC testnet)
- `BSC_CHAIN_ID` — BSC chain ID (defaults to 97)
- `BSC_USDT_ADDRESS` — USDT contract on BSC
- `BSC_USDC_ADDRESS` — USDC contract on BSC

- [ ] **Step 2: Commit**

```bash
git add tsa-api-go/CLAUDE.md
git commit -m "Update CLAUDE.md with multi-chain architecture and test commands"
```
