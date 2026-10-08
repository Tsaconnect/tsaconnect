package services

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestProviderTerms_KnownProviders(t *testing.T) {
	fw, err := providerTerms(&models.ITIntent{Provider: models.ITProviderFlutterwave})
	if err != nil || !fw.flutterwaveFulfils || fw.companyWallet || fw.metric != "fw" || fw.provider != "flutterwave" {
		t.Fatalf("flutterwave terms = %+v err = %v", fw, err)
	}
	cw, err := providerTerms(&models.ITIntent{Provider: models.ITProviderTreasury})
	if err != nil || cw.flutterwaveFulfils || !cw.companyWallet || cw.metric != "treasury" || cw.label != "company-wallet" {
		t.Fatalf("treasury terms = %+v err = %v", cw, err)
	}
}

// A row written before the provider column existed is Flutterwave's — the one
// fallback stays in models.ITIntentProvider.
func TestProviderTerms_LegacyEmptyIsFlutterwave(t *testing.T) {
	for _, i := range []*models.ITIntent{nil, {}} {
		terms, err := providerTerms(i)
		if err != nil || !terms.flutterwaveFulfils || terms.provider != models.ITProviderFlutterwave {
			t.Fatalf("legacy terms = %+v err = %v", terms, err)
		}
	}
}

// Spec S45: the default is an error, never Flutterwave's terms.
func TestProviderTerms_UnknownIsAnError(t *testing.T) {
	for _, p := range []string{"tsa_pool", "bogus", "TREASURY"} {
		terms, err := providerTerms(&models.ITIntent{Provider: p})
		if !errors.Is(err, ErrUnknownITProvider) || !errors.Is(err, ErrITBadState) {
			t.Fatalf("provider %q: err = %v, want ErrUnknownITProvider wrapping ErrITBadState", p, err)
		}
		if terms != (itProviderTerms{}) {
			t.Fatalf("provider %q: terms = %+v, want the zero value", p, terms)
		}
	}
}

func TestFlutterwaveFulfilledProviders_IncludesLegacyEmpty(t *testing.T) {
	if got := flutterwaveFulfilledProviders(); !reflect.DeepEqual(got, []string{"", models.ITProviderFlutterwave}) {
		t.Fatalf("flutterwave-fulfilled providers = %q, want ['' flutterwave]", got)
	}
}

func TestKnownITProviders_AllHaveTerms(t *testing.T) {
	for _, p := range knownITProviders {
		if _, err := providerTerms(&models.ITIntent{Provider: p}); err != nil {
			t.Fatalf("known provider %q has no terms: %v", p, err)
		}
	}
}

func TestRequireFlutterwaveFulfils(t *testing.T) {
	if err := requireFlutterwaveFulfils(&models.ITIntent{}, "converted at"); err != nil {
		t.Fatalf("legacy row refused: %v", err)
	}
	err := requireFlutterwaveFulfils(&models.ITIntent{Provider: models.ITProviderTreasury}, "converted at")
	if !errors.Is(err, ErrITBadState) || err.Error() != "invalid IT intent state: a company-wallet buy is never converted at Flutterwave" {
		t.Fatalf("treasury err = %v", err)
	}
	if err := requireFlutterwaveFulfils(&models.ITIntent{Provider: "tsa_pool"}, "sent by"); !errors.Is(err, ErrUnknownITProvider) {
		t.Fatalf("pool err = %v", err)
	}
}
