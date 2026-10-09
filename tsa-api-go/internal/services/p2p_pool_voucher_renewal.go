package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// poolRenewalNoticeEvery spaces renewal notices; the scheduler ticks at start,
// so a restart must not re-notify (plan B P13).
const poolRenewalNoticeEvery = 20 * time.Hour

// SweepVoucherRenewals (spec §8.4 step 6, S28) walks every live LP stock ad:
//   - one past its voucher deadline, or superseded by a newer signed stock ad
//     of the same LP, chain and token, is closed once it holds no live trade;
//   - the LP's current ad, when due (poolVoucherRenewDue: its OWN maxAmount,
//     M12) and the LP is active (only an active LP can renew), gets one
//     liquidity.voucher_expiring notice per 20 h, recorded first as an
//     ad.renewal_due audit row.
//
// It only notifies and closes: it never builds or signs a voucher, so it
// cannot add to what an LP has committed. A failed read or write is logged,
// the rest of the sweep still runs, and the error is returned so the scheduler
// sees a failing tick.
func (s *P2PTradeService) SweepVoucherRenewals(ctx context.Context) (notified, closed int, err error) {
	if s.pm == nil {
		return 0, 0, nil
	}
	ids, err := s.pm.LPIDs(ctx)
	if err != nil || len(ids) == 0 {
		return 0, 0, err
	}
	var ads []models.P2PAd
	// Newest first, so the first ad seen per (LP, chain, token) is the one
	// LatestStockAd would return and every later one is superseded.
	if err := s.db.WithContext(ctx).
		Where("pm_id IN ? AND side = ? AND status IN ?", ids, models.P2PAdSideSell,
			[]string{models.P2PAdStatusActive, models.P2PAdStatusPaused}).
		Order("created_at DESC").Find(&ads).Error; err != nil {
		return 0, 0, err
	}
	now := s.clock.Now()
	var errs []error
	current := map[string]bool{}
	for i := range ads {
		ad := &ads[i]
		key := fmt.Sprintf("%s|%d|%s", ad.PMID, ad.ChainID, ad.Token)
		superseded := current[key]
		current[key] = true

		expired := ad.VoucherDeadline != nil && !ad.VoucherDeadline.After(now)
		if expired || superseded {
			reason := "superseded"
			if expired {
				reason = "expired"
			}
			// closeStockAd keeps an ad that still holds a live trade.
			ok, err := s.closeStockAd(ctx, ad, reason)
			if err != nil {
				log.Printf("[p2p pool] %s stock ad %s not closed: %v", reason, ad.ID, err)
				errs = append(errs, fmt.Errorf("close stock ad %s: %w", ad.ID, err))
			} else if ok {
				closed++
			}
			continue
		}
		if !poolVoucherRenewDue(ad, now) {
			continue
		}
		sent, err := s.noticeVoucherRenewal(ctx, ad, now)
		if err != nil {
			errs = append(errs, err)
		} else if sent {
			notified++
		}
	}
	return notified, closed, errors.Join(errs...)
}

// noticeVoucherRenewal tells the LP its current stock ad is due, at most once
// per poolRenewalNoticeEvery. The audit row is written before the event, so a
// failed write never notifies and a written one is never repeated early.
func (s *P2PTradeService) noticeVoucherRenewal(ctx context.Context, ad *models.P2PAd, now time.Time) (bool, error) {
	lp, err := s.lookupActiveLP(ctx, ad.SellerID)
	if err != nil {
		return false, err
	}
	if lp == nil || ad.PMID == nil || lp.ID != *ad.PMID {
		return false, nil // a paused or removed LP cannot renew (openableFor)
	}
	var recent int64
	if err := s.db.WithContext(ctx).Model(&models.P2PAuditLog{}).
		Where("ad_id = ? AND action = ? AND created_at > ?", ad.ID, models.P2PAuditAdRenewalDue, now.Add(-poolRenewalNoticeEvery)).
		Count(&recent).Error; err != nil {
		return false, fmt.Errorf("renewal notices of stock ad %s: %w", ad.ID, err)
	}
	if recent > 0 {
		return false, nil
	}
	meta, _ := json.Marshal(map[string]any{"deadline": ad.VoucherDeadline, "remaining": ad.Remaining, "amount": ad.Amount})
	adID := ad.ID
	// Written here with the service clock (audit() stamps wall time), so the
	// 20 h spacing is measured on one clock.
	if err := s.db.WithContext(ctx).Create(&models.P2PAuditLog{ID: uuid.New(), AdID: &adID,
		Action: models.P2PAuditAdRenewalDue, Metadata: meta, CreatedAt: now}).Error; err != nil {
		return false, fmt.Errorf("record renewal notice of stock ad %s: %w", ad.ID, err)
	}
	// Due by date names the date; due because the voucher is nearly used up
	// (remaining below its floor) has no date to give.
	when := "soon"
	if ad.VoucherDeadline != nil && ad.VoucherDeadline.Sub(now) <= poolVoucherRenewWindow {
		when = "by " + ad.VoucherDeadline.UTC().Format("2 January 2006")
	}
	s.emit(events.LiquidityVoucherExpiring, ad.SellerID, uuid.Nil, "Renew your liquidity",
		fmt.Sprintf("Your TSA Connect liquidity needs renewing %s. Open the app and renew so your %s stays in the pool.", when, ad.Token),
		map[string]any{"adId": ad.ID})
	return true, nil
}
