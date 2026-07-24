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
	if len(idNumber) != 11 {
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
		AccountName:   fwVA.AccountName,
		Status:        models.FiatVAStatusActive,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("it va persist: %w", err)
	}
	return row, nil
}

// AutoCreditFromVA settles a charge against a standing FiatVirtualAccount.
// Resolves the VA by fw customer id (primary) or reference (fallback), prices
// at the live buy rate, creates a buy ITIntent, and credits MCGP. Idempotent
// on payment_ref (= provider charge id).
func (s *ITService) AutoCreditFromVA(ctx context.Context, fwCustomerID, reference, chargeID, amountNGN string) error {
	if !s.IsConfigured() {
		return ErrITNotConfigured
	}
	chargeID = strings.TrimSpace(chargeID)
	if chargeID == "" {
		return errors.New("it va credit: empty charge id")
	}

	// Idempotency: already credited for this charge.
	var existing models.ITIntent
	err := s.db.WithContext(ctx).
		Where("payment_ref = ? AND direction = ?", chargeID, models.ITDirectionBuy).
		First(&existing).Error
	if err == nil {
		if existing.Status == models.ITStatusCredited || existing.Status == models.ITStatusAwaitingRate {
			return nil
		}
		if existing.Status == models.ITStatusPendingPayment || existing.Status == models.ITStatusFailed {
			_, err = s.ApproveBuyCredit(ctx, existing.ID, "")
			return err
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
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

	intent := &models.ITIntent{
		ID:            uuid.New(),
		UserID:        va.UserID,
		WalletAddress: wallet,
		Direction:     models.ITDirectionBuy,
		Status:        models.ITStatusPendingPayment,
		NgnKobo:       paidKobo.String(),
		RateKobo:      "0",
		McgpAmount:    "0",
		PaymentRef:    chargeID,
		PMID:          pmID,
	}

	if fx.Stale {
		intent.Status = models.ITStatusAwaitingRate
		intent.FailureReason = "fx stale at settlement — operator must reprice"
		if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
			return err
		}
		log.Printf("[it va] ALARM awaiting_rate intent=%s charge=%s — stale FX, no credit", intent.ID, chargeID)
		return nil
	}

	mcgp := mcgpForNgn(paidKobo, buyRate)
	if mcgp.Sign() <= 0 {
		return fmt.Errorf("it va credit: amount too small after pricing")
	}
	intent.RateKobo = buyRate.String()
	intent.McgpAmount = mcgp.String()
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		// Unique payment_ref race — redelivery.
		if isUniqueViolation(err) {
			return s.AutoCreditFromVA(ctx, fwCustomerID, reference, chargeID, amountNGN)
		}
		return err
	}

	_, err = s.ApproveBuyCredit(ctx, intent.ID, wallet)
	return err
}

func (s *ITService) resolveFiatVA(ctx context.Context, fwCustomerID, reference string) (*models.FiatVirtualAccount, error) {
	fwCustomerID = strings.TrimSpace(fwCustomerID)
	reference = strings.TrimSpace(reference)
	var va models.FiatVirtualAccount
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

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}
