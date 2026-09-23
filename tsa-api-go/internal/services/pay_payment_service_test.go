package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func newPayPaymentRig(t *testing.T) (*PayPaymentService, *models.PayMerchant) {
	t.Helper()
	db := newPayTestDB(t)
	now := func() time.Time { return payTestNow }
	merchants := NewPayMerchantService(db, nil, nil, []byte(strings.Repeat("k", 32)), now)
	return NewPayPaymentService(db, merchants, 100, "https://tsaconnectworld.com/pay", now), seedPayMerchant(t, db)
}

func payIn() CreatePayPaymentInput {
	return CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: "PNR-ABC123",
		Description: "LOS-ABV 12 Oct", ReturnURL: "https://www.skyair.ng/booking/done"}
}

func TestPayPayment_CreateSnapshotsTheSplitAndTarget(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, created, err := s.Create(context.Background(), m, models.PayModeLive, payIn())
	if err != nil || !created {
		t.Fatalf("err %v created %v", err, created)
	}
	if p.CustomerTotalKobo != 5_050_506 || p.MerchantNetKobo != 5_000_000 || p.FeeBps != 100 || p.CustomerFeeSharePercent != 100 ||
		p.SettleBankCode != "058" || p.SettleAccountNumber != "0123456789" || p.SettlementMode != models.PaySettlementFiat ||
		p.Status != models.PayStatusPending || !p.ExpiresAt.Equal(payTestNow.Add(30*time.Minute)) {
		t.Fatalf("payment = %+v", p)
	}
	v := s.View(p)
	if v.AppURL != "tsaconnect://pay?id="+p.ID.String() || v.CheckoutURL != "https://tsaconnectworld.com/pay/"+p.ID.String() || v.Fee != 50_506 {
		t.Fatalf("view = %+v", v)
	}
}

func TestPayPayment_SameReferenceSameBodyIsTheSamePayment(t *testing.T) {
	s, m := newPayPaymentRig(t)
	a, _, _ := s.Create(context.Background(), m, models.PayModeLive, payIn())
	b, created, err := s.Create(context.Background(), m, models.PayModeLive, payIn())
	if err != nil || created || a.ID != b.ID {
		t.Fatalf("retry: err %v created %v", err, created)
	}
	for name, mutate := range map[string]func(*CreatePayPaymentInput){
		"amount":      func(in *CreatePayPaymentInput) { in.AmountKobo = 6_000_000 },
		"description": func(in *CreatePayPaymentInput) { in.Description = "other" },
		"return url":  func(in *CreatePayPaymentInput) { in.ReturnURL = "https://skyair.ng/other" },
	} {
		in := payIn()
		mutate(&in)
		if _, _, err := s.Create(context.Background(), m, models.PayModeLive, in); !errors.Is(err, ErrPayReferenceConflict) {
			t.Fatalf("changed %s: err = %v", name, err)
		}
	}
}

// The insert-race path: another request stored the same reference between our
// pre-check and our insert. We must return that payment, not an error.
func TestPayPayment_AnInsertRaceReturnsTheWinner(t *testing.T) {
	s, m := newPayPaymentRig(t)
	winner, _, _ := s.Create(context.Background(), m, models.PayModeLive, payIn())
	loser := *winner
	loser.ID = [16]byte{}
	got, created, err := s.createOrExisting(context.Background(), &loser, payIn())
	if err != nil || created || got.ID != winner.ID {
		t.Fatalf("err %v created %v got %v", err, created, got)
	}
}

func TestPayPayment_CreateRefusesWhatTheMerchantDidNotAllow(t *testing.T) {
	s, m := newPayPaymentRig(t)
	for name, mutate := range map[string]func(*CreatePayPaymentInput){
		"below min":        func(in *CreatePayPaymentInput) { in.AmountKobo = 99_999 },
		"above max":        func(in *CreatePayPaymentInput) { in.AmountKobo = 500_000_001 },
		"not NGN":          func(in *CreatePayPaymentInput) { in.Currency = "USD" },
		"no reference":     func(in *CreatePayPaymentInput) { in.Reference = " " },
		"long reference":   func(in *CreatePayPaymentInput) { in.Reference = strings.Repeat("r", 129) },
		"foreign return":   func(in *CreatePayPaymentInput) { in.ReturnURL = "https://evil.example/phish" },
		"http return":      func(in *CreatePayPaymentInput) { in.ReturnURL = "http://skyair.ng/done" },
		"lookalike return": func(in *CreatePayPaymentInput) { in.ReturnURL = "https://skyair.ng.evil.example/x" },
		"userinfo return":  func(in *CreatePayPaymentInput) { in.ReturnURL = "https://skyair.ng@evil.example/x" },
		"port return":      func(in *CreatePayPaymentInput) { in.ReturnURL = "https://skyair.ng:8443/x" },
		"expiry too short": func(in *CreatePayPaymentInput) { in.ExpiresInSeconds = 899 },
		"expiry too long":  func(in *CreatePayPaymentInput) { in.ExpiresInSeconds = 86_401 },
	} {
		in := payIn()
		mutate(&in)
		if _, _, err := s.Create(context.Background(), m, models.PayModeLive, in); !errors.Is(err, ErrPayPaymentInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	for i, ok := range []func(*CreatePayPaymentInput){
		func(in *CreatePayPaymentInput) { in.AmountKobo = 100_000 },
		func(in *CreatePayPaymentInput) { in.AmountKobo = 500_000_000 },
		func(in *CreatePayPaymentInput) { in.ExpiresInSeconds = 900 },
		func(in *CreatePayPaymentInput) { in.ExpiresInSeconds = 86_400 },
		func(in *CreatePayPaymentInput) { in.ReturnURL = "https://SKYAIR.NG/x" },
	} {
		in := payIn()
		in.Reference = "OK-" + string(rune('A'+i))
		ok(&in)
		if _, _, err := s.Create(context.Background(), m, models.PayModeLive, in); err != nil {
			t.Fatalf("boundary %d refused: %v", i, err)
		}
	}
	m.SettlementMode = ""
	if _, _, err := s.Create(context.Background(), m, models.PayModeLive, payIn()); !errors.Is(err, ErrPayMerchantNotReady) {
		t.Fatalf("unconfigured merchant: err = %v", err)
	}
}

func TestPayPayment_AMerchantReadsOnlyItsOwnPaymentsInItsOwnMode(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeLive, payIn())
	other := seedPayMerchant(t, s.db)
	if _, err := s.GetForMerchant(context.Background(), other.ID, models.PayModeLive, p.ID); !errors.Is(err, ErrPayPaymentNotFound) {
		t.Fatalf("other merchant: err = %v", err)
	}
	if _, err := s.GetForMerchant(context.Background(), m.ID, models.PayModeTest, p.ID); !errors.Is(err, ErrPayPaymentNotFound) {
		t.Fatalf("a test key read a live payment: err = %v", err)
	}
}
