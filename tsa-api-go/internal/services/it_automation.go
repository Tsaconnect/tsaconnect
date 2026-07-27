package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// IT automation — the Flutterwave-era NGN legs. The legacy flow is fully
// operator-manual (admin approves buy credits after eyeballing the bank
// statement, pays sell payouts by hand); these seams automate both while
// keeping the manual paths as fallback:
//
//	buy:  dynamic VA per intent (reference = PaymentRef) → charge.completed
//	      webhook → AutoCreditFromInbound → ApproveBuyCredit (idempotent)
//	sell: CreateSellIntent debits on-chain → AutoPayout fires the gateway →
//	      transfer.disburse webhook → ResolvePayoutFromWebhook → Settle/Reverse
//
// Every entry point is idempotent or claim-guarded because webhooks redeliver.

// ITPayoutRefPrefix namespaces IT sell payout references so webhook dispatch
// can route them away from PM-trade payouts (whose refs are bare trade UUIDs).
const ITPayoutRefPrefix = "IT-SELL-"

// itPayoutRef builds the provider transfer reference for a sell payout. The
// UUID's hyphens are stripped so the whole reference is 40 chars ("IT-SELL-" +
// 32 hex): Flutterwave rejects references longer than 42 ("size must be between
// 6 and 42"), and the canonical 36-char UUID plus the prefix is 44. uuid.Parse
// accepts the hyphen-less form, so ResolvePayoutFromWebhook round-trips it —
// keep these two in lockstep.
func itPayoutRef(intentID uuid.UUID) string {
	return ITPayoutRefPrefix + strings.ReplaceAll(intentID.String(), "-", "")
}

// ITSellBank is the payout destination captured at sell creation. Bank fields
// (Code/Number/Name) are the legacy NGN-bank rail; the rest support
// cross-currency / mobile-money withdrawal (mirrors PayoutRequest). Empty
// DestinationCurrency/RailType ⇒ legacy NGN bank behavior, unchanged.
type ITSellBank struct {
	Code   string
	Number string
	Name   string

	DestinationCurrency string // ISO, e.g. "KES"; "" ⇒ "NGN"
	RailType            string // "bank" | "mobile_money"; "" ⇒ "bank"
	MobileNetwork       string // mobile_money only, e.g. "wave"
	MobileCountry       string // mobile_money only, ISO-2, e.g. "SN"
	MobileMsisdn        string // mobile_money only
}

// validateSellBank fails fast (before any chain call) on a malformed sell
// destination. The legacy bank rail's field requirement is enforced by the
// HTTP handler (bank is optional here for the manual-payout fallback); only
// the mobile_money rail's required fields are checked at this layer.
func validateSellBank(bank *ITSellBank) error {
	if bank == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(bank.RailType)) {
	case "", "bank":
		return nil
	case "mobile_money":
		if strings.TrimSpace(bank.MobileNetwork) == "" || strings.TrimSpace(bank.MobileCountry) == "" || strings.TrimSpace(bank.MobileMsisdn) == "" {
			return errors.New("it: mobile money sell requires network, country and phone number")
		}
		return nil
	default:
		return fmt.Errorf("it: unknown payout rail %q", bank.RailType)
	}
}

// applySellBank copies the payout destination from the sell request onto the
// intent. Extracted from CreateSellIntent so the field mapping is unit
// testable independent of the on-chain debit path (which needs a live
// blockchain client).
func applySellBank(intent *models.ITIntent, bank *ITSellBank) {
	if bank == nil {
		return
	}
	intent.BankCode = strings.TrimSpace(bank.Code)
	intent.AccountNumber = strings.TrimSpace(bank.Number)
	intent.AccountName = strings.TrimSpace(bank.Name)
	intent.DestinationCurrency = strings.ToUpper(strings.TrimSpace(bank.DestinationCurrency))
	intent.RailType = strings.ToLower(strings.TrimSpace(bank.RailType))
	intent.MobileNetwork = strings.TrimSpace(bank.MobileNetwork)
	intent.MobileCountry = strings.ToUpper(strings.TrimSpace(bank.MobileCountry))
	intent.MobileMsisdn = strings.TrimSpace(bank.MobileMsisdn)
}

// sellIntentHasPayoutDestination reports whether the intent carries a payout
// destination for its rail: the legacy bank rail (empty or "bank") needs
// BankCode+AccountNumber; mobile_money needs the mobile triple instead. An
// unrecognized rail has no valid destination.
func sellIntentHasPayoutDestination(intent *models.ITIntent) bool {
	switch strings.ToLower(strings.TrimSpace(intent.RailType)) {
	case "", "bank":
		return intent.BankCode != "" && intent.AccountNumber != ""
	case "mobile_money":
		return intent.MobileNetwork != "" && intent.MobileCountry != "" && intent.MobileMsisdn != ""
	default:
		return false
	}
}

// ITVAProvisioner issues a per-intent dynamic virtual account for a buy.
type ITVAProvisioner interface {
	CreateDynamicVA(ctx context.Context, reference string, amountNaira float64, narration string) (FWVirtualAccount, error)
}

// SetPayoutGateway wires the fiat payout gateway for sell auto-payouts.
func (s *ITService) SetPayoutGateway(g NombaGateway) { s.payout = g }

// SetVAProvisioner wires dynamic virtual-account provisioning for buys.
func (s *ITService) SetVAProvisioner(v ITVAProvisioner) { s.va = v }

// HasAutoPayout reports whether sell payouts fire automatically.
func (s *ITService) HasAutoPayout() bool { return s != nil && s.payout != nil }

// HasVAProvisioning reports whether buys get per-intent virtual accounts.
func (s *ITService) HasVAProvisioning() bool { return s != nil && s.va != nil }

// ─── Buy: dynamic VA + webhook auto-credit ─────────────────────────────────

// ProvisionBuyVA issues an amount-locked dynamic virtual account for a buy
// intent (reference = the intent's PaymentRef, which the charge.completed
// webhook echoes back) and stores the account details on the intent. Safe to
// re-call: an intent that already has a VA returns it unchanged.
func (s *ITService) ProvisionBuyVA(ctx context.Context, intent *models.ITIntent) error {
	if s.va == nil {
		return errors.New("it: no VA provisioner configured")
	}
	if intent.Direction != models.ITDirectionBuy {
		return ErrITBadState
	}
	if intent.VAAccountNumber != "" {
		return nil
	}
	naira, err := koboToNairaFloat(intent.NgnKobo)
	if err != nil {
		return fmt.Errorf("it: bad intent amount: %w", err)
	}
	va, err := s.va.CreateDynamicVA(ctx, intent.PaymentRef, naira, "TSA Connect IT purchase")
	if err != nil {
		return err
	}
	intent.VAAccountNumber = va.AccountNumber
	intent.VABankName = va.BankName
	return s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ?", intent.ID).
		Updates(map[string]any{
			"va_account_number": intent.VAAccountNumber,
			"va_bank_name":      intent.VABankName,
		}).Error
}

// AutoCreditFromInbound credits a buy intent from a verified inbound-payment
// webhook. paymentRef is the strong attribution key (the intent's PaymentRef,
// echoed back as the charge reference); amountNGN is the settled naira amount
// as a decimal string. Underpayment is a hard error (recorded for operator
// review — never credit more MCGP than was paid for); overpayment credits the
// intent as quoted and logs the excess.
func (s *ITService) AutoCreditFromInbound(ctx context.Context, paymentRef, amountNGN string) error {
	if !s.IsConfigured() {
		return ErrITNotConfigured
	}
	ref := strings.TrimSpace(paymentRef)
	if ref == "" {
		return errors.New("it auto-credit: empty payment ref")
	}
	var intent models.ITIntent
	err := s.db.WithContext(ctx).
		Where("payment_ref = ? AND direction = ?", ref, models.ITDirectionBuy).
		First(&intent).Error
	if err != nil {
		return fmt.Errorf("it auto-credit: no buy intent for ref %q: %w", ref, err)
	}
	if intent.Status == models.ITStatusCredited {
		return nil // webhook redelivery — already done
	}

	paidKobo, err := nairaToKobo(amountNGN)
	if err != nil {
		return fmt.Errorf("it auto-credit: bad paid amount %q: %w", amountNGN, err)
	}
	expected, ok := new(big.Int).SetString(intent.NgnKobo, 10)
	if !ok {
		return fmt.Errorf("it auto-credit: corrupt intent amount %q", intent.NgnKobo)
	}
	if paidKobo.Cmp(expected) < 0 {
		return fmt.Errorf("it auto-credit: underpaid ref %q: paid %s kobo, expected %s kobo — operator review required", ref, paidKobo, expected)
	}
	if paidKobo.Cmp(expected) > 0 {
		log.Printf("[it] overpayment on ref %q: paid %s kobo, quoted %s kobo — crediting as quoted", ref, paidKobo, expected)
	}

	// ApproveBuyCredit is claim-guarded + idempotent; the empty wallet arg
	// falls back to the wallet captured at intent creation.
	_, err = s.ApproveBuyCredit(ctx, intent.ID, "")
	return err
}

// ─── Sell: gateway auto-payout + webhook settle/reverse ────────────────────

// AutoPayout fires the fiat leg for a debited sell intent through the payout
// gateway and records the provider's transfer id. Idempotent: an intent that
// already has a PayoutID is not re-sent (the reference-derived idempotency key
// at the provider is the second line of defense).
func (s *ITService) AutoPayout(ctx context.Context, intent *models.ITIntent) error {
	if s.payout == nil {
		return errors.New("it: no payout gateway configured")
	}
	if intent.Direction != models.ITDirectionSell || intent.Status != models.ITStatusDebited {
		return ErrITBadState
	}
	if intent.PayoutID != "" {
		return nil
	}
	if !sellIntentHasPayoutDestination(intent) {
		return errors.New("it: sell intent has no payout destination")
	}
	// Disburse the NET — the fee stays behind in the payout balance as fiat.
	// Pre-deduction intents carry no net and settle at their gross terms.
	net, err := payoutKobo(intent)
	if err != nil {
		return fmt.Errorf("it payout: %w", err)
	}
	naira, err := koboToNairaString(net.String())
	if err != nil {
		return fmt.Errorf("it payout: bad intent amount: %w", err)
	}
	// AmountNGN is always our locked NGN amount — for a cross-currency/mobile
	// rail the gateway converts via DestinationCurrency; for legacy NGN bank
	// (DestinationCurrency empty) it pays it out as-is. See FlutterwaveGateway.
	payoutID, err := s.payout.InitiatePayout(ctx, PayoutRequest{
		BankCode:            intent.BankCode,
		AccountNumber:       intent.AccountNumber,
		AccountName:         intent.AccountName,
		AmountNGN:           naira,
		Reference:           itPayoutRef(intent.ID),
		SenderName:          "TSA Connect",
		DestinationCurrency: intent.DestinationCurrency,
		RailType:            intent.RailType,
		MobileNetwork:       intent.MobileNetwork,
		MobileCountry:       intent.MobileCountry,
		MobileMsisdn:        intent.MobileMsisdn,
	})
	if err != nil {
		_ = s.db.WithContext(ctx).Model(&models.ITIntent{}).
			Where("id = ?", intent.ID).
			Update("failure_reason", "auto-payout: "+err.Error()).Error
		return err
	}
	intent.PayoutID = payoutID
	return s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ?", intent.ID).
		Updates(map[string]any{"payout_id": payoutID, "failure_reason": ""}).Error
}

// ResolvePayoutFromWebhook closes a sell intent from a transfer-status
// webhook. merchantTxRef must carry the ITPayoutRefPrefix. Mapping:
// success → settle (frees the on-chain backing), failed → reverse (restores
// the user's premium), pending → no-op, reversed → operator case.
func (s *ITService) ResolvePayoutFromWebhook(ctx context.Context, merchantTxRef, status string) error {
	if !s.IsConfigured() {
		return ErrITNotConfigured
	}
	idStr := strings.TrimPrefix(strings.TrimSpace(merchantTxRef), ITPayoutRefPrefix)
	intentID, err := uuid.Parse(idStr)
	if err != nil {
		return fmt.Errorf("it payout webhook: bad ref %q: %w", merchantTxRef, err)
	}
	switch strings.ToLower(status) {
	case "success":
		_, err = s.SettleSell(ctx, intentID, "")
		return err
	case "failed":
		_, err = s.ReverseSell(ctx, intentID, "")
		return err
	case "pending":
		return nil
	case "reversed":
		log.Printf("[it] payout REVERSED for intent %s — operator action required (funds returned after success; premium already settled)", intentID)
		return fmt.Errorf("%w: it payout reversal", ErrNonSettlingEvent)
	default:
		return fmt.Errorf("it payout webhook: unknown status %q for intent %s", status, intentID)
	}
}

// ─── Flutterwave VA provisioner ────────────────────────────────────────────

// FlutterwaveVAProvisioner issues dynamic (amount-locked, 1h) virtual accounts
// under a single company customer. Per-payer customers (with each user's
// BVN/NIN) are a follow-up once the app collects them; the company-customer
// model mirrors the Nomba single-parent VA setup.
type FlutterwaveVAProvisioner struct {
	client     *FlutterwaveClient
	customerID string
}

// NewFlutterwaveVAProvisioner returns nil unless both the client and the
// company customer id are configured (dormant-by-default).
func NewFlutterwaveVAProvisioner(client *FlutterwaveClient, customerID string) *FlutterwaveVAProvisioner {
	if !client.IsEnabled() || strings.TrimSpace(customerID) == "" {
		return nil
	}
	return &FlutterwaveVAProvisioner{client: client, customerID: strings.TrimSpace(customerID)}
}

// CreateDynamicVA issues a one-time NGN virtual account locked to the exact
// amount, expiring after an hour (Flutterwave's default window).
func (p *FlutterwaveVAProvisioner) CreateDynamicVA(ctx context.Context, reference string, amountNaira float64, narration string) (FWVirtualAccount, error) {
	return p.client.CreateVirtualAccount(ctx, FWVirtualAccountRequest{
		CustomerID:  p.customerID,
		Reference:   reference,
		Currency:    "NGN",
		Amount:      amountNaira,
		AccountType: "dynamic",
		Expiry:      3600,
		Narration:   narration,
	})
}

// CreateVirtualAccount issues a STATIC (permanent, amount-0) NGN virtual account
// for a corporate Payment Merchant, returning the account number. accountRef is
// the PM's deterministic "pm-<userID>" reference — the SAME attribution key the
// charge.completed webhook carries back, so the existing provider-agnostic
// inbound matcher (resolveInboundPM → WHERE nomba_account_ref = ?) keys on it
// with no change.
//
// idType ("BVN"|"NIN") + idNumber are the account holder's identity, REQUIRED by
// Flutterwave live mode for NGN static accounts. They are supplied by the merchant
// at activation time, forwarded to Flutterwave here, and never stored — do not
// log idNumber.
//
// This signature satisfies services.NombaProvisioner, so *FlutterwaveVAProvisioner
// is a drop-in for the Nomba client in PMOnboardingService — the whole point of
// moving PM onboarding off Nomba.
func (p *FlutterwaveVAProvisioner) CreateVirtualAccount(ctx context.Context, accountRef, accountName, idType, idNumber string) (string, error) {
	req := FWVirtualAccountRequest{
		CustomerID:  p.customerID,
		Reference:   accountRef,
		Currency:    "NGN",
		Amount:      0,
		AccountType: "static",
		Narration:   accountName,
	}
	switch strings.ToUpper(strings.TrimSpace(idType)) {
	case "NIN":
		req.NIN = strings.TrimSpace(idNumber)
	default: // "BVN" (default) — the historical identity type for NGN VAs
		req.BVN = strings.TrimSpace(idNumber)
	}
	va, err := p.client.CreateVirtualAccount(ctx, req)
	if err != nil {
		return "", err
	}
	return va.AccountNumber, nil
}

// ─── Kobo/naira helpers (big.Int — no float drift on money) ────────────────

// nairaToKobo converts a decimal naira string ("500", "500.5", "500.00") to
// kobo. More than 2 decimal places is rejected.
func nairaToKobo(s string) (*big.Int, error) {
	s = strings.TrimSpace(s)
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	switch len(frac) {
	case 0:
		frac = "00"
	case 1:
		frac += "0"
	case 2:
	default:
		return nil, fmt.Errorf("more than 2 decimal places in %q", s)
	}
	n, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok {
		return nil, fmt.Errorf("not a decimal amount: %q", s)
	}
	return n, nil
}

// koboToNairaString renders kobo as a 2dp naira decimal string.
func koboToNairaString(kobo string) (string, error) {
	n, ok := new(big.Int).SetString(strings.TrimSpace(kobo), 10)
	if !ok || n.Sign() < 0 {
		return "", fmt.Errorf("bad kobo amount %q", kobo)
	}
	q, r := new(big.Int).DivMod(n, big.NewInt(100), new(big.Int))
	return fmt.Sprintf("%s.%02d", q.String(), r.Int64()), nil
}

// koboToNairaFloat is for provider APIs that take numeric amounts. Kobo fits
// float64 exactly far beyond any realistic IT amount (2^53 kobo ≈ ₦90T).
func koboToNairaFloat(kobo string) (float64, error) {
	n, ok := new(big.Int).SetString(strings.TrimSpace(kobo), 10)
	if !ok || n.Sign() < 0 {
		return 0, fmt.Errorf("bad kobo amount %q", kobo)
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(n), big.NewFloat(100)).Float64()
	return f, nil
}
