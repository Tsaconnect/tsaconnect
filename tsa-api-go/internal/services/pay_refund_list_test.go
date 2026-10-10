package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestPayRefundList_OpenFirstThenNewestWithinTheRange(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	a := r.paidDirect(t, "LIST-A", "34", "")
	b := r.paidDirect(t, "LIST-B", "34", "")
	if _, err := r.pay.EnsurePayCode(ctx, a); err != nil {
		t.Fatal(err)
	}
	done := r.insertRefund(t, a, models.PayRefundConfirmed, 5_000_000)
	open := r.insertRefund(t, b, models.PayRefundRequested, 1_000_000)
	old := r.insertRefund(t, a, models.PayRefundCancelled, 1_000_000)
	r.db.Model(old).Update("created_at", payTestNow.AddDate(0, -1, 0))
	r.db.Model(done).Update("created_at", payTestNow.Add(time.Minute)) // newer than the open one, still after it

	start, end, _ := ParsePayDateRange("2026-09-23", "2026-09-23")
	items, total, err := r.s.List(ctx, PayRefundFilter{MerchantID: r.m.ID, Mode: models.PayModeLive, From: start, To: end, Page: 1, Limit: 20})
	if err != nil || total != 2 || len(items) != 2 || items[0].ID != open.ID || items[1].ID != done.ID {
		t.Fatalf("items %+v total %d err %v", items, total, err)
	}
	if !strings.HasPrefix(items[1].PaymentCode, "TSA-") || items[1].Description != "LOS-ABV 12 Oct" || items[0].Amount != "1.00" {
		t.Fatalf("item fields %+v", items[1])
	}
	confirmed, total, err := r.s.List(ctx, PayRefundFilter{MerchantID: r.m.ID, Mode: models.PayModeLive, Status: models.PayRefundConfirmed})
	if err != nil || total != 1 || confirmed[0].ID != done.ID {
		t.Fatalf("status filter %+v", confirmed)
	}
	if _, total, err := r.s.List(ctx, PayRefundFilter{MerchantID: uuid.New(), Mode: models.PayModeLive}); err != nil || total != 0 {
		t.Fatalf("another merchant's refunds were listed: %d %v", total, err)
	}
}

// Rows that tie on status class and created_at keep one order, so a page
// boundary never repeats or drops a refund; a mode's list holds only its own.
func TestPayRefundList_PagesAreStableAndStayInTheirMode(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	p := r.paidDirect(t, "PAGE-1", "34", "")
	want := map[uuid.UUID]bool{}
	for range 5 {
		want[r.insertRefund(t, p, models.PayRefundCancelled, 1_000_000).ID] = true
	}
	testPayment := r.paid(t, models.PayModeTest, "PAGE-2", nil)
	r.insertRefund(t, testPayment, models.PayRefundCancelled, 1_000_000)

	var seen []uuid.UUID
	for page := 1; page <= 3; page++ {
		items, total, err := r.s.List(ctx, PayRefundFilter{MerchantID: r.m.ID, Mode: models.PayModeLive, Page: page, Limit: 2})
		if err != nil || total != 5 {
			t.Fatalf("page %d: total %d err %v", page, total, err)
		}
		for _, it := range items {
			seen = append(seen, it.ID)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("pages held %d refunds, want 5: %v", len(seen), seen)
	}
	for _, id := range seen {
		if !want[id] {
			t.Fatalf("refund %s listed twice or from another mode", id)
		}
		delete(want, id)
	}
	if test, total, err := r.s.List(ctx, PayRefundFilter{MerchantID: r.m.ID, Mode: models.PayModeTest}); err != nil || total != 1 || test[0].Mode != models.PayModeTest {
		t.Fatalf("test mode %+v %d %v", test, total, err)
	}
}

// A page asks for at most 100 rows however much is requested.
func TestPayRefundList_LimitIsCappedAt100(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "CAP100", "34", "")
	for range 101 {
		r.insertRefund(t, p, models.PayRefundCancelled, 1)
	}
	items, total, err := r.s.List(context.Background(), PayRefundFilter{MerchantID: r.m.ID, Mode: models.PayModeLive, Page: 1, Limit: 1000})
	if err != nil || total != 101 || len(items) != 100 {
		t.Fatalf("%d items of %d, err %v", len(items), total, err)
	}
	if admin, total, err := r.s.AdminList(context.Background(), PayRefundAdminFilter{Page: 2, Limit: 1000}); err != nil || total != 101 || len(admin) != 1 {
		t.Fatalf("admin page 2: %d items of %d, err %v", len(admin), total, err)
	}
}

func TestPayRefundListForPayment_CapRemainingAndRefundable(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "LFP-1", "34", "")
	r.insertRefund(t, p, models.PayRefundConfirmed, 20_000_000)
	got, err := r.s.ListForPayment(context.Background(), p)
	if err != nil || len(got.Refunds) != 1 || got.Cap != "34.00" || got.Remaining != "14.00" || !got.Refundable || got.NotRefundableReason != "" {
		t.Fatalf("%+v %v", got, err)
	}
	r.insertRefund(t, p, models.PayRefundRequested, 5_000_000)
	got, err = r.s.ListForPayment(context.Background(), p)
	if err != nil || got.Remaining != "9.00" || got.Refundable || got.NotRefundableReason != "A refund is already in progress" {
		t.Fatalf("with an open refund: %+v", got)
	}
}

// Only the capacity lookup failing (the order read, say) must not hide the
// refunds already made: they are listed, with a try-again sentence.
func TestPayRefundListForPayment_ACapacityLookupFailureStillListsTheRefunds(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "LFP-DB", "34", "")
	r.insertRefund(t, p, models.PayRefundConfirmed, 5_000_000)
	if err := r.db.Migrator().DropTable(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	got, err := r.s.ListForPayment(context.Background(), p)
	if err != nil || got == nil || len(got.Refunds) != 1 || got.Refundable ||
		got.NotRefundableReason != "Refund availability can't be checked right now — try again shortly." {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPayRefundListForPayment_SaysWhyNot(t *testing.T) {
	r := newRefundRig(t)
	processing := r.paidDirect(t, "LFP-2", "34", "")
	processing.Status = models.PayStatusProcessing
	allBack := r.paidDirect(t, "LFP-3", "34", "34")
	full := r.paidDirect(t, "LFP-4", "33", "")
	r.insertRefund(t, full, models.PayRefundConfirmed, 33_000_000)
	for p, why := range map[*models.PayPayment]string{
		processing: "Only paid payments can be refunded",
		allBack:    "Refunds aren't available for this payment",
		full:       "This payment has been fully refunded",
	} {
		got, err := r.s.ListForPayment(context.Background(), p)
		if err != nil || got.Refundable || got.NotRefundableReason != why {
			t.Errorf("%s: %+v %v", p.Reference, got, err)
		}
	}
}

func TestPayRefundAdminList_NamesTheMerchantAndFilters(t *testing.T) {
	r := newRefundRig(t)
	a := r.paidDirect(t, "ADM-1", "34", "")
	b := r.paidDirect(t, "ADM-2", "34", "")
	sent := r.insertRefund(t, a, models.PayRefundSubmitted, 1_000_000)
	r.insertRefund(t, b, models.PayRefundConfirmed, 2_000_000)
	items, total, err := r.s.AdminList(context.Background(), PayRefundAdminFilter{Status: models.PayRefundSubmitted, Page: 1, Limit: 20})
	if err != nil || total != 1 || items[0].ID != sent.ID || items[0].MerchantName != "Sky Air" {
		t.Fatalf("%+v %d %v", items, total, err)
	}
	byPayment, total, err := r.s.AdminList(context.Background(), PayRefundAdminFilter{PaymentID: &b.ID})
	if err != nil || total != 1 || byPayment[0].PaymentID != b.ID {
		t.Fatalf("payment filter %+v", byPayment)
	}
	// Review M10: live and test refunds can be told apart and filtered.
	testPayment := r.paid(t, models.PayModeTest, "ADM-3", nil)
	r.insertRefund(t, testPayment, models.PayRefundConfirmed, 1_000_000)
	live, total, err := r.s.AdminList(context.Background(), PayRefundAdminFilter{Mode: models.PayModeLive})
	if err != nil || total != 2 || live[0].Mode != models.PayModeLive || live[1].Mode != models.PayModeLive {
		t.Fatalf("live filter %+v", live)
	}
	if test, total, _ := r.s.AdminList(context.Background(), PayRefundAdminFilter{Mode: models.PayModeTest}); total != 1 || test[0].Mode != models.PayModeTest {
		t.Fatalf("test filter %+v", test)
	}
}

// The payer sees what the merchant refunded them; nobody else holding the id does.
func TestPayCheckoutView_OnlyThePayerSeesTheMerchantRefund(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	p := r.paidDirect(t, "CHK-1", "34", "")
	ref := r.insertRefund(t, p, models.PayRefundConfirmed, 12_500_000)
	hash := "0xfeed"
	r.db.Model(ref).Updates(map[string]any{"token_symbol": "USDT", "chain_id": 56, "tx_hash": hash})

	v, err := r.pay.CheckoutView(ctx, &models.User{ID: r.payer}, p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	mr := v.MerchantRefund
	if mr == nil || mr.Status != PayRefundStatusPartial || mr.Amount != "12.50" || mr.TokenSymbol != "USDT" || mr.ChainID != 56 || mr.TxHash != hash || mr.ConfirmedAt == nil {
		t.Fatalf("merchantRefund %+v", mr)
	}
	if other, _ := r.pay.CheckoutView(ctx, &models.User{ID: uuid.New()}, p.ID, true); other.MerchantRefund != nil {
		t.Fatal("someone else saw the refund")
	}
}

func TestPayerHistory_CarriesTheMerchantRefund(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	paid := r.paidDirect(t, "HIS-1", "34", "")
	pending := r.paidDirect(t, "HIS-2", "34", "")
	r.insertRefund(t, paid, models.PayRefundConfirmed, 34_000_000)
	r.insertRefund(t, pending, models.PayRefundRequested, 1_000_000)
	items, err := r.pay.PayerHistory(ctx, r.payer)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uuid.UUID]*PayMerchantRefund{}
	for _, it := range items {
		got[it.ID] = it.MerchantRefund
	}
	if mr := got[paid.ID]; mr == nil || mr.Status != PayRefundStatusFull || mr.Amount != "34.00" {
		t.Errorf("paid: %+v", mr)
	}
	if mr := got[pending.ID]; mr == nil || mr.Status != "pending" || mr.Amount != "0.00" {
		t.Errorf("pending: %+v", mr)
	}
}

// An open refund already explains why another can't be asked for; a failing
// capacity lookup must not replace that with a try-again sentence.
func TestPayRefundListForPayment_AnOpenRefundWinsOverAFailedCapacityLookup(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "LFP-OPEN-DB", "34", "")
	r.insertRefund(t, p, models.PayRefundSubmitted, 5_000_000)
	if err := r.db.Migrator().DropTable(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	got, err := r.s.ListForPayment(context.Background(), p)
	if err != nil || got == nil || len(got.Refunds) != 1 || got.Refundable || got.NotRefundableReason != "A refund is already in progress" {
		t.Fatalf("%+v %v", got, err)
	}
}
