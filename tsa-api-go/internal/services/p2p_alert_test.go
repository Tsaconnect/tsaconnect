package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func p2pTestAlert(kind P2PAlertKind) P2PAlert {
	paid := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	return P2PAlert{Kind: kind, TradeID: uuid.MustParse("7b0c1d2e-0000-4000-8000-000000000001"),
		Token: "USDT", Amount: "25.5", FiatAmount: "38250", FiatCurrency: "NGN", FromStatus: "paid",
		MarkedPaidAt: &paid, RaisedRole: "buyer", Reason: "seller not releasing", Age: 2*time.Hour + 5*time.Minute}
}

func TestNotifyP2P_PaidTradeCancelledCarriesTradeAndAmount(t *testing.T) {
	m := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(m, []string{"ops@tsa.test"}, "https://admin.tsa.test")
	if err := n.NotifyP2P(context.Background(), p2pTestAlert(P2PAlertPaidTradeCancelled)); err != nil {
		t.Fatal(err)
	}
	n.Flush()
	sent := m.records()
	if len(sent) != 1 {
		t.Fatalf("sent %d, want 1", len(sent))
	}
	body := sent[0].subject + "\n" + sent[0].message
	for _, want := range []string{"7b0c1d2e-0000-4000-8000-000000000001", "25.5 USDT", "38250 NGN", "paid", "2026-10-09 08:00:00"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q\n%s", want, body)
		}
	}
}

func TestNotifyP2P_DisputeFiledDeepLinks(t *testing.T) {
	m := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(m, []string{"ops@tsa.test"}, "https://admin.tsa.test")
	_ = n.NotifyP2P(context.Background(), p2pTestAlert(P2PAlertDisputeFiled))
	n.Flush()
	s := m.records()[0]
	if s.ctaURL != "https://admin.tsa.test/p2p-disputes/7b0c1d2e-0000-4000-8000-000000000001" {
		t.Fatalf("cta = %q", s.ctaURL)
	}
	if !strings.Contains(s.message, "buyer") || !strings.Contains(s.message, "seller not releasing") {
		t.Fatalf("body = %s", s.message)
	}
}

func TestNotifyP2P_PaidUnreleasedSaysHowLong(t *testing.T) {
	m := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(m, []string{"ops@tsa.test"}, "")
	_ = n.NotifyP2P(context.Background(), p2pTestAlert(P2PAlertPaidUnreleased))
	n.Flush()
	if s := m.records()[0]; !strings.Contains(s.subject+s.message, "2h5m0s") {
		t.Fatalf("no age: %+v", s)
	}
}

func TestNotifyP2P_DormantWhenUnconfigured(t *testing.T) {
	m := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(m, nil, "")
	_ = n.NotifyP2P(context.Background(), p2pTestAlert(P2PAlertDisputeFiled))
	n.Flush()
	if len(m.records()) != 0 {
		t.Fatal("sent with no recipients")
	}
}

type recordingAlerter struct{ got []P2PAlert }

func (r *recordingAlerter) NotifyP2P(_ context.Context, a P2PAlert) error {
	r.got = append(r.got, a)
	return nil
}

func TestTradeServiceAlert_FillsTradeFields(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	rec := &recordingAlerter{}
	svc.SetAlerter(rec)
	tr := &models.P2PTrade{ID: uuid.New(), Token: "USDT", ChainID: 146, TokenAmount: "25500000",
		FiatAmount: "38250", FiatCurrency: "NGN", Status: models.P2PTradeStatusPaid}
	svc.alert(context.Background(), P2PAlertPaidTradeCancelled, tr, nil)
	if len(rec.got) != 1 || rec.got[0].Amount != "25.5" || rec.got[0].FromStatus != "paid" {
		t.Fatalf("got %+v", rec.got)
	}
	svc.SetAlerter(nil)
	svc.alert(context.Background(), P2PAlertPaidTradeCancelled, tr, nil) // must not panic
}

func TestNotifyP2P_PaidUnreleasedCTAIsTradeDetail(t *testing.T) {
	m := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(m, []string{"ops@tsa.test"}, "https://admin.tsa.test")
	_ = n.NotifyP2P(context.Background(), p2pTestAlert(P2PAlertPaidUnreleased))
	n.Flush()
	if got := m.records()[0].ctaURL; got != "https://admin.tsa.test/p2p-trades/7b0c1d2e-0000-4000-8000-000000000001" {
		t.Fatalf("cta = %q", got)
	}
}

func TestNotifyP2P_ReasonIsCappedAndRedacted(t *testing.T) {
	m := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(m, []string{"ops@tsa.test"}, "")
	a := p2pTestAlert(P2PAlertDisputeFiled)
	a.Reason = strings.Repeat("x", 500)
	_ = n.NotifyP2P(context.Background(), a)
	n.Flush()
	if msg := m.records()[0].message; strings.Contains(msg, strings.Repeat("x", 301)) {
		t.Fatal("reason not capped at 300")
	}
}
