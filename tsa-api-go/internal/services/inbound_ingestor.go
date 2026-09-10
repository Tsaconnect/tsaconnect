package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Statement row types Flutterwave emits. Only `funding` is customer money
// arriving in the central account; the rest are our own movements.
const (
	fwRowTypeFunding = "funding"
	fwRowTypeFee     = "fee"
)

const (
	// inboundDefaultCurrency is the only currency the central account collects
	// today. Kept a parameter anyway so a second one is config, not surgery.
	inboundDefaultCurrency = "NGN"

	// inboundDefaultLookback is how far back each poll reads. It only has to
	// exceed the longest gap we expect between polls — a restart, a deploy —
	// because re-reading rows we already hold is free (unique on ProviderRef).
	// Wider is safer than narrower: a row that falls out of the window before
	// it is ever read is a credit lost for good.
	inboundDefaultLookback = 24 * time.Hour

	// inboundRemarkPrefix is the measured prefix of a funding remark:
	//   "Received Money from GTBANK|ADEMU EMMANUEL O."
	// Undocumented and unstable, hence the belt-and-braces parse below.
	inboundRemarkPrefix = "received money from "

	// Column widths from models.InboundCredit. Oversized free text is
	// truncated rather than rejected — Postgres would error the whole INSERT
	// on an overflow, and a provider that starts sending longer remarks must
	// not be able to stop us recording that money arrived.
	inboundSenderRawMax  = 255
	inboundSenderBankMax = 64
	inboundSenderNameMax = 128
)

// InboundStatementSource is the narrow slice of the Flutterwave client the
// ingestor needs. *FlutterwaveClient satisfies it; tests inject a fake.
type InboundStatementSource interface {
	FetchWalletStatement(ctx context.Context, currency string, from, to time.Time) ([]FWStatementEntry, error)
}

// IngestResult is one poll's outcome, surfaced for logging and tests.
type IngestResult struct {
	Rows       int // statement rows returned by the provider
	Funding    int // rows that were customer money arriving
	Inserted   int // new inbound_credits rows written
	Duplicates int // rows already held (the steady state — polls overlap)
	Skipped    int // non-funding rows
	Unparsed   int // funding rows whose remark did not yield bank + name
	Errors     int // rows that could not be written
}

// InboundIngestor turns Flutterwave wallet-statement rows into durable
// inbound_credits rows. That is its entire job.
//
// It does NO matching, on purpose (design §3). The record that money arrived
// must survive every bug downstream of it: matching, claim sources, settle
// paths. Once a row is here, attribution is re-runnable; if a row never gets
// here, the money is invisible and there is nothing to re-run. Everything
// below is written to that standard — a row is dropped only when it is
// provably not customer money, never because we failed to understand it.
//
// It is safe to run in either INBOUND_MODE. Under `va` it simply records
// credits nothing claims yet, which is exactly how the poll is meant to be
// observed against real traffic before the flag flips.
type InboundIngestor struct {
	db       *gorm.DB
	src      InboundStatementSource
	currency string
	lookback time.Duration
	// now is injectable so tests can pin the clock without sleeping.
	now func() time.Time
}

// NewInboundIngestor builds the ingestor. A blank currency or non-positive
// lookback falls back to the documented defaults.
func NewInboundIngestor(db *gorm.DB, src InboundStatementSource, currency string, lookback time.Duration) *InboundIngestor {
	if strings.TrimSpace(currency) == "" {
		currency = inboundDefaultCurrency
	}
	if lookback <= 0 {
		lookback = inboundDefaultLookback
	}
	return &InboundIngestor{
		db:       db,
		src:      src,
		currency: strings.ToUpper(strings.TrimSpace(currency)),
		lookback: lookback,
		now:      time.Now,
	}
}

// Currency is the currency this ingestor polls (for log lines).
func (i *InboundIngestor) Currency() string { return i.currency }

// RunOnce polls the statement for the lookback window and ingests it.
func (i *InboundIngestor) RunOnce(ctx context.Context) (IngestResult, error) {
	if i == nil || i.db == nil || i.src == nil {
		return IngestResult{}, fmt.Errorf("inbound ingestor: not configured")
	}
	to := i.now().UTC()
	from := to.Add(-i.lookback)

	rows, err := i.src.FetchWalletStatement(ctx, i.currency, from, to)
	if err != nil {
		return IngestResult{}, fmt.Errorf("inbound ingest fetch: %w", err)
	}
	return i.IngestRows(ctx, rows)
}

// IngestRows writes every funding row in rows that we do not already hold.
//
// Exported separately from RunOnce so the parse-and-persist half can be
// driven directly — by tests, and later by an operator replaying a statement
// page by hand after an outage.
func (i *InboundIngestor) IngestRows(ctx context.Context, rows []FWStatementEntry) (IngestResult, error) {
	res := IngestResult{Rows: len(rows)}
	fees := pairFundingFees(rows)
	seenAt := i.now().UTC()

	var firstErr error
	for idx, row := range rows {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if !IsCustomerFunding(row) {
			res.Skipped++
			continue
		}
		res.Funding++

		credit := i.buildCredit(row, fees[idx], seenAt)
		if credit.SenderBank == "" || credit.SenderName == "" {
			// Not an error and never a reason to skip: the remark format is
			// the provider's, not a contract. The raw text is stored either
			// way and a human (or a later parser) can still read it.
			res.Unparsed++
		}

		inserted, err := i.insert(ctx, credit)
		switch {
		case err != nil:
			res.Errors++
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[inbound-ingest] WARNING credit not recorded ref=%s amountKobo=%d: %v",
				credit.ProviderRef, credit.AmountKobo, err)
		case inserted:
			res.Inserted++
		default:
			res.Duplicates++
		}
	}
	return res, firstErr
}

// insert writes one credit, tolerating the row already being there.
//
// Dedup is the unique index on provider_ref plus ON CONFLICT DO NOTHING, not
// a SELECT-then-INSERT: two pollers (or a poll overlapping a manual replay)
// would both see "absent" and both insert, and a duplicated credit is a
// double-credit waiting to happen once the matcher lands.
func (i *InboundIngestor) insert(ctx context.Context, credit *models.InboundCredit) (bool, error) {
	res := i.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "provider_ref"}},
			DoNothing: true,
		}).
		Create(credit)
	if res.Error != nil {
		return false, fmt.Errorf("inbound credit insert: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (i *InboundIngestor) buildCredit(row FWStatementEntry, feeKobo int64, seenAt time.Time) *models.InboundCredit {
	amountKobo := KoboFromMajor(row.Amount.Float64())
	bank, name := parseSenderRemark(row.Remarks)

	txAt := row.TransactionDate.Time
	if txAt.IsZero() {
		// transaction_date is NOT NULL and is what the reconciler windows on.
		// Our own clock is a worse timestamp than the provider's but an
		// infinitely better one than none.
		txAt = seenAt
	}
	currency := strings.ToUpper(strings.TrimSpace(row.Currency))
	if currency == "" {
		currency = i.currency
	}

	return &models.InboundCredit{
		Provider:        models.InboundCreditProviderFlutterwave,
		ProviderRef:     providerRefFor(row),
		Currency:        currency,
		AmountKobo:      amountKobo,
		FeeKobo:         feeKobo,
		GrossKobo:       amountKobo + feeKobo,
		SenderRaw:       truncate(strings.TrimSpace(row.Remarks), inboundSenderRawMax),
		SenderBank:      truncate(bank, inboundSenderBankMax),
		SenderName:      truncate(name, inboundSenderNameMax),
		TransactionDate: txAt.UTC(),
		SeenAt:          seenAt,
		Status:          models.InboundCreditUnmatched,
	}
}

// IsCustomerFunding reports whether a statement row is money a customer sent
// us. Everything else on the statement — our payouts, our fees, settlements,
// reversals, bills, card activity — is our own movement and must never become
// a claimable credit.
//
// Exported because the reconciler must classify rows EXACTLY as the ingestor
// does. Two copies that drift would make the two sides count different sets and
// alert on a gap that is pure definition mismatch, every tick, forever.
func IsCustomerFunding(row FWStatementEntry) bool {
	if !strings.EqualFold(strings.TrimSpace(row.Type), fwRowTypeFunding) {
		return false
	}
	// Direction is not on every measured row, so absence is not disqualifying;
	// an explicit debit is.
	if d := strings.TrimSpace(row.Direction); d != "" && strings.EqualFold(d, "debit") {
		return false
	}
	return true
}

// providerRefFor returns the dedupe key for a statement row: the NIBSS NIP
// session id, which is unique per credit.
//
// When the provider omits it we synthesize one from the row's own content
// instead of refusing the row. Refusing would mean discarding a credit that
// really arrived — the one thing this pipeline may never do — while inserting
// without a key would let the next poll record the same money twice.
// balance.after is part of the digest precisely because it is the field that
// still differs between two otherwise identical credits: each one lands on a
// different running balance.
func providerRefFor(row FWStatementEntry) string {
	if ref := strings.TrimSpace(row.Reference); ref != "" {
		return truncate(ref, 64)
	}
	seed := fmt.Sprintf("%s|%.2f|%.2f|%s|%s",
		row.TransactionDate.UTC().Format(time.RFC3339Nano),
		row.Amount.Float64(),
		row.Balance.After.Float64(),
		strings.TrimSpace(row.Remarks),
		strings.TrimSpace(row.Type),
	)
	sum := sha256.Sum256([]byte(seed))
	return "synthetic:" + hex.EncodeToString(sum[:16])
}

// parseSenderRemark pulls the bank and account name out of a funding remark:
//
//	"Received Money from GTBANK|ADEMU EMMANUEL O."  →  ("GTBANK", "ADEMU EMMANUEL O.")
//
// The format is undocumented, was measured once, and Flutterwave can change it
// without telling anyone. So this returns empty strings rather than guessing
// whenever the shape is not the one we measured, and the caller stores the raw
// text regardless. A parse failure costs an automatic match, which a human can
// fix from the admin queue; a dropped row costs the money itself.
func parseSenderRemark(remark string) (bank, name string) {
	s := strings.TrimSpace(remark)
	if s == "" {
		return "", ""
	}
	if len(s) < len(inboundRemarkPrefix) || !strings.EqualFold(s[:len(inboundRemarkPrefix)], inboundRemarkPrefix) {
		return "", ""
	}
	body := strings.TrimSpace(s[len(inboundRemarkPrefix):])
	// Split on the FIRST pipe only: a bank name cannot contain one, an account
	// name conceivably could.
	pipe := strings.IndexByte(body, '|')
	if pipe < 0 {
		return "", ""
	}
	bank = strings.TrimSpace(body[:pipe])
	name = strings.TrimSpace(body[pipe+1:])
	if bank == "" || name == "" {
		return "", ""
	}
	return bank, name
}

// ─── Fee pairing ───────────────────────────────────────────────────────────

const (
	// inboundFeePairWindow bounds how far from a credit a fee row may sit and
	// still be considered its charge.
	inboundFeePairWindow = 5 * time.Minute
	// inboundFeeExpectedBps is the measured wallet-funding charge (1.40%). It
	// ranks candidates; it never manufactures a fee that no row evidences.
	inboundFeeExpectedBps = 140
	// inboundFeeMaxBps is the outer sanity band. A "fee" larger than this
	// fraction of the credit is some other charge that happens to be nearby.
	inboundFeeMaxBps = 500
)

// pairFundingFees decides, for every funding row in rows, how much fee was
// taken out of that credit BEFORE it landed — i.e. what must be added back to
// reconstruct what the sender actually transferred. The result is keyed by
// index into rows; a missing key means zero.
//
// THE UNCERTAINTY THIS FUNCTION EXISTS FOR (design §11, the blocking probe):
// we have NOT verified whether Flutterwave nets its 1.40% funding charge off
// the credit or bills it as an independent wallet debit. If it nets, a ₦2,000
// transfer shows up as a ₦1,972 credit and naive exact matching fails for
// every user; if it bills separately, the credit row is already gross and
// adding a fee back would inflate every amount by 1.4% and fail just as hard.
// Both shapes put a `fee` row near the credit, so the row's mere existence
// proves nothing. The discriminator used here is the fee row's OWN balance
// movement:
//
//   - before != after — the fee moved the wallet on its own, so it was billed
//     separately and the credit row already carries the gross amount: 0.
//   - before == after (or no balance at all) — the fee is a memo for value
//     that never reached the wallet, so it came out of the credit: add it back.
//
// When the probe lands, this is the only function that should need to change.
//
// Ambiguity always yields zero rather than a guess. Two same-sized credits in
// the same five minutes produce two indistinguishable fee rows, and pairing
// them arbitrarily would move a fee onto the wrong credit — a wrong GrossKobo
// is a wrong match, and a wrong match is money credited to the wrong user.
func pairFundingFees(rows []FWStatementEntry) map[int]int64 {
	paired := make(map[int]int64)
	feeUsed := make(map[int]bool)

	// Pass 1: a fee that carries the credit's own reference is unambiguous
	// provenance and outranks any heuristic.
	for ci, credit := range rows {
		if !IsCustomerFunding(credit) || strings.TrimSpace(credit.Reference) == "" {
			continue
		}
		for fi, fee := range rows {
			if fi == ci || feeUsed[fi] || !isFeeRow(fee) {
				continue
			}
			if strings.TrimSpace(fee.Reference) != strings.TrimSpace(credit.Reference) {
				continue
			}
			feeUsed[fi] = true
			paired[ci] = nettedFeeKobo(fee)
			break
		}
	}

	// Pass 2: proximity + plausible size, and only when a single credit is the
	// clear best home for the fee.
	for fi, fee := range rows {
		if feeUsed[fi] || !isFeeRow(fee) {
			continue
		}
		best, ok := bestCreditForFee(fi, rows, paired)
		if !ok {
			continue
		}
		feeUsed[fi] = true
		paired[best] = nettedFeeKobo(fee)
	}
	return paired
}

// bestCreditForFee returns the index of the single funding row the fee at
// feeIdx most plausibly belongs to. ok is false when nothing qualifies or when
// two candidates are equally good.
func bestCreditForFee(feeIdx int, rows []FWStatementEntry, alreadyPaired map[int]int64) (int, bool) {
	fee := rows[feeIdx]
	feeKobo := KoboFromMajor(fee.Amount.Float64())
	if feeKobo <= 0 || fee.TransactionDate.IsZero() {
		return 0, false
	}

	bestIdx := -1
	var bestScore float64
	tied := false

	for ci, credit := range rows {
		if ci == feeIdx || !IsCustomerFunding(credit) || credit.TransactionDate.IsZero() {
			continue
		}
		if _, done := alreadyPaired[ci]; done {
			continue
		}
		creditKobo := KoboFromMajor(credit.Amount.Float64())
		if creditKobo <= 0 || feeKobo*10000 > creditKobo*inboundFeeMaxBps {
			continue
		}
		gap := credit.TransactionDate.Sub(fee.TransactionDate.Time)
		if gap < 0 {
			gap = -gap
		}
		if gap > inboundFeePairWindow {
			continue
		}
		// Score is distance from the expected 1.40%, tie-broken by time. Lower
		// is better.
		expected := float64(creditKobo) * inboundFeeExpectedBps / 10000
		score := math.Abs(float64(feeKobo)-expected) + gap.Seconds()
		switch {
		case bestIdx < 0 || score < bestScore:
			bestIdx, bestScore, tied = ci, score, false
		case score == bestScore:
			tied = true
		}
	}
	if bestIdx < 0 || tied {
		return 0, false
	}
	return bestIdx, true
}

func isFeeRow(row FWStatementEntry) bool {
	return strings.EqualFold(strings.TrimSpace(row.Type), fwRowTypeFee)
}

// nettedFeeKobo returns the fee row's amount when it was netted off a credit,
// and 0 when it was billed as its own wallet debit. See pairFundingFees for
// why the balance movement is the discriminator.
//
// CONFIRMED 2026-09-10: Flutterwave debits the funding fee as its OWN
// transaction the moment the money lands, so in production this returns 0 and
// GrossKobo equals the credit as it appears on the statement. The netting
// branch is kept because a provider can change this without telling us, and
// because being wrong in that direction shorts every buyer.
//
// Netting requires POSITIVE evidence: both balance sides present and equal.
// Absent balance data used to satisfy before == after and silently add the fee
// back, inflating gross by ~1.4% and failing the exact-amount match for every
// payer. Missing evidence now means "not netted", which is also what we know to
// be true.
func nettedFeeKobo(fee FWStatementEntry) int64 {
	before := KoboFromMajor(fee.Balance.Before.Float64())
	after := KoboFromMajor(fee.Balance.After.Float64())
	if before <= 0 || after <= 0 {
		return 0
	}
	if before != after {
		return 0
	}
	return KoboFromMajor(fee.Amount.Float64())
}

// ─── small helpers ─────────────────────────────────────────────────────────

// KoboFromMajor converts a major-unit amount (naira) to integer kobo. Rounding
// is half-away-from-zero: the provider sends 2dp values as float64, so 19.72
// arrives as 19.719999… and a truncation would quietly lose a kobo per row.
//
// Exported for the same reason as IsCustomerFunding: the reconciler compares
// integer sums against ours with a ZERO tolerance, so it must round identically
// or every tick reports a phantom gap.
func KoboFromMajor(v float64) int64 {
	return int64(math.Round(v * 100))
}

// truncate caps a string at max BYTES without splitting a rune. Postgres sizes
// varchar in characters, so a byte cap is always within the column; cutting
// mid-rune, though, would produce invalid UTF-8 and fail the INSERT — turning
// a cosmetic overflow into a lost credit.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
