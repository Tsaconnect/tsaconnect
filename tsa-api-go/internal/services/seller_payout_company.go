package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The company route. The escrow's release lands in the treasury as USDC; the
// naira is paid to the seller from the central company account (the Flutterwave
// wallet IT sells already pay out of) with ONE transfer, and the USDC stays in
// the treasury until an operator rebalances it in a batch. There is no bridge
// and no Paycrest sale on this route, so a small order costs no fixed bridge fee.
//
// The money rules are the IT sell payout's: the attempt is claimed BEFORE the
// request leaves (a crash mid-request still reads as "may have been sent"), a
// refusal is final only for a reference's first attempt, and an outcome nobody
// can see is never sent again — the payout parks and a person reads Flutterwave.

// SellerPayoutRefPrefix namespaces the transfer reference of a seller payout so
// webhook dispatch routes it here and not to a PM trade (bare UUID) or an IT sell.
const SellerPayoutRefPrefix = "SPO-"

const (
	// sellerPayoutCompanyUncertainAfter is how long a claimed attempt may have no
	// recorded transfer id before it is treated as "may have been sent".
	sellerPayoutCompanyUncertainAfter = 2 * time.Minute
	// sellerPayoutCompanyPendingAlertAfter is when a transfer the provider still
	// calls pending is worth an operator's look.
	sellerPayoutCompanyPendingAlertAfter = 30 * time.Minute
)

// SellerPayoutCompanyPayer is what the company route needs of the central
// account: send a transfer, read it back, read the NGN balance.
type SellerPayoutCompanyPayer interface {
	InitiatePayout(ctx context.Context, req PayoutRequest) (string, error)
	GetTransfer(ctx context.Context, id string) (FWTransfer, error)
	FetchBalances(ctx context.Context) ([]FWBalance, error)
}

// FlutterwaveCompanyPayer is the live payer: the same gateway IT sells pay
// through, and the client that reads transfers and balances back.
type FlutterwaveCompanyPayer struct {
	Gateway NombaGateway
	Client  *FlutterwaveClient
}

func (f FlutterwaveCompanyPayer) InitiatePayout(ctx context.Context, req PayoutRequest) (string, error) {
	return f.Gateway.InitiatePayout(ctx, req)
}
func (f FlutterwaveCompanyPayer) GetTransfer(ctx context.Context, id string) (FWTransfer, error) {
	return f.Client.GetTransfer(ctx, id)
}
func (f FlutterwaveCompanyPayer) FetchBalances(ctx context.Context) ([]FWBalance, error) {
	return f.Client.FetchBalances(ctx)
}

// sellerPayoutRef is the provider transfer reference of attempt generation gen:
// "SPO-" + 32 hex, plus "-<gen>" after a checked failure ("SPO-" + 32 hex + "-N"
// stays inside Flutterwave's 42 character limit for any generation below 100).
func sellerPayoutRef(id uuid.UUID, gen int) string {
	ref := SellerPayoutRefPrefix + strings.ReplaceAll(id.String(), "-", "")
	if gen > 0 {
		ref += "-" + strconv.Itoa(gen)
	}
	return ref
}

// ngnAvailable is the central account's NGN balance, and false when none is listed.
func ngnAvailable(bals []FWBalance) (float64, bool) {
	for _, b := range bals {
		if strings.EqualFold(strings.TrimSpace(b.Currency), "NGN") {
			return b.AvailableBalance, true
		}
	}
	return 0, false
}

// WithCompany gives the machine the central account for company-route payouts.
// feeKobo is the provider's transfer fee the float must also cover.
func (o *SellerPayoutOrchestrator) WithCompany(c SellerPayoutCompanyPayer, feeKobo int64) *SellerPayoutOrchestrator {
	o.company, o.companyFeeKobo = c, feeKobo
	return o
}

// WithCompanyMax is the largest payout the company route takes (0 = no cap): an
// operator's re-route obeys the same limit the planner does.
func (o *SellerPayoutOrchestrator) WithCompanyMax(maxKobo int64) *SellerPayoutOrchestrator {
	o.companyMaxKobo = maxKobo
	return o
}

// companyNothingSent is true only when no company transfer can exist for the
// payout: none was ever attempted, or the ONE attempt made was refused on its first
// reference (a refusal proves nothing exists only for a reference's first attempt;
// a later reference's refusal says nothing about an earlier one that may have landed).
func companyNothingSent(p *models.SellerPayoutOrder) bool {
	return p.CompanyAttempts == 0 ||
		(p.CompanyAttempts == 1 && p.ParkedFrom == models.SellerPayoutPayingCompany && p.RetrySafe)
}

// advanceCompany takes one step for a company-route payout.
func (o *SellerPayoutOrchestrator) advanceCompany(ctx context.Context, p *models.SellerPayoutOrder) error {
	if p.Status == models.SellerPayoutPayingCompany {
		return o.observeCompany(ctx, p)
	}
	return o.startCompany(ctx, p)
}

// startCompany checks the float, claims the attempt and sends the transfer.
func (o *SellerPayoutOrchestrator) startCompany(ctx context.Context, p *models.SellerPayoutOrder) error {
	if !o.sendingAllowed(ctx) {
		o.restartWindow(ctx, p)
		return nil // paused: the payout waits where it is
	}
	if o.company == nil {
		return o.parkSafe(ctx, p, "the central account is not configured for payouts")
	}
	naira, err := koboToNairaString(strconv.FormatInt(p.FiatTargetKobo, 10))
	if err != nil || p.FiatTargetKobo <= 0 {
		return o.parkSafe(ctx, p, fmt.Sprintf("unusable payout amount %d kobo", p.FiatTargetKobo))
	}
	bals, err := o.company.FetchBalances(ctx)
	if err != nil {
		return fmt.Errorf("central account balance: %w", err) // unknown is not short: tried again next tick
	}
	need := float64(p.FiatTargetKobo+o.companyFeeKobo) / 100
	if avail, ok := ngnAvailable(bals); !ok || avail < need {
		return o.parkSafe(ctx, p, fmt.Sprintf("the central account holds ₦%.2f and this payout needs ₦%.2f: top it up, then retry", avail, need))
	}

	ref := sellerPayoutRef(p.ID, p.CompanyRefGen)
	// Claimed before the request leaves; of two ticks only one wins.
	if err := o.move(ctx, p, models.SellerPayoutPayingCompany, map[string]any{
		"company_ref": ref, "company_attempts": gorm.Expr("company_attempts + 1"), "company_transfer_id": "",
	}, models.SellerPayoutReceived); err != nil {
		if errors.Is(err, errSellerPayoutTransition) {
			return nil
		}
		return err
	}
	p.CompanyRef, p.CompanyAttempts, p.CompanyTransferID = ref, p.CompanyAttempts+1, ""

	id, err := o.company.InitiatePayout(ctx, PayoutRequest{
		BankCode: p.InstitutionCode, AccountNumber: p.AccountNumber, AccountName: p.AccountName,
		Amount: naira, AmountNGN: naira, SourceCurrency: "NGN", Reference: ref,
	})
	if err != nil {
		var rej *ProviderRejectedError
		if errors.As(err, &rej) {
			// A refusal on a reference's first attempt proves no transfer exists.
			return o.parkSafe(ctx, p, "the provider refused the transfer: "+err.Error())
		}
		return o.park(ctx, p, fmt.Sprintf("the transfer may have been sent (%v): look up reference %s on Flutterwave before retrying", err, ref))
	}
	res := o.db.WithContext(ctx).Model(&models.SellerPayoutOrder{}).
		Where("id = ? AND status = ?", p.ID, models.SellerPayoutPayingCompany).
		Update("company_transfer_id", id)
	if res.Error != nil {
		log.Printf("[seller-payout] ALERT seller_payout_company_id_unrecorded=1 payout=%s ref=%s transfer=%s: %v", p.ID, ref, id, res.Error)
		return res.Error
	}
	if res.RowsAffected == 0 {
		// The row moved between the claim and this write (a reversal, an admin
		// action): the transfer exists but its id is on no row.
		log.Printf("[seller-payout] ALERT seller_payout_company_id_unrecorded=1 payout=%s ref=%s transfer=%s: the payout moved before the id was written", p.ID, ref, id)
	}
	p.CompanyTransferID = id
	return nil
}

// observeCompany follows the transfer to a final state read from the provider's
// own API, never from a webhook alone.
func (o *SellerPayoutOrchestrator) observeCompany(ctx context.Context, p *models.SellerPayoutOrder) error {
	if o.company == nil {
		return nil
	}
	if p.CompanyTransferID == "" {
		// Claimed, but no transfer id was ever recorded: the request may have
		// reached the provider. Never sent again; a person reads Flutterwave.
		if o.now().Sub(p.UpdatedAt) > sellerPayoutCompanyUncertainAfter {
			return o.park(ctx, p, fmt.Sprintf("the transfer was claimed but its outcome was never recorded: look up reference %s on Flutterwave", p.CompanyRef))
		}
		return nil
	}
	tr, err := o.company.GetTransfer(ctx, p.CompanyTransferID)
	if err != nil {
		return fmt.Errorf("read transfer %s: %w", p.CompanyTransferID, err)
	}
	if tr.Reference != "" && tr.Reference != p.CompanyRef {
		return o.park(ctx, p, fmt.Sprintf("transfer %s carries reference %q, not %q", p.CompanyTransferID, tr.Reference, p.CompanyRef))
	}
	switch strings.ToUpper(strings.TrimSpace(tr.Status)) {
	case "SUCCESSFUL", "SUCCESS", "SUCCEEDED":
		if err := o.move(ctx, p, models.SellerPayoutPaid, map[string]any{"company_paid_at": o.now().UTC()}, models.SellerPayoutPayingCompany); err != nil {
			if errors.Is(err, errSellerPayoutTransition) {
				return nil
			}
			return err
		}
		o.paidNotice(p)
		return nil
	case "FAILED", "CANCELLED", "CANCELED":
		return o.park(ctx, p, fmt.Sprintf("the provider reports transfer %s %s (%s): confirm no money left, then retry", p.CompanyTransferID, strings.ToLower(tr.Status), tr.ProviderResponse.Message))
	}
	switch strings.ToUpper(strings.TrimSpace(tr.Status)) {
	case "", "NEW", "PENDING", "PROCESSING", "QUEUED", "INITIATED":
		// still in flight
	default:
		// REVERSED, or a status this code has never seen: money may have moved
		// either way, so a person reads it.
		return o.park(ctx, p, fmt.Sprintf("the provider reports transfer %s as %q: read it on Flutterwave before anything else", p.CompanyTransferID, tr.Status))
	}
	if o.now().Sub(p.UpdatedAt) > sellerPayoutCompanyPendingAlertAfter {
		alertf("seller-payout-company-pending:"+p.ID.String(),
			"[seller-payout] ALERT seller_payout_company_pending=1 payout=%s ref=%s transfer=%s still %s", p.ID, p.CompanyRef, p.CompanyTransferID, tr.Status)
	}
	return nil
}

// paidNotice tells the seller the naira was sent.
func (o *SellerPayoutOrchestrator) paidNotice(p *models.SellerPayoutOrder) {
	o.tell(p, events.SellerPayoutPaid, "Your payout was sent",
		fmt.Sprintf("₦%s for order #%s was sent to your %s account ending %s.",
			payFiatAmount(p.FiatTargetKobo), p.MarketplaceOrderID.String()[:8], p.InstitutionName, lastDigits(p.AccountNumber, 4)))
}

// NudgeCompanyPayout is the transfer.disburse webhook's whole job here: it makes
// the machine look at the payout on its next tick. The webhook never decides the
// outcome; the provider's own transfer read does.
func NudgeCompanyPayout(ctx context.Context, db *gorm.DB, merchantTxRef string) error {
	res := db.WithContext(ctx).Model(&models.SellerPayoutOrder{}).
		Where("company_ref = ?", merchantTxRef).UpdateColumn("checked_at", nil)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		log.Printf("[seller-payout] webhook for unknown transfer reference %q", merchantTxRef)
	}
	return nil
}

// FlagCompanyReversal handles a transfer.reversal webhook for a seller payout:
// the bank sent the naira back, so a payout that reads paid (or is still being
// watched) is parked for a person and the seller is not left believing it was paid.
func FlagCompanyReversal(ctx context.Context, db *gorm.DB, merchantTxRef string) error {
	res := db.WithContext(ctx).Model(&models.SellerPayoutOrder{}).
		Where("company_ref = ? AND status IN ?", merchantTxRef, []string{models.SellerPayoutPaid, models.SellerPayoutPayingCompany}).
		Updates(map[string]any{
			"status": models.SellerPayoutNeedsReview, "parked_from": models.SellerPayoutPayingCompany, "retry_safe": false,
			"company_paid_at": nil,
			"failure_reason":  truncateReason("the provider reports transfer " + merchantTxRef + " reversed: the naira came back; confirm, then retry or pay by hand"),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[seller-payout] ALERT seller_payout_company_reversed=1 ref=%s — parked for review", merchantTxRef)
	}
	return nil
}
