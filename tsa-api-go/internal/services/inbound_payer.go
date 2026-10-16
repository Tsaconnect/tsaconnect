package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Payer declarations (owner decision 2026-10-16): a buyer whose naira came from
// an account that is not in their own name resolves it themselves. The
// statement carries the sender's name and bank but never their account number,
// so the number is only used for a name enquiry at the bank; a credit is
// attributed on a declaration only when the typed name, the bank's name for
// that account and the statement's sender name all agree, the sender bank is
// the declared one, the amount is the claim's, the money arrived after the
// claim opened, and nobody else's claim could have been owed it.

// PayerSelfResolveMaxKobo is the largest claim a declaration may settle
// without an operator: ₦500,000 (owner, 2026-10-16). Larger ones go to review.
// One constant for both paths into a declared bind, so the limit never depends
// on whether the money landed before or after the declaration.
const PayerSelfResolveMaxKobo int64 = 500_000 * 100

// NameMatchDeclared is the match strength recorded on a credit bound on a
// payer declaration rather than on the claim owner's own legal name.
const NameMatchDeclared NameMatchStrength = "declared"

// payerArrivalSkew tolerates the statement's clock running slightly behind
// ours: a credit stamped a moment before the claim opened is still this claim's.
const payerArrivalSkew = 2 * time.Minute

// payerBindResumeAfter is how long a credit may sit in `binding` before the
// matcher treats the bind as interrupted and finishes it. Far longer than any
// settle takes (the slowest wait on a chain receipt for minutes): Accept is
// idempotent once an earlier call has finished, not while one is still
// running, so a live bind must never be resumed alongside itself.
const payerBindResumeAfter = 30 * time.Minute

// payerDeclarationTTL closes a waiting declaration whose claim can no longer be
// paid: buy claims stop matching after a day.
const payerDeclarationTTL = 48 * time.Hour

var (
	ErrPayerBadInput        = errors.New("payer declaration: invalid details")
	ErrPayerClaimNotFound   = errors.New("payer declaration: no open payment for this order")
	ErrPayerAlreadyMatched  = errors.New("payer declaration: this order's payment is already matched")
	ErrPayerAccountNotFound = errors.New("payer declaration: the bank has no such account")
	ErrPayerUnknownBank     = errors.New("payer declaration: unknown bank")
	ErrPayerUnavailable     = errors.New("payer declaration: not available right now")
	ErrPayerLookupFailed    = errors.New("payer declaration: the bank could not be reached")
	// ErrPayerNameMismatch: the typed name is not the bank's name for the
	// account. The bank's name is deliberately NOT returned — that would make
	// this endpoint a name-enquiry oracle for any account number.
	ErrPayerNameMismatch = errors.New("payer declaration: the name does not match the bank's record")
)

// DeclarePayerResult is the declaration and what became of it.
type DeclarePayerResult struct {
	Declaration *models.InboundPayerDeclaration
	// Outcome is the declaration's status after the attempt: matched, waiting
	// or review.
	Outcome string
}

// InboundPayerService records declarations and settles what they prove.
type InboundPayerService struct {
	db      *gorm.DB
	matcher *InboundMatcher
	banks   BankDirectory
}

// NewInboundPayerService wires the service. A nil matcher or bank directory
// leaves it answering ErrPayerUnavailable rather than half-working.
func NewInboundPayerService(db *gorm.DB, matcher *InboundMatcher, banks BankDirectory) *InboundPayerService {
	return &InboundPayerService{db: db, matcher: matcher, banks: banks}
}

var payerAccountNumber = regexp.MustCompile(`^\d{10}$`)

// Declare records which account paid for the caller's open claim orderID, after
// the bank confirms the account's name, and binds a matching credit that has
// already arrived. orderID is the claim id: an IT intent (MCGP or stable/pool
// buy) or a P2P trade.
func (s *InboundPayerService) Declare(ctx context.Context, userID, orderID uuid.UUID, bankCode, accountNumber, accountName string) (*DeclarePayerResult, error) {
	if s == nil || s.db == nil || s.matcher == nil || s.banks == nil {
		return nil, ErrPayerUnavailable
	}
	bankCode = strings.TrimSpace(bankCode)
	accountNumber = strings.TrimSpace(accountNumber)
	accountName = strings.Join(strings.Fields(accountName), " ")
	if bankCode == "" || !payerAccountNumber.MatchString(accountNumber) || accountName == "" || len(accountName) > 128 {
		return nil, ErrPayerBadInput
	}

	claim, err := s.matcher.openClaimByID(ctx, "NGN", orderID)
	if err != nil {
		return nil, err
	}
	// Someone else's order reads exactly like no order: nothing about another
	// user's claim is confirmed to the caller.
	if claim == nil || claim.UserID != userID {
		var done int64
		s.db.WithContext(ctx).Model(&models.InboundPayerDeclaration{}).
			Where("claim_id = ? AND user_id = ? AND status = ?", orderID, userID, models.PayerDeclarationMatched).
			Count(&done)
		if done > 0 {
			return nil, ErrPayerAlreadyMatched
		}
		return nil, ErrPayerClaimNotFound
	}
	if err := requireVerifiedForMoneyIn(ctx, s.db, userID); err != nil {
		return nil, err
	}

	bankName, err := s.bankName(ctx, bankCode)
	if err != nil {
		return nil, err
	}
	looked, err := s.banks.Resolve(ctx, bankCode, accountNumber)
	if err != nil {
		if errors.Is(err, ErrNombaAccountNotFound) {
			return nil, ErrPayerAccountNotFound
		}
		return nil, fmt.Errorf("%w: %v", ErrPayerLookupFailed, err)
	}
	resolved := strings.Join(strings.Fields(looked.AccountName), " ")
	if resolved == "" {
		return nil, ErrPayerAccountNotFound
	}
	if !MatchAccountName(accountName, resolved).IsMatch() {
		return nil, ErrPayerNameMismatch
	}

	decl := &models.InboundPayerDeclaration{
		UserID: userID, ClaimType: claim.Type, ClaimID: claim.ID, AmountKobo: claim.AmountKobo,
		BankCode: bankCode, BankName: bankName, AccountNumber: accountNumber,
		TypedName: accountName, ResolvedName: resolved, Status: models.PayerDeclarationWaiting,
	}
	// A newer declaration replaces an unmatched one; a matched one is final.
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "claim_type"}, {Name: "claim_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"bank_code", "bank_name", "account_number", "typed_name", "resolved_name",
			"amount_kobo", "status", "review_reason", "credit_id", "updated_at",
		}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Neq{Column: clause.Column{Table: "inbound_payer_declarations", Name: "status"}, Value: models.PayerDeclarationMatched},
		}},
	}).Create(decl)
	if res.Error != nil {
		return nil, fmt.Errorf("payer declaration: save: %w", res.Error)
	}
	// Reload into a fresh row: when the upsert updated an existing declaration,
	// decl still carries the id minted for the insert that never happened.
	decl = &models.InboundPayerDeclaration{}
	if err := s.db.WithContext(ctx).
		Where("claim_type = ? AND claim_id = ?", claim.Type, claim.ID).First(decl).Error; err != nil {
		return nil, fmt.Errorf("payer declaration: reload: %w", err)
	}
	if decl.Status == models.PayerDeclarationMatched {
		return nil, ErrPayerAlreadyMatched
	}

	if err := s.settleArrived(ctx, *claim, decl); err != nil {
		// The declaration is saved: the money it describes is still found
		// when it next lands or when the buyer resubmits. Reported as saved,
		// with the failure logged where an engineer will see it.
		log.Printf("[inbound-payer] WARNING declaration=%s saved but the settle attempt failed: %v", decl.ID, err)
		_ = s.db.WithContext(ctx).First(decl, "id = ?", decl.ID).Error
	}
	return &DeclarePayerResult{Declaration: decl, Outcome: decl.Status}, nil
}

// settleArrived looks for a credit that already arrived for this declaration
// and binds it when exactly one fits, nobody else could be owed it, and the
// claim is within the self-resolve cap.
//
// Only credits the matcher ALREADY held because nobody's own name was on them.
// An unmatched credit has not been through the matcher's rules yet — its
// sender may be another buyer, whose own name would bind it — and is left to
// the matcher, which consults this declaration only when no candidate's name
// matches. Credits held for any other reason (two names matched, an owner with
// no verified name, a currency or product refusal) are an operator's.
func (s *InboundPayerService) settleArrived(ctx context.Context, claim InboundClaim, decl *models.InboundPayerDeclaration) error {
	// Amount filtered in SQL (a whole-naira claim matches grosses that round
	// to it); arrival time in Go, where timestamp semantics don't vary by
	// database.
	var credits []models.InboundCredit
	if err := s.db.WithContext(ctx).
		Where("status = ? AND hold_reason_code = ? AND gross_kobo BETWEEN ? AND ?",
			models.InboundCreditHeld, models.InboundHoldNoNameMatch,
			claim.AmountKobo-50, claim.AmountKobo+49).
		Order("transaction_date ASC").
		Limit(50).
		Find(&credits).Error; err != nil {
		return fmt.Errorf("payer declaration: load credits: %w", err)
	}

	opened := claim.OpenedAt.Add(-payerArrivalSkew)
	var fit, unsure []models.InboundCredit
	for _, c := range credits {
		if !inboundSameCurrency(c.Currency, claim.Currency) || RoundKoboToNaira(c.GrossKobo) != claim.AmountKobo ||
			c.TransactionDate.Before(opened) {
			continue
		}
		switch payerFits(decl, &c) {
		case payerFitYes:
			fit = append(fit, c)
		case payerFitBankUnsure:
			unsure = append(unsure, c)
		}
	}

	switch {
	case len(fit) == 1 && len(unsure) == 0:
		credit := fit[0]
		if claim.AmountKobo > PayerSelfResolveMaxKobo {
			return s.markReview(ctx, decl, &credit.ID, "over the self-resolve limit — an admin confirms")
		}
		// Who the money could have been for is decided as of its arrival, not
		// now: the real owner's order may have expired since. The matcher
		// recorded the candidates when it held the credit; only a credit whose
		// sole candidate was this claim is the declarer's to settle.
		if credit.HeldClaimIDs != heldClaimKey(claim) {
			return s.markReview(ctx, decl, &credit.ID, "other orders were waiting for this amount when the payment arrived — an admin confirms")
		}
		if _, _, err := s.matcher.bindDeclared(ctx, &credit, claim, decl, models.InboundCreditHeld); err != nil {
			return err
		}
		return s.db.WithContext(ctx).First(decl, "id = ?", decl.ID).Error
	case len(fit)+len(unsure) > 1:
		return s.markReview(ctx, decl, nil, fmt.Sprintf("%d payments fit this declaration — an admin picks the right one", len(fit)+len(unsure)))
	case len(unsure) == 1:
		return s.markReview(ctx, decl, &unsure[0].ID, "the sender bank on the statement could not be compared — an admin confirms")
	}
	return nil
}

func (s *InboundPayerService) markReview(ctx context.Context, decl *models.InboundPayerDeclaration, creditID *uuid.UUID, reason string) error {
	if err := markDeclarationReview(ctx, s.db, decl.ID, creditID, reason); err != nil {
		return err
	}
	return s.db.WithContext(ctx).First(decl, "id = ?", decl.ID).Error
}

func markDeclarationReview(ctx context.Context, db *gorm.DB, declID uuid.UUID, creditID *uuid.UUID, reason string) error {
	updates := map[string]any{"status": models.PayerDeclarationReview, "review_reason": truncate(reason, 255)}
	if creditID != nil {
		updates["credit_id"] = *creditID
	}
	if err := db.WithContext(ctx).Model(&models.InboundPayerDeclaration{}).
		Where("id = ? AND status IN ?", declID,
			[]string{models.PayerDeclarationWaiting, models.PayerDeclarationReview}).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("payer declaration: mark review: %w", err)
	}
	return nil
}

// bankName is the directory's name for code; an unknown code is refused rather
// than declared under a name nobody can compare.
func (s *InboundPayerService) bankName(ctx context.Context, code string) (string, error) {
	banks, err := s.banks.ListBanks(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPayerLookupFailed, err)
	}
	for _, b := range banks {
		if strings.EqualFold(strings.TrimSpace(b.Code), code) {
			return strings.TrimSpace(b.Name), nil
		}
	}
	return "", ErrPayerUnknownBank
}

// Declarations lists declarations for the admin queue, newest first. An empty
// status lists every one; an unknown one is refused rather than answered with
// an empty page that looks like an empty queue.
func (s *InboundPayerService) Declarations(ctx context.Context, status string, limit, offset int) ([]models.InboundPayerDeclaration, error) {
	if s == nil || s.db == nil {
		return nil, ErrPayerUnavailable
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	q := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit).Offset(offset)
	switch status = strings.TrimSpace(status); status {
	case "":
	case models.PayerDeclarationWaiting, models.PayerDeclarationMatched,
		models.PayerDeclarationReview, models.PayerDeclarationClosed:
		q = q.Where("status = ?", status)
	default:
		return nil, fmt.Errorf("%w: unknown declaration status %q", ErrInboundBadRequest, status)
	}
	var out []models.InboundPayerDeclaration
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("payer declarations: list: %w", err)
	}
	return out, nil
}

// ─── fit ─────────────────────────────────────────────────────────────────────

type payerFit int

const (
	payerFitNo payerFit = iota
	payerFitYes
	// payerFitBankUnsure — the names agree but the statement's bank could not
	// be compared with the declared one; never bound without an operator.
	payerFitBankUnsure
)

// payerFits applies the declaration's sender rules to one credit (amount and
// timing are the caller's): the statement's sender name must agree with BOTH
// the typed name and the bank's name for the account, and its bank must be the
// declared bank.
func payerFits(decl *models.InboundPayerDeclaration, credit *models.InboundCredit) payerFit {
	if strings.TrimSpace(credit.SenderName) == "" {
		return payerFitNo
	}
	if !MatchAccountName(decl.ResolvedName, credit.SenderName).IsMatch() ||
		!MatchAccountName(decl.TypedName, credit.SenderName).IsMatch() {
		return payerFitNo
	}
	switch payerSameBank(decl.BankName, credit.SenderBank) {
	case bankSame:
		return payerFitYes
	case bankDifferent:
		return payerFitNo
	default:
		return payerFitBankUnsure
	}
}

type bankVerdict int

const (
	bankUnsure bankVerdict = iota
	bankSame
	bankDifferent
)

// payerBankAliases expands the short forms statement remarks use for a bank
// into the words of its directory name.
var payerBankAliases = map[string]string{
	"gtb":       "guaranty trust",
	"gtbank":    "guaranty trust",
	"gtco":      "guaranty trust",
	"uba":       "united africa",
	"fcmb":      "first city monument",
	"fbn":       "first nigeria",
	"firstbank": "first nigeria",
	"stanbic":   "stanbic ibtc",
	"alat":      "wema",
}

// payerBankAmbiguous are words several different banks' names share. A bank
// reduced to one of these alone ("First Bank" → {first}) says nothing about
// which bank it is: {first} is wholly inside both First Bank of Nigeria and
// First City Monument Bank.
var payerBankAmbiguous = map[string]bool{
	"first": true, "union": true, "united": true, "standard": true,
	"trust": true, "city": true, "global": true, "nigeria": true, "africa": true,
	"digital": true, "services": true, "capital": true,
}

// payerBankTokens is the bank's identifying words, after noise and aliases.
func payerBankTokens(name string) map[string]bool {
	out := map[string]bool{}
	for t := range normalizeBankName(name) {
		if alias, ok := payerBankAliases[t]; ok {
			for a := range normalizeBankName(alias) {
				out[a] = true
			}
			continue
		}
		out[t] = true
	}
	return out
}

// payerSameBank compares the declared bank with the statement's sender bank.
// One name's identifying words wholly inside the other's is the same bank —
// unless the smaller is a single word several banks share, which is unsure;
// no shared word at all is a different bank; anything in between is unsure.
func payerSameBank(declared, statement string) bankVerdict {
	a, b := payerBankTokens(declared), payerBankTokens(statement)
	if len(a) == 0 || len(b) == 0 {
		return bankUnsure
	}
	small, big := a, b
	if len(b) < len(a) {
		small, big = b, a
	}
	if subset(small, big) {
		if len(small) == 1 && len(big) > 1 {
			for t := range small {
				if payerBankAmbiguous[t] {
					return bankUnsure
				}
			}
		}
		return bankSame
	}
	if jaccard(a, b) == 0 {
		return bankDifferent
	}
	return bankUnsure
}

// ─── matcher side ────────────────────────────────────────────────────────────

// heldClaimKey is a claim's entry in InboundCredit.HeldClaimIDs.
func heldClaimKey(c InboundClaim) string { return c.Type + ":" + c.ID.String() }

// heldClaimIDs is the HeldClaimIDs value for a no-name hold over candidates.
func heldClaimIDs(candidates []InboundClaim) string {
	keys := make([]string, 0, len(candidates))
	for _, c := range candidates {
		keys = append(keys, heldClaimKey(c))
	}
	return truncate(strings.Join(keys, ","), 1024)
}

// openClaimByID finds the open claim with id across every registered source,
// stamped with its type; nil when none is open.
func (m *InboundMatcher) openClaimByID(ctx context.Context, currency string, id uuid.UUID) (*InboundClaim, error) {
	claims, err := m.gatherClaims(ctx, currency)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPayerUnavailable, err)
	}
	for i := range claims {
		if claims[i].ID == id {
			return &claims[i], nil
		}
	}
	return nil, nil
}

// bindDeclared settles credit to claim on decl.
//
// The row is CLAIMED first — moved from `from` to `binding` with the claim
// named — and only then does the product settle run. A held credit is exactly
// what admins refund and assign; claiming it before any value moves means an
// admin acting at the same moment is refused (the row is no longer held),
// rather than both settling the same naira. An interrupted bind is finished by
// resumeDeclaredBinds, never handed back.
//
// Never the operator path (withManualBind): the product's own review gates
// apply, and a refusal holds the credit with the product's reason.
//
// decided reports that the credit left `from` (bound or held for review);
// matched that it now belongs to this claim.
func (m *InboundMatcher) bindDeclared(ctx context.Context, credit *models.InboundCredit, claim InboundClaim, decl *models.InboundPayerDeclaration, from string) (decided, matched bool, err error) {
	src, ok := m.sourceFor(claim.Type)
	if !ok {
		return false, false, fmt.Errorf("inbound payer: no source registered for claim type %q", claim.Type)
	}
	claimQ := m.db.WithContext(ctx).Model(&models.InboundCredit{}).Where("id = ? AND status = ?", credit.ID, from)
	if from == models.InboundCreditHeld {
		claimQ = claimQ.Where("hold_reason_code = ?", models.InboundHoldNoNameMatch)
	}
	res := claimQ.Updates(map[string]any{
		"status":             models.InboundCreditBinding,
		"matched_claim_type": claim.Type,
		"matched_claim_id":   claim.ID,
		"matched_user_id":    claim.UserID,
		"updated_at":         m.now().UTC(),
	})
	if res.Error != nil {
		return false, false, fmt.Errorf("inbound payer: claim credit %s: %w", credit.ID, res.Error)
	}
	if res.RowsAffected == 0 {
		log.Printf("[inbound-payer] ref=%s was decided concurrently — leaving it alone", credit.ProviderRef)
		return false, false, nil
	}
	return m.settleClaimedDeclared(ctx, src, credit, claim, decl, from)
}

// settleClaimedDeclared runs the product settle for a credit already claimed
// into `binding`, then records the outcome. Shared by the first attempt and
// the resume of an interrupted one.
func (m *InboundMatcher) settleClaimedDeclared(ctx context.Context, src ClaimSource, credit *models.InboundCredit, claim InboundClaim, decl *models.InboundPayerDeclaration, from string) (decided, matched bool, err error) {
	if aerr := src.Accept(ctx, claim.ID, credit); aerr != nil {
		var review *ClaimNeedsReviewError
		if errors.As(aerr, &review) {
			// The product refused before moving value: hold it with the
			// product's own reason, for an operator.
			if uerr := m.db.WithContext(ctx).Model(&models.InboundCredit{}).
				Where("id = ? AND status = ?", credit.ID, models.InboundCreditBinding).
				Updates(map[string]any{
					"status": models.InboundCreditHeld, "hold_reason_code": review.Code,
					"hold_reason":        truncate(review.Reason, inboundHoldReasonMax),
					"matched_claim_type": "", "matched_claim_id": nil, "matched_user_id": nil,
				}).Error; uerr != nil {
				return false, false, fmt.Errorf("inbound payer: hold %s after product review: %w", credit.ProviderRef, uerr)
			}
			if decl != nil {
				if rerr := markDeclarationReview(ctx, m.db, decl.ID, &credit.ID,
					"the product held this payment for review ("+review.Code+") — an admin confirms"); rerr != nil {
					log.Printf("[inbound-payer] WARNING declaration=%s not moved to review: %v", decl.ID, rerr)
				}
			}
			log.Printf("[inbound-payer] HELD ref=%s reason=%s (declared bind)", credit.ProviderRef, review.Code)
			return true, false, nil
		}
		// Anything else is retryable. Only an unmatched credit goes back where
		// it came from (the matcher retries it next tick). A row that came out
		// of `held` stays claimed and the resume finishes it: handing it back
		// would let an admin refund money the settle may have delivered.
		if from == models.InboundCreditUnmatched {
			_ = m.db.WithContext(ctx).Model(&models.InboundCredit{}).
				Where("id = ? AND status = ?", credit.ID, models.InboundCreditBinding).
				Updates(map[string]any{
					"status": models.InboundCreditUnmatched, "matched_claim_type": "",
					"matched_claim_id": nil, "matched_user_id": nil,
				}).Error
		}
		return false, false, fmt.Errorf("inbound payer: settle %s claim %s for ref %s: %w",
			claim.Type, claim.ID, credit.ProviderRef, aerr)
	}

	now := m.now().UTC()
	res := m.db.WithContext(ctx).Model(&models.InboundCredit{}).
		Where("id = ? AND status = ?", credit.ID, models.InboundCreditBinding).
		Updates(map[string]any{
			"status": models.InboundCreditMatched, "matched_at": now,
			"match_strength": string(NameMatchDeclared),
			"hold_reason":    "", "hold_reason_code": "",
		})
	if res.Error != nil {
		// Settled, not recorded: the row stays `binding` and the resume
		// re-runs the (idempotent) settle and records it.
		return true, false, fmt.Errorf("inbound payer: settled ref %s but could not record the bind: %w", credit.ProviderRef, res.Error)
	}
	if res.RowsAffected == 0 {
		// Another finisher recorded it first; its record stands, and one
		// audit row is the right number.
		return true, true, nil
	}
	if decl != nil {
		m.recordDeclaredBind(ctx, credit, claim, decl)
	}
	log.Printf("[inbound-payer] bound ref=%s %s claim=%s on a payer declaration", credit.ProviderRef, claim.Type, claim.ID)
	return true, true, nil
}

// recordDeclaredBind marks the declaration matched and writes the audit row.
// Neither failing un-settles anything, so both are logged rather than failed.
func (m *InboundMatcher) recordDeclaredBind(ctx context.Context, credit *models.InboundCredit, claim InboundClaim, decl *models.InboundPayerDeclaration) {
	now := m.now().UTC()
	if err := m.db.WithContext(ctx).Model(&models.InboundPayerDeclaration{}).
		Where("id = ?", decl.ID).
		Updates(map[string]any{
			"status": models.PayerDeclarationMatched, "credit_id": credit.ID,
			"matched_at": now, "review_reason": "",
		}).Error; err != nil {
		log.Printf("[inbound-payer] WARNING credit=%s bound but declaration=%s not marked matched: %v",
			credit.ID, decl.ID, err)
	}
	_, last4 := maskAccountNumber(decl.AccountNumber)
	meta, _ := json.Marshal(map[string]any{
		"claimType": claim.Type, "claimId": claim.ID, "declarationId": decl.ID,
		"bankCode": decl.BankCode, "bankName": decl.BankName, "accountLast4": last4,
		"grossKobo": credit.GrossKobo, "providerRef": credit.ProviderRef,
	})
	actor := decl.UserID
	if err := m.db.WithContext(ctx).Create(&models.InboundCreditAuditLog{
		CreditID: credit.ID, ActorID: &actor, Action: models.InboundAuditPayerDeclared,
		Note: "bound on the buyer's verified payer declaration", Metadata: meta,
	}).Error; err != nil {
		log.Printf("[inbound-payer] WARNING credit=%s bound but audit row not written: %v", credit.ID, err)
	}
}

// resumeDeclaredBinds finishes declared binds that were interrupted after the
// credit was claimed (a crash, a failed settle or record). The settle is
// idempotent on the provider ref, so re-running it is safe; handing the row
// back to `held` would not be — an admin could then refund money the product
// already delivered.
func (m *InboundMatcher) resumeDeclaredBinds(ctx context.Context) error {
	cutoff := m.now().UTC().Add(-payerBindResumeAfter)
	var stuck []models.InboundCredit
	if err := m.db.WithContext(ctx).
		Where("status = ? AND updated_at < ?", models.InboundCreditBinding, cutoff).
		Limit(20).Find(&stuck).Error; err != nil {
		return fmt.Errorf("inbound payer: load interrupted binds: %w", err)
	}
	var errs []error
	for i := range stuck {
		c := &stuck[i]
		// Take the row before touching it: bumping updated_at past the cutoff
		// is a conditional write only one resumer (another tick, another
		// process) can win.
		lease := m.db.WithContext(ctx).Model(&models.InboundCredit{}).
			Where("id = ? AND status = ? AND updated_at < ?", c.ID, models.InboundCreditBinding, cutoff).
			Update("updated_at", m.now().UTC())
		if lease.Error != nil {
			errs = append(errs, fmt.Errorf("inbound payer: lease ref %s: %w", c.ProviderRef, lease.Error))
			continue
		}
		if lease.RowsAffected == 0 {
			continue // someone else is finishing it
		}
		if c.MatchedClaimID == nil {
			log.Printf("[inbound-payer] WARNING ref=%s is binding with no claim named — needs an engineer", c.ProviderRef)
			continue
		}
		src, ok := m.sourceFor(c.MatchedClaimType)
		if !ok {
			errs = append(errs, fmt.Errorf("inbound payer: resume ref %s: no source for %q", c.ProviderRef, c.MatchedClaimType))
			continue
		}
		claim := InboundClaim{Type: c.MatchedClaimType, ID: *c.MatchedClaimID}
		if c.MatchedUserID != nil {
			claim.UserID = *c.MatchedUserID
		}
		var decl *models.InboundPayerDeclaration
		var d models.InboundPayerDeclaration
		if err := m.db.WithContext(ctx).Where("claim_type = ? AND claim_id = ?", claim.Type, claim.ID).
			First(&d).Error; err == nil {
			decl = &d
		}
		log.Printf("[inbound-payer] resuming interrupted declared bind ref=%s", c.ProviderRef)
		if _, _, err := m.settleClaimedDeclared(ctx, src, c, claim, decl, models.InboundCreditBinding); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// bindOnDeclaration is the matcher's fallback when no candidate's own legal
// name matches the sender: a waiting declaration that fits exactly one
// candidate binds it (within the cap, and only when no other candidate asks
// the same amount). handled=false leaves the caller to hold the credit as it
// would have anyway; handled=true returns the matcher's decided verdict.
func (m *InboundMatcher) bindOnDeclaration(ctx context.Context, credit *models.InboundCredit, candidates []InboundClaim) (handled, decided bool, err error) {
	if len(candidates) == 0 {
		return false, false, nil
	}
	byKey := make(map[string]InboundClaim, len(candidates))
	ids := make([]uuid.UUID, 0, len(candidates))
	for _, c := range candidates {
		byKey[c.Type+"/"+c.ID.String()] = c
		ids = append(ids, c.ID)
	}
	var decls []models.InboundPayerDeclaration
	if err := m.db.WithContext(ctx).
		Where("claim_id IN ? AND status = ?", ids, models.PayerDeclarationWaiting).
		Find(&decls).Error; err != nil {
		return false, false, fmt.Errorf("inbound match: load payer declarations: %w", err)
	}

	type fitDecl struct {
		decl  models.InboundPayerDeclaration
		claim InboundClaim
		fit   payerFit
	}
	var fits []fitDecl
	for _, d := range decls {
		claim, ok := byKey[d.ClaimType+"/"+d.ClaimID.String()]
		if !ok || credit.TransactionDate.Before(claim.OpenedAt.Add(-payerArrivalSkew)) {
			continue
		}
		if f := payerFits(&d, credit); f != payerFitNo {
			fits = append(fits, fitDecl{decl: d, claim: claim, fit: f})
		}
	}
	if len(fits) == 0 {
		return false, false, nil
	}

	review := func(reason string) {
		for _, f := range fits {
			if rerr := markDeclarationReview(ctx, m.db, f.decl.ID, &credit.ID, reason); rerr != nil {
				log.Printf("[inbound-payer] WARNING declaration=%s not moved to review: %v", f.decl.ID, rerr)
			}
		}
	}
	switch {
	case len(fits) > 1:
		review(fmt.Sprintf("%d declarations fit one payment — an admin picks the right one", len(fits)))
		return false, false, nil
	case fits[0].fit == payerFitBankUnsure:
		review("the sender bank on the statement could not be compared — an admin confirms")
		return false, false, nil
	case fits[0].claim.AmountKobo > PayerSelfResolveMaxKobo:
		review("over the self-resolve limit — an admin confirms")
		return false, false, nil
	case len(candidates) > 1:
		// Another buyer waits on this same amount; the money may be theirs.
		review("another order is waiting for the same amount — an admin confirms")
		return false, false, nil
	}
	f := fits[0]
	decided, _, err = m.bindDeclared(ctx, credit, f.claim, &f.decl, models.InboundCreditUnmatched)
	return true, decided, err
}

// closeSettledDeclarations closes waiting or review declarations that no
// longer describe money still owed: the claim's payment matched on the buyer's
// own name (claimID set), or a waiting declaration outlived any claim it could
// settle (claimID nil). Keeps the admin queue to declarations that still mean
// something.
func (m *InboundMatcher) closeSettledDeclarations(ctx context.Context, claimID *uuid.UUID) error {
	q := m.db.WithContext(ctx).Model(&models.InboundPayerDeclaration{})
	reason := "closed: the order can no longer be paid"
	if claimID != nil {
		q = q.Where("claim_id = ? AND status IN ?", *claimID,
			[]string{models.PayerDeclarationWaiting, models.PayerDeclarationReview})
		reason = "closed: the order's payment matched the buyer's own name"
	} else {
		q = q.Where("status = ? AND created_at < ?", models.PayerDeclarationWaiting, m.now().UTC().Add(-payerDeclarationTTL))
	}
	if err := q.Updates(map[string]any{"status": models.PayerDeclarationClosed, "review_reason": reason}).Error; err != nil {
		return fmt.Errorf("payer declarations: close settled: %w", err)
	}
	return nil
}

// closeDeclarationsAfterAdmin brings declarations into line with an admin's
// decision on a credit, so the review queue empties as admins work it: the
// declaration for the order the credit was assigned to is matched, and any
// other declaration that pointed at the credit is closed with the reason.
// assignedClaim is nil for a refund or ignore.
func closeDeclarationsAfterAdmin(ctx context.Context, db *gorm.DB, creditID uuid.UUID, assignedClaim *uuid.UUID, outcome string) error {
	now := time.Now().UTC()
	if assignedClaim != nil {
		if err := db.WithContext(ctx).Model(&models.InboundPayerDeclaration{}).
			Where("claim_id = ? AND status <> ?", *assignedClaim, models.PayerDeclarationMatched).
			Updates(map[string]any{
				"status": models.PayerDeclarationMatched, "credit_id": creditID,
				"matched_at": now, "review_reason": "",
			}).Error; err != nil {
			return fmt.Errorf("payer declarations: match after assign: %w", err)
		}
	}
	q := db.WithContext(ctx).Model(&models.InboundPayerDeclaration{}).
		Where("credit_id = ? AND status IN ?", creditID,
			[]string{models.PayerDeclarationReview, models.PayerDeclarationWaiting})
	if assignedClaim != nil {
		q = q.Where("claim_id <> ?", *assignedClaim)
	}
	if err := q.Updates(map[string]any{
		"status":        models.PayerDeclarationClosed,
		"review_reason": truncate("an admin "+outcome, 255),
	}).Error; err != nil {
		return fmt.Errorf("payer declarations: close after admin: %w", err)
	}
	return nil
}
