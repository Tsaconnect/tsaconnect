package services

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestPayRefundAggregates_CountConfirmedRefundsOnly(t *testing.T) {
	r := newRefundRig(t)
	a := r.paidDirect(t, "AGG-A", "33", "")
	b := r.paidDirect(t, "AGG-B", "33", "")
	c := r.paidDirect(t, "AGG-C", "33", "")
	r.insertRefund(t, a, models.PayRefundConfirmed, 12_500_000)
	r.insertRefund(t, a, models.PayRefundRequested, 5_000_000)
	r.insertRefund(t, b, models.PayRefundConfirmed, 33_000_000)
	r.insertRefund(t, b, models.PayRefundFailed, 4_000_000)

	aggs, err := r.s.AggregateByPayment(context.Background(), []uuid.UUID{a.ID, b.ID, c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := aggs[a.ID]; got != (PayRefundAgg{RefundedMicroUSD: 12_500_000, CapMicroUSD: 33_000_000, Status: PayRefundStatusPartial}) {
		t.Errorf("a: %+v", got)
	}
	if got := aggs[b.ID]; got != (PayRefundAgg{RefundedMicroUSD: 33_000_000, CapMicroUSD: 33_000_000, Status: PayRefundStatusFull}) {
		t.Errorf("b: %+v", got)
	}
	if _, ok := aggs[c.ID]; ok {
		t.Errorf("c has no refunds: %+v", aggs[c.ID])
	}
	if got, _ := r.s.AggregateByPayment(context.Background(), nil); len(got) != 0 {
		t.Errorf("no ids: %+v", got)
	}
}

func TestPayPaymentViews_CarryRefundTotals(t *testing.T) {
	r := newRefundRig(t)
	a := r.paidDirect(t, "VIEW-A", "33", "")
	b := r.paidDirect(t, "VIEW-B", "33", "")
	r.insertRefund(t, a, models.PayRefundConfirmed, 12_500_000)

	views, err := r.pay.Views(context.Background(), []models.PayPayment{*a, *b})
	if err != nil {
		t.Fatal(err)
	}
	if views[0].RefundedAmount != "12.50" || views[0].RefundStatus != PayRefundStatusPartial {
		t.Errorf("refunded payment: %q %q", views[0].RefundedAmount, views[0].RefundStatus)
	}
	if views[1].RefundedAmount != "0.00" || views[1].RefundStatus != PayRefundStatusNone {
		t.Errorf("unrefunded payment: %q %q", views[1].RefundedAmount, views[1].RefundStatus)
	}
	// View alone reads the row only: never refunded as far as it knows.
	if v := r.pay.View(a); v.RefundedAmount != "0.00" || v.RefundStatus != PayRefundStatusNone {
		t.Errorf("View: %q %q", v.RefundedAmount, v.RefundStatus)
	}
	full := r.pay.ViewWithRefunds(a, PayRefundAgg{RefundedMicroUSD: 33_000_000, CapMicroUSD: 33_000_000, Status: PayRefundStatusFull})
	if full.RefundedAmount != "33.00" || full.RefundStatus != PayRefundStatusFull || full.Status != models.PayStatusSucceeded {
		t.Errorf("ViewWithRefunds: %+v", full)
	}
}

// payment.refunded carries the refund being confirmed (top-level `refund`)
// and the payment's totals already counting it, read in the same transaction.
func TestPayWebhook_RefundedPayloadCarriesTheRefundAndItsTotals(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "HOOK-A", "33", "")
	at := payTestNow
	err := r.db.Transaction(func(tx *gorm.DB) error {
		ref := &models.PayRefund{PaymentID: p.ID, MerchantID: p.MerchantID, Mode: p.Mode, AmountMicroUSD: 12_500_000,
			CapMicroUSD: 33_000_000, ToAddress: refundPayerWallet, Status: models.PayRefundConfirmed, RequestedVia: models.PayRefundViaWeb, ConfirmedAt: &at}
		if err := tx.Create(ref).Error; err != nil {
			return err
		}
		return r.pay.enqueueWebhookWith(tx, p, models.PayEventRefunded, map[string]any{"refund": map[string]any{"id": ref.ID.String(), "amount": "12.50"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	hooks := r.webhooks(t, models.PayEventRefunded)
	if len(hooks) != 1 || hooks[0]["livemode"] != true {
		t.Fatalf("hooks %+v", hooks)
	}
	data := hooks[0]["data"].(map[string]any)
	refund, _ := hooks[0]["refund"].(map[string]any) // top level, beside data
	if data["id"] != p.ID.String() || data["refundedAmount"] != "12.50" || data["refundStatus"] != "partial" || refund["amount"] != "12.50" {
		t.Fatalf("body %+v", hooks[0])
	}
	if _, nested := data["refund"]; nested {
		t.Fatal("refund belongs beside data, not inside it")
	}

	// An owner's resend of the delivered event is rebuilt, and still carries
	// the same top-level refund (review I3).
	var first models.PayWebhookDelivery
	if err := r.db.Where("event = ?", models.PayEventRefunded).First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Model(&first).Update("delivered_at", payTestNow).Error; err != nil {
		t.Fatal(err)
	}
	sender := NewPayWebhookSender(r.db, nil, nil, func() time.Time { return payTestNow })
	sender.SetPayments(r.pay)
	again, err := sender.ResendForMerchant(context.Background(), r.m.ID, p.Mode, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	var resent map[string]any
	if err := json.Unmarshal([]byte(again.Payload), &resent); err != nil {
		t.Fatal(err)
	}
	resentRefund, _ := resent["refund"].(map[string]any)
	if again.ID == first.ID || resentRefund["id"] != refund["id"] || resentRefund["amount"] != "12.50" {
		t.Fatalf("resent body %+v", resent)
	}
}

// With refunds unwired nothing reads pay_refunds; the payload is the view.
func TestPayWebhook_WithoutRefundsWiredThePayloadIsTheView(t *testing.T) {
	r := newRefundRig(t)
	r.pay.SetRefundAggregator(nil)
	p := r.paidDirect(t, "HOOK-B", "33", "")
	if err := r.db.Transaction(func(tx *gorm.DB) error { return r.pay.enqueueWebhook(tx, p, models.PayEventSucceeded) }); err != nil {
		t.Fatal(err)
	}
	hooks := r.webhooks(t, models.PayEventSucceeded)
	data := hooks[len(hooks)-1]["data"].(map[string]any)
	if _, has := hooks[len(hooks)-1]["refund"]; has || data["refundedAmount"] != "0.00" || data["refundStatus"] != "none" {
		t.Fatalf("data %+v", data)
	}
	if r.pay.RefundsWired() {
		t.Fatal("RefundsWired with no aggregator")
	}
}

// The aggregator is what Plan 1's summary and export and Plan 2's receipt read.
func TestPayRefundAggregator_FeedsSummaryExportAndReceipt(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	p := r.paidDirect(t, "RECON-A", "33", "")
	r.insertRefund(t, p, models.PayRefundConfirmed, 12_500_000)
	start, end, err := ParsePayDateRange("2026-09-23", "2026-09-23")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := r.pay.Summary(ctx, r.m.ID, models.PayModeLive, start, end)
	if err != nil || sum.Refunds.Count != 1 || sum.Refunds.AmountMicroUSD != 12_500_000 {
		t.Fatalf("summary refunds %+v err %v", sum.Refunds, err)
	}
	var buf bytes.Buffer
	if err := r.pay.ExportCSV(ctx, &buf, r.m.ID, models.PayModeLive, start, end); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), ",12.50,partial,") {
		t.Fatalf("export:\n%s", buf.String())
	}
	code, err := r.pay.EnsurePayCode(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := r.pay.PublicReceipt(ctx, code)
	if err != nil || receipt.RefundStatus != "partial" || receipt.RefundedAmount != "12.50" {
		t.Fatalf("receipt %+v err %v", receipt, err)
	}
}

func TestPayRefundTotalsConfirmed_CountsByConfirmedAt(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	p := r.paidDirect(t, "TOT-A", "33", "")
	r.insertRefund(t, p, models.PayRefundConfirmed, 10_000_000)
	old := r.insertRefund(t, p, models.PayRefundConfirmed, 2_000_000)
	if err := r.db.Model(old).Update("confirmed_at", payTestNow.AddDate(0, -1, 0)).Error; err != nil {
		t.Fatal(err)
	}
	r.insertRefund(t, p, models.PayRefundRequested, 3_000_000)
	start, end, _ := ParsePayDateRange("2026-09-23", "2026-09-23")
	if got, err := r.s.TotalsConfirmed(ctx, r.m.ID, models.PayModeLive, start, end); err != nil || got != (PaySummaryRefunds{Count: 1, AmountMicroUSD: 10_000_000}) {
		t.Fatalf("day: %+v %v", got, err)
	}
	if got, _ := r.s.TotalsConfirmed(ctx, r.m.ID, models.PayModeLive, time.Time{}, time.Time{}); got != (PaySummaryRefunds{Count: 2, AmountMicroUSD: 12_000_000}) {
		t.Fatalf("all time: %+v", got)
	}
	if got, _ := r.s.TotalsConfirmed(ctx, r.m.ID, models.PayModeTest, time.Time{}, time.Time{}); got != (PaySummaryRefunds{}) {
		t.Fatalf("test mode: %+v", got)
	}
}
