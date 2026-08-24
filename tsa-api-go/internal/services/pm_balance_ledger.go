package services

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Per-currency PM ledger operations.
//
// These supersede the NGN-only operations in pm_ngn_ledger.go, which remain and
// delegate here with "NGN" so no caller is forced to change in the same commit.
//
// ── Which representation the guard evaluates ──
//
// During dual-write there are two representations of a PM's naira balance —
// payment_merchants.available_ngn/reserved_ngn (legacy) and a pm_balances row —
// and a guarded UPDATE has to pick one to evaluate. The choice is per currency,
// and it is deliberate:
//
//	NGN     → the guard runs on the LEGACY columns, exactly as it does today.
//	          NGN behaviour is therefore byte-identical, and the legacy columns
//	          stay authoritative for as long as rolling back is still an option.
//	non-NGN → the guard runs on pm_balances, because no legacy column exists.
//
// Both representations are written inside ONE transaction, so a crash between
// them cannot leave them divergent. When the legacy columns are eventually
// dropped, NGN simply joins the non-NGN path and this split disappears.
//
// ── Why credit is an upsert and the others are not ──
//
// Reserve/Consume/Release/Withdraw all operate on a balance that must already
// exist, and correctly report "insufficient" when it does not. Credit is the
// operation that CREATES a currency's balance, and a conditional UPDATE cannot
// create a row — so it is a single INSERT ... ON CONFLICT DO UPDATE rather than
// a read-modify-write. Two concurrent first-credits in a currency (a webhook and
// its retry, say) would otherwise race, and the sole caller logs and swallows
// that error — losing real money that already landed in the provider balance.

// normaliseCurrency upper-cases and trims a currency code for ledger use.
func normaliseCurrency(currency string) string {
	return strings.ToUpper(strings.TrimSpace(currency))
}

// isLegacyNGN reports whether this currency is mirrored by the legacy
// payment_merchants columns.
func isLegacyNGN(currency string) bool { return normaliseCurrency(currency) == "NGN" }

// ReserveIn atomically ear-marks amount of a PM's free balance in one currency.
// Returns ok=false (no error) when the free balance cannot cover it — including
// when the PM holds no balance in that currency at all.
func (s *PMService) ReserveIn(ctx context.Context, pmID uuid.UUID, currency, amount string) (bool, error) {
	return s.reserveInOn(s.db.WithContext(ctx), pmID, currency, amount)
}

// ReserveInTx is the transaction-aware variant, so a sell-open can reserve the
// fiat payout in the SAME transaction as the crypto-capacity update.
func (s *PMService) ReserveInTx(tx *gorm.DB, pmID uuid.UUID, currency, amount string) (bool, error) {
	return s.reserveInOn(tx, pmID, currency, amount)
}

func (s *PMService) reserveInOn(db *gorm.DB, pmID uuid.UUID, currency, amount string) (bool, error) {
	if err := validateNGNAmount(amount); err != nil {
		return false, err
	}
	cur := normaliseCurrency(currency)

	var ok bool
	err := inTx(db, func(tx *gorm.DB) error {
		// Guard on the authoritative representation for this currency.
		if isLegacyNGN(cur) {
			legacy, err := s.reserveNGNOn(tx, pmID, amount)
			if err != nil {
				return err
			}
			if !legacy {
				return nil // insufficient — ok stays false, nothing mirrored
			}
			// Mirror into pm_balances. Unconditional: the legacy guard has
			// already proven sufficiency, and this row is not yet authoritative.
			if err := mirrorReserve(tx, pmID, cur, amount); err != nil {
				return err
			}
			ok = true
			return nil
		}

		res := tx.Model(&models.PMBalance{}).
			Where(
				"pm_id = ? AND currency = ? AND (CAST(available AS NUMERIC) - CAST(reserved AS NUMERIC)) >= CAST(? AS NUMERIC)",
				pmID, cur, amount,
			).
			Update("reserved", gorm.Expr("CAST(reserved AS NUMERIC) + CAST(? AS NUMERIC)", amount))
		if res.Error != nil {
			return res.Error
		}
		ok = res.RowsAffected == 1
		return nil
	})
	return ok, err
}

// ConsumeIn deducts amount from both available and reserved in one currency,
// confirming a completed payout. Returns ErrNGNAlreadySettled (not a hard error)
// when the guard fails, which most commonly means a prior attempt already
// settled this reservation — see ConsumeNGN for why that distinction matters
// under webhook redelivery.
func (s *PMService) ConsumeIn(ctx context.Context, pmID uuid.UUID, currency, amount string) error {
	if err := validateNGNAmount(amount); err != nil {
		return err
	}
	cur := normaliseCurrency(currency)

	return inTx(s.db.WithContext(ctx), func(tx *gorm.DB) error {
		if isLegacyNGN(cur) {
			if err := s.consumeNGNOn(tx, pmID, amount); err != nil {
				return err // includes ErrNGNAlreadySettled — nothing mirrored
			}
			return mirrorConsume(tx, pmID, cur, amount)
		}

		res := tx.Model(&models.PMBalance{}).
			Where(
				"pm_id = ? AND currency = ? AND CAST(reserved AS NUMERIC) >= CAST(? AS NUMERIC)",
				pmID, cur, amount,
			).
			Updates(map[string]any{
				"available": gorm.Expr("CAST(available AS NUMERIC) - CAST(? AS NUMERIC)", amount),
				"reserved":  gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", amount),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNGNAlreadySettled
		}
		return nil
	})
}

// ReleaseIn un-reserves amount in one currency (cancelling without paying out).
// Only reserved decreases.
func (s *PMService) ReleaseIn(ctx context.Context, pmID uuid.UUID, currency, amount string) error {
	return s.releaseInOn(s.db.WithContext(ctx), pmID, currency, amount)
}

// ReleaseInTx is the transaction-aware variant of ReleaseIn.
func (s *PMService) ReleaseInTx(tx *gorm.DB, pmID uuid.UUID, currency, amount string) error {
	return s.releaseInOn(tx, pmID, currency, amount)
}

func (s *PMService) releaseInOn(db *gorm.DB, pmID uuid.UUID, currency, amount string) error {
	if err := validateNGNAmount(amount); err != nil {
		return err
	}
	cur := normaliseCurrency(currency)

	return inTx(db, func(tx *gorm.DB) error {
		if isLegacyNGN(cur) {
			if err := s.releaseNGNOn(tx, pmID, amount); err != nil {
				return err
			}
			return mirrorRelease(tx, pmID, cur, amount)
		}

		res := tx.Model(&models.PMBalance{}).
			Where(
				"pm_id = ? AND currency = ? AND CAST(reserved AS NUMERIC) >= CAST(? AS NUMERIC)",
				pmID, cur, amount,
			).
			Update("reserved", gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", amount))
		if res.Error != nil {
			return res.Error
		}
		return nil
	})
}

// CreditIn adds amount to a PM's available balance in one currency, creating
// the balance row if this is the first credit in that currency.
//
// The upsert is the whole point: see the file header. A FirstOrCreate here would
// reintroduce exactly the read-modify-write race the rest of this ledger is
// built to avoid.
func (s *PMService) CreditIn(ctx context.Context, pmID uuid.UUID, currency, amount string) error {
	if err := validateNGNAmount(amount); err != nil {
		return err
	}
	cur := normaliseCurrency(currency)

	return inTx(s.db.WithContext(ctx), func(tx *gorm.DB) error {
		if isLegacyNGN(cur) {
			if err := s.creditNGNOn(tx, pmID, amount); err != nil {
				return err
			}
		}
		return upsertCredit(tx, pmID, cur, amount)
	})
}

// WithdrawIn deducts amount from available only, guarded so the FREE balance
// covers it — a withdrawal must never dip into funds reserved for in-flight
// trades. Returns ok=false (no error) when the free balance is insufficient.
func (s *PMService) WithdrawIn(ctx context.Context, pmID uuid.UUID, currency, amount string) (bool, error) {
	if err := validateNGNAmount(amount); err != nil {
		return false, err
	}
	cur := normaliseCurrency(currency)

	var ok bool
	err := inTx(s.db.WithContext(ctx), func(tx *gorm.DB) error {
		if isLegacyNGN(cur) {
			legacy, err := s.withdrawNGNOn(tx, pmID, amount)
			if err != nil {
				return err
			}
			if !legacy {
				return nil
			}
			if err := mirrorWithdraw(tx, pmID, cur, amount); err != nil {
				return err
			}
			ok = true
			return nil
		}

		res := tx.Model(&models.PMBalance{}).
			Where(
				"pm_id = ? AND currency = ? AND (CAST(available AS NUMERIC) - CAST(reserved AS NUMERIC)) >= CAST(? AS NUMERIC)",
				pmID, cur, amount,
			).
			Update("available", gorm.Expr("CAST(available AS NUMERIC) - CAST(? AS NUMERIC)", amount))
		if res.Error != nil {
			return res.Error
		}
		ok = res.RowsAffected == 1
		return nil
	})
	return ok, err
}

// ── internals ────────────────────────────────────────────────────────────────

// inTx runs fn inside a transaction, unless db is already one — in which case it
// runs fn directly so a caller-supplied transaction is not nested. GORM's
// Transaction() would use a savepoint there, which would let an inner rollback
// leave the outer transaction alive with only half the ledger written.
func inTx(db *gorm.DB, fn func(tx *gorm.DB) error) error {
	if _, alreadyInTx := db.Statement.ConnPool.(gorm.TxCommitter); alreadyInTx {
		return fn(db)
	}
	return db.Transaction(fn)
}

// upsertCredit is the single atomic statement that creates-or-increments a
// balance row.
func upsertCredit(tx *gorm.DB, pmID uuid.UUID, currency, amount string) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "pm_id"}, {Name: "currency"}},
		DoUpdates: clause.Assignments(map[string]any{
			"available":  gorm.Expr("CAST(pm_balances.available AS NUMERIC) + CAST(? AS NUMERIC)", amount),
			"updated_at": gorm.Expr("CURRENT_TIMESTAMP"),
		}),
	}).Create(&models.PMBalance{
		PMID:      pmID,
		Currency:  currency,
		Available: amount,
		// Explicit "0" rather than the zero value: an empty string is rejected by
		// Postgres but silently stored by SQLite, where CAST('' AS NUMERIC) is 0
		// — green tests, failing production.
		Reserved: "0",
	}).Error
}

// The mirror* helpers write the pm_balances side of an NGN operation whose guard
// already passed on the legacy columns. They create the row when absent so a PM
// that predates the backfill still converges.

func mirrorReserve(tx *gorm.DB, pmID uuid.UUID, currency, amount string) error {
	return mirrorAdjust(tx, pmID, currency, map[string]any{
		"reserved": gorm.Expr("CAST(reserved AS NUMERIC) + CAST(? AS NUMERIC)", amount),
	})
}

func mirrorRelease(tx *gorm.DB, pmID uuid.UUID, currency, amount string) error {
	return mirrorAdjust(tx, pmID, currency, map[string]any{
		"reserved": gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", amount),
	})
}

func mirrorWithdraw(tx *gorm.DB, pmID uuid.UUID, currency, amount string) error {
	return mirrorAdjust(tx, pmID, currency, map[string]any{
		"available": gorm.Expr("CAST(available AS NUMERIC) - CAST(? AS NUMERIC)", amount),
	})
}

func mirrorConsume(tx *gorm.DB, pmID uuid.UUID, currency, amount string) error {
	return mirrorAdjust(tx, pmID, currency, map[string]any{
		"available": gorm.Expr("CAST(available AS NUMERIC) - CAST(? AS NUMERIC)", amount),
		"reserved":  gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", amount),
	})
}

// mirrorAdjust applies a delta to an existing pm_balances row.
//
// It deliberately does NOT create the row when absent. A reserve whose legacy
// guard passed proves the PM has funds in the legacy columns, so seeding a fresh
// row at zero and then adding the reservation would produce reserved > available
// — a row that violates chk_pm_balances_ledger and misstates the balance. Every
// PM that exists at migration time gets its row from the backfill, and every PM
// created afterwards gets one on its first credit (an upsert), so a missing row
// here means the two representations have genuinely drifted. Leaving the update
// to affect zero rows is the honest outcome: the divergence check reports it
// rather than papering over it with a fabricated row.
func mirrorAdjust(tx *gorm.DB, pmID uuid.UUID, currency string, updates map[string]any) error {
	return tx.Model(&models.PMBalance{}).
		Where("pm_id = ? AND currency = ?", pmID, currency).
		Updates(updates).Error
}
