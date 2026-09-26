package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

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

// buyOrderClaimWindow bounds how long an unpaid MCGP buy order may claim an
// incoming charge.
//
// Nothing else expires one. OpenBuyOrder supersedes a previous MCGP order, but
// only when the user opens another MCGP buy — opening a stablecoin purchase
// runs through CreateStableBuyIntent and leaves the MCGP row untouched. So an
// abandoned order sits in pending_payment indefinitely and silently swallows
// the user's next unrelated transfer, which is precisely the complaint this
// window answers.
//
// Deliberately the same 24h as stableBuyClaimWindow: the question is identical
// (how long may an unpaid order lie in wait for money?) and two different
// answers on the two legs would be a difference nobody could justify later.
// Past the window the charge creates a fresh order at the live rate, which is
// the same thing that happens for a user with no open order at all.
const buyOrderClaimWindow = stableBuyClaimWindow

var (
	ErrITVANotConfigured  = errors.New("IT virtual account provisioning unavailable")
	ErrITVAWalletRequired = errors.New("set up a wallet before activating Instant Transfer")
	ErrITVANotFound       = errors.New("no Instant Transfer account for this user")
	ErrITKYCRequired      = errors.New("a BVN or NIN is required")
	// ErrITVARetired is the cutover's answer to an activation request. Central
	// mode retires virtual accounts as a class, so this is permanent for as long
	// as the mode is on — not a "try again later" the caller should retry.
	ErrITVARetired = errors.New("Instant Transfer accounts are retired — pay into the account shown on your order")
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
	// Before every other check, including whether provisioning is even wired:
	// under central inbound, nothing reads a virtual account's statement. A VA
	// minted now would take the user's money into an account no ingestor polls
	// and no claim can be matched against, and the user would have no way to
	// tell that from a working one. Refusing is the only honest answer, and
	// refusing FIRST means the answer does not change with whether a Flutterwave
	// client happens to be configured.
	if _, ok := s.ActiveCentralAccount(); ok {
		return nil, ErrITVARetired
	}
	if !s.HasFiatVAProvisioning() {
		return nil, ErrITVANotConfigured
	}
	if user == nil {
		return nil, errors.New("it va: nil user")
	}
	// A virtual account is a standing pay-in account — AutoCreditFromVA credits
	// whatever lands on it, order or no order — so minting one is a money-in
	// entry point like any buy. After the retirement answer above (410 is the
	// honest reply in central mode whatever the user's status) and before any
	// provider call, so an unverified user never becomes a Flutterwave customer.
	if err := requireVerifiedForMoneyIn(ctx, s.db, user.ID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(user.WalletAddress) == "" {
		return nil, ErrITVAWalletRequired
	}
	// A virtual account is a COLLECTION account, so provisioning one in a
	// currency we cannot collect in creates an account nobody can usefully pay
	// into. The registry is the authority on that — USD, EUR and GBP have no
	// virtual-account product at the provider and can never be deposit
	// currencies, so this is a permanent distinction rather than a staging one.
	if s.currencies != nil {
		if _, err := s.currencies.RequireDeposit(itDepositCurrency); err != nil {
			return nil, err
		}
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
		case models.ITStatusPaymentReceived, models.ITStatusAwaitingFunds,
			models.ITStatusConverting, models.ITStatusDelivering,
			models.ITStatusBuyAwaitingRate, models.ITStatusSettled:
			// A stablecoin buy already claimed this charge. The buy watcher owns
			// it from here; the webhook's job is done — EXCEPT when the claim
			// crashed before pricing: `payment_received` + `!Repriced` means the
			// charge was claimed but never priced (see priceStableBuy). That
			// intent still carries its creation-time quote, which is the rate
			// lock the design removed — reappearing in exactly this crash
			// window. `break` falls through to the fork below, which is
			// idempotent for the already-priced case and completes pricing for
			// the unpriced one.
			//
			// `ITStatusSettled` is safe to add here despite being shared with
			// the MCGP sell/debit leg: for direction=buy (this whole switch is
			// scoped to `direction = ?` = buy in the query above) `settled` is
			// unreachable by MCGP, whose buy terminal is `credited` — it only
			// occurs on a completed stablecoin delivery.
			//
			// Gated on HasStableBuy for the same reason the fork below is: with
			// buy unconfigured there is no fork to fall through TO, so control
			// would reach the MCGP path holding a charge id a stablecoin intent
			// already owns — and payment_ref carries a partial unique index.
			if s.HasStableBuy() &&
				models.ITIntentAsset(&existing) != models.ITAssetMCGP &&
				existing.Status == models.ITStatusPaymentReceived && !existing.Repriced {
				break
			}
			return nil
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

	// Fork: a stablecoin purchase awaiting payment claims this charge. If the
	// user has none, fall through to the MCGP credit path below, unchanged.
	//
	// amountNGN is a naira decimal string ("165025.00"), NOT kobo — the MCGP
	// path below converts it the same way at :259. settleStableBuyFromCharge
	// takes minor units, so the conversion happens HERE, at the boundary.
	//
	// This must run BEFORE the wallet guard below: a stablecoin buy is
	// delivered by Flutterwave to a chain address, not an on-chain MCGP
	// credit, so it needs no users.wallet_address. Putting the fork after that
	// guard would strand the money of any buyer who never registered one.
	paidMinor, err := nairaToKobo(amountNGN)
	if err != nil {
		return fmt.Errorf("it va credit: bad amount %q: %w", amountNGN, err)
	}
	if paidMinor.Sign() <= 0 {
		// Checked before the fork, not just the MCGP path below: a "0.00"
		// charge reaching settleStableBuyFromCharge would claim and then fail
		// to price a legitimate open purchase, parking it in
		// buy_awaiting_rate and burning its payment_ref for nothing.
		return fmt.Errorf("it va credit: non-positive amount")
	}
	//
	// HasStableBuy is the gate that matters most in this function. With the
	// buy leg unconfigured — Flutterwave present but no converter, merchant
	// id, rate source or deliverer — claiming here would suppress the MCGP
	// credit and hand the money to a state machine no watcher is driving. The
	// user's naira would sit indefinitely with nothing able to fulfil it. Fall
	// through to the MCGP path instead: the user gets a credit rather than a
	// hold, which is the recoverable failure of the two.
	if s.HasStableBuy() {
		claimed, err := s.settleStableBuyFromCharge(ctx, va.UserID, chargeID, paidMinor.String())
		if err != nil {
			return fmt.Errorf("it va credit: stable buy: %w", err)
		}
		if claimed {
			return nil
		}
	}

	wallet := strings.TrimSpace(user.WalletAddress)
	if wallet == "" {
		log.Printf("[it va] ALARM credit with no wallet user=%s charge=%s — parking", va.UserID, chargeID)
		return fmt.Errorf("it va credit: user %s has no wallet", va.UserID)
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
	//
	// Scoped to MCGP (or the pre-Asset-column legacy rows): the fork above
	// already claims a stablecoin buy before this point, but scoping here too
	// makes correctness structural rather than order-dependent — nothing on
	// this path can overwrite mcgp_amount/rate_kobo on a USDC/USDT intent.
	//
	// Bounded by buyOrderClaimWindow for the reason that constant records: an
	// abandoned order never expires on its own, and an unbounded match lets one
	// swallow a payment made days later for something else entirely.
	var pending models.ITIntent
	pendErr := s.db.WithContext(ctx).
		Where("user_id = ? AND direction = ? AND status = ? AND (asset IS NULL OR asset = '' OR asset = ?) AND created_at >= ?",
			va.UserID, models.ITDirectionBuy, models.ITStatusPendingPayment, models.ITAssetMCGP,
			time.Now().Add(-buyOrderClaimWindow)).
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
				// The VA's own currency, never a default: this parked intent is
				// repriced later by an operator, and inheriting NGN here would
				// have it repriced at the naira rate.
				Currency: va.Currency,
				PMID:     pmID,
				// net_ngn_kobo carries no column default (see the numeric-column
				// rule on models.ITIntent): "" there means "settle at the gross"
				// and PostgreSQL rejects it outright. Nothing has been deducted
				// from a parked credit, so net IS the gross until
				// RepriceAndCreditAwaitingRate applies the real fee.
				NetNgnKobo: paidMinor.String(),
			}
		} else if pendErr != nil {
			return pendErr
		}
		intent.Status = models.ITStatusAwaitingRate
		intent.FailureReason = "fx stale at settlement — operator must reprice"
		intent.NgnKobo = paidMinor.String()
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
	mcgp, fees, err := s.creditableMcgp(ctx, paidMinor, buyRate, va.Currency)
	if err != nil {
		return fmt.Errorf("it va credit: %w", err)
	}

	if pendErr == nil {
		if err := s.db.WithContext(ctx).Model(&pending).Updates(map[string]any{
			"payment_ref":        chargeID,
			"ngn_kobo":           paidMinor.String(),
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
		Currency:      va.Currency,
		NgnKobo:       paidMinor.String(),
		RateKobo:      buyRate.String(),
		McgpAmount:    mcgp.String(),
		PaymentRef:    chargeID,
		PMID:          pmID,
	}
	// The credited MCGP above is already NET of fees, so the fee columns must
	// record what was taken — exactly as the update branch a few lines up does
	// for an existing pending order. Without this the row credits net but reads
	// as a fee-free purchase, and settleITFee has no platform-kept base.
	applyFees(intent, fees)
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

// IsUniqueViolation is isUniqueViolation for callers outside this package.
func IsUniqueViolation(err error) bool { return isUniqueViolation(err) }

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}

// ─── central-account settlement (inbound attribution design §6.1) ──────────

// itInboundRefDomain namespaces the on-chain credit ref for a buy settled from
// the CENTRAL account rather than a per-user virtual account. The ref becomes
// keccak256("IT:inbound:" || NIBSS session id), replacing the charge-keyed
// keccak256(itVARefDomain || chargeID) that VA settlement uses.
//
// The property being carried over verbatim from the VA path: the ref is derived
// from THE MONEY, not from the order. Every route that can settle one credit —
// the poller's first attempt, its 30-second replay after a crash, an operator
// assigning the credit by hand from the admin queue — presents the SAME ref to
// the contract, so `usedRef` refuses the second and only one can ever succeed.
// Keying on the intent instead would let two intents for one transfer credit
// twice, and the session id is the only identifier the central account gives us
// that is unique per credit.
//
// The domain differs from itVARefDomain so the two rails can never collide on a
// shared identifier, and so an on-chain ref stays traceable to the rail that
// produced it.
const itInboundRefDomain = "IT:inbound:"

// CreditBuyOrderFromInbound settles an MCGP buy order against a credit into the
// central account. It is the sibling of AutoCreditFromVA: same live-rate
// pricing, same fee deduction, same approveBuyCreditWithRef broadcast — keyed
// on the NIBSS session id instead of the Flutterwave charge id.
//
// It is deliberately NARROWER than AutoCreditFromVA. That function has to work
// out who paid, whether a stablecoin purchase wants the money first, and which
// of several orders to settle into, because a webhook arrives carrying nothing
// but a charge. Here the matcher has already decided all of that and names one
// specific intent, so this only has to make the credit happen exactly once.
//
// paidKobo is what the SENDER transferred (the credit's gross, snapped to whole
// naira), not what landed after the provider's cut. The user is credited for the
// money they actually sent; the funding charge is a platform cost covered by the
// platform fee, exactly as the 2.15% VA charge was.
func (s *ITService) CreditBuyOrderFromInbound(ctx context.Context, intentID uuid.UUID, providerRef string, paidKobo *big.Int) error {
	if !s.IsConfigured() {
		return ErrITNotConfigured
	}
	providerRef = strings.TrimSpace(providerRef)
	if providerRef == "" {
		return errors.New("it inbound credit: empty provider ref")
	}
	if paidKobo == nil || paidKobo.Sign() <= 0 {
		return errors.New("it inbound credit: non-positive amount")
	}
	onChainRef := itRefKey(itInboundRefDomain, providerRef)

	// Layer 1 of three: has this session id already been attributed? The other
	// two are the partial unique index on payment_ref and the contract's
	// usedRef. All three key on the money, so a replay cannot slip past them by
	// arriving through a different route.
	var existing models.ITIntent
	err := s.db.WithContext(ctx).
		Where("payment_ref = ? AND direction = ?", providerRef, models.ITDirectionBuy).
		First(&existing).Error
	switch {
	case err == nil:
		switch existing.Status {
		case models.ITStatusCredited:
			return nil
		case models.ITStatusPendingPayment, models.ITStatusFailed:
			// Priced and attributed, but the broadcast never landed. Re-drive
			// with the SAME ref — the contract rejects it if it did land.
			_, err = s.approveBuyCreditWithRef(ctx, existing.ID, "", &onChainRef)
			return err
		default:
			// `processing` (broadcast with no reported outcome) or anything
			// else. The money is real and is not credited, so this stays
			// retryable rather than reporting success — a nil here would let
			// the matcher mark the inbound credit `matched` with nothing
			// behind it.
			return fmt.Errorf("it inbound credit: intent %s already owns ref %s in state %q",
				existing.ID, providerRef, existing.Status)
		}
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return err
	}

	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return fmt.Errorf("it inbound credit: %w", err)
	}
	// Asset check as well as direction+status, for the reason
	// RepriceAndCreditAwaitingRate gives: crediting a non-MCGP buy here would
	// issue MCGP from company supply for a purchase made in something else.
	if intent.Direction != models.ITDirectionBuy ||
		intent.Status != models.ITStatusPendingPayment ||
		models.ITIntentAsset(intent) != models.ITAssetMCGP {
		return fmt.Errorf("it inbound credit: intent %s is not an open MCGP buy (%s/%s)",
			intent.ID, intent.Direction, intent.Status)
	}
	// Only an order still carrying its MINTED reference may be settled. A row
	// whose payment_ref is already a provider identifier has other money
	// landing against it, and payment_ref carries a partial unique index.
	if !strings.HasPrefix(intent.PaymentRef, mintedRefPrefix) {
		return fmt.Errorf("it inbound credit: intent %s already carries payment ref %q",
			intent.ID, intent.PaymentRef)
	}

	wallet := s.intentWallet(intent, "")
	if wallet == "" {
		var user models.User
		if err := s.db.WithContext(ctx).First(&user, "id = ?", intent.UserID).Error; err != nil {
			return fmt.Errorf("it inbound credit: user: %w", err)
		}
		wallet = strings.TrimSpace(user.WalletAddress)
	}
	if wallet == "" {
		return fmt.Errorf("it inbound credit: user %s has no wallet", intent.UserID)
	}

	buyRate, _, fx, err := s.EffectiveKoboRates(ctx)
	if err != nil {
		return fmt.Errorf("it inbound credit: rate: %w", err)
	}
	if fx.Stale {
		// The VA path parks a stale-FX credit as `awaiting_rate` because a
		// webhook is delivered once and cannot be asked for again. Here the
		// inbound_credits row IS durable and the poller re-reads it every 30
		// seconds, so a retryable error is the better answer: no operator
		// ticket, and — the part that matters — no second on-chain ref domain.
		// RepriceAndCreditAwaitingRate derives its ref from itVARefDomain, so
		// parking here would hand the operator recovery a DIFFERENT ref for the
		// same money and usedRef would no longer be able to refuse the double
		// credit.
		return ErrITFxUnavailable
	}

	// Fees come off the naira that landed; only the net buys MCGP.
	mcgp, fees, err := s.creditableMcgp(ctx, paidKobo, buyRate, itDepositCurrency)
	if err != nil {
		return fmt.Errorf("it inbound credit: %w", err)
	}

	// Repriced at the LIVE rate on the amount actually sent, not at the quote
	// the order was minted with — identical to VA settlement, and the reason
	// the order's own figure is never trusted here.
	//
	// Guarded on the state read above, not written by id alone. Everything
	// between that read and this write — the wallet lookup, the rate, the fee
	// quote — is time in which the user can cancel the order or the sweep can
	// expire it. An unguarded write then left a CANCELLED row carrying this
	// session id and amounts nobody agreed to, with approveBuyCreditWithRef
	// refusing it one line later. The status transition itself stays in
	// approveBuyCreditWithRef's claim: folding it in here would move the row
	// to `processing` before that claim runs, and the claim only accepts
	// pending_payment|failed — the broadcast would never happen.
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ? AND payment_ref = ?",
			intent.ID, models.ITStatusPendingPayment, intent.PaymentRef).
		Updates(map[string]any{
			"payment_ref":        providerRef,
			"ngn_kobo":           paidKobo.String(),
			"rate_kobo":          buyRate.String(),
			"mcgp_amount":        mcgp.String(),
			"fee_ngn_kobo":       fees.Fee.String(),
			"net_ngn_kobo":       fees.Net.String(),
			"platform_kept_kobo": fees.PlatformKept.String(),
			"wallet_address":     wallet,
		})
	if err := res.Error; err != nil {
		if isUniqueViolation(err) {
			// Another route claimed this session id between our lookup and our
			// write. Retryable: the re-drive takes the idempotency branch above
			// and observes the winner's outcome.
			return fmt.Errorf("it inbound credit: concurrent settlement of ref %s", providerRef)
		}
		return err
	}
	if res.RowsAffected == 0 {
		// Cancelled, expired or claimed by other money since the read. The same
		// outcome as finding it closed in the first place, and just as
		// retryable: the matcher leaves the credit unmatched, and on the next
		// tick the order is no longer offered, so the credit holds for an
		// operator rather than settling into a closed order.
		return fmt.Errorf("it inbound credit: intent %s is no longer an open MCGP buy", intent.ID)
	}

	_, err = s.approveBuyCreditWithRef(ctx, intent.ID, wallet, &onChainRef)
	return err
}
