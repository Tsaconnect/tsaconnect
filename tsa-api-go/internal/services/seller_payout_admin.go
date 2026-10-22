package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var (
	// ErrPayoutNotParked: the action is for a payout waiting in needs_review.
	ErrPayoutNotParked = errors.New("this payout is not waiting for review")
	// ErrPayoutRetryUnsafe: the payout stopped after money may have moved; a
	// retry is not offered unless the operator forces it and says why.
	ErrPayoutRetryUnsafe = errors.New("this payout stopped after money may have moved; check the chain and Paycrest, then retry with force")
	// ErrPayoutNeedsNote: a person acting on a payout says why.
	ErrPayoutNeedsNote = errors.New("a note is required")
	// ErrPayoutNotFound: no such payout.
	ErrPayoutNotFound = errors.New("payout not found")
	// ErrPayoutHoldsFunds: the treasury holds this payout's proven funds; closing
	// it would hide them. Pay the seller's wallet, or mark it paid once it is.
	ErrPayoutHoldsFunds = errors.New("the treasury holds this payout's funds: pay the seller's wallet instead of cancelling it")
	// ErrPayoutStillLive: a sale or a transfer for this payout is still alive;
	// marking it paid by hand could pay the seller twice.
	ErrPayoutStillLive = errors.New("a Paycrest order or a transfer for this payout is still alive: wait for it to end, or force it knowing the seller was paid")
)

// SellerPayoutAdmin is the operators' way through payouts the machine parked.
// It never moves money on its own authority: a retry puts the payout back where
// it stopped (the machine re-derives what to do from the legs and the chain), a
// mark-paid records an out-of-band payment, a cancel closes a payout nothing
// was sent for, and a wallet payment goes through the machine's own legs.
// Every action is audited with who and why.
type SellerPayoutAdmin struct {
	db   *gorm.DB
	orch *SellerPayoutOrchestrator
}

func NewSellerPayoutAdmin(db *gorm.DB, orch *SellerPayoutOrchestrator) *SellerPayoutAdmin {
	return &SellerPayoutAdmin{db: db, orch: orch}
}

// PayoutDetail is one payout with everything an operator needs to decide.
type PayoutDetail struct {
	Payout *models.SellerPayoutOrder   `json:"payout"`
	Legs   []models.RampLeg            `json:"legs"`
	Sale   *models.PaycrestOrderRecord `json:"paycrestOrder,omitempty"`
	Order  *payoutOrderSummary         `json:"order,omitempty"`
}

type payoutOrderSummary struct {
	ID            uuid.UUID `json:"id"`
	Status        string    `json:"status"`
	ReleaseTxHash string    `json:"releaseTxHash,omitempty"`
}

// List returns payouts, newest first. No statuses means everything not yet
// finished (the queue an operator watches).
func (a *SellerPayoutAdmin) List(ctx context.Context, statuses []string, limit int) ([]models.SellerPayoutOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	// Parked payouts first: a long queue of waiting ones must never hide them.
	q := a.db.WithContext(ctx).
		Order("CASE WHEN status = 'needs_review' THEN 0 ELSE 1 END").
		Order("created_at DESC").Limit(limit)
	if len(statuses) == 0 {
		q = q.Where("status NOT IN ?", []string{models.SellerPayoutPaid, models.SellerPayoutPaidWallet, models.SellerPayoutCancelled})
	} else {
		q = q.Where("status IN ?", statuses)
	}
	var rows []models.SellerPayoutOrder
	return rows, q.Find(&rows).Error
}

func (a *SellerPayoutAdmin) load(ctx context.Context, id uuid.UUID) (*models.SellerPayoutOrder, error) {
	var p models.SellerPayoutOrder
	err := a.db.WithContext(ctx).First(&p, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPayoutNotFound
	}
	return &p, err
}

// Detail is the payout, its legs, its Paycrest order and its marketplace order.
func (a *SellerPayoutAdmin) Detail(ctx context.Context, id uuid.UUID) (*PayoutDetail, error) {
	p, err := a.load(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &PayoutDetail{Payout: p}
	if d.Legs, err = a.orch.legs.ForOrder(ctx, p.ID); err != nil {
		return nil, err
	}
	if p.PaycrestOrderID != nil {
		var sale models.PaycrestOrderRecord
		if err := a.db.WithContext(ctx).First(&sale, "id = ?", *p.PaycrestOrderID).Error; err == nil {
			d.Sale = &sale
		}
	}
	var o struct {
		ID            uuid.UUID
		Status        string
		ReleaseTxHash string
	}
	if err := a.db.WithContext(ctx).Table("orders").Select("id", "status", "release_tx_hash").
		Where("id = ?", p.MarketplaceOrderID).Limit(1).Scan(&o).Error; err == nil && o.ID != uuid.Nil {
		d.Order = &payoutOrderSummary{ID: o.ID, Status: o.Status, ReleaseTxHash: o.ReleaseTxHash}
	}
	return d, nil
}

// Retry puts a parked payout back in the status it stopped in. The machine then
// re-derives its next step from the legs and the chain, so a retry can only do
// what the machine would have done: a payout that stopped after money may have
// moved needs force, and a note saying what was checked.
func (a *SellerPayoutAdmin) Retry(ctx context.Context, id, admin uuid.UUID, note string, force bool) error {
	if strings.TrimSpace(note) == "" {
		return ErrPayoutNeedsNote
	}
	p, err := a.load(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != models.SellerPayoutNeedsReview || p.ParkedFrom == "" {
		return ErrPayoutNotParked
	}
	if !p.RetrySafe && !force {
		return ErrPayoutRetryUnsafe
	}
	to := p.ParkedFrom
	if to == models.SellerPayoutSelling {
		// The machine only RECOVERS a payout left in selling; it never opens the
		// sale from there. With a live Paycrest order it is adopted; with none,
		// the payout goes back to where the sale is opened.
		var live int64
		if err := a.db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
			Where("seller_payout_id = ? AND status NOT IN ?", p.ID,
				[]string{models.PaycrestStatusCancelled, models.PaycrestStatusFailed}).Count(&live).Error; err != nil {
			return err
		}
		if live == 0 {
			to = models.SellerPayoutBridged
		}
	}
	// A retry is a fresh start for the retry windows.
	if err := a.orch.move(ctx, p, to, map[string]any{"failure_reason": "", "retry_safe": false, "attempt_started_at": a.orch.now()}, models.SellerPayoutNeedsReview); err != nil {
		return err
	}
	a.audit(ctx, "seller_payout.retry", admin, p, map[string]any{"note": note, "force": force, "to": to})
	return nil
}

// MarkPaid records that the seller was paid outside the machine (a bank
// transfer made by hand, a Paycrest order settled another way). The reference
// is how someone later finds the payment.
func (a *SellerPayoutAdmin) MarkPaid(ctx context.Context, id, admin uuid.UUID, note, reference string, force bool) error {
	if strings.TrimSpace(note) == "" || strings.TrimSpace(reference) == "" {
		return ErrPayoutNeedsNote
	}
	p, err := a.load(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != models.SellerPayoutNeedsReview {
		return ErrPayoutNotParked
	}
	if !force {
		live, err := a.hasLiveVenue(ctx, p)
		if err != nil {
			return err
		}
		if live {
			return ErrPayoutStillLive
		}
	}
	if err := a.orch.move(ctx, p, models.SellerPayoutPaid, map[string]any{"failure_reason": truncateReason("paid by hand: " + reference)}, models.SellerPayoutNeedsReview); err != nil {
		return err
	}
	a.audit(ctx, "seller_payout.marked_paid", admin, p, map[string]any{"note": note, "reference": reference, "force": force})
	return nil
}

// hasLiveVenue is true while a Paycrest sale of this payout has not ended or a
// leg is still pending: paying the seller by hand then risks paying twice.
func (a *SellerPayoutAdmin) hasLiveVenue(ctx context.Context, p *models.SellerPayoutOrder) (bool, error) {
	if p.PaycrestOrderID != nil {
		var sale models.PaycrestOrderRecord
		if err := a.db.WithContext(ctx).Select("id", "status").First(&sale, "id = ?", *p.PaycrestOrderID).Error; err != nil {
			return false, err
		}
		if !models.PaycrestOrderTerminal(sale.Status) {
			return true, nil
		}
	}
	var pending int64
	err := a.db.WithContext(ctx).Model(&models.RampLeg{}).
		Where("order_id = ? AND status = ?", p.ID, models.RampLegStatusPending).Count(&pending).Error
	return pending > 0, err
}

// Cancel closes a payout nothing was sent for: no bridge, no sale, no deposit
// ever left the treasury. What the treasury holds for it is then the operator's
// to return by hand (or pay the wallet instead, before cancelling).
func (a *SellerPayoutAdmin) Cancel(ctx context.Context, id, admin uuid.UUID, note string) error {
	if strings.TrimSpace(note) == "" {
		return ErrPayoutNeedsNote
	}
	p, err := a.load(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != models.SellerPayoutNeedsReview {
		return ErrPayoutNotParked
	}
	if n, ok := new(big.Int).SetString(p.ReceivedAmount, 10); ok && n.Sign() > 0 {
		return ErrPayoutHoldsFunds
	}
	legs, err := a.orch.legs.ForOrder(ctx, p.ID)
	if err != nil {
		return err
	}
	for i := range legs {
		if !(legs[i].Status == models.RampLegStatusFailed && legs[i].TxHash == "") || p.PaycrestOrderID != nil {
			return ErrWalletPaymentNotSafe
		}
	}
	if err := a.orch.move(ctx, p, models.SellerPayoutCancelled, map[string]any{"failure_reason": truncateReason("cancelled: " + note)}, models.SellerPayoutNeedsReview); err != nil {
		return err
	}
	a.audit(ctx, "seller_payout.cancelled", admin, p, map[string]any{"note": note})
	return nil
}

// PayWallet pays the seller's wallet instead (see StartWalletPayment).
func (a *SellerPayoutAdmin) PayWallet(ctx context.Context, id, admin uuid.UUID, note string) error {
	if strings.TrimSpace(note) == "" {
		return ErrPayoutNeedsNote
	}
	p, err := a.load(ctx, id)
	if err != nil {
		return err
	}
	if err := a.orch.StartWalletPayment(ctx, p); err != nil {
		return err
	}
	a.audit(ctx, "seller_payout.pay_wallet", admin, p, map[string]any{"note": note, "wallet": p.SellerWallet})
	return nil
}

// audit writes the action to the same log the signer and liquidity admin use.
func (a *SellerPayoutAdmin) audit(ctx context.Context, action string, admin uuid.UUID, p *models.SellerPayoutOrder, meta map[string]any) {
	meta["payoutId"] = p.ID.String()
	meta["orderId"] = p.MarketplaceOrderID.String()
	meta["from"] = p.ParkedFrom
	raw, _ := json.Marshal(meta)
	row := models.P2PAuditLog{ID: uuid.New(), ActorID: &admin, Action: action, Metadata: datatypes.JSON(raw)}
	if err := a.db.WithContext(ctx).Create(&row).Error; err != nil {
		fmt.Printf("[seller-payout] audit %s for payout %s failed: %v\n", action, p.ID, err)
	}
}
