package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ITConversionRefPrefix marks the conversion record of an IT-settled
// converted buy; the rest of the reference is the intent's id (spec §15.4).
const ITConversionRefPrefix = "ITC-"

const (
	ConversionFeeLabel = "Conversion (provider)"
)

var ErrStableBuyAbandonCapReached = errors.New("too many unpaid purchases were cancelled or expired today")

var (
	// ErrConvertedOverpaid: live pricing gives more than the converted buy was
	// opened for (N4). Deterministic: it parks.
	ErrConvertedOverpaid = errors.New("converted buy: the live amount is above what was opened")
	// ErrConvertedNoWaiver: "deliver anyway" never applies to a converted buy (N4).
	ErrConvertedNoWaiver = errors.New("a converted purchase cannot be delivered above its opened amount; refund it")
)

func ITConversionRef(intentID uuid.UUID) string { return ITConversionRefPrefix + intentID.String() }

func IsITConversion(o *models.PaycrestOrderRecord) bool {
	return o != nil && strings.HasPrefix(o.Reference, ITConversionRefPrefix)
}

// ConvertedTreasury is the slice of the conversion *Treasury an open needs.
type ConvertedTreasury interface {
	Lane(chainKey string) *TreasuryLane
	CheckCaps(chainKey string, orderUSD, inFlightUSD float64) error
}

// ConvertedBuys is the conversion treasury as converted OPENS and the
// operator's attach-hash see it. Empty: no converted buy opens.
type ConvertedBuys struct {
	TreasuryAddress string
	Treasury        ConvertedTreasury
	Verifiers       map[string]ERC20TransferVerifier // by chain key: the attach-hash pre-check
	Probes          map[string]TreasuryTxProbe       // by chain key: its depth and block-time check
	// FeeCaps holds RAMP_CONVERTED_FEE_MAX_BPS for the opens (the pricer's own
	// caps). Nil applies the pricer's default cap.
	FeeCaps ConvertedFeeCaps
}

// ConvertedFeeCaps answers whether a conversion budget B′ is one the pricer
// could have offered for the requested chain; *PaycrestQuoteService is it.
type ConvertedFeeCaps interface {
	ConvertedBudgetAllowed(requestedChain string, budgetBps int) bool
}

func (s *ITService) SetConvertedBuys(c ConvertedBuys) {
	c.TreasuryAddress = strings.TrimSpace(c.TreasuryAddress)
	s.converted = c
}

// ConvertedOpen is what a converted open needs beyond the leg's quote: the
// venue the pricer gave for her target at this D, and her own wallet.
type ConvertedOpen struct {
	Venue         PaycrestVenue
	WalletAddress string
}

// convertedTreasuryFor is the conversion treasury's address for a leg on
// chain, once its lane exists and its advisory exposure caps take D (the
// orchestrator runs the authoritative check before it moves money).
func (s *ITService) convertedTreasuryFor(chain string, q *StableBuyQuote, v PaycrestVenue) (string, error) {
	c := s.converted
	chain = strings.ToLower(strings.TrimSpace(chain))
	if c.TreasuryAddress == "" || c.Treasury == nil || c.Treasury.Lane(chain) == nil {
		return "", fmt.Errorf("%w: no conversion treasury on %s", ErrConversionUnavailable, chain)
	}
	// The fee cap is the pricer's, held here too: an exported open never
	// relies on its caller having priced the venue (ledger rule).
	if !c.feeCaps().ConvertedBudgetAllowed(v.RequestedChain, v.MarkupBps) {
		return "", fmt.Errorf("%w: a %d bps conversion budget is over the cap for %s",
			ErrConversionUnavailable, v.MarkupBps, v.RequestedChain)
	}
	// T must be what this D owes at the venue's budget, in the leg's own
	// units (S23), and something: a budget outside [0, 100%) would open a buy
	// that owes her nothing or more than was bought.
	legUnits, err := CanonicalToChainUnits(q.AssetUnits, venueLegDecimals(v))
	if err != nil || v.Kind != models.RouteKindConverted || !strings.EqualFold(v.ChainKey, chain) || !strings.EqualFold(v.Token, q.Asset) ||
		v.MarkupBps < 0 || v.MarkupBps >= 10_000 || ConversionOwed(legUnits, v.MarkupBps).Sign() <= 0 ||
		v.TokenAmountBaseUnits != ConversionOwed(legUnits, v.MarkupBps).String() {
		return "", fmt.Errorf("%w: the converted venue was not priced on this settlement leg", ErrConversionUnavailable)
	}
	if err := c.Treasury.CheckCaps(chain, StableFromBaseUnits(q.AssetUnits), 0); err != nil {
		return "", fmt.Errorf("%w: %s", ErrConversionUnavailable, RedactAlertText(err.Error()))
	}
	return c.TreasuryAddress, nil
}

// venueLegDecimals is the decimals a converted venue's T is in: 6 unless the
// pricer said otherwise.
func venueLegDecimals(v PaycrestVenue) int {
	if v.LegDecimals == 0 {
		return StableAssetDecimals
	}
	return v.LegDecimals
}

// recordLegDecimals is what a conversion record stores for its leg: nil for
// 6 dp, so a 6-dp record is written exactly as before the pool (nil reads as
// 6, conversionLegDecimals).
func recordLegDecimals(v PaycrestVenue) *int {
	if d := venueLegDecimals(v); d != StableAssetDecimals {
		return &d
	}
	return nil
}

// feeCaps is the wired cap, or the pricer's default when none is wired.
func (c ConvertedBuys) feeCaps() ConvertedFeeCaps {
	if c.FeeCaps == nil {
		return (*PaycrestQuoteService)(nil)
	}
	return c.FeeCaps
}

// newConversionRecord is the row the orchestrator drives once the leg lands.
func newConversionRecord(intent *models.ITIntent, in ConvertedOpen, holder string) *models.PaycrestOrderRecord {
	v := in.Venue
	chain := strings.ToLower(intent.DestChain)
	return &models.PaycrestOrderRecord{
		UserID:                 intent.UserID,
		Direction:              models.PaycrestDirectionBuy,
		Status:                 models.PaycrestStatusAwaitingPayment,
		Reference:              ITConversionRef(intent.ID),
		FiatCode:               intent.Currency,
		FiatAmount:             FormatMinor(intent.AmountMinor),
		Token:                  intent.Asset,
		ChainKey:               chain,
		Network:                chain,
		Rate:                   v.Rate,
		TokenAmount:            FormatStableUnits(intent.AssetAmount), // D, a decimal like Paycrest's own rows
		WalletAddress:          in.WalletAddress,
		RouteKind:              models.RouteKindConverted,
		RequestedChain:         v.RequestedChain,
		RequestedToken:         v.RequestedToken,
		RequestedChainID:       v.RequestedChainID,
		RequestedTokenAddress:  v.RequestedAddress,
		RequestedTokenDecimals: v.RequestedDecimals,
		DestinationAddress:     firstNonEmpty(v.RequestedDestination, in.WalletAddress),
		MarkupBps:              v.MarkupBps,
		QuotedAmount:           v.TokenAmountBaseUnits, // T, checked against this D by convertedTreasuryFor
		LegDecimals:            recordLegDecimals(v),   // the units QuotedAmount is in
		EstimatedAmount:        v.EstimatedAmount,
		PromisedAmount:         v.PromisedAmount,
		Holder:                 holder,
		PayAmount:              FormatMinor(intent.AmountMinor),
		ValidUntil:             intent.RateExpiresAt,
	}
}

// OpenTreasuryConvertedBuy opens a route-1 converted buy (spec §15): the
// company wallet's purchase delivering USDC to the conversion treasury —
// every limit of OpenTreasuryBuy, the stock taken under the lock — and its
// conversion record, in the SAME transaction (Decision C4).
func (s *ITService) OpenTreasuryConvertedBuy(ctx context.Context, userID uuid.UUID, q *StableBuyQuote, chain string, in ConvertedOpen) (*models.ITIntent, *StableBuyInstructions, *models.PaycrestOrderRecord, error) {
	if q == nil {
		return nil, nil, nil, errors.New("it treasury converted buy: nil quote")
	}
	// Her own wallet is where the conversion delivers by default: refuse an
	// unusable one before anything is written.
	in.WalletAddress = strings.TrimSpace(in.WalletAddress)
	if !common.IsHexAddress(in.WalletAddress) {
		return nil, nil, nil, fmt.Errorf("%w: a converted buy needs her EVM wallet", ErrBadDeliveryAddress)
	}
	treasury, err := s.convertedTreasuryFor(chain, q, in.Venue)
	if err != nil {
		return nil, nil, nil, err
	}
	var rec *models.PaycrestOrderRecord
	intent, payInto, err := s.openTreasuryBuy(ctx, userID, q, chain, treasury, func(tx *gorm.DB, intent *models.ITIntent) error {
		rec = newConversionRecord(intent, in, models.HolderCompanyWallet)
		return tx.Create(rec).Error
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return intent, payInto, rec, nil
}

// ConversionFor is the conversion record a buy feeds, or nil. It reads the
// database only: the record existing is what makes a delivery feed a
// conversion, never runtime wiring (N1).
func (s *ITService) ConversionFor(ctx context.Context, intent *models.ITIntent) (*models.PaycrestOrderRecord, error) {
	if intent == nil || intent.Direction != models.ITDirectionBuy || !IsRoutedStableBuy(intent) {
		return nil, nil
	}
	return conversionRecordOn(ctx, s.db, intent.ID)
}

// ConversionsFor is ConversionFor over a list, in one query: each converted
// buy's record by intent id.
func (s *ITService) ConversionsFor(ctx context.Context, intents []models.ITIntent) (map[uuid.UUID]*models.PaycrestOrderRecord, error) {
	refs := map[string]uuid.UUID{}
	for i := range intents {
		if intents[i].Direction == models.ITDirectionBuy && IsRoutedStableBuy(&intents[i]) {
			refs[ITConversionRef(intents[i].ID)] = intents[i].ID
		}
	}
	out := map[uuid.UUID]*models.PaycrestOrderRecord{}
	if len(refs) == 0 {
		return out, nil
	}
	keys := make([]string, 0, len(refs))
	for ref := range refs {
		keys = append(keys, ref)
	}
	var rows []models.PaycrestOrderRecord
	if err := s.db.WithContext(ctx).Where("reference IN ?", keys).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		out[refs[rows[i].Reference]] = &rows[i]
	}
	return out, nil
}

func conversionRecordOn(ctx context.Context, db *gorm.DB, intentID uuid.UUID) (*models.PaycrestOrderRecord, error) {
	var rows []models.PaycrestOrderRecord
	if err := db.WithContext(ctx).Where("reference = ?", ITConversionRef(intentID)).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// markConversion moves the intent's record off one of `from` in one guarded
// write. No record (a direct buy) is a no-op.
func markConversion(ctx context.Context, db *gorm.DB, intentID uuid.UUID, from []string, updates map[string]any) error {
	return db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Where("reference = ? AND status IN ?", ITConversionRef(intentID), from).Updates(updates).Error
}

// handoffConversion gives a delivered leg to the orchestrator: the record
// becomes `converting` with the leg's hash, restated from what was delivered.
// Runs on the caller's transaction. Idempotent for the same hash.
func (s *ITService) handoffConversion(ctx context.Context, tx *gorm.DB, intent *models.ITIntent, txHash string) error {
	rec, err := conversionRecordOn(ctx, tx, intent.ID)
	if err != nil || rec == nil {
		return err
	}
	delivered, ok := new(big.Int).SetString(strings.TrimSpace(intent.AssetAmount), 10)
	if !ok || delivered.Sign() <= 0 {
		return fmt.Errorf("it converted buy: intent %s has no delivered amount", intent.ID)
	}
	// N4 at the last gate: whatever priced D, a bridge larger than the record
	// was opened (and capped, and reserved) for never starts. Before the
	// handoff, token_amount is still the opened D. A larger D is capped at
	// it — the excess stays in our treasury and an operator refunds its
	// naira — rather than refused: the leg is already delivered, and a
	// refusal would roll the settle back every tick with no exit (N1).
	if rec.Status != models.PaycrestStatusConverting {
		opened, err := DecimalToBaseUnits(rec.TokenAmount, StableAssetDecimals)
		if err != nil || opened.Sign() <= 0 {
			log.Printf("[it converted buy] ALERT ramp_conversion_handoff_overpaid intent=%s record=%s opened=%q unreadable",
				intent.ID, rec.ID, rec.TokenAmount)
			return fmt.Errorf("%w: its opened amount %q is unreadable", ErrConvertedOverpaid, rec.TokenAmount)
		}
		if delivered.Cmp(opened) > 0 {
			excess := new(big.Int).Sub(delivered, opened)
			log.Printf("[it converted buy] ALERT ramp_converted_handoff_capped intent=%s record=%s opened=%s delivered=%s excess=%s "+
				"— bridging the opened amount only; refund her the excess naira", intent.ID, rec.ID,
				FormatStableUnits(opened.String()), FormatStableUnits(delivered.String()), FormatStableUnits(excess.String()))
			delivered = opened
		}
	}
	updates, err := handoffUpdates(rec, delivered, txHash)
	if err != nil {
		return err
	}
	res := tx.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Where("id = ? AND status IN ?", rec.ID, []string{models.PaycrestStatusAwaitingPayment, models.PaycrestStatusPending}).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 1 {
		log.Printf("[it converted buy] METRIC ramp_conversion_handoff intent=%s record=%s tx=%s", intent.ID, rec.ID, txHash)
		return nil
	}
	if rec.Status == models.PaycrestStatusConverting && strings.EqualFold(rec.TxHash, txHash) {
		return nil // already handed off with this leg
	}
	log.Printf("[it converted buy] ALERT ramp_conversion_handoff_refused intent=%s record=%s status=%s", intent.ID, rec.ID, rec.Status)
	return fmt.Errorf("%w: conversion record %s is %s", ErrITBadState, rec.ID, rec.Status)
}

// conversionLegDecimals is the record's leg-token decimals: 6 unless the opener
// recorded otherwise (spec S23). Read from the record, never the wiring (N1).
func conversionLegDecimals(rec *models.PaycrestOrderRecord) int {
	if rec != nil && rec.LegDecimals != nil {
		return *rec.LegDecimals
	}
	return StableAssetDecimals
}

// handoffUpdates turn a record into `converting` on txHash, restated from what
// was delivered (canonical units): D can be smaller than opened (live
// pricing), never larger (N4). quoted_amount is in the leg's own base units.
func handoffUpdates(rec *models.PaycrestOrderRecord, delivered *big.Int, txHash string) (map[string]any, error) {
	legUnits, err := CanonicalToChainUnits(delivered, conversionLegDecimals(rec))
	if err != nil {
		return nil, fmt.Errorf("it converted buy: record %s: %w", rec.ID, err)
	}
	return map[string]any{
		"status": models.PaycrestStatusConverting, "tx_hash": txHash, "failure_reason": "",
		"token_amount":  FormatStableUnits(delivered.String()),
		"quoted_amount": ConversionOwed(legUnits, rec.MarkupBps).String(),
	}, nil
}

// convertedCeiling (N4): a converted buy never delivers more than it was
// opened for — a bigger bridge was never priced, capped or reserved for.
func (s *ITService) convertedCeiling(ctx context.Context, intent *models.ITIntent, units *big.Int) error {
	rec, err := s.ConversionFor(ctx, intent)
	if err != nil || rec == nil {
		return err
	}
	opened, err := DecimalToBaseUnits(rec.TokenAmount, StableAssetDecimals)
	if err != nil {
		return fmt.Errorf("%w: its opened amount %q is unreadable", ErrConvertedOverpaid, rec.TokenAmount)
	}
	if units.Cmp(opened) > 0 {
		return fmt.Errorf("%w: %s USDC above the %s opened", ErrConvertedOverpaid,
			FormatStableUnits(units.String()), FormatStableUnits(opened.String()))
	}
	return nil
}

// ConvertedTerms are a converted venue's economics on a delivered leg (spec
// §15.7): T, the all-in rate, and the fee lines — the leg's own plus the
// conversion budget as "Conversion (provider)".
type ConvertedTerms struct {
	OwedUnits          *big.Int
	EffectiveRateMinor *big.Int
	Fees               []StableBuyFee
}

func NewConvertedTerms(q *StableBuyQuote, v *PaycrestVenue) (*ConvertedTerms, error) {
	if q == nil || v == nil || q.AssetUnits == nil || q.FiatMinor == nil {
		return nil, errors.New("converted terms: missing quote or venue")
	}
	owed := ConversionOwed(q.AssetUnits, v.MarkupBps)
	if owed.Sign() <= 0 {
		return nil, fmt.Errorf("converted terms: nothing owed at a %d bps budget", v.MarkupBps)
	}
	fees := append(append([]StableBuyFee{}, q.Fees...), conversionFee(q.AssetUnits, owed, displayRateOf(q)))
	return &ConvertedTerms{OwedUnits: owed, EffectiveRateMinor: ratePerDeliveredUnit(q.FiatMinor, owed), Fees: fees}, nil
}

// ConversionFeeLine is the same line for a stored order: D − T at the
// intent's display rate.
func ConversionFeeLine(intent *models.ITIntent, rec *models.PaycrestOrderRecord) *StableBuyFee {
	d, okD := new(big.Int).SetString(strings.TrimSpace(intent.AssetAmount), 10)
	rate, okR := new(big.Int).SetString(strings.TrimSpace(StableBuyDisplayRate(intent)), 10)
	if !okD || !okR || rec == nil {
		return nil
	}
	f := conversionFee(d, ConversionOwed(d, rec.MarkupBps), rate)
	return &f
}

func conversionFee(delivered, owed, displayRate *big.Int) StableBuyFee {
	units := new(big.Int).Sub(delivered, owed)
	ngn := new(big.Int).Mul(units, displayRate)
	ngn.Quo(ngn, stableUnitsPerToken())
	return StableBuyFee{Kind: FeeKindConversion, Label: ConversionFeeLabel, Provider: FeeProviderShown,
		Units: units.String(), NgnMinor: ngn.String()}
}

func displayRateOf(q *StableBuyQuote) *big.Int {
	if q.DisplayRateMinor != nil {
		return q.DisplayRateMinor
	}
	return q.RatePerUnitMinor
}

// checkRoutedAbandonCap refuses after too many unpaid routed buys in 24h —
// enforced here so every entry point gets it (N2), not only the adapters.
func (s *ITService) checkRoutedAbandonCap(ctx context.Context, userID uuid.UUID) error {
	limit := s.RoutedAbandonCap()
	if limit <= 0 {
		return nil
	}
	n, err := s.CountAbandonedRoutedStableBuys(ctx, userID, time.Now().Add(-24*time.Hour))
	if err != nil {
		return err
	}
	if n >= int64(limit) {
		log.Printf("[it stable buy] METRIC ramp_abandon_cap_hit at=service user=%s abandoned=%d cap=%d", userID, n, limit)
		return ErrStableBuyAbandonCapReached
	}
	return nil
}

// OpenFlutterwaveConvertedBuy opens a route-2 converted buy (spec §15):
// Flutterwave's routed purchase — the IT agreement, its minimum and maximum —
// delivering USDC on Polygon to the conversion treasury, and its record, in
// one transaction (Decision C4).
func (s *ITService) OpenFlutterwaveConvertedBuy(ctx context.Context, userID uuid.UUID, q *StableBuyQuote, in ConvertedOpen) (*models.ITIntent, *StableBuyInstructions, *models.PaycrestOrderRecord, error) {
	if q == nil {
		return nil, nil, nil, errors.New("it flutterwave converted buy: nil quote")
	}
	var rec *models.PaycrestOrderRecord
	intent, payInto, err := s.openRoutedStableBuy(ctx, userID, q, routedDelivery{
		address: func() (string, error) {
			in.WalletAddress = strings.TrimSpace(in.WalletAddress)
			if !common.IsHexAddress(in.WalletAddress) {
				return "", fmt.Errorf("%w: a converted buy needs her EVM wallet", ErrBadDeliveryAddress)
			}
			return s.convertedTreasuryFor(strings.ToLower(RoutedDirectChain), q, in.Venue)
		},
		validate: func(_, _, addr string) error {
			if !common.IsHexAddress(addr) {
				return fmt.Errorf("%w: the conversion treasury address is not an EVM address", ErrBadDeliveryAddress)
			}
			return nil
		},
		insert: func(ctx context.Context, intent *models.ITIntent) error {
			return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(intent).Error; err != nil {
					return err
				}
				rec = newConversionRecord(intent, in, models.HolderFlutterwave)
				return tx.Create(rec).Error
			})
		},
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return intent, payInto, rec, nil
}

// finishFlutterwaveConverted is FinalizeBuyDelivery's SUCCESSFUL case for a
// converted buy (spec §15.4, Decision C13): hand off with Flutterwave's hash;
// without one, wait from the send, then settle the intent and park the
// record for an operator. The intent never settles past a record that cannot
// take the delivery.
func (s *ITService) finishFlutterwaveConverted(ctx context.Context, intent *models.ITIntent, tr FWTransfer) error {
	hash := tr.OnChainHash()
	if hash != "" && !evmTxHashRe.MatchString(hash) {
		log.Printf("[it converted buy] ALERT it_stable_buy_conversion_bad_hash intent=%s transfer=%s — treated as no hash",
			intent.ID, tr.ID)
		hash = ""
	}
	if hash != "" {
		// The orchestrator proves custody from this hash before it bridges.
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := s.handoffConversion(ctx, tx, intent, hash); err != nil {
				return err
			}
			return settleDeliveringIntent(tx, intent.ID)
		})
	}
	if time.Since(intent.UpdatedAt) < s.rampTerms().fwHashWait {
		return nil // the next tick reads the transfer again
	}
	log.Printf("[it converted buy] ALERT it_stable_buy_conversion_no_hash provider=flutterwave intent=%s transfer=%s — "+
		"Flutterwave reported SUCCESSFUL without an on-chain hash; parked for an operator", intent.ID, tr.ID)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.PaycrestOrderRecord{}).
			Where("reference = ? AND status IN ?", ITConversionRef(intent.ID),
				[]string{models.PaycrestStatusAwaitingPayment, models.PaycrestStatusPending}).
			// She reads this reason on her order screen: user copy never
			// names a provider (the ALERT above carries the detail).
			Updates(map[string]any{"status": models.PaycrestStatusRecoveryPending, "fault": models.FaultPlatform,
				"failure_reason": hashlessParkReason})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			rec, err := conversionRecordOn(ctx, tx, intent.ID)
			if err != nil {
				return err
			}
			if !isHashlessPark(rec) {
				status := "missing"
				if rec != nil {
					status = rec.Status
				}
				log.Printf("[it converted buy] ALERT ramp_conversion_handoff_refused intent=%s status=%s", intent.ID, status)
				return fmt.Errorf("%w: the conversion record is %s", ErrITBadState, status)
			}
		}
		// UpdateColumn, not Update: updated_at stays the delivery send time,
		// which attach-hash measures a pasted transfer against (M2).
		return tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intent.ID, models.ITStatusDelivering).
			UpdateColumn("status", models.ITStatusSettled).Error
	})
}

// hashlessParkReason is the parked record's reason, as she reads it.
const hashlessParkReason = "Your payment was delivered for conversion; we are confirming it on-chain before converting."

// conversionHashSendSkew is how far before the recorded send a transfer may
// be mined and still be this delivery's: the send is recorded only once the
// transfer request returns, and block clocks drift.
const conversionHashSendSkew = 2 * time.Minute

func settleDeliveringIntent(tx *gorm.DB, intentID uuid.UUID) error {
	return tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intentID, models.ITStatusDelivering).
		Update("status", models.ITStatusSettled).Error
}

// isHashlessPark: a route-2 record Flutterwave delivered without a hash, still
// before custody — the only record an operator may attach a hash to.
func isHashlessPark(rec *models.PaycrestOrderRecord) bool {
	return rec != nil && rec.Status == models.PaycrestStatusRecoveryPending &&
		rec.Holder == models.HolderFlutterwave && rec.TxHash == ""
}

// AttachConversionHash is the operator's exit from a hashless park (N5): the
// hash is attached only when the chain proves it — a successful receipt
// RAMP_TREASURY_CONFIRMATIONS deep, a Transfer of the lane's USDC to the
// treasury of at least the delivered amount, and no other record holding it.
// The orchestrator verifies it again at custody.
func (s *ITService) AttachConversionHash(ctx context.Context, adminID, intentID uuid.UUID, hash, note string) (*models.PaycrestOrderRecord, error) {
	note, hash = strings.TrimSpace(note), strings.TrimSpace(hash)
	if note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	if !evmTxHashRe.MatchString(hash) {
		return nil, fmt.Errorf("%w: not a transaction hash", ErrITBadState)
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	rec, err := s.ConversionFor(ctx, intent)
	if err != nil {
		return nil, err
	}
	if !isHashlessPark(rec) {
		return nil, fmt.Errorf("%w: only a hashless Flutterwave conversion can take a hash", ErrITBadState)
	}
	delivered, ok := new(big.Int).SetString(strings.TrimSpace(intent.AssetAmount), 10)
	if !ok || delivered.Sign() <= 0 {
		return nil, fmt.Errorf("%w: the order has no delivered amount", ErrITBadState)
	}
	c := s.converted
	chain := rec.ChainKey
	verifier, probe := c.Verifiers[chain], c.Probes[chain]
	var lane *TreasuryLane
	if c.Treasury != nil {
		lane = c.Treasury.Lane(chain)
	}
	if verifier == nil || probe == nil || lane == nil || lane.SponsorLane == nil || lane.tokenAddress(rec.Token) == "" || c.TreasuryAddress == "" {
		return nil, fmt.Errorf("%w: no verifier or lane on %s", ErrConversionUnavailable, chain)
	}
	gen := probe.EndpointGeneration() // the proof, its depth and its time come from one endpoint
	credited, err := verifier.VerifyTransfer(ctx, hash, lane.tokenAddress(rec.Token), c.TreasuryAddress)
	if err != nil || credited == nil || credited.Cmp(delivered) < 0 {
		reason := "it credited the treasury less than was delivered"
		if err != nil {
			reason = RedactAlertText(err.Error())
		}
		return nil, fmt.Errorf("%w: the hash does not prove the delivery: %s", ErrITBadState, reason)
	}
	need := uint64(s.treasuryLimits().confirmations)
	receipt, err := probe.TransactionReceipt(ctx, common.HexToHash(hash))
	if err != nil {
		return nil, fmt.Errorf("%w: its receipt is unreadable: %s", ErrITBadState, RedactAlertText(err.Error()))
	}
	if receipt == nil || receipt.BlockNumber == nil || receipt.Status != types.ReceiptStatusSuccessful {
		return nil, fmt.Errorf("%w: the transaction did not succeed", ErrITBadState)
	}
	head, err := probe.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: the chain head is unreadable: %s", ErrITBadState, RedactAlertText(err.Error()))
	}
	if mined := receipt.BlockNumber.Uint64(); head < mined || head-mined+1 < need {
		return nil, fmt.Errorf("%w: the receipt is not %d blocks deep yet", ErrITBadState, need)
	}
	// A transfer to the treasury proves THIS delivery only if it was mined
	// after the delivery was sent — never an older top-up. The park keeps
	// the intent's updated_at at the send.
	header, err := probe.HeaderByNumber(ctx, receipt.BlockNumber)
	if err != nil || header == nil {
		reason := "no block"
		if err != nil {
			reason = RedactAlertText(err.Error())
		}
		return nil, fmt.Errorf("%w: its block is unreadable: %s", ErrITBadState, reason)
	}
	if err := endpointSteady(probe, gen); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrITBadState, err)
	}
	sent := intent.UpdatedAt.Add(-conversionHashSendSkew)
	if minedAt := time.Unix(int64(header.Time), 0); minedAt.Before(sent) {
		return nil, fmt.Errorf("%w: the transaction was mined at %s, before the delivery was sent at %s",
			ErrITBadState, minedAt.UTC().Format(time.RFC3339), intent.UpdatedAt.UTC().Format(time.RFC3339))
	}
	var others int64
	if err := s.db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Where("LOWER(tx_hash) = LOWER(?) AND id <> ?", hash, rec.ID).Count(&others).Error; err != nil {
		return nil, err
	}
	if others > 0 {
		return nil, fmt.Errorf("%w: another order already holds that hash", ErrITBadState)
	}
	updates, err := handoffUpdates(rec, delivered, hash)
	if err != nil {
		return nil, err
	}
	updates["fault"] = ""
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.PaycrestOrderRecord{}).
			Where("id = ? AND status = ? AND holder = ? AND tx_hash = ?", rec.ID, models.PaycrestStatusRecoveryPending, models.HolderFlutterwave, "").
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: the record changed while the hash was checked", ErrITBadState)
		}
		return auditIntent(tx, intent.ID, adminID, models.ITAuditConversionHashAttached, note,
			map[string]any{"txHash": hash, "record": rec.ID.String()})
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[it converted buy] ALERT ramp_conversion_hash_attached intent=%s record=%s tx=%s admin=%s", intent.ID, rec.ID, hash, adminID)
	return conversionRecordOn(ctx, s.db, intent.ID)
}
