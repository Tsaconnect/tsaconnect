package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// ITHandler serves /api/it/* — Instant Transfer (buy/sell MCGP with Naira via
// the company PM, routed through Contract B's spend-only premium). Registered
// only when IT is configured (operator signer present); dormant otherwise.
//
// In this phase the NGN legs are admin-approved (buy credit / sell settle /
// reverse) because auto-credit is gated on the confirmed strong attribution key.
type ITHandler struct {
	db *gorm.DB
	it *services.ITService
}

func NewITHandler(db *gorm.DB, it *services.ITService) *ITHandler {
	return &ITHandler{db: db, it: it}
}

func (h *ITHandler) userWallet(userID uuid.UUID) string {
	var u models.User
	if err := h.db.First(&u, "id = ?", userID).Error; err != nil {
		return ""
	}
	return u.WalletAddress
}

// GET /api/it/rate — the live Naira buy/sell rates, derived from the admin-set
// USD price and the current FX feed. Same JSON shape as before so the app is
// unchanged. 404 when no price is set; 503 when FX pricing is unavailable.
func (h *ITHandler) GetRate(c *gin.Context) {
	buyKobo, sellKobo, fx, err := h.it.EffectiveKoboRates(c.Request.Context())
	if err != nil {
		if errors.Is(err, services.ErrITNoRate) {
			utils.ErrorResponse(c, http.StatusNotFound, "IT rate not set")
			return
		}
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Instant Transfer pricing is temporarily unavailable")
		return
	}
	// `stale` is additive (existing keys unchanged). The display tolerates a stale
	// rate, but the app should refuse to quote money against it — buy/sell on the
	// server reject stale FX outright.
	utils.SuccessResponse(c, http.StatusOK, "Rate", gin.H{
		"buyRateKobo": buyKobo.String(), "sellRateKobo": sellKobo.String(),
		"stale": fx.Stale,
	})
}

// GET /api/it/withdraw/quote?ngn=<amt>&currency=<ISO> — live FX preview for a
// multi-currency withdrawal: how much destCurrency an NGN amount converts to
// right now, via a pass-through Flutterwave quote (no company spread). 400 on
// bad params; 503 when the live FX quote source is unavailable (Flutterwave
// dormant or an unusable rate).
func (h *ITHandler) QuoteWithdrawal(c *gin.Context) {
	currency := strings.ToUpper(strings.TrimSpace(c.Query("currency")))
	if currency == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "currency is required")
		return
	}
	ngn, err := strconv.ParseFloat(strings.TrimSpace(c.Query("ngn")), 64)
	if err != nil || ngn <= 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "ngn must be a positive number")
		return
	}
	destAmount, rate, err := h.it.QuoteWithdrawal(c.Request.Context(), ngn, currency)
	if err != nil {
		if errors.Is(err, services.ErrITQuoteUnavailable) {
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "Instant Transfer FX quote is temporarily unavailable")
			return
		}
		if errors.Is(err, services.ErrUnsupportedWithdrawalCurrency) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Withdrawals in this currency are not yet supported")
			return
		}
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	// `stale` is additive for parity with GET /api/it/rate's response shape.
	// This quote is fetched live per-request (no cached feed behind it), so
	// there is no staleness signal to surface here — always false.
	utils.SuccessResponse(c, http.StatusOK, "Quote", gin.H{
		"destCurrency": currency,
		"destAmount":   destAmount,
		"rate":         rate,
		"stale":        false,
	})
}

// GET /api/it/withdraw/destinations — the product catalog of currency/rail
// (and, for mobile_money, network) combinations IT withdrawal supports.
// Currently NGN/bank only. The app's destination picker renders only these
// entries so it never offers a rail the API can't fulfill. Single source of
// truth: services.WithdrawalDestinationCatalog — also backs
// SupportedWithdrawalQuoteCurrencies and the Sell destination validation
// below, so this list can't drift from what Sell actually accepts.
func (h *ITHandler) WithdrawalDestinations(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Destinations", gin.H{
		"destinations": services.WithdrawalDestinationCatalog,
	})
}

// GET /api/it/balance — premium breakdown + legacy.
func (h *ITHandler) GetBalance(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	swap := h.it.Swap()
	bought, _ := swap.PremiumBought(user.WalletAddress)
	credited, _ := swap.PremiumCredited(user.WalletAddress)
	pending, _ := swap.PendingSettle(user.WalletAddress)
	legacy, _ := swap.LegacyRemaining(user.WalletAddress)
	utils.SuccessResponse(c, http.StatusOK, "Balance", gin.H{
		"premiumBought":   bigOrZero(bought),   // self-exitable (USDC-bought)
		"premiumCredited": bigOrZero(credited), // IT/NGN, spend-only
		"pendingSettle":   bigOrZero(pending),  // in-flight NGN sell
		"legacyRemaining": bigOrZero(legacy),
	})
}

type itBuyReq struct {
	NgnKobo string `json:"ngnKobo"`
}

type itActivateVAReq struct {
	IDType   string `json:"idType"`
	IDNumber string `json:"idNumber"`
}

func fiatVAPayTo(va *models.FiatVirtualAccount, fallbackName string) gin.H {
	name := strings.TrimSpace(va.AccountName)
	if name == "" {
		name = strings.TrimSpace(fallbackName)
	}
	return gin.H{
		"accountNumber": va.AccountNumber,
		"bank":          va.BankName,
		"accountName":   name,
	}
}

// fiatVAView is the client-safe projection of a FiatVirtualAccount. The raw
// model is deliberately NOT serialized: fw_customer_id and reference are the
// keys webhook settlement attributes payments on, and clients have no use for
// them.
func fiatVAView(va *models.FiatVirtualAccount) gin.H {
	return gin.H{
		"id":            va.ID,
		"currency":      va.Currency,
		"accountNumber": va.AccountNumber,
		"bankName":      va.BankName,
		"accountName":   va.AccountName,
		"status":        va.Status,
		"createdAt":     va.CreatedAt,
	}
}

// GET /api/it/va — standing pay-into account for IT buys (Spec 1).
func (h *ITHandler) GetVA(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	va, err := h.it.GetFiatVA(c.Request.Context(), user.ID, "NGN")
	if err != nil {
		if errors.Is(err, services.ErrITVANotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "No Instant Transfer account yet — activate with BVN or NIN")
			return
		}
		log.Printf("[it] GetVA user=%s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not load Instant Transfer account")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Instant Transfer account", gin.H{
		"va":    fiatVAView(va),
		"payTo": fiatVAPayTo(va, user.Name),
	})
}

// POST /api/it/va/activate — one-time BVN/NIN gate; mints permanent static VA.
// idNumber is forwarded to Flutterwave and NEVER stored or logged.
func (h *ITHandler) ActivateVA(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	if !h.requireITAgreement(c, user.ID) {
		return
	}
	if !h.it.HasFiatVAProvisioning() {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Instant Transfer account activation unavailable")
		return
	}
	var req itActivateVAReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	idType := strings.ToUpper(strings.TrimSpace(req.IDType))
	if idType != "BVN" && idType != "NIN" {
		utils.ErrorResponse(c, http.StatusBadRequest, "idType must be BVN or NIN")
		return
	}
	idNumber := strings.TrimSpace(req.IDNumber)
	if !utils.IsValidNgIDNumber(idNumber) {
		utils.ErrorResponse(c, http.StatusBadRequest, idType+" must be exactly 11 digits")
		return
	}

	va, err := h.it.ActivateFiatVA(c.Request.Context(), user, idType, idNumber)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrITVANotConfigured):
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "Instant Transfer account activation unavailable")
		case errors.Is(err, services.ErrITVAWalletRequired):
			utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		case errors.Is(err, services.ErrITKYCRequired):
			utils.ErrorResponse(c, http.StatusBadRequest, "A BVN or NIN is required")
		default:
			// Never log idNumber — only user id + error.
			log.Printf("[it] ActivateVA user=%s: %v", user.ID, err)
			msg := err.Error()
			if strings.Contains(msg, "flutterwave") {
				utils.ErrorResponse(c, http.StatusBadRequest, "Could not verify identity — check your "+idType+" and try again")
				return
			}
			utils.ErrorResponse(c, http.StatusInternalServerError, "Activation failed — please try again")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Instant Transfer account activated", gin.H{
		"va":    fiatVAView(va),
		"payTo": fiatVAPayTo(va, user.Name),
	})
}

// POST /api/it/buy — open a pending buy order + standing VA details.
// Settlement still happens when funds land (live rate, actual amount); the
// pending intent gives the user progress on the hub until then.
func (h *ITHandler) Buy(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	if !h.requireITAgreement(c, user.ID) {
		return
	}
	var req itBuyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	va, err := h.it.GetFiatVA(c.Request.Context(), user.ID, "NGN")
	if errors.Is(err, services.ErrITVANotFound) {
		mcgp, rate, qerr := h.it.QuoteBuyAmount(c.Request.Context(), req.NgnKobo)
		if qerr != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, qerr.Error())
			return
		}
		utils.SuccessResponse(c, http.StatusOK, "Activation required", gin.H{
			"needsActivation": true,
			"quote": gin.H{
				"ngnKobo":    strings.TrimSpace(req.NgnKobo),
				"mcgpAmount": mcgp.String(),
				"rateKobo":   rate.String(),
			},
			"payTo":  nil,
			"intent": nil,
		})
		return
	}
	if err != nil {
		log.Printf("[it] Buy GetFiatVA user=%s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not load Instant Transfer account")
		return
	}

	intent, err := h.it.OpenBuyOrder(c.Request.Context(), user.ID, user.WalletAddress, req.NgnKobo)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Buy order opened", gin.H{
		"needsActivation": false,
		"quote": gin.H{
			// ngnKobo is what the user transfers; mcgpAmount is what they get
			// AFTER fees — feeNgnKobo is the difference, in naira.
			"ngnKobo":    intent.NgnKobo,
			"mcgpAmount": intent.McgpAmount,
			"rateKobo":   intent.RateKobo,
			"feeNgnKobo": intent.FeeNgnKobo,
			"netNgnKobo": intent.NetNgnKobo,
		},
		"payTo":  fiatVAPayTo(va, user.Name),
		"intent": intent,
	})
}

type itSellReq struct {
	McgpAmount string `json:"mcgpAmount"`
	// Payout destination — required when auto-payout is active so the fiat
	// leg can fire without an operator; optional on the legacy manual flow.
	BankCode      string `json:"bankCode"`
	AccountNumber string `json:"accountNumber"`
	AccountName   string `json:"accountName"`

	// Multi-currency / mobile-money payout destination — optional. Empty
	// DestinationCurrency and RailType ⇒ legacy NGN bank (unchanged, does not
	// require these fields). When either is set, the (currency, rail[,
	// network]) combination is validated against
	// services.WithdrawalDestinationCatalog before the intent is created.
	DestinationCurrency string `json:"destinationCurrency"`
	RailType            string `json:"railType"`
	MobileNetwork       string `json:"mobileNetwork"`
	MobileCountry       string `json:"mobileCountry"`
	MobileMsisdn        string `json:"mobileMsisdn"`
}

// POST /api/it/sell — debit credited premium (on-chain) and open the NGN payout.
func (h *ITHandler) Sell(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	if !h.requireITAgreement(c, user.ID) {
		return
	}
	var req itSellReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	// A rail/currency selection must resolve to a catalog entry BEFORE we ever
	// touch the service — reject an unfulfillable destination up front rather
	// than creating an intent the payout leg can't complete. Empty rail AND
	// currency ⇒ legacy NGN bank, which needs no catalog check (it predates
	// the catalog and isn't itself catalog-gated here).
	if req.RailType != "" || req.DestinationCurrency != "" {
		if !services.IsSupportedWithdrawalDestination(req.DestinationCurrency, req.RailType, req.MobileNetwork) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported withdrawal destination for this currency/rail combination")
			return
		}
	}
	rail := strings.ToLower(strings.TrimSpace(req.RailType))
	// When auto-payout is wired, the destination is mandatory — a sell with no
	// bank would silently fall back to the manual queue the PM wants retired.
	// This bank-account requirement only applies to the bank rail (legacy NGN
	// or a catalog bank currency like GHS); mobile_money's required fields are
	// enforced by the service layer (validateSellBank).
	var bank *services.ITSellBank
	switch rail {
	case "", "bank":
		if req.BankCode != "" || req.AccountNumber != "" {
			// The 10-digit NUBAN rule is NGN-specific — a non-NGN bank rail
			// (e.g. GHS) has a different account-number format, so only
			// require non-empty fields here and defer real format validation
			// to Flutterwave/product, same as the mobile_money path below.
			isNGN := req.DestinationCurrency == "" || strings.EqualFold(req.DestinationCurrency, "NGN")
			if isNGN {
				if req.BankCode == "" || len(req.AccountNumber) != 10 {
					utils.ErrorResponse(c, http.StatusBadRequest, "bankCode and a 10-digit accountNumber are required")
					return
				}
			} else if req.BankCode == "" || req.AccountNumber == "" {
				utils.ErrorResponse(c, http.StatusBadRequest, "bankCode and accountNumber are required")
				return
			}
			bank = &services.ITSellBank{Code: req.BankCode, Number: req.AccountNumber, Name: req.AccountName}
		} else if h.it.HasAutoPayout() {
			utils.ErrorResponse(c, http.StatusBadRequest, "Select a payout bank account")
			return
		}
	default:
		bank = &services.ITSellBank{
			MobileNetwork: req.MobileNetwork,
			MobileCountry: req.MobileCountry,
			MobileMsisdn:  req.MobileMsisdn,
		}
	}
	if bank != nil {
		bank.DestinationCurrency = req.DestinationCurrency
		bank.RailType = req.RailType
	}
	intent, err := h.it.CreateSellIntent(c.Request.Context(), user.ID, user.WalletAddress, req.McgpAmount, bank)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sell submitted", gin.H{"intent": intent})
}

// GET /api/it/intents — caller's IT history.
func (h *ITHandler) ListIntents(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	intents, _ := h.it.ListUserIntents(c.Request.Context(), user.ID)
	for i := range intents {
		intents[i].FailureReason = services.UserFacingITFailure(intents[i].FailureReason)
	}
	utils.SuccessResponse(c, http.StatusOK, "Intents", gin.H{"intents": intents})
}

// GET /api/it/intents/:id — single intent (scoped to caller).
func (h *ITHandler) GetIntent(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	intent, err := h.it.GetIntent(c.Request.Context(), id)
	if err != nil || intent.UserID != user.ID {
		utils.ErrorResponse(c, http.StatusNotFound, "Intent not found")
		return
	}
	intent.FailureReason = services.UserFacingITFailure(intent.FailureReason)
	utils.SuccessResponse(c, http.StatusOK, "Intent", gin.H{"intent": intent})
}

// ──────────────────────────────── admin ───────────────────────────────────

type itRateReq struct {
	BuyPriceUsdMicros int64 `json:"buyPriceUsdMicros"`
	SellDiscountBps   int   `json:"sellDiscountBps"`
}

// GET /api/it/admin/rate — the raw USD config plus a derived Naira preview for
// the admin dashboard. 404 when no price is set yet.
func (h *ITHandler) AdminGetRate(c *gin.Context) {
	rate, err := h.it.GetActiveRate(c.Request.Context())
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "IT rate not set")
		return
	}
	out := gin.H{
		"buyPriceUsdMicros": rate.BuyPriceUsdMicros,
		"sellDiscountBps":   rate.SellDiscountBps,
	}
	// Best-effort live preview; omit (not error) when the FX feed is down so the
	// admin can still see and edit the configured USD price.
	if buyKobo, sellKobo, fx, derr := h.it.EffectiveKoboRates(c.Request.Context()); derr == nil {
		out["derived"] = gin.H{
			"buyRateKobo":  buyKobo.String(),
			"sellRateKobo": sellKobo.String(),
			"ngnPerUsd":    fx.NgnPerUsd,
			"fxStale":      fx.Stale,
		}
	}
	utils.SuccessResponse(c, http.StatusOK, "Rate", out)
}

// POST /api/it/admin/rate
func (h *ITHandler) AdminSetRate(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req itRateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	rate, err := h.it.SetRate(c.Request.Context(), admin.ID, req.BuyPriceUsdMicros, req.SellDiscountBps)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Rate updated", gin.H{"rate": rate})
}

// POST /api/it/admin/credit/:id — approve a paid buy intent (operator credits premium).
func (h *ITHandler) AdminApproveCredit(c *gin.Context) {
	h.adminIntentAction(c, func(intentID uuid.UUID, wallet string) (*models.ITIntent, error) {
		return h.it.ApproveBuyCredit(c.Request.Context(), intentID, wallet)
	})
}

// POST /api/it/admin/reprice/:id — reprice and credit a buy parked as
// `awaiting_rate` (money landed while the FX feed was stale). Prices at the
// CURRENT live rate; refuses while FX is still stale.
func (h *ITHandler) AdminRepriceCredit(c *gin.Context) {
	h.adminIntentAction(c, func(intentID uuid.UUID, _ string) (*models.ITIntent, error) {
		return h.it.RepriceAndCreditAwaitingRate(c.Request.Context(), intentID)
	})
}

// POST /api/it/admin/settle/:id — finalize a sell after a confirmed NGN payout.
func (h *ITHandler) AdminSettle(c *gin.Context) {
	h.adminIntentAction(c, func(intentID uuid.UUID, wallet string) (*models.ITIntent, error) {
		return h.it.SettleSell(c.Request.Context(), intentID, wallet)
	})
}

// POST /api/it/admin/reverse/:id — reverse a sell whose NGN payout failed.
func (h *ITHandler) AdminReverse(c *gin.Context) {
	h.adminIntentAction(c, func(intentID uuid.UUID, wallet string) (*models.ITIntent, error) {
		return h.it.ReverseSell(c.Request.Context(), intentID, wallet)
	})
}

// GET /api/it/admin/intents?direction=&status=&limit=&offset= — the operator
// order console. Cross-user, newest first, with optional filters. Each row
// carries the account identity so a reviewer knows whose order it is, and the
// RAW failure_reason (not the softened user copy) so operators see the real
// cause.
func (h *ITHandler) AdminListIntents(c *gin.Context) {
	direction := strings.ToLower(strings.TrimSpace(c.Query("direction")))
	if direction != "" && direction != models.ITDirectionBuy && direction != models.ITDirectionSell {
		utils.ErrorResponse(c, http.StatusBadRequest, "direction must be buy or sell")
		return
	}
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))

	limit := clamp(atoiDefault(c.Query("limit"), defaultPageSize), 1, maxPageSize)
	offset := atoiDefault(c.Query("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	intents, total, err := h.it.ListIntentsAdmin(c.Request.Context(), direction, status, limit, offset)
	if err != nil {
		log.Printf("[it] AdminListIntents: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not load orders")
		return
	}

	// Resolve all account identities in one query, not one per row.
	ids := make([]uuid.UUID, 0, len(intents))
	for i := range intents {
		ids = append(ids, intents[i].UserID)
	}
	accounts := make(map[uuid.UUID]gin.H, len(ids))
	if len(ids) > 0 {
		var users []models.User
		if err := h.db.WithContext(c.Request.Context()).
			Select("id", "username", "name", "email").
			Where("id IN ?", ids).Find(&users).Error; err == nil {
			for _, u := range users {
				accounts[u.ID] = gin.H{"username": u.Username, "name": u.Name, "email": u.Email}
			}
		}
	}
	rows := make([]gin.H, 0, len(intents))
	for i := range intents {
		row := gin.H{"intent": intents[i]}
		if acc, ok := accounts[intents[i].UserID]; ok {
			row["account"] = acc
		}
		rows = append(rows, row)
	}
	utils.SuccessResponse(c, http.StatusOK, "Orders", gin.H{
		"intents": rows, "total": total, "limit": limit, "offset": offset,
	})
}

func (h *ITHandler) adminIntentAction(c *gin.Context, fn func(uuid.UUID, string) (*models.ITIntent, error)) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	intent, err := h.it.GetIntent(c.Request.Context(), id)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Intent not found")
		return
	}
	wallet := h.userWallet(intent.UserID)
	if wallet == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Intent user has no wallet")
		return
	}
	out, err := fn(id, wallet)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "OK", gin.H{"intent": out})
}

// GET /api/it/agreement — the current trading agreement plus whether the caller
// has already accepted it. The app renders the greeting from the user's profile.
func (h *ITHandler) GetAgreement(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	accepted, err := h.it.HasAcceptedITAgreement(c.Request.Context(), user.ID)
	if err != nil {
		log.Printf("[it] GetAgreement user=%s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not load the agreement")
		return
	}
	ag := services.CurrentITAgreement()
	utils.SuccessResponse(c, http.StatusOK, "Agreement", gin.H{
		"version":  ag.Version,
		"title":    ag.Title,
		"intro":    ag.Intro,
		"clauses":  ag.Clauses,
		"accepted": accepted,
	})
}

// POST /api/it/agreement/accept — record the caller's acceptance of the current
// version. Idempotent. The user must be able to trade IT (has a wallet) — but
// the agreement itself gates trading, so we accept without a wallet check here.
func (h *ITHandler) AcceptAgreement(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if err := h.it.AcceptITAgreement(c.Request.Context(), user.ID, c.ClientIP(), c.Request.UserAgent()); err != nil {
		log.Printf("[it] AcceptAgreement user=%s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not record acceptance")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Agreement accepted", gin.H{
		"version":  services.ITAgreementVersion,
		"accepted": true,
	})
}

// requireITAgreement is the shared trade gate for ActivateVA/Buy/Sell. It writes
// a 403 with a machine code the app matches on to show the agreement, and
// returns false when the caller is blocked (handler must stop).
func (h *ITHandler) requireITAgreement(c *gin.Context, userID uuid.UUID) bool {
	err := h.it.RequireITAgreement(c.Request.Context(), userID)
	if err == nil {
		return true
	}
	if errors.Is(err, services.ErrITAgreementRequired) {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"code":    "it_agreement_required",
			"message": "Please review and accept the trading agreement to continue",
		})
		return false
	}
	log.Printf("[it] agreement gate user=%s: %v", userID, err)
	utils.ErrorResponse(c, http.StatusInternalServerError, "Could not verify the trading agreement")
	return false
}
