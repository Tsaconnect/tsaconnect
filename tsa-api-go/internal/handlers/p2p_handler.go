package handlers

import (
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
	// nomba is optional — when nil, bank-list and account-resolve endpoints
	// return a 503-ish "feature unavailable" response. The trade routes
	// themselves don't depend on it.
	nomba *services.NombaClient
}

// NewP2PHandler constructs a P2PHandler. Pass nil for `trade` to disable the
// P2P routes — handlers will return a configuration-error response so the
// frontend can show "feature unavailable" cleanly instead of a 500.
func NewP2PHandler(db *gorm.DB, trade *services.P2PTradeService, messages *services.P2PMessageService, nomba *services.NombaClient) *P2PHandler {
	return &P2PHandler{db: db, trade: trade, messages: messages, nomba: nomba}
}

// IsConfigured reports whether the handler has its trade service wired up.
// routes.go checks this so we don't register routes that always 503.
func (h *P2PHandler) IsConfigured() bool {
	return h != nil && h.trade != nil
}

// HasNomba reports whether the bank directory / account-resolve endpoints
// can serve requests. Used by routes.go to skip registering them when
// credentials are absent.
func (h *P2PHandler) HasNomba() bool {
	return h != nil && h.nomba.IsEnabled()
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
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, services.ErrAdNotFound), errors.Is(err, services.ErrTradeNotFound),
		errors.Is(err, services.ErrPaymentMethodMissing),
		errors.Is(err, services.ErrDisputeNotFound):
		status = http.StatusNotFound
	case errors.Is(err, services.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, services.ErrInvalidParams),
		errors.Is(err, services.ErrAmountOutOfRange),
		errors.Is(err, services.ErrAmountOverflow),
		errors.Is(err, services.ErrUnsupportedToken),
		errors.Is(err, services.ErrSelfTrade):
		status = http.StatusBadRequest
	case errors.Is(err, services.ErrAdNotActive),
		errors.Is(err, services.ErrInsufficientRemaining),
		errors.Is(err, services.ErrInvalidTransition),
		errors.Is(err, services.ErrAdHasOpenTrades),
		errors.Is(err, services.ErrDisputeAlreadyOpen):
		status = http.StatusConflict
	case errors.Is(err, services.ErrRateStale):
		status = http.StatusServiceUnavailable
	case errors.Is(err, services.ErrPhase2Migration):
		// Legacy lock-on-publish surface invoked by a stale caller. Map to
		// 410 Gone so the FE can distinguish "your client is out of date"
		// from a generic server bug.
		status = http.StatusGone
	}
	c.AbortWithStatusJSON(status, gin.H{"success": false, "message": err.Error()})
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

// ─── Payment methods ───────────────────────────────────────────────────────

type createPaymentMethodRequest struct {
	Type          string `json:"type" binding:"required"`
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
	if req.Type != models.P2PPaymentTypeBank && req.Type != models.P2PPaymentTypeMobileMoney {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid payment method type"})
		return
	}
	pm := &models.P2PPaymentMethod{
		ID:            uuid.New(),
		UserID:        user.ID,
		Type:          req.Type,
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
	banks, err := h.nomba.ListBanks(c.Request.Context())
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
	bankCode := strings.TrimSpace(c.Query("bankCode"))
	accountNumber := strings.TrimSpace(c.Query("accountNumber"))
	if bankCode == "" || len(accountNumber) != 10 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "bankCode and a 10-digit accountNumber are required",
		})
		return
	}
	result, err := h.nomba.Lookup(c.Request.Context(), bankCode, accountNumber)
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

// poolMoveRequest is the shared shape for prepare-deposit / prepare-withdraw.
// SellerAddress is omitted intentionally — the seller wallet is always the
// authenticated user's wallet (User.WalletAddress); accepting a separate
// sellerAddress would let any user prepare/confirm against another seller's
// wallet, surfacing fake "deposit confirmed" audit rows under the wrong actor.
type poolMoveRequest struct {
	Token  string `json:"token" binding:"required"`
	Amount string `json:"amount" binding:"required"`
}

// confirmPoolMoveRequest carries the tx hash plus the original amount the
// user intended to move. The service verifies the on-chain event amount
// matches `Amount` — without this check, an attacker who learned a victim's
// txHash could submit it under their own (different-amount) deposit.
type confirmPoolMoveRequest struct {
	TxHash string `json:"txHash" binding:"required"`
	Token  string `json:"token" binding:"required"`
	Amount string `json:"amount" binding:"required"`
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
	if user.WalletAddress == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "User has no wallet address"})
		return
	}
	var req poolMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	raw, err := h.trade.PrepareApprove(c.Request.Context(), user.ID, user.WalletAddress, req.Token, req.Amount)
	if mapServiceError(c, err) {
		return
	}
	allowance, allowanceErr := h.trade.GetAllowance(c.Request.Context(), user.WalletAddress, req.Token)
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
func (h *P2PHandler) GetAllowance(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	if user.WalletAddress == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "User has no wallet address"})
		return
	}
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "token query param is required"})
		return
	}
	allowance, err := h.trade.GetAllowance(c.Request.Context(), user.WalletAddress, token)
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
	if user.WalletAddress == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "User has no wallet address"})
		return
	}
	var req poolMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	raw, err := h.trade.PrepareDeposit(c.Request.Context(), user.ID, user.WalletAddress, req.Token, req.Amount)
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
	if user.WalletAddress == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "User has no wallet address"})
		return
	}
	var req confirmPoolMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	ev, err := h.trade.ConfirmDeposit(c.Request.Context(), user.ID, user.WalletAddress, req.Token, req.Amount, req.TxHash)
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
	if user.WalletAddress == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "User has no wallet address"})
		return
	}
	var req poolMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	raw, err := h.trade.PrepareWithdraw(c.Request.Context(), user.ID, user.WalletAddress, req.Token, req.Amount)
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
	if user.WalletAddress == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "User has no wallet address"})
		return
	}
	var req confirmPoolMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	ev, err := h.trade.ConfirmWithdraw(c.Request.Context(), user.ID, user.WalletAddress, req.Token, req.Amount, req.TxHash)
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

// GetPoolBalance reads the seller's own (available, locked) pool balance.
// Restricted to the authenticated wallet — pool balances are on-chain public
// data, but the endpoint scope follows the same invariant as deposit/withdraw
// so a buyer can't trivially enumerate other sellers' balances through this
// surface. Query params: ?token=USDT
func (h *P2PHandler) GetPoolBalance(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	if user.WalletAddress == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "User has no wallet address"})
		return
	}
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "token query param is required"})
		return
	}
	bal, err := h.trade.GetPoolBalance(c.Request.Context(), user.WalletAddress, token)
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
	Token            string   `json:"token" binding:"required"`
	TokenAmount      string   `json:"tokenAmount" binding:"required"`
	FiatCurrency     string   `json:"fiatCurrency" binding:"required"`
	// Rate is required for fixed-rate ads (the default) but must be omitted
	// for float-rate ads. The service-layer validator enforces this; we
	// can't use `binding:"required"` here because Gin would reject float
	// requests before the service sees them.
	Rate             string   `json:"rate"`
	RateType         string   `json:"rateType"`
	FloatOffsetBps   *int     `json:"floatOffsetBps"`
	FloatAnchor      string   `json:"floatAnchor"`
	MinLimit         string   `json:"minLimit"`
	MaxLimit         string   `json:"maxLimit"`
	PaymentMethodIDs []string `json:"paymentMethodIds" binding:"required,min=1"`
	Terms            string   `json:"terms"`
	SellerAddress    string   `json:"sellerAddress" binding:"required"`
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
	pmIDs, err := parseUUIDList(req.PaymentMethodIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	result, err := h.trade.CreateAd(c.Request.Context(), user.ID, req.SellerAddress, services.CreateAdParams{
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
	if _, ok := h.require(c); !ok {
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
	decorated := h.decorateAdsWithEffectiveRate([]models.P2PAd{*ad})[0]
	c.JSON(http.StatusOK, gin.H{"success": true, "data": decorated})
}

// adListItem wraps a P2PAd with the transient rateStale flag computed at
// response time. For float ads, ad.Rate is overwritten with the freshly
// computed effective rate when the feed is fresh; on a stale feed the cached
// ad.Rate is left as-is and rateStale is set so the FE can disable the card.
type adListItem struct {
	models.P2PAd
	RateStale bool `json:"rateStale,omitempty"`
}

// decorateAdsWithEffectiveRate computes effective rate per row for the list
// endpoints. Mutates the local copy of each ad (not the DB row).
func (h *P2PHandler) decorateAdsWithEffectiveRate(rows []models.P2PAd) []adListItem {
	out := make([]adListItem, len(rows))
	for i := range rows {
		ad := rows[i] // local copy — do NOT persist mutations
		item := adListItem{P2PAd: ad}
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
	}
	if seller := c.Query("sellerId"); seller != "" {
		if sid, err := uuid.Parse(seller); err == nil {
			f.SellerID = sid
		}
	}
	if f.Status == "" {
		// Default: only active marketplace ads.
		f.Status = models.P2PAdStatusActive
	}
	rows, err := h.trade.ListAds(c.Request.Context(), f)
	if mapServiceError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h.decorateAdsWithEffectiveRate(rows)})
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
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h.decorateAdsWithEffectiveRate(rows)})
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

type openTradeRequest struct {
	FiatAmount      string `json:"fiatAmount" binding:"required"`
	PaymentMethodID string `json:"paymentMethodId" binding:"required"`
	BuyerAddress    string `json:"buyerAddress" binding:"required"`
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
	pmID, err := uuid.Parse(req.PaymentMethodID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid paymentMethodId"})
		return
	}
	trade, raw, err := h.trade.OpenTrade(c.Request.Context(), adID, user.ID, req.BuyerAddress, services.OpenTradeParams{
		FiatAmount:      req.FiatAmount,
		PaymentMethodID: pmID,
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
	Reason        string `json:"reason" binding:"required"`
	CallerAddress string `json:"callerAddress"`
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
	raw, err := h.trade.PrepareRaiseDispute(c.Request.Context(), id, user.ID, caller, req.Reason)
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
	c.JSON(http.StatusOK, gin.H{"success": true, "data": trade})
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
