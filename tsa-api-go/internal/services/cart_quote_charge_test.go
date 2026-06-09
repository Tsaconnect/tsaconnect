package services

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// cartLookupsFromService builds the same CartFeeLookups the cart handler wires
// (services.FeeRate(FeatureProductMerchant) / services.FeeFlatUSD(FeatureProduct),
// nil chain). Defined here so the regression test exercises the EXACT path the
// production quote uses without importing the handlers package.
func cartLookupsFromService() *models.CartFeeLookups {
	return &models.CartFeeLookups{
		MerchantBPS: func(token string) (int, bool) {
			return FeeRate(models.FeatureProductMerchant, token, nil)
		},
		GasFlatUSD: func(token string) (float64, bool) {
			return FeeFlatUSD(models.FeatureProduct, token, nil)
		},
	}
}

// TestCartQuoteEqualsCharge is the §6 (B5/I8) quote==charge regression.
//
// It proves the cart QUOTE (models.CalculateCartSummary) computes its gas and
// merchant components from the EXACT same fee_config rows the on-chain CHARGE
// reads (FeeConfigService.RateFor / FeeFlatUSD). If anyone changes the seed, the
// service read and the cart math move together — there is no hardcoded fallback
// in CalculateCartSummary that could disagree with the service.
func TestCartQuoteEqualsCharge(t *testing.T) {
	svc, _ := newSeededService(t)
	prev := Fees
	Fees = svc
	t.Cleanup(func() { Fees = prev })

	lookups := cartLookupsFromService()

	items := []models.CartItem{
		{Quantity: 2, Price: 50.0}, // subtotal 100
		{Quantity: 1, Price: 25.0}, // subtotal 25 → total subtotal 125
	}
	const wantSubtotal = 125.0

	for _, token := range []string{"USDC", "USDT", "MCGP"} {
		t.Run(token, func(t *testing.T) {
			summary := models.CalculateCartSummary(items, nil, 0, token, lookups)

			if summary.Subtotal != wantSubtotal {
				t.Fatalf("subtotal=%v want %v", summary.Subtotal, wantSubtotal)
			}

			// Gas component: cart GasFee == FeeFlatUSD(FeatureProduct, token) when
			// not exempt, else 0. This is the SAME read PrepareApprove makes.
			gasFlat, gasExempt := FeeFlatUSD(models.FeatureProduct, token, nil)
			wantGas := gasFlat
			if gasExempt {
				wantGas = 0
			}
			if summary.GasFee != wantGas {
				t.Errorf("GasFee=%v want %v (FeeFlatUSD(product,%s)=%v exempt=%v)",
					summary.GasFee, wantGas, token, gasFlat, gasExempt)
			}

			// Merchant component: cart MerchantFee == subtotal * FeeRate(
			// FeatureProductMerchant, token)/10000 when not exempt, else 0. SAME
			// read CalculatePlatformFee makes.
			mBps, mExempt := FeeRate(models.FeatureProductMerchant, token, nil)
			wantMerchant := summary.Subtotal * (float64(mBps) / 10000.0)
			if mExempt {
				wantMerchant = 0
			}
			if summary.MerchantFee != wantMerchant {
				t.Errorf("MerchantFee=%v want %v (FeeRate(product_merchant,%s)=%d exempt=%v)",
					summary.MerchantFee, wantMerchant, token, mBps, mExempt)
			}

			// MCGP is exempt on both components.
			if token == "MCGP" {
				if summary.GasFee != 0 {
					t.Errorf("MCGP GasFee=%v want 0", summary.GasFee)
				}
				if summary.MerchantFee != 0 {
					t.Errorf("MCGP MerchantFee=%v want 0", summary.MerchantFee)
				}
			}

			// Total includes gas (buyer-paid) but NOT the merchant fee (baked
			// into listing price). Quote total must be deterministic from the
			// same service reads.
			wantTotal := summary.Subtotal + summary.Shipping + wantGas
			if summary.Total != wantTotal {
				t.Errorf("Total=%v want %v", summary.Total, wantTotal)
			}
		})
	}
}

// TestCartQuoteEqualsCharge_AfterAdminEdit proves the quote tracks the charge
// even after an admin changes the rate at runtime (the divergence the old
// hardcoded-constant design could not survive). Refreshing the replica moves
// BOTH the cart quote and the service read in lock-step.
func TestCartQuoteEqualsCharge_AfterAdminEdit(t *testing.T) {
	svc, _ := newSeededService(t)
	prev := Fees
	Fees = svc
	t.Cleanup(func() { Fees = prev })

	// Confirm a new merchant rate (300 bps) + gas flat ($0.25) on chain.
	if err := svc.RefreshFromConfirmedRate(models.FeatureProductMerchant, models.FeeTokenAny, nil, 300, 0, true, true); err != nil {
		t.Fatalf("refresh merchant: %v", err)
	}
	if err := svc.RefreshFromConfirmedRate(models.FeatureProduct, models.FeeTokenAny, nil, 0, 0.25, true, true); err != nil {
		t.Fatalf("refresh product gas: %v", err)
	}

	lookups := cartLookupsFromService()
	items := []models.CartItem{{Quantity: 1, Price: 200.0}}
	summary := models.CalculateCartSummary(items, nil, 0, "USDC", lookups)

	if summary.GasFee != 0.25 {
		t.Errorf("after-edit GasFee=%v want 0.25", summary.GasFee)
	}
	wantMerchant := 200.0 * (300.0 / 10000.0) // $6
	if summary.MerchantFee != wantMerchant {
		t.Errorf("after-edit MerchantFee=%v want %v", summary.MerchantFee, wantMerchant)
	}
}
