package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// A TSA Pay merchant's own account, from the app and the web: where they are
// paid, their keys, webhooks and return domains, read and changed by the user
// who owns the merchant. These are the changes a stolen login would make to
// redirect a merchant's money or impersonate them, so each asks for the account
// password again (counted toward the login lockout), a bank account must be in
// the owner's own name, and every change waits for a code emailed to the owner
// (a guarded owner action — see services.ConfirmOwnerAction). Every
// applied change is announced to the owner on every channel.

// WithEvents lets the owner routes tell the owner what changed. Nil leaves the
// change silent, which only a test should ever do.
func (h *PayHandler) WithEvents(bus *events.Bus) *PayHandler {
	h.events = bus
	return h
}

// payOwnerMerchantView is what the owner reads of their own merchant: where
// they are paid, with the account number masked (they recognise it by its last
// four digits; the full number is no use on a screen), whether each API key
// exists (by its public prefix — never a hash or a secret), where webhooks go and
// customers may return to, and the version a change from this screen is made
// against. LiveEnabled is the merchant's own switch, which a super admin sets;
// LiveAllowed is whether live payments actually work right now — that switch
// AND TSA Pay live being on globally — and is what a client picks the mode of
// a new payment link by.
type payOwnerMerchantView struct {
	ID             uuid.UUID `json:"id"`
	BusinessName   string    `json:"businessName"`
	Status         string    `json:"status"`
	LiveEnabled    bool      `json:"liveEnabled"`
	LiveAllowed    bool      `json:"liveAllowed"`
	Version        int       `json:"version"`
	SettlementMode string    `json:"settlementMode"`
	BankCode       string    `json:"bankCode,omitempty"`
	AccountNumber  string    `json:"accountNumber,omitempty"`
	AccountName    string    `json:"accountName,omitempty"`
	ChainID        int64     `json:"chainId,omitempty"`
	TokenAddress   string    `json:"tokenAddress,omitempty"`
	TokenSymbol    string    `json:"tokenSymbol,omitempty"`
	WalletAddress  string    `json:"walletAddress,omitempty"`
	WebsiteDomain  string    `json:"websiteDomain,omitempty"`
	HasLiveKey     bool      `json:"hasLiveKey"`
	HasTestKey     bool      `json:"hasTestKey"`
	LiveKeyPrefix  string    `json:"liveKeyPrefix,omitempty"`
	TestKeyPrefix  string    `json:"testKeyPrefix,omitempty"`
	WebhookURL     string    `json:"webhookUrl"`
	TestWebhookURL string    `json:"testWebhookUrl"`
	ReturnDomains  []string  `json:"returnDomains"`
}

func (h *PayHandler) payOwnerView(m *models.PayMerchant) payOwnerMerchantView {
	v := payOwnerMerchantView{ID: m.ID, BusinessName: m.BusinessName, Status: m.Status, LiveEnabled: m.LiveEnabled,
		LiveAllowed: m.LiveAllowed(h.liveEnabled), Version: m.Version, SettlementMode: m.SettlementMode,
		WebsiteDomain: m.WebsiteDomain, HasLiveKey: m.LiveKeyHash != nil, HasTestKey: m.TestKeyHash != nil,
		WebhookURL: m.WebhookURL, TestWebhookURL: m.TestWebhookURL, ReturnDomains: m.ReturnDomainList()}
	if v.HasLiveKey {
		v.LiveKeyPrefix = m.LiveKeyPrefix
	}
	if v.HasTestKey {
		v.TestKeyPrefix = m.TestKeyPrefix
	}
	if v.ReturnDomains == nil {
		v.ReturnDomains = []string{}
	}
	switch m.SettlementMode {
	case models.PaySettlementFiat:
		v.BankCode, v.AccountNumber, v.AccountName = m.BankCode, payMaskAccount(m.AccountNumber), m.AccountName
	case models.PaySettlementCrypto:
		v.ChainID, v.TokenAddress, v.TokenSymbol, v.WalletAddress = m.ChainID, m.TokenAddress, m.TokenSymbol, m.WalletAddress
	}
	return v
}

// payOwnerSettlementAnswer is a change's outcome: where the confirmation code
// was sent, or — for a change to where the merchant is already paid — the
// merchant as it is.
type payOwnerSettlementAnswer struct {
	Merchant     *payOwnerMerchantView           `json:"merchant,omitempty"`
	Confirmation *services.PayOwnerActionPending `json:"confirmation,omitempty"`
}

// GET /api/pay/merchant — the caller's own merchant.
func (h *PayHandler) OwnerMerchant(c *gin.Context) {
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Merchant", h.payOwnerView(m))
}

// payOwnerMerchant is the signed-in user and the merchant they own. Every owner
// route is scoped to its own merchant: a user without one is answered 404
// no_merchant and reaches nothing else. false means the response has been written.
func (h *PayHandler) payOwnerMerchant(c *gin.Context) (*models.User, *models.PayMerchant, bool) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil, nil, false
	}
	m, err := h.merchants.GetByOwner(c.Request.Context(), user.ID)
	if err != nil {
		payOwnerFail(c, err)
		return nil, nil, false
	}
	return user, m, true
}

type payOwnerSettlementRequest struct {
	services.PaySettlementChange
	Password string `json:"password"`
}

// PUT /api/pay/merchant/settlement — the owner changes where they are paid. A
// bank account or a crypto wallet answers 202 and waits for the emailed code
// (POST /api/pay/merchant/actions/:id/confirm), which applies and announces it.
// Saving the destination it already has answers 200 with the merchant unchanged.
func (h *PayHandler) OwnerUpdateSettlement(c *gin.Context) {
	var req payOwnerSettlementRequest
	user, ok := payOwnerBind(c, &req)
	if !ok || !h.payOwnerPassword(c, user, req.Password) {
		return
	}
	res, err := h.merchants.UpdateSettlementByOwner(c.Request.Context(), user.ID, req.PaySettlementChange)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	if res.Pending != nil {
		payOwnerPending(c, res.Pending)
		return
	}
	view := h.payOwnerView(res.Merchant)
	utils.SuccessResponse(c, http.StatusOK, "Settlement unchanged", payOwnerSettlementAnswer{Merchant: &view})
}

type payOwnerActionAnswer struct {
	Merchant          *payOwnerMerchantView `json:"merchant,omitempty"`
	Mode              string                `json:"mode,omitempty"`
	Key               string                `json:"key,omitempty"`
	SecretAction      string                `json:"secretAction,omitempty"`
	WebhookSecretLive string                `json:"webhookSecretLive,omitempty"`
	WebhookSecretTest string                `json:"webhookSecretTest,omitempty"`
}

// POST /api/pay/merchant/actions/:id/confirm — the emailed code that applies a
// waiting guarded action. The key or secret it produces is returned once, here
// (never cached: the route group sends no-store).
func (h *PayHandler) OwnerConfirmAction(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		payOwnerFail(c, services.ErrPayActionNoPending)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Enter the code we emailed you")
		return
	}
	res, err := h.merchants.ConfirmOwnerAction(c.Request.Context(), user.ID, id, req.Code)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	h.announceOwnerAction(user.ID, res)
	out := payOwnerActionAnswer{Mode: res.Mode, Key: res.Key, SecretAction: res.SecretAction,
		WebhookSecretLive: res.WebhookSecretLive, WebhookSecretTest: res.WebhookSecretTest}
	if res.Merchant != nil {
		v := h.payOwnerView(res.Merchant)
		out.Merchant = &v
	}
	utils.SuccessResponse(c, http.StatusOK, "Change confirmed", out)
}

// announceOwnerAction tells the owner, on every channel, what a confirmed action did.
func (h *PayHandler) announceOwnerAction(owner uuid.UUID, res *services.PayOwnerActionResult) {
	switch res.Kind {
	case models.PayOwnerActionSettlement:
		h.announceSettlement(owner, res.Merchant)
	case models.PayOwnerActionKeys:
		h.announce(owner, events.SecurityPayKeyRolled, fmt.Sprintf("Your %s API key was rolled", res.Mode),
			"The old key has stopped working. If you didn't do this, contact support immediately.")
	case models.PayOwnerActionEndpoints:
		h.announce(owner, events.SecurityPayEndpointsChanged, "Your TSA Pay webhook settings were changed",
			// The host only: a webhook URL can carry a token, and this alert is stored, emailed and pushed.
			fmt.Sprintf("Live webhooks: %s; test webhooks: %s. If you didn't make this change, contact support immediately.",
				nonEmpty(services.PayRedactURL(res.Merchant.WebhookURL), "none"), nonEmpty(services.PayRedactURL(res.Merchant.TestWebhookURL), "none")))
	case models.PayOwnerActionWebhookSecret:
		// "reveal" → "revealed", "roll" → "rolled".
		h.announce(owner, events.SecurityPayWebhookSecret, fmt.Sprintf("Your webhook signing secret was %sed", strings.TrimSuffix(res.SecretAction, "e")),
			"If you didn't do this, roll the secret and contact support immediately.")
	}
}

// announceLive tells the owner a super admin switched their live payments on or off.
func (h *PayHandler) announceLive(m *models.PayMerchant) {
	if m.LiveEnabled {
		h.announce(m.UserID, events.PayLiveEnabled, fmt.Sprintf("Live payments are now on for %s", m.BusinessName),
			"Your live API key now takes real payments.")
		return
	}
	h.announce(m.UserID, events.PayLiveDisabled, fmt.Sprintf("Live payments for %s have been switched off — contact support", m.BusinessName),
		"Your live API key is refused until live payments are switched back on. Test payments still work.")
}

func (h *PayHandler) announce(owner uuid.UUID, kind, title, message string) {
	if h.events == nil {
		return
	}
	h.events.Publish(events.Event{Type: kind, UserID: owner, Title: title, Message: message})
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// payOwnerPassword checks the password the request carries, counted toward the
// login lockout: a session is not enough — a stolen or borrowed device must not
// be able to make a guarded change, nor guess the password here unseen. false
// means the response has been written.
func (h *PayHandler) payOwnerPassword(c *gin.Context, user *models.User, password string) bool {
	switch err := checkPasswordWithLockout(config.DB, h.events, user, password); {
	case errors.Is(err, errAccountLocked):
		utils.ErrorResponseWithCode(c, http.StatusLocked, "Your account is temporarily locked. Try again later.", "account_locked")
		return false
	case err != nil:
		utils.ErrorResponseWithCode(c, http.StatusUnauthorized, "Incorrect password", "password_incorrect")
		return false
	}
	return true
}

// payOwnerPending answers 202 with where the code went.
func payOwnerPending(c *gin.Context, p *services.PayOwnerActionPending) {
	c.JSON(http.StatusAccepted, gin.H{"success": true, "message": "We've emailed you a code to confirm this change",
		"data": payOwnerSettlementAnswer{Confirmation: p}})
}

// payOwnerMode reads ?mode=live|test (default live).
func payOwnerMode(c *gin.Context) (string, bool) {
	mode := strings.TrimSpace(c.DefaultQuery("mode", models.PayModeLive))
	if mode != models.PayModeLive && mode != models.PayModeTest {
		utils.ErrorResponse(c, http.StatusBadRequest, "mode must be live or test")
		return "", false
	}
	return mode, true
}

// GET /api/pay/merchant/payments?mode&status&source&q&page&limit — the owner's
// payments in one mode, newest first: all of them, or only those their server
// made (source=api), they sent as one-time links (source=link), or customers
// started through their open links (source=open). status=cancelled is
// the ones they called off; status=expired only those that ran out of time.
func (h *PayHandler) OwnerPayments(c *gin.Context) {
	source := strings.TrimSpace(c.Query("source"))
	switch source {
	case "", models.PaySourceAPI, models.PaySourceLink, models.PaySourceOpen:
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "source must be api, link or open")
		return
	}
	h.ownerPaymentList(c, source)
}

// ownerPaymentList is the body of every owner payments listing: the owner's own
// merchant in the mode asked, narrowed to source ("" is every source), one page
// of at most 50.
func (h *PayHandler) ownerPaymentList(c *gin.Context, source string) {
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return
	}
	mode, ok := payOwnerMode(c)
	if !ok {
		return
	}
	page, limit := payOwnerPage(c)
	rows, total, err := h.payments.List(c.Request.Context(), services.PayListFilter{MerchantID: &m.ID, Mode: mode,
		Status: strings.TrimSpace(c.Query("status")), ForOwner: true, Source: source, Query: c.Query("q"), Page: page, Limit: limit})
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	items, err := h.payments.Views(c.Request.Context(), rows)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payments", gin.H{"items": items, "total": total, "page": page, "limit": limit})
}

// payOwnerPage reads ?page&limit for an owner listing: page from 1, limit 20 by
// default and at most 50.
func payOwnerPage(c *gin.Context) (page, limit int) {
	page, _ = strconv.Atoi(c.Query("page"))
	limit, _ = strconv.Atoi(c.Query("limit"))
	if limit <= 0 {
		limit = 20
	}
	return max(page, 1), min(limit, 50)
}

// payOwnerPayment is the owner's merchant, the mode asked, and the payment :id
// names in it. Another merchant's payment, or one in the other mode, is not
// found. false means the response has been written.
func (h *PayHandler) payOwnerPayment(c *gin.Context) (*models.PayMerchant, string, *models.PayPayment, bool) {
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return nil, "", nil, false
	}
	mode, ok := payOwnerMode(c)
	if !ok {
		return nil, "", nil, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Payment not found")
		return nil, "", nil, false
	}
	p, err := h.payments.GetForMerchant(c.Request.Context(), m.ID, mode, id)
	if err != nil {
		if errors.Is(err, services.ErrPayPaymentNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "Payment not found")
			return nil, "", nil, false
		}
		payOwnerFail(c, err)
		return nil, "", nil, false
	}
	return m, mode, p, true
}

// GET /api/pay/merchant/payments/:id?mode — one payment and its webhook attempts.
func (h *PayHandler) OwnerPayment(c *gin.Context) {
	_, _, p, ok := h.payOwnerPayment(c)
	if !ok {
		return
	}
	deliveries, err := h.payments.Deliveries(c.Request.Context(), p.ID)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	payer, err := h.payments.Payer(c.Request.Context(), p)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	views := make([]payOwnerDeliveryView, len(deliveries))
	for i, d := range deliveries {
		views[i] = payOwnerDeliveryView{ID: d.ID, Event: d.Event, Attempts: d.Attempts, LastStatusCode: d.LastStatusCode,
			LastError: d.LastError, NextAttemptAt: d.NextAttemptAt, DeliveredAt: d.DeliveredAt, FailedAt: d.FailedAt, CreatedAt: d.CreatedAt}
	}
	payment, err := h.payments.Views(c.Request.Context(), []models.PayPayment{*p})
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment", gin.H{"payment": payment[0], "payer": payer, "deliveries": views})
}

// payOwnerDeliveryView is one webhook attempt as the owner sees it: how it went,
// never the payload (JSON names as on models.PayWebhookDelivery).
type payOwnerDeliveryView struct {
	ID             uuid.UUID  `json:"id"`
	Event          string     `json:"event"`
	Attempts       int        `json:"attempts"`
	NextAttemptAt  *time.Time `json:"nextAttemptAt,omitempty"`
	LastStatusCode int        `json:"lastStatusCode"`
	LastError      string     `json:"lastError,omitempty"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
	FailedAt       *time.Time `json:"failedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// payOwnerBind is the signed-in owner with the request body read into req.
// false means the response has been written.
func payOwnerBind(c *gin.Context, req any) (*models.User, bool) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil, false
	}
	if err := c.ShouldBindJSON(req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return nil, false
	}
	return user, true
}

// POST /api/pay/merchant/keys {mode, password} — roll the live or test API key.
func (h *PayHandler) OwnerRequestKeys(c *gin.Context) {
	var req struct {
		Mode     string `json:"mode"`
		Password string `json:"password"`
	}
	user, ok := payOwnerBind(c, &req)
	if !ok || !h.payOwnerPassword(c, user, req.Password) {
		return
	}
	p, err := h.merchants.RequestKeyRoll(c.Request.Context(), user.ID, strings.TrimSpace(req.Mode))
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	payOwnerPending(c, p)
}

// PUT /api/pay/merchant/endpoints {webhookUrl, testWebhookUrl, returnDomains,
// expectedVersion, password} — where webhooks go and customers may return to.
func (h *PayHandler) OwnerRequestEndpoints(c *gin.Context) {
	var req struct {
		services.PayEndpointsChange
		Password string `json:"password"`
	}
	user, ok := payOwnerBind(c, &req)
	if !ok || !h.payOwnerPassword(c, user, req.Password) {
		return
	}
	p, err := h.merchants.RequestEndpointsChange(c.Request.Context(), user.ID, req.PayEndpointsChange)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	payOwnerPending(c, p)
}

// POST /api/pay/merchant/webhook-secret {action: reveal|roll, password}
func (h *PayHandler) OwnerRequestWebhookSecret(c *gin.Context) {
	var req struct {
		Action   string `json:"action"`
		Password string `json:"password"`
	}
	user, ok := payOwnerBind(c, &req)
	if !ok || !h.payOwnerPassword(c, user, req.Password) {
		return
	}
	p, err := h.merchants.RequestWebhookSecret(c.Request.Context(), user.ID, strings.TrimSpace(req.Action))
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	payOwnerPending(c, p)
}

// GET /api/pay/merchant/banks — the banks an owner can be paid to. The p2p bank
// routes are KYC-gated for traders; an owner needs the same directory without
// that gate.
func (h *PayHandler) OwnerBanks(c *gin.Context) {
	if _, _, ok := h.payOwnerMerchant(c); !ok {
		return
	}
	banks, err := h.merchants.Banks(c.Request.Context())
	if err != nil {
		payOwnerBankFail(c, err)
		return
	}
	if banks == nil {
		banks = []services.NombaBank{}
	}
	utils.SuccessResponse(c, http.StatusOK, "Banks", banks)
}

// GET /api/pay/merchant/banks/resolve?bankCode&accountNumber — the name the bank
// holds for an account, so the owner sees whose it is before saving it.
func (h *PayHandler) OwnerResolveBank(c *gin.Context) {
	if _, _, ok := h.payOwnerMerchant(c); !ok {
		return
	}
	bankCode, accountNumber, ok := validBankLookup(c.Query("bankCode"), c.Query("accountNumber"))
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, bankLookupInvalidMessage)
		return
	}
	res, err := h.merchants.ResolveBank(c.Request.Context(), bankCode, accountNumber)
	if err != nil {
		payOwnerBankFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Account", res)
}

// payOwnerBankFail maps a bank directory refusal. The provider's own words are
// logged, never shown.
func payOwnerBankFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPayBanksUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "Bank lookups aren't available right now — try again later", "banks_unavailable")
	case errors.Is(err, services.ErrNombaAccountNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Account could not be resolved at the selected bank")
	default:
		log.Printf("[tsa-pay] owner bank lookup failed: %s: %v", c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusBadGateway, "We couldn't reach the bank directory — try again")
	}
}

// GET /api/pay/merchant/settlement-options — the stablecoins, per chain, an
// owner may be paid in: exactly what a crypto payout change will accept.
func (h *PayHandler) OwnerSettlementOptions(c *gin.Context) {
	if _, _, ok := h.payOwnerMerchant(c); !ok {
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Settlement options", h.merchants.SettlementOptions())
}

// announceSettlement tells the owner, in the app and by email and push, that
// their payout destination changed — so a change they did not make is seen.
func (h *PayHandler) announceSettlement(owner uuid.UUID, m *models.PayMerchant) {
	if h.events == nil {
		return
	}
	h.events.Publish(events.Event{
		Type:   events.SecurityPaySettlementChanged,
		UserID: owner,
		Title:  "Your TSA Pay payout details were changed",
		Message: fmt.Sprintf("Payments to %s will now be paid to %s. If you didn't make this change, contact support immediately.",
			m.BusinessName, paySettlementDestination(m)),
		Data: map[string]interface{}{"merchantId": m.ID.String()},
	})
}

// paySettlementDestination names where the merchant is now paid, in words the
// owner recognises, without printing a full account number — and a wallet by
// its first six and last four characters, so a lookalike that only shares the
// ending is visible.
func paySettlementDestination(m *models.PayMerchant) string {
	if m.SettlementMode == models.PaySettlementFiat {
		return fmt.Sprintf("the bank account %s (%s)", payMaskAccount(m.AccountNumber), m.AccountName)
	}
	return fmt.Sprintf("%s to the wallet %s", m.TokenSymbol, services.PayShortAddress(m.WalletAddress))
}

// payOwnerFail maps an owner-route refusal to its status and a machine code.
// Every message is written for the owner; a bank provider's own words and any
// unrecognised error are logged, never echoed.
func payOwnerFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPayMerchantNotFound):
		utils.ErrorResponseWithCode(c, http.StatusNotFound, "You don't have a TSA Pay merchant account", "no_merchant")
	case errors.Is(err, services.ErrPayMerchantAmbiguous):
		log.Printf("[tsa-pay] owner request refused: %v", err)
		utils.ErrorResponseWithCode(c, http.StatusConflict, "We couldn't tell which merchant account to change — contact support", "merchant_ambiguous")
	case errors.Is(err, services.ErrPayMerchantSuspended):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, "Your merchant account is suspended — contact support", "merchant_suspended")
	case errors.Is(err, services.ErrPaySettlementNameMismatch):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "That account isn't in your business name or your verified name. Use an account in your own name.", "name_mismatch")
	case errors.Is(err, services.ErrPayAccountUnverifiable):
		log.Printf("[tsa-pay] owner account lookup failed: %v", err)
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "We couldn't verify that account. Check the bank and account number and try again.", "account_unverifiable")
	case errors.Is(err, services.ErrPayMerchantInvalid):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrPayMerchantStale):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Your payment settings changed since you opened this screen — reload and try again", "merchant_stale")
	case errors.Is(err, services.ErrPayCodeUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "We couldn't send you a confirmation code right now — try again later or contact support", "confirmation_unavailable")
	case errors.Is(err, services.ErrPayActionNoPending):
		utils.ErrorResponseWithCode(c, http.StatusGone, "This change has expired or had too many wrong codes — start again", "no_pending_change")
	case errors.Is(err, services.ErrPayActionBusy):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Another change is being requested — try again", "action_busy")
	case errors.Is(err, services.ErrPayCodeInvalid):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "That code isn't right", "code_invalid")
	default:
		log.Printf("[tsa-pay] owner request failed: %s %s: %v", c.Request.Method, c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
	}
}
