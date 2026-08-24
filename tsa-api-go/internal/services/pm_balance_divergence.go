package services

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// PMBalanceDivergence is one PM whose legacy NGN columns disagree with its
// pm_balances NGN row.
type PMBalanceDivergence struct {
	PMID            string
	LegacyAvailable string
	LegacyReserved  string
	RowAvailable    string
	RowReserved     string
	Reason          string
}

func (d PMBalanceDivergence) String() string {
	return fmt.Sprintf("pm=%s %s: legacy(avail=%s, res=%s) vs row(avail=%s, res=%s)",
		d.PMID, d.Reason, d.LegacyAvailable, d.LegacyReserved, d.RowAvailable, d.RowReserved)
}

// CheckPMBalanceDivergence compares every PM's legacy available_ngn/reserved_ngn
// against its pm_balances NGN row and returns each mismatch.
//
// This exists as a runtime check rather than only a test because the dual-write
// window is where a drift would be introduced, and nothing else would notice it.
// The existing NGN reconciler compares an aggregate against the provider's
// parent balance — it has no view of the two internal representations at all. A
// drift that goes undetected until the legacy columns are dropped is a drift
// that rollback can no longer help with, because the columns you would roll back
// to are the ones that drifted.
//
// Comparison is numeric, not string equality: "1500" and "1500.00" are the same
// balance, and numeric(38,2) round-trips can legitimately differ in formatting.
func CheckPMBalanceDivergence(ctx context.Context, db *gorm.DB) ([]PMBalanceDivergence, error) {
	var pms []models.PaymentMerchant
	if err := db.WithContext(ctx).Find(&pms).Error; err != nil {
		return nil, err
	}

	var rows []models.PMBalance
	if err := db.WithContext(ctx).Where("currency = ?", "NGN").Find(&rows).Error; err != nil {
		return nil, err
	}
	byPM := make(map[string]models.PMBalance, len(rows))
	for _, r := range rows {
		byPM[r.PMID.String()] = r
	}

	var out []PMBalanceDivergence
	for _, pm := range pms {
		id := pm.ID.String()
		row, ok := byPM[id]
		if !ok {
			out = append(out, PMBalanceDivergence{
				PMID:            id,
				LegacyAvailable: pm.AvailableNGN,
				LegacyReserved:  pm.ReservedNGN,
				Reason:          "no NGN pm_balances row",
			})
			continue
		}
		if !decimalEqual(pm.AvailableNGN, row.Available) || !decimalEqual(pm.ReservedNGN, row.Reserved) {
			out = append(out, PMBalanceDivergence{
				PMID:            id,
				LegacyAvailable: pm.AvailableNGN,
				LegacyReserved:  pm.ReservedNGN,
				RowAvailable:    row.Available,
				RowReserved:     row.Reserved,
				Reason:          "amount mismatch",
			})
		}
	}
	return out, nil
}

// decimalEqual compares two decimal strings by value, tolerating differences in
// trailing zeros and a missing fractional part.
func decimalEqual(a, b string) bool {
	na, oka := new(big.Rat).SetString(strings.TrimSpace(orZero(a)))
	nb, okb := new(big.Rat).SetString(strings.TrimSpace(orZero(b)))
	if !oka || !okb {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return na.Cmp(nb) == 0
}

func orZero(s string) string {
	if strings.TrimSpace(s) == "" {
		return "0"
	}
	return s
}
