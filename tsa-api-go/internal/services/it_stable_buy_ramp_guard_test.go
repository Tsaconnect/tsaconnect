package services

import (
	"context"
	"errors"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Which provider serves a currency's stablecoin ramp is a server fact, held in
// the currency registry. Until now only the app read it: the buy screens route
// naira to Paycrest and leave these Flutterwave screens unlinked. That made the
// routing a client-side convention, and an app build older than the cutover
// kept calling this path directly.
//
// The cost was real. On 2026-09-09 a naira buyer on an older build opened a
// Flutterwave purchase for USDC on Base; Flutterwave refused the conversion ten
// times, the intent hit the retry cap, and the money is still held with no
// operator path out. The registry already said Paycrest — nothing asked it.
func TestCreateStableBuyIntent_RefusesACurrencyThatRampsElsewhere(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	// Production has NGN's ramp_provider column empty, which the registry
	// resolves against the compiled-in default rather than reading literally.
	// That default is Paycrest, so this fixture is the production shape.
	s.SetCurrencyRegistry(NewCurrencyRegistry(newCurrencyTestDB(t)))

	_, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if !errors.Is(err, ErrStableBuyWrongRamp) {
		t.Fatalf("err = %v, want ErrStableBuyWrongRamp", err)
	}

	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d intents written for a currency that ramps elsewhere, want 0", n)
	}
}

// An explicit column value always wins over the compiled-in default, so an
// operator can move a currency back onto this rail without a deploy — the same
// escape hatch CurrencyRegistry.RampProvider already documents.
func TestCreateStableBuyIntent_AllowsACurrencyPinnedToFlutterwave(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	db := newCurrencyTestDB(t)
	// The registry seeds its rows on construction, so the pin has to land
	// after that and be reloaded — updating first writes to an empty table.
	reg := NewCurrencyRegistry(db)
	if err := db.Model(&models.Currency{}).Where("code = ?", "NGN").
		Update("ramp_provider", RampProviderFlutterwave).Error; err != nil {
		t.Fatalf("pin NGN to flutterwave: %v", err)
	}
	if err := reg.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	s.SetCurrencyRegistry(reg)

	if _, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM); err != nil {
		t.Fatalf("unexpected error for a currency pinned to this rail: %v", err)
	}
}

// The registry is optional on ITService — several construction sites never wire
// one. Gating on a nil registry would take the rail down wherever that is true,
// which is a worse failure than the one being fixed.
func TestCreateStableBuyIntent_NilRegistryIsNoGate(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	// Deliberately do not call SetCurrencyRegistry.

	if _, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM); err != nil {
		t.Fatalf("unexpected error with no registry wired: %v", err)
	}
}
