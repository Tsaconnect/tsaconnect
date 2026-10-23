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
	// Company is the central-account transfer of a company-route payout.
	Company *payoutCompanyDetail `json:"company,omitempty"`
}

type payoutCompanyDetail struct {
	Ref        string `json:"ref"`
	TransferID string `json:"transferId,omitempty"`
	Attempts   int    `json:"attempts"`
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
	if p.Venue == models.SellerPayoutVenueCompany {
		d.Company = &payoutCompanyDetail{Ref: p.CompanyRef, TransferID: p.CompanyTransferID, Attempts: p.CompanyAttempts}
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
	updates := map[string]any{"failure_reason": "", "retry_safe": false, "attempt_started_at": a.orch.now()}
	if to == models.SellerPayoutPayingCompany && p.CompanyTransferID != "" && a.orch.company != nil {
		// A transfer exists: it must be provably dead before another is sent.
		tr, terr := a.orch.company.GetTransfer(ctx, p.CompanyTransferID)
		if terr != nil {
			return fmt.Errorf("cannot read transfer %s to prove it failed: %w", p.CompanyTransferID, terr)
		}
		if st := strings.ToUpper(strings.TrimSpace(tr.Status)); st != "FAILED" && st != "CANCELLED" && st != "CANCELED" {
			return ErrPayoutStillLive
		}
	}
	if to == models.SellerPayoutPayingCompany {
		// A company transfer is claimed before it is sent, so a retry starts a new
		// attempt on a NEW reference: the provider would only replay the old answer.
		to = models.SellerPayoutReceived
		updates["company_ref_gen"] = gorm.Expr("company_ref_gen + 1")
		updates["company_transfer_id"] = ""
	}
	// A retry is a fresh start for the retry windows.
	if err := a.orch.move(ctx, p, to, updates, models.SellerPayoutNeedsReview); err != nil {
		return notParkedIfRaced(err)
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
		return notParkedIfRaced(err)
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
		return notParkedIfRaced(err)
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
		return notParkedIfRaced(err)
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

// notParkedIfRaced reports a lost race (another action moved the payout first)
// as "not parked", which the handler answers with 409 instead of a 500.
func notParkedIfRaced(err error) error {
	if errors.Is(err, errSellerPayoutTransition) {
		return ErrPayoutNotParked
	}
	return err
}

// CompanyOwed is what the company route leaves for an operator to rebalance: the
// USDC (or USDT) the treasury holds for company payouts that are PAID and not yet
// marked rebalanced, summed per token in base units, with the payouts behind it.
type CompanyOwed struct {
	Totals  map[string]string          `json:"totals"`
	Payouts []models.SellerPayoutOrder `json:"payouts"`
}

func (a *SellerPayoutAdmin) CompanyOwed(ctx context.Context) (*CompanyOwed, error) {
	var rows []models.SellerPayoutOrder
	// Only payouts the central account actually sent (a transfer id is recorded):
	// one marked paid by hand left the wallet untouched and owes it nothing.
	if err := a.db.WithContext(ctx).
		Where("venue = ? AND status = ? AND rebalanced_at IS NULL AND COALESCE(company_transfer_id, '') <> ''",
			models.SellerPayoutVenueCompany, models.SellerPayoutPaid).
		Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	sums := map[string]*big.Int{}
	for _, r := range rows {
		n, ok := new(big.Int).SetString(r.ReceivedAmount, 10)
		if !ok {
			continue
		}
		t := strings.ToUpper(r.Token)
		if sums[t] == nil {
			sums[t] = new(big.Int)
		}
		sums[t].Add(sums[t], n)
	}
	out := &CompanyOwed{Totals: map[string]string{}, Payouts: rows}
	for t, n := range sums {
		out.Totals[t] = n.String()
	}
	return out, nil
}

// MarkRebalanced records that an operator moved the USDC of these paid company
// payouts back (a batch). Only paid, not-yet-rebalanced company payouts count;
// anything else in ids is refused so a wrong id never hides money still owed.
func (a *SellerPayoutAdmin) MarkRebalanced(ctx context.Context, ids []uuid.UUID, admin uuid.UUID, note, reference string) (int64, error) {
	if strings.TrimSpace(note) == "" || strings.TrimSpace(reference) == "" {
		return 0, ErrPayoutNeedsNote
	}
	seen := map[uuid.UUID]bool{}
	uniq := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return 0, ErrPayoutNotFound
	}
	now := a.orch.now()
	var affected int64
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.SellerPayoutOrder{}).
			Where("id IN ? AND venue = ? AND status = ? AND rebalanced_at IS NULL AND COALESCE(company_transfer_id, '') <> ''",
				uniq, models.SellerPayoutVenueCompany, models.SellerPayoutPaid).
			UpdateColumn("rebalanced_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != int64(len(uniq)) {
			return ErrPayoutNotParked // some were not eligible: nothing changes
		}
		affected = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	raw, _ := json.Marshal(map[string]any{"ids": uniq, "note": note, "reference": reference, "count": affected})
	row := models.P2PAuditLog{ID: uuid.New(), ActorID: &admin, Action: "seller_payout.rebalanced", Metadata: datatypes.JSON(raw)}
	if err := a.db.WithContext(ctx).Create(&row).Error; err != nil {
		fmt.Printf("[seller-payout] audit seller_payout.rebalanced failed: %v\n", err)
	}
	return affected, nil
}
