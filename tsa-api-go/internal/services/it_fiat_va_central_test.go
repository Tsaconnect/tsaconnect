package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// A virtual account minted after the cutover is an account nothing polls: the
// ingestor reads the company account's statement only, so money sent to a fresh
// VA lands where no claim can ever be matched against it. Activation has to
// refuse rather than hand out that dead end.
func TestActivateFiatVA_RefusedUnderCentralInbound(t *testing.T) {
	s := newITTestService(t)
	s.SetInboundCentral(true, fullCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}

	if _, err := s.ActivateFiatVA(context.Background(), user, "BVN", "12345678901"); !errors.Is(err, ErrITVARetired) {
		t.Fatalf("ActivateFiatVA err = %v, want ErrITVARetired", err)
	}
}

// The mirror: a fully configured account with the mode still off must not retire
// anything. Staging the INBOUND_CENTRAL_* values is how an operator prepares a
// cutover, and it must leave activation exactly as it was.
func TestActivateFiatVA_UnaffectedInVAMode(t *testing.T) {
	s := newITTestService(t)
	s.SetInboundCentral(false, fullCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}

	_, err := s.ActivateFiatVA(context.Background(), user, "BVN", "12345678901")
	if errors.Is(err, ErrITVARetired) {
		t.Fatal("ActivateFiatVA refused as retired in VA mode")
	}
	// No Flutterwave client in this rig, so the VA path stops at its own
	// not-configured error — proving the call reached the legacy path.
	if !errors.Is(err, ErrITVANotConfigured) {
		t.Fatalf("ActivateFiatVA err = %v, want ErrITVANotConfigured", err)
	}
}
