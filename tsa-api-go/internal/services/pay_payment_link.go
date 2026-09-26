package services

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// CreatePayLinkInput is an owner asking a customer to pay: how much, what it
// is for, who it is for, their own reference if they keep one, and how many
// days the link stays open.
type CreatePayLinkInput struct {
	AmountKobo    int64  `json:"amount"`
	Description   string `json:"description"`
	CustomerName  string `json:"customerName"`
	Reference     string `json:"reference"`
	ExpiresInDays int    `json:"expiresInDays"`
}

const (
	payLinkDefaultDays        = 7
	payLinkMaxCustomerNameLen = 100
	payLinkReferencePrefix    = "link_"
	payReferenceRandomChars   = 12
)

// payLinkDays are how long an owner may keep a link open. A link waits on a
// person reading a message, not on a checkout page they are already looking
// at, so it lives for days where an API payment lives for minutes.
var payLinkDays = []int{1, 3, 7}

// CreateLink makes a payment link: a payment the owner creates by hand and
// sends to a customer, rather than one their own server creates with an API
// key. It goes through the same create as the API — the merchant's amount
// bounds, NGN, the fee split and the settlement snapshot all apply — with the
// rules that differ for a person typing it in:
//   - the description is required, because it is what the payer sees;
//   - a customer name may be given (it is shown to the payer too);
//   - it stays open 1, 3 or 7 days (default 7);
//   - a blank reference is made up for them, and one they already used is
//     refused (ErrPayLinkReferenceTaken), never returned as a retry;
//   - there is no return URL: a link finishes on the TSA receipt.
func (s *PayPaymentService) CreateLink(ctx context.Context, m *models.PayMerchant, mode string, in CreatePayLinkInput) (*models.PayPayment, error) {
	description := strings.TrimSpace(in.Description)
	if description == "" {
		return nil, payInvalid("description is required", "Say what the payment is for")
	}
	if payHasHiddenChars(description) {
		return nil, payInvalid("description must not contain control or format characters", "What it's for can't contain hidden or control characters")
	}
	customerName := strings.TrimSpace(in.CustomerName)
	if utf8.RuneCountInString(customerName) > payLinkMaxCustomerNameLen {
		return nil, payInvalid(fmt.Sprintf("customerName must be at most %d characters", payLinkMaxCustomerNameLen),
			fmt.Sprintf("The customer's name must be at most %d characters", payLinkMaxCustomerNameLen))
	}
	if payHasHiddenChars(customerName) {
		return nil, payInvalid("customerName must not contain control or format characters", "The customer's name can't contain hidden or control characters")
	}
	days := in.ExpiresInDays
	if days == 0 {
		days = payLinkDefaultDays
	}
	if !slices.Contains(payLinkDays, days) {
		return nil, payInvalid("expiresInDays must be 1, 3 or 7", "A link can stay open for 1, 3 or 7 days")
	}
	reference := strings.TrimSpace(in.Reference)
	if reference == "" {
		var err error
		if reference, err = newPayReference(payLinkReferencePrefix); err != nil {
			return nil, err
		}
	}

	p, _, err := s.create(ctx, m, mode, CreatePayPaymentInput{
		AmountKobo:       in.AmountKobo,
		Currency:         "NGN",
		Reference:        reference,
		Description:      description,
		ExpiresInSeconds: days * 24 * 60 * 60,
	}, payCreateOptions{source: models.PaySourceLink, customerName: customerName, maxExpirySeconds: payLinkDefaultDays * 24 * 60 * 60})
	return p, err
}

// payZeroWidthJoiner is the one format character allowed: it is what holds a
// combined emoji (👨‍👩‍👧) together, and it cannot reorder or hide text on its own.
const payZeroWidthJoiner = '\u200d'

// payHasHiddenChars reports whether s holds a control character (Cc) or an
// invisible format character (Cf) other than the zero-width joiner. Both are
// text the payer is shown: a right-to-left override or a zero-width space can
// make what they read differ from what the owner typed, and a NUL cannot be
// stored at all.
func payHasHiddenChars(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return r != payZeroWidthJoiner && unicode.In(r, unicode.Cc, unicode.Cf)
	})
}

// newPayReference is the reference a payment gets when nobody names one: the
// prefix ("link_", "open_") and 12 random base32 characters (60 bits),
// lower-cased so it reads easily if a customer quotes it back.
func newPayReference(prefix string) (string, error) {
	suffix, err := payRandomLower(payReferenceRandomChars)
	if err != nil {
		return "", err
	}
	return prefix + suffix, nil
}

// payRandomLower is n random lower-case base32 characters (a-z, 2-7): 5 bits
// each, from crypto/rand.
func payRandomLower(n int) (string, error) {
	b := make([]byte, (n*5+7)/8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.EncodeToString(b)[:n]), nil
}
