package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Paycrest on-ramp orders carry the payer's identity (source.kyc): full name,
// date of birth and, for naira, a BVN or NIN. The user types the number and
// birth date once; the app keeps them in the phone's secure store and sends
// them with each Paycrest buy. The server passes them straight to Paycrest:
// they are never written to the database and never logged. A BuyerID prints as
// "[redacted]" so a stray %v cannot leak one.

// BuyerID is the identity a Paycrest buy is opened under.
type BuyerID struct {
	// Type is "bvn" or "nin".
	Type string
	// Number is the 11-digit BVN or NIN.
	Number string
	// BirthDate is YYYY-MM-DD.
	BirthDate string
}

const (
	BuyerIDTypeBVN = "bvn"
	BuyerIDTypeNIN = "nin"
)

var (
	// ErrBuyerIDRequired: a Paycrest buy was asked for without the buyer's
	// ID. The app asks for it (only when the phone holds none) and retries.
	ErrBuyerIDRequired = errors.New("buyer id required")
	// ErrBuyerIDInvalid: the ID or birth date is malformed, or the buyer is
	// under 18. Always wrapped in a *BuyerIDInvalidError saying which.
	ErrBuyerIDInvalid = errors.New("buyer id invalid")
	// ErrBuyerIDRejected: Paycrest refused the order over the identity it
	// carried (wrong number, or a name that doesn't match the refund account).
	ErrBuyerIDRejected = errors.New("buyer id rejected")
	// ErrRefundAccountNotBuyers: the bank's name on the refund account is not
	// the buyer's verified name. A Paycrest buy is filed under her identity,
	// so the refund account must be hers.
	ErrRefundAccountNotBuyers = errors.New("refund account is not in the buyer's name")
)

// BuyerIDInvalidError names what is wrong, in words the user reads.
type BuyerIDInvalidError struct{ Reason string }

func (e *BuyerIDInvalidError) Error() string { return "buyer id invalid: " + e.Reason }
func (e *BuyerIDInvalidError) Unwrap() error { return ErrBuyerIDInvalid }

func invalidBuyerID(reason string) error { return &BuyerIDInvalidError{Reason: reason} }

// BuyerIDRequiredCode, BuyerIDInvalidCode and BuyerIDRejectedCode are the
// machine codes the app routes on: ask for the ID, or reopen it for editing.
const (
	BuyerIDRequiredCode = "buyer_id_required"
	BuyerIDInvalidCode  = "buyer_id_invalid"
	BuyerIDRejectedCode = "buyer_id_rejected"
	RefundNotBuyersCode = "refund_account_not_yours"
)

var elevenDigits = regexp.MustCompile(`^[0-9]{11}$`)

func (b BuyerID) String() string   { return "[redacted]" }
func (b BuyerID) GoString() string { return "[redacted]" }

// Normalized returns the ID trimmed and lower-cased where it matters, after
// checking it: an 11-digit BVN or NIN and an 18-or-over birth date, as
// Paycrest requires for a naira on-ramp. now is the clock the age is
// measured against.
func (b BuyerID) Normalized(now time.Time) (BuyerID, error) {
	out := BuyerID{
		Type:      strings.ToLower(strings.TrimSpace(b.Type)),
		Number:    strings.ReplaceAll(strings.TrimSpace(b.Number), " ", ""),
		BirthDate: strings.TrimSpace(b.BirthDate),
	}
	if out.Type != BuyerIDTypeBVN && out.Type != BuyerIDTypeNIN {
		return BuyerID{}, invalidBuyerID("Choose BVN or NIN.")
	}
	if !elevenDigits.MatchString(out.Number) {
		return BuyerID{}, invalidBuyerID(fmt.Sprintf("Your %s is 11 digits.", strings.ToUpper(out.Type)))
	}
	born, err := time.Parse("2006-01-02", out.BirthDate)
	if err != nil {
		return BuyerID{}, invalidBuyerID("Enter your date of birth.")
	}
	if born.AddDate(18, 0, 0).After(now) {
		return BuyerID{}, invalidBuyerID("You must be 18 or over to buy.")
	}
	if born.Before(now.AddDate(-120, 0, 0)) {
		return BuyerID{}, invalidBuyerID("Check your date of birth.")
	}
	return out, nil
}

// paycrestIDDocumentType is Paycrest's name for the document: BVN is its own
// type, a NIN is a national ID.
func (b BuyerID) paycrestIDDocumentType() string {
	if b.Type == BuyerIDTypeBVN {
		return "BVN"
	}
	return "nationalID"
}

// PaycrestPersonKYC is source.kyc for a person, as Paycrest's v2 on-ramp
// expects it. FullName must match the refund account holder's name.
type PaycrestPersonKYC struct {
	FullName string
	ID       BuyerID
}

func (k PaycrestPersonKYC) String() string { return "[redacted]" }

func (k PaycrestPersonKYC) payload() map[string]any {
	return map[string]any{
		"entityType": "person",
		"personDetails": map[string]any{
			"fullName":  k.FullName,
			"birthDate": k.ID.BirthDate,
			"idDocument": map[string]any{
				"type":          k.ID.paycrestIDDocumentType(),
				"number":        k.ID.Number,
				"issuerCountry": "NG",
			},
		},
	}
}

// redactIDNumbers blanks every run of 11 or more digits, wherever it sits
// ("BVN22212345678", or one glued to other digits), so a provider error that
// echoes a BVN/NIN back never reaches a log or a user. Shorter runs (a 10-digit
// NUBAN, amounts) stay readable for diagnosis; redactBuyerID also blanks this
// buyer's own values.
var digitRun = regexp.MustCompile(`[0-9]+`)

func redactIDNumbers(s string) string {
	return digitRun.ReplaceAllStringFunc(s, func(run string) string {
		if len(run) >= 11 {
			return "[redacted]"
		}
		return run
	})
}

// redactBuyerID blanks this buyer's own number and birth date from an error
// before anything logs or returns it.
func redactBuyerID(err error, id BuyerID) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	clean := msg
	for _, v := range []string{id.Number, id.BirthDate} {
		if v != "" {
			clean = strings.ReplaceAll(clean, v, "[redacted]")
		}
	}
	clean = redactIDNumbers(clean)
	if clean == msg {
		return err
	}
	return &redactedError{msg: clean, cause: err}
}

// redactedError keeps the cause's identity for errors.Is while showing only
// the cleaned text.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

// paycrestKYCRefusal reports whether Paycrest's refusal is about the identity
// on the order: a 400 naming the source's kyc fields, or a name mismatch. A
// bare "mismatch" (rate, token, network) is not hers to fix this way.
func paycrestKYCRefusal(err error) bool {
	var re *PaycrestRequestError
	if !errors.As(err, &re) || re.Status != 400 {
		return false
	}
	msg := strings.ToLower(re.Msg)
	for _, k := range []string{"kyc", "fullname", "birthdate", "iddocument", "persondetails"} {
		if strings.Contains(msg, k) {
			return true
		}
	}
	if !strings.Contains(msg, "does not match") && !strings.Contains(msg, "mismatch") {
		return false
	}
	// A mismatch about her identity, never one about a rate, token or network.
	for _, k := range []string{"name", "bvn", "nin", "birth", "dob", "identity", "id document", "id number"} {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}

// ScrubPaycrestKYC returns a Paycrest payload fit for storage: every "kyc"
// object removed, at any depth, in case Paycrest echoes source.kyc back in a
// webhook. Called after the signature is checked against the raw body. A body
// that is not JSON keeps only its text with long digit runs blanked.
//
// A body with no kyc object is returned byte for byte, so the stored payload
// still matches its stored signature; only a body that carried one is
// re-encoded (numbers kept exact).
func ScrubPaycrestKYC(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return []byte(redactIDNumbers(string(body)))
	}
	if !dropKYC(v) {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return []byte(redactIDNumbers(string(body)))
	}
	return out
}

// dropKYC removes every "kyc" key in place and reports whether it found one.
func dropKYC(v any) bool {
	found := false
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if strings.EqualFold(k, "kyc") {
				delete(t, k)
				found = true
				continue
			}
			if dropKYC(child) {
				found = true
			}
		}
	case []any:
		for _, child := range t {
			if dropKYC(child) {
				found = true
			}
		}
	}
	return found
}
