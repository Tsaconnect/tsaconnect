package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The operator surface over held inbound credits (design §8).
//
// Under central-account attribution a `held` credit is real customer money that
// nobody can be shown to own. Decision 9 gives it NO automatic exit: nothing
// expires it, nothing refunds it, nothing retries. Every held credit is
// resolved by a human or not at all, which makes this file the whole recovery
// path for the money the matcher could not place.
//
// Two rules shape everything below:
//
//  1. Assignment goes through the matcher, never through an UPDATE here. A
//     hand-assigned credit and an auto-matched one must reach the product's
//     settle path by the same route, or the two will diverge the first time
//     that path changes.
//  2. Every action is written to an append-only audit row. inbound_credits
//     carries only the current state; who decided, when, and why has to
//     outlive it.

var (
	ErrInboundCreditNotFound = errors.New("inbound credit not found")
	// ErrInboundCreditNotHeld guards the two ways a resolved credit gets hurt:
	// re-binding one already attributed (stranding the first attribution) and
	// two operators resolving the same queue entry different ways.
	ErrInboundCreditNotHeld = errors.New("only a held credit can be resolved")
	// ErrInboundBinderUnavailable means no matcher is wired. Refusing is the
	// point — writing the status here instead is exactly the divergence rule 1
	// above exists to prevent.
	ErrInboundBinderUnavailable = errors.New("manual assignment is unavailable")
	ErrInboundBadRequest        = errors.New("invalid inbound credit request")
)

// inboundStatusAny is the ListCredits status that means "do not filter".
// Spelled out rather than using "" so an empty query string keeps its safer
// meaning: the hold queue.
const inboundStatusAny = "all"

// Why a candidate claim was not bound. These are what the console renders next
// to each near miss, and they are the four distinct things an operator has to
// tell apart when deciding where money goes.
const (
	// InboundRejectAmount — the names line up but the figures do not.
	InboundRejectAmount = "amount_differs"
	// InboundRejectName — the amount is right but the account name is not
	// evidence of the same person.
	InboundRejectName = "name_no_match"
	// InboundRejectNoKYCName — the amount is right and the user has no
	// Didit-verified name to compare against at all. Deliberately distinct
	// from InboundRejectName: "cannot attribute" is not "does not match", and
	// an operator who confuses the two rules out the wrong person.
	InboundRejectNoKYCName = "no_verified_name"
	// InboundRejectContention — this claim matched on both, and so did another.
	// This is the matcher refusing to rank, which is why a human is here.
	InboundRejectContention = "contention"
)

// inboundClaimTypes is the closed set Assign will accept. A claim type the
// matcher does not know is a typo, and a typo must not reach the bind path.
var inboundClaimTypes = map[string]struct{}{
	InboundClaimTypeITMCGP:   {},
	InboundClaimTypeITStable: {},
	InboundClaimTypeP2PTrade: {},
}

var inboundCreditStatuses = map[string]struct{}{
	models.InboundCreditUnmatched: {},
	models.InboundCreditMatched:   {},
	models.InboundCreditHeld:      {},
	models.InboundCreditRefunded:  {},
	models.InboundCreditIgnored:   {},
}

// InboundManualBinder is the narrow slice of the matcher this surface needs:
// one call that binds a credit to a claim exactly the way an automatic match
// would, plus the admin who asked for it.
type InboundManualBinder interface {
	BindManually(ctx context.Context, creditID uuid.UUID, claimType string, claimID uuid.UUID, adminID uuid.UUID) error
}

// InboundAdminService backs /api/admin/inbound.
type InboundAdminService struct {
	db     *gorm.DB
	binder InboundManualBinder
	// sources are read only to explain a hold — this service never calls
	// Accept. The claims carry their own Type, so there is no map here that
	// could disagree with what a source returns.
	sources []ClaimSource
	now     func() time.Time
}

func NewInboundAdminService(db *gorm.DB) *InboundAdminService {
	return &InboundAdminService{db: db, now: time.Now}
}

// SetBinder wires the matcher's manual bind path. Until it is called, Assign
// refuses; listing, near-miss evidence, refund and ignore all still work, so
// the queue is never invisible just because attribution is not yet wired.
// Mirrors how the poller takes its matcher.
func (s *InboundAdminService) SetBinder(b InboundManualBinder) { s.binder = b }

// AddClaimSource registers a product whose open claims should be considered
// when explaining a hold. Sources are additive and independent: one being
// absent narrows the evidence, it never breaks the queue.
func (s *InboundAdminService) AddClaimSource(src ClaimSource) {
	if src != nil {
		s.sources = append(s.sources, src)
	}
}

// InboundCreditQuery filters the credit list. A zero value is the hold queue,
// newest first — the screen an operator actually opens.
type InboundCreditQuery struct {
	// Status defaults to `held`; inboundStatusAny ("all") disables the filter.
	Status   string
	Currency string
	From     time.Time
	To       time.Time
	Limit    int
	Offset   int
}

// ListCredits returns one page of credits plus the unpaged total.
//
// The rows are models.InboundCredit as stored: the raw statement remark is on
// the record verbatim alongside the parsed bank and account name, and an
// operator deciding where money goes needs the text the bank actually wrote,
// not only our reading of it.
func (s *InboundAdminService) ListCredits(ctx context.Context, q InboundCreditQuery) ([]models.InboundCredit, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("inbound admin service: not configured")
	}

	status := strings.ToLower(strings.TrimSpace(q.Status))
	if status == "" {
		status = models.InboundCreditHeld
	}
	if status != inboundStatusAny {
		if _, ok := inboundCreditStatuses[status]; !ok {
			return nil, 0, fmt.Errorf("%w: unknown status %q", ErrInboundBadRequest, q.Status)
		}
	}

	tx := s.db.WithContext(ctx).Model(&models.InboundCredit{})
	if status != inboundStatusAny {
		tx = tx.Where("status = ?", status)
	}
	if cur := strings.ToUpper(strings.TrimSpace(q.Currency)); cur != "" {
		tx = tx.Where("currency = ?", cur)
	}
	// Filter on transaction_date, not created_at: an operator reconciling
	// against a bank statement is thinking in when the money landed, not when
	// our poller happened to notice it.
	if !q.From.IsZero() {
		tx = tx.Where("transaction_date >= ?", q.From)
	}
	if !q.To.IsZero() {
		tx = tx.Where("transaction_date <= ?", q.To)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var rows []models.InboundCredit
	// Tie-break on id: transaction_date has second granularity and a batch of
	// credits can share one, which would make paging skip or repeat rows.
	if err := tx.Order("transaction_date DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// InboundCandidateUser is the slim identity shown beside a candidate claim —
// deliberately not the whole models.User, and deliberately not the verified
// legal name (the token sets below carry the match evidence without opening a
// second surface onto document data).
type InboundCandidateUser struct {
	ID       uuid.UUID `json:"id"`
	Username string    `json:"username"`
	Email    string    `json:"email"`
}

// InboundCandidate is one open claim that nearly took this credit, with the
// evidence for and against it.
type InboundCandidate struct {
	ClaimType  string                `json:"claimType"`
	ClaimID    uuid.UUID             `json:"claimId"`
	UserID     uuid.UUID             `json:"userId"`
	User       *InboundCandidateUser `json:"user,omitempty"`
	AmountKobo int64                 `json:"amountKobo"`
	OpenedAt   time.Time             `json:"openedAt"`

	AmountMatches bool `json:"amountMatches"`
	// AmountDeltaKobo is claim minus credit at naira granularity: negative
	// means the claim asked for less than arrived.
	AmountDeltaKobo int64 `json:"amountDeltaKobo"`

	// NameStrength is the matcher's own verdict: exact | partial | none.
	NameStrength string `json:"nameStrength"`
	// KYCNameTokens and SharedNameTokens come from AccountNameTokens, the same
	// normalization the matcher compares with, so this view cannot drift into
	// explaining a decision by different rules than the one that made it.
	KYCNameTokens    []string `json:"kycNameTokens"`
	SharedNameTokens []string `json:"sharedNameTokens"`

	// Rejection is empty when this claim was bindable on its own — which means
	// the state changed after the hold, and is worth an operator's attention
	// rather than a fabricated reason.
	Rejection string `json:"rejection"`
}

// InboundCandidatesView is the evidence behind one hold.
type InboundCandidatesView struct {
	Credit models.InboundCredit `json:"credit"`
	// SenderNameTokens is the matcher's reading of the statement's account
	// name. Shown next to the raw remark because the gap between the two is
	// often the whole explanation.
	SenderNameTokens []string `json:"senderNameTokens"`
	// MatchedNairaKobo is what the amount rule actually compared: the gross
	// rounded to whole naira.
	MatchedNairaKobo int64              `json:"matchedNairaKobo"`
	Candidates       []InboundCandidate `json:"candidates"`
	// SourceErrors names products whose open claims could not be read. Without
	// it an operator would read a short list as "nothing else matched" when it
	// really means "we could not look".
	SourceErrors []string `json:"sourceErrors,omitempty"`
}

// Candidates explains one hold: the open claims that nearly matched, and why
// each was rejected.
//
// Only near misses are listed — a claim that agrees on neither amount nor name
// is not evidence of anything, and burying three real candidates under every
// open order in the system is how an operator picks the wrong one.
func (s *InboundAdminService) Candidates(ctx context.Context, creditID uuid.UUID) (*InboundCandidatesView, error) {
	credit, err := s.load(ctx, creditID)
	if err != nil {
		return nil, err
	}

	view := &InboundCandidatesView{
		Credit:           *credit,
		SenderNameTokens: AccountNameTokens(credit.SenderName),
		MatchedNairaKobo: inboundAdminRoundToNairaKobo(credit.GrossKobo),
		Candidates:       []InboundCandidate{},
	}

	claims := make([]InboundClaim, 0, 16)
	for _, src := range s.sources {
		got, err := src.OpenClaims(ctx, credit.Currency)
		if err != nil {
			// One product being down must not blank the queue. Record it and
			// keep going — partial evidence beats none, as long as the caller
			// is told it is partial.
			log.Printf("[inbound-admin] open claims unavailable for credit=%s: %v", creditID, err)
			view.SourceErrors = append(view.SourceErrors, err.Error())
			continue
		}
		claims = append(claims, got...)
	}
	if len(claims) == 0 {
		return view, nil
	}

	users, err := s.usersForClaims(ctx, claims)
	if err != nil {
		return nil, err
	}

	// Pass 1: score everything, so contention can be detected before any
	// rejection is written. A claim is only "contended" relative to the others.
	type scored struct {
		claim        InboundClaim
		amountOK     bool
		strength     NameMatchStrength
		hasKYCName   bool
		kycTokens    []string
		sharedTokens []string
	}
	senderSet := make(map[string]struct{}, len(view.SenderNameTokens))
	for _, tok := range view.SenderNameTokens {
		senderSet[tok] = struct{}{}
	}

	all := make([]scored, 0, len(claims))
	bindable := 0
	for _, claim := range claims {
		u := users[claim.UserID]
		kycName := ""
		if u != nil {
			kycName = u.VerifiedLegalName
		}
		kycTokens := AccountNameTokens(kycName)

		sc := scored{
			claim:      claim,
			amountOK:   claim.AmountKobo == view.MatchedNairaKobo,
			strength:   MatchAccountName(kycName, credit.SenderName),
			hasKYCName: len(kycTokens) > 0,
			kycTokens:  kycTokens,
		}
		for _, tok := range kycTokens {
			if _, shared := senderSet[tok]; shared {
				sc.sharedTokens = append(sc.sharedTokens, tok)
			}
		}
		if sc.amountOK && sc.strength.IsMatch() {
			bindable++
		}
		all = append(all, sc)
	}

	// Pass 2: turn scores into rejections.
	for _, sc := range all {
		nameOK := sc.strength.IsMatch()
		// Not a near miss at all: neither leg agrees, and nothing about it
		// helps an operator decide.
		if !sc.amountOK && !nameOK {
			continue
		}

		c := InboundCandidate{
			ClaimType:        sc.claim.Type,
			ClaimID:          sc.claim.ID,
			UserID:           sc.claim.UserID,
			AmountKobo:       sc.claim.AmountKobo,
			OpenedAt:         sc.claim.OpenedAt,
			AmountMatches:    sc.amountOK,
			AmountDeltaKobo:  sc.claim.AmountKobo - view.MatchedNairaKobo,
			NameStrength:     string(sc.strength),
			KYCNameTokens:    sc.kycTokens,
			SharedNameTokens: sc.sharedTokens,
		}
		if u := users[sc.claim.UserID]; u != nil {
			c.User = &InboundCandidateUser{ID: u.ID, Username: u.Username, Email: u.Email}
		}

		switch {
		case !sc.amountOK:
			c.Rejection = InboundRejectAmount
		case !sc.hasKYCName:
			c.Rejection = InboundRejectNoKYCName
		case !nameOK:
			c.Rejection = InboundRejectName
		case bindable > 1:
			c.Rejection = InboundRejectContention
		}
		view.Candidates = append(view.Candidates, c)
	}

	// Strongest evidence first: both legs agreeing, then the amount (which is
	// exact and unambiguous), then a name alone.
	sort.SliceStable(view.Candidates, func(i, j int) bool {
		return inboundCandidateRank(view.Candidates[i]) > inboundCandidateRank(view.Candidates[j])
	})
	return view, nil
}

func inboundCandidateRank(c InboundCandidate) int {
	rank := 0
	if c.AmountMatches {
		rank += 2
	}
	if c.NameStrength == string(NameMatchExact) {
		rank += 2
	} else if c.NameStrength == string(NameMatchPartial) {
		rank++
	}
	return rank
}

// usersForClaims loads only the columns the candidate view needs. Selecting the
// whole User would drag every unrelated field of a person's record into an
// endpoint about a payment.
func (s *InboundAdminService) usersForClaims(ctx context.Context, claims []InboundClaim) (map[uuid.UUID]*models.User, error) {
	ids := make([]uuid.UUID, 0, len(claims))
	seen := make(map[uuid.UUID]struct{}, len(claims))
	for _, c := range claims {
		if _, dup := seen[c.UserID]; dup || c.UserID == uuid.Nil {
			continue
		}
		seen[c.UserID] = struct{}{}
		ids = append(ids, c.UserID)
	}
	out := make(map[uuid.UUID]*models.User, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var users []models.User
	if err := s.db.WithContext(ctx).Model(&models.User{}).
		Select("id", "username", "email", "verified_legal_name").
		Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	for i := range users {
		out[users[i].ID] = &users[i]
	}
	return out, nil
}

// Assign attributes a held credit to a claim by hand.
//
// It delegates to the matcher and does NOT write the status itself. That is the
// whole contract of this endpoint: a manual match has to run the same bind and
// the same product settle path as an automatic one, so the two can never
// diverge as either side changes.
func (s *InboundAdminService) Assign(ctx context.Context, adminID, creditID uuid.UUID, claimType string, claimID uuid.UUID, note string) (*models.InboundCredit, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("inbound admin service: not configured")
	}
	if s.binder == nil {
		return nil, ErrInboundBinderUnavailable
	}
	claimType = strings.ToLower(strings.TrimSpace(claimType))
	if _, ok := inboundClaimTypes[claimType]; !ok {
		return nil, fmt.Errorf("%w: unknown claim type %q", ErrInboundBadRequest, claimType)
	}
	if claimID == uuid.Nil {
		return nil, fmt.Errorf("%w: claim id is required", ErrInboundBadRequest)
	}

	credit, err := s.load(ctx, creditID)
	if err != nil {
		return nil, err
	}
	// Pre-check only. The matcher owns the real guard — it is the one holding
	// the row when it writes — but failing here keeps an obviously wrong click
	// from reaching a product settle path at all.
	if credit.Status != models.InboundCreditHeld {
		return nil, fmt.Errorf("%w (status %s)", ErrInboundCreditNotHeld, credit.Status)
	}

	if err := s.binder.BindManually(ctx, creditID, claimType, claimID, adminID); err != nil {
		// No audit row: nothing happened, and a log claiming otherwise is worse
		// than no log.
		return nil, err
	}

	s.audit(ctx, creditID, adminID, models.InboundAuditAssigned, note, map[string]any{
		"claimType":     claimType,
		"claimId":       claimID,
		"grossKobo":     credit.GrossKobo,
		"currency":      credit.Currency,
		"heldReason":    credit.HoldReason,
		"providerRef":   credit.ProviderRef,
		"previousState": credit.Status,
	})

	// Re-read rather than assume: the matcher decides what a bound credit looks
	// like, and guessing here is how this surface starts telling operators
	// something the row does not say.
	return s.load(ctx, creditID)
}

// MarkRefunded records that an operator returned the money to the sender.
func (s *InboundAdminService) MarkRefunded(ctx context.Context, adminID, creditID uuid.UUID, note string) (*models.InboundCredit, error) {
	return s.resolve(ctx, adminID, creditID, models.InboundCreditRefunded, models.InboundAuditRefunded, note)
}

// MarkIgnored records that an operator ruled the credit was not customer money
// (own-float top-up, reversal, test credit).
func (s *InboundAdminService) MarkIgnored(ctx context.Context, adminID, creditID uuid.UUID, note string) (*models.InboundCredit, error) {
	return s.resolve(ctx, adminID, creditID, models.InboundCreditIgnored, models.InboundAuditIgnored, note)
}

// resolve moves a held credit to a terminal operator-decided state.
//
// Unlike Assign there is no matcher involved — nothing is being attributed —
// so the status write and its audit row happen here, together, in one
// transaction. Either both land or neither does; an unexplained status change
// on money is the one outcome not worth having.
func (s *InboundAdminService) resolve(ctx context.Context, adminID, creditID uuid.UUID, status, action, note string) (*models.InboundCredit, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("inbound admin service: not configured")
	}
	note = strings.TrimSpace(note)
	// Both claims are unverifiable after the fact: "refunded" asserts money
	// left the account, "ignored" asserts it was never the customer's. The note
	// is the only record of which transfer, or of what made it ours.
	if note == "" {
		return nil, fmt.Errorf("%w: a note is required — it is the only record of what was actually done", ErrInboundBadRequest)
	}

	var out *models.InboundCredit
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Conditional update, not read-then-write: two operators can be looking
		// at the same queue entry, and the loser has to be told rather than
		// quietly overwrite the winner.
		res := tx.Model(&models.InboundCredit{}).
			Where("id = ? AND status = ?", creditID, models.InboundCreditHeld).
			Updates(map[string]any{
				"status":      status,
				"resolved_by": adminID,
				"updated_at":  s.now().UTC(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// Distinguish "no such credit" from "someone got there first".
			var existing models.InboundCredit
			if err := tx.First(&existing, "id = ?", creditID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrInboundCreditNotFound
				}
				return err
			}
			return fmt.Errorf("%w (status %s)", ErrInboundCreditNotHeld, existing.Status)
		}

		var updated models.InboundCredit
		if err := tx.First(&updated, "id = ?", creditID).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.InboundCreditAuditLog{
			CreditID: creditID,
			ActorID:  &adminID,
			Action:   action,
			Note:     note,
			Metadata: inboundAuditMetadata(map[string]any{
				"grossKobo":     updated.GrossKobo,
				"currency":      updated.Currency,
				"providerRef":   updated.ProviderRef,
				"heldReason":    updated.HoldReason,
				"previousState": models.InboundCreditHeld,
			}),
			CreatedAt: s.now().UTC(),
		}).Error; err != nil {
			return err
		}
		out = &updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *InboundAdminService) load(ctx context.Context, creditID uuid.UUID) (*models.InboundCredit, error) {
	var credit models.InboundCredit
	if err := s.db.WithContext(ctx).First(&credit, "id = ?", creditID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInboundCreditNotFound
		}
		return nil, err
	}
	return &credit, nil
}

// audit writes the append-only record of an operator action.
//
// A failure here is logged, never returned. It is only reached after the action
// has already happened — the bind is done, the money has moved — and reporting
// a failure to the operator would invite them to retry a completed money
// movement. The credit's own ResolvedBy still names them; what is lost is the
// surrounding detail, which is worth a loud log and nothing more.
func (s *InboundAdminService) audit(ctx context.Context, creditID, adminID uuid.UUID, action, note string, meta map[string]any) {
	row := &models.InboundCreditAuditLog{
		CreditID:  creditID,
		ActorID:   &adminID,
		Action:    action,
		Note:      strings.TrimSpace(note),
		Metadata:  inboundAuditMetadata(meta),
		CreatedAt: s.now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		log.Printf("[inbound-admin] FAILED to write audit row action=%s credit=%s admin=%s: %v",
			action, creditID, adminID, err)
	}
}

func inboundAuditMetadata(meta map[string]any) datatypes.JSON {
	if len(meta) == 0 {
		return nil
	}
	blob, err := json.Marshal(meta)
	if err != nil {
		// Metadata is supplementary; the action, actor and time are on the row
		// itself and are what attribution actually rests on.
		log.Printf("[inbound-admin] audit metadata not serializable: %v", err)
		return nil
	}
	return datatypes.JSON(blob)
}

// inboundAdminRoundToNairaKobo rounds minor units to whole naira, matching the
// amount rule the matcher applies (design §5.4). Claims are minted at whole
// naira, so sub-naira differences are provider rounding rather than a different
// payment — and this view has to agree with the matcher about that or it will
// explain a hold by a rule that did not cause it.
func inboundAdminRoundToNairaKobo(kobo int64) int64 {
	if kobo < 0 {
		return -(((-kobo) + 50) / 100 * 100)
	}
	return (kobo + 50) / 100 * 100
}
