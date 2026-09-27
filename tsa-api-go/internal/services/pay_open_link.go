package services

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Open links: the reusable kind of payment link. The owner makes one (a label,
// optionally a narrower amount range) and prints or shares its address; every
// customer who opens it starts their own payment, for the amount they enter,
// through the same create every other payment goes through.

const (
	payOpenLinkMaxLabelLen = 80
	payOpenLinkMaxNoteLen  = 140
	payOpenLinkSlugBaseLen = 40
	payOpenLinkSlugChars   = 8 // 40 random bits: a slug is not guessable
	payOpenLinkMaxSlugLen  = 64
	payOpenReferencePrefix = "open_"
	payOpenLinkRecentLimit = 20
	payFreshAttempts       = 5 // payTryFresh's tries
)

var (
	// ErrPayOpenLinkNotFound is an open link that doesn't exist — or, for an
	// owner, isn't theirs or is in the other mode.
	ErrPayOpenLinkNotFound = errors.New("this payment link doesn't exist")
	// ErrPayOpenLinkClosed is an open link that can't take a payment now — see
	// payOpenLinkNotTaking for why.
	ErrPayOpenLinkClosed = errors.New("this payment link isn't taking payments right now")
	// ErrPayOpenLinkStale is an edit made against a version of the link that
	// has since changed.
	ErrPayOpenLinkStale = errors.New("this payment link changed since it was read")
)

// PayOpenLinkInput is an owner making an open link: what the payer sees, and
// optionally a narrower range than the merchant's own (0 = the merchant's bound).
type PayOpenLinkInput struct {
	Label         string `json:"label"`
	MinAmountKobo int64  `json:"minAmount"`
	MaxAmountKobo int64  `json:"maxAmount"`
}

// PayOpenLinkUpdate is an owner's edit of an open link: every field it carries
// replaces what is there, and every field it omits is left as it is.
// ExpectedVersion is the version the owner was looking at, and is required.
type PayOpenLinkUpdate struct {
	Label           *string `json:"label"`
	MinAmountKobo   *int64  `json:"minAmount"`
	MaxAmountKobo   *int64  `json:"maxAmount"`
	Active          *bool   `json:"active"`
	ExpectedVersion *int    `json:"expectedVersion"`
}

// Why an open link isn't taking payments (PayOpenLinkView.NotTakingReason).
const (
	PayOpenLinkSuspended    = "suspended"            // the merchant is suspended
	PayOpenLinkNoPayout     = "no_payout"            // the merchant has nowhere to be paid
	PayOpenLinkLiveOff      = "live_off"             // a live link, and live payments are off
	PayOpenLinkPaused       = "paused"               // the owner paused it
	PayOpenLinkRangeOutside = "range_outside_limits" // the merchant's limits moved and no amount fits the link's range
)

// PayOpenLinkView is an open link as its owner reads it.
//   - MinAmount/MaxAmount are the link's own range as set (0 = the merchant's
//     bound, exactly as an edit sends them); MerchantMinAmount/MerchantMaxAmount
//     are those bounds, so a screen can show the effective range.
//   - PaidCount is its succeeded payments. PaidTotal is the sum of their
//     amounts — the price, as the payments list shows it; ReceivedTotal is what
//     the merchant receives from them (the price less any share of the fee they
//     carry). Neither includes a fee the customer paid on top.
//   - TakingPayments is whether a customer can pay through it right now, and
//     NotTakingReason why not when they can't.
type PayOpenLinkView struct {
	ID                uuid.UUID `json:"id"`
	Mode              string    `json:"mode"`
	Slug              string    `json:"slug"`
	URL               string    `json:"url"`
	Label             string    `json:"label"`
	MinAmount         int64     `json:"minAmount"`
	MaxAmount         int64     `json:"maxAmount"`
	MerchantMinAmount int64     `json:"merchantMinAmount"`
	MerchantMaxAmount int64     `json:"merchantMaxAmount"`
	Active            bool      `json:"active"`
	Version           int       `json:"version"`
	TakingPayments    bool      `json:"takingPayments"`
	NotTakingReason   string    `json:"notTakingReason,omitempty"`
	PaidCount         int64     `json:"paidCount"`
	PaidTotal         int64     `json:"paidTotal"`
	ReceivedTotal     int64     `json:"receivedTotal"`
	CreatedAt         time.Time `json:"createdAt"`
}

// PayOpenLinkPublicView is what the payer's page reads before they enter an
// amount: whose link, what for, the range they may pay (effective — the
// link's own narrowed by the merchant's), and whether it takes payments now. A
// closed link never shows a range no amount fits.
type PayOpenLinkPublicView struct {
	MerchantName string `json:"merchantName"`
	Label        string `json:"label"`
	Mode         string `json:"mode"`
	MinAmount    int64  `json:"minAmount"`
	MaxAmount    int64  `json:"maxAmount"`
	Active       bool   `json:"active"`
}

// PayOpenPaymentInput is a customer starting a payment through an open link.
type PayOpenPaymentInput struct {
	AmountKobo int64  `json:"amount"`
	Note       string `json:"note"`
}

// CreateOpenLink makes an open link for merchant m in mode.
func (s *PayPaymentService) CreateOpenLink(ctx context.Context, m *models.PayMerchant, mode string, in PayOpenLinkInput) (*models.PayOpenLink, error) {
	if m.SettlementMode == "" {
		return nil, ErrPayMerchantNotReady
	}
	label, err := payOpenLinkLabel(in.Label)
	if err != nil {
		return nil, err
	}
	if err := payOpenLinkRange(m, in.MinAmountKobo, in.MaxAmountKobo); err != nil {
		return nil, err
	}
	l := &models.PayOpenLink{MerchantID: m.ID, Mode: mode, Label: label, MinAmountKobo: in.MinAmountKobo,
		MaxAmountKobo: in.MaxAmountKobo, Active: true, Version: 1}
	err = s.withFreshSlug(ctx, m, func(slug string) error {
		l.Slug = slug
		return s.db.WithContext(ctx).Create(l).Error
	})
	if err != nil {
		return nil, err
	}
	return l, nil
}

// payOpenLinkSlugSuffix is a slug's random half. A variable only so a test can
// make it collide.
var payOpenLinkSlugSuffix = func() (string, error) { return payRandomLower(payOpenLinkSlugChars) }

// errPaySlugRetired is a new slug that was once another link's address.
var errPaySlugRetired = errors.New("slug was retired")

// withFreshSlug calls write with a new slug from m's business name until one is
// neither live (the unique index refuses it) nor retired (PayRetiredSlug).
func (s *PayPaymentService) withFreshSlug(ctx context.Context, m *models.PayMerchant, write func(slug string) error) error {
	return payTryFresh(func() (string, error) {
		suffix, err := payOpenLinkSlugSuffix()
		return payOpenLinkSlugBase(m.BusinessName) + "-" + suffix, err
	}, func(slug string) error {
		var retired int64
		if err := s.db.WithContext(ctx).Model(&models.PayRetiredSlug{}).Where("slug = ?", slug).Count(&retired).Error; err != nil {
			return err
		}
		if retired > 0 {
			return errPaySlugRetired
		}
		return write(slug)
	}, func(err error) bool { return isUniqueViolation(err) || errors.Is(err, errPaySlugRetired) })
}

// payOpenLinkSlugFormat is every slug a link can have: lower-case letters and
// digits in dash-separated runs.
var payOpenLinkSlugFormat = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidPayOpenLinkSlug reports whether slug (lower-cased) could be an open
// link's address at all — so a request for anything else is answered not found
// without a lookup (a NUL or stray byte would otherwise reach the database).
func ValidPayOpenLinkSlug(slug string) bool {
	slug = strings.ToLower(slug)
	return len(slug) <= payOpenLinkMaxSlugLen && payOpenLinkSlugFormat.MatchString(slug)
}

// payTryFresh calls try with a fresh random value from next until one doesn't
// collide with a value already stored (collided says whether it did). Random
// values collide rarely, so a few tries are plenty; a collision every time is a
// real fault, and is returned.
func payTryFresh(next func() (string, error), try func(string) error, collided func(error) bool) error {
	for attempt := 1; ; attempt++ {
		v, err := next()
		if err != nil {
			return err
		}
		err = try(v)
		if err == nil || !collided(err) || attempt == payFreshAttempts {
			return err
		}
	}
}

// payOpenLinkSlugBase is the readable half of a slug: the business name in
// lower-case letters and digits, anything else a single "-", at most 40
// characters — "Mama Ngozi Store" is "mama-ngozi-store".
func payOpenLinkSlugBase(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if b.Len() >= payOpenLinkSlugBaseLen {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	if base := strings.Trim(b.String(), "-"); base != "" {
		return base
	}
	return "pay"
}

// payOpenLinkLabel checks an open link's label and returns it trimmed.
func payOpenLinkLabel(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	switch {
	case label == "":
		return "", payInvalid("label is required", "Give the link a name your customers will recognise")
	case utf8.RuneCountInString(label) > payOpenLinkMaxLabelLen:
		return "", payInvalid(fmt.Sprintf("label must be at most %d characters", payOpenLinkMaxLabelLen),
			fmt.Sprintf("The name must be at most %d characters", payOpenLinkMaxLabelLen))
	case payHasHiddenChars(label):
		return "", payInvalid("label must not contain control or format characters", "The name can't contain hidden or control characters")
	}
	return label, nil
}

// payOpenLinkRange checks an open link's range against merchant m: each bound,
// when set (0 = the merchant's own), lies inside the merchant's; together they
// must leave some amount to pay.
func payOpenLinkRange(m *models.PayMerchant, lo, hi int64) error {
	within := func(v int64) bool { return v == 0 || (v >= m.MinAmountKobo && v <= m.MaxAmountKobo) }
	bounds := "₦" + koboAsNairaText(m.MinAmountKobo) + " and ₦" + koboAsNairaText(m.MaxAmountKobo)
	switch {
	case !within(lo):
		return payInvalid("minAmount is outside the merchant's bounds", "The smallest amount must be between "+bounds)
	case !within(hi):
		return payInvalid("maxAmount is outside the merchant's bounds", "The largest amount must be between "+bounds)
	case lo != 0 && hi != 0 && lo > hi:
		return payInvalid("minAmount is more than maxAmount", "The smallest amount can't be more than the largest")
	}
	return nil
}

// payOpenLinkBounds is the range a customer may pay through l: the link's own,
// narrowed by the merchant's bounds as they are now.
func payOpenLinkBounds(l *models.PayOpenLink, m *models.PayMerchant) (lo, hi int64) {
	return max(l.MinAmountKobo, m.MinAmountKobo), min(cmp.Or(l.MaxAmountKobo, m.MaxAmountKobo), m.MaxAmountKobo)
}

// payOpenLinkNotTaking is why a customer can't start a payment through l now,
// or "" when they can: the merchant could be paid through any other payment
// (active, a payout set, live allowed for a live link), the owner hasn't paused
// it, and some amount still fits its range — the merchant's limits may have
// moved since the range was set.
func payOpenLinkNotTaking(l *models.PayOpenLink, m *models.PayMerchant, liveEnabled bool) string {
	switch {
	case m.Status != models.PayMerchantActive:
		return PayOpenLinkSuspended
	case m.SettlementMode == "":
		return PayOpenLinkNoPayout
	case l.Mode == models.PayModeLive && !m.LiveAllowed(liveEnabled):
		return PayOpenLinkLiveOff
	case !l.Active:
		return PayOpenLinkPaused
	}
	if lo, hi := payOpenLinkBounds(l, m); lo > hi {
		return PayOpenLinkRangeOutside
	}
	return ""
}

// OpenLinkForMerchant is one of merchant m's open links in mode.
func (s *PayPaymentService) OpenLinkForMerchant(ctx context.Context, merchantID uuid.UUID, mode string, id uuid.UUID) (*models.PayOpenLink, error) {
	var l models.PayOpenLink
	err := s.db.WithContext(ctx).Where("id = ? AND merchant_id = ? AND mode = ?", id, merchantID, mode).First(&l).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPayOpenLinkNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// OpenLinks is one page of merchant m's open links in mode, newest first, with
// the total. The caller bounds page and limit.
func (s *PayPaymentService) OpenLinks(ctx context.Context, merchantID uuid.UUID, mode string, page, limit int) ([]models.PayOpenLink, int64, error) {
	// A fresh query for each statement, as List does: a gorm chain reused
	// after Count carries its state into the Find.
	mine := func() *gorm.DB {
		return s.db.WithContext(ctx).Model(&models.PayOpenLink{}).Where("merchant_id = ? AND mode = ?", merchantID, mode)
	}
	var total int64
	if err := mine().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var ls []models.PayOpenLink
	err := mine().Order("created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&ls).Error
	return ls, total, err
}

// UpdateOpenLink edits one of m's current open links, against the version the
// owner read.
func (s *PayPaymentService) UpdateOpenLink(ctx context.Context, m *models.PayMerchant, mode string, id uuid.UUID, in PayOpenLinkUpdate) (*models.PayOpenLink, error) {
	if in.ExpectedVersion == nil {
		return nil, payInvalid("expectedVersion is required", "Reload the link and try again")
	}
	cur, err := s.OpenLinkForMerchant(ctx, m.ID, mode, id)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{"version": gorm.Expr("version + 1")}
	if in.Label != nil {
		label, err := payOpenLinkLabel(*in.Label)
		if err != nil {
			return nil, err
		}
		fields["label"] = label
	}
	lo, hi := cur.MinAmountKobo, cur.MaxAmountKobo
	if in.MinAmountKobo != nil {
		lo = *in.MinAmountKobo
	}
	if in.MaxAmountKobo != nil {
		hi = *in.MaxAmountKobo
	}
	// The range is checked only when it changes: pausing or renaming a link
	// must still work after the merchant's own limits have moved past it.
	if lo != cur.MinAmountKobo || hi != cur.MaxAmountKobo {
		if err := payOpenLinkRange(m, lo, hi); err != nil {
			return nil, err
		}
		fields["min_amount_kobo"], fields["max_amount_kobo"] = lo, hi
	}
	if in.Active != nil {
		fields["active"] = *in.Active
	}
	res := s.db.WithContext(ctx).Model(&models.PayOpenLink{}).
		Where("id = ? AND merchant_id = ? AND mode = ? AND version = ?", id, m.ID, mode, *in.ExpectedVersion).
		Updates(fields)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrPayOpenLinkStale // edited since it was read
	}
	return s.OpenLinkForMerchant(ctx, m.ID, mode, id)
}

// ReplaceOpenLink gives one of m's open links a new address — a printed QR was
// copied, say. The old slug stops existing at once and is retired for good
// (PayRetiredSlug), in the same transaction; the link keeps its label, range,
// pause state and every payment made through it. Guarded on the version read,
// so a replace racing an edit (or another replace) is ErrPayOpenLinkStale
// rather than silently winning.
func (s *PayPaymentService) ReplaceOpenLink(ctx context.Context, m *models.PayMerchant, mode string, id uuid.UUID) (*models.PayOpenLink, error) {
	cur, err := s.OpenLinkForMerchant(ctx, m.ID, mode, id)
	if err != nil {
		return nil, err
	}
	// Each try is its own transaction: on Postgres a slug collision aborts the
	// transaction it happens in, so a retry can't share it.
	err = s.withFreshSlug(ctx, m, func(slug string) error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&models.PayOpenLink{}).Where("id = ? AND version = ?", cur.ID, cur.Version).
				Updates(map[string]any{"slug": slug, "version": gorm.Expr("version + 1")})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return ErrPayOpenLinkStale
			}
			return tx.Create(&models.PayRetiredSlug{Slug: cur.Slug, OpenLinkID: cur.ID, RetiredAt: s.now()}).Error
		})
	})
	if err != nil {
		return nil, err
	}
	return s.OpenLinkForMerchant(ctx, m.ID, mode, id)
}

// OpenLinkViews is ls as their owner reads them, each with its succeeded
// payments counted and summed in one query.
func (s *PayPaymentService) OpenLinkViews(ctx context.Context, m *models.PayMerchant, ls []models.PayOpenLink, liveEnabled bool) ([]PayOpenLinkView, error) {
	ids := make([]uuid.UUID, len(ls))
	for i := range ls {
		ids[i] = ls[i].ID
	}
	type paid struct {
		OpenLinkID uuid.UUID
		Count      int64
		Total      int64
		Received   int64
	}
	var rows []paid
	if len(ids) > 0 {
		if err := s.db.WithContext(ctx).Model(&models.PayPayment{}).
			Select("open_link_id, COUNT(*) AS count, COALESCE(SUM(amount_kobo), 0) AS total, COALESCE(SUM(merchant_net_kobo), 0) AS received").
			Where("open_link_id IN ? AND status = ?", ids, models.PayStatusSucceeded).
			Group("open_link_id").Scan(&rows).Error; err != nil {
			return nil, err
		}
	}
	byLink := make(map[uuid.UUID]paid, len(rows))
	for _, r := range rows {
		byLink[r.OpenLinkID] = r
	}
	views := make([]PayOpenLinkView, len(ls))
	for i := range ls {
		l := &ls[i]
		why := payOpenLinkNotTaking(l, m, liveEnabled)
		views[i] = PayOpenLinkView{ID: l.ID, Mode: l.Mode, Slug: l.Slug, URL: s.openLinkURL(l.Slug), Label: l.Label,
			MinAmount: l.MinAmountKobo, MaxAmount: l.MaxAmountKobo, MerchantMinAmount: m.MinAmountKobo, MerchantMaxAmount: m.MaxAmountKobo,
			Active: l.Active, Version: l.Version, TakingPayments: why == "", NotTakingReason: why, PaidCount: byLink[l.ID].Count,
			PaidTotal: byLink[l.ID].Total, ReceivedTotal: byLink[l.ID].Received, CreatedAt: l.CreatedAt}
	}
	return views, nil
}

// OpenLinkPayments is the most recent payments made through open link id that
// someone actually started paying (see payHideUntriedOpen).
func (s *PayPaymentService) OpenLinkPayments(ctx context.Context, id uuid.UUID) ([]models.PayPayment, error) {
	var ps []models.PayPayment
	err := payHideUntriedOpen(s.db.WithContext(ctx).Where("open_link_id = ?", id)).
		Order("created_at DESC").Limit(payOpenLinkRecentLimit).Find(&ps).Error
	return ps, err
}

// payHideUntriedOpen leaves out untried open-link payments
// (PayPayment.Untried): scan noise, not payments an owner needs to see.
func payHideUntriedOpen(q *gorm.DB) *gorm.DB {
	return q.Where("NOT (" + models.PayUntriedOpenSQL + ")")
}

// openLinkURL is the address a customer opens: the public checkout's /l/ page.
func (s *PayPaymentService) openLinkURL(slug string) string {
	return strings.TrimRight(s.checkoutBase, "/") + "/l/" + slug
}

// openLinkBySlug is the open link at slug and its merchant.
func (s *PayPaymentService) openLinkBySlug(ctx context.Context, slug string) (*models.PayOpenLink, *models.PayMerchant, error) {
	var l models.PayOpenLink
	if err := s.db.WithContext(ctx).Where("slug = ?", strings.ToLower(strings.TrimSpace(slug))).First(&l).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrPayOpenLinkNotFound
		}
		return nil, nil, err
	}
	m, err := s.merchants.Get(ctx, l.MerchantID)
	if err != nil {
		return nil, nil, err
	}
	return &l, m, nil
}

// OpenLinkPublic is what the payer's page shows for slug. A link that can't
// take a payment now reads active:false (the page says so) rather than not
// found; only a slug that never existed is not found.
func (s *PayPaymentService) OpenLinkPublic(ctx context.Context, slug string, liveEnabled bool) (*PayOpenLinkPublicView, error) {
	l, m, err := s.openLinkBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	lo, hi := payOpenLinkBounds(l, m)
	if lo > hi {
		lo, hi = m.MinAmountKobo, m.MaxAmountKobo // closed anyway; never show a range no amount fits
	}
	return &PayOpenLinkPublicView{MerchantName: m.BusinessName, Label: l.Label, Mode: l.Mode, MinAmount: lo, MaxAmount: hi,
		Active: payOpenLinkNotTaking(l, m, liveEnabled) == ""}, nil
}

// CreateFromOpenLink is a customer starting a payment through the open link at
// slug, for the amount they entered: a new payment every time (source "open",
// a fresh "open_" reference, the default 30-minute expiry, no return URL),
// through the same create as every other payment. Its description is the
// customer's own note and nothing else — empty when they left none — because it
// is shown as their words; where a purpose is needed the link's label stands in
// (openLinkLabels).
func (s *PayPaymentService) CreateFromOpenLink(ctx context.Context, slug string, in PayOpenPaymentInput, liveEnabled bool) (*models.PayPayment, error) {
	l, m, err := s.openLinkBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if payOpenLinkNotTaking(l, m, liveEnabled) != "" {
		return nil, ErrPayOpenLinkClosed
	}
	note := strings.TrimSpace(in.Note)
	switch {
	case utf8.RuneCountInString(note) > payOpenLinkMaxNoteLen:
		return nil, payInvalid(fmt.Sprintf("note must be at most %d characters", payOpenLinkMaxNoteLen),
			fmt.Sprintf("What it's for must be at most %d characters", payOpenLinkMaxNoteLen))
	case payHasHiddenChars(note):
		return nil, payInvalid("note must not contain control or format characters", "What it's for can't contain hidden or control characters")
	}
	if lo, hi := payOpenLinkBounds(l, m); in.AmountKobo < lo || in.AmountKobo > hi {
		return nil, payInvalid(fmt.Sprintf("amount must be between %d and %d kobo", lo, hi),
			"Enter an amount between ₦"+koboAsNairaText(lo)+" and ₦"+koboAsNairaText(hi))
	}
	// A reference drawn twice is refused as taken (never a retry for anything
	// but the API): draw another.
	var p *models.PayPayment
	err = payTryFresh(func() (string, error) { return newPayReference(payOpenReferencePrefix) }, func(reference string) error {
		var err error
		p, _, err = s.create(ctx, m, l.Mode, CreatePayPaymentInput{AmountKobo: in.AmountKobo, Currency: "NGN", Reference: reference,
			Description: note}, payCreateOptions{source: models.PaySourceOpen, openLinkID: &l.ID, openLinkSlug: l.Slug, maxExpirySeconds: payMaxExpirySeconds})
		return err
	}, payReferenceTaken)
	return p, err
}

// openLinkLabels is the label of each open link in ids, in one query.
func (s *PayPaymentService) openLinkLabels(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	labels := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return labels, nil
	}
	var ls []models.PayOpenLink
	if err := s.db.WithContext(ctx).Select("id", "label").Where("id IN ?", ids).Find(&ls).Error; err != nil {
		return nil, err
	}
	for _, l := range ls {
		labels[l.ID] = l.Label
	}
	return labels, nil
}

// openLinkURLIfTaking is the address payment p was started at, while it is
// still open link l's address and l takes payments, else "" — what the payer's
// expired page offers as "Start a new payment". A link replaced since (a poster
// was copied) sends nobody from an old payment to its new address.
func (s *PayPaymentService) openLinkURLIfTaking(p *models.PayPayment, l *models.PayOpenLink, m *models.PayMerchant, liveEnabled bool) string {
	if p.OpenLinkSlug != l.Slug || payOpenLinkNotTaking(l, m, liveEnabled) != "" {
		return ""
	}
	return s.openLinkURL(l.Slug)
}
