package services

import (
	"strings"
	"testing"
)

func TestUserFacingITFailure_NotOperator(t *testing.T) {
	raw := "credit broadcast: gas estimation failed (transaction would likely revert): failed to estimate gas: execution reverted: Not operator"
	got := UserFacingITFailure(raw)
	if got == "" || strings.Contains(got, "Not operator") || strings.Contains(got, "gas estimation") {
		t.Fatalf("still technical: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "payment") {
		t.Fatalf("expected payment reassurance, got %q", got)
	}
}

func TestUserFacingITFailure_Empty(t *testing.T) {
	if UserFacingITFailure("") != "" {
		t.Fatal("empty in → empty out")
	}
}
