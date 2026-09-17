package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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
	db         *gorm.DB
	it         *services.ITService
	currencies *services.CurrencyRegistry
	// legs answers which DIRECTIONS a ramp currency can trade right now. Set
	// separately because the Paycrest stack is built later in boot (and is
	// dormant without a key) — see SetRampLegs.
	legs RampLegSource
}

// RampLegSource is the narrow seam over PaycrestQuoteService this handler
// needs. *services.PaycrestQuoteService satisfies it as-is.
type RampLegSource interface {
	RampLegs(ctx context.Context) (map[string]services.RampLegs, error)
}

func NewITHandler(db *gorm.DB, it *services.ITService, currencies *services.CurrencyRegistry) *ITHandler {
	return &ITHandler{db: db, it: it, currencies: currencies}
}

// SetRampLegs wires the live direction source. Without it, and whenever it
// cannot answer, Currencies falls back to advertising both directions for any
// currency that has a provider — see the reasoning there.
func (h *ITHandler) SetRampLegs(src RampLegSource) { h.legs = src }

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

// rampLegsTimeout bounds the live direction lookup on the currency list. The
// fallback (both directions for provider-backed currencies) is correct and
// instant, so there is no reason to make a user wait on Paycrest to reach it.
const rampLegsTimeout = 2 * time.Second

// GET /api/it/currencies — every currency the ramp knows about, and who serves
// it.
//
// Payout-only currencies are LISTED with an empty rampProvider rather than
// omitted. The app should be able to say "not available in USD" instead of
// silently lacking the option, which reads as a bug to the user.
//
// rampProvider says WHO serves the currency; rampBuy/rampSell say WHICH WAY it
// can trade at this moment. The two are separate because they change for
// different reasons — a provider is a product decision an operator makes, a
// direction is provider liquidity that moves on its own.
func (h *ITHandler) Currencies(c *gin.Context) {
	if h.currencies == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Currency list unavailable")
		return
	}
	type row struct {
		Code           string `json:"code"`
		Exponent       int    `json:"exponent"`
		RampProvider   string `json:"rampProvider"`
		RampBuy        bool   `json:"rampBuy"`
		RampSell       bool   `json:"rampSell"`
		DepositEnabled bool   `json:"depositEnabled"`
		PayoutEnabled  bool   `json:"payoutEnabled"`
		MinPayoutMinor int64  `json:"minPayoutMinor"`
	}

	// Which directions actually have liquidity right now. A currency can have a
	// provider and still trade one way only: UGX and TZS are offramp-only
	// today, so the app must show Sell without Buy.
	//
	// legs == nil means we could not find out — the Paycrest stack is dormant,
	// or its order book has never loaded. Then every currency WITH a provider
	// is reported as trading both ways, which is what this endpoint said before
	// directions existed.
	//
	// That fallback is deliberately optimistic, and the choice is asymmetric:
	// reporting "no legs" would take the live NGN rail out of the app because a
	// public book was briefly unreachable, while an over-reported leg costs the
	// user one error at quote time and nothing more — CreateBuy/CreateSell
	// still refuse an order no venue can fill.
	var legs map[string]services.RampLegs
	if h.legs != nil {
		// Bounded well under the Paycrest client's own 30s timeout: this runs
		// on every hub focus, the answer is cached for 20s, and the fallback
		// below is both cheap and safe — so waiting on a slow upstream buys
		// nothing a user would want.
		ctx, cancel := context.WithTimeout(c.Request.Context(), rampLegsTimeout)
		defer cancel()

		var err error
		if legs, err = h.legs.RampLegs(ctx); err != nil {
			log.Printf("[IT] ramp_legs_unavailable err=%v — reporting both directions for provider-backed currencies", err)
			legs = nil
		}
		// An EMPTY map is not the same as a nil one in Go, and reading it as
		// "nothing trades" would take every currency dark. RampLegs already
		// refuses to return one, so this is the guard for a future source that
		// does not — the distinction is too costly to leave to one caller.
		if len(legs) == 0 {
			legs = nil
		}
	}

	all := h.currencies.All()
	out := make([]row, 0, len(all))
	for _, cur := range all {
		provider := h.currencies.RampProvider(cur.Code)
		buy, sell := false, false
		// Paycrest specifically, not "any non-empty provider". Two reasons,
		// both silent failures otherwise:
		//
		//   The kill switch. An operator disables a rail by setting
		//   ramp_provider to something unrecognised, and requirePaycrestEnabled
		//   then refuses every order. A != none test would keep reporting the
		//   legs Paycrest's book still lists, so the app would show live
		//   buttons for a rail that refuses everything.
		//
		//   A second provider. The legs come from PAYCREST's book; looking a
		//   Flutterwave currency up in it would report closed with no error and
		//   no log. Any other provider stays false until it has a leg source of
		//   its own, which is the honest answer and matches what the order path
		//   will actually do.
		if provider == services.RampProviderPaycrest {
			if legs == nil {
				buy, sell = true, true
			} else {
				l := legs[strings.ToUpper(cur.Code)]
				buy, sell = l.Buy, l.Sell
			}
		}
		out = append(out, row{
			Code:           cur.Code,
			Exponent:       cur.Exponent,
			RampProvider:   provider,
			RampBuy:        buy,
			RampSell:       sell,
			DepositEnabled: cur.DepositEnabled,
			PayoutEnabled:  cur.PayoutEnabled,
			MinPayoutMinor: cur.MinPayoutMinor,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	utils.SuccessResponse(c, http.StatusOK, "Currencies", out)
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
	wallet, ok := callerWallet(c, user, c.Query("address"))
	if !ok {
		return
	}
	swap := h.it.Swap()
	bought, _ := swap.PremiumBought(wallet)
	credited, _ := swap.PremiumCredited(wallet)
	pending, _ := swap.PendingSettle(wallet)
	legacy, _ := swap.LegacyRemaining(wallet)
	utils.SuccessResponse(c, http.StatusOK, "Balance", gin.H{
		"premiumBought":   bigOrZero(bought),   // self-exitable (USDC-bought)
		"premiumCredited": bigOrZero(credited), // IT/NGN-credited
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

// centralPayTo is the same card as fiatVAPayTo, filled from the one company
// account every payer is sent to after the cutover — plus the figure to send.
//
// The amount is not a display convenience. Nothing we mint survives a bank
// transfer into a shared account, so the payer's name and this exact number are
// the whole attribution key (design §5.3): a buyer who sends a different figure
// is a hold for an operator instead of an instant settlement. It is taken from
// the MINTED order rather than from what the user asked for, because
// OpenBuyOrder snaps the ask to whole naira and the screen must never disagree
// with the row the matcher compares against — which is also why the kobo divide
// exactly here.
//
// Key names match fiatVAPayTo's ("bank", not "bankName") so the buy screen
// renders one card either side of the cutover.
//
// payerLegalName is the requesting user's own verified name, so the instruction
// can name the account holder to pay from; "" gives the generic wording.
func centralPayTo(a services.CentralAccount, ngnKobo, payerLegalName string) gin.H {
	return gin.H{
		"accountNumber": a.AccountNumber,
		"bank":          a.BankName,
		"accountName":   a.AccountName,
		"amountNgn":     wholeNairaFromKobo(ngnKobo),
		// Absent from fiatVAPayTo on purpose: a VA is already in the payer's
		// own name, so the warning would be telling them to fix what is not
		// broken.
		"payerInstruction": a.PayerInstruction(payerLegalName),
	}
}

// wholeNairaFromKobo renders kobo as the naira figure a payer types into their
// bank app. Unparseable input yields "" rather than a guess — a wrong number
// here is money sent to an amount nothing will match.
//
// Rounds half away from zero because that is what the order was minted with
// (wholeNairaKobo) and what the matcher rounds a credit's gross with
// (roundKoboToNaira). Truncating instead showed a pre-cutover order of ₦1,000.50
// as ₦1,000 while the claim offered for it asked ₦1,001: the payer sends what
// the screen told them, the two figures disagree by a naira, and the credit
// holds for an operator every single time with the money already gone.
func wholeNairaFromKobo(ngnKobo string) string {
	n, ok := new(big.Int).SetString(strings.TrimSpace(ngnKobo), 10)
	if !ok {
		return ""
	}
	hundred := big.NewInt(100)
	q, r := new(big.Int).QuoRem(n, hundred, new(big.Int))
	if r.CmpAbs(big.NewInt(50)) >= 0 {
		if n.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q.String()
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
	// A pre-cutover user still has a virtual-account row, and after the cutover
	// nothing reads its statement — so returning it is a pay-into card for an
	// account that swallows transfers. Gone rather than 404: this is not "you
	// have not activated yet", which is what would send them to an activation
	// that now refuses. The account to pay into comes with the order.
	if _, ok := h.it.ActiveCentralAccount(); ok {
		utils.ErrorResponse(c, http.StatusGone, services.ErrITVARetired.Error())
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
		case errors.Is(err, services.ErrITVARetired):
			// Gone, not 5xx: the cutover removed the feature, so a retry can
			// only fail again. The default branch below would answer "please try
			// again", which is how a user ends up hammering a dead path.
			utils.ErrorResponse(c, http.StatusGone, err.Error())
		case errors.Is(err, services.ErrITVANotConfigured):
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "Instant Transfer account activation unavailable")
		case errors.Is(err, services.ErrITVAWalletRequired):
			utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		case errors.Is(err, services.ErrKYCVerificationRequired):
			utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), kycRefusalCode(err))
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

// buyPayTo is the pay-into card for a buy order: the company account and the
// exact figure in central mode, the user's own virtual account otherwise. One
// place for the choice, so the card on Buy and the card on the order detail
// cannot disagree about where the money goes.
//
// va may be nil in VA mode when the caller has none; the card is then nil
// rather than an account the user cannot pay into.
func (h *ITHandler) buyPayTo(ctx context.Context, user *models.User, ngnKobo string,
	central services.CentralAccount, centralMode bool, va *models.FiatVirtualAccount) gin.H {
	if centralMode {
		// Read from the row, not the context user: the matcher compares against
		// the stored column, so the name shown must be that same value.
		return centralPayTo(central, ngnKobo, h.it.VerifiedLegalName(ctx, user.ID))
	}
	if va == nil {
		return nil
	}
	return fiatVAPayTo(va, user.Name)
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
	// Ahead of the VA branch below, which answers a user with no virtual
	// account by telling them to activate one. An unverified user must be sent
	// to verification instead of onto a rail that is being retired.
	if !h.requireVerifiedForMoneyIn(c, user.ID) {
		return
	}

	// Central mode has no per-user account to find, so the VA lookup — and the
	// activation refusal hanging off it — must not run at all. Left in place it
	// locks every user without a legacy virtual account out of buying MCGP
	// entirely, for an activation that ActivateFiatVA now refuses anyway.
	central, centralMode := h.it.ActiveCentralAccount()

	var va *models.FiatVirtualAccount
	if !centralMode {
		var err error
		va, err = h.it.GetFiatVA(c.Request.Context(), user.ID, "NGN")
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
	}

	intent, err := h.it.OpenBuyOrder(c.Request.Context(), user.ID, user.WalletAddress, req.NgnKobo)
	if err != nil {
		// The pre-check above already refused an unverified user, so reaching
		// here means the status changed underneath the request. Still map it
		// properly: a blanket 400 gives the app no code to route on and leaves
		// the user on a payment screen with nowhere to go, which is the whole
		// failure the machine code exists to prevent.
		if code := kycRefusalCode(err); code != "" {
			utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), code)
			return
		}
		// One open order per user. 409 naming the order, not a 400 dead end:
		// the app routes the user to the order they already have, which may be
		// the one they are paying into right now.
		var open *services.ITBuyOrderOpenError
		if errors.As(err, &open) && open.Intent != nil {
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"code":    itBuyOrderOpenCode,
				"message": itBuyOrderOpenMessage(open.Intent.NgnKobo),
				"data": gin.H{
					"intentId": open.Intent.ID,
					"ngnKobo":  open.Intent.NgnKobo,
				},
			})
			return
		}
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	payTo := h.buyPayTo(c.Request.Context(), user, intent.NgnKobo, central, centralMode, va)
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
		"payTo":  payTo,
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

// POST /api/it/sell — debit spendable premium (credited first, then bought) and open the NGN payout.
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
	// The service detaches the sell from this request's cancellation: once the
	// debit is broadcast, a client hanging up must not stop it half way.
	intent, err := h.it.CreateSellIntent(c.Request.Context(), user.ID, user.WalletAddress, req.McgpAmount, bank)
	if err != nil {
		r := sellFailureResponse(err, intent)
		if r.accepted && intent != nil {
			intent.FailureReason = services.UserFacingITFailure(intent.FailureReason)
			c.JSON(r.status, gin.H{"success": true, "message": r.message, "code": r.code, "data": gin.H{"intent": intent}})
			return
		}
		if r.code != "" {
			utils.ErrorResponseWithCode(c, r.status, r.message, r.code)
			return
		}
		utils.ErrorResponse(c, r.status, r.message)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sell submitted", gin.H{"intent": intent})
}

type sellOutcome struct {
	status   int
	code     string
	message  string
	accepted bool // the sell exists and is still progressing
}

// sellFailureResponse maps a CreateSellIntent error to what the user is told.
// The two sell-outcome errors carry chain/provider detail that stays in logs.
func sellFailureResponse(err error, intent *models.ITIntent) sellOutcome {
	switch {
	case errors.Is(err, services.ErrITSellPayoutRejected):
		message := "We couldn't send your payout, so this sale was cancelled. Your MCGP is being returned to your balance — this can take a few minutes."
		if intent != nil && intent.Status == models.ITStatusReversed {
			message = "We couldn't send your payout, so this sale was cancelled. Your MCGP has been returned to your balance (it can take a few minutes to show)."
		}
		return sellOutcome{
			status:  http.StatusUnprocessableEntity,
			code:    "it_sell_payout_rejected",
			message: message,
		}
	case errors.Is(err, services.ErrITSellDebitPending):
		return sellOutcome{
			status:   http.StatusAccepted,
			code:     "it_sell_processing",
			message:  "Your sale is processing. We're confirming it on-chain and will pay out as soon as it's confirmed.",
			accepted: true,
		}
	default:
		return sellOutcome{status: http.StatusBadRequest, message: err.Error()}
	}
}

// adminSellActionStatus maps an admin sell settle/reverse/retry refusal to an
// HTTP status an operator can act on.
func adminSellActionStatus(err error) int {
	switch {
	case errors.Is(err, services.ErrITSellPayoutConflict):
		return http.StatusConflict
	case errors.Is(err, services.ErrITSellPayoutUnverified),
		// The chain has not shown the debit yet: transient, try again shortly.
		errors.Is(err, services.ErrITSellDebitPending):
		return http.StatusServiceUnavailable
	case errors.Is(err, services.ErrITSellPayoutRejected):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
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
	data := gin.H{"intent": intent}
	if services.IsBuyAwaitingTransfer(intent) {
		// The order detail is where a user comes back to pay, so it carries the
		// same card Buy returned. Only while the order is still waiting for
		// money: on anything else a pay-into card invites a second transfer.
		central, centralMode := h.it.ActiveCentralAccount()
		var va *models.FiatVirtualAccount
		if !centralMode {
			if found, err := h.it.GetFiatVA(c.Request.Context(), user.ID, "NGN"); err == nil {
				va = found
			} else if !errors.Is(err, services.ErrITVANotFound) {
				log.Printf("[it] GetIntent GetFiatVA user=%s: %v", user.ID, err)
			}
		}
		if payTo := h.buyPayTo(c.Request.Context(), user, intent.NgnKobo, central, centralMode, va); payTo != nil {
			data["payTo"] = payTo
		}
	}
	utils.SuccessResponse(c, http.StatusOK, "Intent", data)
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

// POST /api/it/admin/stable/buy/:id/reprice — resume a stablecoin buy parked
// as `buy_awaiting_rate` (money landed but could not be priced). Prices the
// naira that arrived at the CURRENT live rate.
//
// Does not use adminIntentAction: a stablecoin buy is delivered to
// intent.DestAddress, not users.wallet_address, so requiring the latter here
// (as the MCGP admin actions do) would block recovery for a buyer who never
// registered an on-chain MCGP wallet.
func (h *ITHandler) PostRepriceParkedStableBuy(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	intent, err := h.it.RepriceParkedStableBuy(c.Request.Context(), id)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "OK", gin.H{"intent": intent})
}

// POST /api/it/admin/settle/:id — finalize an MCGP sell. With automated
// payouts, refused unless Flutterwave reports the transfer SUCCESSFUL: settling
// a payout that failed takes the user's MCGP.
func (h *ITHandler) AdminSettle(c *gin.Context) {
	h.adminSellAction(c, func(intentID uuid.UUID) (*models.ITIntent, error) {
		return h.it.AdminSettleSell(c.Request.Context(), intentID)
	})
}

// POST /api/it/admin/reverse/:id — reverse an MCGP sell whose payout did not
// happen. Refused unless Flutterwave reports the transfer FAILED, the payout
// was rejected, or the provider has no transfer for it: reversing a payout
// that went out pays the user twice.
func (h *ITHandler) AdminReverse(c *gin.Context) {
	h.adminSellAction(c, func(intentID uuid.UUID) (*models.ITIntent, error) {
		return h.it.AdminReverseSell(c.Request.Context(), intentID)
	})
}

// POST /api/it/admin/sell/:id/retry-payout — re-drive the payout of a debited
// MCGP sell. An existing transfer for the sell's reference is adopted rather
// than sent again.
func (h *ITHandler) AdminRetrySellPayout(c *gin.Context) {
	h.adminSellAction(c, func(intentID uuid.UUID) (*models.ITIntent, error) {
		return h.it.RetrySellPayout(c.Request.Context(), intentID)
	})
}

// POST /api/it/admin/sell/:id/reevaluate — re-read the chain (and the payout
// transfer) for a sell parked in `needs_review` and close it whichever way the
// evidence points. The exit door for a parked sell.
func (h *ITHandler) AdminReevaluateSell(c *gin.Context) {
	h.adminSellAction(c, func(intentID uuid.UUID) (*models.ITIntent, error) {
		return h.it.ReevaluateSell(c.Request.Context(), intentID)
	})
}

// POST /api/it/admin/stable/:id/repay — pay a parked stablecoin sell again on
// a fresh payout reference. Refused unless Flutterwave confirms the previous
// transfer FAILED.
func (h *ITHandler) AdminRepayStableSell(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	out, err := h.it.RepayStableSell(c.Request.Context(), id)
	if err != nil {
		utils.ErrorResponse(c, adminSellActionStatus(err), err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "OK", gin.H{"intent": out})
}

// GET /api/it/admin/sell/stuck?minutes= — MCGP sells still processing,
// debiting or debited past the threshold (default 30m), with the evidence an
// operator needs: debit ref and tx, payout id, attempts, rejection, reason.
func (h *ITHandler) GetStuckMCGPSells(c *gin.Context) {
	minutes := 30
	if raw := c.Query("minutes"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			minutes = v
		}
	}
	rows, err := h.it.ListStuckMCGPSells(c.Request.Context(), time.Duration(minutes)*time.Minute)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Stuck sells", gin.H{"stuck": rows, "thresholdMinutes": minutes})
}

func (h *ITHandler) adminSellAction(c *gin.Context, fn func(uuid.UUID) (*models.ITIntent, error)) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	if _, err := h.it.GetIntent(c.Request.Context(), id); err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Intent not found")
		return
	}
	out, err := fn(id)
	if err != nil {
		utils.ErrorResponse(c, adminSellActionStatus(err), err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "OK", gin.H{"intent": out})
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

// ──────────────────────────── stablecoin sell (brokered) ──────────────────

type stableQuoteRequest struct {
	Asset    string `json:"asset" binding:"required"`
	Amount   string `json:"amount" binding:"required"`
	Currency string `json:"currency" binding:"required"`
}

type stableSellRequest struct {
	stableQuoteRequest
	SourceChain string `json:"sourceChain" binding:"required"`
	Bank        struct {
		Code                string `json:"code"`
		Number              string `json:"number"`
		Name                string `json:"name"`
		DestinationCurrency string `json:"destinationCurrency"`
		RailType            string `json:"railType"`
		MobileNetwork       string `json:"mobileNetwork"`
		MobileCountry       string `json:"mobileCountry"`
		MobileMsisdn        string `json:"mobileMsisdn"`
	} `json:"bank"`
}

type stableDepositRequest struct {
	TxHash string `json:"txHash" binding:"required"`
}

// POST /api/it/stable/quote — prices a brokered stablecoin sell.
func (h *ITHandler) PostStableQuote(c *gin.Context) {
	var req stableQuoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	q, err := h.it.QuoteStableSell(c.Request.Context(), req.Asset, req.Amount, req.Currency)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	// Spread AND fee are both itemised: the spread replaces only the
	// swap/routing bps legs, so the fee line is a separate charge the user is
	// entitled to see — collapsing them would hide the platform fee inside
	// what looks like pure FX margin.
	utils.SuccessResponse(c, http.StatusOK, "Quote", gin.H{
		"asset":       q.Asset,
		"currency":    q.Currency,
		"amount":      fmt.Sprintf("%.6f", services.StableFromBaseUnits(q.AssetAmount)),
		"gross":       q.GrossMinor.String(),
		"afterSpread": q.SpreadMinor.String(),
		"fee":         q.FeeMinor.String(),
		"net":         q.NetMinor.String(),
		"rate":        q.RatePerUnitMinor.String(),
		"expiresAt":   q.ExpiresAt,
	})
}

// POST /api/it/stable/sell — creates the sell and returns where to send the funds.
func (h *ITHandler) PostStableSell(c *gin.Context) {
	var req stableSellRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	// The same trade gate ActivateVA/Buy/Sell use. This ramp routes real user
	// funds to a third party under the CBN VASP pilot, so it is squarely a
	// trade and must not be reachable without the accepted agreement.
	if !h.requireITAgreement(c, user.ID) {
		return
	}
	ctx := c.Request.Context()
	q, err := h.it.QuoteStableSell(ctx, req.Asset, req.Amount, req.Currency)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	bank := &services.ITSellBank{
		Code: req.Bank.Code, Number: req.Bank.Number, Name: req.Bank.Name,
		DestinationCurrency: req.Bank.DestinationCurrency,
		RailType:            req.Bank.RailType,
		MobileNetwork:       req.Bank.MobileNetwork,
		MobileCountry:       req.Bank.MobileCountry,
		MobileMsisdn:        req.Bank.MobileMsisdn,
	}
	intent, deposit, err := h.it.CreateStableSellIntent(ctx, user.ID, q, req.SourceChain, bank)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sell created", gin.H{"intent": intent, "deposit": deposit})
}

// POST /api/it/stable/sell/:id/deposit — records the transaction the user broadcast.
func (h *ITHandler) PostStableDeposit(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid intent id")
		return
	}
	var req stableDepositRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.it.ReportStableDeposit(c.Request.Context(), user.ID, id, req.TxHash); err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Deposit recorded", gin.H{"recorded": true})
}

// POST /api/it/buy/:id/cancel — the user's own exit from a buy still awaiting
// payment, MCGP or stablecoin. Answers with the cancelled intent.
func (h *ITHandler) PostCancelBuy(c *gin.Context) {
	h.cancelBuy(c, func(ctx context.Context, userID, id uuid.UUID) (*models.ITIntent, error) {
		return h.it.CancelBuy(ctx, userID, id)
	})
}

// POST /api/it/stable/buy/:id/cancel — the stablecoin-only form, kept for app
// builds that predate /it/buy/:id/cancel. Same service path, same refusals;
// it still refuses an MCGP order.
func (h *ITHandler) PostCancelStableBuy(c *gin.Context) {
	h.cancelBuy(c, func(ctx context.Context, userID, id uuid.UUID) (*models.ITIntent, error) {
		return nil, h.it.CancelStableBuy(ctx, userID, id)
	})
}

// itBuyPaymentArrivedMessage is shown to the user verbatim.
const itBuyPaymentArrivedMessage = "A payment for this order has already arrived, so it can't be cancelled. " +
	"It's being processed — if it doesn't complete, contact support."

// itBuyOrderOpenMessage is the whole of what an app build older than the
// open-order screen shows for this refusal: that build has no route to the
// order and no MCGP cancel, only an error screen with this text. So it names the
// order by its amount and says how to get back to it — asking for that same
// amount returns the open order and its pay-into details (reopenOrRefuse).
func itBuyOrderOpenMessage(openNgnKobo string) string {
	naira := wholeNairaFromKobo(openNgnKobo)
	if naira == "" {
		return "You already have an open buy order waiting for your transfer. Pay into it, or cancel it from its order details to start a new one."
	}
	return fmt.Sprintf("You already have an open buy order for ₦%s waiting for your transfer. "+
		"To see where to pay, go back and continue with ₦%s. "+
		"To buy a different amount, cancel that order from its order details first (update the app if you don't see Cancel).",
		naira, naira)
}

const (
	itBuyOrderOpenCode      = "it_buy_order_open"
	itBuyPaymentArrivedCode = "it_buy_payment_arrived"
)

func (h *ITHandler) cancelBuy(c *gin.Context, cancel func(ctx context.Context, userID, id uuid.UUID) (*models.ITIntent, error)) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid intent id")
		return
	}
	intent, err := cancel(c.Request.Context(), user.ID, id)
	if err != nil {
		if errors.Is(err, services.ErrITBuyPaymentArrived) {
			utils.ErrorResponseWithCode(c, http.StatusConflict, itBuyPaymentArrivedMessage, itBuyPaymentArrivedCode)
			return
		}
		if stableStatusFor(err) == http.StatusInternalServerError {
			// Logged, not returned: the text is a database error.
			log.Printf("[it] cancel buy user=%s intent=%s: %v", user.ID, id, err)
			utils.ErrorResponseWithCode(c, http.StatusInternalServerError,
				"Could not cancel the order right now. Try again.", stableCodeFor(err))
			return
		}
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	data := gin.H{"cancelled": true}
	if intent != nil {
		intent.FailureReason = services.UserFacingITFailure(intent.FailureReason)
		data["intent"] = intent
	}
	utils.SuccessResponse(c, http.StatusOK, "Cancelled", data)
}

// GetStuckStableSells lists brokered sells that have not advanced.
//
// The platform holds no float, so a stall never costs it money — but it does
// leave a user's funds parked at a hop they cannot see. The transaction
// hashes on each row are the operator's leverage against a provider that has
// not credited the funds.
func (h *ITHandler) GetStuckStableSells(c *gin.Context) {
	minutes := 30
	if raw := c.Query("minutes"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			minutes = v
		}
	}
	rows, err := h.it.ListStuckStableSells(c.Request.Context(), time.Duration(minutes)*time.Minute)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Stuck sells", gin.H{"stuck": rows, "thresholdMinutes": minutes})
}

// GetStuckStableBuys lists brokered buys that have not advanced.
//
// Every row here is user money we are holding mid-flight — naira not yet
// converted, or a stablecoin transfer not yet settled. buy_awaiting_rate rows
// are the most urgent: the watcher deliberately never auto-advances that
// park, so this list is the only way an operator learns one exists.
func (h *ITHandler) GetStuckStableBuys(c *gin.Context) {
	minutes := 30
	if raw := c.Query("minutes"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			minutes = v
		}
	}
	rows, err := h.it.ListStuckStableBuys(c.Request.Context(), time.Duration(minutes)*time.Minute)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Stuck buys", gin.H{"stuck": rows, "thresholdMinutes": minutes})
}

// The machine codes every money-in refusal carries, on every gated path. The
// app routes kycRequiredCode to the verification flow and shows
// kycLegalNameMissingCode as a contact-support message, so a second spelling
// of either would silently lose an entry point on the client.
const (
	kycRequiredCode         = "kyc_verification_required"
	kycLegalNameMissingCode = "kyc_legal_name_missing"
)

// kycRefusalCode names the money-in refusal err carries, or "" when it is not
// one. The name-missing case is checked first because it also matches the
// general sentinel.
func kycRefusalCode(err error) string {
	switch {
	case errors.Is(err, services.ErrKYCLegalNameMissing):
		return kycLegalNameMissingCode
	case errors.Is(err, services.ErrKYCVerificationRequired):
		return kycRequiredCode
	default:
		return ""
	}
}

// stableStatusFor maps ramp errors to HTTP codes. Anything the caller can fix
// is a 400; a missing provider or unconfigured deposit address is a 503,
// because the request was fine and the platform was not.
func stableStatusFor(err error) int {
	switch {
	case errors.Is(err, services.ErrStableNotAnAsset),
		errors.Is(err, services.ErrStablePrecision),
		errors.Is(err, services.ErrStableBelowMinimum),
		errors.Is(err, services.ErrStableQuoteExpired),
		errors.Is(err, services.ErrStableNoDestination),
		errors.Is(err, services.ErrStableFeeExceedsProceeds),
		errors.Is(err, services.ErrStableRateImplausible),
		errors.Is(err, services.ErrBadDeliveryAddress),
		errors.Is(err, services.ErrUnsupportedStableChain),
		// Both mapped to 400 (not 404/409): a 404 here would tell a probing
		// caller "this id doesn't exist" vs "it exists but isn't yours" by
		// process of elimination against ErrStableDepositAlreadyClaimed's
		// code, and a 409 on the claimed case would confirm the hash is
		// live/valid to someone who doesn't own it. Same status, same shape
		// as every other client-fixable input error.
		errors.Is(err, services.ErrStableIntentNotFound),
		errors.Is(err, services.ErrStableDepositAlreadyClaimed),
		// Shared bad-state guard for RepriceParkedStableBuy (not currently
		// parked) and CancelStableBuy (not pending_payment). Client-fixable in
		// the sense that acting on the wrong id/state is the caller's mistake,
		// not a server fault — a DB error below still falls through to 500.
		errors.Is(err, services.ErrITBadState),
		errors.Is(err, services.ErrStableBuyBelowMinimum),
		errors.Is(err, services.ErrStableBuyAlreadyOpen),
		errors.Is(err, services.ErrStableBuyNoVA),
		errors.Is(err, services.ErrStableBuyNoWallet),
		// The caller asked for a rail this currency does not use. 400, not 503:
		// the ramp is up, this client is simply out of date, and a 5xx would
		// have it retry a request that can never succeed.
		errors.Is(err, services.ErrStableBuyWrongRamp),
		errors.Is(err, services.ErrUnsupportedChainAsset):
		return http.StatusBadRequest
	case errors.Is(err, services.ErrStableQuoteUnavailable),
		errors.Is(err, services.ErrStableNoDepositAddress),
		errors.Is(err, services.ErrStableNoTokenAddress),
		// The stablecoin is safely in our Flutterwave balance, not lost — but
		// it needs an operator, not a retry from the caller. Ours, not theirs.
		errors.Is(err, services.ErrStableDeliveryExhausted):
		return http.StatusServiceUnavailable
	case errors.Is(err, services.ErrKYCVerificationRequired):
		// 403, alongside the agreement gate: the caller is authenticated and
		// the request is well-formed, but they are not permitted to transact
		// yet. A 400 would read as "fix your input" on a screen where there is
		// no input to fix.
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// stableCodeFor maps a ramp error to a stable, snake_case machine code so the
// app can branch on WHY a request failed instead of matching message strings.
// ErrStableDepositAlreadyClaimed is the one that matters most: it means the
// user's deposit was claimed by someone else's intent, and the app must show
// that prominently rather than as a generic error. Returns "" for anything
// unmapped — callers should treat that as "no specific code, use the message".
func stableCodeFor(err error) string {
	switch {
	case errors.Is(err, services.ErrStableDepositAlreadyClaimed):
		return "stable_deposit_already_claimed"
	case errors.Is(err, services.ErrStableIntentNotFound):
		return "stable_intent_not_found"
	case errors.Is(err, services.ErrStableQuoteExpired):
		return "stable_quote_expired"
	case errors.Is(err, services.ErrStableRateImplausible):
		return "stable_rate_implausible"
	case errors.Is(err, services.ErrStableBelowMinimum):
		return "stable_below_minimum"
	case errors.Is(err, services.ErrStableNotAnAsset):
		return "stable_not_an_asset"
	case errors.Is(err, services.ErrStableNoDepositAddress),
		errors.Is(err, services.ErrStableNoTokenAddress),
		// Also raised by the whole buy path when HasStableBuy is not
		// satisfied, so the app can say "not available right now" rather than
		// showing a bare message on a money screen.
		errors.Is(err, services.ErrStableQuoteUnavailable):
		return "stable_not_configured"
	case errors.Is(err, services.ErrStableFeeExceedsProceeds):
		return "stable_fee_exceeds_proceeds"
	case errors.Is(err, services.ErrITBadState):
		// "this action does not apply to the intent's current state", one
		// machine code for every ramp endpoint that can raise it. NOT
		// buy-specific: it_stable_sell.go raises ErrITBadState at seven sites,
		// so a bad-state SELL deposit would otherwise report a BUY code.
		return "stable_bad_state"
	case errors.Is(err, services.ErrStableBuyBelowMinimum):
		return "stable_buy_below_minimum"
	case errors.Is(err, services.ErrStableBuyAlreadyOpen):
		return "stable_buy_already_open"
	case errors.Is(err, services.ErrStableBuyNoVA):
		return "stable_buy_no_va"
	case errors.Is(err, services.ErrStableBuyNoWallet):
		return "stable_buy_no_wallet"
	case errors.Is(err, services.ErrStableBuyWrongRamp):
		// The client asked for a rail this currency does not use — an app build
		// older than the Paycrest cutover. Distinct from stable_not_configured
		// so the app can send the user to the right screen rather than tell
		// them buying is unavailable.
		return "stable_buy_wrong_ramp"
	case errors.Is(err, services.ErrUnsupportedChainAsset):
		return "stable_unsupported_chain_asset"
	case errors.Is(err, services.ErrStableDeliveryExhausted):
		return "stable_delivery_exhausted"
	case errors.Is(err, services.ErrKYCVerificationRequired):
		return kycRefusalCode(err)
	default:
		return ""
	}
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

// requireVerifiedForMoneyIn writes the 403 + machine code and returns false
// when the caller may not pay in yet (handler must stop). Money-out endpoints
// must not call it — see the note on services.ErrKYCVerificationRequired.
func (h *ITHandler) requireVerifiedForMoneyIn(c *gin.Context, userID uuid.UUID) bool {
	err := h.it.RequireVerifiedForMoneyIn(c.Request.Context(), userID)
	if err == nil {
		return true
	}
	if code := kycRefusalCode(err); code != "" {
		utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), code)
		return false
	}
	// Fail closed, loudly. A DB error here must not read as "verified".
	log.Printf("[it] kyc gate user=%s: %v", userID, err)
	utils.ErrorResponse(c, http.StatusInternalServerError, "Could not check your verification status")
	return false
}

type stableBuyQuoteRequest struct {
	Asset    string `json:"asset" binding:"required"`
	Amount   string `json:"amount" binding:"required"` // fiat MINOR units
	Currency string `json:"currency" binding:"required"`
}

// stableBuyCurrency normalises the caller-supplied currency and rejects
// anything Phase B1 does not support. NGN is the only one: the money arrives
// through the user's standing NGN virtual account. Without this, a EUR request
// is priced, quoted and only fails much later at the VA lookup, with a message
// about a missing virtual account rather than an unsupported currency.
func stableBuyCurrency(c *gin.Context, raw string) (string, bool) {
	currency := strings.ToUpper(strings.TrimSpace(raw))
	if currency != "NGN" {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest,
			"Stablecoin purchases are NGN-only", "stable_buy_unsupported_currency")
		return "", false
	}
	return currency, true
}

type stableBuyRequest struct {
	stableBuyQuoteRequest
	DestChain string `json:"destChain" binding:"required"`
}

// PostStableBuyQuote — POST /api/it/stable/buy/quote — prices an indicative
// stablecoin purchase.
func (h *ITHandler) PostStableBuyQuote(c *gin.Context) {
	var req stableBuyQuoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	currency, ok := stableBuyCurrency(c, req.Currency)
	if !ok {
		return
	}
	q, err := h.it.QuoteStableBuy(c.Request.Context(), req.Asset, req.Amount, currency)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Quote", gin.H{
		"asset":       q.Asset,
		"currency":    q.Currency,
		"fiatMinor":   q.FiatMinor.String(),
		"grossUnits":  q.GrossAssetUnits.String(),
		"assetUnits":  q.AssetUnits.String(),
		"ratePerUnit": q.RatePerUnitMinor.String(),
		"expiresAt":   q.ExpiresAt,
		// Indicative, not locked: the binding price is struck when the naira
		// lands. Say so in the payload so the app cannot present it as firm.
		"indicative": true,
	})
}

// PostStableBuy — POST /api/it/stable/buy — opens a purchase and returns
// where to pay.
func (h *ITHandler) PostStableBuy(c *gin.Context) {
	var req stableBuyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	currency, ok := stableBuyCurrency(c, req.Currency)
	if !ok {
		return
	}
	if !h.requireITAgreement(c, user.ID) {
		return
	}
	ctx := c.Request.Context()

	q, err := h.it.QuoteStableBuy(ctx, req.Asset, req.Amount, currency)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	// The address is OURS to supply, never the caller's to send: it is read
	// from the user's wallet. There is deliberately no address field on the
	// request body.
	addr, err := h.it.UserDeliveryAddress(ctx, user.ID, req.DestChain)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	intent, payInto, err := h.it.CreateStableBuyIntent(ctx, user.ID, q, req.DestChain, addr)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Purchase created", gin.H{
		"intent": intent, "payInto": payInto,
	})
}

// GetStableBuyChains — GET /api/it/stable/buy/chains?asset=USDC — the chains
// this user can receive `asset` on.
func (h *ITHandler) GetStableBuyChains(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	chains, err := h.it.DeliverableChainsForUser(c.Request.Context(), user.ID, c.Query("asset"))
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Chains", gin.H{"chains": chains})
}
