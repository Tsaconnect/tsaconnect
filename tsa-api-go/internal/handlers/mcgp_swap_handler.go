package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
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
// mcgpSwapPort is the slice of *services.MCGPSwapService this handler needs.
// Declared on the consumer side so the handler can be exercised end to end
// without an EVM client — the concrete service reaches a chain on construction,
// which is why these endpoints previously had no tests at all.
type mcgpSwapPort interface {
	Allowance(token, owner string) (*big.Int, error)
	LegacyEntitlement(user string) (*big.Int, error)
	LegacyRemaining(user string) (*big.Int, error)
	PremiumBalance(user string) (*big.Int, error)
	PremiumBought(user string) (*big.Int, error)
	PremiumCredited(user string) (*big.Int, error)
	PrepareApprove(from, token string, amount *big.Int) ([]byte, error)
	PrepareBuyPremium(from string, mcgpAmount, maxUsdc *big.Int) ([]byte, error)
	PrepareSellLegacy(from string, mcgpAmount, minUsdc *big.Int) ([]byte, error)
	PrepareSellPremium(from string, mcgpAmount, minUsdc *big.Int) ([]byte, error)
	PrepareSpend(from, to string, amount *big.Int, ref [32]byte) ([]byte, error)
	PrepareWithdrawPremium(from string, mcgpAmount *big.Int) ([]byte, error)
	Quote(kind uint8, mcgpAmount *big.Int) (*big.Int, error)
	VerifyAmountEvent(eventName, txHash string, account common.Address) (*services.AmountEvent, error)
	VerifyPremiumSpent(txHash string, from, to common.Address, ref [32]byte) (*services.PremiumSpentEvent, error)
}

// mcgpRelayPort is the relayer subset. Nil (or a disabled relayer) means every
// flow self-pays, which is the pre-gasless behaviour.
type mcgpRelayPort interface {
	IsEnabled() bool
	Relay(ctx context.Context, authenticatedWallet string, req services.ForwardRequest) (string, error)
	Scaffold(ctx context.Context, from, to string, data []byte, gas uint64) (services.ForwardRequestScaffold, error)
}

type MCGPSwapHandler struct {
	db          *gorm.DB
	swap        mcgpSwapPort
	relay       mcgpRelayPort
	swapAddress string
}

// NewMCGPSwapHandler returns a handler bound to the swap service.
func NewMCGPSwapHandler(db *gorm.DB, swap *services.MCGPSwapService) *MCGPSwapHandler {
	return &MCGPSwapHandler{db: db, swap: swap}
}

// WithRelayer enables the gasless path. Left unset, IsRelayEnabled() reports
// false and the app self-pays — the dormant-by-default pattern.
func (h *MCGPSwapHandler) WithRelayer(relay mcgpRelayPort, swapAddress string) *MCGPSwapHandler {
	h.relay = relay
	h.swapAddress = swapAddress
	return h
}

func (h *MCGPSwapHandler) relayEnabled() bool {
	return h.relay != nil && h.relay.IsEnabled()
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
	wallet, ok := requireOwnWallet(c, user, c.Query("address"))
	if !ok {
		return
	}
	premium, err := h.swap.PremiumBalance(wallet)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Failed to read premium balance: "+err.Error())
		return
	}
	legacyRemaining, err := h.swap.LegacyRemaining(wallet)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Failed to read legacy entitlement: "+err.Error())
		return
	}
	legacyEntitlement, _ := h.swap.LegacyEntitlement(wallet)
	// premiumBought/premiumCredited split the merged premiumBalance into its two
	// on-chain buckets so the app can tell a user which MCGP is sellable for USDC
	// (bought) vs. spend-only, naira-bought (credited) — see sellPremium's
	// "Insufficient bought premium" revert. Additive fields: read best-effort so
	// an older contract/RPC hiccup still returns the pre-existing shape.
	bought, _ := h.swap.PremiumBought(wallet)
	credited, _ := h.swap.PremiumCredited(wallet)
	utils.SuccessResponse(c, http.StatusOK, "Balance", gin.H{
		"walletAddress":     wallet,
		"premiumBalance":    premium.String(),
		"premiumBought":     bigOrZero(bought),   // sellable for USDC
		"premiumCredited":   bigOrZero(credited), // naira-bought, spend-only (in-app + IT)
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
	// FromAddress is the wallet the device holds a signing key for. Checked
	// against the account's registered address — see callerWallet.
	FromAddress string `json:"fromAddress"`
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
	wallet, ok := callerWallet(c, user, req.FromAddress)
	if !ok {
		return
	}
	allowance, err := h.swap.Allowance(req.Token, wallet)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Allowance read failed: "+err.Error())
		return
	}
	tx, err := h.swap.PrepareApprove(wallet, req.Token, amount)
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
	McgpAmount  string `json:"mcgpAmount"`
	MaxUsdc     string `json:"maxUsdc"`
	FromAddress string `json:"fromAddress"`
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
	wallet, ok := callerWallet(c, user, req.FromAddress)
	if !ok {
		return
	}
	tx, err := h.swap.PrepareBuyPremium(wallet, mcgpAmt, maxUsdc)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Prepare failed: "+err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", tx)
}

type sellReq struct {
	McgpAmount  string `json:"mcgpAmount"`
	MinUsdc     string `json:"minUsdc"`
	FromAddress string `json:"fromAddress"`
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
	wallet, ok := callerWallet(c, user, req.FromAddress)
	if !ok {
		return
	}
	var (
		tx  []byte
		err error
	)
	if legacy {
		tx, err = h.swap.PrepareSellLegacy(wallet, mcgpAmt, minUsdc)
	} else {
		tx, err = h.swap.PrepareSellPremium(wallet, mcgpAmt, minUsdc)
	}
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Prepare failed: "+err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", tx)
}

type withdrawReq struct {
	McgpAmount  string `json:"mcgpAmount"`
	FromAddress string `json:"fromAddress"`
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
	wallet, ok := callerWallet(c, user, req.FromAddress)
	if !ok {
		return
	}
	tx, err := h.swap.PrepareWithdrawPremium(wallet, mcgpAmt)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Prepare failed: "+err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", tx)
}

type payReq struct {
	Username   string `json:"username"`
	McgpAmount string `json:"mcgpAmount"`
	// IdempotencyKey makes the derived `ref` stable across retries of one
	// payment intent. Also accepted as the `Idempotency-Key` header.
	IdempotencyKey string `json:"idempotencyKey"`
	FromAddress    string `json:"fromAddress"`
}

// payRef derives the single-use `ref` for a spend.
//
// With an idempotency key the ref is DERIVED from (payer, key), so retrying the
// same intent yields the SAME ref and the on-chain `usedRef` guard collapses the
// duplicate. That is what stops a relayed spend and a self-paid retry of the same
// payment from both landing — two independent prepares would otherwise mint two
// different refs, and neither the chain nor the ledger's ref_hash index could tell
// they were one intent.
//
// Without a key we mint a random ref, preserving the behaviour older app builds
// depend on: still single-use, just unable to dedupe a retry.
//
// The payer is bound into the hash so one user cannot burn another user's key.
func payRef(payerWallet, idempotencyKey string) common.Hash {
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		return crypto.Keccak256Hash([]byte("mcgp-pay:" + uuid.New().String()))
	}
	payer := strings.ToLower(strings.TrimSpace(payerWallet))
	return crypto.Keccak256Hash([]byte("mcgp-pay:v2:" + payer + ":" + key))
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
	wallet, ok := callerWallet(c, user, req.FromAddress)
	if !ok {
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

	// Single-use ref (also enforced on-chain via usedRef). Derived from the
	// caller's idempotency key when one is supplied — see payRef.
	idem := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idem == "" {
		idem = req.IdempotencyKey
	}
	ref := payRef(wallet, idem)
	var ref32 [32]byte
	copy(ref32[:], ref.Bytes())

	tx, err := h.swap.PrepareSpend(wallet, target.WalletAddress, amount, ref32)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "Prepare failed: "+err.Error())
		return
	}
	payload := gin.H{
		"ref":           ref.Hex(),
		"recipient":     target.Username,
		"recipientName": target.Name,
		// The resolved on-chain address, so the app can rebuild the calldata
		// locally and byte-compare before signing. Without it the app is
		// blind-signing a payee it never saw.
		"recipientAddress": target.WalletAddress,
		"mcgpAmount":       amount.String(),
		"tx":               rawJSON(tx),
	}

	// When relaying is available, hand back the EIP-712 scaffold too. The app
	// decides which path to take; a scaffold being absent is the explicit
	// "self-pay" signal.
	if h.relayEnabled() {
		// Reuse the calldata from the tx prepared above. Calling PrepareSpend a
		// second time would repeat its GetNonce + SuggestGasPrice + EstimateGas —
		// three extra RPC round trips on the hot path of every payment, purely to
		// recompute bytes we already hold.
		if callData := spendCallData(tx); len(callData) > 0 {
			if scaffold, sErr := h.relay.Scaffold(c.Request.Context(), wallet, h.swapAddress, callData, 0); sErr == nil {
				payload["forwardRequest"] = scaffold
			}
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Prepared", payload)
}

// spendCallData extracts the raw calldata from a prepared unsigned tx. The
// prepare helpers return a JSON envelope; the forwarder needs just the bytes.
func spendCallData(prepared []byte) []byte {
	var env struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(prepared, &env); err != nil {
		return nil
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(env.Data, "0x"))
	if err != nil {
		return nil
	}
	return raw
}

// GET /api/mcgp/relay/status — the app's explicit signal for whether to relay
// or self-pay. Deliberately a 200 with a boolean rather than an error, because a
// transport failure must NOT be read as "relayer down": falling back on an
// ambiguous outcome is how one intent becomes two on-chain spends.
func (h *MCGPSwapHandler) RelayStatus(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "OK", gin.H{
		"relayerEnabled": h.relayEnabled(),
	})
}

// maxRelayBodyBytes bounds the relay endpoint's request body. A ForwardRequest
// is a few hundred bytes even with generous calldata.
const maxRelayBodyBytes = 64 << 10

type relaySubmitReq struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Value     string `json:"value"`
	Gas       uint64 `json:"gas"`
	Deadline  uint64 `json:"deadline"`
	Data      string `json:"data"`
	Signature string `json:"signature"`
}

// POST /api/mcgp/relay — submit a user-signed ForwardRequest for sponsorship.
func (h *MCGPSwapHandler) SubmitRelay(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if !h.relayEnabled() {
		// 200 with the disabled flag, not an error: the app must be able to tell
		// "relaying is off" apart from "the request failed".
		utils.SuccessResponse(c, http.StatusOK, "Relaying is not available", gin.H{
			"relayerEnabled": false,
		})
		return
	}

	// Cap the body before decoding. `data` and `signature` are caller-supplied
	// hex, and nothing else in the middleware chain bounds request size — the IP
	// rate limiter allows 500 requests per 15 minutes, which is a lot of room to
	// push large bodies through an authenticated endpoint. A ForwardRequest is a
	// few hundred bytes; 64 KiB is generous.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRelayBodyBytes)

	var req relaySubmitReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	data, err := hex.DecodeString(strings.TrimPrefix(req.Data, "0x"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid calldata")
		return
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(req.Signature, "0x"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid signature")
		return
	}
	value, ok := new(big.Int).SetString(strings.TrimSpace(req.Value), 10)
	if !ok {
		value = new(big.Int)
	}

	txHash, err := h.relay.Relay(c.Request.Context(), user.WalletAddress, services.ForwardRequest{
		From:      req.From,
		To:        req.To,
		Value:     value,
		Gas:       req.Gas,
		Deadline:  req.Deadline,
		Data:      data,
		Signature: sig,
	})
	if err != nil {
		utils.ErrorResponse(c, relayStatusCode(err), clientSafeRelayMessage(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Relayed", gin.H{"txHash": txHash})
}

// clientSafeRelayMessage keeps internal detail out of the response body.
//
// The relayer wraps the underlying RPC error on a transport failure, so
// err.Error() can read "dial tcp 10.0.0.5:8545: connect: connection refused" —
// handing an authenticated but untrusted caller our internal RPC topology. Only
// the on-chain revert reason is safe to pass through, and only because it is
// already public on the chain.
func clientSafeRelayMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrSimulationReverted):
		return err.Error() // the revert reason is public on-chain and actionable
	case errors.Is(err, services.ErrSimulationUnavailable):
		return "Could not check the transaction right now. Please try again."
	case errors.Is(err, services.ErrRelayerUnderfunded), errors.Is(err, services.ErrDailyCapExceeded):
		return "Gas sponsorship is temporarily unavailable. Please try again later."
	case errors.Is(err, services.ErrRateLimited),
		errors.Is(err, services.ErrSenderMismatch),
		errors.Is(err, services.ErrTargetNotAllowed),
		errors.Is(err, services.ErrSelectorNotAllowed),
		errors.Is(err, services.ErrDeadlineExpired),
		errors.Is(err, services.ErrDeadlineTooFar),
		errors.Is(err, services.ErrGasCeiling),
		errors.Is(err, services.ErrBelowMinSponsored),
		errors.Is(err, services.ErrRelayerDisabled):
		return err.Error() // these are our own sentinels; no internal detail in them
	default:
		// Anything unclassified may be a wrapped infrastructure error.
		return "Could not relay the transaction."
	}
}

// relayStatusCode maps relayer errors to HTTP codes. The distinction that
// matters: 503 means "try again", everything else means "do not retry this
// request as-is".
func relayStatusCode(err error) int {
	switch {
	case errors.Is(err, services.ErrRelayerDisabled):
		return http.StatusServiceUnavailable
	case errors.Is(err, services.ErrSimulationUnavailable),
		errors.Is(err, services.ErrRelayerUnderfunded),
		errors.Is(err, services.ErrDailyCapExceeded):
		return http.StatusServiceUnavailable
	case errors.Is(err, services.ErrRateLimited):
		return http.StatusTooManyRequests
	case errors.Is(err, services.ErrSenderMismatch):
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
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
	// Bind the recipient to the account whose wallet the chain says was paid.
	// That is the candidate itself: re-resolving by address could pick another
	// account holding the same address in different case.
	if !strings.EqualFold(ev.To.Hex(), candidate.WalletAddress) {
		utils.ErrorResponse(c, http.StatusBadRequest, "On-chain recipient is not a known user")
		return
	}
	recipient := candidate

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
