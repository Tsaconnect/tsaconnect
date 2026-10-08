package services

import (
	"crypto/rand"
	"errors"
	"strings"
	"unicode"
)

// Payment codes (spec D7): ten Crockford base32 characters — 50 random bits —
// shown as TSA-XXXXX-XXXXX and stored bare. A payer reads one off the hosted
// page or a printed invoice and types it into the app (Wallet → TSA Pay), so
// the alphabet has no I, L, O or U, and what she types is forgiven the usual
// confusions. The app (tsa-app/utils/payCode.ts) and the landing site
// (tsa-landing src/lib/pay-core.ts) mirror NormalizePayCode: change all three
// together.

const (
	payCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	payCodeLen      = 10
	payCodePrefix   = "TSA"
	// payCodeIndex is the unique index on pay_payments.pay_code. Postgres names
	// it in a unique violation; SQLite names the column instead.
	payCodeIndex  = "idx_pay_payments_pay_code"
	payCodeColumn = "pay_payments.pay_code"
)

// ErrPayCodeNotFound answers every code that names no payment the caller may
// see — malformed or unknown alike, so the two can't be told apart.
var ErrPayCodeNotFound = errors.New("payment code not found")

// newPayCode draws a fresh code from crypto/rand: 56 random bits, of which the
// top 50 become ten base32 characters.
func newPayCode() (string, error) {
	var b [7]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	var bits uint64
	for _, x := range b {
		bits = bits<<8 | uint64(x)
	}
	out := make([]byte, payCodeLen)
	for i := range out {
		out[i] = payCodeAlphabet[(bits>>(51-5*i))&31]
	}
	return string(out), nil
}

// payCodeNext is where createOrExisting and EnsurePayCode draw codes from.
// Tests replace it to force a collision.
var payCodeNext = newPayCode

// NormalizePayCode reads a code as a payer typed or pasted it: whitespace and
// dashes of any kind dropped, upper-cased, a leading TSA dropped when exactly
// a code's worth of characters follows it (a bare code may itself start with
// TSA), O read as 0, I and L as 1. Anything that is then not exactly ten
// alphabet characters is not a code.
func NormalizePayCode(in string) (string, bool) {
	if len(in) > 64 {
		return "", false
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(in) {
		if unicode.IsSpace(r) || unicode.Is(unicode.Pd, r) {
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	if len(s) == len(payCodePrefix)+payCodeLen && strings.HasPrefix(s, payCodePrefix) {
		s = s[len(payCodePrefix):]
	}
	if len(s) != payCodeLen {
		return "", false
	}
	out := []byte(s)
	for i, c := range out {
		switch c {
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		if strings.IndexByte(payCodeAlphabet, c) < 0 {
			return "", false
		}
		out[i] = c
	}
	return string(out), true
}

// FormatPayCode shows a stored code as the payer sees it: TSA-XXXXX-XXXXX.
// Anything that is not a stored code (an empty one, above all) is "".
func FormatPayCode(code string) string {
	if len(code) != payCodeLen {
		return ""
	}
	return payCodePrefix + "-" + code[:5] + "-" + code[5:]
}

// payCodeTaken reports whether err is the pay_code unique index refusing a code
// another payment already has — and only that index, so a reference race (the
// other unique index on the table) is never retried as a collision.
func payCodeTaken(err error) bool {
	if !isUniqueViolation(err) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, payCodeIndex) || strings.Contains(msg, payCodeColumn)
}
