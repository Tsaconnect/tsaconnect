package handlers

import (
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// MCGPSwapHandler serves the /api/mcgp endpoints for McgpEntitlementSwap
// ("Swap Contract B"): premium buy/spend/sell, legacy redeem, withdraw, and
// balance reads. Follows the prepare→sign→confirm pattern — the backend returns
// an unsigned tx, the app signs + broadcasts, the backend confirms via the
// on-chain event. Audit ledger rows are written at CONFIRM time (after the
// event is verified), so an abandoned prepare leaves no orphan rows. Routes are
// registered only when the swap service is configured (dormant-by-default).
type MCGPSwapHandler struct {
	db   *gorm.DB
	swap *services.MCGPSwapService
}

// NewMCGPSwapHandler returns a handler bound to the swap service.
func NewMCGPSwapHandler(db *gorm.DB, swap *services.MCGPSwapService) *MCGPSwapHandler {
	return &MCGPSwapHandler{db: db, swap: swap}
}

// parseAmount parses a positive base-unit decimal string into a big.Int.
func parseAmount(s string) (*big.Int, bool) {
	v, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok || v.Sign() <= 0 {
		return nil, false
	}
	return v, true
}

// parseMinUsdc allows zero (no slippage floor) but rejects negatives/garbage.
func parseMinUsdc(s string) (*big.Int, bool) {
	v, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok || v.Sign() < 0 {
		return nil, false
	}
	return v, true
}

// GET /api/mcgp/balance — the caller's premium balance + legacy entitlement.
func (h *MCGPSwapHandler) GetBalance(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	premium, err := h.swap.PremiumBalance(user.WalletAddress)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Failed to read premium balance: "+err.Error())
		return
	}
	legacyRemaining, err := h.swap.LegacyRemaining(user.WalletAddress)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Failed to read legacy entitlement: "+err.Error())
		return
	}
	legacyEntitlement, _ := h.swap.LegacyEntitlement(user.WalletAddress)
	utils.SuccessResponse(c, http.StatusOK, "Balance", gin.H{
		"walletAddress":     user.WalletAddress,
		"premiumBalance":    premium.String(),
		"legacyEntitlement": bigOrZero(legacyEntitlement),
		"legacyRemaining":   legacyRemaining.String(),
	})
}

// GET /api/mcgp/quote?kind=premiumBuy|premiumSell|legacySell&amount=<base-units>
func (h *MCGPSwapHandler) GetQuote(c *gin.Context) {
	amount, ok := parseAmount(c.Query("amount"))
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid amount")
		return
	}
	kind, ok := priceKindFromString(c.Query("kind"))
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid kind (premiumBuy|premiumSell|legacySell)")
		return
	}
	usdc, err := h.swap.Quote(kind, amount)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Quote failed: "+err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Quote", gin.H{
		"kind":       c.Query("kind"),
		"mcgpAmount": amount.String(),
		"usdcAmount": usdc.String(),
	})
}

// ─────────────────────────── prepare endpoints ────────────────────────────
// Prepare endpoints return the unsigned tx for the app to sign + broadcast.
// They do NOT write to the ledger (that happens at confirm).

type approveReq struct {
	Token  string `json:"token"`  // "usdc" (for buy) | "mcgp" (for legacy sell)
	Amount string `json:"amount"` // base units
}

// POST /api/mcgp/approve/prepare — unsigned ERC-20 approve(swap, amount) tx +
// the current allowance, so the app can skip approve when it already covers the
// amount (mirrors the P2P deposit-approve pattern).
func (h *MCGPSwapHandler) PrepareApprove(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req approveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	amount, ok := parseAmount(req.Amount)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid amount")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	allowance, err := h.swap.Allowance(req.Token, user.WalletAddress)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Allowance read failed: "+err.Error())
		return
	}
	tx, err := h.swap.PrepareApprove(user.WalletAddress, req.Token, amount)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Prepare failed: "+err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Prepared", gin.H{
		"currentAllowance": allowance.String(),
		"tx":               rawJSON(tx),
	})
}

type buyReq struct {
	McgpAmount string `json:"mcgpAmount"`
	MaxUsdc    string `json:"maxUsdc"`
}

// POST /api/mcgp/buy/prepare — unsigned buyPremium tx.
func (h *MCGPSwapHandler) PrepareBuy(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req buyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	mcgpAmt, ok := parseAmount(req.McgpAmount)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid mcgpAmount")
		return
	}
	maxUsdc, ok := parseAmount(req.MaxUsdc)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid maxUsdc")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	tx, err := h.swap.PrepareBuyPremium(user.WalletAddress, mcgpAmt, maxUsdc)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Prepare failed: "+err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", tx)
}

type sellReq struct {
	McgpAmount string `json:"mcgpAmount"`
	MinUsdc    string `json:"minUsdc"`
}

// POST /api/mcgp/sell/prepare — unsigned sellPremium tx.
func (h *MCGPSwapHandler) PrepareSell(c *gin.Context) { h.prepareSell(c, false) }

// POST /api/mcgp/legacy/sell/prepare — unsigned sellLegacy tx (redeem-only).
func (h *MCGPSwapHandler) PrepareLegacySell(c *gin.Context) { h.prepareSell(c, true) }

func (h *MCGPSwapHandler) prepareSell(c *gin.Context, legacy bool) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req sellReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	mcgpAmt, ok := parseAmount(req.McgpAmount)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid mcgpAmount")
		return
	}
	minUsdc, ok := parseMinUsdc(req.MinUsdc)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid minUsdc")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	var (
		tx  []byte
		err error
	)
	if legacy {
		tx, err = h.swap.PrepareSellLegacy(user.WalletAddress, mcgpAmt, minUsdc)
	} else {
		tx, err = h.swap.PrepareSellPremium(user.WalletAddress, mcgpAmt, minUsdc)
	}
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Prepare failed: "+err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", tx)
}

type withdrawReq struct {
	McgpAmount string `json:"mcgpAmount"`
}

// POST /api/mcgp/withdraw/prepare — unsigned withdrawPremiumToWallet tx.
func (h *MCGPSwapHandler) PrepareWithdraw(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req withdrawReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	mcgpAmt, ok := parseAmount(req.McgpAmount)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid mcgpAmount")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	tx, err := h.swap.PrepareWithdrawPremium(user.WalletAddress, mcgpAmt)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Prepare failed: "+err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", tx)
}

type payReq struct {
	Username   string `json:"username"`
	McgpAmount string `json:"mcgpAmount"`
}

// POST /api/mcgp/pay/prepare — peer pay-by-username. Resolves the recipient,
// mints a single-use ref, and returns an unsigned spend(to, amount, ref) tx for
// the payer to sign. No ledger row yet — it's written on confirm.
func (h *MCGPSwapHandler) PreparePay(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req payReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	amount, ok := parseAmount(req.McgpAmount)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid mcgpAmount")
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Username is required")
		return
	}
	if strings.EqualFold(username, user.Username) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot pay yourself")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	var target models.User
	if err := h.db.Where("LOWER(username) = LOWER(?)", username).First(&target).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "User not found")
		return
	}
	if target.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Recipient has not set up a wallet yet")
		return
	}

	// Single-use ref (idempotency key, also enforced on-chain via usedRef).
	ref := crypto.Keccak256Hash([]byte("mcgp-pay:" + uuid.New().String()))
	var ref32 [32]byte
	copy(ref32[:], ref.Bytes())

	tx, err := h.swap.PrepareSpend(user.WalletAddress, target.WalletAddress, amount, ref32)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Prepare failed: "+err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Prepared", gin.H{
		"ref":           ref.Hex(),
		"recipient":     target.Username,
		"recipientName": target.Name,
		"mcgpAmount":    amount.String(),
		"tx":            rawJSON(tx),
	})
}

// ─────────────────────────── confirm endpoints ────────────────────────────

type confirmSwapReq struct {
	TxHash    string `json:"txHash"`
	Direction string `json:"direction"` // buy_premium | sell_premium | sell_legacy | withdraw
}

// POST /api/mcgp/confirm — verify a buy/sell/legacy/withdraw event for the
// caller and write a confirmed audit ledger row (idempotent on tx+direction).
func (h *MCGPSwapHandler) ConfirmSwap(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	var req confirmSwapReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.TxHash) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "txHash is required")
		return
	}
	eventName, dir, ok := swapEventForDirection(req.Direction)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid direction")
		return
	}
	// Idempotency: skip if we've already recorded this tx for this user+direction.
	// A query error must NOT be treated as "not yet confirmed" (it would risk a
	// duplicate row), so fail closed.
	var existing int64
	if err := h.db.Model(&models.MCGPSwapLedgerEntry{}).
		Where("tx_hash = ? AND user_id = ? AND direction = ?", req.TxHash, user.ID, dir).
		Count(&existing).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Ledger lookup failed")
		return
	}
	if existing > 0 {
		utils.SuccessResponse(c, http.StatusOK, "Already confirmed", nil)
		return
	}
	ev, err := h.swap.VerifyAmountEvent(eventName, req.TxHash, common.HexToAddress(user.WalletAddress))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Verification failed: "+err.Error())
		return
	}
	entry := &models.MCGPSwapLedgerEntry{
		UserID:     user.ID,
		Direction:  dir,
		McgpAmount: ev.McgpAmount.String(),
		UsdcAmount: ev.UsdcAmount.String(),
		TxHash:     req.TxHash,
		Status:     models.MCGPLedgerConfirmed,
	}
	if err := h.db.Create(entry).Error; err != nil {
		// Possible unique-index race (another confirm of the same tx won). If the
		// row now exists, treat as already confirmed; otherwise surface the error.
		var n int64
		h.db.Model(&models.MCGPSwapLedgerEntry{}).
			Where("tx_hash = ? AND user_id = ? AND direction = ?", req.TxHash, user.ID, dir).
			Count(&n)
		if n > 0 {
			utils.SuccessResponse(c, http.StatusOK, "Already confirmed", nil)
			return
		}
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to record ledger entry")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Confirmed", gin.H{
		"ledgerId":   entry.ID,
		"mcgpAmount": entry.McgpAmount,
		"usdcAmount": entry.UsdcAmount,
	})
}

type confirmPayReq struct {
	TxHash   string `json:"txHash"`
	Ref      string `json:"ref"`
	Username string `json:"username"`
}

// POST /api/mcgp/pay/confirm — verify the PremiumSpent event for a prepared
// payment and write confirmed spend_out + spend_in ledger rows (idempotent on
// the unique ref).
func (h *MCGPSwapHandler) ConfirmPay(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	var req confirmPayReq
	if err := c.ShouldBindJSON(&req); err != nil ||
		strings.TrimSpace(req.TxHash) == "" || strings.TrimSpace(req.Ref) == "" || strings.TrimSpace(req.Username) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "txHash, ref and username are required")
		return
	}
	// Idempotency: a confirmed spend_out already exists for this ref. Fail closed
	// on a query error (don't risk a duplicate).
	var existing int64
	if err := h.db.Model(&models.MCGPSwapLedgerEntry{}).Where("ref_hash = ?", req.Ref).Count(&existing).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Ledger lookup failed")
		return
	}
	if existing > 0 {
		utils.SuccessResponse(c, http.StatusOK, "Already confirmed", nil)
		return
	}
	// The client names the recipient, but we use it only as a verification
	// candidate: VerifyPremiumSpent requires the on-chain `to` topic to equal
	// this address, and we bind the spend_in row to the user owning the VERIFIED
	// address (not the client's username), so attribution can't be spoofed.
	var candidate models.User
	if err := h.db.Where("LOWER(username) = LOWER(?)", strings.TrimSpace(req.Username)).First(&candidate).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Recipient not found")
		return
	}
	if candidate.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Recipient has no wallet")
		return
	}
	var ref32 [32]byte
	copy(ref32[:], common.HexToHash(req.Ref).Bytes())
	ev, err := h.swap.VerifyPremiumSpent(req.TxHash,
		common.HexToAddress(user.WalletAddress), common.HexToAddress(candidate.WalletAddress), ref32)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Verification failed: "+err.Error())
		return
	}
	// Bind the recipient to the user owning the on-chain `to` address.
	var recipient models.User
	if err := h.db.Where("LOWER(wallet_address) = LOWER(?)", ev.To.Hex()).First(&recipient).Error; err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "On-chain recipient is not a known user")
		return
	}

	out := &models.MCGPSwapLedgerEntry{
		UserID:             user.ID,
		CounterpartyUserID: &recipient.ID,
		Direction:          models.MCGPLedgerSpendOut,
		McgpAmount:         ev.Amount.String(),
		RefHash:            req.Ref,
		TxHash:             req.TxHash,
		Status:             models.MCGPLedgerConfirmed,
	}
	// Write both legs atomically: the payer's spend_out (carries the unique ref)
	// and the recipient's spend_in mirror, so the dual-entry ledger can't desync.
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(out).Error; e != nil {
			return e
		}
		return tx.Create(&models.MCGPSwapLedgerEntry{
			UserID:             recipient.ID,
			CounterpartyUserID: &user.ID,
			Direction:          models.MCGPLedgerSpendIn,
			McgpAmount:         ev.Amount.String(),
			TxHash:             req.TxHash,
			Status:             models.MCGPLedgerConfirmed,
		}).Error
	})
	if err != nil {
		// Unique-ref race: another confirm committed first. Treat as confirmed.
		var n int64
		h.db.Model(&models.MCGPSwapLedgerEntry{}).Where("ref_hash = ?", req.Ref).Count(&n)
		if n > 0 {
			utils.SuccessResponse(c, http.StatusOK, "Already confirmed", nil)
			return
		}
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to record payment")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment confirmed", gin.H{"ledgerId": out.ID})
}

// GET /api/mcgp/ledger — the caller's recent swap history (newest first).
func (h *MCGPSwapHandler) GetLedger(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var entries []models.MCGPSwapLedgerEntry
	h.db.Where("user_id = ?", user.ID).Order("created_at DESC").Limit(100).Find(&entries)
	utils.SuccessResponse(c, http.StatusOK, "Ledger", gin.H{"entries": entries})
}

// ───────────────────────────────── helpers ────────────────────────────────

// swapEventForDirection maps an API direction to (eventName, ledgerDirection).
func swapEventForDirection(direction string) (eventName, ledgerDir string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case models.MCGPLedgerBuyPremium, "buy":
		return "PremiumBought", models.MCGPLedgerBuyPremium, true
	case models.MCGPLedgerSellPremium, "sell":
		return "PremiumSold", models.MCGPLedgerSellPremium, true
	case models.MCGPLedgerSellLegacy, "legacy":
		return "LegacyRedeemed", models.MCGPLedgerSellLegacy, true
	case models.MCGPLedgerWithdraw:
		return "PremiumWithdrawn", models.MCGPLedgerWithdraw, true
	}
	return "", "", false
}

func priceKindFromString(s string) (uint8, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "premiumbuy", "premium_buy", "buy":
		return services.PriceKindPremiumBuy, true
	case "premiumsell", "premium_sell", "sell":
		return services.PriceKindPremiumSell, true
	case "legacysell", "legacy_sell", "legacy":
		return services.PriceKindLegacySell, true
	}
	return 0, false
}

func bigOrZero(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

// rawJSON nests already-encoded JSON (the prepared tx) inside a response body.
type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}
