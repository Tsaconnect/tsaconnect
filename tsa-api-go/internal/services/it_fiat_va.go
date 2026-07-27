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
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// Spec 1 — permanent per-user static VA for IT bank-transfer buys.
//
//	activate: CreateCustomer (per-user) → static VA + BVN/NIN → FiatVirtualAccount
//	settle:   charge.completed → AutoCreditFromVA → create credited ITIntent at live rate

const (
	// ITVARefPrefix is the create-REQUEST reference prefix for standing IT VAs.
	// Attribution uses the persisted RESPONSE reference + fw_customer_id; this
	// prefix is a secondary dispatch hint when the response ref still starts ITVA-.
	ITVARefPrefix = "ITVA-"

	// itVARefDomain namespaces the on-chain credit ref for VA settlement. The
	// ref is keccak(domain || providerChargeID) — keyed on the CHARGE, not the
	// intent, so the contract's single-use `usedRef` rejects a second credit
	// even if two intent rows were somehow created for one charge (Spec 1 §3).
	itVARefDomain = "IT:va:"
)

var (
	ErrITVANotConfigured  = errors.New("IT virtual account provisioning unavailable")
	ErrITVAWalletRequired = errors.New("set up a wallet before activating Instant Transfer")
	ErrITVANotFound       = errors.New("no Instant Transfer account for this user")
	ErrITKYCRequired      = errors.New("a BVN or NIN is required")
)

// SetFlutterwaveClient wires the Flutterwave API client used for per-user
// customer + static VA provisioning (Spec 1). Distinct from SetVAProvisioner
// (legacy per-intent dynamic VAs under the company customer).
func (s *ITService) SetFlutterwaveClient(c *FlutterwaveClient) { s.fw = c }

// HasFiatVAProvisioning reports whether Spec 1 activate can call Flutterwave.
func (s *ITService) HasFiatVAProvisioning() bool {
	return s != nil && s.fw != nil && s.fw.IsEnabled()
}

// GetFiatVA returns the user's active standing VA for currency (default NGN).
func (s *ITService) GetFiatVA(ctx context.Context, userID uuid.UUID, currency string) (*models.FiatVirtualAccount, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "NGN"
	}
	var va models.FiatVirtualAccount
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND currency = ? AND status = ?", userID, currency, models.FiatVAStatusActive).
		First(&va).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrITVANotFound
	}
	return &va, err
}

// ActivateFiatVA mints a permanent static NGN VA for the user under a new
// Flutterwave customer. idType/idNumber are forwarded to FW and NEVER stored
// or logged. Idempotent: an existing active row is returned unchanged.
func (s *ITService) ActivateFiatVA(ctx context.Context, user *models.User, idType, idNumber string) (*models.FiatVirtualAccount, error) {
	if !s.HasFiatVAProvisioning() {
		return nil, ErrITVANotConfigured
	}
	if user == nil {
		return nil, errors.New("it va: nil user")
	}
	if strings.TrimSpace(user.WalletAddress) == "" {
		return nil, ErrITVAWalletRequired
	}
	idType = strings.ToUpper(strings.TrimSpace(idType))
	idNumber = strings.TrimSpace(idNumber)
	if idType != "BVN" && idType != "NIN" {
		return nil, ErrITKYCRequired
	}
	// Same 11-digit rule the handler enforces — re-checked here because the
	// service is the boundary any other caller crosses.
	if !utils.IsValidNgIDNumber(idNumber) {
		return nil, ErrITKYCRequired
	}

	if existing, err := s.GetFiatVA(ctx, user.ID, "NGN"); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrITVANotFound) {
		return nil, err
	}

	first, last := splitPersonName(user.Name)
	reqRef := ITVARefPrefix + strings.ReplaceAll(user.ID.String(), "-", "")

	// Identity (BVN/NIN) goes on the VA — not the customer. Sending id_type /
	// id_number on /customers is rejected as REQUEST_NOT_VALID. Phone must be
	// the nested {country_code,number} object; omit when we can't normalize.
	custReq := FWCustomerRequest{
		Email:         user.Email,
		Name:          &FWName{First: first, Last: last},
		Phone:         ParseFWPhone(user.PhoneNumber),
		MetaReference: "it-cust-" + strings.ReplaceAll(user.ID.String(), "-", ""),
	}
	customerID, err := s.fw.CreateCustomer(ctx, custReq)
	if err != nil && custReq.Phone != nil {
		// Phone still wrong shape / rejected — retry email+name only.
		log.Printf("[it va] CreateCustomer with phone failed user=%s: %v — retrying without phone", user.ID, err)
		custReq.Phone = nil
		customerID, err = s.fw.CreateCustomer(ctx, custReq)
	}
	if err != nil {
		return nil, fmt.Errorf("it va create customer: %w", err)
	}

	vaReq := FWVirtualAccountRequest{
		CustomerID:  customerID,
		Reference:   reqRef,
		Currency:    "NGN",
		Amount:      0,
		AccountType: "static",
		Narration:   strings.TrimSpace(user.Name),
	}
	switch idType {
	case "NIN":
		vaReq.NIN = idNumber
	default:
		vaReq.BVN = idNumber
	}
	fwVA, err := s.fw.CreateVirtualAccount(ctx, vaReq)
	if err != nil {
		return nil, fmt.Errorf("it va create: %w", err)
	}

	// Persist RESPONSE reference — charge.completed echoes this, not reqRef.
	respRef := strings.TrimSpace(fwVA.Reference)
	if respRef == "" {
		respRef = reqRef
	}
	row := &models.FiatVirtualAccount{
		UserID:        user.ID,
		Currency:      "NGN",
		Provider:      models.FiatVAProviderFlutterwave,
		FwCustomerID:  customerID,
		FwVAID:        fwVA.ID,
		Reference:     respRef,
		AccountNumber: fwVA.AccountNumber,
		BankName:      fwVA.BankName,
		// FW sometimes returns an empty account_name on static VAs; fall back to
		// the KYC'd profile name so the buy screen never shows a blank row.
		AccountName: firstNonEmpty(fwVA.AccountName, strings.TrimSpace(user.Name)),
		Status:      models.FiatVAStatusActive,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("it va persist: %w", err)
	}
	return row, nil
}

// AutoCreditFromVA settles a charge against a standing FiatVirtualAccount.
// Resolves the VA by reference (API-verified) or fw customer id, prices at the
// live buy rate, settles into the user's open pending buy order when one exists
// (so the hub order progresses to credited), otherwise creates a new buy
// ITIntent.
//
// Idempotency has three layers, because this is the point where real money
// becomes an on-chain credit: the payment_ref lookup below, the partial unique
// index uq_it_intents_payment_ref underneath it, and the charge-derived
// single-use on-chain ref. It NEVER returns nil for a charge it did not
// actually settle — a nil marks the webhook processed and stops all re-drives.
func (s *ITService) AutoCreditFromVA(ctx context.Context, fwCustomerID, reference, chargeID, amountNGN string) error {
	if !s.IsConfigured() {
		return ErrITNotConfigured
	}
	chargeID = strings.TrimSpace(chargeID)
	if chargeID == "" {
		return errors.New("it va credit: empty charge id")
	}

	// The on-chain credit ref is derived from the charge, so every path below
	// (first attempt, webhook redelivery, reconciler re-drive) presents the SAME
	// ref to the contract and only one can ever succeed.
	chargeRef := itRefKey(itVARefDomain, chargeID)

	// Idempotency: have we already seen this charge?
	var existing models.ITIntent
	err := s.db.WithContext(ctx).
		Where("payment_ref = ? AND direction = ?", chargeID, models.ITDirectionBuy).
		First(&existing).Error
	if err == nil {
		switch existing.Status {
		case models.ITStatusCredited:
			return nil // settled
		case models.ITStatusAwaitingRate:
			return nil // parked for the operator; RepriceAndCreditAwaitingRate owns it
		case models.ITStatusPendingPayment, models.ITStatusFailed:
			_, err = s.approveBuyCreditWithRef(ctx, existing.ID, "", &chargeRef)
			return err
		case models.ITStatusCancelled:
			// The order was superseded in the window between our reading it and
			// claiming it. The money is real and is this user's — detach the charge
			// from the dead row and settle it below as if freshly arrived.
			res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
				Where("id = ? AND status = ? AND payment_ref = ?",
					existing.ID, models.ITStatusCancelled, chargeID).
				Update("payment_ref", "")
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				// Someone moved it under us — retry rather than risk double-settling.
				return fmt.Errorf("it va credit: intent %s changed during recovery", existing.ID)
			}
			log.Printf("[it va] charge=%s was attributed to superseded intent=%s — re-settling", chargeID, existing.ID)
			// falls through to the normal settle flow below
		default:
			// `processing` (a broadcast that never reported back) or any future
			// status. The money HAS landed and is not credited — returning nil here
			// would mark the webhook processed and drop it silently, so alarm and
			// stay retryable for the reconciler.
			log.Printf("[it va] ALARM charge=%s landed but intent=%s is %q — not credited, will retry",
				chargeID, existing.ID, existing.Status)
			return fmt.Errorf("it va credit: intent %s in non-creditable state %q", existing.ID, existing.Status)
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	va, err := s.resolveFiatVA(ctx, fwCustomerID, reference)
	if err != nil {
		return fmt.Errorf("it va credit: resolve: %w", err)
	}

	var user models.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", va.UserID).Error; err != nil {
		return fmt.Errorf("it va credit: user: %w", err)
	}
	wallet := strings.TrimSpace(user.WalletAddress)
	if wallet == "" {
		log.Printf("[it va] ALARM credit with no wallet user=%s charge=%s — parking", va.UserID, chargeID)
		return fmt.Errorf("it va credit: user %s has no wallet", va.UserID)
	}

	paidKobo, err := nairaToKobo(amountNGN)
	if err != nil {
		return fmt.Errorf("it va credit: bad amount %q: %w", amountNGN, err)
	}
	if paidKobo.Sign() <= 0 {
		return fmt.Errorf("it va credit: non-positive amount")
	}

	buyRate, _, fx, err := s.EffectiveKoboRates(ctx)
	if err != nil {
		return fmt.Errorf("it va credit: rate: %w", err)
	}

	var pmID *uuid.UUID
	if pm, _ := s.CompanyPM(ctx); pm != nil {
		pmID = &pm.ID
	}

	// Prefer the user's open buy order so the hub "Awaiting transfer" row
	// progresses to credited instead of leaving an orphan pending + a new intent.
	var pending models.ITIntent
	pendErr := s.db.WithContext(ctx).
		Where("user_id = ? AND direction = ? AND status = ?",
			va.UserID, models.ITDirectionBuy, models.ITStatusPendingPayment).
		Order("created_at DESC").
		First(&pending).Error

	if fx.Stale {
		intent := &pending
		if errors.Is(pendErr, gorm.ErrRecordNotFound) {
			intent = &models.ITIntent{
				ID:            uuid.New(),
				UserID:        va.UserID,
				WalletAddress: wallet,
				Direction:     models.ITDirectionBuy,
				PMID:          pmID,
			}
		} else if pendErr != nil {
			return pendErr
		}
		intent.Status = models.ITStatusAwaitingRate
		intent.FailureReason = "fx stale at settlement — operator must reprice"
		intent.NgnKobo = paidKobo.String()
		intent.PaymentRef = chargeID
		intent.RateKobo = "0"
		intent.McgpAmount = "0"
		if pendErr == nil {
			if err := s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
				"status": intent.Status, "failure_reason": intent.FailureReason,
				"ngn_kobo": intent.NgnKobo, "payment_ref": intent.PaymentRef,
				"rate_kobo": intent.RateKobo, "mcgp_amount": intent.McgpAmount,
			}).Error; err != nil {
				return err
			}
		} else if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
			return err
		}
		log.Printf("[it va] ALARM awaiting_rate intent=%s charge=%s — stale FX, no credit", intent.ID, chargeID)
		return nil
	}

	// Fees come off the naira that landed; only the net buys MCGP.
	mcgp, fees, err := s.creditableMcgp(ctx, paidKobo, buyRate)
	if err != nil {
		return fmt.Errorf("it va credit: %w", err)
	}

	if pendErr == nil {
		if err := s.db.WithContext(ctx).Model(&pending).Updates(map[string]any{
			"payment_ref":        chargeID,
			"ngn_kobo":           paidKobo.String(),
			"rate_kobo":          buyRate.String(),
			"mcgp_amount":        mcgp.String(),
			"fee_ngn_kobo":       fees.Fee.String(),
			"net_ngn_kobo":       fees.Net.String(),
			"platform_kept_kobo": fees.PlatformKept.String(),
			"wallet_address":     firstNonEmpty(pending.WalletAddress, wallet),
		}).Error; err != nil {
			return err
		}
		_, err = s.approveBuyCreditWithRef(ctx, pending.ID, wallet, &chargeRef)
		return err
	}
	if !errors.Is(pendErr, gorm.ErrRecordNotFound) {
		return pendErr
	}

	intent := &models.ITIntent{
		ID:            uuid.New(),
		UserID:        va.UserID,
		WalletAddress: wallet,
		Direction:     models.ITDirectionBuy,
		Status:        models.ITStatusPendingPayment,
		NgnKobo:       paidKobo.String(),
		RateKobo:      buyRate.String(),
		McgpAmount:    mcgp.String(),
		PaymentRef:    chargeID,
		PMID:          pmID,
	}
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		if isUniqueViolation(err) {
			// A concurrent delivery of this charge won the race and owns the
			// intent. Return retryable rather than recursing — the re-drive takes
			// the idempotency branch above and observes the winner's outcome.
			log.Printf("[it va] charge=%s lost create race (concurrent delivery) — deferring", chargeID)
			return fmt.Errorf("it va credit: concurrent settlement of charge %s", chargeID)
		}
		return err
	}

	_, err = s.approveBuyCreditWithRef(ctx, intent.ID, wallet, &chargeRef)
	return err
}

// resolveFiatVA maps a settled charge back to the account that received it.
//
// Reference FIRST, customer id second: the dispatcher verifies the reference
// against the Flutterwave API before we are called (verifyFlutterwaveCharge
// rejects a reference the API does not confirm), whereas the customer id is
// taken from the webhook body and is not independently checked. Preferring the
// verified key means a payload pairing one user's reference with another user's
// customer id can never credit the wrong account.
func (s *ITService) resolveFiatVA(ctx context.Context, fwCustomerID, reference string) (*models.FiatVirtualAccount, error) {
	fwCustomerID = strings.TrimSpace(fwCustomerID)
	reference = strings.TrimSpace(reference)
	var va models.FiatVirtualAccount
	if reference != "" {
		err := s.db.WithContext(ctx).
			Where("reference = ? AND status = ?", reference, models.FiatVAStatusActive).
			First(&va).Error
		if err == nil {
			return &va, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if fwCustomerID != "" {
		err := s.db.WithContext(ctx).
			Where("fw_customer_id = ? AND status = ?", fwCustomerID, models.FiatVAStatusActive).
			First(&va).Error
		if err == nil {
			return &va, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return nil, ErrITVANotFound
}

// QuoteBuyAmount prices an indicative NGN buy without creating an intent.
func (s *ITService) QuoteBuyAmount(ctx context.Context, ngnKobo string) (mcgpWei, rateKobo *big.Int, err error) {
	ngn, ok := new(big.Int).SetString(strings.TrimSpace(ngnKobo), 10)
	if !ok || ngn.Sign() <= 0 {
		return nil, nil, fmt.Errorf("invalid ngnKobo")
	}
	buyRate, _, fx, err := s.EffectiveKoboRates(ctx)
	if err != nil {
		return nil, nil, err
	}
	if fx.Stale {
		return nil, nil, ErrITFxUnavailable
	}
	mcgp := mcgpForNgn(ngn, buyRate)
	if mcgp.Sign() <= 0 {
		return nil, nil, fmt.Errorf("amount too small")
	}
	return mcgp, buyRate, nil
}

func splitPersonName(full string) (first, last string) {
	full = strings.TrimSpace(full)
	if full == "" {
		return "Customer", "User"
	}
	parts := strings.Fields(full)
	if len(parts) == 1 {
		return parts[0], parts[0]
	}
	return parts[0], strings.Join(parts[1:], " ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}
