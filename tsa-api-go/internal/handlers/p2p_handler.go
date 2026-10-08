package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// P2PHandler bundles the P2P trade + payment-method HTTP surface.
//
// The service does the heavy lifting; the handler is purely HTTP I/O:
// parse → validate-shape → dispatch → map errors → respond.
type P2PHandler struct {
	db       *gorm.DB
	trade    *services.P2PTradeService
	messages *services.P2PMessageService
	// banks is optional — when nil, bank-list and account-resolve endpoints
	// return a 503-ish "feature unavailable" response. The trade routes
	// themselves don't depend on it. Backed by whichever fiat provider is
	// active (Flutterwave since 2026-07; Nomba as fallback).
	banks services.BankDirectory
	stats *services.MerchantStatsService
	// central answers where PM buyers are being sent right now. Optional only
	// because it is wired after construction (SetInboundCentralSource); leaving
	// it nil once INBOUND_MODE=central is on hands buyers the merchant's own
	// account, which no longer feeds the statement the ingestor reads.
	central CentralAccountSource
}

// CentralAccountSource is the single thing the PM buy screens need from the IT
// service: the account payers must be sent to right now. Narrow interface
// rather than *services.ITService because P2P has no other business with that
// service, and because the one method carries the mode with it — there is no
// way to ask this seam for an account without asking whether it is live.
type CentralAccountSource interface {
	ActiveCentralAccount() (services.CentralAccount, bool)
}

// SetInboundCentralSource wires the pay-into surface to the cutover, from
// main.go. Separate from the constructor because the IT service is built later
// in boot than the P2P handler.
func (h *P2PHandler) SetInboundCentralSource(src CentralAccountSource) { h.central = src }

// pmPayTo picks the account a PM buyer must send fiat to.
//
// After the cutover that is the company account and NOT the merchant's: the
// buyer's transfer has to land on the statement the ingestor reads, or the
// trade — which is offered to the matcher as a claim either way — waits on a
// credit that will never arrive. The merchant is credited from that account and
// forwarded to separately (see P2PSettlementForwarder), so nothing they are owed
// changes; only where the buyer sends it does.
//
// payerID is the user who will send the transfer, and only when they are the
// one receiving this response: the instruction names their verified legal name,
// which must never reach anyone else (a trade is also fetched by its seller).
// Nil gives the generic "in your own name" wording. The name is looked up only
// in central mode, where it is used.
func (h *P2PHandler) pmPayTo(ctx context.Context, accountNumber, bank, merchantName string, payerID *uuid.UUID) *models.PmPayTo {
	if h.central != nil {
		if central, ok := h.central.ActiveCentralAccount(); ok {
			legalName := ""
			if payerID != nil {
				legalName = services.VerifiedLegalNameOf(ctx, h.db, *payerID)
			}
			return &models.PmPayTo{
				AccountNumber:    central.AccountNumber,
				Bank:             central.BankName,
				AccountName:      central.AccountName,
				PayerInstruction: central.PayerInstruction(legalName),
			}
		}
	}
	return &models.PmPayTo{AccountNumber: accountNumber, Bank: bank, AccountName: merchantName}
}

// NewP2PHandler constructs a P2PHandler. Pass nil for `trade` to disable the
// P2P routes — handlers will return a configuration-error response so the
// frontend can show "feature unavailable" cleanly instead of a 500.
func NewP2PHandler(db *gorm.DB, trade *services.P2PTradeService, messages *services.P2PMessageService, banks services.BankDirectory, stats *services.MerchantStatsService) *P2PHandler {
	return &P2PHandler{db: db, trade: trade, messages: messages, banks: banks, stats: stats}
}

// IsConfigured reports whether the handler has its trade service wired up.
// routes.go checks this so we don't register routes that always 503.
func (h *P2PHandler) IsConfigured() bool {
	return h != nil && h.trade != nil
}

// HasNomba reports whether the bank directory / account-resolve endpoints
// can serve requests. Used by routes.go to skip registering them when
// credentials are absent. (Name is historical — the directory is provider-
// agnostic; kept to avoid churning call sites.)
func (h *P2PHandler) HasNomba() bool {
	return h != nil && h.banks != nil
}

// ─── Helpers ───────────────────────────────────────────────────────────────

func (h *P2PHandler) require(c *gin.Context) (*models.User, bool) {
	user := getUserFromContext(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Please authenticate",
		})
		return nil, false
	}
	if user.VerificationStatus != "verified" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "Identity verification required for P2P",
		})
		return nil, false
	}
	if h.stats != nil {
		go func(uid uuid.UUID) { _ = h.stats.Touch(context.Background(), uid) }(user.ID)
	}
	return user, true
}

func parseUUIDParam(c *gin.Context, key string) (uuid.UUID, bool) {
	raw := c.Param(key)
	id, err := uuid.Parse(raw)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": fmt.Sprintf("Invalid %s", key),
		})
		return uuid.Nil, false
	}
	return id, true
}

// mapServiceError translates service-layer error sentinels into HTTP status +
// JSON. Returns true if the handler should abort after responding.
func mapServiceError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	// An LP deposit outside the pool limits: the app shows the headroom, so it
	// is sent both as a decimal string and in exact base units (18 dp on BSC).
	var lim *services.LiquidityLimitError
	if errors.As(err, &lim) {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"success": false, "message": err.Error(), "code": lim.Code,
			"data": gin.H{
				"headroom":      services.BaseUnitsToDecimal(lim.Headroom, lim.Decimals),
				"headroomUnits": lim.Headroom.String(),
				"decimals":      lim.Decimals,
			},
		})
		return true
	}
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, services.ErrAdNotFound), errors.Is(err, services.ErrTradeNotFound),
		errors.Is(err, services.ErrPaymentMethodMissing),
		errors.Is(err, services.ErrDisputeNotFound):
		status = http.StatusNotFound
	case errors.Is(err, services.ErrForbidden),
		// The buyer has not finished verification. The blanket P2P check in
		// require() normally catches this first; the service raises it too so
		// a caller that reaches OpenTrade by any other route is refused the
		// same way rather than opening a trade nobody can attribute.
		errors.Is(err, services.ErrKYCVerificationRequired):
		status = http.StatusForbidden
	case errors.Is(err, services.ErrInvalidParams),
		errors.Is(err, services.ErrAmountOutOfRange),
		errors.Is(err, services.ErrAmountOverflow),
		errors.Is(err, services.ErrUnsupportedToken),
		errors.Is(err, services.ErrSelfTrade),
		errors.Is(err, services.ErrNotApplicableForBuyAd):
		status = http.StatusBadRequest
	case errors.Is(err, services.ErrAdNotActive),
		errors.Is(err, services.ErrInsufficientRemaining),
		errors.Is(err, services.ErrInvalidTransition),
		errors.Is(err, services.ErrAdHasOpenTrades),
		errors.Is(err, services.ErrDisputeAlreadyOpen),
		errors.Is(err, services.ErrActiveTradeExists),
		errors.Is(err, services.ErrSellerPoolInsufficient),
		errors.Is(err, services.ErrPoolStockAdExists),
		// The asset was delisted from P2P after this ad/pool existed. Creating
		// an ad in it also carries ErrInvalidParams and maps to 400 above.
		errors.Is(err, services.ErrAssetNotTradeable):
		// 409: well-formed but conflicts with current state — incl. a maker
		// whose pool can no longer back the trade (buyer should pick another ad).
		status = http.StatusConflict
	case errors.Is(err, services.ErrTradePrepareReverted):
		// The on-chain tx would revert — request as-formed can't be fulfilled.
		// The message carries the underlying revert reason.
		status = http.StatusUnprocessableEntity
	case errors.Is(err, services.ErrRateStale),
		errors.Is(err, services.ErrChainUnavailable),
		errors.Is(err, services.ErrPoolClosed):
		status = http.StatusServiceUnavailable
	case errors.Is(err, services.ErrPhase2Migration):
		// Legacy lock-on-publish surface invoked by a stale caller. Map to
		// 410 Gone so the FE can distinguish "your client is out of date"
		// from a generic server bug.
		status = http.StatusGone
	}
	body := gin.H{"success": false, "message": err.Error()}
	// The KYC refusal is the one error here the app routes on rather than just
	// displaying, so it carries the same machine code as the IT legs.
	if code := services.KYCRefusalCode(err); code != "" {
		body["code"] = code
	}
	if errors.Is(err, services.ErrPoolClosed) {
		body["code"] = "liquidity_pool_closed"
	}
	c.AbortWithStatusJSON(status, body)
	return true
}

// unsignedTxJSON converts the service's raw JSON bytes into a generic value
// so the response is JSON-typed (not a base64-encoded string).
func unsignedTxJSON(raw []byte) any {
	var v any
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		// Server bug: the service handed us non-JSON bytes. Don't silently
		// flip the response shape to string — log and surface nil so the
		// caller can tell from the (success=true, unsignedTx=null) pair
		// that something is off rather than crashing trying to read .to.
		log.Printf("[p2p] unsignedTxJSON: failed to unmarshal prepared tx (%d bytes): %v", len(raw), err)
		return nil
	}
	return v
}

// ─── Chains ────────────────────────────────────────────────────────────────

// GetChains returns the P2P-enabled chains + their tradeable assets, plus the
// pool chain for an active liquidity provider.
func (h *P2PHandler) GetChains(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	chains := []services.P2PChainInfo{}
	if h.trade != nil {
		// The pool chain is added for an active LP only (plan B P10).
		if cs := h.trade.ChainsFor(c.Request.Context(), user.ID); cs != nil {
			chains = cs
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": chains})
}

// ─── Payment methods ───────────────────────────────────────────────────────

type createPaymentMethodRequest struct {
	Provider      string `json:"provider" binding:"required"`
	Type          string `json:"type"` // ignored; type is always derived from the provider catalog
	Country       string `json:"country" binding:"required"`
	Currency      string `json:"currency" binding:"required"`
	BankName      string `json:"bankName" binding:"required"`
	BankCode      string `json:"bankCode"`
	AccountNumber string `json:"accountNumber" binding:"required"`
	AccountName   string `json:"accountName" binding:"required"`
	IsDefault     bool   `json:"isDefault"`
}

func (h *P2PHandler) CreatePaymentMethod(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	var req createPaymentMethodRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if !services.IsValidProvider(req.Provider) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid payment provider"})
		return
	}
	// Type is derived from the provider catalog so the two never disagree.
	pmType := services.ProviderType(req.Provider)
	pm := &models.P2PPaymentMethod{
		ID:            uuid.New(),
		UserID:        user.ID,
		Type:          pmType,
		Provider:      req.Provider,
		Country:       strings.ToUpper(req.Country),
		Currency:      strings.ToUpper(req.Currency),
		BankName:      req.BankName,
		BankCode:      req.BankCode,
		AccountNumber: req.AccountNumber,
		AccountName:   req.AccountName,
		IsDefault:     req.IsDefault,
	}
	if err := h.db.Create(pm).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": pm})
}

func (h *P2PHandler) ListPaymentMethods(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	var rows []models.P2PPaymentMethod
	if err := h.db.Where("user_id = ? AND deleted_at IS NULL", user.ID).
		Order("created_at DESC").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows})
}

// ListPaymentProviders returns the curated payment-provider catalog used by
// the create-ad / add-payment-method pickers. Static data — no auth-scoped
// rows — but kept under the authed group for consistency.
func (h *P2PHandler) ListPaymentProviders(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	// Optional ?currency= scopes the catalog to providers serving that fiat
	// (plus universal ones). Omitted → full catalog (back-compat).
	providers := services.ProvidersForCurrency(c.Query("currency"))
	c.JSON(http.StatusOK, gin.H{"success": true, "data": providers})
}

func (h *P2PHandler) DeletePaymentMethod(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	// Refuse delete if the method is referenced by any open ad or trade.
	var inUse int64
	h.db.Model(&models.P2PTrade{}).
		Where("payment_method_id = ? AND status IN ?", id,
			[]string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid,
				models.P2PTradeStatusReleasing, models.P2PTradeStatusDisputed}).
		Count(&inUse)
	if inUse > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": "Payment method is bound to open trades; cannot delete",
		})
		return
	}
	res := h.db.Model(&models.P2PPaymentMethod{}).
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, user.ID).
		Update("deleted_at", time.Now().UTC())
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": res.Error.Error()})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Payment method not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"id": id, "deleted": true}})
}

// ─── Bank directory & name enquiry (Nomba-backed) ──────────────────────────

// ListNigerianBanks returns the Nomba-supported Nigerian bank directory.
// The frontend uses this to populate the bank dropdown on the add-payment
// form. Results are cached server-side; clients should additionally
// AsyncStorage-cache to avoid hitting this on every form open.
func (h *P2PHandler) ListNigerianBanks(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	if !h.HasNomba() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "Bank directory is unavailable",
		})
		return
	}
	// ?country=GH selects another market's directory. A payout to a Ghanaian
	// account needs Ghanaian bank codes, and a directory pinned to one country
	// at construction can only ever serve one. Omitted ⇒ the configured default
	// (NG), so existing callers are unaffected.
	country := strings.ToUpper(strings.TrimSpace(c.Query("country")))
	// Bound the shape before forwarding it to the provider. This is user input
	// reaching an outbound API call, and while url.Values encodes it (so there
	// is no injection), an unbounded string still lets a caller drive arbitrary
	// upstream requests. ISO-3166-1 alpha-2 is the only thing the endpoint
	// means, so anything else is a client bug worth reporting as one.
	if country != "" && !isISO2Country(country) {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "country must be a two-letter ISO country code",
		})
		return
	}

	var (
		banks []services.NombaBank
		err   error
	)
	if country != "" {
		scoped, ok := h.banks.(interface {
			ListBanksIn(ctx context.Context, country string) ([]services.NombaBank, error)
		})
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"message": "Bank directory for other countries is unavailable",
			})
			return
		}
		banks, err = scoped.ListBanksIn(c.Request.Context(), country)
	} else {
		banks, err = h.banks.ListBanks(c.Request.Context())
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": banks})
}

// ResolveAccountName performs a NUBAN name enquiry against the given
// bank code. Returns 404 when Nomba reports no match so the frontend can
// distinguish "wrong number" from "outage".
func (h *P2PHandler) ResolveAccountName(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	if !h.HasNomba() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "Account verification is unavailable",
		})
		return
	}
	bankCode, accountNumber, ok := validBankLookup(c.Query("bankCode"), c.Query("accountNumber"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": bankLookupInvalidMessage,
		})
		return
	}
	result, err := h.banks.Resolve(c.Request.Context(), bankCode, accountNumber)
	if err != nil {
		if errors.Is(err, services.ErrNombaAccountNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"message": "Account could not be resolved at the selected bank",
			})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// ─── Deposit / withdraw / balance ──────────────────────────────────────────

// poolMoveRequest is the shared shape for prepare-approve / prepare-deposit /
// prepare-withdraw. WalletAddress is the wallet the seller is operating from —
// the app's currently-active wallet, which may not match the user's primary
// User.WalletAddress (the in-app HD wallet lets users hold several derived
// accounts). The address is purely informational on prepare endpoints (used
// for gas estimation against the right balance); on confirm endpoints the
// service cross-checks it against the on-chain event's `seller` field, so a
// caller can't credit themselves with someone else's deposit by claiming a
// different address.
type poolMoveRequest struct {
	Token         string `json:"token" binding:"required"`
	Amount        string `json:"amount" binding:"required"`
	WalletAddress string `json:"walletAddress" binding:"required"`
	// ChainID selects the pool's chain. 0 (omitted by the current app) defaults
	// to Sonic in the service for back-compat.
	ChainID int64 `json:"chainId"`
}

// confirmPoolMoveRequest carries the tx hash plus the original amount and
// wallet the user intended to move from. The service verifies the on-chain
// event's `seller` matches WalletAddress and amount matches Amount — without
// these checks, an attacker who learned a victim's txHash could submit it
// under their own (different-amount) deposit or credit themselves with the
// victim's deposit by claiming a different address.
type confirmPoolMoveRequest struct {
	TxHash        string `json:"txHash" binding:"required"`
	Token         string `json:"token" binding:"required"`
	Amount        string `json:"amount" binding:"required"`
	WalletAddress string `json:"walletAddress" binding:"required"`
	// ChainID selects the pool's chain. 0 (omitted) defaults to Sonic.
	ChainID int64 `json:"chainId"`
}

// PrepareApprove returns the unsigned ERC-20 approve tx the seller must
// submit before their first deposit (or whenever the current allowance is
// short). Response carries the current allowance so the frontend can skip
// this step when it already exceeds the intended deposit.
func (h *P2PHandler) PrepareApprove(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	var req poolMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	// TEMP diagnostic: log the wallet address being used vs the user's
	// registered primary, so we can confirm the FE is actually sending the
	// active-wallet field after the multi-wallet refactor.
	log.Printf("[p2p PrepareApprove] user=%s registeredWallet=%s requestWallet=%q token=%s amount=%s",
		user.ID, user.WalletAddress, req.WalletAddress, req.Token, req.Amount)
	raw, err := h.trade.PrepareApprove(c.Request.Context(), user.ID, req.WalletAddress, req.Token, req.Amount, req.ChainID)
	if mapServiceError(c, err) {
		return
	}
	allowance, allowanceErr := h.trade.GetAllowance(c.Request.Context(), req.WalletAddress, req.Token, req.ChainID)
	allowanceStr := "0"
	if allowanceErr == nil && allowance != nil {
		allowanceStr = allowance.String()
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"unsignedTx":       unsignedTxJSON(raw),
			"currentAllowance": allowanceStr,
		},
	})
}

// GetAllowance is the lightweight view counterpart — exposed so the FE
// can check allowance state without preparing an approve tx (e.g. on
// pool-screen mount, or after the user broadcasts an approve to refresh).
// Query params: ?token=USDT&address=0x...  (address is the active wallet).
func (h *P2PHandler) GetAllowance(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	token := c.Query("token")
	address := c.Query("address")
	if token == "" || address == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "token and address query params are required"})
		return
	}
	chainID, _ := strconv.ParseInt(c.Query("chainId"), 10, 64)
	allowance, err := h.trade.GetAllowance(c.Request.Context(), address, token, chainID)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"allowance": allowance.String()},
	})
}

func (h *P2PHandler) PrepareDeposit(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	var req poolMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	// TEMP diagnostic: log the wallet address being used vs the user's
	// registered primary, so we can confirm the FE is sending the active-
	// wallet field and that the address matches what was funded on chain.
	log.Printf("[p2p PrepareDeposit] user=%s registeredWallet=%s requestWallet=%q token=%s amount=%s",
		user.ID, user.WalletAddress, req.WalletAddress, req.Token, req.Amount)
	raw, err := h.trade.PrepareDeposit(c.Request.Context(), user.ID, req.WalletAddress, req.Token, req.Amount, req.ChainID)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"unsignedTx": unsignedTxJSON(raw)},
	})
}

func (h *P2PHandler) ConfirmDeposit(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	var req confirmPoolMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	ev, err := h.trade.ConfirmDeposit(c.Request.Context(), user.ID, req.WalletAddress, req.Token, req.Amount, req.TxHash, req.ChainID)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"token":  req.Token,
			"amount": ev.Amount.String(),
		},
	})
}

func (h *P2PHandler) PrepareWithdraw(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	var req poolMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	raw, err := h.trade.PrepareWithdraw(c.Request.Context(), user.ID, req.WalletAddress, req.Token, req.Amount, req.ChainID)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"unsignedTx": unsignedTxJSON(raw)},
	})
}

func (h *P2PHandler) ConfirmWithdraw(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	var req confirmPoolMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	ev, err := h.trade.ConfirmWithdraw(c.Request.Context(), user.ID, req.WalletAddress, req.Token, req.Amount, req.TxHash, req.ChainID)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"token":  req.Token,
			"amount": ev.Amount.String(),
		},
	})
}

// GetPoolBalance reads (available, locked) pool balance for a given wallet
// on a given token. Pool balances are on-chain public data, so we don't
// gate by owner — any authenticated user may read any wallet's balances
// (same as Sonicscan would show). Query params: ?token=USDT&address=0x...
func (h *P2PHandler) GetPoolBalance(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	token := c.Query("token")
	address := c.Query("address")
	if token == "" || address == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "token and address query params are required"})
		return
	}
	chainID, _ := strconv.ParseInt(c.Query("chainId"), 10, 64)
	bal, err := h.trade.GetPoolBalance(c.Request.Context(), address, token, chainID)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"available": bal.Available.String(),
			"locked":    bal.Locked.String(),
		},
	})
}

// ─── Ads ───────────────────────────────────────────────────────────────────

type createAdRequest struct {
	// Side selects sell-ad (default) or buy-ad. Sell-ads use the off-chain
	// voucher/pool flow; buy-ads are JIT-funded by the user at trade-open.
	Side         string `json:"side"`
	Token        string `json:"token" binding:"required"`
	TokenAmount  string `json:"tokenAmount" binding:"required"`
	FiatCurrency string `json:"fiatCurrency" binding:"required"`
	// Rate is required for fixed-rate ads (the default) but must be omitted
	// for float-rate ads. The service-layer validator enforces this; we
	// can't use `binding:"required"` here because Gin would reject float
	// requests before the service sees them.
	Rate           string `json:"rate"`
	RateType       string `json:"rateType"`
	FloatOffsetBps *int   `json:"floatOffsetBps"`
	FloatAnchor    string `json:"floatAnchor"`
	MinLimit       string `json:"minLimit"`
	MaxLimit       string `json:"maxLimit"`
	// Optional at the binding layer: PM ads settle via the merchant's Nomba VA
	// and need no manual method. The service requires >=1 for non-PM ads.
	PaymentMethodIDs []string `json:"paymentMethodIds" binding:"omitempty"`
	Terms            string   `json:"terms"`
	SellerAddress    string   `json:"sellerAddress" binding:"required"`
	// ChainID selects the P2P chain. Omit (or send 0) to default to Sonic.
	ChainID int64 `json:"chainId"`
}

func (h *P2PHandler) CreateAd(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	var req createAdRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	// Only merchants (and admin tiers, who are implicitly trusted) may
	// publish ads. The pool-model rewrite makes ad creation cheap (off-chain
	// voucher signing, no per-ad gas), but we still want a verified
	// counterparty behind every ad to keep the marketplace clean of spam /
	// unverified sellers. Buyer-side actions (open trade, mark paid, dispute)
	// stay open to all authenticated users. An active LP is admitted only on
	// the pool chain, for its stock ad; never for an ordinary ad (plan B I1).
	lpOnPool := h.trade.IsPoolChain(req.ChainID) && h.trade.IsActiveLP(c.Request.Context(), user.ID)
	if user.Role != models.RoleMerchant && !hasAdminPrivilege(c, user) && !lpOnPool {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "Merchant account required to publish P2P ads.",
		})
		return
	}
	// Validate side up-front so a typo doesn't silently fall through to the
	// sell-ad path. Empty defaults to "sell".
	side := strings.ToLower(strings.TrimSpace(req.Side))
	if side == "" {
		side = models.P2PAdSideSell
	}
	if side != models.P2PAdSideSell && side != models.P2PAdSideBuy {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "side must be 'sell' or 'buy'"})
		return
	}
	pmIDs, err := parseUUIDList(req.PaymentMethodIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	result, err := h.trade.CreateAd(c.Request.Context(), user.ID, req.SellerAddress, services.CreateAdParams{
		Side:             side,
		Token:            req.Token,
		TokenAmount:      req.TokenAmount,
		FiatCurrency:     req.FiatCurrency,
		Rate:             req.Rate,
		RateType:         req.RateType,
		FloatOffsetBps:   req.FloatOffsetBps,
		FloatAnchor:      req.FloatAnchor,
		MinLimit:         req.MinLimit,
		MaxLimit:         req.MaxLimit,
		PaymentMethodIDs: pmIDs,
		Terms:            req.Terms,
		ChainID:          req.ChainID,
	})
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data": gin.H{
			"ad":        result.Ad,
			"typedData": result.TypedData,
		},
	})
}

type confirmTxRequest struct {
	TxHash string `json:"txHash" binding:"required"`
}

// signAdRequest carries the seller's EIP-712 signature over the AdVoucher
// payload returned by CreateAd. Signature is a 65-byte 0x-prefixed hex
// string. SellerAddress is required so the FE explicitly pins the wallet
// that signed (the service's recover step double-checks against this);
// the implicit fallback to user.WalletAddress that we previously had hid
// errors when the active wallet differed from the user's primary.
type signAdRequest struct {
	Signature     string `json:"signature" binding:"required"`
	SellerAddress string `json:"sellerAddress" binding:"required"`
}

func (h *P2PHandler) SignAd(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req signAdRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	sig := ethcommon.FromHex(req.Signature)
	ad, err := h.trade.SignAd(c.Request.Context(), id, user.ID, req.SellerAddress, sig)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": ad})
}

func (h *P2PHandler) GetAd(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ad, err := h.trade.GetAd(c.Request.Context(), id)
	if mapServiceError(c, err) {
		return
	}
	// Detail page benefits from the same effective-rate / staleness signal
	// as the browse list — let the FE disable Open Trade when the feed is stale.
	decorated := h.decorateAdsWithEffectiveRate(c.Request.Context(), []models.P2PAd{*ad})[0]
	// Embed the seller's payment-method rows the buyer should send NGN to.
	// Without this the FE had to call /payment-methods (which returns the
	// CALLER's methods) and filter — guaranteed empty for any buyer who
	// doesn't happen to share PM IDs with the seller, i.e. always.
	detail := adDetailItem{adListItem: decorated}
	if pms, err := h.trade.LoadAdPaymentMethods(c.Request.Context(), ad); err != nil {
		// Non-fatal — render the ad without PMs and let the FE surface a
		// "seller has no payment methods" message instead of 5xx'ing the
		// whole detail view.
		log.Printf("[p2p GetAd] LoadAdPaymentMethods failed for ad=%s: %v", ad.ID, err)
	} else {
		detail.PaymentMethods = pms
	}
	// PM enrichment: if this ad is backed by a payment merchant, attach its
	// static Nomba inbound account so the buy screen can show the pay-to card.
	// Best-effort — a lookup error leaves IsPm=false / PmPayTo=nil.
	if ad.PMID != nil {
		var pm struct {
			InboundAccountNumber string
			InboundBank          string
			Name                 string
		}
		if err := h.db.Raw(
			`SELECT inbound_account_number, inbound_bank, name FROM payment_merchants WHERE id = ?`, *ad.PMID,
		).Scan(&pm).Error; err != nil {
			log.Printf("[p2p GetAd] PM lookup failed for ad=%s pm=%s: %v", ad.ID, *ad.PMID, err)
		} else if pm.InboundAccountNumber != "" || pm.Name != "" {
			detail.IsPm = true
			// The viewer is the prospective buyer — the one who would pay.
			detail.PmPayTo = h.pmPayTo(c.Request.Context(), pm.InboundAccountNumber, pm.InboundBank, pm.Name, &user.ID)
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": detail})
}

type expressQuoteRequest struct {
	Side         string `json:"side" binding:"required"` // 'buy' = user buys crypto; 'sell' = user sells
	Token        string `json:"token" binding:"required"`
	FiatCurrency string `json:"fiatCurrency" binding:"required"`
	Amount       string `json:"amount" binding:"required"` // token base units
}

// ExpressQuote returns the best-matching active ad (+ up to 3 alternatives) for a
// one-shot Express buy/sell, ranked by price. The match is decorated with the same
// effective-rate / seller-stats signals as the browse list; opening it is the
// normal POST /p2p/ads/:id/trades flow.
func (h *P2PHandler) ExpressQuote(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	var req expressQuoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	matches, err := h.trade.ExpressMatch(c.Request.Context(), services.ExpressMatchParams{
		Side: req.Side, Token: req.Token, FiatCurrency: req.FiatCurrency, Amount: req.Amount,
	})
	if mapServiceError(c, err) {
		return
	}
	decorated := h.decorateAdsWithEffectiveRate(c.Request.Context(), matches)
	var best any
	alternatives := []adListItem{}
	if len(decorated) > 0 {
		best = decorated[0]
		end := len(decorated)
		if end > 4 {
			end = 4
		}
		alternatives = decorated[1:end]
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"best": best, "alternatives": alternatives}})
}

// adListItem wraps a P2PAd with the transient rateStale flag computed at
// response time. For float ads, ad.Rate is overwritten with the freshly
// computed effective rate when the feed is fresh; on a stale feed the cached
// ad.Rate is left as-is and rateStale is set so the FE can disable the card.
//
// SellerUsername is also attached here (looked up in a single batched query)
// so the FE can render "@ojay" instead of "Seller •14a0c0" without a per-row
// extra request.
type adListItem struct {
	models.P2PAd
	RateStale            bool                            `json:"rateStale,omitempty"`
	SellerUsername       string                          `json:"sellerUsername,omitempty"`
	PaymentMethodSummary []services.PaymentMethodSummary `json:"paymentMethodSummary,omitempty"`
	SellerStats          *services.SellerStats           `json:"sellerStats,omitempty"`
}

// adDetailItem extends adListItem with the seller's payment-method rows the
// ad accepts. Detail-only — the marketplace list never embeds bank account
// numbers (avoids broadcasting account info to anyone scraping the feed).
type adDetailItem struct {
	adListItem
	PaymentMethods []models.P2PPaymentMethod `json:"paymentMethods,omitempty"`
	// PM-detail fields: when the ad is a PM ad, the buy screen renders a
	// "pay to this Nomba account" card from PmPayTo. Nil/false for manual ads.
	IsPm    bool            `json:"isPm"`
	PmPayTo *models.PmPayTo `json:"pmPayTo,omitempty"`
}

// decorateAdsWithEffectiveRate computes effective rate per row for the list
// endpoints. Mutates the local copy of each ad (not the DB row).
func (h *P2PHandler) decorateAdsWithEffectiveRate(ctx context.Context, rows []models.P2PAd) []adListItem {
	out := make([]adListItem, len(rows))
	// Single batched username lookup keyed by seller_id. Failure is
	// non-fatal — the FE falls back to a truncated UUID.
	ids := make([]uuid.UUID, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].SellerID)
	}
	usernames, err := h.trade.LookupUsernames(ctx, ids)
	if err != nil {
		log.Printf("[p2p] LookupUsernames failed: %v — list response will fall back to seller IDs", err)
		usernames = map[uuid.UUID]string{}
	}
	// Gather every payment-method id referenced across all ads, resolve their
	// providers in one query, then attach a privacy-safe summary per ad.
	pmIDSet := map[uuid.UUID]struct{}{}
	for i := range rows {
		for _, raw := range rows[i].PaymentMethodIDs {
			if id, err := uuid.Parse(raw); err == nil {
				pmIDSet[id] = struct{}{}
			}
		}
	}
	pmIDs := make([]uuid.UUID, 0, len(pmIDSet))
	for id := range pmIDSet {
		pmIDs = append(pmIDs, id)
	}
	providerByID, perr := h.trade.LoadProvidersByMethodIDs(ctx, pmIDs)
	if perr != nil {
		log.Printf("[p2p] LoadProvidersByMethodIDs failed: %v — list will omit payment summaries", perr)
		providerByID = map[uuid.UUID]string{}
	}
	statsByID := map[uuid.UUID]services.SellerStats{}
	if h.stats != nil {
		if m, serr := h.stats.GetStats(ctx, ids); serr != nil {
			log.Printf("[p2p] GetStats failed: %v — list omits sellerStats", serr)
		} else {
			statsByID = m
		}
	}
	for i := range rows {
		ad := rows[i] // local copy — do NOT persist mutations
		item := adListItem{P2PAd: ad}
		if u, ok := usernames[ad.SellerID]; ok {
			item.SellerUsername = u
		}
		if st, ok := statsByID[ad.SellerID]; ok {
			stCopy := st
			item.SellerStats = &stCopy
		}
		slugs := make([]string, 0, len(ad.PaymentMethodIDs))
		for _, raw := range ad.PaymentMethodIDs {
			if id, err := uuid.Parse(raw); err == nil {
				if prov, ok := providerByID[id]; ok {
					slugs = append(slugs, prov)
				}
			}
		}
		item.PaymentMethodSummary = services.PaymentSummaryFromProviders(slugs)
		rate, err := h.trade.ComputeEffectiveRate(&ad)
		if err != nil {
			if errors.Is(err, services.ErrRateStale) {
				item.RateStale = true
				// leave item.Rate at the cached value (last computed)
			}
			// Non-stale errors (e.g. malformed data) fall through with the
			// cached rate so the list endpoint never 5xxs on one bad row.
		} else {
			item.Rate = rate
		}
		out[i] = item
	}
	return out
}

func (h *P2PHandler) ListAds(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	f := services.AdFilter{
		Side:         c.Query("side"),
		Token:        c.Query("token"),
		FiatCurrency: c.Query("fiatCurrency"),
		Status:       c.Query("status"),
		Limit:        limit,
		Offset:       offset,
		// Marketplace browse: nobody may open a trade on an ad in a delisted
		// asset, so don't offer one. The owner sees theirs via ListMyAds.
		ExcludeDelisted: true,
	}
	if seller := c.Query("sellerId"); seller != "" {
		if sid, err := uuid.Parse(seller); err == nil {
			f.SellerID = sid
		}
	}
	if cid := c.Query("chainId"); cid != "" {
		if n, err := strconv.ParseInt(cid, 10, 64); err == nil {
			f.ChainID = n
		}
	}
	if pm := c.Query("pm"); pm != "" {
		b := pm == "true" || pm == "1"
		f.PM = &b
	}
	if f.Status == "" {
		// Default: only active marketplace ads.
		f.Status = models.P2PAdStatusActive
	}
	rows, err := h.trade.ListAds(c.Request.Context(), f)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h.decorateAdsWithEffectiveRate(c.Request.Context(), rows)})
}

func (h *P2PHandler) ListMyAds(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	rows, err := h.trade.ListAds(c.Request.Context(), services.AdFilter{
		SellerID: user.ID,
		Status:   c.Query("status"),
		Limit:    limit,
		Offset:   offset,
	})
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h.decorateAdsWithEffectiveRate(c.Request.Context(), rows)})
}

func (h *P2PHandler) PauseAd(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.trade.PauseAd(c.Request.Context(), id, user.ID); mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"adId": id, "status": "paused"}})
}

func (h *P2PHandler) ResumeAd(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.trade.ResumeAd(c.Request.Context(), id, user.ID); mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"adId": id, "status": "active"}})
}

type cancelAdRequest struct {
	SellerAddress string `json:"sellerAddress" binding:"required"`
}

// PrepareCancelAd returns the unsigned cancelVoucher tx for an active or
// paused ad. Renamed from PrepareCloseAd in the pool-model migration —
// cancellation is on-chain because the contract is the source of truth for
// voucher revocation.
func (h *P2PHandler) PrepareCancelAd(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req cancelAdRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	// Buy-ads have no on-chain voucher — cancellation is a DB-only flip with no
	// unsigned tx to broadcast. Detect the side and short-circuit so the FE
	// doesn't have to know which cancel flavour to call.
	if ad, err := h.trade.GetAd(c.Request.Context(), id); err == nil && ad.Side == models.P2PAdSideBuy {
		cancelled, err := h.trade.CancelBuyAd(c.Request.Context(), id, user.ID)
		if mapServiceError(c, err) {
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data":    gin.H{"ad": cancelled, "unsignedTx": nil},
		})
		return
	}
	raw, err := h.trade.PrepareCancelAd(c.Request.Context(), id, user.ID, req.SellerAddress)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"unsignedTx": unsignedTxJSON(raw)},
	})
}

func (h *P2PHandler) ConfirmCancelAd(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req confirmTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	ad, err := h.trade.ConfirmCancelAd(c.Request.Context(), id, user.ID, req.TxHash)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": ad})
}

// ─── Trades ────────────────────────────────────────────────────────────────

// openTradeRequest carries the opener's wallet. For a sell-ad the opener is
// the BUYER (BuyerAddress); for a buy-ad the opener is the SELLER/depositor
// (SellerAddress). Both are optional at the binding layer — the handler picks
// the non-empty one (falling back to the user's primary wallet) and the
// service branches on the ad's side. On the buy-ad/JIT sell path TokenAmount
// (base units) is the authoritative crypto amount the user is selling — the
// app approves exactly this, and the service derives fiat from it; on the
// sell-ad path TokenAmount is empty and the amount is derived from fiat.
type openTradeRequest struct {
	FiatAmount string `json:"fiatAmount"`
	// Optional: PM ads settle to the merchant's Nomba VA, so the buyer selects
	// no manual method. The service waives it when the ad is a PM ad.
	PaymentMethodID string `json:"paymentMethodId" binding:"omitempty"`
	BuyerAddress    string `json:"buyerAddress"`
	SellerAddress   string `json:"sellerAddress"`
	TokenAmount     string `json:"tokenAmount"`
	// Payout destination for a PM sell trade (opening a sell against a PM
	// buy-ad): the bank account the PM automation pays the user to. Optional at
	// the binding layer; the service requires account+bank when the ad is a PM
	// buy-ad and ignores them otherwise.
	PayoutAccountNumber string `json:"payoutAccountNumber"`
	PayoutBank          string `json:"payoutBank"`
	PayoutAccountName   string `json:"payoutAccountName"`
}

func (h *P2PHandler) OpenTrade(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	adID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req openTradeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	// paymentMethodId is optional — PM ads carry no manual method (the service
	// waives it). Parse only when present; leave uuid.Nil otherwise.
	var pmID uuid.UUID
	if rawPM := strings.TrimSpace(req.PaymentMethodID); rawPM != "" {
		parsed, perr := uuid.Parse(rawPM)
		if perr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid paymentMethodId"})
			return
		}
		pmID = parsed
	}
	// One of fiatAmount / tokenAmount must be present: sell-ad takes are
	// fiat-driven; buy-ad (JIT sell) opens are tokenAmount-authoritative. The
	// service derives the other from the ad's rate.
	if strings.TrimSpace(req.FiatAmount) == "" && strings.TrimSpace(req.TokenAmount) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "fiatAmount or tokenAmount is required"})
		return
	}
	// Opener wallet: BuyerAddress (sell-ad take) or SellerAddress (buy-ad/JIT
	// take). The service branches on ad.Side; we just thread the wallet the
	// user is operating from. Fall back to the user's primary wallet.
	openerAddr := req.BuyerAddress
	if openerAddr == "" {
		openerAddr = req.SellerAddress
	}
	if openerAddr == "" {
		openerAddr = user.WalletAddress
	}
	trade, raw, err := h.trade.OpenTrade(c.Request.Context(), adID, user.ID, openerAddr, services.OpenTradeParams{
		FiatAmount:          req.FiatAmount,
		PaymentMethodID:     pmID,
		TokenAmount:         req.TokenAmount,
		PayoutAccountNumber: req.PayoutAccountNumber,
		PayoutBank:          req.PayoutBank,
		PayoutAccountName:   req.PayoutAccountName,
	})
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data": gin.H{
			"trade":      trade,
			"unsignedTx": unsignedTxJSON(raw),
		},
	})
}

// confirmTradeRequest mirrors confirmAdRequest but for trade confirmations —
// includes an optional caller address (buyer for open, seller for release).
type confirmTradeRequest struct {
	TxHash        string `json:"txHash" binding:"required"`
	CallerAddress string `json:"callerAddress"`
}

func (h *P2PHandler) ConfirmOpenTrade(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req confirmTradeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	buyerAddr := req.CallerAddress
	if buyerAddr == "" {
		buyerAddr = user.WalletAddress
	}
	trade, err := h.trade.ConfirmOpenTrade(c.Request.Context(), id, user.ID, buyerAddr, req.TxHash)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": trade})
}

type markPaidRequest struct {
	ProofImageURL string `json:"proofImageUrl" binding:"required"`
}

func (h *P2PHandler) MarkPaid(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req markPaidRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	trade, err := h.trade.MarkPaid(c.Request.Context(), id, user.ID, req.ProofImageURL)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": trade})
}

type releaseRequest struct {
	SellerAddress string `json:"sellerAddress" binding:"required"`
}

func (h *P2PHandler) PrepareRelease(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req releaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	result, err := h.trade.PrepareRelease(c.Request.Context(), id, user.ID, req.SellerAddress)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"unsignedTx": unsignedTxJSON(result.UnsignedTx)},
	})
}

type cancelRequest struct {
	CallerAddress string `json:"callerAddress"`
}

func (h *P2PHandler) PrepareCancel(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req cancelRequest
	// Tolerate an empty request body — callerAddress is optional and defaults
	// to user.WalletAddress. Match the EOF sentinel via errors.Is rather than
	// stringly comparing err.Error() so future gin-binding versions that
	// reword the message ("unexpected EOF" etc.) don't accidentally start
	// rejecting empty bodies.
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	callerAddr := req.CallerAddress
	if callerAddr == "" {
		callerAddr = user.WalletAddress
	}
	raw, err := h.trade.PrepareCancel(c.Request.Context(), id, user.ID, callerAddr)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"unsignedTx": unsignedTxJSON(raw)},
	})
}

type raiseDisputeRequest struct {
	Reason        string   `json:"reason" binding:"required"`
	CallerAddress string   `json:"callerAddress"`
	EvidenceUrls  []string `json:"evidenceUrls"`
}

func (h *P2PHandler) RaiseDispute(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req raiseDisputeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	caller := req.CallerAddress
	if caller == "" {
		caller = user.WalletAddress
	}
	raw, err := h.trade.PrepareRaiseDispute(c.Request.Context(), id, user.ID, caller, req.Reason, req.EvidenceUrls)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"unsignedTx": unsignedTxJSON(raw)},
	})
}

func (h *P2PHandler) ConfirmRaiseDispute(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req confirmTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	trade, err := h.trade.ConfirmRaiseDispute(c.Request.Context(), id, user.ID, req.TxHash)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": trade})
}

func (h *P2PHandler) GetDispute(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	d, err := h.trade.GetDispute(c.Request.Context(), id, user.ID, false)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": d})
}

// ─── Chat messages (Step 10) ───────────────────────────────────────────────

type sendMessageRequest struct {
	Type     string `json:"type" binding:"required"`
	Body     string `json:"body"`
	ImageURL string `json:"imageUrl"`
}

func (h *P2PHandler) SendMessage(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	tradeID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if h.messages == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Chat not configured"})
		return
	}
	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	var msg *models.P2PMessage
	var err error
	switch req.Type {
	case models.P2PMessageTypeText:
		msg, err = h.messages.SendText(c.Request.Context(), tradeID, user.ID, req.Body)
	case models.P2PMessageTypeImage:
		msg, err = h.messages.SendImage(c.Request.Context(), tradeID, user.ID, req.ImageURL)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "type must be text or image"})
		return
	}
	if mapMessageError(c, err) {
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": msg})
}

func (h *P2PHandler) ListMessages(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	tradeID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if h.messages == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Chat not configured"})
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	var since *time.Time
	if s := c.Query("since"); s != "" {
		if parsed, err := time.Parse(time.RFC3339, s); err == nil {
			since = &parsed
		}
	}
	rows, err := h.messages.List(c.Request.Context(), tradeID, user.ID, services.MessageFilter{
		Since: since,
		Limit: limit,
	})
	if mapMessageError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows})
}

// mapMessageError extends mapServiceError with chat-specific sentinels.
func mapMessageError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, services.ErrMessageEmpty),
		errors.Is(err, services.ErrImageURLInvalid):
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return true
	case errors.Is(err, services.ErrMessageTooLong):
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"success": false, "message": err.Error()})
		return true
	}
	return mapServiceError(c, err)
}

// ─── Admin dispute resolution ──────────────────────────────────────────────

// adminRequire is the admin equivalent of `require` — auth middleware
// (`adminAuth`) already enforces role; this only pulls the user out.
func (h *P2PHandler) adminRequire(c *gin.Context) (*models.User, bool) {
	user := getUserFromContext(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"success": false, "message": "Please authenticate",
		})
		return nil, false
	}
	return user, true
}

func (h *P2PHandler) AdminListDisputes(c *gin.Context) {
	if _, ok := h.adminRequire(c); !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	status := c.Query("status")
	if status == "" {
		status = models.P2PDisputeStatusOpen
	}
	rows, err := h.trade.ListDisputes(c.Request.Context(), status, limit, offset)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows})
}

func (h *P2PHandler) AdminGetDispute(c *gin.Context) {
	if _, ok := h.adminRequire(c); !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	d, err := h.trade.GetDispute(c.Request.Context(), id, uuid.Nil, true)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": d})
}

type p2pAdminResolveRequest struct {
	RefundBuyer  bool   `json:"refundBuyer"`
	Note         string `json:"note"`
	AdminAddress string `json:"adminAddress"`
}

func (h *P2PHandler) AdminResolveDispute(c *gin.Context) {
	admin, ok := h.adminRequire(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req p2pAdminResolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	addr := req.AdminAddress
	if addr == "" {
		addr = admin.WalletAddress
	}
	raw, err := h.trade.PrepareAdminResolveDispute(c.Request.Context(), id, admin.ID, addr, req.RefundBuyer, req.Note)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"unsignedTx": unsignedTxJSON(raw)},
	})
}

func (h *P2PHandler) ConfirmAdminResolveDispute(c *gin.Context) {
	admin, ok := h.adminRequire(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req confirmTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	trade, err := h.trade.ConfirmAdminResolveDispute(c.Request.Context(), id, admin.ID, req.TxHash)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": trade})
}

func (h *P2PHandler) ConfirmCancel(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req confirmTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	trade, err := h.trade.ConfirmCancel(c.Request.Context(), id, user.ID, req.TxHash)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": trade})
}

func (h *P2PHandler) ConfirmRelease(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req confirmTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	trade, err := h.trade.ConfirmRelease(c.Request.Context(), id, user.ID, req.TxHash)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": trade})
}

func (h *P2PHandler) GetTrade(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	trade, err := h.trade.GetTrade(c.Request.Context(), id, user.ID)
	if mapServiceError(c, err) {
		return
	}
	h.enrichTradeWithPM(c.Request.Context(), trade, user.ID)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": trade})
}

// enrichTradeWithPM populates the trade's virtual PM-detail fields (IsPM,
// PmPayTo, PayerName) for the buy screen. Best-effort: any query error is
// logged and the fields are left unset — the PM enrichment must never fail
// the trade endpoint. Adds ≤2 lightweight queries.
func (h *P2PHandler) enrichTradeWithPM(ctx context.Context, trade *models.P2PTrade, callerID uuid.UUID) {
	if trade == nil || h.db == nil {
		return
	}
	var pm struct {
		InboundAccountNumber string
		InboundBank          string
		Name                 string
	}
	err := h.db.Raw(
		`SELECT pm.inbound_account_number, pm.inbound_bank, pm.name
		 FROM p2p_ads a JOIN payment_merchants pm ON pm.id = a.pm_id
		 WHERE a.id = ?`, trade.AdID,
	).Scan(&pm).Error
	if err != nil {
		log.Printf("[p2p GetTrade] PM lookup failed for trade=%s ad=%s: %v", trade.ID, trade.AdID, err)
	} else if pm.InboundAccountNumber != "" || pm.Name != "" {
		trade.IsPM = true
		// Name the buyer only to the buyer; the seller sees the generic wording.
		var payerID *uuid.UUID
		if callerID == trade.BuyerID {
			payerID = &trade.BuyerID
		}
		trade.PmPayTo = h.pmPayTo(ctx, pm.InboundAccountNumber, pm.InboundBank, pm.Name, payerID)
	}

	var payerName string
	if err := h.db.Raw(`SELECT name FROM users WHERE id = ?`, trade.BuyerID).Scan(&payerName).Error; err != nil {
		log.Printf("[p2p GetTrade] payer name lookup failed for trade=%s buyer=%s: %v", trade.ID, trade.BuyerID, err)
	} else {
		trade.PayerName = payerName
	}
}

func (h *P2PHandler) ListTrades(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	rows, err := h.trade.ListTrades(c.Request.Context(), services.TradeFilter{
		Role:   c.Query("role"),
		UserID: user.ID,
		Status: c.Query("status"),
		Limit:  limit,
		Offset: offset,
	})
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows})
}

// ─── Seller profile & ratings ─────────────────────────────────────────────

func (h *P2PHandler) GetSellerProfile(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if h.stats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "reputation service unavailable"})
		return
	}
	profile, err := h.stats.GetProfile(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	ads, err := h.trade.ListAds(c.Request.Context(), services.AdFilter{SellerID: id, Status: models.P2PAdStatusActive, ExcludeDelisted: true})
	if err != nil {
		ads = nil // non-fatal
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"userId": profile.UserID, "stats": profile.Stats,
		"recentRatings": profile.RecentRatings, "activeAds": ads,
	}})
}

type rateTradeRequest struct {
	Value int16 `json:"value"`
}

func (h *P2PHandler) RateTrade(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	tradeID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req rateTradeRequest
	if err := c.ShouldBindJSON(&req); err != nil || (req.Value != 1 && req.Value != -1) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "value must be 1 or -1"})
		return
	}
	var trade models.P2PTrade
	if err := h.db.First(&trade, "id = ?", tradeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "trade not found"})
		return
	}
	if user.ID != trade.BuyerID && user.ID != trade.SellerID {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "not a party to this trade"})
		return
	}
	if trade.Status != models.P2PTradeStatusReleased {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "message": "can only rate a released trade"})
		return
	}
	ratee := trade.SellerID
	if user.ID == trade.SellerID {
		ratee = trade.BuyerID
	}
	rating := &models.P2PRating{ID: uuid.New(), TradeID: tradeID, RaterID: user.ID, RateeID: ratee, Value: req.Value}
	if err := h.db.Create(rating).Error; err != nil {
		var existing int64
		h.db.Model(&models.P2PRating{}).
			Where("trade_id = ? AND rater_id = ?", tradeID, user.ID).Count(&existing)
		if existing > 0 {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "already rated this trade"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "could not save rating"})
		}
		return
	}
	_ = h.stats.RecordRating(c.Request.Context(), rating)
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": rating})
}

// ─── pure helpers ──────────────────────────────────────────────────────────

func parseUUIDList(in []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(in))
	for _, s := range in {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("invalid UUID: %s", s)
		}
		out = append(out, id)
	}
	return out, nil
}

// isISO2Country reports whether s is two ASCII letters. Shape only — the
// provider is the authority on which countries it actually serves, and
// duplicating that list here would just drift.
func isISO2Country(s string) bool {
	if len(s) != 2 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
