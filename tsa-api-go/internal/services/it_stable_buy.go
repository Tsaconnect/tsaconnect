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

var (
	// ErrStableBuyAlreadyOpen enforces one purchase awaiting payment per user.
	// The standing VA's reference identifies the USER, not the purchase, so a
	// second intent in pending_payment would make an incoming charge ambiguous.
	//
	// It holds in central mode for the same reason, arrived at differently:
	// there the key is the payer's name plus the amount, so two open purchases
	// by one user are only distinguishable while their amounts differ — and the
	// matcher holds on any contention rather than picking. Keeping the rule is
	// cheaper than explaining a hold to the one user it would affect.
	ErrStableBuyAlreadyOpen = errors.New("it stable buy: this user already has a purchase awaiting payment")
	// ErrStableBuyNoVA means the user has no standing virtual account yet.
	//
	// VA MODE ONLY. Central mode never looks a virtual account up, so this is
	// not reachable there — see payIntoForStableBuy.
	ErrStableBuyNoVA = errors.New("it stable buy: user has no virtual account; activate one first")
	// ErrStableBuyNoWallet means the user has no wallet to receive into.
	ErrStableBuyNoWallet = errors.New("it stable buy: user has no wallet")
	// ErrStableBuyWrongRamp means this currency's stablecoin ramp is served by
	// another provider, or by none. The registry is the authority on that and
	// the app already reads it; this is the same answer enforced server-side,
	// where an out-of-date client cannot route around it.
	//
	// Worded for a reader, not a maintainer: the handler returns err.Error()
	// verbatim and only an out-of-date app can reach this, so its text lands on
	// a buyer's screen. Which provider actually serves the currency goes to the
	// log instead — it is not something a buyer can act on.
	ErrStableBuyWrongRamp = errors.New("buying stablecoins with this currency has moved to a new provider — update the app to continue")
)

// HasStableBuy reports whether everything the BUY leg needs is actually wired.
//
// Deliberately NOT the sell leg's prerequisites: buy never touches Polygon. It
// needs the Flutterwave converter and merchant id (to turn the user's naira
// into stablecoin), a live rate source (to price what lands) and the deliverer
// (to send the asset on). Taking naira with any of these absent means holding
// money that nothing in the process can fulfil — the charge is claimed, the
// MCGP credit is suppressed, and no watcher exists to convert or deliver.
//
// Every entry point onto the buy path consults this: the quote, intent
// creation, and — the load-bearing one — the webhook fork, which must fall
// through to the MCGP credit rather than claim a charge it cannot fulfil.
func (s *ITService) HasStableBuy() bool {
	return s != nil &&
		s.stableConverter != nil &&
		strings.TrimSpace(s.stableMerchantID) != "" &&
		s.rateQuoter != nil &&
		s.stableDeliverer != nil
}

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
//
// wallet_addresses rows are gated against deliveryMatrix — the same
// single source of truth DeliverableChains uses — and cannot overwrite a
// chain already set from users.wallet_address. Today RegisterNonEVMAddress
// only ever writes non-EVM chain_keys (it gates on ChainRegistry.ForKey), so
// this is latent, not live: but that endpoint is public and user-controlled,
// and without the gate a future EVM entry in the chain registry would let a
// client silently override the server-held EVM address with one it typed in
// a request body.
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
		addr := strings.TrimSpace(r.Address)
		if addr == "" || key == "" {
			continue
		}
		if _, deliverable := deliveryMatrix[key]; !deliverable {
			continue // not a chain we deliver to at all
		}
		if _, fromEVM := out[key]; fromEVM {
			continue // users.wallet_address already served this chain; never overwrite it
		}
		out[key] = addr
	}

	if len(out) == 0 {
		return nil, ErrStableBuyNoWallet
	}
	return out, nil
}

// UserDeliveryAddress returns the user's own address on `chain`.
//
// No address is ever accepted from THIS request body — there is no field in
// the buy call to type one into. But that does not mean every address behind
// it is safe from the user: the two sources in userDeliveryAddresses differ.
// EVM chains (ETHEREUM, POLYGON, BASE) resolve from users.wallet_address,
// server-held and untouched by anything the caller sends here. SOLANA
// resolves from wallet_addresses, which the user themselves populated via
// POST /api/wallet/nonevm/register — RegisterNonEVMAddress validates that
// value for shape only, so it IS user-supplied, and there is no server-side
// derivation to verify it against.
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

// StableBuyInstructions is where the user sends their naira.
//
// Two shapes, one struct. In VA mode it is the user's own standing virtual
// account, unchanged between purchases, and Reference identifies them. In
// central mode it is the ONE company account everybody pays into, Reference is
// empty because nothing on a bank transfer carries it back to us, and AmountNGN
// carries the whole load: the payer's name plus this exact figure is the entire
// attribution key (design §5.3).
type StableBuyInstructions struct {
	AccountNumber string `json:"accountNumber"`
	BankName      string `json:"bankName"`
	AccountName   string `json:"accountName"`
	// Reference is the VA's own reference, echoed by the charge webhook. Empty
	// in central mode: a wallet-funding statement row has no reference of ours
	// on it, only the NIBSS session id the provider mints.
	Reference string `json:"reference,omitempty"`
	// AmountNGN is the exact figure to transfer, in whole major units, set only
	// in central mode. It is not a display convenience — matching compares the
	// credit against this number, so a payer who rounds it themselves is a hold
	// for an operator rather than an instant settlement.
	AmountNGN string `json:"amountNgn,omitempty"`
	// PayerInstruction is what the payer must be told before they transfer.
	// Set only in central mode — in VA mode the account is already in their own
	// name and the wording would be wrong. See CentralAccount.PayerInstruction.
	PayerInstruction string `json:"payerInstruction,omitempty"`
}

// StableBuyOpenError is ErrStableBuyAlreadyOpen naming the purchase that is
// open, so a caller can send the user to it (Decision D26). Its text is the
// sentinel's and errors.Is matches the sentinel, so every refusal reads unchanged.
type StableBuyOpenError struct{ IntentID uuid.UUID }

func (e *StableBuyOpenError) Error() string        { return ErrStableBuyAlreadyOpen.Error() }
func (e *StableBuyOpenError) Is(target error) bool { return target == ErrStableBuyAlreadyOpen }

// CreateStableBuyIntent opens a purchase through POST /api/it/stable/buy and
// tells the user where to pay. Delivery is validated before anything is
// written: it is the one irreversible step.
func (s *ITService) CreateStableBuyIntent(
	ctx context.Context,
	userID uuid.UUID,
	q *StableBuyQuote,
	destChain, destAddress string,
) (*models.ITIntent, *StableBuyInstructions, error) {
	if !s.HasStableBuy() {
		return nil, nil, fmt.Errorf("%w: stablecoin purchases are not enabled", ErrStableQuoteUnavailable)
	}
	if err := s.stableBuyPrechecks(ctx, userID, q, false); err != nil {
		return nil, nil, err
	}
	// Which provider serves this currency's ramp is a server fact. An older
	// app build still calls this path for naira, and Flutterwave then refuses
	// the conversion while the money is held. Gated ONLY here: not in
	// QuoteStableBuy (settlement prices through it) and not on the ramp
	// router's path, which chose the provider itself. A nil registry is
	// deliberately no gate.
	if s.currencies != nil {
		if p := s.currencies.RampProvider(q.Currency); p != RampProviderFlutterwave {
			// Both quoted: the currency reaches here from a request body, and
			// TrimSpace does not remove an embedded newline that could forge a
			// second log line.
			log.Printf("[it stable buy] refused: %q ramps on %q, not flutterwave — caller is an out-of-date client",
				q.Currency, p)
			return nil, nil, ErrStableBuyWrongRamp
		}
	}
	return s.openStableBuy(ctx, userID, q, destChain, destAddress, stableBuyOpen{
		provider: models.ITProviderFlutterwave,
		minUnits: s.stableMinSell(),
		validate: ValidateBuyDelivery,
	})
}

// stableBuyPrechecks is what every entry point refuses on first: no quote,
// then — before the pay-into lookup, so an unverified user is told to verify
// rather than to activate a virtual account — KYC. A routed Flutterwave buy
// also needs the IT agreement (owner 2026-10-04 #4). Opening only: settlement
// has no such check, or money in flight would strand. Which rail must be wired
// is each entry point's own check (Decision D47).
func (s *ITService) stableBuyPrechecks(ctx context.Context, userID uuid.UUID, q *StableBuyQuote, requireAgreement bool) error {
	if q == nil {
		return errors.New("it stable buy: nil quote")
	}
	if err := requireVerifiedForMoneyIn(ctx, s.db, userID); err != nil {
		return err
	}
	if requireAgreement {
		return s.RequireITAgreement(ctx, userID)
	}
	return nil
}

// stableBuyOpen is how one entry point opens a purchase.
type stableBuyOpen struct {
	provider string
	// routed stamps RateExpiresAt: the quote is honoured until then plus the
	// processing grace (spec §5.1).
	routed bool
	// minUnits/maxUnits bound the delivered tokens; nil is no bound (the
	// company wallet's minimum is on naira, checked by its quote).
	minUnits, maxUnits *big.Int
	validate           func(chain, asset, address string) error
	// insert writes the row; nil is a plain insert. The company wallet's takes
	// its stock under the (chain, token) lock (Task 6).
	insert func(ctx context.Context, intent *models.ITIntent) error
	// status is the row's first status; "" means pending_payment. A pool
	// order starts opening (S11) and becomes claimable only once every slice
	// is locked on-chain.
	status string
	// deferHonour leaves RateExpiresAt NULL at insert: the honour window
	// starts when the pay-in is shown (spec §12.2 step 6), and a failed open
	// is then never counted as abandoned (M2).
	deferHonour bool
}

// openStableBuy is the one write every entry point shares.
func (s *ITService) openStableBuy(
	ctx context.Context,
	userID uuid.UUID,
	q *StableBuyQuote,
	destChain, destAddress string,
	o stableBuyOpen,
) (*models.ITIntent, *StableBuyInstructions, error) {
	destChain = strings.ToUpper(strings.TrimSpace(destChain))
	destAddress = strings.TrimSpace(destAddress)
	if err := o.validate(destChain, q.Asset, destAddress); err != nil {
		return nil, nil, err
	}
	if q.AssetUnits == nil || q.AssetUnits.Sign() <= 0 ||
		(o.minUnits != nil && q.AssetUnits.Cmp(o.minUnits) < 0) {
		floor := 0.0
		if o.minUnits != nil {
			floor = StableFromBaseUnits(o.minUnits)
		}
		return nil, nil, fmt.Errorf("%w: minimum delivery is %g %s", ErrStableBuyBelowMinimum, floor, q.Asset)
	}
	if o.maxUnits != nil && q.AssetUnits.Cmp(o.maxUnits) > 0 {
		return nil, nil, fmt.Errorf("%w: maximum is %g %s", ErrStableBuyAboveMaximum, StableFromBaseUnits(o.maxUnits), q.Asset)
	}

	// Where she pays, resolved before anything is written. Central mode never
	// touches the virtual-account table (see payIntoForStableBuy).
	payInto, fiatMinor, ch, err := s.payIntoForStableBuy(ctx, userID, q.Currency, q.FiatMinor)
	if err != nil {
		return nil, nil, err
	}
	// The funding line she was quoted is the cost of one channel; the order
	// must be paid through that same channel, or her price is not the cost.
	if q.PayIn != "" && q.PayIn != ch {
		return nil, nil, fmt.Errorf("%w: how this purchase is paid changed since the quote (%s, quoted %s) — get a new quote",
			ErrStableQuoteUnavailable, ch, q.PayIn)
	}
	// Retire abandoned purchases before looking for an open one.
	if err := s.expireStaleStableBuys(ctx, userID); err != nil {
		return nil, nil, err
	}
	open, err := s.findOpenStableBuy(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if open != nil {
		return nil, nil, &StableBuyOpenError{IntentID: open.ID}
	}
	// N2: the shared abandon cap holds at every routed entry point, not only
	// behind the router's adapters.
	if o.routed {
		if err := s.checkRoutedAbandonCap(ctx, userID); err != nil {
			return nil, nil, err
		}
	}
	// Find-then-Create is not atomic: the partial unique index
	// uq_it_intents_open_stable_buy_v2 (config/database.go) closes the race on
	// Postgres and is translated below. SQLite has none, hence the find.

	display := q.RatePerUnitMinor
	if q.DisplayRateMinor != nil {
		display = q.DisplayRateMinor
	}
	intent := &models.ITIntent{
		ID:          uuid.New(),
		UserID:      userID,
		Direction:   models.ITDirectionBuy,
		Status:      firstNonEmpty(o.status, models.ITStatusPendingPayment),
		Provider:    o.provider,
		Asset:       q.Asset,
		AssetAmount: q.AssetUnits.String(),
		Currency:    q.Currency,
		DestChain:   destChain,
		DestAddress: destAddress,
		LockedRate:  q.RatePerUnitMinor.String(),
		DisplayRate: display.String(),
		Fees:        marshalFees(q.Fees),
		// Every numeric column carries a value: PostgreSQL rejects
		// ''::numeric and SQLite hides it. fiatMinor, not q.FiatMinor:
		// central mode asks for whole naira (in VA mode they are equal).
		NgnKobo:     fiatMinor.String(),
		AmountMinor: fiatMinor.String(),
		RateKobo:    q.RatePerUnitMinor.String(),
		RateMinor:   q.RatePerUnitMinor.String(),
		McgpAmount:  "0",
		FeeNgnKobo:  "0",
		NetNgnKobo:  fiatMinor.String(),
		// No default tag, so '' would reach Postgres (SQLSTATE 22P02) — the
		// exact defect the sell leg hit.
		PlatformKeptKobo: "0",
		ReceivedAmount:   "0",
	}
	if o.routed && !o.deferHonour {
		honourUntil := time.Now().Add(s.stableLockTTL())
		intent.RateExpiresAt = &honourUntil
	}
	insert := o.insert
	if insert == nil {
		insert = func(ctx context.Context, i *models.ITIntent) error { return s.db.WithContext(ctx).Create(i).Error }
	}
	if err := insert(ctx, intent); err != nil {
		if isUniqueViolation(err) {
			if winner, ferr := s.findOpenStableBuy(ctx, userID); ferr == nil && winner != nil {
				return nil, nil, &StableBuyOpenError{IntentID: winner.ID}
			}
			return nil, nil, ErrStableBuyAlreadyOpen
		}
		return nil, nil, err
	}
	return intent, payInto, nil
}

// findOpenStableBuy is the user's stablecoin purchase awaiting payment, or a
// pool order still opening (S11), of any provider, or nil.
func (s *ITService) findOpenStableBuy(ctx context.Context, userID uuid.UUID) (*models.ITIntent, error) {
	var rows []models.ITIntent
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND direction = ? AND status IN ? AND asset IN ?",
			userID, models.ITDirectionBuy, []string{models.ITStatusPendingPayment, models.ITStatusOpening},
			[]string{models.ITAssetUSDC, models.ITAssetUSDT}).
		Order("created_at DESC").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// payIntoForStableBuy resolves where the buyer sends their naira, and the exact
// amount the intent should be minted at.
//
// ── Central mode ──
//
// Every payer goes to the one company account, so there is nothing per-user to
// look up and ErrStableBuyNoVA cannot arise: the branch that returns it is not
// merely unlikely here, it is not on the path at all. That matters more than it
// sounds — a "cannot happen" error that still has a live code path is how a
// retired feature comes back as a 500 nobody can reproduce.
//
// Attribution moves from a reference nobody can see to the payer's own name
// plus the amount, so the amount has to be a number a human can type into a
// bank app and reproduce exactly. That is why it is rounded to whole naira:
// matching compares at naira granularity (design §5.4), and 99 kobo of
// difference is 99 kobo the payer never sees and the matcher cannot forgive.
//
// ── VA mode ──
//
// Unchanged. The user's standing virtual account, refused when they have none.
func (s *ITService) payIntoForStableBuy(
	ctx context.Context,
	userID uuid.UUID,
	currency string,
	fiatMinor *big.Int,
) (*StableBuyInstructions, *big.Int, PayInChannel, error) {
	if central, ok := s.ActiveCentralAccount(); ok {
		// wholeNairaKobo, not a rounding of our own. It is the SAME function the
		// matcher rounds the incoming credit with, so the ask and the credit it
		// will be compared against cannot round in opposite directions and turn
		// a correct payment into a hold. Central mode is naira-only by
		// construction — the account is a Nigerian NUBAN — so an NGN-specific
		// helper is the honest one here rather than a currency-generic wrapper
		// that implies a generality nothing has.
		asked, ok := new(big.Int).SetString(wholeNairaKobo(fiatMinor.String()), 10)
		if !ok || asked.Sign() <= 0 {
			// Rounding took the ask to nothing. Only reachable for an amount
			// under half a naira, which the delivery minimum already refuses —
			// but "pay ₦0 into this account" must never render.
			return nil, nil, "", fmt.Errorf("%w: amount rounds to zero at whole-naira granularity",
				ErrStableBuyBelowMinimum)
		}
		return &StableBuyInstructions{
			AccountNumber: central.AccountNumber,
			BankName:      central.BankName,
			AccountName:   central.AccountName,
			// No Reference: nothing we mint survives a bank transfer into the
			// company account. The name and the amount ARE the reference.
			AmountNGN:        minorToMajor(asked.Int64(), 2),
			PayerInstruction: central.PayerInstruction(VerifiedLegalNameOf(ctx, s.db, userID)),
		}, asked, PayInCentral, nil
	}

	var va models.FiatVirtualAccount
	err := s.db.WithContext(ctx).
		// Status filter matters: handing out an inactive VA's details tells the
		// user to pay into an account that will not credit them.
		Where("user_id = ? AND currency = ? AND status = ?",
			userID, currency, models.FiatVAStatusActive).
		First(&va).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, "", ErrStableBuyNoVA
	} else if err != nil {
		return nil, nil, "", err
	}
	return &StableBuyInstructions{
		AccountNumber: va.AccountNumber,
		BankName:      va.BankName,
		AccountName:   va.AccountName,
		Reference:     va.Reference,
	}, fiatMinor, PayInVA, nil
}

// stableBuyExpiredReason marks the rows expireStaleStableBuys retired.
const stableBuyExpiredReason = "expired before payment"

// expireStaleStableBuys retires this user's abandoned purchases.
//
// Past stableBuyClaimWindow a pending_payment row can no longer claim a charge
// (settleStableBuyFromCharge bounds its candidate query by age), but nothing
// retired it either: it still trips ErrStableBuyAlreadyOpen and wedges the
// user out of buying, and it is deliberately absent from the stuck list —
// nothing has been paid, so no operator ever sees it. It also silently
// diverted the user's late transfer to the MCGP path, handing them an
// irreversible on-chain credit when they asked for stablecoin.
//
// Opportunistic rather than a periodic job: it runs on the user's next
// attempt, which is exactly when the wedge is felt, and it needs no new
// scheduler registration on a live boot path. Cancelling is safe against a
// charge landing in the same instant — the claim query requires BOTH
// pending_payment and an age inside the window, so a row this touches was
// already unclaimable.
func (s *ITService) expireStaleStableBuys(ctx context.Context, userID uuid.UUID) error {
	if err := s.expireStaleOpeningBuys(ctx, userID); err != nil {
		return err
	}
	// One cutoff for both reads: every row the update retires was also read
	// as routed or not, so no converted buy's record is left behind.
	cutoff := time.Now().Add(-stableBuyClaimWindow)
	stale := func(db *gorm.DB) *gorm.DB {
		return db.WithContext(ctx).Model(&models.ITIntent{}).
			Where("user_id = ? AND direction = ? AND status = ? AND asset IN ? AND created_at < ?",
				userID, models.ITDirectionBuy, models.ITStatusPendingPayment,
				[]string{models.ITAssetUSDC, models.ITAssetUSDT}, cutoff)
	}
	var candidates []models.ITIntent
	if err := stale(s.db).Where("rate_expires_at IS NOT NULL").Select("id", "provider").Find(&candidates).Error; err != nil {
		return err
	}
	var routed []models.ITIntent // the routed buys this write actually expired
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := stale(tx).Updates(map[string]any{
			"status":         models.ITStatusCancelled,
			"failure_reason": stableBuyExpiredReason,
		}).Error; err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		// A converted buy's record expires with its intent, in the same
		// write (spec §15.4) — but only if THIS write expired the intent: a
		// late payment may have claimed it since the read above, and its
		// record must stay open for that claim. No record is a no-op.
		ids := make([]uuid.UUID, len(candidates))
		for i := range candidates {
			ids[i] = candidates[i].ID
		}
		if err := tx.WithContext(ctx).Model(&models.ITIntent{}).Select("id", "provider").
			Where("id IN ? AND status = ? AND failure_reason = ?", ids, models.ITStatusCancelled, stableBuyExpiredReason).
			Find(&routed).Error; err != nil {
			return err
		}
		for i := range routed {
			if err := markConversion(ctx, tx, routed[i].ID, []string{models.PaycrestStatusAwaitingPayment},
				map[string]any{"status": models.PaycrestStatusExpired}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for i := range routed {
		// Spec §7: unpaid routed opens are logged however they end.
		log.Printf("[it stable buy] METRIC ramp_it_open_unpaid intent=%s user=%s provider=%s reason=expired",
			routed[i].ID, userID, models.ITIntentProvider(&routed[i]))
	}
	return nil
}

// stableBuyOpeningStaleAfter is the retired slice pool's opening sweep bound
// (SweepStaleOpening, spec 2026-10-08 F3): an open took seconds, so an order
// still `opening` after it was left by a crash.
const stableBuyOpeningStaleAfter = 2 * time.Minute

// expireStaleOpeningBuys retires this user's stuck `opening` pool orders.
// Only the retired slice pool ever wrote `opening` (the shared vault never
// does), and its sweep went with it — so without this a crash mid-open wedges
// the user out of stablecoin buys forever (findOpenStableBuy counts it).
// Closed as a failed open (hidden from her lists, never an abandon), exactly
// as the old sweep did — but only while no slice of the order is still live:
// a slice row is written before its on-chain open, so a live one may hold
// LP money on-chain, and an operator settles that by hand.
func (s *ITService) expireStaleOpeningBuys(ctx context.Context, userID uuid.UUID) error {
	cutoff := time.Now().Add(-stableBuyOpeningStaleAfter)
	var ids []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("user_id = ? AND direction = ? AND status = ? AND created_at < ?",
			userID, models.ITDirectionBuy, models.ITStatusOpening, cutoff).
		Pluck("id", &ids).Error; err != nil || len(ids) == 0 {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, id := range ids {
			var live int64
			if err := tx.Model(&models.P2PTrade{}).Where("it_intent_id = ? AND status NOT IN ?", id,
				[]string{models.P2PTradeStatusReleased, models.P2PTradeStatusCancelled, models.P2PTradeStatusResolved}).
				Count(&live).Error; err != nil {
				return err
			}
			if live > 0 {
				log.Printf("[liquidity] ALERT liquidity_slice_opening_stuck intent=%s user=%s live_slices=%d — not expired; settle the slices by hand",
					id, userID, live)
				continue
			}
			res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", id, models.ITStatusOpening).
				Updates(map[string]any{"status": models.ITStatusCancelled, "failure_reason": models.ITFailurePoolOpenFailed})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				continue
			}
			if err := markConversion(ctx, tx, id, []string{models.PaycrestStatusAwaitingPayment},
				map[string]any{"status": models.PaycrestStatusExpired}); err != nil {
				return err
			}
			log.Printf("[liquidity] METRIC ramp_pool_open outcome=swept intent=%s user=%s", id, userID)
		}
		return nil
	})
}

// CancelStableBuy is the stablecoin-only form of CancelBuy, kept for the
// POST /api/it/stable/buy/:id/cancel route older app builds call. It refuses an
// MCGP order (ErrITBadState) so that route keeps meaning what its path says;
// everything else — ownership, the status CAS, the late-payment guard — is
// CancelBuy's.
func (s *ITService) CancelStableBuy(ctx context.Context, userID, intentID uuid.UUID) error {
	_, err := s.cancelBuy(ctx, userID, intentID, true)
	return err
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
	// Spec §5.4 / S45: only a buy Flutterwave fulfils is converted here. The
	// company wallet (and any provider without terms) would otherwise be
	// bought a second time at Flutterwave with the user's naira.
	if err := requireFlutterwaveFulfils(intent, "converted at"); err != nil {
		return err
	}
	switch intent.Status {
	case models.ITStatusConverting, models.ITStatusDelivering, models.ITStatusSettled:
		return nil
	case models.ITStatusPaymentReceived, models.ITStatusAwaitingFunds:
	default:
		return ErrITBadState
	}

	// Never advance a claim that was never priced. settleStableBuyFromCharge
	// now claims and prices in one write, so the only way to reach here with
	// Repriced == false is a row claimed by an older build — a rolling deploy.
	// Converting such a row spends the real received_amount while delivery
	// still carries the CREATION-TIME asset_amount: an overpayer silently
	// loses the excess, and an underpayer's delivery is drawn from the POOLED
	// merchant balance, i.e. another user's stablecoin.
	if !stableBuyPriced(intent) {
		priced, err := s.priceStableBuy(ctx, intent, intent.ReceivedAmount, intent.Status, intent.Status)
		if err != nil {
			// Money has landed and cannot be handed back: park it visibly for
			// an operator rather than converting on terms we cannot stand by.
			log.Printf("[it stable buy] ALARM intent=%s was claimed but never priced and cannot be priced now: %v",
				intent.ID, err)
			if uErr := s.db.WithContext(ctx).Model(&models.ITIntent{}).
				Where("id = ? AND status = ?", intent.ID, intent.Status).
				Updates(map[string]any{
					"status":         models.ITStatusBuyAwaitingRate,
					"failure_reason": "could not price before conversion: " + err.Error(),
				}).Error; uErr != nil {
				return uErr
			}
			return err
		}
		if !priced {
			// Another tick advanced it while we were on the rate call.
			return nil
		}
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
	if err := requireFlutterwaveFulfils(intent, "converted at"); err != nil {
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
		pr := tr.ProviderResponse
		reason := fwFailureReason("buy conversion failed", tr)
		log.Printf("[it stable buy] conversion failed intent=%s transfer=%s code=%s msg=%q",
			intent.ID, tr.ID, safeLogToken(pr.Code), pr.Message)
		if pr.BelowLimit() {
			// Under Flutterwave's floor the same amount fails every time —
			// intent 898ce9ef burned all ten attempts on it. Park it for an
			// operator (it is in the stuck list) instead of retrying; the operator
			// refunds, or resumes once the amount is fixed.
			log.Printf("[it stable buy] ALERT it_stable_buy_below_provider_limit intent=%s leg=conversion — parked, not retried", intent.ID)
			return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
				"status":         models.ITStatusBuyAwaitingRate,
				"failure_reason": reason,
				"conversion_id":  "",
				// The parked reference is spent: Flutterwave would replay its
				// cached FAILED transfer. A resume mints a fresh one (I7).
				"conversion_attempts": intent.ConversionAttempts + 1,
			}).Error
		}
		return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
			"status":              models.ITStatusAwaitingFunds,
			"failure_reason":      reason,
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

// ErrStableFloatShort means a converted buy is waiting for the company's
// stablecoin float to cover its delivery: the conversion landed below
// AssetAmount (an honoured quote, or slippage past the spread). It always
// comes with ErrStableCreditNotVisible, so it is still a wait that burns no
// attempt — and the watcher reports it so its failure-streak alert fires.
var ErrStableFloatShort = errors.New("it stable buy: not enough stablecoin in the Flutterwave wallet to deliver")

// fwFailureReason records why Flutterwave failed a transfer: its own code,
// type and message when it gave them (message %q, so provider text cannot
// forge a field), within failure_reason's 256 bytes.
func fwFailureReason(prefix string, tr FWTransfer) string {
	reason := prefix + ": " + tr.ID
	if pr := tr.ProviderResponse; !pr.Empty() {
		reason += fmt.Sprintf(" code=%s type=%s msg=%q", safeLogToken(pr.Code), safeLogToken(pr.Type), pr.Message)
	}
	if len(reason) > 256 {
		reason = strings.ToValidUTF8(reason[:256], "")
	}
	return reason
}

// safeLogToken prints a provider token bare when it is plain, quoted
// otherwise, so a code can never smuggle a space or newline into a log line.
func safeLogToken(s string) string {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '-') {
			return strconv.Quote(s)
		}
	}
	return s
}

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
	if err := requireFlutterwaveFulfils(intent, "sent by"); err != nil {
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
	// What is sent, and what the wallet must hold to send it (D31).
	sendUnits, holdUnits := s.routedDeliveryAmounts(intent, units)
	amountMajor := StableFromBaseUnits(sendUnits)
	neededMajor := StableFromBaseUnits(holdUnits)

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
	if !found || available < neededMajor {
		// The conversion settled, so a shortfall here is the company float not
		// covering it. Still a wait (ErrStableCreditNotVisible: no delivery
		// attempt burned), but one that needs a top-up, so it is marked.
		log.Printf("[it stable buy] METRIC ramp_fw_float_short intent=%s asset=%s available=%v needed=%v",
			intent.ID, intent.Asset, available, neededMajor)
		return fmt.Errorf("%w: %w: %s available=%v needed=%v",
			ErrStableFloatShort, ErrStableCreditNotVisible, intent.Asset, available, neededMajor)
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
	if err := requireFlutterwaveFulfils(intent, "sent by"); err != nil {
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
		// The record existing decides the handoff, never the wiring (N1).
		rec, err := s.ConversionFor(ctx, intent)
		if err != nil {
			return err
		}
		if rec != nil {
			return s.finishFlutterwaveConverted(ctx, intent, tr)
		}
		return s.db.WithContext(ctx).Model(intent).
			Update("status", models.ITStatusSettled).Error
	case "FAILED":
		// Clear the id and bump the counter so the retry mints a fresh
		// reference instead of colliding with Flutterwave's idempotency on the
		// transfer that just failed. Stays in `delivering`: the asset is in our
		// balance and the user has not been paid.
		pr := tr.ProviderResponse
		log.Printf("[it stable buy] delivery failed intent=%s transfer=%s code=%s msg=%q",
			intent.ID, tr.ID, safeLogToken(pr.Code), pr.Message)
		attempts := intent.DeliveryAttempts + 1
		if pr.BelowLimit() {
			// Decision D17: below the send floor never succeeds. Jump to the
			// cap — DeliverStableBuy then refuses with ErrStableDeliveryExhausted
			// and the stuck list shows it — instead of retrying ten times.
			log.Printf("[it stable buy] ALERT it_stable_buy_below_provider_limit intent=%s leg=delivery — parked, not retried", intent.ID)
			attempts = maxStableDeliveryAttempts
		}
		return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
			"failure_reason":    fwFailureReason("delivery failed", tr),
			"delivery_id":       "",
			"delivery_attempts": attempts,
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
		// Only providers Flutterwave fulfils (spec S45); the company wallet and
		// every other provider have their own watcher branch.
		Where("provider IN ?", flutterwaveFulfilledProviders()).
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

// StuckStableBuy is a buy holding user funds that has not advanced.
type StuckStableBuy struct {
	ID          uuid.UUID `json:"id"`
	UserID      uuid.UUID `json:"userId"`
	Status      string    `json:"status"`
	Asset       string    `json:"asset"`
	Currency    string    `json:"currency"`
	DestChain   string    `json:"destChain"`
	AmountMinor string    `json:"amountMinor"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ListStuckStableBuys surfaces buys stalled longer than olderThan.
//
// Every non-terminal state is included, because each one is an intent where we
// are holding either the user's naira or their stablecoin. pending_payment is
// excluded: nothing has been paid, so there is nothing at risk.
func (s *ITService) ListStuckStableBuys(ctx context.Context, olderThan time.Duration) ([]StuckStableBuy, error) {
	var out []StuckStableBuy
	err := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Select("id", "user_id", "status", "asset", "currency", "dest_chain",
			"received_amount AS amount_minor", "updated_at").
		Where("direction = ? AND asset IN ? AND status IN ? AND updated_at < ?",
			models.ITDirectionBuy,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT},
			[]string{
				models.ITStatusPaymentReceived,
				models.ITStatusAwaitingFunds,
				models.ITStatusConverting,
				models.ITStatusDelivering,
				// Money landed but unpriced — parked for a human. The watcher
				// deliberately does NOT auto-advance these, so the stuck list
				// is the only way anyone learns they exist.
				models.ITStatusBuyAwaitingRate,
			},
			time.Now().Add(-olderThan)).
		Scan(&out).Error
	return out, err
}
