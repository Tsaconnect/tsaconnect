package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Opening a change and paying for it. A paid change is paid from the wallet
// linked to the account — one the account has proven it owns through the
// wallet-link proof (handlers/wallet_proof.go), checked by the handler before
// Create is called. That wallet is then the only one a stablecoin fee is
// accepted from, the one an MCGP hold is taken on, and the only one a refund can
// ever go to.

// nameChangeMCGPChain is where spendable MCGP lives (Contract B on Sonic).
const nameChangeMCGPChain = "sonic"

// nameChangePurchasePrefix marks a Didit session being bought (see ClaimSession).
const nameChangePurchasePrefix = "purchasing:"

// NameChangeRequest is how the user chose to pay. ProvenWallet is never read
// from the request body: the handler sets it to the account's linked wallet
// once requireProvenWallet has confirmed the account proved it owns it.
type NameChangeRequest struct {
	PayMethod    string `json:"payMethod"`
	Chain        string `json:"chain"`
	Token        string `json:"token"`
	ProvenWallet string `json:"-"`
}

// Create opens a change and starts paying for it. The account itself is not
// touched here or at any point before a verification passes.
//
// MCGP is debited at once (a hold, nothing to sign): the change comes back
// `paid`, or `paying` when the debit's outcome is not known yet — the sweep
// then settles it from chain state. A stablecoin change comes back `paying`
// with the transfer to make; ConfirmPayment proves it.
func (s *NameChangeService) Create(ctx context.Context, userID uuid.UUID, in NameChangeRequest) (*NameChangeCreated, error) {
	if !s.Enabled() {
		return nil, ErrNameChangeDisabled
	}
	u, err := s.loadUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.VerificationStatus.String != models.VerificationStatusVerified {
		return nil, ErrNameChangeNotVerified
	}
	if open, err := s.openChange(ctx, userID); err != nil {
		return nil, err
	} else if open != nil {
		return nil, ErrNameChangeOpen
	}
	if _, next, err := s.limit(ctx, userID); err != nil {
		return nil, err
	} else if next != nil {
		return nil, &NameChangeLimitError{NextAllowedAt: *next}
	}
	if blockers, err := s.Blockers(ctx, userID); err != nil {
		return nil, err
	} else if len(blockers) > 0 {
		return nil, &NameChangeBlockedError{Blockers: blockers}
	}

	now := s.now()
	ch := &models.UserNameChange{ID: uuid.New(), UserID: userID, FeeUSD: s.feeUSD(), FeeStatus: models.NameChangeFeeNone,
		OldName: u.Name.String, OldVerifiedName: strings.TrimSpace(u.VerifiedLegalName.String),
		OriginalDiditSessionID: strings.TrimSpace(u.DiditSessionID.String), ExpiresAt: now.Add(s.cfg.TTL),
		// Stamped from the service clock: a payment is held to having been
		// made after this moment, so it must be the same clock that judges it.
		CreatedAt: now}

	if s.free() {
		ch.PayMethod, ch.Status = models.NameChangePayFree, models.NameChangeStatusPaid
		if err := s.insert(ctx, ch); err != nil {
			return nil, err
		}
		s.publishStarted(ch)
		return &NameChangeCreated{Change: s.view(ch)}, nil
	}

	method := strings.TrimSpace(in.PayMethod)
	chain := nameChangeMCGPChain
	switch method {
	case models.NameChangePayMCGP:
	case models.NameChangePayStablecoin:
		chain = strings.ToLower(strings.TrimSpace(in.Chain))
	default:
		return nil, fmt.Errorf("%w: payMethod must be %q or %q", ErrNameChangeInvalid, models.NameChangePayMCGP, models.NameChangePayStablecoin)
	}
	// The handler proved the wallet; this holds it to still being the one on
	// the account, so no caller can pay (or be refunded) from any other.
	wallet := strings.TrimSpace(in.ProvenWallet)
	if !common.IsHexAddress(wallet) || !strings.EqualFold(wallet, strings.TrimSpace(u.WalletAddress.String)) {
		return nil, ErrNameChangeWalletUnproven
	}
	wallet = common.HexToAddress(wallet).Hex()
	if method == models.NameChangePayMCGP {
		return s.createMCGP(ctx, ch, wallet)
	}
	return s.createStable(ctx, ch, wallet, chain, in.Token)
}

func (s *NameChangeService) insert(ctx context.Context, ch *models.UserNameChange) error {
	if err := s.db.WithContext(ctx).Create(ch).Error; err != nil {
		// The one-open-change index: another request got there first.
		if isUniqueViolation(err) {
			return ErrNameChangeOpen
		}
		return err
	}
	return nil
}

func (s *NameChangeService) createMCGP(ctx context.Context, ch *models.UserNameChange, wallet string) (*NameChangeCreated, error) {
	if !s.mcgpAvailable() {
		return nil, ErrNameChangeMethodUnavailable
	}
	amount, err := s.mcgpAmount()
	if err != nil {
		log.Printf("[name-change] MCGP price unavailable: %v", err)
		return nil, ErrNameChangeMethodUnavailable
	}
	// Checked before anything is written, so a short balance is a plain
	// refusal rather than a cancelled change; Debit checks again before it
	// broadcasts.
	if bal, err := s.mcgp.PremiumBalance(wallet); err == nil && bal.Cmp(amount) < 0 {
		return nil, ErrNameChangeInsufficient
	}
	ref := s.debits.DebitRef(ch.ID)
	ch.Status, ch.PayMethod, ch.PayToken, ch.PayChain = models.NameChangeStatusPaying, models.NameChangePayMCGP, "MCGP", nameChangeMCGPChain
	ch.PayAmount, ch.PayDecimals, ch.PayFrom, ch.PayRef = amount.String(), 18, wallet, &ref
	// The change is written before the debit is broadcast: if the process dies
	// in between, this row is how the sweep knows a debit may exist.
	if err := s.insert(ctx, ch); err != nil {
		return nil, err
	}

	pay, err := s.debits.Debit(ctx, DebitInput{UserID: ch.UserID, OrderID: ch.ID, WalletAddress: wallet,
		McgpAmount: amount, AllowBought: amount})
	switch {
	case err == nil && pay != nil && pay.Status == models.BillPayStatusDebited:
		s.debitLanded(ctx, ch)
	case errors.Is(err, ErrBillPayInsufficient):
		s.closeUnpaid(ctx, ch, "not enough spendable MCGP")
		return nil, ErrNameChangeInsufficient
	case errors.Is(err, ErrTxNotBroadcast), errors.Is(err, ErrBillPayBadState), errors.Is(err, ErrBillPayDisabled):
		s.closeUnpaid(ctx, ch, "the debit was not sent")
		return nil, ErrNameChangePaymentFailed
	case err != nil:
		// Sent, but whether it landed is not known yet. The change stays
		// `paying` and the sweep settles it from the contract's debit record.
		log.Printf("[name-change] change=%s debit outcome unknown, left for the sweep: %v", ch.ID, err)
	}
	return &NameChangeCreated{Change: s.view(s.reload(ctx, ch))}, nil
}

// debitLanded moves a paying MCGP change to paid now its hold is confirmed —
// or, if the change was closed while the debit was in flight, returns the hold,
// which exists and now buys nothing.
func (s *NameChangeService) debitLanded(ctx context.Context, ch *models.UserNameChange) bool {
	if s.markPaid(ctx, ch, map[string]any{"fee_status": models.NameChangeFeeHeld}) {
		return true
	}
	if s.setFee(ctx, ch.ID, []string{models.NameChangeFeeNone}, models.NameChangeFeeHeld, "") {
		s.resolveFee(ctx, s.reload(ctx, ch))
	}
	return false
}

func (s *NameChangeService) createStable(ctx context.Context, ch *models.UserNameChange, wallet, chain, token string) (*NameChangeCreated, error) {
	var opt *StableFeeOption
	for _, o := range s.stableOptions(ctx) {
		if strings.EqualFold(o.Chain, chain) && strings.EqualFold(o.Token, strings.TrimSpace(token)) {
			o := o
			opt = &o
			break
		}
	}
	if opt == nil {
		return nil, ErrNameChangeMethodUnavailable
	}
	amount := stableForUSDMicros(s.feeMicros, opt.Decimals)
	ch.Status, ch.PayMethod, ch.PayToken, ch.PayChain = models.NameChangeStatusPaying, models.NameChangePayStablecoin, opt.Token, opt.Chain
	ch.PayAmount, ch.PayDecimals, ch.PayTokenAddress = amount.String(), opt.Decimals, opt.TokenAddress
	ch.PayTo, ch.PayFrom = s.rail.To(), wallet
	if err := s.insert(ctx, ch); err != nil {
		return nil, err
	}
	transfer := &NameChangeTransfer{Chain: opt.Chain, ChainID: opt.ChainID, Token: opt.Token, TokenAddress: opt.TokenAddress,
		From: wallet, To: ch.PayTo, Amount: displayAmount(amount, opt.Decimals), AmountBaseUnits: ch.PayAmount, Decimals: opt.Decimals}
	if raw, err := s.rail.PrepareTransfer(opt.Chain, opt.TokenAddress, wallet, amount); err == nil && json.Valid(raw) {
		transfer.UnsignedTx = raw
	}
	return &NameChangeCreated{Change: s.view(ch), Transfer: transfer}, nil
}

// markPaid moves a paying change to paid and starts its clock: the attempts
// window runs from when the fee was confirmed, not from when it was asked for.
// The user is told (email included) the moment it happens.
func (s *NameChangeService) markPaid(ctx context.Context, ch *models.UserNameChange, updates map[string]any) bool {
	all := map[string]any{"status": models.NameChangeStatusPaid, "expires_at": s.now().Add(s.cfg.TTL)}
	for k, v := range updates {
		all[k] = v
	}
	moved, err := s.transition(ctx, ch.ID, []string{models.NameChangeStatusPaying}, all)
	if err != nil {
		log.Printf("[name-change] change=%s could not be marked paid: %v", ch.ID, err)
	}
	if moved {
		s.publishStarted(s.reload(ctx, ch))
	}
	return moved
}

// closeUnpaid ends a change whose fee never arrived. Nothing was taken, so
// there is nothing to return.
func (s *NameChangeService) closeUnpaid(ctx context.Context, ch *models.UserNameChange, why string) bool {
	now := s.now()
	moved, err := s.transition(ctx, ch.ID, []string{models.NameChangeStatusPaying}, map[string]any{
		"status": models.NameChangeStatusCancelled, "review_note": truncateRunes("fee not paid: "+why, 512),
		"user_message": "This name change was closed because its fee wasn't paid.", "completed_at": &now})
	if err != nil {
		log.Printf("[name-change] change=%s could not be closed unpaid: %v", ch.ID, err)
	}
	return moved
}

// ─── stablecoin payment ──────────────────────────────────────────────────────

// stableOutcome is what one look at a stablecoin fee transaction settled.
type stableOutcome int

const (
	stablePending stableOutcome = iota // not mined, not deep enough, or unreadable
	stableRefuted                      // will never pay this change
	stableProven
)

// nameChangeStableReasons words ProveStableTransfer's reasons the way a
// name-change fee has always been explained (the review note reads these).
var nameChangeStableReasons = map[StableTransferReason]string{
	StableReasonUnreadable:   "not found on-chain yet",
	StableReasonShallow:      "waiting for confirmations",
	StableReasonNothingMoved: "the transaction moved none of the fee to the treasury from the proven wallet",
	StableReasonReverted:     "the transaction reverted or paid someone else",
	StableReasonWrongSender:  "sent by a wallet other than the one proven for this change",
	StableReasonTooEarly:     "made before this change was opened",
}

// checkStablePayment reads txHash and holds it to everything a fee for ch must
// be: the right token to the treasury, sent by the proven wallet itself, mined
// after the change was opened, and deep enough. txHash is the change's named
// payment, or the one it stopped waiting for (ClearedPayRef). The policy is
// the shared ProveStableTransfer.
func (s *NameChangeService) checkStablePayment(ctx context.Context, ch *models.UserNameChange, txHash string) (stableOutcome, *StablePayment, string) {
	if s.rail == nil || txHash == "" {
		return stablePending, nil, "no payment to check"
	}
	p, err := s.rail.VerifyPayment(ctx, ch.PayChain, txHash, ch.PayTokenAddress, ch.PayFrom, ch.PayTo)
	out := ProveStableTransfer(p, err, StableTransferWant{
		From:      ch.PayFrom,
		NotBefore: ch.CreatedAt.Add(-time.Minute), // a minute for clock skew between us and the chain
		MinConf:   uint64(s.minConfirmations(ch.PayChain)),
	})
	switch out.Reason {
	case StableReasonUnreadable:
		if err != nil && !errors.Is(err, ErrTransferNotFound) {
			log.Printf("[name-change] change=%s payment not readable yet: %s", ch.ID, RedactAlertText(err.Error()))
		}
		p = nil
	case StableReasonReverted:
		p = nil
	}
	switch out.Verdict {
	case StableTransferProven:
		return stableProven, p, ""
	case StableTransferRefuted:
		return stableRefuted, p, nameChangeStableReasons[out.Reason]
	}
	return stablePending, p, nameChangeStableReasons[out.Reason]
}

func (s *NameChangeService) minConfirmations(chain string) int {
	if n, ok := s.cfg.MinConfirmationsByChain[strings.ToLower(chain)]; ok && n > 0 {
		return n
	}
	return s.cfg.MinConfirmations
}

var (
	// errPayRefTaken: another change holds the transaction and its claim stands.
	errPayRefTaken = errors.New("name change: transaction already named by another change")
	// errPayRefUndecided: another change holds the transaction and the chain
	// cannot say yet whether that claim is good — the node has not seen the
	// transaction, or cannot be read. Asked again, it will be decided.
	errPayRefUndecided = errors.New("name change: another change's claim on the transaction can't be tested yet")
)

// ConfirmPayment records the user's stablecoin transaction and proves it.
//
// The hash is stored first, whatever the proof says: a transfer that is not
// mined yet is then still found by the sweep, and if the change closes before
// it lands the money is refunded rather than stranded. A stablecoin change that
// closed unpaid within the last few days also takes a late confirm — its
// payment is recorded and sent back — even while a change paid another way is
// open. A hash this change already refuted, or one we stopped waiting for on
// any of the user's changes, is answered from what is known of it instead of
// being taken again.
//
// "This transaction will never be taken" is always said as payment_mismatch
// (or no_open_change): the app stops asking about a hash only on those, so
// bad_state is kept for answers that may change.
func (s *NameChangeService) ConfirmPayment(ctx context.Context, userID uuid.UUID, txHash string) (*NameChangeView, error) {
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	if len(txHash) != 66 || !strings.HasPrefix(txHash, "0x") {
		return nil, fmt.Errorf("%w: txHash must be a 0x-prefixed transaction hash", ErrNameChangeInvalid)
	}
	if cleared, err := s.clearedWith(ctx, userID, txHash); err != nil {
		return nil, err
	} else if cleared != nil {
		return s.confirmCleared(ctx, cleared)
	}
	open, err := s.openChange(ctx, userID)
	if err != nil {
		return nil, err
	}
	ch := open
	if ch == nil || ch.PayMethod != models.NameChangePayStablecoin {
		// No stablecoin change open: the payment may be a late one for a
		// stablecoin change that closed unpaid.
		if ch, err = s.recentlyClosedUnpaid(ctx, userID); err != nil {
			return nil, err
		}
	}
	switch {
	case ch == nil && open == nil:
		return nil, ErrNameChangeNotFound
	case ch == nil:
		return nil, fmt.Errorf("%w: your name change is paid another way, so this transaction can't pay it", ErrNameChangePaymentMismatch)
	}
	if slices.Contains(ch.RefutedPayRefs, txHash) {
		// Already read and found never to pay this change: taking it again
		// would only refute it again, and a phone polling it would loop.
		return nil, fmt.Errorf("%w: that transaction was already checked and can't pay this fee", ErrNameChangePaymentMismatch)
	}
	if ch.PayRef != nil && *ch.PayRef != txHash {
		// Another transaction is already recorded. Only one that can never
		// pay (refuted) makes way for this one.
		if out, _, _ := s.checkStablePayment(ctx, ch, *ch.PayRef); out != stableRefuted {
			if ch.Status == models.NameChangeStatusPaying || ch.FeeStatus == models.NameChangeFeeNone {
				return nil, ErrNameChangePaymentPending
			}
			return nil, fmt.Errorf("%w: this name change's fee was paid by another transaction", ErrNameChangePaymentMismatch)
		}
		s.clearPayRef(ctx, ch, *ch.PayRef)
		ch = s.reload(ctx, ch)
	}
	if ch.PayRef == nil {
		if ch.Status != models.NameChangeStatusPaying && !(ch.Status == models.NameChangeStatusCancelled && ch.FeeStatus == models.NameChangeFeeNone) {
			return nil, ErrNameChangeBadState
		}
		_, err := s.claimPayRef(ctx, ch, txHash, func() *gorm.DB {
			return s.db.WithContext(ctx).Model(&models.UserNameChange{}).
				Where("id = ? AND pay_ref IS NULL AND fee_status = ?", ch.ID, models.NameChangeFeeNone).Update("pay_ref", txHash)
		})
		switch {
		case errors.Is(err, errPayRefTaken):
			return nil, fmt.Errorf("%w: that transaction already paid for another change", ErrNameChangePaymentMismatch)
		case errors.Is(err, errPayRefUndecided):
			return nil, ErrNameChangePaymentOnChain
		case err != nil:
			return nil, err
		}
		ch = s.reload(ctx, ch)
	}
	if ch.FeeStatus != models.NameChangeFeeNone {
		return s.view(ch), nil // already proven and recorded
	}
	switch out, _, reason := s.settleStablePayment(ctx, ch); out {
	case stablePending:
		if reason == "waiting for confirmations" {
			return nil, fmt.Errorf("%w (%s)", ErrNameChangePaymentOnChain, reason)
		}
		return nil, ErrNameChangePaymentOnChain
	case stableRefuted:
		return nil, fmt.Errorf("%w: %s", ErrNameChangePaymentMismatch, reason)
	}
	// Paid — or, when it came too late or too short to use, recorded and on its
	// way back; the change's status and message say which.
	return s.view(s.reload(ctx, ch)), nil
}

// claimPayRef runs write — an update storing txHash as a change's payment —
// and, when another change already holds that transaction, first tests the
// holder's claim (releaseSquattedRef) and tries once more. Without that, anyone
// could name a victim's pending transaction for their own change first and the
// victim could never record the payment they made. errPayRefTaken: the holder's
// claim stands; errPayRefUndecided: it cannot be tested yet. The bool is
// whether write moved a row.
func (s *NameChangeService) claimPayRef(ctx context.Context, claimant *models.UserNameChange, txHash string, write func() *gorm.DB) (bool, error) {
	res := write()
	if res.Error == nil {
		return res.RowsAffected == 1, nil
	}
	if !isUniqueViolation(res.Error) {
		return false, res.Error
	}
	if err := s.releaseSquattedRef(ctx, claimant, txHash); err != nil {
		return false, err
	}
	if res = write(); res.Error != nil {
		if isUniqueViolation(res.Error) {
			return false, errPayRefTaken
		}
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// releaseSquattedRef clears txHash from the change holding it when that claim
// can never pay the holder: the transaction is refuted for it, or — readable
// while the transaction is still pending — it was signed by a wallet other than
// the holder's, or it lives on the claimant's chain rather than the holder's.
// nil: released (or gone in between). errPayRefTaken: the holder keeps it — its
// fee is recorded, it is the user's own other change, or the chain shows the
// claim good (proven, or signed by the holder's wallet). errPayRefUndecided:
// the chain cannot tell yet — the node has not seen the transaction (the victim
// confirming a squatted hash before it reached our node), its signer cannot be
// recovered, or the chain cannot be read.
func (s *NameChangeService) releaseSquattedRef(ctx context.Context, claimant *models.UserNameChange, txHash string) error {
	var holder models.UserNameChange
	err := s.db.WithContext(ctx).Where("pay_ref = ?", txHash).First(&holder).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil // released in between
	case err != nil:
		return err
	case holder.ID == claimant.ID || holder.UserID == claimant.UserID ||
		holder.PayMethod != models.NameChangePayStablecoin || holder.FeeStatus != models.NameChangeFeeNone:
		return errPayRefTaken
	case s.rail == nil:
		return errPayRefUndecided
	}
	why := ""
	switch out, _, reason := s.checkStablePayment(ctx, &holder, txHash); out {
	case stableProven:
		return errPayRefTaken
	case stableRefuted:
		why = reason
	default:
		known, sender, err := s.rail.TransactionKnown(ctx, claimant.PayChain, txHash)
		switch {
		case err != nil:
			log.Printf("[name-change] change=%s claim on %s held by %s untestable: %v", claimant.ID, txHash, holder.ID, err)
			return errPayRefUndecided
		case !known:
			return errPayRefUndecided
		case holder.PayChain != claimant.PayChain:
			why = "the transaction is on " + claimant.PayChain + ", not " + holder.PayChain
		case sender == "":
			return errPayRefUndecided
		case !strings.EqualFold(sender, holder.PayFrom):
			why = "signed by " + sender + ", not the holder's proven wallet"
		default:
			return errPayRefTaken
		}
	}
	if !s.clearPayRef(ctx, &holder, txHash) {
		// Moved in between (proven, recorded, or cleared by another call): ask again.
		return errPayRefUndecided
	}
	log.Printf("[name-change] AUDIT name_change_pay_ref_released=1 tx=%s holder=%s holder_user=%s claimant=%s reason=%q",
		txHash, holder.ID, holder.UserID, claimant.ID, why)
	return nil
}

// recentlyClosedUnpaid is the user's latest stablecoin change that closed with
// no fee recorded, within the late-payment window. One already watching a
// transaction we stopped waiting for takes no other: that one is how its money
// comes back.
func (s *NameChangeService) recentlyClosedUnpaid(ctx context.Context, userID uuid.UUID) (*models.UserNameChange, error) {
	var rows []models.UserNameChange
	if err := s.db.WithContext(ctx).Where("user_id = ? AND status = ? AND pay_method = ? AND fee_status = ? AND completed_at > ? AND cleared_pay_ref IS NULL",
		userID, models.NameChangeStatusCancelled, models.NameChangePayStablecoin, models.NameChangeFeeNone,
		s.now().Add(-nameChangeLatePaymentWindow)).Order("completed_at desc").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// settleStablePayment acts on the stored transaction of a change whose fee is
// not yet recorded: a proven full payment marks a paying change paid; a proven
// payment the change can no longer use (underpaid, or it closed meanwhile) is
// recorded as received and refunded; a refuted one is cleared so another can be
// named. Shared by ConfirmPayment and the sweep, and race-safe between them:
// losing the paid transition to a close means refunding, never "mismatch".
func (s *NameChangeService) settleStablePayment(ctx context.Context, ch *models.UserNameChange) (stableOutcome, *StablePayment, string) {
	if ch.PayRef == nil {
		return stablePending, nil, "no payment to check"
	}
	out, p, reason := s.checkStablePayment(ctx, ch, *ch.PayRef)
	switch out {
	case stableRefuted:
		s.clearPayRef(ctx, ch, *ch.PayRef)
		return out, p, reason
	case stablePending:
		return out, p, reason
	}
	credited := p.Credited.String()
	want, _ := new(big.Int).SetString(ch.PayAmount, 10)
	if want != nil && p.Credited.Cmp(want) >= 0 && ch.Status == models.NameChangeStatusPaying &&
		s.markPaid(ctx, ch, map[string]any{"fee_status": models.NameChangeFeeReceived, "pay_credited": credited}) {
		return out, p, ""
	}
	// Not usable for this change: record what arrived and send it back —
	// unless it is too small to be worth sending, or the user has already had
	// their allowance of refunds, when it is kept and left to support.
	now := s.now()
	note, msg := "payment arrived after the change closed", "Your payment arrived after this name change closed, so it is being refunded."
	if want != nil && p.Credited.Cmp(want) < 0 {
		note, msg = "underpaid: "+credited+" of "+ch.PayAmount, "Your payment was less than the fee, so it is being refunded. Start again to change your name."
	}
	decision := nameChangeFeeBounce
	why, told := s.bounceRefusal(ctx, ch, p.Credited)
	if why != "" {
		decision = nameChangeFeeKeepAll
		note += "; not sent back: " + why
		msg = "Your payment couldn't be used for this name change, and " + told +
			". Contact support with your transaction and we'll sort it out."
	}
	s.transition(ctx, ch.ID, []string{models.NameChangeStatusPaying}, map[string]any{
		"status": models.NameChangeStatusCancelled, "completed_at": &now, "review_note": note, "user_message": msg})
	res := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where("id = ? AND status = ? AND fee_status = ?", ch.ID, models.NameChangeStatusCancelled, models.NameChangeFeeNone).
		Updates(map[string]any{"fee_status": models.NameChangeFeeReceived, "pay_credited": credited, "fee_decision": decision,
			"review_note": truncateRunes(note, 512), "user_message": msg})
	if res.Error == nil && res.RowsAffected == 1 {
		// Only the call that recorded the money reports it — a racing sweep
		// or confirm that lost the update did not keep anything.
		if why != "" {
			log.Printf("[name-change] ALERT name_change_bounce_kept_total=1 change=%s user=%s chain=%s tx=%s credited=%s reason=%q — for support",
				ch.ID, ch.UserID, ch.PayChain, *ch.PayRef, credited, why)
		}
		log.Printf("[name-change] change=%s stablecoin fee received but unusable (%s)", ch.ID, note)
		s.resolveFee(ctx, s.reload(ctx, ch))
	}
	return out, p, ""
}

// bounceRefusal says why a payment that cannot pay its change is NOT sent back
// (empty: it is): below the minimum refund, or past the user's refund
// allowance — bounces count toward it, so short or late payments cannot be
// used to make the treasury pay gas at will. why is for the record, told for
// the user. A limit that cannot be read refunds: the user's money is not kept
// on a database error.
func (s *NameChangeService) bounceRefusal(ctx context.Context, ch *models.UserNameChange, credited *big.Int) (why, told string) {
	if credited.Cmp(s.minRefund(ch)) < 0 {
		return "below the minimum refund", "it is too small to send back automatically"
	}
	next, err := s.refundLimit(ctx, ch.UserID)
	if err != nil {
		log.Printf("[name-change] change=%s refund allowance unreadable, refunding the bounce: %v", ch.ID, err)
		return "", ""
	}
	if next != nil {
		return "refund allowance used (next from " + next.UTC().Format("2006-01-02") + ")", "you've already had a refund recently"
	}
	return "", ""
}

// nameChangeRefutedKept bounds RefutedPayRefs: enough to answer a phone still
// polling its last few hashes, without letting a stream of bad hashes grow a row.
const nameChangeRefutedKept = 16

// withRefuted is the change's refuted hashes with txHash added (newest last).
// Read from a row locked for the write (updateRefuting), never a stale copy.
func withRefuted(ch *models.UserNameChange, txHash string) datatypes.JSONSlice[string] {
	out := datatypes.JSONSlice[string]{}
	for _, h := range ch.RefutedPayRefs {
		if h != txHash {
			out = append(out, h)
		}
	}
	out = append(out, txHash)
	if n := len(out); n > nameChangeRefutedKept {
		out = out[n-nameChangeRefutedKept:]
	}
	return out
}

// updateRefuting runs a guarded update of change id (where must select it)
// that also adds txHash to the change's refuted hashes. The hashes are re-read
// under a row lock in the same transaction, so refutations racing on one change
// — a named payment and a cleared one, say — each keep their entry. Reports
// whether the guarded update moved the row.
func (s *NameChangeService) updateRefuting(ctx context.Context, id uuid.UUID, txHash string, where func(*gorm.DB) *gorm.DB, updates map[string]any) (bool, error) {
	moved := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cur models.UserNameChange
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "refuted_pay_refs").
			Where("id = ?", id).First(&cur).Error; err != nil {
			return err
		}
		all := map[string]any{"refuted_pay_refs": withRefuted(&cur, txHash)}
		for k, v := range updates {
			all[k] = v
		}
		res := where(tx.Model(&models.UserNameChange{})).Updates(all)
		moved = res.Error == nil && res.RowsAffected == 1
		return res.Error
	})
	return moved, err
}

// clearPayRef forgets a transaction that can never pay this change, and
// remembers it as refuted. Reports whether it was this call that cleared it.
func (s *NameChangeService) clearPayRef(ctx context.Context, ch *models.UserNameChange, txHash string) bool {
	moved, err := s.updateRefuting(ctx, ch.ID, txHash, func(q *gorm.DB) *gorm.DB {
		return q.Where("id = ? AND pay_ref = ? AND fee_status = ?", ch.ID, txHash, models.NameChangeFeeNone)
	}, map[string]any{"pay_ref": nil})
	if err != nil {
		log.Printf("[name-change] change=%s could not clear a refuted payment: %v", ch.ID, err)
		return false
	}
	return moved
}

// ─── payments we stopped waiting for ─────────────────────────────────────────

// clearedWith is the user's change still watching txHash as a payment it
// stopped waiting for, or nil.
func (s *NameChangeService) clearedWith(ctx context.Context, userID uuid.UUID, txHash string) (*models.UserNameChange, error) {
	var rows []models.UserNameChange
	if err := s.db.WithContext(ctx).Where("user_id = ? AND cleared_pay_ref = ? AND pay_ref_cleared_at > ?",
		userID, txHash, s.now().Add(-nameChangeLatePaymentWindow)).Order("pay_ref_cleared_at desc").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// confirmCleared answers a confirm of a transaction the change stopped waiting
// for: settled now if it has landed, otherwise a mismatch that says it is
// still being watched — it is not this change's payment any more either way.
func (s *NameChangeService) confirmCleared(ctx context.Context, ch *models.UserNameChange) (*NameChangeView, error) {
	switch out := s.settleClearedPayment(ctx, ch); out {
	case stableProven:
		return s.view(s.reload(ctx, ch)), nil
	case stableRefuted:
		return nil, fmt.Errorf("%w: that transaction can't pay this fee", ErrNameChangePaymentMismatch)
	}
	return nil, fmt.Errorf("%w: this name change closed while that transaction was still unconfirmed; if it reaches the network it will be sent back to your wallet automatically",
		ErrNameChangePaymentMismatch)
}

// settleClearedPayment looks again at a transaction the change stopped waiting
// for. Refuted: it is forgotten. Proven: it landed after we gave up on it, and
// as our side closed the change the money goes back — a return_ops refund, not
// counted toward the user's refund allowance. An admin's clear goes back whole;
// one the sweep dropped as unknown to the chain is held to the minimum refund
// (below it: kept whole, for support), as a hash named first and broadcast only
// after the drop is otherwise a way to have the treasury return dust.
func (s *NameChangeService) settleClearedPayment(ctx context.Context, ch *models.UserNameChange) stableOutcome {
	if ch.ClearedPayRef == nil || ch.FeeStatus != models.NameChangeFeeNone {
		return stablePending
	}
	txHash := *ch.ClearedPayRef
	out, p, _ := s.checkStablePayment(ctx, ch, txHash)
	switch out {
	case stablePending:
		return out
	case stableRefuted:
		if _, err := s.updateRefuting(ctx, ch.ID, txHash, func(q *gorm.DB) *gorm.DB {
			return q.Where("id = ? AND cleared_pay_ref = ? AND fee_status = ?", ch.ID, txHash, models.NameChangeFeeNone)
		}, map[string]any{"cleared_pay_ref": nil}); err != nil {
			log.Printf("[name-change] change=%s could not forget a refuted cleared payment: %v", ch.ID, err)
		}
		return out
	}
	credited := p.Credited.String()
	decision, note := nameChangeFeeReturnOps, "payment "+txHash+" landed after we stopped waiting for it; returned"
	msg := "Your payment reached the network after this name change was closed, so it is being sent back to your wallet."
	if !ch.PayRefClearedByAdmin && p.Credited.Cmp(s.minRefund(ch)) < 0 {
		// Dropped by the sweep, not an admin: returned only from the minimum up.
		decision, note = nameChangeFeeKeepAll, "payment "+txHash+" landed after we stopped waiting for it; not sent back: below the minimum refund"
		msg = "Your payment reached the network after this name change was closed, and it is too small to send back automatically. " +
			"Contact support with your transaction and we'll sort it out."
	}
	moved, err := s.claimPayRef(ctx, ch, txHash, func() *gorm.DB {
		return s.db.WithContext(ctx).Model(&models.UserNameChange{}).
			Where("id = ? AND cleared_pay_ref = ? AND pay_ref IS NULL AND fee_status = ?", ch.ID, txHash, models.NameChangeFeeNone).
			Updates(map[string]any{"pay_ref": txHash, "cleared_pay_ref": nil, "fee_status": models.NameChangeFeeReceived,
				"pay_credited": credited, "fee_decision": decision, "review_note": truncateRunes(note, 512), "user_message": msg})
	})
	if errors.Is(err, errPayRefUndecided) {
		log.Printf("[name-change] change=%s cleared payment %s landed but another change's claim on it can't be tested yet; asking again later", ch.ID, txHash)
		return stablePending
	}
	if err != nil {
		alertf("name-change-cleared:"+ch.ID.String(),
			"[name-change] ALERT name_change_cleared_payment_unrecorded_total=1 change=%s tx=%s credited=%s — landed, but could not be recorded for its refund: %v",
			ch.ID, txHash, credited, err)
		return stablePending
	}
	if !moved {
		return stablePending
	}
	if decision == nameChangeFeeKeepAll {
		log.Printf("[name-change] ALERT name_change_bounce_kept_total=1 change=%s user=%s chain=%s tx=%s credited=%s reason=%q — for support",
			ch.ID, ch.UserID, ch.PayChain, txHash, credited, "landed after it was dropped; below the minimum refund")
	} else {
		log.Printf("[name-change] change=%s cleared payment %s landed (%s); returning it", ch.ID, txHash, credited)
	}
	s.resolveFee(ctx, s.reload(ctx, ch))
	return stableProven
}

// ─── cancel ──────────────────────────────────────────────────────────────────

// Cancel stops a change before its first verification attempt and gives the fee
// back: an MCGP hold is reversed, a stablecoin fee refunded to the proven wallet
// that paid it — at most MaxRefundedCancels times per RefundWindow. Once a
// session has been bought the fee is spent on it.
func (s *NameChangeService) Cancel(ctx context.Context, userID uuid.UUID) (*NameChangeView, error) {
	ch, err := s.openChange(ctx, userID)
	if err != nil {
		return nil, err
	}
	if ch == nil {
		return nil, ErrNameChangeNotFound
	}
	if ch.Status == models.NameChangeStatusPaying {
		switch {
		case ch.PayMethod == models.NameChangePayMCGP:
			// A debit may be landing; cancelling now could leave it held with
			// nobody to return it. The sweep settles the question first.
			return nil, ErrNameChangePaymentPending
		case ch.PayRef != nil:
			// A payment was named: it decides whether there is money to return.
			switch out, _, _ := s.settleStablePayment(ctx, ch); out {
			case stablePending:
				return nil, ErrNameChangePaymentPending
			case stableProven:
				if ch = s.reload(ctx, ch); ch.Status != models.NameChangeStatusPaid {
					return s.view(ch), nil // closed and refunding already
				}
			default:
				ch = s.reload(ctx, ch)
			}
		}
		if ch.Status == models.NameChangeStatusPaying {
			if !s.closeUnpaid(ctx, ch, "cancelled before paying") {
				return nil, ErrNameChangeBadState
			}
			return s.view(s.reload(ctx, ch)), nil
		}
	}
	if ch.Status != models.NameChangeStatusPaid || ch.Attempts != 0 {
		return nil, ErrNameChangeCancelNotAllowed
	}
	if ch.PayMethod != models.NameChangePayFree {
		if next, err := s.refundLimit(ctx, userID); err != nil {
			return nil, err
		} else if next != nil {
			return nil, &NameChangeLimitError{NextAllowedAt: *next, Cancel: true}
		}
	}
	now := s.now()
	msg := "You cancelled this name change. Your fee is being returned."
	if ch.PayMethod == models.NameChangePayFree {
		msg = "You cancelled this name change."
	}
	// Guarded on attempts too: a verification that started in between has
	// spent the fee, and must win.
	res := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where("id = ? AND status = ? AND attempts = 0", ch.ID, models.NameChangeStatusPaid).
		Updates(finished(map[string]any{"status": models.NameChangeStatusCancelled, "completed_at": &now, "fee_decision": nameChangeFeeReturn,
			"review_note": "cancelled by the user before verification", "user_message": msg}))
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, ErrNameChangeBadState
	}
	ch = s.reload(ctx, ch)
	s.resolveFee(ctx, ch)
	return s.view(s.reload(ctx, ch)), nil
}
