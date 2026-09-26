package handlers

import (
	"regexp"
	"strings"
)

// bankLookupInvalidMessage is the answer to a malformed bank lookup.
const bankLookupInvalidMessage = "bankCode and a 10-digit accountNumber are required"

var tenDigitsRE = regexp.MustCompile(`^[0-9]{10}$`)

// validBankLookup trims a name-enquiry request and reports whether it can be
// sent: a bank code and a 10-digit (NUBAN) account number.
func validBankLookup(bankCode, accountNumber string) (string, string, bool) {
	bankCode, accountNumber = strings.TrimSpace(bankCode), strings.TrimSpace(accountNumber)
	return bankCode, accountNumber, bankCode != "" && tenDigitsRE.MatchString(accountNumber)
}
