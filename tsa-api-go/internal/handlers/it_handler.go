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

// POST /api/it/buy — create a buy intent; returns the payment reference to
// include in the Nomba transfer to the company PM.
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
	var req itBuyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	intent, err := h.it.CreateBuyIntent(c.Request.Context(), user.ID, user.WalletAddress, req.NgnKobo, nil)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	// Surface the real pay-into account. Preferred: a per-intent dynamic
	// Flutterwave VA (amount-locked, reference = PaymentRef → webhook
	// auto-credit). Fallback: the company PM's static VA (operator-matched).
	// payTo is null when neither exists so the app can show a clear
	// "payments unavailable" state instead of a fake account.
	var payTo interface{}
	if h.it.HasVAProvisioning() {
		if err := h.it.ProvisionBuyVA(c.Request.Context(), intent); err != nil {
			log.Printf("[it] dynamic VA for intent %s failed (falling back to company VA): %v", intent.ID, err)
		} else {
			payTo = gin.H{
				"accountNumber": intent.VAAccountNumber,
				"bank":          intent.VABankName,
				"accountName":   "TSA Connect",
				"reference":     intent.PaymentRef,
				"expiresInSec":  3600,
			}
		}
	}
	if payTo == nil {
		if pm, _ := h.it.CompanyPM(c.Request.Context()); pm != nil {
			payTo = gin.H{
				"accountNumber": pm.InboundAccountNumber,
				"bank":          pm.InboundBank,
				"accountName":   pm.Name,
				"reference":     intent.PaymentRef,
			}
		}
	}
	utils.SuccessResponse(c, http.StatusOK, "Buy intent created", gin.H{
		"intent":     intent,
		"paymentRef": intent.PaymentRef,
		"payTo":      payTo,
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
