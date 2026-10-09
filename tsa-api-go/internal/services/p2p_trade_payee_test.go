package services

import "testing"

func TestMaskAccountNumber(t *testing.T) {
	if m, l := maskAccountNumber("1234567890"); m != "******7890" || l != "7890" {
		t.Fatalf("got %q %q", m, l)
	}
	if m, l := maskAccountNumber("123"); m != "***" || l != "" {
		t.Fatalf("got %q %q", m, l)
	}
}
