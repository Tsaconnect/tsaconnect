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
	"gorm.io/gorm"
)

var (
	ErrStableNoDepositAddress = errors.New("it stable: no Flutterwave deposit address configured")
	ErrStableQuoteExpired     = errors.New("it stable: quote has expired")
	ErrStableNoDestination    = errors.New("it stable: a payout destination is required")
	// ErrStableIntentNotFound covers both "no such intent" and "this intent
	// belongs to someone else" — deliberately the same error and message for
	// both, so a caller probing IDs cannot distinguish a missing intent from
	// one they don't own.
	ErrStableIntentNotFound = errors.New("it stable: sell intent not found")
	// ErrStableDepositAlreadyClaimed means this on-chain deposit transaction is
	// already attached to a (possibly different) intent. The chain proves a
	// transfer happened, not who sent it — Flutterwave exposes one shared
	// merchant address — so a deposit hash may fund at most one intent.
	ErrStableDepositAlreadyClaimed = errors.New("it stable: this deposit has already been reported against a sell")
	// ErrStableOverDelivered means far more arrived than the intent quoted.
	// Not the trade the user was priced for, so it is parked for an operator
	// rather than paid out — see the ceiling in ConfirmStableDeposit.
	ErrStableOverDelivered = errors.New("it stable: deposit exceeds the quoted amount by more than the allowed tolerance")
	// ErrStableNoTokenAddress means no token contract is configured for the
	// asset being sold. CreateStableSellIntent must fail closed on this rather
	// than hand out an instruction set with an empty tokenAddress — see the
	// field doc on StableDepositInstructions.TokenAddress.
	ErrStableNoTokenAddress = errors.New("it stable: no token address configured for asset")
)

// defaultStableOverDeliveryTolBps is how far above the quoted amount a deposit
// may land and still be paid out automatically. Deliberately tight: a bridge
// or a provider never delivers MORE than it was given, so any material excess
// is a mis-sized report rather than ordinary slippage. 200 bps leaves room for
// a rounding artefact without leaving room for an attributed windfall.
const defaultStableOverDeliveryTolBps = 200

// SetStableOverDeliveryToleranceBps bounds how far a deposit may exceed the
// quoted amount. Non-positive restores the default.
func (s *ITService) SetStableOverDeliveryToleranceBps(bps int64) { s.stableOverDeliverBps = bps }

// stableOverDeliveryCeiling is the largest received amount that may still be
// paid out against a quote of `quoted`, or nil when there is no usable quote.
func (s *ITService) stableOverDeliveryCeiling(quoted *big.Int) *big.Int {
	if quoted == nil || quoted.Sign() <= 0 {
		return nil
	}
	bps := s.stableOverDeliverBps
	if bps <= 0 {
		bps = defaultStableOverDeliveryTolBps
	}
	c := new(big.Int).Mul(quoted, big.NewInt(10000+bps))
	return c.Quo(c, big.NewInt(10000))
}

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
	Chain   string `json:"chain"`
	Address string `json:"address"`
	Asset   string `json:"asset"`
	// TokenAddress is the exact ERC-20 contract ConfirmStableDeposit will
	// verify the transfer against (s.stableTokens[asset]) — never left for the
	// app to resolve itself. Polygon carries two live USDC contracts (native
	// and bridged USDC.e); an app-side symbol lookup can silently pick the
	// wrong one, and a deposit sent to it will never match on-chain, leaving
	// the user's funds parked at Flutterwave with nothing to reconcile against.
	TokenAddress    string   `json:"tokenAddress"`
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
		ID:          uuid.New(),
		UserID:      userID,
		Direction:   models.ITDirectionSell,
		Status:      models.ITStatusPendingDeposit,
		Asset:       q.Asset,
		AssetAmount: q.AssetAmount.String(),
		Currency:    q.Currency,
		SourceChain: strings.ToUpper(strings.TrimSpace(sourceChain)),

		// Persisted so an abandoned sell is resumable via GET /it/intents/:id
		// without minting a second intent for the same deposit — see the
		// field doc on models.ITIntent.DepositAddress.
		DepositAddress: addr,

		// Every numeric column must carry a value, not "". PostgreSQL rejects
		// `''::numeric` outright (SQLSTATE 22P02) and GORM sends any field
		// without a column `default:` tag in the INSERT whether or not it was
		// set — so an unset numeric string is an empty string on the wire and
		// the whole INSERT fails. SQLite's type affinity accepts it silently,
		// which is why the test fixtures never saw this. The values below are
		// the correct ones, not filler:
		//
		//	McgpAmount       a stablecoin sell has no MCGP leg at all
		//	FeeNgnKobo       the fee genuinely deducted by the quote — writing
		//	                 "0" here would make the ledger understate what the
		//	                 user was charged
		//	PlatformKeptKobo the platform's margin on a brokered trade is the
		//	                 rate spread realised inside Flutterwave's own
		//	                 balances, never a carve out of this intent
		//	ReceivedAmount   not known until the deposit is verified on-chain;
		//	                 ConfirmStableDeposit overwrites it
		//
		// BeforeSave mirrors the *Kobo columns into their *Minor twins, so
		// setting these three also fills FeeMinor/NetMinor/PlatformKeptMinor.
		McgpAmount:       "0",
		NgnKobo:          q.GrossMinor.String(),
		NetNgnKobo:       q.NetMinor.String(),
		FeeNgnKobo:       stableMinorOrZero(q.FeeMinor),
		PlatformKeptKobo: "0",
		ReceivedAmount:   "0",
		RateKobo:         q.RatePerUnitMinor.String(),
		LockedRate:       q.RatePerUnitMinor.String(),
		RateExpiresAt:    &expires,

		BankCode:            strings.TrimSpace(bank.Code),
		AccountNumber:       strings.TrimSpace(bank.Number),
		AccountName:         strings.TrimSpace(bank.Name),
		DestinationCurrency: strings.TrimSpace(bank.DestinationCurrency),
		RailType:            strings.TrimSpace(bank.RailType),
		MobileNetwork:       strings.TrimSpace(bank.MobileNetwork),
		MobileCountry:       strings.TrimSpace(bank.MobileCountry),
		MobileMsisdn:        strings.TrimSpace(bank.MobileMsisdn),
	}

	// Resolve the SAME token contract ConfirmStableDeposit will verify the
	// arrival against — mirroring its lookup exactly, asset-key normalisation
	// included, so the app is handed the one contract that can ever confirm.
	// Fail closed rather than create the intent: handing out a deposit
	// instruction with no contract is worse than refusing the sell, and a
	// caller who never sees the intent ID cannot mistake this for "reported,
	// pending" (see ErrStableNoTokenAddress).
	asset := models.ITIntentAsset(intent)
	token := s.stableTokens[asset]
	if token == "" {
		return nil, nil, fmt.Errorf("%w: %s", ErrStableNoTokenAddress, asset)
	}

	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, nil, err
	}

	return intent, &StableDepositInstructions{
		Chain:           stableDepositChain,
		Address:         addr,
		Asset:           q.Asset,
		TokenAddress:    token,
		AmountBaseUnits: q.AssetAmount,
		AmountDisplay:   fmt.Sprintf("%.6f", StableFromBaseUnits(q.AssetAmount)),
	}, nil
}

// stableMinorOrZero renders a minor-unit figure for a numeric column, mapping
// a nil (fee engine dormant, or a test fixture that carries no fee line) to
// "0" rather than "" — see the numeric-column note in CreateStableSellIntent.
func stableMinorOrZero(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
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

// getIntentForUser fetches an intent scoped to its owner. It returns
// ErrStableIntentNotFound both when the id doesn't exist at all and when it
// belongs to a different user — the two cases must be indistinguishable to
// the caller, or the endpoint becomes an oracle for "does this intent id
// belong to someone" (IDOR).
func (s *ITService) getIntentForUser(ctx context.Context, userID, intentID uuid.UUID) (*models.ITIntent, error) {
	var i models.ITIntent
	err := s.db.WithContext(ctx).First(&i, "id = ? AND user_id = ?", intentID, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrStableIntentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

// ReportStableDeposit records the source transaction the user broadcast.
//
// Scoped to userID — the caller must own the intent, otherwise
// ErrStableIntentNotFound (never leaking that a differently-owned intent
// exists). This is the ownership check that stops one user attaching their
// deposit report to someone else's sell intent (IDOR).
//
// Idempotent: the app may retry, and a repeat report of the same hash is a
// no-op rather than a state reset.
//
// A given on-chain deposit tx can fund at most one intent — enforced by the
// DB's partial unique index on deposit_tx_hash (config/database.go), because
// the chain only proves a transfer happened, not who sent it (Flutterwave's
// deposit address is shared across every user). A collision surfaces as
// ErrStableDepositAlreadyClaimed rather than a raw DB error.
func (s *ITService) ReportStableDeposit(ctx context.Context, userID, intentID uuid.UUID, srcTxHash string) error {
	srcTxHash = strings.TrimSpace(srcTxHash)
	if srcTxHash == "" {
		return errors.New("it stable: empty deposit transaction hash")
	}
	intent, err := s.getIntentForUser(ctx, userID, intentID)
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
		if isUniqueViolation(res.Error) {
			return ErrStableDepositAlreadyClaimed
		}
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Lost the race. If the winner landed the row exactly where this call
		// was trying to land it, that is a success from the caller's point of
		// view — same idempotent contract as the early-return above.
		reloaded, rErr := s.getIntentForUser(ctx, userID, intentID)
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
	if s.stableChain == nil {
		return errors.New("it stable: deposit verification is not configured")
	}

	// Resolve the SETTLED (Polygon) transaction that actually delivered.
	//
	// A user already on Polygon does exactly what StableDepositInstructions
	// tells them: a plain ERC-20 transfer to Flutterwave's address. LiFi never
	// routed that transaction and has nothing to say about it — asking would
	// return a non-2xx and pin the intent in deposit_reported forever with the
	// funds already at Flutterwave. For a same-chain send the source tx IS the
	// settled tx, and the on-chain receipt below is the authority either way;
	// LiFi is only ever the LOOKUP for a cross-chain hop, never the proof.
	settledTx := strings.TrimSpace(intent.DepositTxHash)
	if !strings.EqualFold(strings.TrimSpace(intent.SourceChain), stableDepositChain) {
		if s.stableLiFi == nil {
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
		settledTx = status.DestTxHash
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

	received, err := s.stableChain.VerifyTransfer(ctx, settledTx, token, fwAddr)
	if err != nil {
		return fmt.Errorf("it stable: could not verify %s on-chain: %w", settledTx, err)
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

	// Bound the scaling ABOVE. Scaling down is the case this design cares
	// about — a bridge delivers less than it was given — but the ratio is
	// symmetric and nothing else constrains the other direction.
	//
	// Two things go wrong without a ceiling. The rate lock's drift exposure was
	// sized against the quoted amount, and it scales linearly with the
	// over-delivery. Worse, creating an intent escrows nothing, so an unbounded
	// ratio removes the cost ceiling on the known deposit-attribution risk: a
	// 10 USDC quote could otherwise claim a 50,000 USDC deposit and be paid on
	// all of it at the attacker's own locked rate.
	//
	// Fail CLOSED rather than capping the payout at the quoted amount — a cap
	// would keep the excess, which is the platform taking a user's money. The
	// intent stays in deposit_reported, which ListStuckStableSells surfaces, so
	// an operator resolves it against the on-chain evidence by hand. Nothing is
	// lost: the funds are provably at Flutterwave either way.
	if over := s.stableOverDeliveryCeiling(quoted); over != nil && received.Cmp(over) > 0 {
		log.Printf("[IT] it_stable_over_delivery intent=%s quoted=%s received=%s ceiling=%s",
			intent.ID, quoted, received, over)
		return fmt.Errorf("%w: quoted %s, received %s", ErrStableOverDelivered, quoted, received)
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
			"settled_tx_hash": settledTx,
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
// outside 6–42 chars. "ITC-" (4) + the hyphen-less UUID (32) = 36, leaving 6
// spare chars — "-" plus up to 5 digits, i.e. attempt counts through 99999.
// maxStableConversionAttempts caps attempts well before that budget could
// ever bind — a prior "IT-CONV-" prefix left only 2 spare chars and rejected
// references outright at attempt 10; this prefix exists so the cap is the
// only limit that matters.
func itConversionRef(intentID uuid.UUID, attempts int) string {
	return fmt.Sprintf("ITC-%s-%d", strings.ReplaceAll(intentID.String(), "-", ""), attempts)
}

// maxStableConversionAttempts caps how many times ConvertAndQueuePayout will
// retry a stablecoin→fiat conversion for one intent. Without a cap,
// FinalizeConversion's FAILED branch bumps ConversionAttempts forever and the
// intent oscillates between converting and deposit_confirmed indefinitely,
// burning provider calls with no terminal state and no operator alert.
const maxStableConversionAttempts = 10

// ErrStableConversionExhausted means a stablecoin sell's conversion has
// failed maxStableConversionAttempts times. ConvertAndQueuePayout refuses to
// call the provider again once this is hit.
//
// The intent is deliberately left in deposit_confirmed rather than moved to a
// terminal status: ListStuckStableSells filters to non-terminal statuses, so
// marking it terminal would hide the very intent an operator most needs to
// see, right when it needs seeing. It stays in the stuck list — the watcher
// just stops burning provider calls on it.
var ErrStableConversionExhausted = errors.New("it stable: conversion attempts exhausted; needs operator review")

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

	// Refuse before touching the provider at all — no balance check, no
	// claim, no conversion call — once this intent has already burned
	// maxStableConversionAttempts. See ErrStableConversionExhausted.
	if intent.ConversionAttempts >= maxStableConversionAttempts {
		log.Printf("[IT] it_stable_conversion_exhausted intent=%s attempts=%d", intent.ID, intent.ConversionAttempts)
		return fmt.Errorf("%w: intent=%s attempts=%d", ErrStableConversionExhausted, intent.ID, intent.ConversionAttempts)
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
// but not yet converted, those converting but not yet settled, and those whose
// fiat is credited and now await the payout.
//
// `debited` is a bucket here and not a terminal state: for a brokered sell it
// is where the fiat exists but has not yet left for the user's bank. Nothing
// else in the service scans for it — CreateSellIntent's AutoPayout call is
// inline on the MCGP path only — so without this bucket every stablecoin sell
// stops one hop short of the user's money.
func (s *ITService) PendingStableSells(ctx context.Context) (reported, confirmed, converting, debited []uuid.UUID, err error) {
	type row struct {
		ID     uuid.UUID
		Status string
	}
	var rows []row
	err = s.db.WithContext(ctx).
		Model(&models.ITIntent{}).
		Select("id", "status").
		Where("direction = ?", models.ITDirectionSell).
		// Scoped to the stablecoin assets so this never sweeps up an MCGP sell,
		// whose `debited` intents CreateSellIntent has already paid out inline.
		Where("asset IN ?", []string{models.ITAssetUSDC, models.ITAssetUSDT}).
		Where("status IN ?", []string{
			models.ITStatusDepositReported,
			models.ITStatusDepositConfirmed,
			models.ITStatusConverting,
			models.ITStatusDebited,
		}).
		Find(&rows).Error
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for _, r := range rows {
		switch r.Status {
		case models.ITStatusDepositReported:
			reported = append(reported, r.ID)
		case models.ITStatusDepositConfirmed:
			confirmed = append(confirmed, r.ID)
		case models.ITStatusConverting:
			converting = append(converting, r.ID)
		case models.ITStatusDebited:
			debited = append(debited, r.ID)
		}
	}
	return reported, confirmed, converting, debited, nil
}

// PayoutStableSell fires the fiat payout for a brokered sell whose conversion
// has settled.
//
// This is the last hop, and it is driven by the watcher rather than called
// inline from FinalizeConversion on purpose: AutoPayout short-circuits on a
// non-empty PayoutID, so re-running it is free, which makes a dropped call
// self-heal on the next tick instead of stranding a user's converted fiat in
// the platform's own balance. It also matches the polling model every other
// step of this ramp uses.
//
// Guarded exactly as CreateSellIntent's inline call is: no gateway or no
// destination means the operator settles manually (legacy path), which is not
// a failure of this intent.
func (s *ITService) PayoutStableSell(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.Direction != models.ITDirectionSell || intent.Status != models.ITStatusDebited {
		return ErrITBadState
	}
	if intent.PayoutID != "" {
		return nil
	}
	if s.payout == nil || !sellIntentHasPayoutDestination(intent) {
		return nil
	}
	return s.AutoPayout(ctx, intent)
}

// StuckStableSell is a brokered sell that has not advanced, with the evidence
// an operator needs to resolve it.
type StuckStableSell struct {
	IntentID       uuid.UUID `json:"intentId"`
	UserID         uuid.UUID `json:"userId"`
	Status         string    `json:"status"`
	Asset          string    `json:"asset"`
	DepositTxHash  string    `json:"depositTxHash,omitempty"`
	SettledTxHash  string    `json:"settledTxHash,omitempty"`
	ReceivedAmount string    `json:"receivedAmount,omitempty"`
	// ConversionAttempts lets an operator see "this has retried N times"
	// directly rather than inferring it — a count, not money, so a plain int
	// is correct here (contrast the *Kobo/*Minor string fields above).
	ConversionAttempts int       `json:"conversionAttempts"`
	StuckSince         time.Time `json:"stuckSince"`
}

// ListStuckStableSells reports sells that have sat in a non-terminal state
// longer than olderThan.
//
// A brokered ramp holds no float, so a stall never costs the platform money —
// but it does leave a user's funds parked at a hop they cannot see. The
// transaction hashes are the point of this list: on-chain proof is what
// resolves a deposit a provider has not credited.
func (s *ITService) ListStuckStableSells(ctx context.Context, olderThan time.Duration) ([]StuckStableSell, error) {
	cutoff := time.Now().Add(-olderThan)
	var intents []models.ITIntent
	err := s.db.WithContext(ctx).
		Where("direction = ?", models.ITDirectionSell).
		Where("asset IN ?", []string{models.ITAssetUSDC, models.ITAssetUSDT}).
		Where("status IN ?", []string{
			models.ITStatusPendingDeposit,
			models.ITStatusDepositReported,
			models.ITStatusDepositConfirmed,
			models.ITStatusConverting,
			// `debited` is non-terminal for a brokered sell: the fiat exists
			// but has not reached the user's bank. A sell that sits here past
			// the threshold is one whose payout never fired, which is exactly
			// the stall an operator most needs to see.
			models.ITStatusDebited,
		}).
		Where("updated_at < ?", cutoff).
		Order("updated_at asc").
		Find(&intents).Error
	if err != nil {
		return nil, err
	}
	out := make([]StuckStableSell, 0, len(intents))
	for i := range intents {
		in := &intents[i]
		out = append(out, StuckStableSell{
			IntentID:           in.ID,
			UserID:             in.UserID,
			Status:             in.Status,
			Asset:              models.ITIntentAsset(in),
			DepositTxHash:      in.DepositTxHash,
			SettledTxHash:      in.SettledTxHash,
			ReceivedAmount:     in.ReceivedAmount,
			ConversionAttempts: in.ConversionAttempts,
			StuckSince:         in.UpdatedAt,
		})
	}
	return out, nil
}
