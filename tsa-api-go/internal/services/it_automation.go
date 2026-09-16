package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
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

// itPayoutRefMaxGen is the last reference generation that fits Flutterwave's
// 42-char limit: the base reference is 40 chars, leaving room for "-N".
const itPayoutRefMaxGen = 9

// itPayoutRefFor is the intent's CURRENT payout reference. Generation 0 is the
// original reference (every MCGP sell, and every sell before repay existed);
// a stablecoin repay moves to "<base>-<gen>".
func itPayoutRefFor(intent *models.ITIntent) string {
	if intent.PayoutRefGen <= 0 {
		return itPayoutRef(intent.ID)
	}
	return fmt.Sprintf("%s-%d", itPayoutRef(intent.ID), intent.PayoutRefGen)
}

// parseITPayoutRef reverses itPayoutRefFor.
func parseITPayoutRef(ref string) (uuid.UUID, int, error) {
	body := strings.TrimPrefix(strings.TrimSpace(ref), ITPayoutRefPrefix)
	gen := 0
	if i := strings.LastIndex(body, "-"); i >= 0 && len(body)-i <= 2 {
		n, err := strconv.Atoi(body[i+1:])
		if err != nil || n < 1 || n > itPayoutRefMaxGen {
			return uuid.Nil, 0, fmt.Errorf("bad payout reference generation in %q", ref)
		}
		gen, body = n, body[:i]
	}
	id, err := uuid.Parse(body)
	if err != nil {
		return uuid.Nil, 0, err
	}
	return id, gen, nil
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
// already has a PayoutID is not re-sent, and every send of one reference
// generation reuses the same reference, which is also the provider's
// idempotency key.
//
// The error classifies the outcome, because the money invariant depends on it:
//
//   - ErrITSellPayoutRejected (MCGP only): positive evidence no transfer
//     exists — nothing was ever sent, or the provider refused the FIRST request
//     for the reference. The intent is marked PayoutRejected; the caller
//     reverses.
//   - a refusal of a RETRY proves nothing (the reference may be taken because
//     the first attempt went out). It is recorded as PayoutRefusedOnRetry and
//     the reconciler reverses only once the provider confirms no transfer.
//   - a brokered stablecoin sell's refusals are only recorded: its watcher
//     retries, and only a provider-confirmed FAILED transfer parks it.
//   - any other error: outcome unknown, nothing marked.
//
// An MCGP sell is only paid out while its debit is still Pending on-chain: a
// debit already settled or reversed means the row is stale, and paying it
// would pay a user whose MCGP was returned.
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
	if intent.PayoutRejected {
		// The provider would only replay its refusal for this reference.
		return fmt.Errorf("%w: payout already rejected for intent %s", ErrITSellPayoutRejected, intent.ID)
	}
	if intent.PayoutRefusedOnRetry {
		return fmt.Errorf("%w: a retry of this payout was refused; awaiting provider confirmation", ErrITBadState)
	}
	isMCGP := models.ITIntentAsset(intent) == models.ITAssetMCGP
	firstAttempt := payoutNeverSubmitted(intent)

	payoutReq, err := s.buildSellPayoutRequest(ctx, intent)
	if err != nil {
		return s.payoutNotSent(ctx, intent, firstAttempt, err)
	}
	if isMCGP {
		if err := s.requireDebitPending(ctx, intent); err != nil {
			return err
		}
	}

	// Claim the attempt BEFORE the request leaves: a crash mid-request must
	// still read as "may have been sent". The guard makes concurrent callers
	// (request, reconciler, admin retry) take turns, and loses to a reverse
	// that has already claimed the row (payout_rejected).
	now := time.Now()
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ? AND COALESCE(payout_id, '') = '' AND payout_rejected = ? AND payout_refused_on_retry = ? AND payout_attempts = ? AND payout_ref_gen = ?",
			intent.ID, models.ITStatusDebited, false, false, intent.PayoutAttempts, intent.PayoutRefGen).
		Updates(map[string]any{
			"payout_attempts":        gorm.Expr("payout_attempts + 1"),
			"last_payout_attempt_at": now,
		})
	if res.Error != nil {
		return fmt.Errorf("it payout: record attempt: %w", res.Error)
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: payout for intent %s is being handled elsewhere", ErrITBadState, intent.ID)
	}
	intent.PayoutAttempts++
	intent.LastPayoutAttemptAt = &now

	payoutID, err := s.payout.InitiatePayout(ctx, payoutReq)
	if err != nil {
		var rej *ProviderRejectedError
		switch {
		case errors.As(err, &rej) && isMCGP && firstAttempt:
			s.markPayoutRejected(ctx, intent, itPayoutReasonPrefix+" rejected: "+err.Error())
			return fmt.Errorf("%w: %v", ErrITSellPayoutRejected, err)
		case errors.As(err, &rej) && isMCGP:
			reason := truncateReason(itPayoutReasonPrefix + " refused on retry: " + err.Error())
			intent.PayoutRefusedOnRetry, intent.FailureReason = true, reason
			if uerr := s.db.WithContext(ctx).Model(&models.ITIntent{}).Where("id = ?", intent.ID).
				Updates(map[string]any{"payout_refused_on_retry": true, "failure_reason": reason}).Error; uerr != nil {
				log.Printf("[it] ALERT it_sell_retry_refusal_unrecorded intent=%s: %v", intent.ID, uerr)
			}
			return err
		}
		reason := truncateReason(itPayoutReasonPrefix + " unknown: " + err.Error())
		if errors.As(err, &rej) {
			reason = truncateReason(itPayoutReasonPrefix + " refused: " + err.Error())
		}
		intent.FailureReason = reason
		_ = s.db.WithContext(ctx).Model(&models.ITIntent{}).
			Where("id = ?", intent.ID).
			Update("failure_reason", reason).Error
		return err
	}
	intent.PayoutID = payoutID
	intent.FailureReason = ""
	return s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ?", intent.ID).
		Updates(map[string]any{"payout_id": payoutID, "failure_reason": ""}).Error
}

// requireDebitPending refuses a payout unless the sell's debit is Pending
// on-chain for this wallet and amount. A debit that is already resolved moves
// the row to needs_review: nothing may pay or reverse it automatically.
func (s *ITService) requireDebitPending(ctx context.Context, intent *models.ITIntent) error {
	rec, err := s.chain().DebitRecord(ctx, itRef("IT:debit:", intent.ID))
	if err != nil {
		return fmt.Errorf("it payout: cannot confirm the debit is pending: %w", err)
	}
	if rec.State == DebitStatePending && debitMatchesIntent(rec, intent) {
		return nil
	}
	s.moveToReview(ctx, intent, []string{models.ITStatusDebited},
		fmt.Sprintf("payout blocked: on-chain debit state %d does not match a pending debit for this sell", rec.State))
	return fmt.Errorf("%w: debit for intent %s is not pending on-chain", ErrITBadState, intent.ID)
}

// itPayoutReasonPrefix starts every failure_reason AutoPayout writes. Rows
// from before PayoutAttempts existed carry "auto-payout: …", and that prefix
// is the only trace that they were ever submitted — payoutNeverSubmitted keys
// on it so a legacy row can never be mistaken for an unused reference.
const itPayoutReasonPrefix = "auto-payout"

// payoutNeverSubmitted reports whether the intent's payout reference has
// provably never been sent to the provider.
func payoutNeverSubmitted(intent *models.ITIntent) bool {
	return intent.PayoutAttempts == 0 && intent.PayoutID == "" &&
		!strings.HasPrefix(intent.FailureReason, itPayoutReasonPrefix)
}

// payoutNotSent handles a payout that failed before any request was made. On
// the first attempt that is a definite rejection for an MCGP sell. A brokered
// stablecoin sell is left to its watcher to retry, as before: nothing was sent,
// and the user's fiat is still sitting in the provider balance.
func (s *ITService) payoutNotSent(ctx context.Context, intent *models.ITIntent, firstAttempt bool, cause error) error {
	if firstAttempt && models.ITIntentAsset(intent) == models.ITAssetMCGP {
		s.markPayoutRejected(ctx, intent, itPayoutReasonPrefix+" rejected (not sent): "+cause.Error())
		return fmt.Errorf("%w: %v", ErrITSellPayoutRejected, cause)
	}
	return cause
}

func (s *ITService) markPayoutRejected(ctx context.Context, intent *models.ITIntent, reason string) {
	reason = truncateReason(reason)
	intent.PayoutRejected = true
	intent.FailureReason = reason
	if err := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ?", intent.ID).
		Updates(map[string]any{"payout_rejected": true, "failure_reason": reason}).Error; err != nil {
		log.Printf("[it] ALERT it_sell_payout_rejection_unrecorded intent=%s: %v", intent.ID, err)
	}
}

// buildSellPayoutRequest prices the payout (the NET, converted to the
// destination currency when it differs) without sending anything.
func (s *ITService) buildSellPayoutRequest(ctx context.Context, intent *models.ITIntent) (PayoutRequest, error) {
	if !sellIntentHasPayoutDestination(intent) {
		return PayoutRequest{}, errors.New("it: sell intent has no payout destination")
	}
	// Disburse the NET — the fee stays behind in the payout balance as fiat.
	// Pre-deduction intents carry no net and settle at their gross terms.
	net, err := payoutKobo(intent)
	if err != nil {
		return PayoutRequest{}, fmt.Errorf("it payout: %w", err)
	}
	naira, err := koboToNairaString(net.String())
	if err != nil {
		return PayoutRequest{}, fmt.Errorf("it payout: bad intent amount: %w", err)
	}
	// PayoutRequest.Amount is what the RECIPIENT receives, denominated in the
	// destination currency — the gateway sends applies_to=destination_currency
	// on every transfer. So a cross-currency payout has to be converted HERE;
	// passing the source-currency figure through would send a naira number as
	// cedis, off by roughly 100x.
	sourceCurrency := models.ITIntentCurrency(intent)
	destCurrency := strings.ToUpper(strings.TrimSpace(intent.DestinationCurrency))
	if destCurrency == "" {
		destCurrency = sourceCurrency
	}

	payoutAmount := naira
	if !strings.EqualFold(destCurrency, sourceCurrency) {
		sourceMajor, cErr := koboToNairaFloat(net.String())
		if cErr != nil {
			return PayoutRequest{}, fmt.Errorf("it payout: bad intent amount: %w", cErr)
		}
		// NOTE: QuoteWithdrawal hardcodes NGN as the quote's SOURCE currency
		// (it_quote.go). That is correct only while every sell is denominated in
		// naira, which is true today. A sell raised in another currency would be
		// converted using an NGN→dest rate — the same class of bug as valuing a
		// cedi fee at the naira rate. Guard it here rather than let it become
		// silently wrong when sells gain a currency.
		if !strings.EqualFold(sourceCurrency, "NGN") {
			return PayoutRequest{}, fmt.Errorf("it payout: cannot convert %s→%s: the FX quote is NGN-sourced; "+
				"QuoteWithdrawal must take a source currency before non-NGN sells can pay out cross-currency",
				sourceCurrency, destCurrency)
		}
		destMajor, _, qErr := s.QuoteWithdrawal(ctx, sourceMajor, destCurrency)
		if qErr != nil {
			// Refusing beats guessing: paying out an unconverted figure would
			// send the wrong amount irrecoverably.
			return PayoutRequest{}, fmt.Errorf("it payout: cannot convert %s→%s: %w", sourceCurrency, destCurrency, qErr)
		}
		payoutAmount = strconv.FormatFloat(destMajor, 'f', 2, 64)
	}

	return PayoutRequest{
		BankCode:      intent.BankCode,
		AccountNumber: intent.AccountNumber,
		AccountName:   intent.AccountName,
		// Both are set during the transition: Amount/SourceCurrency is what the
		// gateway reads, AmountNGN keeps unmigrated consumers working.
		Amount:              payoutAmount,
		SourceCurrency:      sourceCurrency,
		AmountNGN:           naira,
		Reference:           itPayoutRefFor(intent),
		SenderName:          "TSA Connect",
		DestinationCurrency: intent.DestinationCurrency,
		RailType:            intent.RailType,
		MobileNetwork:       intent.MobileNetwork,
		MobileCountry:       intent.MobileCountry,
		MobileMsisdn:        intent.MobileMsisdn,
	}, nil
}

// ResolvePayoutFromWebhook closes a sell intent from a transfer-status
// webhook. merchantTxRef must carry the ITPayoutRefPrefix; payoutID is the
// provider transfer id the webhook names. Mapping: success → settle, failed →
// reverse (MCGP) or park (stablecoin), pending → no-op, reversed → operator.
//
// A signed webhook alone never moves money: the transfer is read back from the
// provider and its status must match. A webhook for another transfer id, or
// for a superseded reference generation, is acknowledged and ignored with an
// ALERT.
func (s *ITService) ResolvePayoutFromWebhook(ctx context.Context, merchantTxRef, payoutID, status string) error {
	if !s.IsConfigured() {
		return ErrITNotConfigured
	}
	intentID, gen, err := parseITPayoutRef(merchantTxRef)
	if err != nil {
		return fmt.Errorf("it payout webhook: bad ref %q: %w", merchantTxRef, err)
	}
	status = strings.ToLower(status)
	switch status {
	case "success", "failed":
	case "pending":
		return nil
	case "reversed":
		log.Printf("[it] payout REVERSED for intent %s — operator action required (funds returned after success; premium already settled)", intentID)
		return fmt.Errorf("%w: it payout reversal", ErrNonSettlingEvent)
	default:
		return fmt.Errorf("it payout webhook: unknown status %q for intent %s", status, intentID)
	}
	settle := status == "success"

	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.Status == models.ITStatusSettled || intent.Status == models.ITStatusReversed {
		_, err = s.resolveSell(ctx, intentID, "", settle) // no-op; alerts on a contradiction
		return err
	}
	if gen != intent.PayoutRefGen {
		alertf("webhook-gen:"+intentID.String()+":"+merchantTxRef,
			"[it] ALERT it_payout_webhook_superseded intent=%s ref=%s current_gen=%d — ignored", intentID, merchantTxRef, intent.PayoutRefGen)
		return fmt.Errorf("%w: superseded payout reference", ErrNonSettlingEvent)
	}
	payoutID = strings.TrimSpace(payoutID)
	if payoutID != "" && intent.PayoutID != "" && payoutID != intent.PayoutID {
		alertf("webhook-id:"+intentID.String()+":"+payoutID,
			"[it] ALERT it_payout_webhook_id_mismatch intent=%s webhook_transfer=%s recorded_transfer=%s — ignored", intentID, payoutID, intent.PayoutID)
		return fmt.Errorf("%w: payout id mismatch", ErrNonSettlingEvent)
	}
	transferID := intent.PayoutID
	if transferID == "" {
		transferID = payoutID
	}
	if transferID == "" {
		return fmt.Errorf("it payout webhook: no transfer id to confirm for intent %s", intentID)
	}
	if s.payoutTransfers == nil {
		return fmt.Errorf("it payout webhook: no provider reader configured to confirm transfer %s", transferID)
	}
	tr, err := s.payoutTransfers.GetTransfer(ctx, transferID)
	if err != nil {
		return fmt.Errorf("it payout webhook: confirm transfer %s: %w", transferID, err)
	}
	confirmed := strings.EqualFold(tr.Status, "FAILED")
	if settle {
		confirmed = fwSucceeded(tr.Status)
	}
	if !confirmed {
		alertf("webhook-status:"+transferID+":"+status,
			"[it] ALERT it_payout_webhook_unconfirmed intent=%s transfer=%s webhook=%s provider=%s — no action", intentID, transferID, status, tr.Status)
		return fmt.Errorf("%w: provider reports transfer %s as %s", ErrNonSettlingEvent, transferID, tr.Status)
	}
	if intent.PayoutID == "" {
		// The webhook beat the send to recording the id. Adopt it only if the
		// provider ties the transfer to this reference.
		if tr.Reference != merchantTxRef {
			alertf("webhook-ref:"+transferID,
				"[it] ALERT it_payout_webhook_reference_mismatch intent=%s transfer=%s provider_ref=%q webhook_ref=%q — ignored", intentID, transferID, tr.Reference, merchantTxRef)
			return fmt.Errorf("%w: transfer reference mismatch", ErrNonSettlingEvent)
		}
		if !s.adoptPayoutID(ctx, intent, transferID) {
			return fmt.Errorf("it payout webhook: could not record transfer %s for intent %s", transferID, intentID)
		}
	}
	if settle {
		_, err = s.SettleSell(ctx, intentID, "")
	} else {
		_, err = s.ReverseSell(ctx, intentID, "")
	}
	return err
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
