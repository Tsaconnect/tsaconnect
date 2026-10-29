package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// A verified user's name is the name on their ID document, so the only way to
// change it is to verify again with a document that says the new name. This
// service owns that change: the fee that pays for the re-verification, the
// attempts, the same-person evidence, and what happens to the account when one
// passes. The Didit calls stay with the KYC handler; this service is told what
// Didit decided.
//
// The account is never degraded while a change runs. It stays verified, payouts
// keep matching the OLD verified name, and a failed or abandoned change leaves
// every column on the user untouched — the worst a change can cost is its fee.
//
// The service is split by concern: views and the quote (name_change_views.go),
// opening and paying (name_change_pay.go), attempts and Didit's decision
// (name_change_attempts.go), what happens to the fee (name_change_settle.go),
// the sweeper (name_change_sweep.go) and admin review (name_change_admin.go).

var (
	ErrNameChangeDisabled          = errors.New("name changes aren't available right now")
	ErrNameChangeNotVerified       = errors.New("your identity isn't verified yet — edit your name in your profile")
	ErrNameChangeUserNotFound      = errors.New("account not found")
	ErrNameChangeOpen              = errors.New("you already have a name change in progress")
	ErrNameChangeNotFound          = errors.New("you don't have a name change in progress")
	ErrNameChangeInvalid           = errors.New("invalid name change request")
	ErrNameChangeWalletUnproven    = errors.New("confirm the wallet linked to your account is yours before paying")
	ErrNameChangeMethodUnavailable = errors.New("that payment method isn't available right now")
	ErrNameChangeInsufficient      = errors.New("you don't have enough spendable MCGP to pay the fee")
	ErrNameChangePaymentFailed     = errors.New("we couldn't take the fee — try again")
	ErrNameChangePaymentOnChain    = errors.New("your payment isn't confirmed on-chain yet — try again in a minute")
	ErrNameChangePaymentMismatch   = errors.New("that transaction doesn't pay this fee")
	ErrNameChangePaymentPending    = errors.New("your payment is still being confirmed")
	ErrNameChangeAttemptsExhausted = errors.New("you've used every verification attempt for this change")
	ErrNameChangeSessionLimit      = errors.New("you've started too many verifications recently — try again later")
	ErrNameChangeSessionPending    = errors.New("your verification is being prepared — try again in a moment")
	ErrNameChangeLapsed            = errors.New("this name change has expired")
	ErrNameChangeUnderReview       = errors.New("your new name is being reviewed")
	ErrNameChangeCancelNotAllowed  = errors.New("a name change can only be cancelled before verification starts")
	ErrNameChangeSelfReview        = errors.New("you can't review your own name change")
	ErrNameChangeBadState          = errors.New("this name change can't do that now")
	// Admin refusals on the money bookkeeping: each is what the chain showed.
	ErrNameChangePaymentLive       = errors.New("the chain still shows this payment — it has paid or may yet pay, so it can't be cleared")
	ErrNameChangeChainUnreadable   = errors.New("the chain couldn't be read — try again in a minute")
	ErrNameChangeRefundSentOnly    = errors.New("a refund can only be recorded as sent, with its transaction hash — if it never left, leave it flagged for support to resolve")
	ErrNameChangeRefundUnproven    = errors.New("that transaction isn't a mined refund of this fee from the treasury to the paying wallet")
	ErrNameChangeAccountUnverified = errors.New("the account is no longer verified, so its name can't be changed")
)

// NameChangeLimitError: the user has completed as many changes as a year allows
// (or, from Cancel, returned as many fees as the refund window allows).
type NameChangeLimitError struct {
	NextAllowedAt time.Time
	Cancel        bool
}

func (e *NameChangeLimitError) Error() string {
	day := e.NextAllowedAt.UTC().Format("2006-01-02")
	if e.Cancel {
		// The allowance is used by any returned fee — a cancel, or a payment
		// that could not be used and was sent back — so the message names
		// neither.
		return fmt.Sprintf("you've already had a name-change fee returned recently — you can cancel for a refund again from %s, or verify now", day)
	}
	return fmt.Sprintf("you can change your name again from %s", day)
}

// NameChangeBlockedError: money is still moving under the account's current
// identity, and a payout matched against the old name must not land on a
// changed one.
type NameChangeBlockedError struct{ Blockers []NameChangeBlocker }

func (e *NameChangeBlockedError) Error() string {
	return "finish what's in progress before changing your name"
}

// NameChangeBlocker is one thing the user must finish first.
type NameChangeBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	// nameChangeVerifyingGrace is how long past its expiry a change with a
	// Didit session still open is given to hear back: a decision Didit is
	// reviewing by hand can take a day, and a pass should not lose to the clock.
	nameChangeVerifyingGrace = 48 * time.Hour
	// nameChangeRefundAlertAfter is when a refund claimed for sending but never
	// recorded as sent or failed becomes a human's problem: it may have left.
	nameChangeRefundAlertAfter = 30 * time.Minute
	// A refund that provably never left is retried after nameChangeRefundRetryBase,
	// doubling each time up to nameChangeRefundRetryMax — a treasury that is
	// killed or out of float is not hammered every sweep. After
	// nameChangeRefundAlertAttempts failed sends (about an hour) a human hears
	// about it.
	nameChangeRefundRetryBase     = time.Minute
	nameChangeRefundRetryMax      = time.Hour
	nameChangeRefundAlertAttempts = 7
	// nameChangeVanishedAfter is how long past a paying change's expiry the
	// transaction named for it may stay unknown to the chain before it is
	// taken to have never been sent (dropped) and the change closes unpaid.
	nameChangeVanishedAfter = 24 * time.Hour
	// nameChangeLateDebitWindow bounds how far back a cancelled MCGP change is
	// checked for a debit that mined after it was given up on (as bills do),
	// and nameChangeLateDebitEvery how often: a debit that late is rare, and
	// every check is a contract read per row.
	nameChangeLateDebitWindow = 48 * time.Hour
	nameChangeLateDebitEvery  = 10 * time.Minute
	// nameChangeDebitGrace is how long a paying MCGP change with no debit row
	// yet is left alone: the request that opened it may still be about to debit.
	nameChangeDebitGrace = 30 * time.Minute
	// nameChangeLatePaymentWindow is how long after a change closed unpaid a
	// stablecoin payment for it is still looked for, and refunded.
	nameChangeLatePaymentWindow = 7 * 24 * time.Hour
	// nameChangeSessionWindow is the window MaxSessionsPer30Days counts over.
	nameChangeSessionWindow = 30 * 24 * time.Hour
	nameChangeSweepBatch    = 100
)

// NameChangeConfig is the policy, from config.
type NameChangeConfig struct {
	Enabled     bool
	FeeUSD      string
	MaxPerYear  int
	MaxAttempts int
	TTL         time.Duration
	// MaxSessionsPer30Days caps Didit sessions per user whatever the fee:
	// Didit bills per session, and a zero fee must not make them free to burn.
	MaxSessionsPer30Days int
	// MaxRefundedCancels fees (at least one) may be returned per user within
	// RefundWindow.
	MaxRefundedCancels int
	RefundWindow       time.Duration
	// RequireFaceMatch holds every change whose Didit decision does not show
	// the same face as the account's original KYC session (Didit's face
	// search). With it off the face result is still recorded for admins.
	RequireFaceMatch bool
	// MinConfirmations a stablecoin fee needs before it counts, per chain key
	// (MinConfirmationsByChain) or overall.
	MinConfirmations        int
	MinConfirmationsByChain map[string]int
	// MinRefundUSD is the smallest stablecoin amount sent back on its own (a
	// bounced payment, or the overpaid part of a kept fee); less is kept and
	// logged for support. Default 0.50.
	MinRefundUSD string
}

// nameChangeDefaultMinRefundUSD is MinRefundUSD when unset or unusable.
const nameChangeDefaultMinRefundUSD = "0.50"

// nameChangeDebits is the MCGP leg. A *BillPaymentService scoped with
// ForPurpose(models.BillPayPurposeNameChange) satisfies it.
type nameChangeDebits interface {
	IsEnabled() bool
	Debit(ctx context.Context, in DebitInput) (*models.BillPayment, error)
	Settle(ctx context.Context, orderID uuid.UUID) (*models.BillPayment, error)
	Reverse(ctx context.Context, orderID uuid.UUID) (*models.BillPayment, error)
	ResolveDebiting(ctx context.Context, orderID uuid.UUID) (string, bool, error)
	AdoptLateDebit(ctx context.Context, orderID uuid.UUID) (bool, error)
	AlertLateDebitsBeyondWindow(ctx context.Context) (int, error)
	DebitRef(orderID uuid.UUID) string
}

// nameChangeMCGP prices MCGP and reads a user's spendable balance.
// *MCGPSwapService satisfies it.
type nameChangeMCGP interface {
	Quote(kind uint8, mcgpAmount *big.Int) (*big.Int, error)
	PremiumBalance(user string) (*big.Int, error)
}

// NameChangeService runs name changes by re-verification.
type NameChangeService struct {
	db        *gorm.DB
	cfg       NameChangeConfig
	feeMicros *big.Int // nil when the configured fee is unusable (no new changes)
	// minRefundMicros is MinRefundUSD in USD micros.
	minRefundMicros *big.Int
	debits          nameChangeDebits
	mcgp            nameChangeMCGP
	rail            stableFeeRail
	bus             *events.Bus
	now             func() time.Time

	// scan pages the sweep's late-debit check; lateMu/lastLate space it out.
	scan     reconcileCursor
	lateMu   sync.Mutex
	lastLate time.Time
}

// NewNameChangeService builds the service with no way to pay yet: SetMCGP and
// SetStableRail each switch one payment method on, and a method never set is
// offered as unavailable. A fee that does not parse stops new changes (with an
// ALERT) rather than charging a guess; changes already running still finish.
func NewNameChangeService(db *gorm.DB, cfg NameChangeConfig, bus *events.Bus, now func() time.Time) *NameChangeService {
	if now == nil {
		now = time.Now
	}
	if cfg.MaxPerYear <= 0 {
		cfg.MaxPerYear = 2
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 7 * 24 * time.Hour
	}
	if cfg.MaxSessionsPer30Days <= 0 {
		cfg.MaxSessionsPer30Days = 6
	}
	if cfg.MaxRefundedCancels <= 0 {
		cfg.MaxRefundedCancels = 1
	}
	if cfg.RefundWindow <= 0 {
		cfg.RefundWindow = 30 * 24 * time.Hour
	}
	if cfg.MinConfirmations <= 0 {
		cfg.MinConfirmations = 1
	}
	s := &NameChangeService{db: db, cfg: cfg, bus: bus, now: now}
	if micros, err := ParseFeeUSDMicros(cfg.FeeUSD); err == nil {
		s.feeMicros = micros
	} else {
		log.Printf("[name-change] ALERT name_change_fee_invalid=1 NAME_CHANGE_FEE_USD=%q — no new name change can start until it is fixed: %v", cfg.FeeUSD, err)
	}
	minRefund, err := ParseFeeUSDMicros(cfg.MinRefundUSD)
	if strings.TrimSpace(cfg.MinRefundUSD) == "" || err != nil {
		if strings.TrimSpace(cfg.MinRefundUSD) != "" {
			log.Printf("[name-change] WARNING NAME_CHANGE_MIN_REFUND_USD=%q is not a usable amount; using %s: %v",
				cfg.MinRefundUSD, nameChangeDefaultMinRefundUSD, err)
		}
		minRefund, _ = ParseFeeUSDMicros(nameChangeDefaultMinRefundUSD)
	}
	s.minRefundMicros = minRefund
	return s
}

// SetMCGP lets the fee be paid from spendable MCGP: held through debits (a
// BillPaymentService scoped to name changes), priced and balance-checked
// through mcgp (the swap).
func (s *NameChangeService) SetMCGP(debits nameChangeDebits, mcgp nameChangeMCGP) {
	s.debits, s.mcgp = debits, mcgp
}

// SetStableRail lets the fee be paid in a stablecoin through rail.
func (s *NameChangeService) SetStableRail(rail stableFeeRail) { s.rail = rail }

// Enabled reports whether users may START name changes. Everything about a
// change already running — paying it, verifying, cancelling, the webhook, admin
// review, the sweep — works whatever it says.
func (s *NameChangeService) Enabled() bool {
	return s != nil && s.cfg.Enabled && s.feeMicros != nil
}

// MaxAttempts is how many Didit sessions one paid change allows.
func (s *NameChangeService) MaxAttempts() int { return s.cfg.MaxAttempts }

func (s *NameChangeService) free() bool { return s.feeMicros != nil && s.feeMicros.Sign() == 0 }

// FeeIsFree reports whether a new change costs nothing (and so needs no
// proven wallet).
func (s *NameChangeService) FeeIsFree() bool { return s.free() }

// ─── reads ───────────────────────────────────────────────────────────────────

// nameChangeUser is the slice of a user row a name change reads. Read through
// Table("users") so the model's PostgreSQL-only defaults stay out of it.
type nameChangeUser struct {
	ID                 uuid.UUID
	Name               sql.NullString
	VerificationStatus sql.NullString
	VerifiedLegalName  sql.NullString
	DiditSessionID     sql.NullString
	WalletAddress      sql.NullString
}

func (s *NameChangeService) loadUser(ctx context.Context, userID uuid.UUID) (*nameChangeUser, error) {
	var u nameChangeUser
	res := s.db.WithContext(ctx).Table("users").
		Select("id", "name", "verification_status", "verified_legal_name", "didit_session_id", "wallet_address").
		Where("id = ?", userID).Limit(1).Scan(&u)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNameChangeUserNotFound
	}
	return &u, nil
}

// openChange is the user's change that has not finished, if any.
func (s *NameChangeService) openChange(ctx context.Context, userID uuid.UUID) (*models.UserNameChange, error) {
	var ch models.UserNameChange
	err := s.db.WithContext(ctx).Where("user_id = ? AND status NOT IN ?", userID, models.NameChangeTerminalStatuses).
		Order("created_at desc").First(&ch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

func (s *NameChangeService) get(ctx context.Context, id uuid.UUID) (*models.UserNameChange, error) {
	var ch models.UserNameChange
	if err := s.db.WithContext(ctx).First(&ch, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNameChangeNotFound
		}
		return nil, err
	}
	return &ch, nil
}

// FindBySession is the change whose CURRENT Didit session is sessionID, or nil.
// An event from a session the change has moved on from is not the change's to
// act on, so only the current one is looked up.
func (s *NameChangeService) FindBySession(ctx context.Context, sessionID string) (*models.UserNameChange, error) {
	if s == nil || strings.TrimSpace(sessionID) == "" || strings.HasPrefix(sessionID, nameChangePurchasePrefix) {
		return nil, nil
	}
	var ch models.UserNameChange
	err := s.db.WithContext(ctx).Where("didit_session_id = ?", sessionID).First(&ch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

// IsNameChangeSession reports whether sessionID was ever bought for a name
// change — current or superseded. First-time KYC must never act on one: its
// approval would write the new document's name straight onto the account,
// skipping every same-person check. Only a definite answer is false.
func (s *NameChangeService) IsNameChangeSession(ctx context.Context, sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || sessionID == "" {
		return false, nil
	}
	// didit_session_ids is a JSON array of strings; matching its text for the
	// quoted id works the same on PostgreSQL (jsonb) and SQLite (tests).
	pattern := `%"` + LikeLiteral(sessionID) + `"%`
	var n int64
	err := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where(`didit_session_id = ? OR CAST(didit_session_ids AS TEXT) LIKE ? ESCAPE '\'`, sessionID, pattern).Count(&n).Error
	return n > 0, err
}

// NameSetByChange reports whether the user's current verified name came from a
// name change. First-time KYC's events for the ORIGINAL session (a redelivered
// approval, a reviewer's data edit) must then leave the name alone: that
// session describes the old document, and reading its name back would quietly
// undo the change. Fails closed — an unreadable table keeps the name.
func (s *NameChangeService) NameSetByChange(ctx context.Context, userID uuid.UUID) bool {
	if s == nil {
		return false
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where("user_id = ? AND status = ?", userID, models.NameChangeStatusApproved).Count(&n).Error; err != nil {
		log.Printf("[name-change] could not tell whether user %s changed their name; keeping it: %v", userID, err)
		return true
	}
	return n > 0
}

// limit reports whether the user has used up the year's changes, and when the
// next one becomes possible. A "year" is the 12 months before now, rolling.
func (s *NameChangeService) limit(ctx context.Context, userID uuid.UUID) (used int, next *time.Time, err error) {
	return s.rollingLimit(ctx, s.db.WithContext(ctx).Where("user_id = ? AND status = ?", userID, models.NameChangeStatusApproved),
		s.now().AddDate(-1, 0, 0), s.cfg.MaxPerYear, func(t time.Time) time.Time { return t.AddDate(1, 0, 0) })
}

// Blockers lists the money still moving under the user's current identity:
// TSA Pay payments being settled to their merchant, instant-transfer sells whose
// naira has not arrived, instant-transfer buys whose naira is being matched by
// the sender's name, and P2P trades where they are the seller (the buyer's
// naira comes in against their current name).
func (s *NameChangeService) Blockers(ctx context.Context, userID uuid.UUID) ([]NameChangeBlocker, error) {
	blockers := []NameChangeBlocker{}
	db := s.db.WithContext(ctx)

	var pay int64
	if err := db.Model(&models.PayPayment{}).
		Where("status = ? AND mode = ? AND merchant_id IN (?)", models.PayStatusProcessing, models.PayModeLive,
			db.Model(&models.PayMerchant{}).Select("id").Where("user_id = ?", userID)).
		Count(&pay).Error; err != nil {
		return nil, err
	}
	if pay > 0 {
		blockers = append(blockers, NameChangeBlocker{Code: "pay_settlement_in_flight",
			Message: fmt.Sprintf("%d TSA Pay payment(s) are still being paid out to your business", pay)})
	}

	var sells int64
	if err := db.Model(&models.ITIntent{}).
		Where("user_id = ? AND direction = ? AND status IN ?", userID, models.ITDirectionSell, models.ITSellAwaitingPayoutStatuses).
		Count(&sells).Error; err != nil {
		return nil, err
	}
	if sells > 0 {
		blockers = append(blockers, NameChangeBlocker{Code: "it_sell_in_flight",
			Message: fmt.Sprintf("%d sale(s) to your bank are still being paid", sells)})
	}

	var buys int64
	if err := db.Model(&models.ITIntent{}).
		Where("user_id = ? AND direction = ? AND status IN ?", userID, models.ITDirectionBuy, models.ITBuyAwaitingAttributionStatuses).
		Count(&buys).Error; err != nil {
		return nil, err
	}
	if buys > 0 {
		blockers = append(blockers, NameChangeBlocker{Code: "it_buy_in_flight",
			Message: fmt.Sprintf("%d purchase(s) paid by bank transfer are still waiting for your transfer or being matched to it", buys)})
	}

	var trades int64
	if err := db.Model(&models.P2PTrade{}).
		Where("seller_id = ? AND status IN ?", userID, models.P2PTradeLiveStatuses).
		Count(&trades).Error; err != nil {
		return nil, err
	}
	if trades > 0 {
		blockers = append(blockers, NameChangeBlocker{Code: "p2p_trade_open",
			Message: fmt.Sprintf("%d P2P trade(s) where you are selling are still open", trades)})
	}
	return blockers, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// transition applies updates only while the change is in one of from, so two
// callers (a webhook and the sweep, a cancel and an attempt) cannot both move it.
func (s *NameChangeService) transition(ctx context.Context, id uuid.UUID, from []string, updates map[string]any) (bool, error) {
	res := s.db.WithContext(ctx).Model(&models.UserNameChange{}).Where("id = ? AND status IN ?", id, from).Updates(finished(updates))
	return res.RowsAffected == 1, res.Error
}

// finished adds to a move into a terminal status what every such move owes:
// the dates of birth read for the same-person check are dropped once the
// change is decided — DOBMatch, whether they matched, is what stays on record.
// Every write that can close a change goes through it.
func finished(updates map[string]any) map[string]any {
	if status, ok := updates["status"].(string); ok && models.IsNameChangeTerminal(status) {
		updates["original_dob"], updates["new_dob"] = "", ""
	}
	return updates
}

func (s *NameChangeService) reload(ctx context.Context, ch *models.UserNameChange) *models.UserNameChange {
	if fresh, err := s.get(ctx, ch.ID); err == nil {
		return fresh
	}
	return ch
}

func (s *NameChangeService) publish(ch *models.UserNameChange, kind, title, message string) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{Type: kind, UserID: ch.UserID, Title: title, Message: message,
		Data: map[string]interface{}{"nameChangeId": ch.ID.String(), "status": ch.Status}})
}

// publishStarted tells the user — by email too — that a fee was taken and a
// change opened, so one they did not start is noticed.
func (s *NameChangeService) publishStarted(ch *models.UserNameChange) {
	s.publish(ch, events.NameChangeStarted, "Your name change has started",
		"Your fee is paid. Verify your identity with your new ID in the app to finish. If you didn't start this, change your password and contact support.")
}

func (s *NameChangeService) publishLapsed(ch *models.UserNameChange) {
	message := "Your name stays as it was."
	switch keep, _ := feeKept(ch); {
	case ch.PayMethod == models.NameChangePayFree || ch.FeeStatus == models.NameChangeFeeNone:
	case keep:
		message += " The fee was used for the verification attempts, so a new change needs a new fee."
	default:
		message += " Your fee is being returned."
	}
	s.publish(ch, events.NameChangeLapsed, "Your name change expired", message)
}
