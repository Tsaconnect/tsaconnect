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
// the declared one, the amount is the claim's, and the money arrived after the
// claim opened.

// DefaultPayerSelfResolveMaxKobo is the largest claim a declaration may settle
// without an operator: ₦500,000 (owner, 2026-10-16). Larger ones go to review.
const DefaultPayerSelfResolveMaxKobo int64 = 500_000 * 100

// NameMatchDeclared is the match strength recorded on a credit bound on a
// payer declaration rather than on the claim owner's own legal name.
const NameMatchDeclared NameMatchStrength = "declared"

// payerArrivalSkew tolerates the statement's clock running slightly behind
// ours: a credit stamped a moment before the claim opened is still this claim's.
const payerArrivalSkew = 2 * time.Minute

var (
	ErrPayerBadInput        = errors.New("payer declaration: invalid details")
	ErrPayerClaimNotFound   = errors.New("payer declaration: no open payment for this order")
	ErrPayerAlreadyMatched  = errors.New("payer declaration: this order's payment is already matched")
	ErrPayerAccountNotFound = errors.New("payer declaration: the bank has no such account")
	ErrPayerUnknownBank     = errors.New("payer declaration: unknown bank")
	ErrPayerUnavailable     = errors.New("payer declaration: not available right now")
	ErrPayerLookupFailed    = errors.New("payer declaration: the bank could not be reached")
)

// PayerNameMismatchError is returned when the typed name is not the bank's
// name for the account. The bank's name is shown back: a name enquiry is what
// every banking app does before a transfer.
type PayerNameMismatchError struct{ ResolvedName string }

func (e *PayerNameMismatchError) Error() string {
	return fmt.Sprintf("payer declaration: the bank has this account as %q", e.ResolvedName)
}

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
	capKobo int64
}

// NewInboundPayerService wires the service. A nil matcher or bank directory
// leaves it answering ErrPayerUnavailable rather than half-working.
func NewInboundPayerService(db *gorm.DB, matcher *InboundMatcher, banks BankDirectory) *InboundPayerService {
	return &InboundPayerService{db: db, matcher: matcher, banks: banks, capKobo: DefaultPayerSelfResolveMaxKobo}
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
		return nil, &PayerNameMismatchError{ResolvedName: resolved}
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
		return nil, err
	}
	return &DeclarePayerResult{Declaration: decl, Outcome: decl.Status}, nil
}

// settleArrived looks for a credit that already arrived for this declaration —
// typically one the matcher held for want of a name match — and binds it when
// exactly one fits and the claim is within the self-resolve cap.
func (s *InboundPayerService) settleArrived(ctx context.Context, claim InboundClaim, decl *models.InboundPayerDeclaration) error {
	// Undecided credits are a short list (held ones wait for a human), so the
	// amount and arrival rules run here rather than as SQL comparisons whose
	// timestamp semantics differ between databases.
	var credits []models.InboundCredit
	if err := s.db.WithContext(ctx).
		Where("status IN ?", []string{models.InboundCreditHeld, models.InboundCreditUnmatched}).
		Order("transaction_date ASC").
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
		if claim.AmountKobo > s.capKobo {
			return s.markReview(ctx, decl, &credit.ID, "over the self-resolve limit — an admin confirms")
		}
		_, matched, err := s.matcher.bindDeclared(ctx, &credit, claim, decl)
		if err != nil {
			return err
		}
		if !matched {
			return s.markReview(ctx, decl, &credit.ID, "the payment could not be settled automatically — an admin confirms")
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
	updates := map[string]any{"status": models.PayerDeclarationReview, "review_reason": truncate(reason, 255)}
	if creditID != nil {
		updates["credit_id"] = *creditID
	}
	if err := s.db.WithContext(ctx).Model(&models.InboundPayerDeclaration{}).
		Where("id = ? AND status <> ?", decl.ID, models.PayerDeclarationMatched).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("payer declaration: mark review: %w", err)
	}
	return s.db.WithContext(ctx).First(decl, "id = ?", decl.ID).Error
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
// status lists every one.
func (s *InboundPayerService) Declarations(ctx context.Context, status string, limit int) ([]models.InboundPayerDeclaration, error) {
	if s == nil || s.db == nil {
		return nil, ErrPayerUnavailable
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit)
	if status = strings.TrimSpace(status); status != "" {
		q = q.Where("status = ?", status)
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
	"fidelity":  "fidelity",
	"stanbic":   "stanbic ibtc",
	"wema":      "wema",
	"alat":      "wema",
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
// One name's identifying words wholly inside the other's is the same bank;
// no shared word at all is a different bank; anything in between is unsure.
func payerSameBank(declared, statement string) bankVerdict {
	a, b := payerBankTokens(declared), payerBankTokens(statement)
	if len(a) == 0 || len(b) == 0 {
		return bankUnsure
	}
	if subset(a, b) || subset(b, a) {
		return bankSame
	}
	if jaccard(a, b) == 0 {
		return bankDifferent
	}
	return bankUnsure
}

// ─── matcher side ────────────────────────────────────────────────────────────

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

// bindDeclared settles credit to claim on decl through the same Accept-then-
// record path every automatic bind takes — never the operator path, so the
// product's own review gates still apply — then marks the declaration matched
// and writes the audit row.
//
// decided is the matcher's own verdict (bound, or held by the product's review
// gate); matched is whether the credit now belongs to this claim. They differ
// when the product refused: the credit is held for an operator and the
// declaration must not read as settled.
func (m *InboundMatcher) bindDeclared(ctx context.Context, credit *models.InboundCredit, claim InboundClaim, decl *models.InboundPayerDeclaration) (decided, matched bool, err error) {
	decided, err = m.bindCredit(ctx, credit, inboundCandidate{claim: claim, strength: NameMatchDeclared}, nil)
	if err != nil || !decided {
		return decided, false, err
	}
	var now models.InboundCredit
	if err := m.db.WithContext(ctx).Select("status", "matched_claim_id").First(&now, "id = ?", credit.ID).Error; err != nil {
		return decided, false, fmt.Errorf("inbound match: re-read credit %s after declared bind: %w", credit.ID, err)
	}
	if now.Status != models.InboundCreditMatched || now.MatchedClaimID == nil || *now.MatchedClaimID != claim.ID {
		return decided, false, nil
	}
	return decided, true, m.recordDeclaredBind(ctx, credit, claim, decl)
}

// recordDeclaredBind marks the declaration matched and writes the audit row.
// Neither failing un-settles anything, so both are logged rather than failed.
func (m *InboundMatcher) recordDeclaredBind(ctx context.Context, credit *models.InboundCredit, claim InboundClaim, decl *models.InboundPayerDeclaration) error {
	now := m.now().UTC()
	if err := m.db.WithContext(ctx).Model(&models.InboundPayerDeclaration{}).
		Where("id = ?", decl.ID).
		Updates(map[string]any{
			"status": models.PayerDeclarationMatched, "credit_id": credit.ID,
			"matched_at": now, "review_reason": "",
		}).Error; err != nil {
		// The money is settled and recorded on the credit; only the
		// declaration's own status lags. Logged, not failed: retrying the bind
		// would be a no-op anyway.
		log.Printf("[inbound-payer] WARNING credit=%s bound but declaration=%s not marked matched: %v",
			credit.ID, decl.ID, err)
	}
	meta, _ := json.Marshal(map[string]any{
		"claimType": claim.Type, "claimId": claim.ID, "declarationId": decl.ID,
		"bankCode": decl.BankCode, "bankName": decl.BankName,
		"accountLast4": last4(decl.AccountNumber),
		"grossKobo":    credit.GrossKobo, "providerRef": credit.ProviderRef,
	})
	actor := decl.UserID
	if err := m.db.WithContext(ctx).Create(&models.InboundCreditAuditLog{
		CreditID: credit.ID, ActorID: &actor, Action: models.InboundAuditPayerDeclared,
		Note: "bound on the buyer's verified payer declaration", Metadata: meta,
	}).Error; err != nil {
		log.Printf("[inbound-payer] WARNING credit=%s bound but audit row not written: %v", credit.ID, err)
	}
	log.Printf("[inbound-payer] bound ref=%s %s claim=%s on declaration=%s",
		credit.ProviderRef, claim.Type, claim.ID, decl.ID)
	return nil
}

// bindOnDeclaration is the matcher's fallback when no candidate's own legal
// name matches the sender: a waiting declaration that fits exactly one
// candidate binds it (within the cap). handled=false leaves the caller to hold
// the credit as it would have anyway; handled=true returns the matcher's
// decided verdict for this credit.
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
			if uerr := m.db.WithContext(ctx).Model(&models.InboundPayerDeclaration{}).
				Where("id = ? AND status = ?", f.decl.ID, models.PayerDeclarationWaiting).
				Updates(map[string]any{
					"status": models.PayerDeclarationReview, "credit_id": credit.ID,
					"review_reason": truncate(reason, 255),
				}).Error; uerr != nil {
				log.Printf("[inbound-payer] WARNING declaration=%s not moved to review: %v", f.decl.ID, uerr)
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
	case fits[0].claim.AmountKobo > DefaultPayerSelfResolveMaxKobo:
		review("over the self-resolve limit — an admin confirms")
		return false, false, nil
	}
	f := fits[0]
	decided, matched, err := m.bindDeclared(ctx, credit, f.claim, &f.decl)
	if err == nil && decided && !matched {
		review("the product held this payment for review — an admin confirms")
	}
	return true, decided, err
}

func last4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}
