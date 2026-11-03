package services

import "strings"

// WithdrawalDestination is one catalog entry: a currency/rail (and, for
// mobile_money, network) combination the app's withdrawal-destination picker
// may offer. This is the single source of truth for what IT withdrawal
// supports — SupportedWithdrawalQuoteCurrencies (it_quote.go) and Sell's
// destination validation (IsSupportedWithdrawalDestination, below) both
// derive from it so the two never drift apart.
type WithdrawalDestination struct {
	Currency string `json:"currency"`
	Rail     string `json:"rail"`
	// Network is set only for the mobile_money rail (e.g. "wave").
	Network string `json:"network,omitempty"`
	Label   string `json:"label"`
	// Country is the ISO-2 country whose rail the payout settles on, and the
	// bank directory the app loads for a "bank" field. Empty for the euro area.
	Country string `json:"country,omitempty"`
	// Fields is what the app must collect for this corridor beyond the legacy
	// bank code + account number + name enquiry. Empty means the legacy NGN
	// form. The server validates against the same list before the debit, so
	// a corridor's requirements ship without an app release.
	Fields []RecipientField `json:"fields,omitempty"`
}

// RecipientField is one input of a corridor's payout form. Keys are the
// app-facing names; the Flutterwave gateway maps them onto the v4 recipient
// (payoutRecipientFromDetails).
type RecipientField struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"` // text | email | phone | country | select | bank | branch
	Options []string `json:"options,omitempty"`
	Hint    string   `json:"hint,omitempty"`
}

// Recipient detail keys. Values travel as one string map end to end.
const (
	PayoutFieldFirstName      = "firstName"
	PayoutFieldLastName       = "lastName"
	PayoutFieldBank           = "bank"   // provider bank code, from the country's bank list
	PayoutFieldBankID         = "bankId" // provider bank id, sent alongside "bank" for branch lookups; never required
	PayoutFieldBranch         = "branch" // provider branch code
	PayoutFieldAccountNumber  = "accountNumber"
	PayoutFieldRoutingNumber  = "routingNumber"
	PayoutFieldSwiftCode      = "swiftCode"
	PayoutFieldSortCode       = "sortCode"
	PayoutFieldBankName       = "bankName"
	PayoutFieldAccountType    = "accountType"
	PayoutFieldEmail          = "email"
	PayoutFieldPhoneCode      = "phoneCountryCode"
	PayoutFieldPhoneNumber    = "phoneNumber"
	PayoutFieldAddressLine1   = "addressLine1"
	PayoutFieldCity           = "city"
	PayoutFieldState          = "state"
	PayoutFieldPostalCode     = "postalCode"
	PayoutFieldAddressCountry = "addressCountry"
)

// Field sets, from the Flutterwave v4 recipient schema as the live API
// reported it on 2026-11-03 (validation errors on deliberately incomplete
// POST /transfers/recipients and /direct-transfers bodies; see
// docs/plans/2026-11-03-international-payout-corridors.md).
var (
	fieldsName = []RecipientField{
		{Key: PayoutFieldFirstName, Label: "First name", Kind: "text"},
		{Key: PayoutFieldLastName, Label: "Last name", Kind: "text"},
	}
	// Corridors outside Africa also need the recipient's contact and address.
	fieldsContact = []RecipientField{
		{Key: PayoutFieldEmail, Label: "Email", Kind: "email"},
		{Key: PayoutFieldPhoneCode, Label: "Phone country code", Kind: "text", Hint: "e.g. 1 or 44"},
		{Key: PayoutFieldPhoneNumber, Label: "Phone number", Kind: "phone"},
		{Key: PayoutFieldAddressLine1, Label: "Street address", Kind: "text"},
		{Key: PayoutFieldCity, Label: "City", Kind: "text"},
		{Key: PayoutFieldState, Label: "State / region", Kind: "text"},
		{Key: PayoutFieldPostalCode, Label: "Postal code", Kind: "text"},
		{Key: PayoutFieldAddressCountry, Label: "Country", Kind: "country", Hint: "2-letter code, e.g. US"},
	}
)

func corridorFields(groups ...[]RecipientField) []RecipientField {
	var out []RecipientField
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// WithdrawalDestinationCatalog is the product catalog of withdrawal
// destinations IT offers. It is the single source of truth: the app's picker
// renders from it, SupportedWithdrawalQuoteCurrencies derives from it, and
// Sell's validation checks against it — so a rail cannot be offered that the
// API would then reject.
//
// Every foreign corridor is paid from the NGN balance: Flutterwave converts on
// the way (applies_to=destination_currency), so no foreign float is needed.
//
// Bank rails only. Mobile money — which matters most in Ghana and Kenya — is
// deliberately still out: it needs per-network verification of the provider's
// `network` values, and an unverified network fails at payout, after the
// on-chain debit.
//
// Every entry here must also be PayoutEnabled in the currency registry. The
// registry governs whether a currency may move money at all; this list governs
// whether we present it as a product.
var WithdrawalDestinationCatalog = []WithdrawalDestination{
	{Currency: "NGN", Rail: "bank", Label: "Nigerian Bank Account", Country: "NG"},
	{
		Currency: "GHS", Rail: "bank", Label: "Ghanaian Bank Account", Country: "GH",
		Fields: corridorFields(fieldsName, []RecipientField{
			{Key: PayoutFieldBank, Label: "Bank", Kind: "bank"},
			{Key: PayoutFieldBranch, Label: "Branch", Kind: "branch"},
			{Key: PayoutFieldAccountNumber, Label: "Account number", Kind: "text"},
		}),
	},
	{
		Currency: "KES", Rail: "bank", Label: "Kenyan Bank Account", Country: "KE",
		Fields: corridorFields(fieldsName, []RecipientField{
			{Key: PayoutFieldBank, Label: "Bank", Kind: "bank"},
			{Key: PayoutFieldAccountNumber, Label: "Account number", Kind: "text"},
		}),
	},
	{
		Currency: "ZAR", Rail: "bank", Label: "South African Bank Account", Country: "ZA",
		Fields: corridorFields(fieldsName, []RecipientField{
			{Key: PayoutFieldBank, Label: "Bank", Kind: "bank"},
			{Key: PayoutFieldAccountNumber, Label: "Account number", Kind: "text"},
		}, fieldsContact),
	},
	{
		Currency: "USD", Rail: "bank", Label: "US Bank Account", Country: "US",
		Fields: corridorFields(fieldsName, []RecipientField{
			{Key: PayoutFieldBank, Label: "Bank", Kind: "bank"},
			{Key: PayoutFieldAccountNumber, Label: "Account number", Kind: "text"},
			{Key: PayoutFieldRoutingNumber, Label: "Routing number (ABA)", Kind: "text"},
			{Key: PayoutFieldSwiftCode, Label: "SWIFT / BIC", Kind: "text"},
			{Key: PayoutFieldAccountType, Label: "Account type", Kind: "select", Options: []string{"checking", "savings"}},
		}, fieldsContact),
	},
	{
		Currency: "EUR", Rail: "bank", Label: "Euro Bank Account (SEPA)",
		Fields: corridorFields(fieldsName, []RecipientField{
			{Key: PayoutFieldBankName, Label: "Bank name", Kind: "text"},
			{Key: PayoutFieldAccountNumber, Label: "IBAN", Kind: "text"},
			{Key: PayoutFieldSwiftCode, Label: "SWIFT / BIC", Kind: "text"},
		}, fieldsContact),
	},
	{
		Currency: "GBP", Rail: "bank", Label: "UK Bank Account", Country: "GB",
		Fields: corridorFields(fieldsName, []RecipientField{
			{Key: PayoutFieldBankName, Label: "Bank name", Kind: "text"},
			{Key: PayoutFieldAccountNumber, Label: "Account number", Kind: "text"},
			{Key: PayoutFieldSortCode, Label: "Sort code", Kind: "text", Hint: "6 digits"},
			{Key: PayoutFieldAccountType, Label: "Account type", Kind: "select", Options: []string{"individual", "corporate"}},
		}, fieldsContact),
	},
}

// LookupWithdrawalDestination returns the catalog entry for (currency, rail,
// network), case-insensitively; network is only compared for mobile_money.
func LookupWithdrawalDestination(currency, rail, network string) (WithdrawalDestination, bool) {
	currency = strings.TrimSpace(currency)
	rail = strings.ToLower(strings.TrimSpace(rail))
	network = strings.TrimSpace(network)
	for _, d := range WithdrawalDestinationCatalog {
		if !strings.EqualFold(d.Currency, currency) || !strings.EqualFold(d.Rail, rail) {
			continue
		}
		if rail == "mobile_money" && !strings.EqualFold(d.Network, network) {
			continue
		}
		return d, true
	}
	return WithdrawalDestination{}, false
}

// ValidatePayoutDetails checks a sell's recipient details against the
// corridor's fields and returns them cleaned (trimmed, upper-cased where the
// provider expects codes). The error lists every field at fault, so the app
// can mark them all at once. A corridor with no fields takes no details.
func ValidatePayoutDetails(d WithdrawalDestination, details map[string]string) (map[string]string, error) {
	if len(d.Fields) == 0 {
		return nil, nil
	}
	clean := make(map[string]string, len(d.Fields)+1)
	var bad []string
	for _, f := range d.Fields {
		v := strings.TrimSpace(details[f.Key])
		switch f.Kind {
		case "country":
			v = strings.ToUpper(v)
			if len(v) != 2 || !isASCIILetters(v) {
				bad = append(bad, f.Key)
				continue
			}
		case "email":
			if !strings.Contains(v, "@") || strings.ContainsAny(v, " ,;") {
				bad = append(bad, f.Key)
				continue
			}
		case "select":
			v = strings.ToLower(v)
			if !containsFold(f.Options, v) {
				bad = append(bad, f.Key)
				continue
			}
		}
		switch f.Key {
		case PayoutFieldSwiftCode:
			v = strings.ToUpper(strings.ReplaceAll(v, " ", ""))
			if n := len(v); n != 8 && n != 11 {
				bad = append(bad, f.Key)
				continue
			}
		case PayoutFieldSortCode:
			v = strings.NewReplacer("-", "", " ", "").Replace(v)
			if len(v) != 6 || !allDigits(v) {
				bad = append(bad, f.Key)
				continue
			}
		case PayoutFieldRoutingNumber:
			if len(v) != 9 || !allDigits(v) {
				bad = append(bad, f.Key)
				continue
			}
		case PayoutFieldPhoneCode:
			v = strings.TrimPrefix(v, "+")
			if v == "" || len(v) > 4 || !allDigits(v) {
				bad = append(bad, f.Key)
				continue
			}
		case PayoutFieldAccountNumber:
			if d.Currency == "EUR" {
				v = strings.ToUpper(strings.ReplaceAll(v, " ", ""))
			}
		}
		if v == "" || len(v) > 120 {
			bad = append(bad, f.Key)
			continue
		}
		clean[f.Key] = v
	}
	if len(bad) > 0 {
		return nil, &PayoutDetailsError{Fields: bad}
	}
	if id := strings.TrimSpace(details[PayoutFieldBankID]); id != "" && allDigits(id) {
		clean[PayoutFieldBankID] = id
	}
	return clean, nil
}

// PayoutDetailsError names the recipient fields that are missing or invalid.
type PayoutDetailsError struct{ Fields []string }

func (e *PayoutDetailsError) Error() string {
	return "payout details missing or invalid: " + strings.Join(e.Fields, ", ")
}

func isASCIILetters(s string) bool {
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return s != ""
}

// IsSupportedWithdrawalDestination reports whether (currency, rail, network)
// is a catalog entry, case-insensitively. network is only compared for the
// mobile_money rail — pass "" for bank.
func IsSupportedWithdrawalDestination(currency, rail, network string) bool {
	_, ok := LookupWithdrawalDestination(currency, rail, network)
	return ok
}

// payoutCountryFor maps a payout currency to the ISO-2 country whose rail it
// settles on, for Flutterwave's fee endpoint.
//
// Currency and country are not interchangeable to that endpoint: it prices a
// rail, and "GHS in NG" is not one. Anything unrecognised returns "" so the
// parameter is omitted entirely rather than defaulting to a country that would
// silently price against the wrong market — an omitted filter gives a generic
// quote, a wrong one gives a confidently incorrect answer.
func payoutCountryFor(currency string) string {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "NGN":
		return "NG"
	case "GHS":
		return "GH"
	case "USD":
		return "US"
	case "GBP":
		return "GB"
	case "KES":
		return "KE"
	case "ZAR":
		return "ZA"
	case "EUR":
		// The euro area has no single ISO-2 country. Omitting the filter is the
		// honest answer: any member state would be a guess about which rail the
		// payout actually settles on.
		return ""
	default:
		return ""
	}
}
