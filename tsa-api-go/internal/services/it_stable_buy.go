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

var (
	// ErrStableBuyAlreadyOpen enforces one purchase awaiting payment per user.
	// The standing VA's reference identifies the USER, not the purchase, so a
	// second intent in pending_payment would make an incoming charge ambiguous.
	ErrStableBuyAlreadyOpen = errors.New("it stable buy: this user already has a purchase awaiting payment")
	// ErrStableBuyNoVA means the user has no standing virtual account yet.
	ErrStableBuyNoVA = errors.New("it stable buy: user has no virtual account; activate one first")
)

// StableBuyInstructions is where the user sends their naira: their own
// standing virtual account, unchanged between purchases.
type StableBuyInstructions struct {
	AccountNumber string `json:"accountNumber"`
	BankName      string `json:"bankName"`
	AccountName   string `json:"accountName"`
	Reference     string `json:"reference"`
}

// CreateStableBuyIntent opens a purchase and tells the user where to pay.
//
// The delivery target is validated HERE rather than at delivery time, because
// delivery is the one irreversible step: Flutterwave rejects an unsupported
// (chain, asset) pair and a rejected transfer to a live address cannot be
// recovered. Nothing is written if the target is bad.
func (s *ITService) CreateStableBuyIntent(
	ctx context.Context,
	userID uuid.UUID,
	q *StableBuyQuote,
	destChain, destAddress string,
) (*models.ITIntent, *StableBuyInstructions, error) {
	if q == nil {
		return nil, nil, errors.New("it stable buy: nil quote")
	}
	destChain = strings.ToUpper(strings.TrimSpace(destChain))
	destAddress = strings.TrimSpace(destAddress)
	if err := ValidateBuyDelivery(destChain, q.Asset, destAddress); err != nil {
		return nil, nil, err
	}
	if q.AssetUnits == nil || q.AssetUnits.Cmp(s.stableMinSell()) < 0 {
		return nil, nil, fmt.Errorf("%w: minimum delivery is %g %s",
			ErrStableBuyBelowMinimum, StableFromBaseUnits(s.stableMinSell()), q.Asset)
	}

	var va models.FiatVirtualAccount
	err := s.db.WithContext(ctx).
		// Status filter matters: handing out an inactive VA's details tells the
		// user to pay into an account that will not credit them.
		Where("user_id = ? AND currency = ? AND status = ?",
			userID, q.Currency, models.FiatVAStatusActive).
		First(&va).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrStableBuyNoVA
	} else if err != nil {
		return nil, nil, err
	}

	var open int64
	if err := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("user_id = ? AND direction = ? AND status = ? AND asset IN ?",
			userID, models.ITDirectionBuy, models.ITStatusPendingPayment,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT}).
		Count(&open).Error; err != nil {
		return nil, nil, err
	}
	if open > 0 {
		return nil, nil, ErrStableBuyAlreadyOpen
	}
	// NOTE: this Count-then-Create is not atomic. Two concurrent requests can
	// both see zero and both insert. Add the backing partial unique index in
	// internal/config/database.go alongside the existing uq_it_intents_* ones:
	//
	//	CREATE UNIQUE INDEX IF NOT EXISTS uq_it_intents_open_stable_buy
	//	  ON it_intents (user_id)
	//	  WHERE direction = 'buy' AND status = 'pending_payment'
	//	    AND asset IN ('USDC','USDT');
	//
	// and translate its violation below to ErrStableBuyAlreadyOpen, so the race
	// loses at the database rather than producing a second claimable intent.
	// SQLite has no such index, which is why the check above stays as well.

	intent := &models.ITIntent{
		ID:          uuid.New(),
		UserID:      userID,
		Direction:   models.ITDirectionBuy,
		Status:      models.ITStatusPendingPayment,
		Asset:       q.Asset,
		AssetAmount: q.AssetUnits.String(),
		Currency:    q.Currency,
		DestChain:   destChain,
		DestAddress: destAddress,
		LockedRate:  q.RatePerUnitMinor.String(),

		// Every numeric column must carry a value, not "". PostgreSQL rejects
		// ''::numeric outright and GORM sends unset fields in the INSERT.
		// SQLite's type affinity accepts them silently, which is how this bit
		// the sell leg. The values below are correct, not filler:
		//
		//	NgnKobo/AmountMinor  what the user pays, as quoted
		//	McgpAmount           a stablecoin buy has no MCGP leg
		//	FeeNgnKobo/NetNgnKobo  B1 charges the spread only; no fee line
		//	ReceivedAmount       unknown until the naira actually lands
		NgnKobo:     q.FiatMinor.String(),
		AmountMinor: q.FiatMinor.String(),
		RateKobo:    q.RatePerUnitMinor.String(),
		RateMinor:   q.RatePerUnitMinor.String(),
		McgpAmount:  "0",
		FeeNgnKobo:  "0",
		NetNgnKobo:  q.FiatMinor.String(),
		// PlatformKeptKobo has no `default:` tag, so GORM puts it in the INSERT
		// as '' and BeforeSave mirrors that into PlatformKeptMinor. Postgres
		// rejects ''::numeric (SQLSTATE 22P02). This is the EXACT defect the
		// sell leg hit — it sets the same field explicitly at
		// it_stable_sell.go:191. SQLite's type affinity hides it, so the test
		// suite stays green while production INSERTs fail.
		PlatformKeptKobo: "0",
		ReceivedAmount:   "0",
	}
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, nil, err
	}

	return intent, &StableBuyInstructions{
		AccountNumber: va.AccountNumber,
		BankName:      va.BankName,
		AccountName:   va.AccountName,
		Reference:     va.Reference,
	}, nil
}

// itBuyConversionRef mints the reference for a buy's NGN→asset conversion.
//
// Same length accounting as itConversionRef: Flutterwave rejects references
// outside 6–42 chars. "ITBC-" (5) + hyphen-less UUID (32) = 37, leaving 5 for
// "-" plus the attempt count.
func itBuyConversionRef(intentID uuid.UUID, attempts int) string {
	return fmt.Sprintf("ITBC-%s-%d", strings.ReplaceAll(intentID.String(), "-", ""), attempts)
}

// ConvertStableBuy converts the user's naira into the stablecoin they bought.
//
// The balance check is the invariant in code: we convert only once Flutterwave
// reports the fiat as SPENDABLE. A charge.completed proves money was received,
// not that it settled into available_balance — and available_balance is what
// ConvertBalance actually draws on. If VA credits turn out to be immediately
// spendable this check passes on the first tick and is invisible; if they wait
// for T+1 it holds the intent correctly. That is the whole point of the state.
func (s *ITService) ConvertStableBuy(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	switch intent.Status {
	case models.ITStatusConverting, models.ITStatusDelivering, models.ITStatusSettled:
		return nil
	case models.ITStatusPaymentReceived, models.ITStatusAwaitingFunds:
	default:
		return ErrITBadState
	}
	if intent.ConversionAttempts >= maxStableConversionAttempts {
		return ErrStableConversionExhausted
	}
	if s.stableConverter == nil {
		return errors.New("it stable buy: converter not configured")
	}

	paid, ok := new(big.Int).SetString(strings.TrimSpace(intent.ReceivedAmount), 10)
	if !ok || paid.Sign() <= 0 {
		return errors.New("it stable buy: intent has no received amount")
	}

	// Read the spendable balance exactly as the sell leg does: FetchBalances
	// reports major units and AvailableBalance is the post-settlement figure
	// ConvertBalance actually draws on.
	balances, err := s.stableConverter.FetchBalances(ctx)
	if err != nil {
		return fmt.Errorf("it stable buy: %w", err)
	}
	var available float64
	found := false
	for _, b := range balances {
		if strings.EqualFold(b.Currency, intent.Currency) {
			available, found = b.AvailableBalance, true
			break
		}
	}
	if !found {
		// Distinct from an ordinary not-yet-settled wait: a missing line can
		// mean the integration itself is not surfacing this currency. Both
		// fail the same way; the log makes them distinguishable.
		log.Printf("[IT] it_stable_balance_line_missing currency=%s intent=%s", intent.Currency, intent.ID)
	}
	needed, _ := new(big.Rat).SetFrac(paid, big.NewInt(100)).Float64()
	if !found || available < needed {
		if intent.Status != models.ITStatusAwaitingFunds {
			if err := s.db.WithContext(ctx).Model(intent).
				Update("status", models.ITStatusAwaitingFunds).Error; err != nil {
				return err
			}
		}
		return fmt.Errorf("%w: %s spendable=%v needed=%v",
			ErrStableCreditNotVisible, intent.Currency, available, needed)
	}

	ref := itBuyConversionRef(intent.ID, intent.ConversionAttempts)
	tr, err := s.stableConverter.ConvertBalance(ctx, s.stableMerchantID,
		intent.Currency, intent.Asset,
		FWAmount{Value: needed, AppliesTo: "source_currency"}, ref)
	if err != nil {
		return fmt.Errorf("it stable buy convert: %w", err)
	}

	// Stop at converting. Flutterwave returns 201 {"status":"NEW"} and settles
	// later, so the asset does not exist yet — delivering here would send
	// stablecoin we have not bought.
	return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
		"status":        models.ITStatusConverting,
		"conversion_id": tr.ID,
	}).Error
}

// FinalizeBuyConversion advances a buy once its NGN→asset conversion settles.
//
// FAILED drops back to awaiting_funds rather than a terminal state: the user's
// naira is still in our balance, so retrying is both safe and correct. The
// attempt counter is bumped alongside so the next call mints a fresh
// reference — reusing the old one would hit Flutterwave's idempotency, return
// the same FAILED transfer, and loop forever.
func (s *ITService) FinalizeBuyConversion(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	switch intent.Status {
	case models.ITStatusDelivering, models.ITStatusSettled:
		return nil
	case models.ITStatusConverting:
	default:
		return ErrITBadState
	}
	if intent.ConversionID == "" {
		return errors.New("it stable buy: converting intent has no conversion id")
	}
	if s.stableTransfers == nil {
		return errors.New("it stable buy: no transfer reader configured")
	}

	tr, err := s.stableTransfers.GetTransfer(ctx, intent.ConversionID)
	if err != nil {
		return fmt.Errorf("it stable buy: %w", err)
	}
	switch strings.ToUpper(tr.Status) {
	case "SUCCESSFUL":
		return s.db.WithContext(ctx).Model(intent).
			Update("status", models.ITStatusDelivering).Error
	case "FAILED":
		return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
			"status":              models.ITStatusAwaitingFunds,
			"failure_reason":      "buy conversion failed: " + tr.ID,
			"conversion_id":       "",
			"conversion_attempts": intent.ConversionAttempts + 1,
		}).Error
	default:
		return fmt.Errorf("%w: conversion %s is %s", ErrStableConversionPending, tr.ID, tr.Status)
	}
}
