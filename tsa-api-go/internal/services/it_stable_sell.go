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
)

var (
	ErrStableNoDepositAddress = errors.New("it stable: no Flutterwave deposit address configured")
	ErrStableQuoteExpired     = errors.New("it stable: quote has expired")
	ErrStableNoDestination    = errors.New("it stable: a payout destination is required")
)

// stableDepositChain is where every sell deposit is routed. Flutterwave accepts
// inbound stablecoins on Polygon and Ethereum only; Polygon is the cheaper and
// is Flutterwave's own documented default. Defined in terms of feeChainPolygon
// (fee_quote_service.go) rather than a second "polygon" literal, uppercased
// because the rest of the stable ramp (SourceChain, deliveryMatrix) keys chains
// upper-case.
var stableDepositChain = strings.ToUpper(feeChainPolygon)

// StableDepositInstructions tells the app where to route the user's funds.
//
// Address is FLUTTERWAVE'S address, never one of ours: the user's stablecoin
// goes straight from their wallet to the provider, so the platform is never in
// the custody path.
type StableDepositInstructions struct {
	Chain           string   `json:"chain"`
	Address         string   `json:"address"`
	Asset           string   `json:"asset"`
	AmountBaseUnits *big.Int `json:"-"`
	AmountDisplay   string   `json:"amount"`
}

// SetStableDepositAddress records Flutterwave's own deposit address for a
// chain. Flutterwave exposes it on the dashboard only — there is no API to
// fetch it — so it is copied in by configuration.
func (s *ITService) SetStableDepositAddress(chain, address string) {
	if s.stableDepositAddrs == nil {
		s.stableDepositAddrs = map[string]string{}
	}
	s.stableDepositAddrs[strings.ToUpper(strings.TrimSpace(chain))] = strings.TrimSpace(address)
}

func (s *ITService) stableDepositAddress(chain string) (string, error) {
	addr := s.stableDepositAddrs[strings.ToUpper(strings.TrimSpace(chain))]
	if addr == "" {
		return "", fmt.Errorf("%w: %s", ErrStableNoDepositAddress, chain)
	}
	if err := ValidateDeliveryAddress(chain, addr); err != nil {
		return "", fmt.Errorf("configured deposit address is invalid: %w", err)
	}
	return addr, nil
}

// CreateStableSellIntent records a priced sell and returns where the user must
// send their stablecoin.
//
// Nothing is paid out here and nothing is reserved: the payout is funded
// entirely by the user's own funds once they provably arrive at Flutterwave
// (see the watcher). The intent is the ledger of that promise, not a claim on
// any balance of ours.
func (s *ITService) CreateStableSellIntent(
	ctx context.Context,
	userID uuid.UUID,
	q *StableSellQuote,
	sourceChain string,
	bank *ITSellBank,
) (*models.ITIntent, *StableDepositInstructions, error) {
	if q == nil {
		return nil, nil, errors.New("it stable: nil quote")
	}
	if time.Now().After(q.ExpiresAt) {
		return nil, nil, ErrStableQuoteExpired
	}
	if bank == nil || (strings.TrimSpace(bank.Number) == "" && strings.TrimSpace(bank.MobileMsisdn) == "") {
		return nil, nil, ErrStableNoDestination
	}
	addr, err := s.stableDepositAddress(stableDepositChain)
	if err != nil {
		return nil, nil, err
	}

	expires := q.ExpiresAt
	intent := &models.ITIntent{
		ID:            uuid.New(),
		UserID:        userID,
		Direction:     models.ITDirectionSell,
		Status:        models.ITStatusPendingDeposit,
		Asset:         q.Asset,
		AssetAmount:   q.AssetAmount.String(),
		Currency:      q.Currency,
		SourceChain:   strings.ToUpper(strings.TrimSpace(sourceChain)),
		NgnKobo:       q.GrossMinor.String(),
		NetNgnKobo:    q.NetMinor.String(),
		RateKobo:      q.RatePerUnitMinor.String(),
		LockedRate:    q.RatePerUnitMinor.String(),
		RateExpiresAt: &expires,

		BankCode:            strings.TrimSpace(bank.Code),
		AccountNumber:       strings.TrimSpace(bank.Number),
		AccountName:         strings.TrimSpace(bank.Name),
		DestinationCurrency: strings.TrimSpace(bank.DestinationCurrency),
		RailType:            strings.TrimSpace(bank.RailType),
		MobileNetwork:       strings.TrimSpace(bank.MobileNetwork),
		MobileCountry:       strings.TrimSpace(bank.MobileCountry),
		MobileMsisdn:        strings.TrimSpace(bank.MobileMsisdn),
	}
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, nil, err
	}

	return intent, &StableDepositInstructions{
		Chain:           stableDepositChain,
		Address:         addr,
		Asset:           q.Asset,
		AmountBaseUnits: q.AssetAmount,
		AmountDisplay:   fmt.Sprintf("%.6f", StableFromBaseUnits(q.AssetAmount)),
	}, nil
}

// SetStableVerifier wires the two independent sources used to confirm that a
// user's sell deposit reached Flutterwave: LiFi to FIND the destination
// transaction, and an on-chain verifier to PROVE it moved the funds.
// tokenAddrs maps asset symbol → the token contract on the deposit chain.
func (s *ITService) SetStableVerifier(lifi *LiFiStatusClient, chain ERC20TransferVerifier, tokenAddrs map[string]string) {
	s.stableLiFi = lifi
	s.stableChain = chain
	s.stableTokens = tokenAddrs
}

// ReportStableDeposit records the source transaction the user broadcast.
//
// Idempotent: the app may retry, and a repeat report of the same hash is a
// no-op rather than a state reset.
func (s *ITService) ReportStableDeposit(ctx context.Context, intentID uuid.UUID, srcTxHash string) error {
	srcTxHash = strings.TrimSpace(srcTxHash)
	if srcTxHash == "" {
		return errors.New("it stable: empty deposit transaction hash")
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.DepositTxHash == srcTxHash && intent.Status != models.ITStatusPendingDeposit {
		return nil // already reported
	}
	if intent.Status != models.ITStatusPendingDeposit && intent.Status != models.ITStatusDepositReported {
		return ErrITBadState
	}

	// Guard the write with the status this read just observed: a concurrent
	// report (the app retrying while a redelivered webhook lands at the same
	// moment) can otherwise both pass the checks above and both write. The
	// predicate makes only one of them actually move the row — the loser's
	// write affects zero rows instead of racing the winner's.
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ?", intent.ID, intent.Status).
		Updates(map[string]any{
			"deposit_tx_hash": srcTxHash,
			"status":          models.ITStatusDepositReported,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Lost the race. If the winner landed the row exactly where this call
		// was trying to land it, that is a success from the caller's point of
		// view — same idempotent contract as the early-return above.
		reloaded, rErr := s.getIntent(ctx, intentID)
		if rErr != nil {
			return rErr
		}
		if reloaded.DepositTxHash == srcTxHash && reloaded.Status == models.ITStatusDepositReported {
			return nil
		}
		return ErrITBadState
	}
	return nil
}

// ConfirmStableDeposit proves the user's funds reached Flutterwave and prices
// the payout from what actually arrived.
//
// Two independent sources must agree before anything advances. LiFi resolves
// the source transaction to a destination transaction; the chain receipt then
// proves that transaction credited Flutterwave's address with the right token.
// A provider's claim alone never moves money.
//
// The payout is computed at the LOCKED rate on the RECEIVED amount. Both halves
// matter: the rate is locked because the user has already irreversibly
// broadcast and cannot walk away from a reprice, and the amount is the received
// one because a bridge delivers less than it was given — quoting the payout off
// the original amount would disburse money the platform never received.
func (s *ITService) ConfirmStableDeposit(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.Status == models.ITStatusDepositConfirmed ||
		intent.Status == models.ITStatusConverting ||
		intent.Status == models.ITStatusDebited ||
		intent.Status == models.ITStatusSettled {
		return nil // already confirmed; webhooks and ticks both redeliver
	}
	if intent.Status != models.ITStatusDepositReported {
		return ErrITBadState
	}
	if s.stableLiFi == nil || s.stableChain == nil {
		return errors.New("it stable: deposit verification is not configured")
	}

	status, err := s.stableLiFi.Status(ctx, intent.DepositTxHash)
	if err != nil {
		return err
	}
	switch status.Status {
	case LiFiStatusDone:
	case LiFiStatusFailed:
		return fmt.Errorf("%w: %s", ErrLiFiFailed, intent.DepositTxHash)
	default:
		return fmt.Errorf("%w: %s", ErrLiFiPending, intent.DepositTxHash)
	}

	asset := models.ITIntentAsset(intent)
	token := s.stableTokens[asset]
	if token == "" {
		return fmt.Errorf("it stable: no token address configured for %s", asset)
	}
	fwAddr, err := s.stableDepositAddress(stableDepositChain)
	if err != nil {
		return err
	}

	received, err := s.stableChain.VerifyTransfer(ctx, status.DestTxHash, token, fwAddr)
	if err != nil {
		return fmt.Errorf("it stable: could not verify %s on-chain: %w", status.DestTxHash, err)
	}
	if received.Sign() <= 0 {
		return fmt.Errorf("%w: zero credited", ErrTransferNotFound)
	}

	// Scale the quoted net by how much actually arrived.
	//
	// Scaling rather than recomputing from the locked rate is deliberate: the
	// quoted net already has the itemised fee line deducted, and recomputing
	// rate × received would silently hand that fee back. Scaling keeps every
	// component of the quote proportional to what the user actually delivered.
	quoted, ok := new(big.Int).SetString(intent.AssetAmount, 10)
	if !ok || quoted.Sign() <= 0 {
		return errors.New("it stable: intent has no usable quoted amount")
	}
	quotedNet, ok := new(big.Int).SetString(intent.NetNgnKobo, 10)
	if !ok || quotedNet.Sign() <= 0 {
		return errors.New("it stable: intent has no usable quoted net")
	}
	net := new(big.Int).Quo(new(big.Int).Mul(quotedNet, received), quoted)
	if net.Sign() <= 0 {
		return fmt.Errorf("it stable: received amount %s leaves nothing to pay out", received)
	}

	// Re-guard the write with a status predicate rather than claiming into an
	// in-progress state up front: the verification work above (LiFi + chain
	// RPC) has to run before we know `received`/`net`, so there is nothing
	// meaningful to claim into before then without adding a new intermediate
	// status. A concurrent confirm (webhook + poller landing together) can
	// still both do that work, but verification is deterministic, so they'd
	// compute the same figures — this predicate is what stops them from both
	// COMMITTING, which is what matters: it keeps the deposit_reported →
	// deposit_confirmed transition single-writer, so ConvertAndQueuePayout's
	// own claim() (it_stable_sell.go) never sees a double-fired trigger.
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ?", intent.ID, models.ITStatusDepositReported).
		Updates(map[string]any{
			"settled_tx_hash": status.DestTxHash,
			"received_amount": received.String(),
			"net_ngn_kobo":    net.String(),
			"status":          models.ITStatusDepositConfirmed,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Lost the race to a concurrent confirm. Re-check rather than surface
		// a spurious failure: if the winner already advanced the intent past
		// deposit_reported, this call is the same idempotent no-op the
		// early-return at the top of this method already grants.
		reloaded, rErr := s.getIntent(ctx, intentID)
		if rErr != nil {
			return rErr
		}
		switch reloaded.Status {
		case models.ITStatusDepositConfirmed, models.ITStatusConverting, models.ITStatusDebited, models.ITStatusSettled:
			return nil
		default:
			return ErrITBadState
		}
	}
	return nil
}

// ErrStableCreditNotVisible means the funds are proven on-chain but Flutterwave
// has not yet credited them. This is a wait, not a failure — the next tick
// retries.
var ErrStableCreditNotVisible = errors.New("it stable: Flutterwave has not credited the deposit yet")

// ErrStableConversionPending means the stablecoin→fiat conversion has been
// accepted but has not settled. A wait, not a failure — the next tick retries.
var ErrStableConversionPending = errors.New("it stable: conversion has not settled yet")

// stableConverter is the slice of the Flutterwave client this path needs.
type stableConverter interface {
	FetchBalances(ctx context.Context) ([]FWBalance, error)
	ConvertBalance(ctx context.Context, merchantID, sourceCurrency, destCurrency string, amount FWAmount, reference string) (FWTransfer, error)
}

// stableTransferReader reads back a transfer's settled status. Conversion is
// asynchronous — accepted as NEW, settled later — so this is what tells us the
// fiat has actually been credited.
type stableTransferReader interface {
	GetTransfer(ctx context.Context, id string) (FWTransfer, error)
}

// SetStableConverter wires the Flutterwave client used to turn a credited
// stablecoin balance into fiat.
func (s *ITService) SetStableConverter(c stableConverter, merchantID string) {
	s.stableConverter = c
	s.stableMerchantID = merchantID
	// The Flutterwave client satisfies both interfaces; keep them separate so
	// tests can stub the settled-status read independently.
	if tr, ok := c.(stableTransferReader); ok {
		s.stableTransfers = tr
	}
}

// SetStableTransfers overrides the settled-status reader (tests; the client
// normally supplies it via SetStableConverter).
func (s *ITService) SetStableTransfers(r stableTransferReader) { s.stableTransfers = r }

// itConversionRef builds the provider transfer reference for a stablecoin→fiat
// conversion.
//
// The reference doubles as Flutterwave's idempotency key (flutterwave_client.go),
// which is exactly what a network-level retry needs — but it is exactly what
// breaks a DELIBERATE retry after FinalizeConversion drops a FAILED conversion
// back to deposit_confirmed: reusing the same reference would make Flutterwave
// hand back the same stale FAILED transfer forever, and the self-heal path
// could never actually heal. Folding ConversionAttempts into the reference
// keeps it stable within one attempt (so redelivered ticks still hit the same
// transfer) while making a genuinely new attempt mint a genuinely new one.
//
// Mirrors itPayoutRef's length accounting: Flutterwave rejects references
// outside 6–42 chars. "IT-CONV-" (8) + the hyphen-less UUID (32) leaves 2
// spare chars, enough for "-N" up to a single-digit attempt count — a
// conversion failing and retrying ten times without operator intervention
// would itself be the incident to investigate, so this budget is not expected
// to bind in practice.
func itConversionRef(intentID uuid.UUID, attempts int) string {
	return fmt.Sprintf("IT-CONV-%s-%d", strings.ReplaceAll(intentID.String(), "-", ""), attempts)
}

// ConvertAndQueuePayout converts the user's credited stablecoin to fiat and
// hands the intent to the existing payout path.
//
// The balance check is the brokerage guarantee in code: money moves only once
// Flutterwave shows it actually holds the user's funds. On-chain arrival proves
// the funds left the user, not that the provider credited them, and converting
// on the strength of the former would spend a balance that belongs to someone
// else's trade.
//
// The intent lands on the EXISTING `debited` status, which is what AutoPayout
// and ResolvePayoutFromWebhook already understand — the entire payout and
// settlement half of this feature is code that already exists and is unchanged.
func (s *ITService) ConvertAndQueuePayout(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	switch intent.Status {
	case models.ITStatusConverting, models.ITStatusDebited, models.ITStatusSettled, models.ITStatusReversed:
		return nil // conversion already initiated or done; ticks redeliver
	case models.ITStatusDepositConfirmed:
	default:
		return ErrITBadState
	}
	if s.stableConverter == nil {
		return errors.New("it stable: no Flutterwave converter configured")
	}

	received, ok := new(big.Int).SetString(intent.ReceivedAmount, 10)
	if !ok || received.Sign() <= 0 {
		return errors.New("it stable: intent has no verified received amount")
	}
	asset := models.ITIntentAsset(intent)
	currency := models.ITIntentCurrency(intent)

	balances, err := s.stableConverter.FetchBalances(ctx)
	if err != nil {
		return fmt.Errorf("it stable: %w", err)
	}
	var available float64
	found := false
	for _, b := range balances {
		if strings.EqualFold(b.Currency, asset) {
			available = b.AvailableBalance
			found = true
			break
		}
	}
	needed := StableFromBaseUnits(received)
	if !found {
		// Flutterwave's balances response is not surfacing a line for this
		// asset at all — distinct from the ordinary "not credited yet" wait,
		// because it can mean the provider integration itself is missing the
		// asset rather than the deposit simply being in flight. Both cases
		// fail the same way (ErrStableCreditNotVisible, retried by the next
		// tick) — this log only makes the two distinguishable operationally.
		log.Printf("[IT] it_stable_balance_line_missing asset=%s intent=%s", asset, intent.ID)
	} else if available < needed {
		log.Printf("[IT] it_stable_credit_not_visible asset=%s intent=%s available=%v needed=%v", asset, intent.ID, available, needed)
	}
	if available < needed {
		return fmt.Errorf("%w: %s available=%v needed=%v",
			ErrStableCreditNotVisible, asset, available, needed)
	}

	// Claim the intent before spending, so two concurrent ticks cannot both
	// convert. A double conversion moves real money twice.
	claimed, err := s.claim(ctx, intent.ID, []string{models.ITStatusDepositConfirmed}, models.ITStatusConverting)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	tr, err := s.stableConverter.ConvertBalance(
		ctx,
		s.stableMerchantID,
		asset, currency,
		FWAmount{Value: StableFromBaseUnits(received), AppliesTo: "source_currency"},
		itConversionRef(intent.ID, intent.ConversionAttempts),
	)
	if err != nil {
		// Release the claim so the next tick retries rather than stranding it.
		_ = s.db.WithContext(ctx).Model(intent).
			Update("status", models.ITStatusDepositConfirmed).Error
		return fmt.Errorf("it stable: conversion failed: %w", err)
	}

	// Stop at `converting` — NOT `debited`.
	//
	// ConvertBalance returns as soon as Flutterwave ACCEPTS the conversion
	// (status NEW); the fiat is credited later. `debited` is what makes
	// AutoPayout fire, so advancing here would disburse fiat that does not
	// exist yet. FinalizeConversion promotes the intent once the conversion's
	// own transfer actually reaches SUCCESSFUL.
	return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
		"conversion_id": tr.ID,
		"status":        models.ITStatusConverting,
	}).Error
}

// FinalizeConversion promotes a converting intent to `debited` once
// Flutterwave reports the stablecoin→fiat conversion actually settled.
//
// This is the gate that keeps the brokerage honest at its last hop: the payout
// path may not run until the fiat the payout spends has genuinely been
// credited. A FAILED conversion drops the intent back to deposit_confirmed so
// the next tick retries — the user's stablecoin is still at Flutterwave, so
// retrying is safe and is the correct response to a transient provider failure.
func (s *ITService) FinalizeConversion(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	switch intent.Status {
	case models.ITStatusDebited, models.ITStatusSettled, models.ITStatusReversed:
		return nil
	case models.ITStatusConverting:
	default:
		return ErrITBadState
	}
	if intent.ConversionID == "" {
		return errors.New("it stable: converting intent has no conversion id")
	}
	if s.stableTransfers == nil {
		return errors.New("it stable: no transfer reader configured")
	}

	tr, err := s.stableTransfers.GetTransfer(ctx, intent.ConversionID)
	if err != nil {
		return fmt.Errorf("it stable: %w", err)
	}
	switch strings.ToUpper(tr.Status) {
	case "SUCCESSFUL":
		return s.db.WithContext(ctx).Model(intent).
			Update("status", models.ITStatusDebited).Error
	case "FAILED":
		// Bump the attempt counter alongside the reset so the NEXT
		// ConvertAndQueuePayout call mints a fresh reference (itConversionRef)
		// instead of reusing the one that just resolved to this FAILED
		// transfer — otherwise the retry would hit Flutterwave's idempotency
		// key, get the same stale FAILED transfer back, and loop forever.
		return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
			"status":              models.ITStatusDepositConfirmed,
			"failure_reason":      "conversion failed: " + tr.ID,
			"conversion_id":       "",
			"conversion_attempts": intent.ConversionAttempts + 1,
		}).Error
	default:
		return fmt.Errorf("%w: conversion %s is %s", ErrStableConversionPending, tr.ID, tr.Status)
	}
}

// PendingStableSells lists brokered sell intents that can still advance:
// those whose deposit was reported but not yet confirmed, those confirmed
// but not yet converted, and those converting but not yet settled.
func (s *ITService) PendingStableSells(ctx context.Context) (reported, confirmed, converting []uuid.UUID, err error) {
	type row struct {
		ID     uuid.UUID
		Status string
	}
	var rows []row
	err = s.db.WithContext(ctx).
		Model(&models.ITIntent{}).
		Select("id", "status").
		Where("direction = ?", models.ITDirectionSell).
		Where("asset IN ?", []string{models.ITAssetUSDC, models.ITAssetUSDT}).
		Where("status IN ?", []string{
			models.ITStatusDepositReported,
			models.ITStatusDepositConfirmed,
			models.ITStatusConverting,
		}).
		Find(&rows).Error
	if err != nil {
		return nil, nil, nil, err
	}
	for _, r := range rows {
		switch r.Status {
		case models.ITStatusDepositReported:
			reported = append(reported, r.ID)
		case models.ITStatusDepositConfirmed:
			confirmed = append(confirmed, r.ID)
		case models.ITStatusConverting:
			converting = append(converting, r.ID)
		}
	}
	return reported, confirmed, converting, nil
}
