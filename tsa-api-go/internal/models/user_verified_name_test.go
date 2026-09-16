package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The verified legal name is identity data, and the whole User struct rides
// along on payloads it was never designed for. It must not appear in any
// generic marshal — the surfaces that need it opt in field by field.
func TestUserJSONOmitsVerifiedLegalName(t *testing.T) {
	now := time.Now()
	u := User{
		ID:                  uuid.New(),
		Name:                "Typed At Signup",
		VerifiedLegalName:   "ADEBAYO CHUKWUEMEKA OKONKWO",
		VerifiedLegalNameAt: &now,
	}

	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal user: %v", err)
	}

	if strings.Contains(string(raw), "ADEBAYO") {
		t.Fatalf("verified legal name leaked into the default user payload: %s", raw)
	}
	if strings.Contains(string(raw), "verifiedLegalName") {
		t.Fatalf("verified legal name key leaked into the default user payload: %s", raw)
	}
}
