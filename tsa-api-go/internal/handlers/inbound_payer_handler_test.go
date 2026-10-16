package handlers

import (
	"errors"
	"strings"
	"testing"
)

// A name-enquiry failure wraps the bank provider's text, which can carry the
// account number; the log keeps only its last four digits.
func TestPayerLogSafeMasksAccountNumbers(t *testing.T) {
	got := payerLogSafe(errors.New("payer declaration: the bank could not be reached: resolve 0123456789 at 058: timeout"))
	if strings.Contains(got, "0123456789") {
		t.Fatalf("log line leaks the account number: %q", got)
	}
	if !strings.Contains(got, "******6789") {
		t.Errorf("log line = %q, want the last four kept", got)
	}
}
