package services

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The product fee MUST be wildcard-only so the token-agnostic cart quote
// provably equals the per-token charge (design §5, finding #5b). A per-token
// (non-"*") product/product_merchant rate must be rejected by the write path.
func TestRefreshFromConfirmedRate_RejectsProductPerTokenOverride(t *testing.T) {
	svc, _ := newSeededService(t)

	for _, feature := range []string{models.FeatureProduct, models.FeatureProductMerchant} {
		for _, token := range []string{"USDC", "USDT", "MCGP"} {
			err := svc.RefreshFromConfirmedRate(feature, token, nil, 200, 0, false, true)
			if err == nil {
				t.Errorf("%s/%s: expected rejection of per-token product override, got nil", feature, token)
				continue
			}
			if !strings.Contains(err.Error(), "wildcard-only") {
				t.Errorf("%s/%s: error = %q, want a wildcard-only rejection", feature, token, err)
			}
		}
	}

	// The wildcard ("*") product rows are still accepted (the legitimate shape).
	if err := svc.RefreshFromConfirmedRate(models.FeatureProductMerchant, models.FeeTokenAny, nil, 250, 0, true, true); err != nil {
		t.Errorf("wildcard product_merchant refresh unexpectedly rejected: %v", err)
	}
	if err := svc.RefreshFromConfirmedRate(models.FeatureProduct, models.FeeTokenAny, nil, 0, 0.15, true, true); err != nil {
		t.Errorf("wildcard product gas refresh unexpectedly rejected: %v", err)
	}

	// A NON-product feature may still carry a per-token override (only product is
	// constrained, because only product is quoted token-agnostically).
	if err := svc.RefreshFromConfirmedRate(models.FeatureWithdrawal, "USDT", nil, 60, 0, false, true); err != nil {
		t.Errorf("per-token withdrawal override unexpectedly rejected: %v", err)
	}
}

// Load must refuse to serve a DB that already contains a per-token product row
// (a hand-edited/corrupt replica) rather than quietly quoting a rate that can
// mismatch the charge.
func TestLoad_RejectsExistingProductPerTokenRow(t *testing.T) {
	db := setupFeeConfigTestDB(t)
	// Insert a forbidden per-token product_merchant row directly (bypassing the
	// write guard) to simulate a corrupt/hand-edited replica.
	if err := db.Exec(
		`INSERT INTO fee_configs (id, feature, token_symbol, chain_id, bps, flat_amount_usd, mcgp_exempt, active)
		 VALUES (?, ?, 'USDT', NULL, 300, 0, 0, 1)`,
		uuid.NewString(), models.FeatureProductMerchant,
	).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	svc := &FeeConfigService{db: db, cache: map[string]models.FeeConfig{}, lastKnownGood: map[string]models.FeeConfig{}}
	err := svc.Load()
	if err == nil {
		t.Fatalf("Load over a per-token product row must error, got nil")
	}
	if !strings.Contains(err.Error(), "wildcard-only") {
		t.Errorf("Load error = %q, want a wildcard-only rejection", err)
	}
}

// productFeeWildcardOnly only constrains the two product features; every other
// feature (and the wildcard product rows) is unconstrained.
func TestProductFeeWildcardOnly_Scope(t *testing.T) {
	mustReject := []struct{ feature, token string }{
		{models.FeatureProduct, "USDC"},
		{models.FeatureProductMerchant, "usdt"}, // case-insensitive
	}
	for _, c := range mustReject {
		if !productFeeWildcardOnly(c.feature, c.token) {
			t.Errorf("productFeeWildcardOnly(%s,%s) = false, want true", c.feature, c.token)
		}
	}
	mustAllow := []struct{ feature, token string }{
		{models.FeatureProduct, models.FeeTokenAny},
		{models.FeatureProductMerchant, "*"},
		{models.FeatureWithdrawal, "USDT"},
		{models.FeatureSwap, "USDC"},
	}
	for _, c := range mustAllow {
		if productFeeWildcardOnly(c.feature, c.token) {
			t.Errorf("productFeeWildcardOnly(%s,%s) = true, want false", c.feature, c.token)
		}
	}
}
