package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrTreasurySendStalled: a sale leg has no proof past the stall window, or
	// its proof shows less than the amount. An operator looks; nothing re-sends
	// it automatically (spec §5.4).
	ErrTreasurySendStalled = errors.New("company wallet: a send has no proof past the stall window")
	// ErrTreasuryResendRefused: the dropped-transaction check did not pass.
	ErrTreasuryResendRefused = errors.New("company wallet: re-send refused — the last send may still land")
	// ErrTreasuryResendNeedsCancel: the stuck send's nonce is still unused, so
	// its signed transaction can still be mined — even if no node we ask has
	// it. Only consuming that nonce (a same-nonce zero-value cancel, per the
	// runbook) ends it. Always returned wrapped with ErrTreasuryResendRefused.
	ErrTreasuryResendNeedsCancel = errors.New("the stuck send can still land — broadcast a same-nonce zero-value cancel first (runbook), then retry")
	// errScanUnreliable: the RPC returned no Transfer logs for the token at all
	// over a range where a busy stablecoin always has some — it may be blind.
	errScanUnreliable = errors.New("the RPC returned none of the token's Transfer logs in a chunk's last blocks, so that chunk's empty answer proves nothing")
)

const (
	// saleScanChunk is the eth_getLogs span of a re-send's chain scan (halved
	// by logScanner when the RPC refuses it). Never one whole-range query: an
	// RPC has been seen to answer that with [] and no error.
	saleScanChunk       uint64 = 2_000
	saleScanCallTimeout        = 15 * time.Second
	// saleScanVisibleBlocks is each chunk's visibility window: the token's
	// unfiltered Transfer logs over a chunk's last 5 blocks must not be empty.
	saleScanVisibleBlocks uint64 = 5
)

// RampLegsOneLiveSaleIndexPresent reports whether uq_ramp_legs_one_live_sale
// exists on ramp_legs in this connection's schema — the database backstop
// against a double send. main.go keeps the company wallet dormant without it
// (review C1). Scoped to the schema and table, so an index of that name
// elsewhere in the database can never stand in for this one.
func RampLegsOneLiveSaleIndexPresent(db *gorm.DB) (bool, error) {
	var n int64
	switch db.Dialector.Name() {
	case "postgres":
		if err := db.Raw(`SELECT count(*) FROM pg_indexes
			WHERE schemaname = current_schema() AND tablename = 'ramp_legs' AND indexname = ?`,
			"uq_ramp_legs_one_live_sale").Scan(&n).Error; err != nil {
			return false, err
		}
	case "sqlite":
		if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'ramp_legs' AND name = ?",
			"uq_ramp_legs_one_live_sale").Scan(&n).Error; err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("cannot check indexes on %s", db.Dialector.Name())
	}
	return n > 0, nil
}

// saleDesk is one wallet that sells to buyers from its own lane (spec S20):
// the company wallet, or the TSA pool's view of the conversion treasury.
type saleDesk struct {
	wallet     *CompanyWallet
	label      string                                                                // "company wallet" | "TSA pool": refusal and log text
	tag        string                                                                // log prefix: "[it treasury buy]" | "[liquidity]"
	scope      func(*gorm.DB) *gorm.DB                                               // the provider filter (P7 lint forms only)
	owns       func(*models.ITIntent) bool                                           // providerTerms-based ownership
	checkOrder func(*models.ITIntent, *big.Int) error                                // per-order check at the send
	onSettle   func(tx *gorm.DB, intent *models.ITIntent, leg *models.RampLeg) error // extra writes in the settle transaction; nil = none
}

// companySale is the company wallet's desk; nil without a wallet.
func (s *ITService) companySale() *saleDesk {
	if !s.HasCompanyWallet() {
		return nil
	}
	return &saleDesk{wallet: s.wallet, label: "company wallet", tag: "[it treasury buy]",
		scope: scopeCompanyWalletBuys, owns: isCompanyWalletBuy, checkOrder: s.companyWalletOrderCheck}
}

// companyBooks is the company wallet's desk for the paths that never touch
// the chain (listing, settling): with no wallet it is the desk's records side
// alone, so those keep working while the wallet is dormant, as they always have.
func (s *ITService) companyBooks() *saleDesk {
	if sd := s.companySale(); sd != nil {
		return sd
	}
	return &saleDesk{label: "company wallet", tag: "[it treasury buy]", scope: scopeCompanyWalletBuys, owns: isCompanyWalletBuy}
}

// companyWalletOrderCheck is step 0 of a company-wallet send (spec §5.4): the
// per-order maximum, at the send itself. Every pricing path enforces it, and
// only an operator's recorded waiver (deliver anyway) lifts it. An amount
// above it here is a bug upstream, so it stops and alerts instead of paying
// out; not stock short, so the watcher reports it as a blocked send.
func (s *ITService) companyWalletOrderCheck(intent *models.ITIntent, units *big.Int) error {
	if maxUnits := s.treasuryLimits().maxUnits; units.Cmp(maxUnits) > 0 && !intent.MaxWaived {
		log.Printf("[it treasury buy] ALERT ramp_treasury_send_above_maximum intent=%s amount=%s max=%s — not sent; no waiver is recorded",
			intent.ID, units, maxUnits)
		return fmt.Errorf("%w: %s above the per-order maximum %s with no waiver recorded — not sent",
			ErrStableBuyAboveMaximum, FormatStableUnits(units.String()), FormatStableUnits(maxUnits.String()))
	}
	return nil
}

func (s *ITService) treasuryLegs(db *gorm.DB) *GormRampLegStore { return &GormRampLegStore{DB: db} }

// PendingTreasuryBuys lists company-wallet buys the watcher must advance:
// paid (to start sending) and delivering (to send or prove). It needs no
// wallet: the list is read even while the company wallet is dormant.
func (s *ITService) PendingTreasuryBuys(ctx context.Context) (received, delivering []uuid.UUID, err error) {
	return s.pendingSaleBuys(ctx, s.companyBooks())
}

// pendingSaleBuys lists the desk's buys the watcher must advance.
func (s *ITService) pendingSaleBuys(ctx context.Context, sd *saleDesk) (received, delivering []uuid.UUID, err error) {
	if sd == nil {
		return nil, nil, ErrTreasuryUnavailable
	}
	type row struct {
		ID     uuid.UUID
		Status string
	}
	var rows []row
	if err := sd.scope(s.db.WithContext(ctx)).Select("id", "status").
		Where("direction = ? AND status IN ?", models.ITDirectionBuy,
			[]string{models.ITStatusPaymentReceived, models.ITStatusDelivering}).
		Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if r.Status == models.ITStatusPaymentReceived {
			received = append(received, r.ID)
		} else {
			delivering = append(delivering, r.ID)
		}
	}
	return received, delivering, nil
}

// liveSaleLeg is the order's sale leg in flight or done, or nil.
func (s *ITService) liveSaleLeg(ctx context.Context, orderID uuid.UUID) (*models.RampLeg, error) {
	return liveSaleLegOn(ctx, s.db, orderID)
}

// liveSaleLegOn is liveSaleLeg read on db — a transaction holding the
// intent's row lock, for the send and refund paths.
func liveSaleLegOn(ctx context.Context, db *gorm.DB, orderID uuid.UUID) (*models.RampLeg, error) {
	var legs []models.RampLeg
	if err := db.WithContext(ctx).
		Where("order_id = ? AND kind = ? AND status IN ?", orderID, models.RampLegKindSale,
			[]string{models.RampLegStatusPending, models.RampLegStatusConfirmed}).
		Order("attempt DESC").Limit(1).Find(&legs).Error; err != nil {
		return nil, err
	}
	if len(legs) == 0 {
		return nil, nil
	}
	return &legs[0], nil
}

// saleIntent is the desk's own buy, or an error: no desk, or another
// provider's order.
func (s *ITService) saleIntent(ctx context.Context, sd *saleDesk, intentID uuid.UUID) (*models.ITIntent, error) {
	if sd == nil || sd.wallet == nil {
		return nil, ErrTreasuryUnavailable
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if intent.Direction != models.ITDirectionBuy || !sd.owns(intent) {
		return nil, ErrITBadState
	}
	return intent, nil
}

// SendTreasuryBuy starts a company-wallet delivery, at most once per order
// (spec §5.4). Safe to call every tick: with a live sale leg it does nothing.
func (s *ITService) SendTreasuryBuy(ctx context.Context, intentID uuid.UUID) error {
	return s.sendSale(ctx, s.companySale(), intentID)
}

// sendSale starts the desk's delivery of one order, at most once per order.
func (s *ITService) sendSale(ctx context.Context, sd *saleDesk, intentID uuid.UUID) error {
	// An owed send: it spends the treasury lane reservation held for it (pre-check and send alike).
	ctx = ReservedSend(ctx)
	intent, err := s.saleIntent(ctx, sd, intentID)
	if err != nil {
		return err
	}
	switch intent.Status {
	case models.ITStatusSettled:
		return nil
	case models.ITStatusPaymentReceived:
		res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
			Where("id = ? AND status = ?", intent.ID, models.ITStatusPaymentReceived).
			Update("status", models.ITStatusDelivering)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // another tick moved it
		}
	case models.ITStatusDelivering:
	default:
		return ErrITBadState
	}

	live, err := s.liveSaleLeg(ctx, intent.ID)
	if err != nil {
		return err
	}
	if live != nil {
		return nil // in flight or done: FinalizeTreasuryBuy observes it
	}
	// N1: a sale to the conversion treasury is a converted buy's leg and is
	// allowed only while its record waits for it — whatever is wired.
	rec, err := s.ConversionFor(ctx, intent)
	if err != nil {
		return err
	}
	switch {
	case rec == nil && sd.wallet.IsConversionTreasury(intent.DestAddress):
		log.Printf("%s ALERT ramp_treasury_send_to_conversion_without_record intent=%s", sd.tag, intent.ID)
		return fmt.Errorf("%w: a sale to the conversion treasury needs its conversion record", ErrITBadState)
	case rec != nil && rec.Status != models.PaycrestStatusAwaitingPayment && rec.Status != models.PaycrestStatusPending:
		log.Printf("%s ALERT ramp_treasury_send_to_conversion_without_record intent=%s record=%s status=%s",
			sd.tag, intent.ID, rec.ID, rec.Status)
		return fmt.Errorf("%w: its conversion record is %s", ErrITBadState, rec.Status)
	}
	chain, token := strings.ToLower(intent.DestChain), strings.ToUpper(intent.Asset)
	lane := sd.wallet.Lane(chain)
	if lane == nil || lane.SponsorLane == nil {
		return fmt.Errorf("%s: no lane on %s", sd.label, chain)
	}
	units, ok := new(big.Int).SetString(strings.TrimSpace(intent.AssetAmount), 10)
	if !ok || units.Sign() <= 0 {
		return fmt.Errorf("it treasury buy: intent %s has no asset amount", intent.ID)
	}
	// 0. The desk's per-order check, at the send itself.
	if err := sd.checkOrder(intent, units); err != nil {
		return err
	}
	tr := sd.wallet.Treasury()
	// 1. Kill switch (the lane has no value caps: CompanyWalletCaps, D63).
	// 2. Gas. 3. Fresh stock, never part.
	if err := tr.CheckCaps(chain, StableFromBaseUnits(units), 0); err != nil {
		return fmt.Errorf("%s: %w", sd.label, err)
	}
	if err := tr.requireGas(ctx, lane, treasuryERC20Gas); err != nil {
		return err
	}
	bal, err := sd.wallet.Balance(ctx, chain, token, true)
	if err != nil {
		return err
	}
	if bal.Cmp(units) < 0 {
		log.Printf("%s METRIC ramp_treasury_stock_short intent=%s chain=%s token=%s needed=%s balance=%s",
			sd.tag, intent.ID, chain, token, units, bal)
		return fmt.Errorf("%w: needed %s, balance %s", ErrTreasuryStockShort, units, bal)
	}
	amount, err := sd.wallet.ToTokenUnits(chain, token, units)
	if err != nil {
		return err
	}
	// 4. Where a re-send's chain scan would start (a read: nothing is sent).
	probe := sd.wallet.Probe(chain)
	if probe == nil {
		return fmt.Errorf("%s: no probe on %s", sd.label, chain)
	}
	fromBlock, err := probe.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("%s: head block on %s: %w", sd.label, chain, err)
	}
	// 5. Reserve the leg under a row lock on the intent, still delivering: an
	// operator's refund (MarkStableBuyRefunded) takes the same lock, so a
	// refunded order is never sent. The unique indexes settle concurrent ticks.
	legs := s.treasuryLegs(s.db)
	leg := &models.RampLeg{
		OrderID: intent.ID, Kind: models.RampLegKindSale,
		ChainKey: chain, Token: token, FromAddr: sd.wallet.Address(), ToAddr: intent.DestAddress,
		Amount: amount.String(), Status: models.RampLegStatusPending, CreatedAt: time.Now().UTC(), FromBlock: fromBlock,
	}
	var existing *models.RampLeg
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").
			First(&locked, "id = ?", intent.ID).Error; err != nil {
			return err
		}
		if locked.Status != models.ITStatusDelivering {
			return fmt.Errorf("%w: order is %s", ErrITBadState, locked.Status)
		}
		// Re-read under the lock: the index below is the backstop, not the
		// only gate.
		live, err := liveSaleLegOn(ctx, tx, intent.ID)
		if err != nil || live != nil {
			existing = live
			return err
		}
		// The same gate as a re-send, a refund and deliver-anyway: no earlier
		// attempt may still pay her.
		reason, err := s.saleGate(ctx, sd, tx, intent.ID, uuid.Nil)
		if err != nil {
			return err
		}
		if reason != "" {
			return fmt.Errorf("%w: %s", ErrITBadState, reason)
		}
		txLegs := s.treasuryLegs(tx)
		attempt, err := txLegs.NextAttempt(ctx, intent.ID, models.RampLegKindSale)
		if err != nil {
			return err
		}
		leg.Attempt = attempt
		existing, err = txLegs.Reserve(ctx, leg)
		return err
	})
	if err != nil {
		return err
	}
	if existing != nil {
		return nil // another tick holds the live sale
	}
	// 6. Send. The leg's nonce and hash are written in the key lane's own
	// allocation transaction, only while the leg is still pending: the leg and
	// the signed call commit together or not at all, so a refund that won the
	// leg first means nothing is ever signed, and a committed call always
	// shows on its leg. The leg is the operation: a retry of this same leg
	// never gets a new nonce.
	hash, attempted, err := tr.TransferTokenRecorded(WithOperationRef(ctx, "ramp_leg:"+leg.ID.String()), lane, token, intent.DestAddress, amount,
		func(hctx context.Context, nonce uint64, txHash string) error {
			res := LaneDB(hctx, s.db).Model(&models.RampLeg{}).
				Where("id = ? AND status = ?", leg.ID, models.RampLegStatusPending).
				Updates(map[string]any{"nonce": nonce, "tx_hash": txHash})
			if res.Error == nil && res.RowsAffected != 1 {
				return fmt.Errorf("leg %s is no longer pending", leg.ID)
			}
			return res.Error
		})
	if err != nil {
		if !attempted {
			// Nothing was committed: free the leg; the next tick sends attempt n+1.
			if ferr := legs.FailExpected(detached(ctx), leg.ID, "", "not broadcast: "+RedactAlertText(err.Error())); ferr != nil {
				log.Printf("%s ALERT ramp_treasury_leg_fail_unrecorded intent=%s leg=%s err=%s", sd.tag, intent.ID, leg.ID, RedactAlertText(ferr.Error()))
			}
			return err
		}
		// Committed and in flight (the lane keeps sending it): the leg already
		// carries its hash, and is only ever observed, never re-sent.
		log.Printf("%s ALERT ramp_treasury_broadcast_uncertain intent=%s leg=%s tx=%q err=%s", sd.tag, intent.ID, leg.ID, hash, RedactAlertText(err.Error()))
		return nil
	}
	log.Printf("%s METRIC ramp_treasury_sent intent=%s chain=%s token=%s amount=%s tx=%s", sd.tag, intent.ID, chain, token, amount, hash)
	return nil
}

// FinalizeTreasuryBuy proves a sent sale on chain and settles the order —
// only on a receipt showing wallet → her address ≥ the amount (spec §5.4).
func (s *ITService) FinalizeTreasuryBuy(ctx context.Context, intentID uuid.UUID) error {
	return s.finalizeSale(ctx, s.companySale(), intentID)
}

// finalizeSale proves the desk's sent sale on chain and settles the order.
func (s *ITService) finalizeSale(ctx context.Context, sd *saleDesk, intentID uuid.UUID) error {
	intent, err := s.saleIntent(ctx, sd, intentID)
	if err != nil {
		return err
	}
	if intent.Status != models.ITStatusDelivering {
		return nil
	}
	leg, err := s.liveSaleLeg(ctx, intent.ID)
	if err != nil || leg == nil {
		return err
	}
	if leg.Status == models.RampLegStatusConfirmed {
		return s.settleSale(ctx, sd, intent, leg, leg.AmountReceived)
	}
	stalled := time.Since(leg.CreatedAt) > s.treasuryLimits().sendStall
	if leg.TxHash == "" {
		if stalled {
			log.Printf("%s ALERT ramp_treasury_send_stalled intent=%s leg=%s tx=none", sd.tag, intent.ID, leg.ID)
			return fmt.Errorf("%w: leg %s never recorded a hash", ErrTreasurySendStalled, leg.ID)
		}
		return nil
	}
	verifier, probe := sd.wallet.Verifier(leg.ChainKey), sd.wallet.Probe(leg.ChainKey)
	if verifier == nil || probe == nil {
		return fmt.Errorf("%s: no verifier or probe on %s", sd.label, leg.ChainKey)
	}
	// A sale the key lane sped up mines under the replacement's hash: verify
	// (and record) whichever signing of the same call mined. One the lane
	// CANCELLED never executed: the leg fails and the next tick sends a new
	// attempt (a new operation, a new nonce) — paid exactly once.
	var cancelled bool
	leg.TxHash, cancelled = sd.wallet.Treasury().followCall(ctx, leg.ChainKey, probe, s.db, &models.RampLeg{}, "tx_hash", leg.ID, leg.TxHash)
	if cancelled && sd.wallet.Treasury().giveUp(ctx, leg.ChainKey, "ramp_leg:"+leg.ID.String(), leg.TxHash) {
		log.Printf("%s ALERT ramp_treasury_send_cancelled intent=%s leg=%s tx=%s — the key lane cancelled it; a new attempt follows", sd.tag, intent.ID, leg.ID, leg.TxHash)
		if err := s.treasuryLegs(s.db).FailExpected(ctx, leg.ID, leg.TxHash, "cancelled: "+ErrCallCancelled.Error()); err != nil && !errors.Is(err, ErrLegNotPending) {
			return err
		}
		return nil
	}
	gen := probe.EndpointGeneration() // the verdict and its depth come from one endpoint
	credited, verr := verifier.VerifyTransferFrom(ctx, leg.TxHash, sd.wallet.TokenAddress(leg.ChainKey, leg.Token),
		sd.wallet.Address(), intent.DestAddress)
	switch ClassifyTransfer(credited, verr) {
	case TransferProven:
		want, _ := new(big.Int).SetString(leg.Amount, 10)
		if want == nil || credited.Cmp(want) < 0 {
			// A short credit never improves: it is a stall, alerted at once.
			log.Printf("%s ALERT ramp_treasury_underdelivered intent=%s tx=%s credited=%s want=%s",
				sd.tag, intent.ID, leg.TxHash, credited, leg.Amount)
			return fmt.Errorf("%w: %s credited %s of %s", ErrTreasurySendStalled, leg.TxHash, credited, leg.Amount)
		}
		deep, err := receiptDeep(ctx, probe, leg.TxHash, types.ReceiptStatusSuccessful, s.saleDepth(), gen)
		if err != nil || !deep {
			return err // not deep enough yet: the next tick looks again
		}
		return s.settleSale(ctx, sd, intent, leg, credited.String())
	case TransferRefuted:
		// Reverted: it moved nothing — but only a receipt RAMP_TREASURY_CONFIRMATIONS
		// deep says so for good; a shallow one can be reorged into a success.
		// Until then the leg stays live: no re-send, no refund.
		deep, err := receiptDeep(ctx, probe, leg.TxHash, types.ReceiptStatusFailed, s.saleDepth(), gen)
		if err != nil || !deep {
			return err
		}
		// Free the leg; the next tick sends attempt n+1.
		log.Printf("%s METRIC ramp_treasury_send_reverted intent=%s tx=%s", sd.tag, intent.ID, leg.TxHash)
		if err := s.treasuryLegs(s.db).FailExpected(ctx, leg.ID, leg.TxHash, "reverted: "+verr.Error()); err != nil && !errors.Is(err, ErrLegNotPending) {
			return err
		}
		return nil
	default:
		if stalled {
			log.Printf("%s ALERT ramp_treasury_send_stalled intent=%s leg=%s tx=%s", sd.tag, intent.ID, leg.ID, leg.TxHash)
			return fmt.Errorf("%w: %s unproven", ErrTreasurySendStalled, leg.TxHash)
		}
		return nil
	}
}

// saleDepth is RAMP_TREASURY_CONFIRMATIONS, at least 1: the depth a sale's
// receipt needs before a settle or a revert is final (review minor).
func (s *ITService) saleDepth() uint64 {
	return uint64(max(1, s.treasuryLimits().confirmations))
}

// settleTreasuryBuy confirms the company-wallet leg and settles the intent.
func (s *ITService) settleTreasuryBuy(ctx context.Context, intent *models.ITIntent, leg *models.RampLeg, received string) error {
	return s.settleSale(ctx, s.companyBooks(), intent, leg, received)
}

// settleSale confirms the leg and settles the intent in one transaction
// (Decision D55): never settled without the hash and the proof. The desk's
// onSettle writes join that transaction.
func (s *ITService) settleSale(ctx context.Context, sd *saleDesk, intent *models.ITIntent, leg *models.RampLeg, received string) error {
	if sd == nil {
		return ErrTreasuryUnavailable
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.treasuryLegs(tx).Confirm(ctx, leg.ID, received, time.Now()); err != nil {
			if !errors.Is(err, ErrLegNotPending) {
				return err
			}
			// Already terminal: settle only over a leg that is confirmed — never
			// over one an operator failed meanwhile (a re-send).
			var cur models.RampLeg
			if err := tx.Select("status").First(&cur, "id = ?", leg.ID).Error; err != nil {
				return err
			}
			if cur.Status != models.RampLegStatusConfirmed {
				return fmt.Errorf("%w: leg %s is %s, not settling over it", ErrITBadState, leg.ID, cur.Status)
			}
		}
		res := tx.Model(&models.ITIntent{}).
			Where("id = ? AND status = ?", intent.ID, models.ITStatusDelivering).
			Updates(map[string]any{"status": models.ITStatusSettled, "tx_hash": leg.TxHash, "failure_reason": ""})
		if res.Error != nil {
			return res.Error
		}
		// Decision C7: a converted buy hands its proven leg to the orchestrator
		// in this same transaction — never a settled intent with nothing
		// driving the bridge. No record: a direct buy, nothing to do.
		if err := s.handoffConversion(ctx, tx, intent, leg.TxHash); err != nil {
			return err
		}
		if sd.onSettle != nil {
			return sd.onSettle(tx, intent, leg)
		}
		return nil
	})
	if err == nil {
		log.Printf("%s METRIC ramp_treasury_settled intent=%s tx=%s", sd.tag, intent.ID, leg.TxHash)
	}
	return err
}

// ResendEvidence is what the dropped-transaction check saw (spec §5.4,
// review C1). The console shows it as is, so an operator sees exactly what
// the API decided on.
type ResendEvidence struct {
	TxHash        string  `json:"txHash,omitempty"`
	Nonce         *uint64 `json:"nonce,omitempty"`
	PendingNonce  uint64  `json:"pendingNonce"`
	LatestNonce   uint64  `json:"latestNonce"`
	HashKnown     bool    `json:"hashKnown"`
	NonceConsumed bool    `json:"nonceConsumed"`
	// NeedsCancel: the recorded nonce is unused at the head, so the signed
	// send can still land; the operator must consume it with a cancel first.
	NeedsCancel bool `json:"needsCancel"`
	// DeepBlock is H − (confirmations − 1); DeepNonce the wallet's nonce
	// there. A nonce counts as consumed only once DeepNonce > Nonce, so a
	// shallow reorg cannot hand it back to the original send.
	DeepBlock uint64 `json:"deepBlock,omitempty"`
	DeepNonce uint64 `json:"deepNonce,omitempty"`
	// ScanTrusted: the scan reached the head with every chunk proved visible.
	ScanTrusted    bool     `json:"scanTrusted"`
	ScannedFrom    uint64   `json:"scannedFrom,omitempty"`
	ScannedTo      uint64   `json:"scannedTo,omitempty"`
	TransfersFound []string `json:"transfersFound,omitempty"`
	// OtherOrdersTransfers: transfers to her the scan saw whose hash another
	// order's sale leg records — that order's payment, so not evidence here.
	OtherOrdersTransfers []string `json:"otherOrdersTransfers,omitempty"`
	Allowed              bool     `json:"allowed"`
	Reason               string   `json:"reason"`
}

// resendEvidence runs the dropped-transaction check on a pending sale leg: the
// stall window first, then chainEvidence on the leg, then secondPaymentGate
// over every other sale of the order.
func (s *ITService) resendEvidence(ctx context.Context, intent *models.ITIntent, leg *models.RampLeg) (*ResendEvidence, error) {
	return s.saleEvidence(ctx, s.companySale(), intent, leg)
}

// saleEvidence is resendEvidence for the desk's sale leg.
func (s *ITService) saleEvidence(ctx context.Context, sd *saleDesk, intent *models.ITIntent, leg *models.RampLeg) (*ResendEvidence, error) {
	if stall := s.treasuryLimits().sendStall; time.Since(leg.CreatedAt) <= stall {
		return &ResendEvidence{TxHash: leg.TxHash, Nonce: leg.Nonce, Reason: fmt.Sprintf("inside the %s stall window", stall)}, nil
	}
	ev, err := s.chainEvidenceFor(ctx, sd, s.db, leg)
	if err != nil || !ev.Allowed {
		return ev, err
	}
	// The leg itself is proved above; every other sale of the order passes the
	// same gate as the automatic send, a refund and deliver-anyway.
	reason, err := s.saleGate(ctx, sd, s.db, intent.ID, leg.ID)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		ev.Allowed, ev.Reason = false, reason
	}
	return ev, nil
}

// chainEvidence asks the chain whether a signed sale can still land, or
// landed. Allowed only when nothing can and nothing did:
//  1. the node does not know the stored hash;
//  2. the wallet has nothing pending (pending nonce == the nonce at head H);
//  3. the leg's nonce is CONSUMED RAMP_TREASURY_CONFIRMATIONS deep — the
//     wallet's nonce at H − (confirmations − 1) is past it — so its signed
//     transaction can never be mined again, not even after a shallow reorg;
//  4. a chunked scan to exactly H finds no Transfer(wallet → her) since the
//     send, every chunk of it proved visible (ScanTrusted) — so whatever
//     consumed the nonce was not her payment. A transfer whose hash another
//     order's sale leg records is that order's payment and does not count
//     (she may have two orders to one address); one recorded nowhere does —
//     it may be this leg's own send, its hash lost.
//
// An unused nonce is never an allowance, however empty the mempool looks: a
// signed transaction our node forgot can resurface from a peer and win that
// nonce after a fresh attempt went out on the next one — paying her twice. It
// is refused with NeedsCancel (ErrTreasuryResendNeedsCancel) until a cancel
// consumes it. A leg with no recorded nonce was never signed (the nonce is
// written before the broadcast), so it needs only the trusted scan.
//
// The head is read once, so the nonce and the scan describe the same block —
// and every read must come from one RPC endpoint: a failover anywhere between
// the first read and the end of the scan makes the scan untrusted.
// db reads the other orders' sale legs (the caller's transaction, if any).
func (s *ITService) chainEvidenceFor(ctx context.Context, sd *saleDesk, db *gorm.DB, leg *models.RampLeg) (*ResendEvidence, error) {
	if sd == nil || sd.wallet == nil {
		return nil, ErrTreasuryUnavailable
	}
	ev := &ResendEvidence{TxHash: leg.TxHash, Nonce: leg.Nonce}
	probe := sd.wallet.Probe(leg.ChainKey)
	if probe == nil {
		return nil, fmt.Errorf("%s: no dropped-transaction probe on %s", sd.label, leg.ChainKey)
	}
	gen := probe.EndpointGeneration()
	if leg.TxHash != "" {
		// Every signing of the same call (a speed-up replaces the hash).
		for _, h := range sd.wallet.Treasury().keyLane(leg.ChainKey).signingsOf(ctx, leg.TxHash) {
			known, terr := txHashKnown(ctx, probe, h)
			if terr != nil {
				return nil, fmt.Errorf("%s: dropped check: %w", sd.label, terr)
			}
			if known {
				ev.HashKnown = true
				ev.Reason = "the node still knows " + h
				return ev, nil
			}
		}
	}
	addr := common.HexToAddress(sd.wallet.Address())
	head, pending, latest, err := walletNoncesAtHead(ctx, probe, addr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", sd.label, err)
	}
	ev.PendingNonce, ev.LatestNonce = pending, latest
	if ev.PendingNonce != ev.LatestNonce {
		ev.Reason = fmt.Sprintf("the wallet has %d pending transaction(s)", ev.PendingNonce-ev.LatestNonce)
		return ev, nil
	}
	if leg.Nonce != nil {
		if ev.LatestNonce <= *leg.Nonce {
			ev.NeedsCancel = true
			ev.Reason = fmt.Sprintf("nonce %d is still unused, so %s", *leg.Nonce, ErrTreasuryResendNeedsCancel)
			return ev, nil
		}
		// Consumed at H is not enough: the cancel the runbook sends lands at the
		// tip, and a shallow reorg could hand the nonce back to the original
		// send. It must be consumed as deep as a settle needs.
		need := uint64(s.treasuryLimits().confirmations)
		if need < 1 {
			need = 1
		}
		if head < need {
			ev.Reason = fmt.Sprintf("the chain head %d is shallower than %d confirmations", head, need)
			return ev, nil
		}
		ev.DeepBlock = head - (need - 1)
		if ev.DeepNonce, err = probe.NonceAt(ctx, addr, new(big.Int).SetUint64(ev.DeepBlock)); err != nil {
			return nil, fmt.Errorf("%s: nonce at block %d: %w", sd.label, ev.DeepBlock, err)
		}
		if ev.DeepNonce <= *leg.Nonce {
			ev.Reason = fmt.Sprintf("nonce %d is consumed at the head but not yet %d blocks deep (the wallet's nonce at block %d is %d) — wait",
				*leg.Nonce, need, ev.DeepBlock, ev.DeepNonce)
			return ev, nil
		}
		ev.NonceConsumed = true
	}
	found, others, from, serr := s.scanSaleTransfersFor(ctx, sd, db, probe, leg, head)
	if serr == nil {
		serr = endpointSteady(probe, gen)
	}
	ev.ScannedFrom, ev.ScannedTo, ev.OtherOrdersTransfers = from, head, others
	switch {
	case serr != nil:
		ev.Reason = "the chain scan could not be trusted: " + RedactAlertText(serr.Error())
	case len(found) > 0:
		ev.ScanTrusted = true
		ev.TransfersFound = found
		ev.Reason = "a transfer to her was mined since the send: " + strings.Join(found, ", ")
	default:
		ev.ScanTrusted, ev.Allowed = true, true
		ev.Reason = fmt.Sprintf("no transfer from the wallet to her in blocks %d–%d", from, head)
	}
	return ev, nil
}

// scanSaleTransfers lists Transfer(wallet → her) events of the leg's token
// from the leg's start block to exactly head, in chunks, leaving out (in
// others) any whose hash another order's sale leg records. Each chunk is proved
// visible on its own: an unfiltered Transfer query for the token over the
// chunk's last saleScanVisibleBlocks blocks must return logs, or that chunk's
// empty answer proves nothing and the scan fails (review N2;
// reference_sonic_rpc_logs). The window is small on purpose: a busy
// stablecoin has transfers in every few blocks, and an unfiltered query over
// thousands of blocks is exactly what RPCs cap or time out.
func (s *ITService) scanSaleTransfersFor(ctx context.Context, sd *saleDesk, db *gorm.DB, probe TreasuryTxProbe, leg *models.RampLeg, head uint64) (found, others []string, from uint64, err error) {
	if leg.FromBlock == 0 {
		return nil, nil, 0, errors.New("the send recorded no start block")
	}
	from = leg.FromBlock
	if head < from {
		return nil, nil, from, fmt.Errorf("head %d is before the send's block %d", head, from)
	}
	token := common.HexToAddress(sd.wallet.TokenAddress(leg.ChainKey, leg.Token))
	topic := common.HexToHash(erc20TransferTopic)
	q := ethereum.FilterQuery{
		Addresses: []common.Address{token},
		Topics: [][]common.Hash{{topic},
			{common.BytesToHash(common.HexToAddress(sd.wallet.Address()).Bytes())},
			{common.BytesToHash(common.HexToAddress(leg.ToAddr).Bytes())}},
	}
	scanner := newLogScanner(saleScanChunk)
	err = scanner.scanLogs(ctx, probe, q, from, head, saleScanCallTimeout, func(logs []types.Log, chunkEnd uint64) error {
		for _, lg := range logs {
			found = append(found, lg.TxHash.Hex())
		}
		lo := from
		if chunkEnd+1 >= saleScanVisibleBlocks && chunkEnd+1-saleScanVisibleBlocks > lo {
			lo = chunkEnd + 1 - saleScanVisibleBlocks
		}
		vctx, cancel := context.WithTimeout(ctx, saleScanCallTimeout)
		defer cancel()
		seen, verr := probe.FilterLogs(vctx, ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(lo), ToBlock: new(big.Int).SetUint64(chunkEnd),
			Addresses: []common.Address{token}, Topics: [][]common.Hash{{topic}},
		})
		if verr != nil {
			return fmt.Errorf("visibility check, blocks %d–%d: %w", lo, chunkEnd, verr)
		}
		if len(seen) == 0 {
			return fmt.Errorf("%w (blocks %d–%d)", errScanUnreliable, lo, chunkEnd)
		}
		return nil
	})
	if err != nil {
		return nil, nil, from, err
	}
	if len(found) == 0 {
		return nil, nil, from, nil
	}
	recorded, err := otherOrdersSaleHashes(ctx, db, leg, found)
	if err != nil {
		return nil, nil, from, err
	}
	var mine []string
	for _, h := range found {
		if recorded[strings.ToLower(h)] {
			others = append(others, h)
		} else {
			mine = append(mine, h)
		}
	}
	return mine, others, from, nil
}

// otherOrdersSaleHashes is the subset of hashes (lower-cased) that a sale leg
// of another order on the leg's chain records. Its own order's legs never
// count: an earlier attempt's transfer is her payment.
func otherOrdersSaleHashes(ctx context.Context, db *gorm.DB, leg *models.RampLeg, hashes []string) (map[string]bool, error) {
	lower := make([]string, len(hashes))
	for i, h := range hashes {
		lower[i] = strings.ToLower(h)
	}
	var recorded []string
	if err := db.WithContext(ctx).Model(&models.RampLeg{}).
		Where("kind = ? AND order_id <> ? AND chain_key = ? AND LOWER(tx_hash) IN ?",
			models.RampLegKindSale, leg.OrderID, leg.ChainKey, lower).
		Pluck("tx_hash", &recorded).Error; err != nil {
		return nil, fmt.Errorf("company wallet: read other orders' sales: %w", err)
	}
	out := make(map[string]bool, len(recorded))
	for _, h := range recorded {
		out[strings.ToLower(h)] = true
	}
	return out, nil
}

// secondPaymentGate is the one check before anything that could pay her a
// second time or hand her naira back (review N1, D61): the automatic send of a
// new attempt, an operator re-send, a refund of a company-wallet order and
// deliver-anyway all call it, so none can be looser than another. It returns
// "" only when no sale of the order can still pay her:
//   - no sale is live (pending or confirmed), except `except` — the stalled
//     leg an operator re-send replaces, which the caller proves itself with
//     chainEvidence;
//   - every failed sale moved nothing and never can:
//     "reverted" — its own receipt still reads failed and refuted, at least
//     RAMP_TREASURY_CONFIRMATIONS deep (a shallow revert can be reorged);
//     "dropped", or "not broadcast" — only chainEvidence's allowance counts:
//     with a recorded nonce (it was signed), that nonce consumed
//     RAMP_TREASURY_CONFIRMATIONS deep and a trusted scan to H with no
//     transfer to her; with none (never signed), the trusted scan alone.
//
// Anything else — an unused nonce, a blind scan, an unrecognised failure —
// refuses. An unreadable chain is an error, which every caller treats as a
// refusal.
func (s *ITService) secondPaymentGate(ctx context.Context, db *gorm.DB, intentID, except uuid.UUID) (string, error) {
	return s.saleGate(ctx, s.companySale(), db, intentID, except)
}

// saleGate is secondPaymentGate over the desk's wallet.
func (s *ITService) saleGate(ctx context.Context, sd *saleDesk, db *gorm.DB, intentID, except uuid.UUID) (string, error) {
	var legs []models.RampLeg
	if err := db.WithContext(ctx).Where("order_id = ? AND kind = ?", intentID, models.RampLegKindSale).
		Order("attempt ASC").Find(&legs).Error; err != nil {
		return "", err
	}
	for i := range legs {
		leg := &legs[i]
		if leg.Status != models.RampLegStatusFailed {
			if leg.ID == except && leg.Status == models.RampLegStatusPending {
				continue
			}
			return fmt.Sprintf("attempt %d is %s: a send is in flight or done — prove it or re-send it", leg.Attempt, leg.Status), nil
		}
		switch {
		case strings.HasPrefix(leg.Error, "not broadcast:"), strings.HasPrefix(leg.Error, "dropped:"):
			// A "not broadcast" leg with a nonce was signed, and another node
			// may still hold it: it is a dropped send in all but name. Without
			// a nonce it was never signed, and chainEvidence needs only the
			// trusted scan.
			if sd == nil || sd.wallet == nil {
				return "", ErrTreasuryUnavailable
			}
			ev, err := s.chainEvidenceFor(ctx, sd, db, leg)
			if err != nil {
				return "", err
			}
			if !ev.Allowed {
				return fmt.Sprintf("attempt %d may still land or have landed: %s", leg.Attempt, ev.Reason), nil
			}
		case strings.HasPrefix(leg.Error, "cancelled:"):
			// The key lane's record is the proof: the leg's nonce mined the
			// lane's cancel, so its call never executed.
			if sd == nil || sd.wallet == nil {
				return "", ErrTreasuryUnavailable
			}
			cancelled, err := sd.wallet.Treasury().keyLane(leg.ChainKey).CallCancelled(ctx, leg.TxHash)
			if err != nil {
				return "", err
			}
			if !cancelled {
				return fmt.Sprintf("attempt %d is not recorded as cancelled by the key lane", leg.Attempt), nil
			}
		case strings.HasPrefix(leg.Error, "reverted:"):
			if sd == nil || sd.wallet == nil {
				return "", ErrTreasuryUnavailable
			}
			deep, err := s.saleRefutedDeepFor(ctx, sd, leg)
			if err != nil {
				return "", err
			}
			if !deep {
				return fmt.Sprintf("attempt %d reverted, but its receipt is not yet %d blocks deep (or no longer refutes it)",
					leg.Attempt, s.treasuryLimits().confirmations), nil
			}
		default:
			return fmt.Sprintf("attempt %d failed for a reason that does not prove nothing moved (%q)", leg.Attempt, leg.Error), nil
		}
	}
	return "", nil
}

// saleRefutedDeep: the leg's own transaction still reads as refuted (reverted,
// or paid no one we sent to) and its receipt is RAMP_TREASURY_CONFIRMATIONS
// deep — the same depth a settle needs.
func (s *ITService) saleRefutedDeepFor(ctx context.Context, sd *saleDesk, leg *models.RampLeg) (bool, error) {
	if leg.TxHash == "" {
		return false, nil
	}
	verifier, probe := sd.wallet.Verifier(leg.ChainKey), sd.wallet.Probe(leg.ChainKey)
	if verifier == nil || probe == nil {
		return false, fmt.Errorf("%s: no verifier or probe on %s", sd.label, leg.ChainKey)
	}
	gen := probe.EndpointGeneration()
	credited, verr := verifier.VerifyTransferFrom(ctx, leg.TxHash, sd.wallet.TokenAddress(leg.ChainKey, leg.Token),
		sd.wallet.Address(), leg.ToAddr)
	if ClassifyTransfer(credited, verr) != TransferRefuted {
		return false, nil
	}
	return receiptDeep(ctx, probe, leg.TxHash, types.ReceiptStatusFailed, s.saleDepth(), gen)
}

// ResendTreasuryBuy is an operator's re-send of a stalled sale, allowed only
// when resendEvidence allows it. It fails the leg (audited, with the
// evidence); the next watcher tick sends a fresh attempt.
func (s *ITService) ResendTreasuryBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	return s.resendSale(ctx, s.companySale(), adminID, intentID, note)
}

// resendSale is an operator's re-send of the desk's stalled sale.
func (s *ITService) resendSale(ctx context.Context, sd *saleDesk, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	intent, err := s.saleIntent(ctx, sd, intentID)
	if err != nil {
		return nil, err
	}
	if intent.Status != models.ITStatusDelivering {
		return nil, ErrITBadState
	}
	leg, err := s.liveSaleLeg(ctx, intent.ID)
	if err != nil {
		return nil, err
	}
	if leg == nil || leg.Status != models.RampLegStatusPending {
		return nil, fmt.Errorf("%w: no pending send to replace", ErrITBadState)
	}
	ev, err := s.saleEvidence(ctx, sd, intent, leg)
	if err != nil {
		return nil, err
	}
	if ev.NeedsCancel {
		return nil, fmt.Errorf("%w: %w (nonce %d)", ErrTreasuryResendRefused, ErrTreasuryResendNeedsCancel, *leg.Nonce)
	}
	if !ev.Allowed {
		return nil, fmt.Errorf("%w: %s", ErrTreasuryResendRefused, ev.Reason)
	}
	if note = strings.TrimSpace(note); note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.treasuryLegs(tx).Fail(ctx, leg.ID, "dropped: operator re-send after the dropped-transaction check"); err != nil {
			return err
		}
		return auditIntent(tx, intent.ID, adminID, models.ITAuditStableBuyResent, note, map[string]any{
			"leg": leg.ID.String(), "attempt": leg.Attempt, "txHash": leg.TxHash, "evidence": ev,
		})
	}); err != nil {
		return nil, err
	}
	log.Printf("%s ALERT ramp_treasury_resend_authorised intent=%s leg=%s tx=%q admin=%s", sd.tag, intent.ID, leg.ID, leg.TxHash, adminID)
	return s.getIntent(ctx, intent.ID)
}
