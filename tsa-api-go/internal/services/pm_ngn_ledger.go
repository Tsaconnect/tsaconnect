package services

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ErrInvalidNGNAmount is returned when a ledger operation receives an amount
// that is empty, negative, non-numeric, or has more than two decimal places.
var ErrInvalidNGNAmount = errors.New("invalid NGN amount: must be a non-negative 2-dp decimal string")

// validateNGNAmount checks that amt is a valid non-negative 2-dp decimal
// string. It reuses the ngnToKobo logic from pm_automation_service.go to
// normalise and validate: the amount must parse, be non-negative, and have
// at most 2 decimal places (ngnToKobo truncates to 2dp, so we validate by
// checking that re-encoding == the normalised form, or more simply that
// the raw fractional part has at most 2 chars).
func validateNGNAmount(amt string) error {
	kobo, ok := ngnToKobo(amt)
	if !ok || kobo < 0 {
		return ErrInvalidNGNAmount
	}
	// Reject more than 2 decimal places: ngnToKobo silently truncates, but we
	// want strict 2-dp for ledger ops (money invariants).
	for i, c := range amt {
		if c == '.' {
			frac := amt[i+1:]
			if len(frac) > 2 {
				return ErrInvalidNGNAmount
			}
			break
		}
	}
	return nil
}

// ReserveNGN atomically ear-marks amountNGN of a PM's free (available -
// reserved) balance for a pending sell trade. Returns ok=false (no error) when
// the free balance is insufficient. The single UPDATE with an inline guard
// expression mirrors the P2P pool pattern in p2p_trade_service.go so it works
// on both Postgres (production) and SQLite (test).
//
// SQL: UPDATE payment_merchants
//
//	SET reserved_ngn = CAST(reserved_ngn AS NUMERIC) + CAST(? AS NUMERIC)
//	WHERE id = ?
//	  AND (CAST(available_ngn AS NUMERIC) - CAST(reserved_ngn AS NUMERIC)) >= CAST(? AS NUMERIC)
func (s *PMService) ReserveNGN(ctx context.Context, pmID uuid.UUID, amountNGN string) (bool, error) {
	return s.reserveNGNOn(s.db.WithContext(ctx), pmID, amountNGN)
}

// ReserveNGNTx is the transaction-aware variant of ReserveNGN: it runs the same
// atomic conditional UPDATE on the supplied *gorm.DB (typically a transaction
// handle) so a sell-open can reserve the fiat payout in the SAME transaction as
// the crypto-capacity update — a rollback of either undoes both. Returns
// ok=false (no error) on insufficient free balance.
func (s *PMService) ReserveNGNTx(tx *gorm.DB, pmID uuid.UUID, amountNGN string) (bool, error) {
	return s.reserveNGNOn(tx, pmID, amountNGN)
}

// reserveNGNOn is the shared conditional UPDATE used by both ReserveNGN (on
// s.db) and ReserveNGNTx (on a caller-supplied tx).
func (s *PMService) reserveNGNOn(db *gorm.DB, pmID uuid.UUID, amountNGN string) (bool, error) {
	if err := validateNGNAmount(amountNGN); err != nil {
		return false, err
	}
	res := db.Model(&models.PaymentMerchant{}).
		Where(
			"id = ? AND (CAST(available_ngn AS NUMERIC) - CAST(reserved_ngn AS NUMERIC)) >= CAST(? AS NUMERIC)",
			pmID, amountNGN,
		).
		Update("reserved_ngn", gorm.Expr("CAST(reserved_ngn AS NUMERIC) + CAST(? AS NUMERIC)", amountNGN))
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ConsumeNGN atomically deducts amountNGN from both available_ngn and
// reserved_ngn (confirming a completed sell trade payout). The guard ensures
// reserved_ngn >= amountNGN so neither column goes negative.
//
// SQL: UPDATE payment_merchants
//
//	SET available_ngn = CAST(available_ngn AS NUMERIC) - CAST(? AS NUMERIC),
//	    reserved_ngn  = CAST(reserved_ngn  AS NUMERIC) - CAST(? AS NUMERIC)
//	WHERE id = ?
//	  AND CAST(reserved_ngn AS NUMERIC) >= CAST(? AS NUMERIC)
func (s *PMService) ConsumeNGN(ctx context.Context, pmID uuid.UUID, amountNGN string) error {
	if err := validateNGNAmount(amountNGN); err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where(
			"id = ? AND CAST(reserved_ngn AS NUMERIC) >= CAST(? AS NUMERIC)",
			pmID, amountNGN,
		).
		Updates(map[string]any{
			"available_ngn": gorm.Expr("CAST(available_ngn AS NUMERIC) - CAST(? AS NUMERIC)", amountNGN),
			"reserved_ngn":  gorm.Expr("CAST(reserved_ngn AS NUMERIC) - CAST(? AS NUMERIC)", amountNGN),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("pm_ngn_ledger: ConsumeNGN failed — reserved_ngn < amount or PM not found")
	}
	return nil
}

// ReleaseNGN atomically un-reserves amountNGN (cancelling a sell trade
// without paying out). Only reserved_ngn decreases; available_ngn is
// unchanged. The guard ensures reserved_ngn >= amountNGN.
//
// SQL: UPDATE payment_merchants
//
//	SET reserved_ngn = CAST(reserved_ngn AS NUMERIC) - CAST(? AS NUMERIC)
//	WHERE id = ?
//	  AND CAST(reserved_ngn AS NUMERIC) >= CAST(? AS NUMERIC)
func (s *PMService) ReleaseNGN(ctx context.Context, pmID uuid.UUID, amountNGN string) error {
	return s.releaseNGNOn(s.db.WithContext(ctx), pmID, amountNGN)
}

// ReleaseNGNTx is the transaction-aware variant of ReleaseNGN: it runs the same
// guarded UPDATE on the supplied *gorm.DB so a cancel can release the held
// reservation in the SAME transaction as the trade-cancel + capacity restore.
func (s *PMService) ReleaseNGNTx(tx *gorm.DB, pmID uuid.UUID, amountNGN string) error {
	return s.releaseNGNOn(tx, pmID, amountNGN)
}

// releaseNGNOn is the shared guarded UPDATE used by ReleaseNGN (on s.db) and
// ReleaseNGNTx (on a caller-supplied tx).
func (s *PMService) releaseNGNOn(db *gorm.DB, pmID uuid.UUID, amountNGN string) error {
	if err := validateNGNAmount(amountNGN); err != nil {
		return err
	}
	res := db.Model(&models.PaymentMerchant{}).
		Where(
			"id = ? AND CAST(reserved_ngn AS NUMERIC) >= CAST(? AS NUMERIC)",
			pmID, amountNGN,
		).
		Update("reserved_ngn", gorm.Expr("CAST(reserved_ngn AS NUMERIC) - CAST(? AS NUMERIC)", amountNGN))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("pm_ngn_ledger: ReleaseNGN failed — reserved_ngn < amount or PM not found")
	}
	return nil
}

// CreditNGN unconditionally adds amountNGN to available_ngn (recording an
// inbound naira settlement). There is no guard — credits are always valid as
// long as the amount is non-negative.
//
// SQL: UPDATE payment_merchants
//
//	SET available_ngn = CAST(available_ngn AS NUMERIC) + CAST(? AS NUMERIC)
//	WHERE id = ?
func (s *PMService) CreditNGN(ctx context.Context, pmID uuid.UUID, amountNGN string) error {
	if err := validateNGNAmount(amountNGN); err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where("id = ?", pmID).
		Update("available_ngn", gorm.Expr("CAST(available_ngn AS NUMERIC) + CAST(? AS NUMERIC)", amountNGN))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("pm_ngn_ledger: CreditNGN failed — PM not found")
	}
	return nil
}

// WithdrawNGN atomically deducts amountNGN from available_ngn, but only when
// the free (available - reserved) balance is sufficient. Returns ok=false (no
// error) when the free balance would go negative — the caller must surface this
// to the operator as "insufficient free balance".
//
// SQL: UPDATE payment_merchants
//
//	SET available_ngn = CAST(available_ngn AS NUMERIC) - CAST(? AS NUMERIC)
//	WHERE id = ?
//	  AND (CAST(available_ngn AS NUMERIC) - CAST(reserved_ngn AS NUMERIC)) >= CAST(? AS NUMERIC)
func (s *PMService) WithdrawNGN(ctx context.Context, pmID uuid.UUID, amountNGN string) (bool, error) {
	if err := validateNGNAmount(amountNGN); err != nil {
		return false, err
	}
	res := s.db.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where(
			"id = ? AND (CAST(available_ngn AS NUMERIC) - CAST(reserved_ngn AS NUMERIC)) >= CAST(? AS NUMERIC)",
			pmID, amountNGN,
		).
		Update("available_ngn", gorm.Expr("CAST(available_ngn AS NUMERIC) - CAST(? AS NUMERIC)", amountNGN))
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
