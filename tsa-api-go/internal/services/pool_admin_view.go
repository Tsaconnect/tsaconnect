package services

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// PoolOrderDetail is the console's view of one pool order (§15, P20).
type PoolOrderDetail struct {
	Intent     *models.ITIntent            `json:"intent"`
	Snapshots  []models.PoolOrder          `json:"snapshots"`
	Slices     []PoolSliceView             `json:"slices"`
	SaleLeg    *models.RampLeg             `json:"saleLeg,omitempty"`
	Conversion *models.PaycrestOrderRecord `json:"conversion,omitempty"`
	Actions    []string                    `json:"actions"`
}

// PoolSliceView is one LP slice as the console shows it: the LP by name only
// (never its settlement bank details), and the forward's state.
type PoolSliceView struct {
	ID          uuid.UUID        `json:"id"`
	LPID        uuid.UUID        `json:"lpId"`
	LPName      string           `json:"lpName"`
	Generation  int              `json:"generation"`
	Units       string           `json:"units"` // decimal token string
	Naira       string           `json:"naira"`
	Status      string           `json:"status"`
	Dropped     bool             `json:"dropped"`
	OpenTxHash  string           `json:"openTxHash,omitempty"`
	ReleaseTx   string           `json:"releaseTxHash,omitempty"`
	Forward     *PoolForwardView `json:"forward,omitempty"`
	SliceAction []string         `json:"actions"` // "pay-lp" | "returned" (F22 only)
}

// PoolForwardView is the LP's naira forward for a slice.
type PoolForwardView struct {
	ID            uuid.UUID `json:"id"`
	Status        string    `json:"status"`
	Amount        string    `json:"amount"`
	PayoutID      string    `json:"payoutId,omitempty"`
	FailureReason string    `json:"failureReason,omitempty"`
	FeeKobo       *string   `json:"feeKobo,omitempty"`
	// StartedAt on a pending forward: outcome unknown, never pay by hand.
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// PoolOrderDetail reads one pool order for the console. Admin only: LP names
// and payout ids are shown; the buyer's view is unchanged.
func (s *ITService) PoolOrderDetail(ctx context.Context, intentID uuid.UUID) (*PoolOrderDetail, error) {
	d := s.poolDesk
	if d == nil {
		return nil, ErrPoolSettleUnwired
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if !isPoolBuy(intent) {
		return nil, ErrStableIntentNotFound
	}
	out := &PoolOrderDetail{Intent: intent, Slices: []PoolSliceView{}, Actions: s.poolOrderActions(ctx, intent)}
	db := s.db.WithContext(ctx)
	if err := db.Where("intent_id = ?", intentID).Order("generation").Find(&out.Snapshots).Error; err != nil {
		return nil, err
	}
	var slices []models.P2PTrade
	if err := db.Where("it_intent_id = ?", intentID).Order("generation, created_at").Find(&slices).Error; err != nil {
		return nil, err
	}
	for _, sl := range slices {
		v := PoolSliceView{ID: sl.ID, Generation: sl.Generation, Units: FormatStableUnits(d.toCanonical(sl.TokenAmount).String()),
			Naira: sl.FiatAmount, Status: sl.Status, Dropped: sl.DroppedAt != nil, OpenTxHash: sl.OpenTxHash, ReleaseTx: sl.ReleaseTxHash,
			SliceAction: []string{}}
		var ad models.P2PAd
		if db.Select("id", "pm_id").First(&ad, "id = ?", sl.AdID).Error == nil && ad.PMID != nil {
			v.LPID = *ad.PMID
			if lp, err := d.c.PM.GetByID(ctx, *ad.PMID); err == nil {
				v.LPName = lp.Name
			}
		}
		var fwd models.P2PSettlementForward
		err := db.First(&fwd, "trade_id = ?", sl.ID).Error
		switch {
		case err == nil:
			v.Forward = &PoolForwardView{ID: fwd.ID, Status: fwd.Status, Amount: fwd.Amount, PayoutID: fwd.PayoutID,
				FailureReason: fwd.FailureReason, FeeKobo: fwd.FeeKobo, StartedAt: fwd.StartedAt}
		case errors.Is(err, gorm.ErrRecordNotFound) && sl.Status == models.P2PTradeStatusReleased &&
			!poolOrderHoldsMoney(intent.Status) && sl.CancelReason == "":
			v.SliceAction = []string{"pay-lp", "returned"} // F22 on an unpaid or terminal order, neither exit taken (review I-5)
		}
		out.Slices = append(out.Slices, v)
	}
	if leg, err := s.liveSaleLeg(ctx, intentID); err == nil {
		out.SaleLeg = leg
	}
	if rec, err := conversionRecordOn(ctx, s.db, intentID); err == nil {
		out.Conversion = rec
	}
	return out, nil
}

// ResendPoolBuy is the company wallet's evidence-gated re-send, on the pool's
// sale desk (S20): the same dropped-transaction check decides.
func (s *ITService) ResendPoolBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	sd := s.poolSale()
	if sd == nil {
		return nil, ErrPoolSettleUnwired
	}
	return s.resendSale(ctx, sd, adminID, intentID, note)
}
