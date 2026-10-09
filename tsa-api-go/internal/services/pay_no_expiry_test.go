package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ─── A payment expires only when its merchant set an expiry (owner, 2026-10-09) ─

func TestPayPayment_WithoutExpiresInItNeverExpires(t *testing.T) {
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	in := payIn()
	in.ExpiresInSeconds = 0
	p, _, err := s.Create(context.Background(), m, models.PayModeTest, in)
	if err != nil || p.ExpiresAt != nil {
		t.Fatalf("err %v expiresAt %v: no expiresIn must mean no expiry", err, p.ExpiresAt)
	}
	if b, _ := json.Marshal(s.View(p)); !strings.Contains(string(b), `"expiresAt":null`) {
		t.Fatalf("view JSON %s: want expiresAt null", b)
	}

	s.now = func() time.Time { return payTestNow.AddDate(5, 0, 0) }
	if n, err := s.ExpireDue(context.Background(), 50); err != nil || n != 0 {
		t.Fatalf("the sweep expired %d (err %v) — a payment with no expiry never expires", n, err)
	}
	v, err := s.CheckoutView(context.Background(), payUser(t, s, false), p.ID, false)
	if err != nil || !v.Payable || v.ExpiresAt != nil {
		t.Fatalf("five years on: view %+v err %v", v, err)
	}
	if _, fresh, err := s.Lock(context.Background(), payUser(t, s, false), p.ID, false); err != nil || !fresh {
		t.Fatalf("five years on: lock err %v fresh %v", err, fresh)
	}
}

func TestPayOpenLink_PaymentsExpireOnlyWhenTheLinkSaysSo(t *testing.T) {
	s, m := newPayPaymentRig(t)
	never := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	p, err := s.CreateFromOpenLink(context.Background(), never.Slug, PayOpenPaymentInput{AmountKobo: 500_000}, false)
	if err != nil || p.ExpiresAt != nil {
		t.Fatalf("err %v expiresAt %v: a link with no payment expiry starts payments that never expire", err, p.ExpiresAt)
	}

	in := openLinkIn()
	in.PaymentExpiresIn = 1800
	timed := mustOpenLink(t, s, m, models.PayModeTest, in)
	p, err = s.CreateFromOpenLink(context.Background(), timed.Slug, PayOpenPaymentInput{AmountKobo: 500_000}, false)
	if err != nil || p.ExpiresAt == nil || !p.ExpiresAt.Equal(payTestNow.Add(30*time.Minute)) {
		t.Fatalf("err %v expiresAt %v: want the link's 30 minutes", err, p.ExpiresAt)
	}

	for _, secs := range []int{-1, 899, 86_401} {
		in := openLinkIn()
		in.PaymentExpiresIn = secs
		if _, err := s.CreateOpenLink(context.Background(), m, models.PayModeTest, in); !errors.Is(err, ErrPayPaymentInvalid) {
			t.Fatalf("%d seconds: err = %v", secs, err)
		}
	}
}

func TestPayOpenLink_EditSetsAndClearsThePaymentExpiry(t *testing.T) {
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	edit := func(secs int) (*models.PayOpenLink, error) {
		return s.UpdateOpenLink(context.Background(), m, models.PayModeTest, l.ID,
			PayOpenLinkUpdate{PaymentExpiresIn: &secs, ExpectedVersion: &l.Version})
	}
	var err error
	if l, err = edit(3600); err != nil || l.PaymentExpiresIn != 3600 {
		t.Fatalf("set: err %v link %+v", err, l)
	}
	paused := false
	if l, err = s.UpdateOpenLink(context.Background(), m, models.PayModeTest, l.ID,
		PayOpenLinkUpdate{Active: &paused, ExpectedVersion: &l.Version}); err != nil || l.PaymentExpiresIn != 3600 {
		t.Fatalf("pausing touched the expiry: err %v link %+v", err, l)
	}
	if _, err := edit(600); !errors.Is(err, ErrPayPaymentInvalid) {
		t.Fatalf("600 seconds: err = %v", err)
	}
	if l, err = edit(0); err != nil || l.PaymentExpiresIn != 0 {
		t.Fatalf("clear: err %v link %+v", err, l)
	}
	views, err := s.OpenLinkViews(context.Background(), m, []models.PayOpenLink{*l}, false)
	if b, _ := json.Marshal(views); err != nil || !strings.Contains(string(b), `"paymentExpiresIn":0`) {
		t.Fatalf("owner view %s err %v: want paymentExpiresIn", b, err)
	}
}

// ─── A payment pays where the merchant is paid NOW, as of its start ─────────

func movePayout(t *testing.T, s *PayPaymentService, m *models.PayMerchant, fields map[string]any) {
	t.Helper()
	if err := s.db.Model(&models.PayMerchant{}).Where("id = ?", m.ID).Updates(fields).Error; err != nil {
		t.Fatal(err)
	}
}

func TestPayPayment_AStartedPaymentPaysTheMerchantsCurrentPayout(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	movePayout(t, s, m, map[string]any{"bank_code": "044", "account_number": "9876543210", "account_name": "SKY AIR NEW"})

	payer := payUser(t, s, false)
	locked, fresh, err := s.Lock(context.Background(), payer, p.ID, false)
	if err != nil || !fresh {
		t.Fatalf("lock: err %v fresh %v", err, fresh)
	}
	if locked.SettleBankCode != "044" || locked.SettleAccountNumber != "9876543210" || locked.SettleAccountName != "SKY AIR NEW" {
		t.Fatalf("payout %s/%s/%s: want the merchant's current account", locked.SettleBankCode, locked.SettleAccountNumber, locked.SettleAccountName)
	}
	if locked.CustomerTotalKobo != p.CustomerTotalKobo || locked.MerchantNetKobo != p.MerchantNetKobo || locked.FeeBps != p.FeeBps {
		t.Fatalf("amounts moved: %+v", locked)
	}

	// Once a customer is paying, the payment's destination is fixed.
	movePayout(t, s, m, map[string]any{"bank_code": "011", "account_number": "1111111111"})
	if _, fresh, err := s.Lock(context.Background(), payer, p.ID, false); err != nil || fresh {
		t.Fatalf("resume: err %v fresh %v", err, fresh)
	}
	if got, _ := s.Get(context.Background(), p.ID); got.SettleBankCode != "044" || got.SettleAccountNumber != "9876543210" {
		t.Fatalf("a payment in progress changed destination: %s/%s", got.SettleBankCode, got.SettleAccountNumber)
	}
}

func TestPayPayment_TheCheckoutShowsTheSettlementTheCustomerWillPay(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	movePayout(t, s, m, map[string]any{"settlement_mode": models.PaySettlementCrypto, "chain_id": 8453,
		"token_symbol": "USDC", "token_address": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
		"wallet_address": "0x00000000000000000000000000000000000000aa"})
	v, err := s.CheckoutView(context.Background(), payUser(t, s, false), p.ID, false)
	if err != nil || v.SettlementMode != models.PaySettlementCrypto || v.SettleTokenSymbol != "USDC" || v.SettleChainID != 8453 {
		t.Fatalf("view %+v err %v: want the merchant's current crypto settlement", v, err)
	}
	locked, _, err := s.Lock(context.Background(), payUser(t, s, false), p.ID, false)
	if err != nil || locked.SettlementMode != models.PaySettlementCrypto || locked.SettleWalletAddress != "0x00000000000000000000000000000000000000aa" {
		t.Fatalf("lock %+v err %v", locked, err)
	}
}

func TestPayPayment_AMerchantWithNowhereToBePaidCannotBeStarted(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	movePayout(t, s, m, map[string]any{"settlement_mode": ""})
	if v, _ := s.CheckoutView(context.Background(), payUser(t, s, false), p.ID, false); v.Payable {
		t.Fatal("payable with no payout set")
	}
	if _, _, err := s.Lock(context.Background(), payUser(t, s, false), p.ID, false); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("lock err = %v", err)
	}
}

// A merchant who changes how they are paid after a payment went out is paid the
// new way: the checkout offers the new route, and that route takes it.
func TestPayCheckout_APaymentFollowsTheMerchantToCrypto(t *testing.T) {
	r := &cryptoRig{payCheckoutRig: newPayCheckoutRig(t),
		pricer: &fakePayPricer{price: big.NewRat(10003, 10000), dec: 18}, rate: &fakePayRate{rate: big.NewRat(1500, 1)}}
	r.svc.SetCrypto(r.pricer, r.rate)
	// r.p was made while the merchant was paid in naira; then they move to crypto.
	movePayout(t, r.pay, r.m, map[string]any{"settlement_mode": models.PaySettlementCrypto, "chain_id": 56,
		"token_address": bscUSDT, "token_symbol": "USDT", "wallet_address": cryptoMerchant})

	if _, err := r.svc.CryptoQuote(context.Background(), r.alice, r.p.ID, payCryptoSource(r.payCheckoutRig)); err != nil {
		t.Fatalf("quote: %v", err)
	}
	if r.quoter.in.ToChainID != 56 || r.quoter.in.ToToken != bscUSDT || r.quoter.in.ToAddress != cryptoMerchant {
		t.Fatalf("quoted to %d/%s/%s, want the merchant's new wallet", r.quoter.in.ToChainID, r.quoter.in.ToToken, r.quoter.in.ToAddress)
	}
	if _, _, err := r.svc.PayCrypto(context.Background(), r.alice, r.p.ID, payCryptoSource(r.payCheckoutRig)); err != nil {
		t.Fatalf("pay: %v", err)
	}
	if got := r.get(t); got.SettlementMode != models.PaySettlementCrypto || got.SettleWalletAddress != cryptoMerchant {
		t.Fatalf("payment settles %s to %s", got.SettlementMode, got.SettleWalletAddress)
	}
}

func TestPayCheckout_APaymentFollowsTheMerchantBackToNaira(t *testing.T) {
	r := newCryptoRig(t)
	movePayout(t, r.pay, r.m, map[string]any{"settlement_mode": models.PaySettlementFiat})
	if _, err := r.direct(t); err != nil {
		t.Fatalf("pay direct: %v", err)
	}
	if r.orders.sell.BankCode != "058" || r.orders.sell.AccountNo != "0123456789" {
		t.Fatalf("sell %+v: want the merchant's bank", r.orders.sell)
	}
}

// ─── A new payout account waits a day before outstanding payments follow it ──

func TestPayMerchant_OnlyAPayoutChangeIsStamped(t *testing.T) {
	s, banks := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	banks.name = "SKY AIR NIGERIA"
	got, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{SettlementMode: payStr("fiat"), BankCode: payStr("044"), AccountNumber: payStr("9876543210")})
	if err != nil || got.PayoutChangedAt == nil {
		t.Fatalf("err %v: a new account must be stamped, got %v", err, got.PayoutChangedAt)
	}
	stamped := *got.PayoutChangedAt
	renamed := "Sky Air Express"
	if got, _, err = s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{BusinessName: &renamed}); err != nil || !got.PayoutChangedAt.Equal(stamped) {
		t.Fatalf("err %v: a rename moved the stamp to %v", err, got.PayoutChangedAt)
	}
	if got, _, err = s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{BankCode: payStr("044"), AccountNumber: payStr("9876543210")}); err != nil || !got.PayoutChangedAt.Equal(stamped) {
		t.Fatalf("err %v: re-saving the same account moved the stamp to %v", err, got.PayoutChangedAt)
	}
	if stored, _ := s.Get(context.Background(), m.ID); stored.PayoutChangedAt == nil || !stored.PayoutChangedAt.Equal(stamped) {
		t.Fatalf("stored stamp %v", stored.PayoutChangedAt)
	}
}

func TestPayPayment_AnOutstandingPaymentFollowsANewPayoutOnlyAfterADay(t *testing.T) {
	s, m := newPayPaymentRig(t)
	in := payIn()
	in.ExpiresInSeconds = 0
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, in)
	in.Reference = "PNR-LATER"
	later, _, _ := s.Create(context.Background(), m, models.PayModeTest, in)
	changed := p.CreatedAt.Add(time.Hour) // created_at is the real clock
	movePayout(t, s, m, map[string]any{"bank_code": "044", "account_number": "9876543210", "payout_changed_at": changed})

	// Within the day: what the payment was made with, on the screen and at the start.
	s.now = func() time.Time { return changed.Add(23 * time.Hour) }
	locked, _, err := s.Lock(context.Background(), payUser(t, s, false), p.ID, false)
	if err != nil || locked.SettleBankCode != "058" || locked.SettleAccountNumber != "0123456789" {
		t.Fatalf("err %v: within a day of the change paid %s/%s", err, locked.SettleBankCode, locked.SettleAccountNumber)
	}

	// After it: the new account.
	s.now = func() time.Time { return changed.Add(payoutCoolOff + time.Second) }
	locked, _, err = s.Lock(context.Background(), payUser(t, s, false), later.ID, false)
	if err != nil || locked.SettleBankCode != "044" || locked.SettleAccountNumber != "9876543210" {
		t.Fatalf("err %v: a day after the change paid %s/%s", err, locked.SettleBankCode, locked.SettleAccountNumber)
	}
}

func TestPayPayment_ThePayoutHoldShowsOnTheCheckoutToo(t *testing.T) {
	s, m := newPayPaymentRig(t)
	in := payIn()
	in.ExpiresInSeconds = 0
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, in)
	changed := p.CreatedAt.Add(time.Hour) // created_at is the real clock
	movePayout(t, s, m, map[string]any{"settlement_mode": models.PaySettlementCrypto, "chain_id": 8453,
		"token_symbol": "USDC", "token_address": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
		"wallet_address": "0x00000000000000000000000000000000000000aa", "payout_changed_at": changed})
	s.now = func() time.Time { return changed.Add(time.Hour) }
	v, err := s.CheckoutView(context.Background(), payUser(t, s, false), p.ID, false)
	if err != nil || v.SettlementMode != models.PaySettlementFiat {
		t.Fatalf("view %+v err %v: within the hold it settles as made", v, err)
	}
	if got, _ := s.GetAsStarted(context.Background(), p.ID); got.SettlementMode != models.PaySettlementFiat {
		t.Fatalf("GetAsStarted settles %s within the hold", got.SettlementMode)
	}
}
