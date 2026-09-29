package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
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

// checkStablePayment reads the change's stored transaction and holds it to
// everything a fee must be: the right token to the treasury, sent by the proven
// wallet itself, mined after the change was opened, and deep enough.
func (s *NameChangeService) checkStablePayment(ctx context.Context, ch *models.UserNameChange) (stableOutcome, *StablePayment, string) {
	if s.rail == nil || ch.PayRef == nil {
		return stablePending, nil, "no payment to check"
	}
	p, err := s.rail.VerifyPayment(ctx, ch.PayChain, *ch.PayRef, ch.PayTokenAddress, ch.PayFrom, ch.PayTo)
	var credited *big.Int
	if p != nil {
		credited = p.Credited
	}
	switch ClassifyTransfer(credited, err) {
	case TransferRefuted:
		return stableRefuted, nil, "the transaction reverted or paid someone else"
	case TransferUnproven:
		if err != nil && !errors.Is(err, ErrTransferNotFound) {
			log.Printf("[name-change] change=%s payment not readable yet: %v", ch.ID, err)
		}
		return stablePending, nil, "not found on-chain yet"
	}
	if !strings.EqualFold(p.Sender, ch.PayFrom) {
		return stableRefuted, p, "sent by a wallet other than the one proven for this change"
	}
	if p.BlockTime.Before(ch.CreatedAt.Add(-time.Minute)) { // a minute for clock skew between us and the chain
		return stableRefuted, p, "made before this change was opened"
	}
	if p.Confirmations < uint64(s.minConfirmations(ch.PayChain)) {
		return stablePending, p, "waiting for confirmations"
	}
	return stableProven, p, ""
}

func (s *NameChangeService) minConfirmations(chain string) int {
	if n, ok := s.cfg.MinConfirmationsByChain[strings.ToLower(chain)]; ok && n > 0 {
		return n
	}
	return s.cfg.MinConfirmations
}

// ConfirmPayment records the user's stablecoin transaction and proves it.
//
// The hash is stored first, whatever the proof says: a transfer that is not
// mined yet is then still found by the sweep, and if the change closes before
// it lands the money is refunded rather than stranded. A change that closed
// unpaid within the last few days also takes a late confirm — its payment is
// recorded and sent back.
func (s *NameChangeService) ConfirmPayment(ctx context.Context, userID uuid.UUID, txHash string) (*NameChangeView, error) {
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	if len(txHash) != 66 || !strings.HasPrefix(txHash, "0x") {
		return nil, fmt.Errorf("%w: txHash must be a 0x-prefixed transaction hash", ErrNameChangeInvalid)
	}
	ch, err := s.openChange(ctx, userID)
	if err != nil {
		return nil, err
	}
	if ch == nil {
		if ch, err = s.recentlyClosedUnpaid(ctx, userID); err != nil {
			return nil, err
		}
		if ch == nil {
			return nil, ErrNameChangeNotFound
		}
	}
	if ch.PayMethod != models.NameChangePayStablecoin {
		return nil, ErrNameChangeBadState
	}
	if ch.PayRef != nil && *ch.PayRef != txHash {
		// Another transaction is already recorded. Only one that can never
		// pay (refuted) makes way for this one.
		if out, _, _ := s.checkStablePayment(ctx, ch); out != stableRefuted {
			if ch.Status == models.NameChangeStatusPaying || ch.FeeStatus == models.NameChangeFeeNone {
				return nil, ErrNameChangePaymentPending
			}
			return nil, ErrNameChangeBadState
		}
		s.clearPayRef(ctx, ch, *ch.PayRef)
		ch = s.reload(ctx, ch)
	}
	if ch.PayRef == nil {
		if ch.Status != models.NameChangeStatusPaying && !(ch.Status == models.NameChangeStatusCancelled && ch.FeeStatus == models.NameChangeFeeNone) {
			return nil, ErrNameChangeBadState
		}
		res := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
			Where("id = ? AND pay_ref IS NULL AND fee_status = ?", ch.ID, models.NameChangeFeeNone).Update("pay_ref", txHash)
		if res.Error != nil {
			if isUniqueViolation(res.Error) {
				return nil, fmt.Errorf("%w: that transaction already paid for another change", ErrNameChangePaymentMismatch)
			}
			return nil, res.Error
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

// recentlyClosedUnpaid is the user's latest stablecoin change that closed with
// no fee recorded, within the late-payment window.
func (s *NameChangeService) recentlyClosedUnpaid(ctx context.Context, userID uuid.UUID) (*models.UserNameChange, error) {
	var rows []models.UserNameChange
	if err := s.db.WithContext(ctx).Where("user_id = ? AND status = ? AND pay_method = ? AND fee_status = ? AND completed_at > ?",
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
	out, p, reason := s.checkStablePayment(ctx, ch)
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
	if why, told := s.bounceRefusal(ctx, ch, p.Credited); why != "" {
		decision = nameChangeFeeKeepAll
		note += "; not sent back: " + why
		msg = "Your payment couldn't be used for this name change, and " + told +
			". Contact support with your transaction and we'll sort it out."
		log.Printf("[name-change] ALERT name_change_bounce_kept_total=1 change=%s user=%s chain=%s tx=%s credited=%s reason=%q — for support",
			ch.ID, ch.UserID, ch.PayChain, *ch.PayRef, credited, why)
	}
	s.transition(ctx, ch.ID, []string{models.NameChangeStatusPaying}, map[string]any{
		"status": models.NameChangeStatusCancelled, "completed_at": &now, "review_note": note, "user_message": msg})
	res := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where("id = ? AND status = ? AND fee_status = ?", ch.ID, models.NameChangeStatusCancelled, models.NameChangeFeeNone).
		Updates(map[string]any{"fee_status": models.NameChangeFeeReceived, "pay_credited": credited, "fee_decision": decision,
			"review_note": truncateRunes(note, 512), "user_message": msg})
	if res.Error == nil && res.RowsAffected == 1 {
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

// clearPayRef forgets a transaction that can never pay this change.
func (s *NameChangeService) clearPayRef(ctx context.Context, ch *models.UserNameChange, txHash string) {
	if err := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where("id = ? AND pay_ref = ? AND fee_status = ?", ch.ID, txHash, models.NameChangeFeeNone).
		Update("pay_ref", nil).Error; err != nil {
		log.Printf("[name-change] change=%s could not clear a refuted payment: %v", ch.ID, err)
	}
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
