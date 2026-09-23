package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

type fakePayOrders struct {
	sell      PaycrestSellRequest
	calls     int
	converted bool
	err       error
	deposit   *big.Int
}

func (f *fakePayOrders) CreateSellForFiat(_ context.Context, req PaycrestSellRequest, converted bool, _ *big.Int) (*models.PaycrestOrderRecord, error) {
	f.sell, f.calls, f.converted = req, f.calls+1, converted
	if f.err != nil {
		return nil, f.err
	}
	return &models.PaycrestOrderRecord{ID: uuid.New()}, nil
}
func (f *fakePayOrders) SellDepositFor(_ context.Context, _, _, _, _ string, converted bool) (*big.Int, error) {
	f.converted = converted
	return new(big.Int).Set(f.deposit), nil
}

type fakePayIntents struct {
	in        CreateSellIntentInput
	err       error
	abandoned int
	status    *LiFiTransferStatus
}

func (f *fakePayIntents) Create(_ context.Context, _ *models.User, in CreateSellIntentInput) (*models.RampSellIntent, *SellSwapQuote, error) {
	f.in = in
	if f.err != nil {
		return nil, nil, f.err
	}
	return &models.RampSellIntent{ID: uuid.New(), Status: models.SellIntentSwapping}, &SellSwapQuote{}, nil
}
func (f *fakePayIntents) AbandonForPay(context.Context, uuid.UUID) error { f.abandoned++; return nil }
func (f *fakePayIntents) SwapStatus(context.Context, uuid.UUID) (*LiFiTransferStatus, error) {
	return f.status, nil
}

type fakePayQuoter struct {
	in  SellQuoteInput
	err error
}

func (f *fakePayQuoter) Quote(_ context.Context, in SellQuoteInput) (*SellSwapQuote, error) {
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	return &SellSwapQuote{Route: &LiFiRoute{FromAmount: "17000000000000000"}}, nil
}

type payCheckoutRig struct {
	svc     *PayCheckoutService
	pay     *PayPaymentService
	p       *models.PayPayment
	m       *models.PayMerchant
	alice   *models.User
	orders  *fakePayOrders
	intents *fakePayIntents
	quoter  *fakePayQuoter
}

func newPayCheckoutRig(t *testing.T) *payCheckoutRig {
	t.Helper()
	pay, m := newPayPaymentRig(t)
	if err := pay.db.AutoMigrate(&models.PaycrestOrderRecord{}, &models.RampSellIntent{}, &models.GasSponsorship{}); err != nil {
		t.Fatal(err)
	}
	p, _, _ := pay.Create(context.Background(), m, models.PayModeLive, payIn())
	r := &payCheckoutRig{pay: pay, p: p, m: m, alice: payUser(t, pay, true),
		orders: &fakePayOrders{deposit: big.NewInt(33_500000)}, intents: &fakePayIntents{}, quoter: &fakePayQuoter{}}
	r.svc = NewPayCheckoutService(pay, r.orders, r.intents, r.quoter, true)
	pay.SetIntents(r.intents)
	return r
}

func (r *payCheckoutRig) get(t *testing.T) *models.PayPayment {
	t.Helper()
	got, _ := r.pay.Get(context.Background(), r.p.ID)
	return got
}

func (r *payCheckoutRig) direct(t *testing.T) (*models.PaycrestOrderRecord, error) {
	t.Helper()
	return r.svc.PayDirect(context.Background(), r.alice, r.p.ID, PayDirectInput{ChainKey: "base", Token: "USDC"})
}

func (r *payCheckoutRig) converted(t *testing.T) (*models.RampSellIntent, *SellSwapQuote, error) {
	t.Helper()
	return r.svc.PayConverted(context.Background(), r.alice, r.p.ID, PayConvertedInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: r.alice.WalletAddress})
}

func TestPayDirect_SellsExactlyTheSnapshottedNetToTheSnapshottedBank(t *testing.T) {
	r := newPayCheckoutRig(t)
	// An admin edits the merchant after the payment was created: the payment keeps its snapshot.
	r.pay.db.Model(r.m).Updates(map[string]any{"bank_code": "044", "account_number": "9876543210", "customer_fee_share_percent": 0})
	if _, err := r.direct(t); err != nil {
		t.Fatal(err)
	}
	if r.orders.sell.FiatAmount != "50000.00" || r.orders.sell.AccountNo != "0123456789" || r.orders.sell.BankCode != "058" || r.orders.converted {
		t.Fatalf("sell = %+v converted=%v", r.orders.sell, r.orders.converted)
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil {
		t.Fatalf("payment = %+v", got)
	}
}

func TestPayDirect_MerchantAbsorbingTheFeeSellsTheReducedNet(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.pay.db.Model(r.m).Update("customer_fee_share_percent", 0)
	r.m.CustomerFeeSharePercent = 0
	in := payIn()
	in.Reference = "SHARE-0"
	p, _, _ := r.pay.Create(context.Background(), r.m, models.PayModeLive, in)
	if _, err := r.svc.PayDirect(context.Background(), r.alice, p.ID, PayDirectInput{ChainKey: "base", Token: "USDC"}); err != nil {
		t.Fatal(err)
	}
	if r.orders.sell.FiatAmount != "49500.00" {
		t.Fatalf("fiat = %s", r.orders.sell.FiatAmount)
	}
}

func TestPayDirect_RetryBySamePayerNeverStartsASecondAttempt(t *testing.T) {
	r := newPayCheckoutRig(t)
	first, _ := r.direct(t)
	if _, err := r.direct(t); !errors.Is(err, ErrPayInProgress) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := r.converted(t); !errors.Is(err, ErrPayInProgress) {
		t.Fatalf("switching routes mid-payment: err = %v", err)
	}
	got := r.get(t)
	if r.orders.calls != 1 || got.Status != models.PayStatusProcessing || *got.PaycrestOrderID != first.ID || *got.PayerUserID != r.alice.ID {
		t.Fatalf("calls %d payment %+v", r.orders.calls, got)
	}
}

func TestPayDirect_SecondCustomerIsRefusedAndTheFirstKeepsTheLock(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.direct(t)
	bob := payUser(t, r.pay, true)
	if _, err := r.svc.PayDirect(context.Background(), bob, r.p.ID, PayDirectInput{ChainKey: "base", Token: "USDC"}); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("err = %v", err)
	}
	if got := r.get(t); r.orders.calls != 1 || *got.PayerUserID != r.alice.ID || got.Status != models.PayStatusProcessing {
		t.Fatalf("calls %d payment %+v", r.orders.calls, got)
	}
}

func TestPayDirect_ReleasesWhenTheOrderCannotBeOpened(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.orders.err = ErrPaycrestOrderOpen
	if _, err := r.direct(t); !errors.Is(err, ErrPaycrestOrderOpen) {
		t.Fatalf("err = %v", err)
	}
	if got := r.get(t); got.Status != models.PayStatusPending || got.PayerUserID != nil {
		t.Fatalf("payment = %+v", got)
	}
}

func TestPayConverted_TargetsTheDepositPlusTheBufferInExactArithmetic(t *testing.T) {
	for _, tc := range []struct{ dep, want int64 }{{33_500000, 33_835000}, {33_000100, 33_330101}, {1, 2}} {
		r := newPayCheckoutRig(t)
		r.orders.deposit = big.NewInt(tc.dep)
		if _, _, err := r.converted(t); err != nil {
			t.Fatal(err)
		}
		if !r.orders.converted || r.intents.in.ToAmount != big.NewInt(tc.want).String() || r.intents.in.PayoutFiat != "50000.00" ||
			r.intents.in.AccountNumber != "0123456789" || *r.intents.in.PayPaymentID != r.p.ID {
			t.Fatalf("dep %d: intent input = %+v", tc.dep, r.intents.in)
		}
	}
}

func TestPayConverted_ReleasesWhenTheIntentCannotBeCreated(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.intents.err = ErrSellIntentOpen
	if _, _, err := r.converted(t); !errors.Is(err, ErrSellIntentOpen) {
		t.Fatalf("err = %v", err)
	}
	if got := r.get(t); got.Status != models.PayStatusPending || got.PayerUserID != nil {
		t.Fatalf("payment = %+v", got)
	}
}

func TestPayConverted_WithoutAnIntentServiceIsUnavailableAndLeavesThePaymentFree(t *testing.T) {
	r := newPayCheckoutRig(t)
	svc := NewPayCheckoutService(r.pay, r.orders, nil, nil, true)
	_, _, err := svc.PayConverted(context.Background(), r.alice, r.p.ID, PayConvertedInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: r.alice.WalletAddress})
	if !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if got := r.get(t); got.Status != models.PayStatusPending || got.PayerUserID != nil {
		t.Fatalf("payment = %+v", got)
	}
}

func TestPayCheckout_RefusalsLeaveThePaymentFree(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.pay.db.Model(&models.PayPayment{}).Where("id = ?", r.p.ID).Update("settlement_mode", models.PaySettlementCrypto)
	if _, err := r.direct(t); !errors.Is(err, ErrPayWrongRoute) {
		t.Fatalf("crypto merchant: err = %v", err)
	}
	if got := r.get(t); got.Status != models.PayStatusPending || got.PayerUserID != nil {
		t.Fatalf("after a refusal: %+v", got)
	}
	in := payIn()
	in.Reference = "TEST-1"
	test, _, _ := r.pay.Create(context.Background(), r.m, models.PayModeTest, in)
	if _, err := r.svc.PayDirect(context.Background(), r.alice, test.ID, PayDirectInput{ChainKey: "base", Token: "USDC"}); !errors.Is(err, ErrPayNotPayable) || r.orders.calls != 0 {
		t.Fatalf("test payment: err %v calls %d", err, r.orders.calls)
	}
	if got, _ := r.pay.Get(context.Background(), test.ID); got.Status != models.PayStatusPending || got.PayerUserID != nil {
		t.Fatalf("a refused test payment must stay free: %+v", got)
	}
	offline := NewPayCheckoutService(r.pay, r.orders, r.intents, nil, false)
	in.Reference = "LIVE-OFF"
	live, _, _ := r.pay.Create(context.Background(), r.m, models.PayModeLive, in)
	if _, err := offline.PayDirect(context.Background(), r.alice, live.ID, PayDirectInput{ChainKey: "base", Token: "USDC"}); !errors.Is(err, ErrPayNotPayable) || r.orders.calls != 0 {
		t.Fatalf("live disabled: err %v calls %d", err, r.orders.calls)
	}
}

func TestPayCheckout_AnUnverifiedPayerIsRefusedBeforeAnyProviderCall(t *testing.T) {
	r := newPayCheckoutRig(t)
	bob := payUser(t, r.pay, false)
	if _, err := r.svc.PayDirect(context.Background(), bob, r.p.ID, PayDirectInput{ChainKey: "base", Token: "USDC"}); !errors.Is(err, ErrKYCVerificationRequired) || r.orders.calls != 0 {
		t.Fatalf("err %v calls %d", err, r.orders.calls)
	}
}

func TestPayQuote_PricesTheSameTargetWithoutLockingAnything(t *testing.T) {
	r := newPayCheckoutRig(t)
	q, err := r.svc.Quote(context.Background(), r.alice, r.p.ID, PayConvertedInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: r.alice.WalletAddress})
	if err != nil {
		t.Fatal(err)
	}
	if q.USDCTarget != "33835000" || q.FromAmount != "17000000000000000" || r.quoter.in.ToAmount != "33835000" || r.quoter.in.FromAmount != "" ||
		r.quoter.in.WalletAddress != r.alice.WalletAddress || r.quoter.in.FiatCode != "NGN" || r.quoter.in.FromChainID != 56 {
		t.Fatalf("quote %+v input %+v", q, r.quoter.in)
	}
	if got := r.get(t); got.Status != models.PayStatusPending || got.PayerUserID != nil {
		t.Fatalf("a quote must not lock: %+v", got)
	}
}

func TestPayQuote_RefusesWhatThisPayerCannotPay(t *testing.T) {
	r := newPayCheckoutRig(t)
	src := PayConvertedInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: r.alice.WalletAddress}
	if _, err := r.direct(t); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Quote(context.Background(), r.alice, r.p.ID, src); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("a payment already in progress: err = %v", err)
	}
	bob := payUser(t, r.pay, true)
	if _, err := r.svc.Quote(context.Background(), bob, r.p.ID, src); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("someone else's payment: err = %v", err)
	}
	in := payIn()
	in.Reference = "TEST-Q"
	test, _, _ := r.pay.Create(context.Background(), r.m, models.PayModeTest, in)
	if _, err := r.svc.Quote(context.Background(), r.alice, test.ID, src); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("test payment: err = %v", err)
	}
	if r.quoter.in.ToAmount != "" {
		t.Fatalf("a refused quote must not reach LiFi: %+v", r.quoter.in)
	}
	noQuoter := NewPayCheckoutService(r.pay, r.orders, r.intents, nil, true)
	if _, err := noQuoter.Quote(context.Background(), r.alice, r.p.ID, src); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("no quoter: err = %v", err)
	}
}

func TestPayQuote_ACryptoMerchantHasNoNairaQuote(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.pay.db.Model(&models.PayPayment{}).Where("id = ?", r.p.ID).Update("settlement_mode", models.PaySettlementCrypto)
	if _, err := r.svc.Quote(context.Background(), r.alice, r.p.ID, PayConvertedInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: r.alice.WalletAddress}); !errors.Is(err, ErrPayWrongRoute) {
		t.Fatalf("err = %v", err)
	}
}

// ---- reconcile ----

func seedSponsorship(t *testing.T, db *gorm.DB, orderID uuid.UUID, txHash string) {
	t.Helper()
	row := &models.GasSponsorship{UserID: uuid.New(), OrderID: orderID, Kind: models.GasSponsorshipKindAuthorization,
		ChainKey: "base", Token: "USDC", Wallet: "0xabc", TxHash: txHash}
	if err := db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
}

func (r *payCheckoutRig) seedOrder(t *testing.T, status string) uuid.UUID {
	t.Helper()
	order, err := r.direct(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.pay.db.Create(&models.PaycrestOrderRecord{ID: order.ID, UserID: r.alice.ID, Direction: models.PaycrestDirectionSell,
		Status: status, Reference: "PCS-" + order.ID.String()[:8]}).Error; err != nil {
		t.Fatal(err)
	}
	return order.ID
}

func TestPayReconcile_OrderOutcomes(t *testing.T) {
	claimed := payTestNow
	setOrder := func(col string, val any) func(r *payCheckoutRig, id uuid.UUID) {
		return func(r *payCheckoutRig, id uuid.UUID) {
			r.pay.db.Model(&models.PaycrestOrderRecord{}).Where("id = ?", id).Update(col, val)
		}
	}
	for name, tc := range map[string]struct {
		status   string
		evidence func(r *payCheckoutRig, orderID uuid.UUID)
		want     string
	}{
		"settled":                         {models.PaycrestStatusSettled, nil, models.PayStatusSucceeded},
		"refunded: retry allowed":         {models.PaycrestStatusRefunded, nil, models.PayStatusPending},
		"expired, nothing sent":           {models.PaycrestStatusExpired, nil, models.PayStatusPending},
		"expired, zero paid":              {models.PaycrestStatusExpired, setOrder("amount_paid", "0"), models.PayStatusPending},
		"expired, amount paid":            {models.PaycrestStatusExpired, setOrder("amount_paid", "10.5"), models.PayStatusFailed},
		"expired, customer claimed":       {models.PaycrestStatusExpired, setOrder("payment_claimed_at", &claimed), models.PayStatusFailed},
		"expired, we relayed a deposit":   {models.PaycrestStatusExpired, func(r *payCheckoutRig, id uuid.UUID) { seedSponsorship(t, r.pay.db, id, "0x"+strings.Repeat("cd", 32)) }, models.PayStatusFailed},
		"expired, sponsorship never sent": {models.PaycrestStatusExpired, func(r *payCheckoutRig, id uuid.UUID) { seedSponsorship(t, r.pay.db, id, "") }, models.PayStatusPending},
		"unmatched":                       {models.PaycrestStatusUnmatched, nil, models.PayStatusFailed},
		"failed at Paycrest":              {models.PaycrestStatusFailed, nil, models.PayStatusFailed},
		"failed with money received":      {models.PaycrestStatusFailed, setOrder("amount_paid", "10.5"), models.PayStatusFailed},
		"cancelled on our side":           {models.PaycrestStatusCancelled, nil, models.PayStatusFailed},
		"awaiting deposit":                {models.PaycrestStatusAwaitingDeposit, nil, models.PayStatusProcessing},
		"pending":                         {models.PaycrestStatusPending, nil, models.PayStatusProcessing},
		"settling":                        {models.PaycrestStatusSettling, nil, models.PayStatusProcessing},
		"a buy-only status":               {models.PaycrestStatusRecoveryPending, nil, models.PayStatusProcessing},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPayCheckoutRig(t)
			withWebhooks(t, r.pay, r.m)
			orderID := r.seedOrder(t, tc.status)
			if tc.evidence != nil {
				tc.evidence(r, orderID)
			}
			if err := r.pay.ReconcileTick(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := r.get(t)
			if got.Status != tc.want {
				t.Fatalf("payment %s, want %s", got.Status, tc.want)
			}
			switch tc.want {
			case models.PayStatusSucceeded:
				if got.SettlementRef != orderID.String() {
					t.Fatalf("settlement ref = %q", got.SettlementRef)
				}
			case models.PayStatusPending:
				if got.PaycrestOrderID != nil || got.PayerUserID != nil {
					t.Fatalf("a released payment must drop its attempt and payer: %+v", got)
				}
				// The customer can start again — with any token.
				if _, _, err := r.converted(t); err != nil {
					t.Fatalf("retry after release: %v", err)
				}
			case models.PayStatusProcessing:
				if got.PaycrestOrderID == nil || *got.PaycrestOrderID != orderID {
					t.Fatalf("a waiting payment keeps its order: %+v", got)
				}
			}
			wantEvents := map[string]int{models.PayStatusSucceeded: 1, models.PayStatusFailed: 1}[tc.want]
			if n := len(deliveries(t, r.pay)); n != wantEvents {
				t.Fatalf("%d webhooks, want %d", n, wantEvents)
			}
		})
	}
}

func TestPayReconcile_AnInFlightPaymentFinishesAfterExpiry(t *testing.T) {
	r := newPayCheckoutRig(t)
	orderID := r.seedOrder(t, models.PaycrestStatusAwaitingDeposit)
	r.pay.now = func() time.Time { return payTestNow.Add(2 * time.Hour) }
	r.pay.db.Model(&models.PaycrestOrderRecord{}).Where("id = ?", orderID).Update("status", models.PaycrestStatusSettled)
	r.pay.ReconcileTick(context.Background())
	if got := r.get(t); got.Status != models.PayStatusSucceeded {
		t.Fatalf("status %s — processing must not expire", got.Status)
	}
}

func TestPayReconcile_ALinkedOrderMissingFromTheTableWaits(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.direct(t) // the fake returns an order id that has no row
	if err := r.pay.ReconcileTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil {
		t.Fatalf("payment = %+v", got)
	}
}

// startConverted starts a converted payment and stores a real intent row
// with the id the fake returned, so the reconciler can read it.
func (r *payCheckoutRig) startConverted(t *testing.T) *models.RampSellIntent {
	t.Helper()
	in, _, err := r.converted(t)
	if err != nil {
		t.Fatal(err)
	}
	row := *in
	row.UserID, row.WalletAddress, row.SourceAddress, row.SourceChainID = r.alice.ID, r.alice.WalletAddress, r.alice.WalletAddress, 56
	row.SourceTokenAddress, row.SourceAmount, row.FiatCode, row.BankCode, row.AccountNumber = evmNativeAddress, "1", "NGN", "058", "0123456789"
	if err := r.pay.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return &row
}

func TestPayReconcile_IntentOutcomes(t *testing.T) {
	hash := "0x" + strings.Repeat("ab", 32)
	for name, tc := range map[string]struct {
		status   string
		swapHash bool
		lifi     string
		sub      string
		after    time.Duration
		want     string // pending may already have become expired: both mean "released"
		abandons int
	}{
		"swapping, no hash, fresh":            {models.SellIntentSwapping, false, "", "", 5 * time.Minute, models.PayStatusProcessing, 0},
		"swapping, no hash, 11 min":           {models.SellIntentSwapping, false, "", "", 11 * time.Minute, models.PayStatusPending, 1},
		"ordering: an offramp is opening":     {models.SellIntentOrdering, true, "", "", 3 * time.Hour, models.PayStatusProcessing, 0},
		"swapped, lifi failed":                {models.SellIntentSwapped, true, LiFiStatusFailed, "", time.Minute, models.PayStatusPending, 1},
		"swapped, done but refunded":          {models.SellIntentSwapped, true, LiFiStatusDone, LiFiSubstatusRefunded, time.Minute, models.PayStatusPending, 1},
		"swapped, done but partial":           {models.SellIntentSwapped, true, LiFiStatusDone, LiFiSubstatusPartial, time.Minute, models.PayStatusFailed, 1},
		"swapped, lifi pending, past expiry":  {models.SellIntentSwapped, true, LiFiStatusPending, "", time.Hour, models.PayStatusProcessing, 0},
		"swapped, done, no order, 1 h":        {models.SellIntentSwapped, true, LiFiStatusDone, LiFiSubstatusCompleted, time.Hour, models.PayStatusProcessing, 0},
		"swapped, done, no order, 3 h":        {models.SellIntentSwapped, true, LiFiStatusDone, "", 3 * time.Hour, models.PayStatusPending, 1},
		"swapped, pending 25 h":               {models.SellIntentSwapped, true, LiFiStatusPending, "", 25 * time.Hour, models.PayStatusFailed, 1},
		"swapped, not found, 10 min":          {models.SellIntentSwapped, true, "NOT_FOUND", "", 10 * time.Minute, models.PayStatusProcessing, 0},
		"swapped, not found, 31 min":          {models.SellIntentSwapped, true, "NOT_FOUND", "", 31 * time.Minute, models.PayStatusPending, 1},
		"failed intent, no hash":              {models.SellIntentFailed, false, "", "", time.Minute, models.PayStatusPending, 0},
		"failed intent with a swap, done 3 h": {models.SellIntentFailed, true, LiFiStatusDone, "", 3 * time.Hour, models.PayStatusPending, 0},
		"failed intent with a swap, pending":  {models.SellIntentFailed, true, LiFiStatusPending, "", time.Hour, models.PayStatusProcessing, 0},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPayCheckoutRig(t)
			in := r.startConverted(t)
			upd := map[string]any{"status": tc.status}
			if tc.swapHash {
				upd["swap_tx_hash"] = hash
			}
			r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(upd)
			if tc.lifi != "" {
				r.intents.status = &LiFiTransferStatus{Status: tc.lifi, Substatus: tc.sub}
			}
			r.pay.now = func() time.Time { return payTestNow.Add(tc.after) }
			r.pay.ReconcileTick(context.Background())
			got := r.get(t)
			released := tc.want == models.PayStatusPending && (got.Status == models.PayStatusPending || got.Status == models.PayStatusExpired)
			if got.Status != tc.want && !released {
				t.Fatalf("payment %s, want %s", got.Status, tc.want)
			}
			if released && (got.SellIntentID != nil || got.PayerUserID != nil) {
				t.Fatalf("a released payment must drop its attempt: %+v", got)
			}
			if r.intents.abandoned != tc.abandons {
				t.Fatalf("abandoned %d, want %d", r.intents.abandoned, tc.abandons)
			}
		})
	}
}

func TestPayReconcile_AnOrderedIntentHandsOverToItsOrder(t *testing.T) {
	r := newPayCheckoutRig(t)
	in := r.startConverted(t)
	orderID := uuid.New()
	r.pay.db.Create(&models.PaycrestOrderRecord{ID: orderID, UserID: r.alice.ID, Direction: models.PaycrestDirectionSell,
		Status: models.PaycrestStatusSettled, Reference: "PCS-ordered"})
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"status": models.SellIntentOrdered, "paycrest_order_id": orderID})
	r.pay.ReconcileTick(context.Background()) // tick 1: hand over
	if got := r.get(t); got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil || *got.PaycrestOrderID != orderID || got.SellIntentID != nil {
		t.Fatalf("after tick 1: %+v", got)
	}
	r.pay.ReconcileTick(context.Background()) // tick 2: settle
	if got := r.get(t); got.Status != models.PayStatusSucceeded || got.SettlementRef != orderID.String() || got.SellIntentID != nil {
		t.Fatalf("payment = %+v", got)
	}
}

// A lock with no attempt behind it — the customer locked, then the app died
// before an order or intent existed — must not hold the payment forever.
func TestPayReconcile_ALockWithNoAttemptIsReleasedAfterTenMinutes(t *testing.T) {
	lock := func(t *testing.T) *payCheckoutRig {
		r := newPayCheckoutRig(t)
		if _, fresh, err := r.pay.Lock(context.Background(), r.alice, r.p.ID, true); err != nil || !fresh {
			t.Fatalf("lock: %v fresh=%v", err, fresh)
		}
		return r
	}
	at := func(r *payCheckoutRig, d time.Duration) *models.PayPayment {
		r.pay.now = func() time.Time { return payTestNow.Add(d) }
		if err := r.pay.ReconcileTick(context.Background()); err != nil {
			t.Fatal(err)
		}
		return r.get(t)
	}
	t.Run("fresh lock is left alone", func(t *testing.T) {
		r := lock(t)
		if got := at(r, 9*time.Minute); got.Status != models.PayStatusProcessing || got.PayerUserID == nil {
			t.Fatalf("payment = %+v", got)
		}
	})
	t.Run("an old lock is released and can be taken again", func(t *testing.T) {
		r := lock(t)
		got := at(r, 11*time.Minute)
		if got.Status != models.PayStatusPending || got.PayerUserID != nil || got.LockedAt != nil {
			t.Fatalf("payment = %+v", got)
		}
		if _, err := r.direct(t); err != nil {
			t.Fatalf("retry after the release: %v", err)
		}
	})
	t.Run("a linked attempt is not this rule's business", func(t *testing.T) {
		r := newPayCheckoutRig(t)
		r.seedOrder(t, models.PaycrestStatusAwaitingDeposit)
		if got := at(r, 11*time.Minute); got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil {
			t.Fatalf("payment = %+v", got)
		}
	})
}

// ---- Postgres (skipped without DATABASE_URL) ----

func newPayCheckoutPostgresRig(t *testing.T) *payCheckoutRig {
	t.Helper()
	db := newITPostgresDB(t, &models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{}, &models.PayAuditEvent{},
		&models.User{}, &models.PaycrestOrderRecord{}, &models.RampSellIntent{}, &models.GasSponsorship{})
	now := func() time.Time { return payTestNow }
	merchants := NewPayMerchantService(db, nil, nil, []byte(strings.Repeat("k", 32)), now)
	pay := NewPayPaymentService(db, merchants, 100, "https://tsaconnectworld.com/pay", now)
	m := seedPayMerchant(t, db)
	p, _, _ := pay.Create(context.Background(), m, models.PayModeLive, payIn())
	r := &payCheckoutRig{pay: pay, p: p, m: m, alice: verifiedPGUser(t, db),
		orders: &fakePayOrders{deposit: big.NewInt(33_500000)}, intents: &fakePayIntents{}, quoter: &fakePayQuoter{}}
	r.svc = NewPayCheckoutService(pay, r.orders, r.intents, r.quoter, true)
	pay.SetIntents(r.intents)
	return r
}

func verifiedPGUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	id := uuid.New()
	u := &models.User{ID: id, Name: "Ada Okafor", Username: "u" + id.String()[:8], Email: id.String() + "@test.com", Password: "x",
		WalletAddress: "0x00000000000000000000000000000000000a11ce", VerificationStatus: models.VerificationStatusVerified,
		VerifiedLegalName: "Ada Okafor"}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func TestPayDirect_ConcurrentStartsOpenOneOrderOnPostgres(t *testing.T) {
	r := newPayCheckoutPostgresRig(t)
	bob := verifiedPGUser(t, r.pay.db)
	var wg sync.WaitGroup
	var mu sync.Mutex
	opened := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		u := r.alice
		if i%2 == 1 {
			u = bob // two customers and repeat taps by each, all at once
		}
		go func() {
			defer wg.Done()
			if _, err := r.svc.PayDirect(context.Background(), u, r.p.ID, PayDirectInput{ChainKey: "base", Token: "USDC"}); err == nil {
				mu.Lock()
				opened++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	got := r.get(t)
	if opened != 1 || r.orders.calls != 1 || got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil {
		t.Fatalf("opened %d order calls %d payment %+v", opened, r.orders.calls, got)
	}
}

func TestPayReconcile_SweepsEveryProcessingPaymentAcrossBatchesOnPostgres(t *testing.T) {
	r := newPayCheckoutPostgresRig(t)
	ctx := context.Background()
	const n = 5
	var payments []*models.PayPayment
	for i := 0; i < n; i++ {
		in := payIn()
		in.Reference = fmt.Sprintf("BATCH-%d", i)
		p, _, err := r.pay.Create(ctx, r.m, models.PayModeLive, in)
		if err != nil {
			t.Fatal(err)
		}
		payer := verifiedPGUser(t, r.pay.db)
		order, err := r.svc.PayDirect(ctx, payer, p.ID, PayDirectInput{ChainKey: "base", Token: "USDC"})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.pay.db.Create(&models.PaycrestOrderRecord{ID: order.ID, UserID: payer.ID, Direction: models.PaycrestDirectionSell,
			Status: models.PaycrestStatusSettled, Reference: "PCS-" + order.ID.String()[:8]}).Error; err != nil {
			t.Fatal(err)
		}
		payments = append(payments, p)
	}
	// A batch of two forces the id-ordered paging to carry across three pages.
	if err := r.pay.reconcileProcessing(ctx, 2); err != nil {
		t.Fatal(err)
	}
	for _, p := range payments {
		if got, _ := r.pay.Get(ctx, p.ID); got.Status != models.PayStatusSucceeded {
			t.Fatalf("payment %s left %s", p.Reference, got.Status)
		}
	}
}

func TestPayReconcile_HandsAnOrderedIntentOverAndSettlesOnPostgres(t *testing.T) {
	r := newPayCheckoutPostgresRig(t)
	ctx := context.Background()
	in, _, err := r.svc.PayConverted(ctx, r.alice, r.p.ID, PayConvertedInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: r.alice.WalletAddress})
	if err != nil {
		t.Fatal(err)
	}
	orderID := uuid.New()
	row := *in
	row.UserID, row.WalletAddress, row.SourceAddress, row.SourceChainID = r.alice.ID, r.alice.WalletAddress, r.alice.WalletAddress, 56
	row.SourceTokenAddress, row.SourceAmount, row.FiatCode, row.Status, row.PaycrestOrderID = evmNativeAddress, "1", "NGN", models.SellIntentOrdered, &orderID
	if err := r.pay.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.pay.db.Create(&models.PaycrestOrderRecord{ID: orderID, UserID: r.alice.ID, Direction: models.PaycrestDirectionSell,
		Status: models.PaycrestStatusSettled, Reference: "PCS-pg-ordered"}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := r.pay.ReconcileTick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.get(t); got.Status != models.PayStatusSucceeded || got.SettlementRef != orderID.String() || got.SellIntentID != nil {
		t.Fatalf("payment = %+v", got)
	}
}
