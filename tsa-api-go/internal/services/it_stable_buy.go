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
	// ErrStableBuyNoWallet means the user has no wallet to receive into.
	ErrStableBuyNoWallet = errors.New("it stable buy: user has no wallet")
)

// userDeliveryAddresses maps CHAIN -> the user's own address.
//
// TWO sources, not one. An earlier draft read models.Wallet.Addresses; that is
// a DEAD COLUMN — models.WalletAddrEntry occurs exactly once in this repo (its
// own declaration) and nothing has written it since the Node/Mongo API was
// retired. Reading it returns an empty map for every user, which would make
// every buy fail with "no address on this chain".
//
//	ETHEREUM / POLYGON / BASE -> users.wallet_address (one EVM address serves all)
//	SOLANA                    -> wallet_addresses where chain_key = 'solana'
//
// Keys are upper-cased on the way out: every chain-key producer in this repo is
// lowercase ("polygon", "solana") while deliveryMatrix keys are uppercase, so a
// case-sensitive lookup silently finds nothing.
func (s *ITService) userDeliveryAddresses(ctx context.Context, userID uuid.UUID) (map[string]string, error) {
	out := make(map[string]string, 4)

	var user models.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrStableBuyNoWallet
		}
		return nil, err
	}
	if evm := strings.TrimSpace(user.WalletAddress); evm != "" {
		for _, chain := range []string{"ETHEREUM", "POLYGON", "BASE"} {
			out[chain] = evm
		}
	}

	var rows []models.WalletAddress
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		key := strings.ToUpper(strings.TrimSpace(r.ChainKey))
		if addr := strings.TrimSpace(r.Address); addr != "" && key != "" {
			out[key] = addr
		}
	}

	if len(out) == 0 {
		return nil, ErrStableBuyNoWallet
	}
	return out, nil
}

// UserDeliveryAddress returns the user's own address on `chain`.
//
// The address is never accepted from the request body. A typed address is the
// single way this flow could lose funds irrecoverably, so there is no code
// path that reads one from the caller.
func (s *ITService) UserDeliveryAddress(ctx context.Context, userID uuid.UUID, chain string) (string, error) {
	chain = strings.ToUpper(strings.TrimSpace(chain))
	addrs, err := s.userDeliveryAddresses(ctx, userID)
	if err != nil {
		return "", err
	}
	addr := addrs[chain]
	if addr == "" {
		return "", fmt.Errorf("%w: you have no %s address to receive on", ErrBadDeliveryAddress, chain)
	}
	return addr, nil
}

// DeliverableChainsForUser lists the chains this user can be paid `asset` on.
func (s *ITService) DeliverableChainsForUser(ctx context.Context, userID uuid.UUID, asset string) ([]string, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}
	addrs, err := s.userDeliveryAddresses(ctx, userID)
	if err != nil {
		return nil, err
	}
	return DeliverableChains(asset, addrs), nil
}

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

// CancelStableBuy lets a user abandon their own stablecoin buy before money
// lands. This is the self-service exit ErrStableBuyAlreadyOpen otherwise
// wedges a user behind, and — together with stableBuyClaimWindow in
// it_stable_buy_settle.go — closes the hazard of an abandoned intent sitting
// in pending_payment indefinitely and later hijacking an unrelated payment
// from the same user (including a plain MCGP purchase).
//
// Refuses anything not owned by userID (ErrStableIntentNotFound, never
// leaking that a differently-owned intent exists) and anything not still
// pending_payment (ErrITBadState) — once money may have landed, cancelling
// would either strand a payment mid-flight or silently un-claim one that
// already settled.
func (s *ITService) CancelStableBuy(ctx context.Context, userID, intentID uuid.UUID) error {
	intent, err := s.getIntentForUser(ctx, userID, intentID)
	if err != nil {
		return err
	}
	// Scoped to stablecoin, matching the endpoint's name: an MCGP buy has its
	// own lifecycle (OpenBuyOrder supersedes the prior one on a new order) and
	// no reason to gain a second cancel path here.
	if !IsStableAsset(intent.Asset) || intent.Status != models.ITStatusPendingPayment {
		return ErrITBadState
	}
	// Status-guarded, same pattern as the settle claim: a charge landing in
	// the same instant must win, not this cancel.
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ?", intent.ID, models.ITStatusPendingPayment).
		Updates(map[string]any{
			"status":         models.ITStatusCancelled,
			"failure_reason": "cancelled by user",
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Lost the race to a charge claiming it — no longer cancellable is not
		// an error from the caller's point of view worth surfacing as one, but
		// it is also not a lie: the intent genuinely isn't pending anymore.
		return ErrITBadState
	}
	return nil
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
		log.Printf("[IT] it_stable_conversion_exhausted intent=%s attempts=%d", intent.ID, intent.ConversionAttempts)
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

	// Claim the intent before spending, so two concurrent ticks cannot both
	// convert. A double conversion moves real money twice. Mirrors
	// ConvertAndQueuePayout in it_stable_sell.go.
	claimed, err := s.claim(ctx, intent.ID,
		[]string{models.ITStatusPaymentReceived, models.ITStatusAwaitingFunds},
		models.ITStatusConverting)
	if err != nil {
		return err
	}
	if !claimed {
		// Another tick already claimed this intent; nothing more to do.
		return nil
	}

	ref := itBuyConversionRef(intent.ID, intent.ConversionAttempts)
	tr, err := s.stableConverter.ConvertBalance(ctx, s.stableMerchantID,
		intent.Currency, intent.Asset,
		FWAmount{Value: needed, AppliesTo: "source_currency"}, ref)
	if err != nil {
		// Release the claim so the next tick retries rather than stranding it
		// in converting with no conversion id.
		_ = s.db.WithContext(ctx).Model(intent).
			Update("status", intent.Status).Error
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

// maxStableDeliveryAttempts caps outbound delivery retries, mirroring
// maxStableConversionAttempts. The intent is deliberately left non-terminal
// when exhausted: the stuck list filters to non-terminal statuses, so marking
// it terminal would hide the intent an operator most needs to see.
const maxStableDeliveryAttempts = 10

// ErrStableDeliveryExhausted means delivery failed too many times and needs an
// operator. The stablecoin is in our Flutterwave balance — stuck, not lost.
var ErrStableDeliveryExhausted = errors.New("it stable buy: delivery attempts exhausted; needs operator review")

// ErrStableDeliveryPending means the outbound transfer has not reached a
// terminal state yet. Distinct from ErrStableConversionPending so an operator
// reading the watcher's log can tell which leg is waiting — the watcher polls
// every 30s, so a misleading sentinel here would be the noisiest line in the
// log and would point at the wrong hop.
var ErrStableDeliveryPending = errors.New("it stable buy: delivery has not settled yet")

// stableDeliverer is the slice of the Flutterwave client used to pay a user
// their stablecoin.
type stableDeliverer interface {
	DirectTransfer(ctx context.Context, req FWDirectTransferRequest) (FWTransfer, error)
}

// SetStableDeliverer wires the outbound crypto payout path.
func (s *ITService) SetStableDeliverer(d stableDeliverer) { s.stableDeliverer = d }

// itBuyDeliveryRef mints the reference for a buy's outbound crypto transfer.
// "ITBD-" (5) + hyphen-less UUID (32) = 37, inside Flutterwave's 6–42 range.
func itBuyDeliveryRef(intentID uuid.UUID, attempts int) string {
	return fmt.Sprintf("ITBD-%s-%d", strings.ReplaceAll(intentID.String(), "-", ""), attempts)
}

// DeliverStableBuy sends the purchased stablecoin to the user's own address.
//
// This is the one irreversible step in the flow, which is why the (chain,
// asset, address) triple was validated at intent creation and the address was
// never typed by the user. Reaching here means the conversion already settled
// SUCCESSFUL, so the asset provably exists in our balance.
func (s *ITService) DeliverStableBuy(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.Status == models.ITStatusSettled {
		return nil
	}
	if intent.Status != models.ITStatusDelivering {
		return ErrITBadState
	}
	// Already sent — never send twice.
	if intent.DeliveryID != "" {
		return nil
	}
	if intent.DeliveryAttempts >= maxStableDeliveryAttempts {
		return ErrStableDeliveryExhausted
	}
	if s.stableDeliverer == nil {
		return errors.New("it stable buy: no deliverer configured")
	}

	// Re-validate immediately before the irreversible call. The matrix is
	// config-adjacent and an intent may have been created under an older one.
	if err := ValidateBuyDelivery(intent.DestChain, intent.Asset, intent.DestAddress); err != nil {
		return err
	}

	units, ok := new(big.Int).SetString(strings.TrimSpace(intent.AssetAmount), 10)
	if !ok || units.Sign() <= 0 {
		return errors.New("it stable buy: intent has no asset amount")
	}
	amountMajor, _ := new(big.Rat).SetFrac(units,
		new(big.Int).Exp(big.NewInt(10), big.NewInt(StableAssetDecimals), nil)).Float64()

	// Prove we hold the stablecoin before spending it — the same gate
	// ConvertStableBuy applies to the naira, and for the same reason. This is
	// the step that actually moves crypto out, so skipping it here would be
	// the one place the never-advance-before-funds-are-present invariant is
	// dropped.
	//
	// AssetAmount came from the /transfers/rates PREVIEW at settlement;
	// ConvertBalance later executes at its own rate minus a conversion fee the
	// spec itself calls uncalibrated. If execution lands worse than the preview
	// by more than the spread, this would ask Flutterwave to send stablecoin we
	// do not hold: rejected transfer, delivery_attempts burning to the cap, and
	// a permanently stuck intent.
	if s.stableConverter == nil {
		return errors.New("it stable buy: no balance reader configured")
	}
	balances, err := s.stableConverter.FetchBalances(ctx)
	if err != nil {
		return fmt.Errorf("it stable buy: %w", err)
	}
	var available float64
	found := false
	for _, b := range balances {
		if strings.EqualFold(b.Currency, intent.Asset) {
			available, found = b.AvailableBalance, true
			break
		}
	}
	if !found {
		log.Printf("[IT] it_stable_balance_line_missing asset=%s intent=%s", intent.Asset, intent.ID)
	}
	if !found || available < amountMajor {
		// ErrStableCreditNotVisible, so the watcher treats it as a wait rather
		// than a failure and does NOT burn a delivery attempt.
		return fmt.Errorf("%w: %s available=%v needed=%v",
			ErrStableCreditNotVisible, intent.Asset, available, amountMajor)
	}

	ref := itBuyDeliveryRef(intent.ID, intent.DeliveryAttempts)
	tr, err := s.stableDeliverer.DirectTransfer(ctx, FWDirectTransferRequest{
		Action:    "instant",
		Type:      "crypto",
		Reference: ref,
		Narration: "TSA Connect stablecoin purchase",
		PaymentInstruction: FWPaymentInstruction{
			SourceCurrency:      intent.Asset,
			DestinationCurrency: intent.Asset,
			Amount:              FWAmount{Value: amountMajor, AppliesTo: "source_currency"},
			Recipient: FWTransferRecipient{
				Crypto: &FWRecipientCrypto{
					Network: intent.DestChain,
					Address: intent.DestAddress,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("it stable buy deliver: %w", err)
	}
	return s.db.WithContext(ctx).Model(intent).
		Update("delivery_id", tr.ID).Error
}

// FinalizeBuyDelivery settles a buy once the outbound transfer completes.
func (s *ITService) FinalizeBuyDelivery(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.Status == models.ITStatusSettled {
		return nil
	}
	if intent.Status != models.ITStatusDelivering || intent.DeliveryID == "" {
		return ErrITBadState
	}
	if s.stableTransfers == nil {
		return errors.New("it stable buy: no transfer reader configured")
	}

	tr, err := s.stableTransfers.GetTransfer(ctx, intent.DeliveryID)
	if err != nil {
		return fmt.Errorf("it stable buy: %w", err)
	}
	switch strings.ToUpper(tr.Status) {
	case "SUCCESSFUL":
		return s.db.WithContext(ctx).Model(intent).
			Update("status", models.ITStatusSettled).Error
	case "FAILED":
		// Clear the id and bump the counter so the retry mints a fresh
		// reference instead of colliding with Flutterwave's idempotency on the
		// transfer that just failed. Stays in `delivering`: the asset is in our
		// balance and the user has not been paid.
		return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
			"failure_reason":    "delivery failed: " + tr.ID,
			"delivery_id":       "",
			"delivery_attempts": intent.DeliveryAttempts + 1,
		}).Error
	default:
		return fmt.Errorf("%w: delivery %s is %s", ErrStableDeliveryPending, tr.ID, tr.Status)
	}
}

// PendingStableBuys lists brokered buy intents that can still advance.
//
// ITStatusBuyAwaitingRate is deliberately excluded: it is a park set only when
// the money landed but could not be priced, and only an operator
// (RepriceParkedStableBuy) may resume it — auto-advancing it here would defeat
// the park. ITStatusCancelled is terminal and likewise excluded.
func (s *ITService) PendingStableBuys(ctx context.Context) (received, awaiting, converting, delivering []uuid.UUID, err error) {
	type row struct {
		ID     uuid.UUID
		Status string
	}
	var rows []row
	err = s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Select("id", "status").
		Where("direction = ? AND asset IN ? AND status IN ?",
			models.ITDirectionBuy,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT},
			[]string{
				models.ITStatusPaymentReceived,
				models.ITStatusAwaitingFunds,
				models.ITStatusConverting,
				models.ITStatusDelivering,
			}).
		Scan(&rows).Error
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for _, r := range rows {
		switch r.Status {
		case models.ITStatusPaymentReceived:
			received = append(received, r.ID)
		case models.ITStatusAwaitingFunds:
			awaiting = append(awaiting, r.ID)
		case models.ITStatusConverting:
			converting = append(converting, r.ID)
		case models.ITStatusDelivering:
			delivering = append(delivering, r.ID)
		}
	}
	return received, awaiting, converting, delivering, nil
}
