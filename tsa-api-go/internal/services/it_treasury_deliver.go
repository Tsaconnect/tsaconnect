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

func (s *ITService) treasuryLegs(db *gorm.DB) *GormRampLegStore { return &GormRampLegStore{DB: db} }

// PendingTreasuryBuys lists company-wallet buys the watcher must advance:
// paid (to start sending) and delivering (to send or prove).
func (s *ITService) PendingTreasuryBuys(ctx context.Context) (received, delivering []uuid.UUID, err error) {
	type row struct {
		ID     uuid.UUID
		Status string
	}
	var rows []row
	if err := s.db.WithContext(ctx).Model(&models.ITIntent{}).Select("id", "status").
		Where("provider = ? AND direction = ? AND status IN ?", models.ITProviderTreasury, models.ITDirectionBuy,
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

func (s *ITService) treasuryIntent(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	if !s.HasCompanyWallet() {
		return nil, ErrTreasuryUnavailable
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if intent.Direction != models.ITDirectionBuy || models.ITIntentProvider(intent) != models.ITProviderTreasury {
		return nil, ErrITBadState
	}
	return intent, nil
}

// SendTreasuryBuy starts a company-wallet delivery, at most once per order
// (spec §5.4). Safe to call every tick: with a live sale leg it does nothing.
func (s *ITService) SendTreasuryBuy(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.treasuryIntent(ctx, intentID)
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
	chain, token := strings.ToLower(intent.DestChain), strings.ToUpper(intent.Asset)
	lane := s.wallet.Lane(chain)
	if lane == nil || lane.SponsorLane == nil {
		return fmt.Errorf("company wallet: no lane on %s", chain)
	}
	units, ok := new(big.Int).SetString(strings.TrimSpace(intent.AssetAmount), 10)
	if !ok || units.Sign() <= 0 {
		return fmt.Errorf("it treasury buy: intent %s has no asset amount", intent.ID)
	}
	tr := s.wallet.Treasury()
	// 1. Kill switch (the lane has no value caps: CompanyWalletCaps, D63).
	// 2. Gas. 3. Fresh stock, never part.
	if err := tr.CheckCaps(chain, StableFromBaseUnits(units), 0); err != nil {
		return fmt.Errorf("company wallet: %w", err)
	}
	if err := tr.requireGas(ctx, lane, treasuryERC20Gas); err != nil {
		return err
	}
	bal, err := s.wallet.Balance(ctx, chain, token, true)
	if err != nil {
		return err
	}
	if bal.Cmp(units) < 0 {
		log.Printf("[it treasury buy] METRIC ramp_treasury_stock_short intent=%s chain=%s token=%s needed=%s balance=%s",
			intent.ID, chain, token, units, bal)
		return fmt.Errorf("%w: needed %s, balance %s", ErrTreasuryStockShort, units, bal)
	}
	amount, err := s.wallet.ToTokenUnits(chain, token, units)
	if err != nil {
		return err
	}
	// 4. Where a re-send's chain scan would start (a read: nothing is sent).
	probe := s.wallet.Probe(chain)
	if probe == nil {
		return fmt.Errorf("company wallet: no probe on %s", chain)
	}
	fromBlock, err := probe.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("company wallet: head block on %s: %w", chain, err)
	}
	// 5. Reserve the leg under a row lock on the intent, still delivering: an
	// operator's refund (MarkStableBuyRefunded) takes the same lock, so a
	// refunded order is never sent. The unique indexes settle concurrent ticks.
	legs := s.treasuryLegs(s.db)
	leg := &models.RampLeg{
		OrderID: intent.ID, Kind: models.RampLegKindSale,
		ChainKey: chain, Token: token, FromAddr: s.wallet.Address(), ToAddr: intent.DestAddress,
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
	// 6. Send, writing the nonce on the leg before the broadcast (review C1).
	// 7. Record the hash on a context that outlives ours.
	hash, attempted, err := tr.TransferTokenRecorded(ctx, lane, token, intent.DestAddress, amount, func(nonce uint64) error {
		res := s.db.WithContext(detached(ctx)).Model(&models.RampLeg{}).
			Where("id = ? AND status = ?", leg.ID, models.RampLegStatusPending).Update("nonce", nonce)
		if res.Error == nil && res.RowsAffected != 1 {
			return fmt.Errorf("leg %s is no longer pending", leg.ID)
		}
		return res.Error
	})
	if err != nil {
		if !attempted {
			// Provably never left: free the leg; the next tick sends attempt n+1.
			if ferr := legs.Fail(detached(ctx), leg.ID, "not broadcast: "+RedactAlertText(err.Error())); ferr != nil {
				log.Printf("[it treasury buy] ALERT ramp_treasury_leg_fail_unrecorded intent=%s leg=%s err=%s", intent.ID, leg.ID, RedactAlertText(ferr.Error()))
			}
			return err
		}
		if hash != "" {
			if serr := legs.SetTxHash(detached(ctx), leg.ID, hash, ""); serr != nil {
				log.Printf("[it treasury buy] ALERT ramp_treasury_hash_unrecorded intent=%s leg=%s tx=%s err=%s", intent.ID, leg.ID, hash, RedactAlertText(serr.Error()))
			}
		}
		// Uncertain: the leg stays pending and is only ever observed, never
		// re-sent by the machine.
		log.Printf("[it treasury buy] ALERT ramp_treasury_broadcast_uncertain intent=%s leg=%s tx=%q err=%s", intent.ID, leg.ID, hash, RedactAlertText(err.Error()))
		return nil
	}
	if err := legs.SetTxHash(detached(ctx), leg.ID, hash, ""); err != nil {
		log.Printf("[it treasury buy] ALERT ramp_treasury_hash_unrecorded intent=%s leg=%s tx=%s err=%s — record it by hand", intent.ID, leg.ID, hash, RedactAlertText(err.Error()))
		return err
	}
	log.Printf("[it treasury buy] METRIC ramp_treasury_sent intent=%s chain=%s token=%s amount=%s tx=%s", intent.ID, chain, token, amount, hash)
	return nil
}

// FinalizeTreasuryBuy proves a sent sale on chain and settles the order —
// only on a receipt showing wallet → her address ≥ the amount (spec §5.4).
func (s *ITService) FinalizeTreasuryBuy(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.treasuryIntent(ctx, intentID)
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
		return s.settleTreasuryBuy(ctx, intent, leg, leg.AmountReceived)
	}
	stalled := time.Since(leg.CreatedAt) > s.treasuryLimits().sendStall
	if leg.TxHash == "" {
		if stalled {
			log.Printf("[it treasury buy] ALERT ramp_treasury_send_stalled intent=%s leg=%s tx=none", intent.ID, leg.ID)
			return fmt.Errorf("%w: leg %s never recorded a hash", ErrTreasurySendStalled, leg.ID)
		}
		return nil
	}
	verifier := s.wallet.Verifier(leg.ChainKey)
	if verifier == nil {
		return fmt.Errorf("company wallet: no verifier on %s", leg.ChainKey)
	}
	credited, verr := verifier.VerifyTransferFrom(ctx, leg.TxHash, s.wallet.TokenAddress(leg.ChainKey, leg.Token),
		s.wallet.Address(), intent.DestAddress)
	switch ClassifyTransfer(credited, verr) {
	case TransferProven:
		want, _ := new(big.Int).SetString(leg.Amount, 10)
		if want == nil || credited.Cmp(want) < 0 {
			// A short credit never improves: it is a stall, alerted at once.
			log.Printf("[it treasury buy] ALERT ramp_treasury_underdelivered intent=%s tx=%s credited=%s want=%s",
				intent.ID, leg.TxHash, credited, leg.Amount)
			return fmt.Errorf("%w: %s credited %s of %s", ErrTreasurySendStalled, leg.TxHash, credited, leg.Amount)
		}
		deep, err := s.saleConfirmed(ctx, leg)
		if err != nil || !deep {
			return err // not deep enough yet: the next tick looks again
		}
		return s.settleTreasuryBuy(ctx, intent, leg, credited.String())
	case TransferRefuted:
		// Reverted: it moved nothing. Free the leg; the next tick sends attempt n+1.
		log.Printf("[it treasury buy] METRIC ramp_treasury_send_reverted intent=%s tx=%s", intent.ID, leg.TxHash)
		if err := s.treasuryLegs(s.db).Fail(ctx, leg.ID, "reverted: "+verr.Error()); err != nil && !errors.Is(err, ErrLegNotPending) {
			return err
		}
		return nil
	default:
		if stalled {
			log.Printf("[it treasury buy] ALERT ramp_treasury_send_stalled intent=%s leg=%s tx=%s", intent.ID, leg.ID, leg.TxHash)
			return fmt.Errorf("%w: %s unproven", ErrTreasurySendStalled, leg.TxHash)
		}
		return nil
	}
}

// saleConfirmed: the sale's receipt is at least RAMP_TREASURY_CONFIRMATIONS
// blocks deep (review minor). An unreadable receipt or head reads as "not yet".
func (s *ITService) saleConfirmed(ctx context.Context, leg *models.RampLeg) (bool, error) {
	probe := s.wallet.Probe(leg.ChainKey)
	if probe == nil {
		return false, fmt.Errorf("company wallet: no probe on %s", leg.ChainKey)
	}
	receipt, err := probe.TransactionReceipt(ctx, common.HexToHash(leg.TxHash))
	if err != nil || receipt == nil || receipt.BlockNumber == nil {
		return false, nil
	}
	head, err := probe.BlockNumber(ctx)
	if err != nil {
		return false, nil
	}
	mined := receipt.BlockNumber.Uint64()
	need := uint64(s.treasuryLimits().confirmations)
	return head >= mined && head-mined+1 >= need, nil
}

// settleTreasuryBuy confirms the leg and settles the intent in one
// transaction (Decision D55): never settled without the hash and the proof.
func (s *ITService) settleTreasuryBuy(ctx context.Context, intent *models.ITIntent, leg *models.RampLeg, received string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.treasuryLegs(tx).Confirm(ctx, leg.ID, received, time.Now()); err != nil && !errors.Is(err, ErrLegNotPending) {
			return err
		}
		return tx.Model(&models.ITIntent{}).
			Where("id = ? AND status = ?", intent.ID, models.ITStatusDelivering).
			Updates(map[string]any{"status": models.ITStatusSettled, "tx_hash": leg.TxHash, "failure_reason": ""}).Error
	})
	if err == nil {
		log.Printf("[it treasury buy] METRIC ramp_treasury_settled intent=%s tx=%s", intent.ID, leg.TxHash)
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
	// ScanTrusted: the scan reached the head with every chunk proved visible.
	ScanTrusted    bool     `json:"scanTrusted"`
	ScannedFrom    uint64   `json:"scannedFrom,omitempty"`
	ScannedTo      uint64   `json:"scannedTo,omitempty"`
	TransfersFound []string `json:"transfersFound,omitempty"`
	Allowed        bool     `json:"allowed"`
	Reason         string   `json:"reason"`
}

// resendEvidence runs the dropped-transaction check on a pending sale leg: the
// stall window first, then chainEvidence.
func (s *ITService) resendEvidence(ctx context.Context, intent *models.ITIntent, leg *models.RampLeg) (*ResendEvidence, error) {
	if stall := s.treasuryLimits().sendStall; time.Since(leg.CreatedAt) <= stall {
		return &ResendEvidence{TxHash: leg.TxHash, Nonce: leg.Nonce, Reason: fmt.Sprintf("inside the %s stall window", stall)}, nil
	}
	return s.chainEvidence(ctx, leg)
}

// chainEvidence asks the chain whether a signed sale can still land, or
// landed. Allowed only when nothing can and nothing did:
//  1. the node does not know the stored hash;
//  2. the wallet has nothing pending (pending nonce == the nonce at head H);
//  3. the leg's nonce is unused at H (nonce ≥ latest) — or, when it was
//     consumed or never recorded (the send may have been mined under a hash we
//     lost), a chunked scan to exactly H finds no Transfer(wallet → her) since
//     the send, every chunk of it proved visible (ScanTrusted).
//
// The head is read once, so the nonce and the scan describe the same block.
func (s *ITService) chainEvidence(ctx context.Context, leg *models.RampLeg) (*ResendEvidence, error) {
	ev := &ResendEvidence{TxHash: leg.TxHash, Nonce: leg.Nonce}
	probe := s.wallet.Probe(leg.ChainKey)
	if probe == nil {
		return nil, fmt.Errorf("company wallet: no dropped-transaction probe on %s", leg.ChainKey)
	}
	if leg.TxHash != "" {
		_, _, terr := probe.TransactionByHash(ctx, common.HexToHash(leg.TxHash))
		switch {
		case terr == nil:
			ev.HashKnown = true
			ev.Reason = "the node still knows " + leg.TxHash
			return ev, nil
		case !errors.Is(terr, ethereum.NotFound):
			return nil, fmt.Errorf("company wallet: dropped check: %w", terr)
		}
	}
	addr := common.HexToAddress(s.wallet.Address())
	head, err := probe.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("company wallet: head block: %w", err)
	}
	if ev.PendingNonce, err = probe.PendingNonceAt(ctx, addr); err != nil {
		return nil, fmt.Errorf("company wallet: pending nonce: %w", err)
	}
	if ev.LatestNonce, err = probe.NonceAt(ctx, addr, new(big.Int).SetUint64(head)); err != nil {
		return nil, fmt.Errorf("company wallet: nonce at block %d: %w", head, err)
	}
	if ev.PendingNonce != ev.LatestNonce {
		// A block mined after H reads as "pending" here: a refusal, never an allowance.
		ev.Reason = fmt.Sprintf("the wallet has %d pending transaction(s)", ev.PendingNonce-ev.LatestNonce)
		return ev, nil
	}
	if leg.Nonce != nil && ev.LatestNonce <= *leg.Nonce {
		ev.Allowed = true
		ev.Reason = fmt.Sprintf("nonce %d was never used: nothing with it is pending or mined", *leg.Nonce)
		return ev, nil
	}
	ev.NonceConsumed = leg.Nonce != nil
	found, from, serr := s.scanSaleTransfers(ctx, probe, leg, head)
	ev.ScannedFrom, ev.ScannedTo = from, head
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
// from the leg's start block to exactly head, in chunks. Each chunk is proved
// visible on its own: an unfiltered Transfer query for the token over the
// chunk's last saleScanVisibleBlocks blocks must return logs, or that chunk's
// empty answer proves nothing and the scan fails (review N2;
// reference_sonic_rpc_logs). The window is small on purpose: a busy
// stablecoin has transfers in every few blocks, and an unfiltered query over
// thousands of blocks is exactly what RPCs cap or time out.
func (s *ITService) scanSaleTransfers(ctx context.Context, probe TreasuryTxProbe, leg *models.RampLeg, head uint64) (found []string, from uint64, err error) {
	if leg.FromBlock == 0 {
		return nil, 0, errors.New("the send recorded no start block")
	}
	from = leg.FromBlock
	if head < from {
		return nil, from, fmt.Errorf("head %d is before the send's block %d", head, from)
	}
	token := common.HexToAddress(s.wallet.TokenAddress(leg.ChainKey, leg.Token))
	topic := common.HexToHash(erc20TransferTopic)
	q := ethereum.FilterQuery{
		Addresses: []common.Address{token},
		Topics: [][]common.Hash{{topic},
			{common.BytesToHash(common.HexToAddress(s.wallet.Address()).Bytes())},
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
		return nil, from, err
	}
	return found, from, nil
}

// failedSalesRefundable says whether a delivering company-wallet order whose
// sales all failed can be refunded without paying twice (review N1). A leg
// that never left ("not broadcast") or reverted moved nothing. A leg failed as
// "dropped" was signed, and its transaction can still land until its nonce is
// consumed by something else: refundable only when chainEvidence shows the
// nonce consumed and a trusted scan with no transfer to her. Anything else —
// an unrecorded nonce, an unrecognised failure — refuses. The reason is ""
// when the refund may go ahead.
func (s *ITService) failedSalesRefundable(ctx context.Context, db *gorm.DB, intentID uuid.UUID) (string, error) {
	var legs []models.RampLeg
	if err := db.WithContext(ctx).Where("order_id = ? AND kind = ? AND status = ?", intentID, models.RampLegKindSale,
		models.RampLegStatusFailed).Order("attempt ASC").Find(&legs).Error; err != nil {
		return "", err
	}
	for i := range legs {
		leg := &legs[i]
		switch {
		case strings.HasPrefix(leg.Error, "not broadcast:"), strings.HasPrefix(leg.Error, "reverted:"):
			continue
		case strings.HasPrefix(leg.Error, "dropped:"):
			if !s.HasCompanyWallet() {
				return "", ErrTreasuryUnavailable
			}
			ev, err := s.chainEvidence(ctx, leg)
			if err != nil {
				return "", err
			}
			if ev.Allowed && ev.NonceConsumed && ev.ScanTrusted {
				continue
			}
			if ev.Allowed && !ev.NonceConsumed {
				return fmt.Sprintf("attempt %d was signed and its nonce is still unused: it can still land", leg.Attempt), nil
			}
			return fmt.Sprintf("attempt %d was signed and may still land or have landed: %s", leg.Attempt, ev.Reason), nil
		default:
			return fmt.Sprintf("attempt %d failed for a reason that does not prove nothing moved (%q)", leg.Attempt, leg.Error), nil
		}
	}
	return "", nil
}

// ResendTreasuryBuy is an operator's re-send of a stalled sale, allowed only
// when resendEvidence allows it. It fails the leg (audited, with the
// evidence); the next watcher tick sends a fresh attempt.
func (s *ITService) ResendTreasuryBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	intent, err := s.treasuryIntent(ctx, intentID)
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
	ev, err := s.resendEvidence(ctx, intent, leg)
	if err != nil {
		return nil, err
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
	log.Printf("[it treasury buy] ALERT ramp_treasury_resend_authorised intent=%s leg=%s tx=%q admin=%s", intent.ID, leg.ID, leg.TxHash, adminID)
	return s.getIntent(ctx, intent.ID)
}
