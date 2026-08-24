package services

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ── Why these assertions use literals, not the source columns ────────────────
//
// The tempting test for a migration that runs `SET amount_minor = ngn_kobo` is
// to assert `amount_minor == ngn_kobo`. That proves only that the statement
// ran. It cannot fail on the mistake that actually matters here: TRANSPOSITION.
// ITIntent carries five same-typed numeric(38,0) columns — ngn_kobo, rate_kobo,
// fee_ngn_kobo, net_ngn_kobo, platform_kept_kobo — and a mapping that copied
// fee into net, or rate into amount, would satisfy every "equals its source"
// assertion while silently mis-stating money.
//
// So each column is seeded with a DISTINCT, non-round value and asserted
// against the literal that value must land on. Getting any pair the wrong way
// round fails loudly and names the field.

func TestITIntentMinorMirror_IsNotTransposed(t *testing.T) {
	// Distinct and non-round: no two values coincide, so a swap cannot hide.
	const (
		wantAmount       = "13781"
		wantRate         = "2757"
		wantFee          = "991"
		wantNet          = "12790"
		wantPlatformKept = "433"
	)

	intent := &models.ITIntent{
		ID:               uuid.New(),
		UserID:           uuid.New(),
		Direction:        models.ITDirectionSell,
		Currency:         "NGN",
		McgpAmount:       "5000000000000000000",
		NgnKobo:          wantAmount,
		RateKobo:         wantRate,
		FeeNgnKobo:       wantFee,
		NetNgnKobo:       wantNet,
		PlatformKeptKobo: wantPlatformKept,
	}

	if err := intent.BeforeSave(nil); err != nil {
		t.Fatalf("BeforeSave: %v", err)
	}

	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"AmountMinor", intent.AmountMinor, wantAmount},
		{"RateMinor", intent.RateMinor, wantRate},
		{"FeeMinor", intent.FeeMinor, wantFee},
		{"NetMinor", intent.NetMinor, wantNet},
		{"PlatformKeptMinor", intent.PlatformKeptMinor, wantPlatformKept},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q — the minor columns are transposed", c.field, c.got, c.want)
		}
	}
}

// TestITIntentMinorMirror_PreservesEmptyFeeColumns pins that "empty" survives
// the mirror rather than becoming "0".
//
// The distinction is load-bearing and predates this work: intents created
// before fee deduction shipped carry empty fee columns, and readers treat empty
// as "no fee was taken, net == gross" so those intents settle at the terms they
// were created under. A mirror that helpfully normalised empty to "0" would
// make a legacy intent look like one that was charged a zero fee — the same
// value, a different meaning, and the fee engine branches on it.
func TestITIntentMinorMirror_PreservesEmptyFeeColumns(t *testing.T) {
	intent := &models.ITIntent{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Direction: models.ITDirectionBuy,
		Currency:  "NGN",
		NgnKobo:   "50000",
		RateKobo:  "2757",
		// Fee columns deliberately unset — a pre-fee-engine intent.
	}

	if err := intent.BeforeSave(nil); err != nil {
		t.Fatalf("BeforeSave: %v", err)
	}

	for _, c := range []struct{ field, got string }{
		{"FeeMinor", intent.FeeMinor},
		{"NetMinor", intent.NetMinor},
		{"PlatformKeptMinor", intent.PlatformKeptMinor},
	} {
		if c.got != "" {
			t.Errorf("%s = %q, want \"\" — empty means \"no fee taken\", which is not the same "+
				"as a zero fee, and the fee engine branches on the difference", c.field, c.got)
		}
	}
}

// TestITIntentCurrency_FallbackIsScopedToHistoricalRows pins the one place the
// empty-means-NGN reading is allowed to live.
func TestITIntentCurrency_FallbackIsScopedToHistoricalRows(t *testing.T) {
	cases := []struct {
		name   string
		intent *models.ITIntent
		want   string
	}{
		{"explicit NGN", &models.ITIntent{Currency: "NGN"}, "NGN"},
		{"explicit GHS", &models.ITIntent{Currency: "GHS"}, "GHS"},
		{"lowercase is normalised", &models.ITIntent{Currency: "ghs"}, "GHS"},
		{"padded is trimmed", &models.ITIntent{Currency: " GHS "}, "GHS"},
		{"pre-migration row reads as NGN", &models.ITIntent{}, "NGN"},
		{"nil reads as NGN", nil, "NGN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := models.ITIntentCurrency(c.intent); got != c.want {
				t.Errorf("ITIntentCurrency = %q, want %q", got, c.want)
			}
		})
	}
}

// TestITIntent_HasNoCurrencyColumnDefault guards the property that makes the
// fallback safe: because the column has no database default, a write path that
// forgets Currency produces an empty value that ITIntentCurrency reads as NGN
// for historical rows — but nothing turns it into a silent default for new
// writes. If someone adds `default:'NGN'` to the struct tag, the distinction
// between "written before the column existed" and "written wrong today"
// disappears, and a cedi intent could be stored as naira with no trace.
func TestITIntent_HasNoCurrencyColumnDefault(t *testing.T) {
	field, ok := currencyFieldTag()
	if !ok {
		t.Fatal("ITIntent has no Currency field")
	}
	if containsDefault(field) {
		t.Errorf("ITIntent.Currency gorm tag has a default (%q). Remove it: the missing default "+
			"is what stops a forgotten Currency from silently denominating an intent in naira.", field)
	}
}

// currencyFieldTag reads the gorm struct tag on ITIntent.Currency by reflection.
func currencyFieldTag() (string, bool) {
	f, ok := reflect.TypeOf(models.ITIntent{}).FieldByName("Currency")
	if !ok {
		return "", false
	}
	return f.Tag.Get("gorm"), true
}

func containsDefault(tag string) bool {
	return strings.Contains(strings.ToLower(tag), "default:")
}
