package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ── doubles ──────────────────────────────────────────────────────────────

type fakeBillVendor struct {
	vendCalls   int
	queryCalls  int
	lastRequest string
	vendResult  BillVendResult
	vendErr     error
	queryResult BillVendResult
	queryErr    error
	verifyName  string
	verifyErr   error
	plansErr    error // injected into DataPlans (vend-time catalog outage)
}

func (f *fakeBillVendor) record(requestID string) { f.vendCalls++; f.lastRequest = requestID }
func (f *fakeBillVendor) VendAirtime(_ context.Context, _, _ string, _ int64, requestID string) (BillVendResult, error) {
	f.record(requestID)
	return f.vendResult, f.vendErr
}
func (f *fakeBillVendor) VendData(_ context.Context, _ string, _ BillPlan, _, requestID string) (BillVendResult, error) {
	f.record(requestID)
	return f.vendResult, f.vendErr
}
func (f *fakeBillVendor) VendElectricity(_ context.Context, _, _, _ string, _ int64, requestID string) (BillVendResult, error) {
	f.record(requestID)
	return f.vendResult, f.vendErr
}
func (f *fakeBillVendor) VendCableTV(_ context.Context, _, _, _, requestID string) (BillVendResult, error) {
	f.record(requestID)
	return f.vendResult, f.vendErr
}
func (f *fakeBillVendor) VendBetting(_ context.Context, _, _ string, _ int64, requestID string) (BillVendResult, error) {
	f.record(requestID)
	return f.vendResult, f.vendErr
}
func (f *fakeBillVendor) VendEpin(_ context.Context, _, _, _, requestID string) (BillVendResult, error) {
	f.record(requestID)
	return f.vendResult, f.vendErr
}
func (f *fakeBillVendor) QueryOrder(context.Context, string) (BillVendResult, error) {
	f.queryCalls++
	return f.queryResult, f.queryErr
}
func (f *fakeBillVendor) VerifyElectricity(context.Context, string, string) (string, error) {
	return f.verifyName, f.verifyErr
}
func (f *fakeBillVendor) VerifyCableTV(context.Context, string, string) (string, error) {
	return f.verifyName, f.verifyErr
}
func (f *fakeBillVendor) AirtimeNetworks(context.Context) ([]BillProvider, error) {
	return []BillProvider{{Code: "01", Name: "MTN", MinAmount: 50, MaxAmount: 200000}}, nil
}
func (f *fakeBillVendor) DataNetworks(context.Context) ([]BillProvider, error) {
	return []BillProvider{{Code: "01", Name: "MTN"}}, nil
}
func (f *fakeBillVendor) DataPlans(context.Context, string) ([]BillPlan, error) {
	if f.plansErr != nil {
		return nil, f.plansErr
	}
	return []BillPlan{{Code: "2", ProductID: "500", Name: "500 MB - Weekly (SME)", CostNGN: 307}}, nil
}
func (f *fakeBillVendor) Discos(context.Context) ([]BillProvider, error) {
	return []BillProvider{{Code: "01", Name: "Eko Electric - EKEDC", MinAmount: 1000, MaxAmount: 200000}}, nil
}
func (f *fakeBillVendor) CableProviders(context.Context) ([]BillProvider, error) {
	return []BillProvider{{Code: "dstv", Name: "DStv"}}, nil
}
func (f *fakeBillVendor) CablePackages(context.Context, string) ([]BillPlan, error) {
	return []BillPlan{{Code: "dstv-padi", Name: "DStv Padi N4,400", AmountNGN: 4400, CostNGN: 4356}}, nil
}
func (f *fakeBillVendor) BettingCompanies(context.Context) ([]BillProvider, error) {
	return []BillProvider{{Code: "product-nairabet", Name: "Nairabet", MinAmount: 100, MaxAmount: 50000}}, nil
}
func (f *fakeBillVendor) EpinPlans(context.Context, string) ([]BillPlan, error) {
	return []BillPlan{{Code: "waecdirect", Name: "WAEC Result Checker PIN", AmountNGN: 5350}}, nil
}

type fakeBillPayer struct {
	db            *gorm.DB
	debitErr      error
	settleErr     error
	reverseErr    error
	debits        int
	settles       int
	reverses      int
	disabled      bool
	leaveDebiting bool // simulate broadcast-without-verify
	leaveFailed   bool // simulate a failed broadcast (row persisted as failed)

	resolveDebitingTo  string // ResolveDebiting outcome ("" = unchanged)
	resolveDebitingWon *bool  // override: did this call win the transition
	resolveDebitingErr error
	lateDebit          bool               // AdoptLateDebit finds a debit on-chain (every order)
	lateDebitOrders    map[uuid.UUID]bool // or only these orders
	stuck              []models.BillPayment
	deepAudits         int
}

func (f *fakeBillPayer) ResolveDebiting(ctx context.Context, orderID uuid.UUID) (string, bool, error) {
	if f.resolveDebitingErr != nil {
		return models.BillPayStatusDebiting, false, f.resolveDebitingErr
	}
	var pay models.BillPayment
	if err := f.db.WithContext(ctx).Where("order_id = ?", orderID).First(&pay).Error; err != nil {
		return "", false, err
	}
	if pay.Status != models.BillPayStatusDebiting || f.resolveDebitingTo == "" {
		return pay.Status, false, nil
	}
	f.db.WithContext(ctx).Model(&pay).Update("status", f.resolveDebitingTo)
	won := f.resolveDebitingTo != models.BillPayStatusDebiting
	if f.resolveDebitingWon != nil {
		won = *f.resolveDebitingWon
	}
	return f.resolveDebitingTo, won, nil
}
func (f *fakeBillPayer) AdoptLateDebit(ctx context.Context, orderID uuid.UUID) (bool, error) {
	if !f.lateDebit && !f.lateDebitOrders[orderID] {
		return false, nil
	}
	res := f.db.WithContext(ctx).Model(&models.BillPayment{}).
		Where("order_id = ? AND status = ?", orderID, models.BillPayStatusFailed).
		Update("status", models.BillPayStatusDebited)
	return res.RowsAffected == 1, res.Error
}
func (f *fakeBillPayer) AlertLateDebitsBeyondWindow(context.Context) (int, error) {
	f.deepAudits++
	return 0, nil
}
func (f *fakeBillPayer) StuckPayments(context.Context, time.Duration, int) ([]models.BillPayment, error) {
	return f.stuck, nil
}

func (f *fakeBillPayer) IsEnabled() bool { return !f.disabled }
func (f *fakeBillPayer) Debit(ctx context.Context, in DebitInput) (*models.BillPayment, error) {
	f.debits++
	if f.debitErr != nil {
		status := ""
		if f.leaveDebiting {
			status = models.BillPayStatusDebiting
		} else if f.leaveFailed {
			status = models.BillPayStatusFailed
		}
		if status != "" {
			var existing models.BillPayment
			if f.db.WithContext(ctx).Where("order_id = ?", in.OrderID).First(&existing).Error != nil {
				pay := &models.BillPayment{ID: uuid.New(), UserID: in.UserID, OrderID: in.OrderID,
					WalletAddress: in.WalletAddress, McgpAmount: in.McgpAmount.String(),
					NgnKobo: "0", RateKobo: "0", DebitRef: "0x" + in.OrderID.String(),
					Status: status}
				f.db.WithContext(ctx).Create(pay)
			}
		}
		return nil, f.debitErr
	}
	pay := &models.BillPayment{ID: uuid.New(), UserID: in.UserID, OrderID: in.OrderID,
		WalletAddress: in.WalletAddress, McgpAmount: in.McgpAmount.String(),
		NgnKobo: in.NgnKobo.String(), RateKobo: in.RateKobo.String(),
		DebitRef: "0x" + in.OrderID.String(), Status: models.BillPayStatusDebited}
	f.db.WithContext(ctx).Create(pay)
	return pay, nil
}
func (f *fakeBillPayer) resolve(ctx context.Context, orderID uuid.UUID, status string) (*models.BillPayment, error) {
	var pay models.BillPayment
	if err := f.db.WithContext(ctx).Where("order_id = ?", orderID).First(&pay).Error; err != nil {
		return nil, err
	}
	pay.Status = status
	f.db.WithContext(ctx).Save(&pay)
	return &pay, nil
}
func (f *fakeBillPayer) Settle(ctx context.Context, orderID uuid.UUID) (*models.BillPayment, error) {
	f.settles++
	if f.settleErr != nil {
		return nil, f.settleErr
	}
	return f.resolve(ctx, orderID, models.BillPayStatusSettled)
}
func (f *fakeBillPayer) Reverse(ctx context.Context, orderID uuid.UUID) (*models.BillPayment, error) {
	f.reverses++
	if f.reverseErr != nil {
		return nil, f.reverseErr
	}
	return f.resolve(ctx, orderID, models.BillPayStatusReversed)
}

type fakeBillPrice struct {
	usdcPerMcgp *big.Int
	err         error
}

func (f *fakeBillPrice) Quote(kind uint8, amount *big.Int) (*big.Int, error) {
	if f.err != nil {
		return nil, f.err
	}
	if kind != PriceKindPremiumSell {
		return nil, fmt.Errorf("unexpected price kind %d", kind)
	}
	return f.usdcPerMcgp, nil
}

type fakeBillFx struct {
	rate  float64
	stale bool
	err   error
}

func (f *fakeBillFx) NgnPerUsd(context.Context) (float64, bool, error) {
	return f.rate, f.stale, f.err
}

// ── rig ──────────────────────────────────────────────────────────────────

type billRig struct {
	svc    *BillService
	vendor *fakeBillVendor
	payer  *fakeBillPayer
	db     *gorm.DB
	userID uuid.UUID
}

func newBillServiceRig(t *testing.T) *billRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, ddl := range []string{
		`CREATE TABLE bill_orders (
			id TEXT PRIMARY KEY, user_id TEXT, category TEXT, provider TEXT,
			provider_name TEXT, customer_id TEXT, customer_name TEXT, phone_number TEXT,
			plan_ref TEXT, plan_name TEXT, meter_type TEXT, ngn_amount INTEGER,
			vend_ngn_amount INTEGER, mcgp_amount TEXT, rate_kobo TEXT,
			vendor_order_id TEXT, vendor_status_code INTEGER, delivery_payload TEXT,
			failure_reason TEXT, status TEXT, quote_expires_at DATETIME,
			claim_seq INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE bill_payments (
			id TEXT PRIMARY KEY, user_id TEXT, order_id TEXT UNIQUE, purpose TEXT NOT NULL DEFAULT 'bill', wallet_address TEXT,
			mcgp_amount TEXT, ngn_kobo TEXT, rate_kobo TEXT, from_bought TEXT,
			debit_ref TEXT UNIQUE, status TEXT, tx_hash TEXT, resolve_tx_hash TEXT,
			failure_reason TEXT, created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE bill_reports (
			id TEXT PRIMARY KEY, order_id TEXT UNIQUE, user_id TEXT, reason TEXT,
			status TEXT, resolved_by TEXT, resolution TEXT, resolved_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE users (
			id TEXT PRIMARY KEY, name TEXT, username TEXT, email TEXT,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT
		)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	vendor := &fakeBillVendor{vendResult: BillVendResult{OrderID: "900", StatusCode: 200, Status: "ORDER_COMPLETED"}}
	payer := &fakeBillPayer{db: db}
	// $0.018/MCGP at ₦1500/$ ⇒ 2700 kobo (₦27) per MCGP.
	price := &fakeBillPrice{usdcPerMcgp: big.NewInt(18_000)}
	fx := &fakeBillFx{rate: 1500}
	svc := NewBillService(db, vendor, payer, price, fx, 300, 5*time.Minute)
	// Reporting a bill order writes to the shared disputes table now, so the
	// bills rig wires the same collaborator main.go does. These tests are what
	// prove the old report API still behaves identically over the new storage.
	if err := db.AutoMigrate(&models.Dispute{}); err != nil {
		t.Fatalf("migrate disputes: %v", err)
	}
	// The daily late-debit audit claims its run in this table (claimDailyRun).
	if err := db.AutoMigrate(&models.JobRunMarker{}); err != nil {
		t.Fatalf("migrate job run markers: %v", err)
	}
	disputes := NewDisputeService(db, nil)
	disputes.RegisterSubject(models.DisputeSubjectBillOrder, BillDisputeSubject(db))
	svc.SetDisputes(disputes)
	return &billRig{svc: svc, vendor: vendor, payer: payer, db: db, userID: uuid.New()}
}

func (r *billRig) airtimeQuote(t *testing.T) *BillQuoteResult {
	t.Helper()
	q, err := r.svc.Quote(context.Background(), BillQuoteInput{
		UserID: r.userID, Category: models.BillCategoryAirtime, Provider: "01",
		PhoneNumber: "08030001111", NgnAmount: 1000,
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	return q
}

// ── quote ────────────────────────────────────────────────────────────────

func TestBillQuoteAirtimeMath(t *testing.T) {
	r := newBillServiceRig(t)
	q := r.airtimeQuote(t)
	// ₦1000 = 100000 kobo at 2700 kobo/MCGP ⇒ 37.037… MCGP, rounded UP.
	if q.RateKobo != "2700" {
		t.Fatalf("rateKobo = %s, want 2700", q.RateKobo)
	}
	want, _ := new(big.Int).SetString("37037037037037037038", 10)
	got, _ := new(big.Int).SetString(q.McgpAmount, 10)
	if got.Cmp(want) != 0 {
		t.Fatalf("mcgpAmount = %s, want %s (rounded up)", got, want)
	}
	if q.Order.Status != models.BillStatusQuoted || q.Order.VendNgnAmount != 1000 {
		t.Fatalf("order staged wrong: %+v", q.Order)
	}
}

func TestBillQuoteDataIsServerPriced(t *testing.T) {
	r := newBillServiceRig(t)
	q, err := r.svc.Quote(context.Background(), BillQuoteInput{
		UserID: r.userID, Category: models.BillCategoryData, Provider: "01",
		PhoneNumber: "08030001111", PlanRef: "2",
		NgnAmount: 5, // client-supplied price must be ignored
	})
	if err != nil {
		t.Fatal(err)
	}
	// cost 307 + 3% markup = 316.21 → ₦317 retail; vend at cost.
	if q.Order.NgnAmount != 317 || q.Order.VendNgnAmount != 307 {
		t.Fatalf("data pricing wrong: user=%d vend=%d", q.Order.NgnAmount, q.Order.VendNgnAmount)
	}
	if q.Order.PlanName == "" {
		t.Fatal("plan name not resolved")
	}
}

func TestBillQuoteCableFaceValue(t *testing.T) {
	r := newBillServiceRig(t)
	q, err := r.svc.Quote(context.Background(), BillQuoteInput{
		UserID: r.userID, Category: models.BillCategoryCableTV, Provider: "dstv",
		CustomerID: "1234567890", PlanRef: "dstv-padi", NgnAmount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.Order.NgnAmount != 4400 {
		t.Fatalf("cable must charge face value, got %d", q.Order.NgnAmount)
	}
}

func TestBillQuoteValidation(t *testing.T) {
	r := newBillServiceRig(t)
	cases := []BillQuoteInput{
		{Category: "unknown", Provider: "01", NgnAmount: 100},
		{Category: models.BillCategoryAirtime, Provider: "99", PhoneNumber: "08030001111", NgnAmount: 100}, // unknown provider
		{Category: models.BillCategoryAirtime, Provider: "01", PhoneNumber: "0803", NgnAmount: 100},        // bad phone
		{Category: models.BillCategoryAirtime, Provider: "01", PhoneNumber: "08030001111", NgnAmount: 10},  // below min
		{Category: models.BillCategoryAirtime, Provider: "01", PhoneNumber: "08030001111", NgnAmount: 1e9}, // above max
		{Category: models.BillCategoryElectricity, Provider: "01", CustomerID: "123", MeterType: "wrong", NgnAmount: 2000},
		{Category: models.BillCategoryEpin, Provider: "waec", PhoneNumber: "08030001111", PlanRef: "nope"}, // unknown plan
	}
	for i, in := range cases {
		in.UserID = r.userID
		if _, err := r.svc.Quote(context.Background(), in); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestBillQuoteStaleFxRejected(t *testing.T) {
	r := newBillServiceRig(t)
	r.svc.fx = &fakeBillFx{rate: 1500, stale: true}
	_, err := r.svc.Quote(context.Background(), BillQuoteInput{
		UserID: r.userID, Category: models.BillCategoryAirtime, Provider: "01",
		PhoneNumber: "08030001111", NgnAmount: 1000,
	})
	if !errors.Is(err, ErrBillRateStale) {
		t.Fatalf("want ErrBillRateStale, got %v", err)
	}
}

// ── pay → vend ───────────────────────────────────────────────────────────

func TestBillPayHappyPath(t *testing.T) {
	r := newBillServiceRig(t)
	r.vendor.vendResult = BillVendResult{OrderID: "900", StatusCode: 200, Status: "ORDER_COMPLETED", Delivery: "TOKEN-1"}
	q := r.airtimeQuote(t)
	o, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != models.BillStatusDelivered {
		t.Fatalf("status = %s, want delivered", o.Status)
	}
	if o.DeliveryPayload != "TOKEN-1" || o.VendorOrderID != "900" {
		t.Fatalf("delivery not recorded: %+v", o)
	}
	if r.payer.debits != 1 || r.payer.settles != 1 || r.payer.reverses != 0 {
		t.Fatalf("payer calls: %+v", r.payer)
	}
	if r.vendor.lastRequest != q.Order.ID.String() {
		t.Fatalf("vend RequestID must be the order id, got %q", r.vendor.lastRequest)
	}
	// Idempotent replay.
	o2, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID)
	if err != nil || o2.Status != models.BillStatusDelivered || r.payer.debits != 1 {
		t.Fatalf("replay must be a no-op: %v %+v debits=%d", err, o2, r.payer.debits)
	}
}

func TestBillPayVendTerminalFailureReverses(t *testing.T) {
	r := newBillServiceRig(t)
	r.vendor.vendResult = BillVendResult{OrderID: "901", StatusCode: 500, Status: "ORDER_CANCELLED", Remark: "no supply"}
	q := r.airtimeQuote(t)
	o, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != models.BillStatusRefunded {
		t.Fatalf("status = %s, want refunded", o.Status)
	}
	if r.payer.reverses != 1 || r.payer.settles != 0 {
		t.Fatalf("payer calls: settles=%d reverses=%d", r.payer.settles, r.payer.reverses)
	}
}

func TestBillPayFloatLowHidesPlatformError(t *testing.T) {
	r := newBillServiceRig(t)
	r.vendor.vendResult = BillVendResult{StatusCode: 417, Status: "ORDER_ERROR", Remark: "INSUFFICIENT_BALANCE"}
	q := r.airtimeQuote(t)
	o, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != models.BillStatusRefunded {
		t.Fatalf("status = %s, want refunded", o.Status)
	}
	if strings.Contains(o.FailureReason, "INSUFFICIENT") {
		t.Fatalf("float exhaustion must not leak to the user: %q", o.FailureReason)
	}
}

func TestBillPay201StaysVending(t *testing.T) {
	r := newBillServiceRig(t)
	// 201 is ORDER_COMPLETED "Network Unresponsive" — NOT success.
	r.vendor.vendResult = BillVendResult{OrderID: "902", StatusCode: 201, Status: "ORDER_COMPLETED"}
	q := r.airtimeQuote(t)
	o, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != models.BillStatusVending {
		t.Fatalf("status = %s, want vending", o.Status)
	}
	if r.payer.settles != 0 && r.payer.reverses != 0 {
		t.Fatal("201 must neither settle nor reverse")
	}
}

func TestBillPayVendTransportErrorParksOrder(t *testing.T) {
	r := newBillServiceRig(t)
	r.vendor.vendErr = fmt.Errorf("connection reset")
	q := r.airtimeQuote(t)
	o, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != models.BillStatusVending {
		t.Fatalf("unknown vend outcome must park in vending, got %s", o.Status)
	}
	if r.payer.reverses != 0 {
		t.Fatal("must not reverse on an unknown outcome — the vend may have succeeded")
	}
}

func TestBillPayDebitFailureReopensQuote(t *testing.T) {
	r := newBillServiceRig(t)
	r.payer.debitErr = ErrBillPayInsufficient
	q := r.airtimeQuote(t)
	_, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID)
	if !errors.Is(err, ErrBillPayInsufficient) {
		t.Fatalf("want ErrBillPayInsufficient, got %v", err)
	}
	var o models.BillOrder
	r.db.First(&o, "id = ?", q.Order.ID)
	if o.Status != models.BillStatusQuoted {
		t.Fatalf("failed debit must reopen the quote, got %s", o.Status)
	}
	if r.vendor.vendCalls != 0 {
		t.Fatal("must not vend without a confirmed debit")
	}
}

func TestBillPayUnverifiedDebitHolds(t *testing.T) {
	r := newBillServiceRig(t)
	r.payer.debitErr = fmt.Errorf("verify: receipt not found")
	r.payer.leaveDebiting = true
	q := r.airtimeQuote(t)
	if _, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID); err == nil {
		t.Fatal("want error")
	}
	var o models.BillOrder
	r.db.First(&o, "id = ?", q.Order.ID)
	if o.Status != models.BillStatusPaying {
		t.Fatalf("unverified debit must hold the order for the reconciler, got %s", o.Status)
	}
}

func TestBillPayFailedDebitIsTerminalNotReplayable(t *testing.T) {
	// THE critical case: a broadcast-failed debit leaves a `failed` payment
	// row. Replaying Pay on that order must never turn into a free vend — the
	// order goes terminal and a retry needs a fresh order (fresh on-chain ref).
	r := newBillServiceRig(t)
	r.payer.debitErr = fmt.Errorf("broadcast: nonce too low")
	r.payer.leaveFailed = true
	q := r.airtimeQuote(t)
	if _, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID); err == nil {
		t.Fatal("want error from failed debit")
	}
	var o models.BillOrder
	r.db.First(&o, "id = ?", q.Order.ID)
	if o.Status != models.BillStatusFailed {
		t.Fatalf("failed debit must terminate the order, got %s", o.Status)
	}
	// Replay: must not vend, must not deliver.
	r.payer.debitErr = nil // even if a fresh debit would now "succeed"
	if _, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID); !errors.Is(err, ErrBillBadState) {
		t.Fatalf("replaying a failed order must be rejected, got %v", err)
	}
	if r.vendor.vendCalls != 0 {
		t.Fatalf("vendCalls = %d — a failed payment must never vend", r.vendor.vendCalls)
	}
}

func TestBillPaymentDebitNeverReplaysFailedRow(t *testing.T) {
	// Companion guard at the payment-service layer: an existing `failed` row
	// must not be returned as a successful debit.
	svc, _ := newBillRig(t)
	orderID := uuid.New()
	in := debitInput(orderID, 5)
	pay := &models.BillPayment{ID: uuid.New(), UserID: in.UserID, OrderID: orderID,
		WalletAddress: billWallet, McgpAmount: "5", NgnKobo: "0", RateKobo: "0",
		DebitRef: "0xfailedref", Status: models.BillPayStatusFailed}
	if err := svc.db.Create(pay).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Debit(context.Background(), in); !errors.Is(err, ErrBillPayBadState) {
		t.Fatalf("want ErrBillPayBadState for failed row, got %v", err)
	}
}

func TestBillPayReplayWhileInProgress(t *testing.T) {
	// An order already in `paying` (another request owns it) replays as-is
	// instead of double-driving the flow.
	r := newBillServiceRig(t)
	q := r.airtimeQuote(t)
	r.db.Model(&models.BillOrder{}).Where("id = ?", q.Order.ID).
		Update("status", models.BillStatusPaying)
	o, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID)
	if err != nil || o.Status != models.BillStatusPaying {
		t.Fatalf("in-flight order must replay untouched: %v %+v", err, o)
	}
	if r.payer.debits != 0 || r.vendor.vendCalls != 0 {
		t.Fatal("replay must neither debit nor vend")
	}
}

func TestBillVendDataTransientCatalogStaysVending(t *testing.T) {
	// A transient catalog failure at vend time must NOT refund — the plan may
	// still be perfectly vendable; the reconciler retries.
	r := newBillServiceRig(t)
	q, err := r.svc.Quote(context.Background(), BillQuoteInput{
		UserID: r.userID, Category: models.BillCategoryData, Provider: "01",
		PhoneNumber: "08030001111", PlanRef: "2",
	})
	if err != nil {
		t.Fatalf("quote should pre-date the outage: %v", err)
	}
	// Outage begins between quote and vend.
	r.vendor.plansErr = fmt.Errorf("catalog timeout")
	o, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != models.BillStatusVending {
		t.Fatalf("status = %s, want vending (not refunded)", o.Status)
	}
	if r.payer.reverses != 0 {
		t.Fatal("transient catalog failure must not reverse the payment")
	}
}

func TestBillValidateHardening(t *testing.T) {
	r := newBillServiceRig(t)
	r.vendor.verifyName = "ADA OBI"
	// Unknown provider is rejected before the vendor is called.
	if _, err := r.svc.Validate(context.Background(), models.BillCategoryElectricity, "99", "123456789"); err == nil {
		t.Fatal("unknown provider must fail")
	}
	// Bad customer ids are rejected before the vendor is called.
	for _, id := range []string{"123", "abcdefgh", strings.Repeat("1", 30)} {
		if _, err := r.svc.Validate(context.Background(), models.BillCategoryElectricity, "01", id); err == nil {
			t.Fatalf("customer id %q must be rejected", id)
		}
	}
	if _, err := r.svc.Validate(context.Background(), models.BillCategoryElectricity, "01", "123456789"); err != nil {
		t.Fatalf("valid lookup: %v", err)
	}
}

func TestBillQuoteOpenQuoteCap(t *testing.T) {
	r := newBillServiceRig(t)
	for i := 0; i < 20; i++ {
		r.airtimeQuote(t)
	}
	_, err := r.svc.Quote(context.Background(), BillQuoteInput{
		UserID: r.userID, Category: models.BillCategoryAirtime, Provider: "01",
		PhoneNumber: "08030001111", NgnAmount: 1000,
	})
	if !errors.Is(err, ErrBillBadRequest) {
		t.Fatalf("21st open quote must be rejected, got %v", err)
	}
}

func TestTruncate512RuneSafe(t *testing.T) {
	s := strings.Repeat("é", 600) // 2 bytes per rune
	out := truncate512(s)
	for _, r := range out {
		if r == '�' {
			t.Fatal("truncate512 split a multi-byte rune")
		}
	}
	if len([]rune(out)) > 512 {
		t.Fatalf("rune length %d > 512", len([]rune(out)))
	}
}

func TestBillPayExpiredQuote(t *testing.T) {
	r := newBillServiceRig(t)
	q := r.airtimeQuote(t)
	r.db.Model(&models.BillOrder{}).Where("id = ?", q.Order.ID).
		Update("quote_expires_at", time.Now().Add(-time.Minute))
	if _, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID); !errors.Is(err, ErrBillQuoteExpired) {
		t.Fatalf("want ErrBillQuoteExpired, got %v", err)
	}
	if r.payer.debits != 0 {
		t.Fatal("expired quote must not debit")
	}
}

func TestBillPayWrongUser(t *testing.T) {
	r := newBillServiceRig(t)
	q := r.airtimeQuote(t)
	if _, err := r.svc.Pay(context.Background(), uuid.New(), billWallet, q.Order.ID); !errors.Is(err, ErrBillNotFound) {
		t.Fatalf("cross-user pay must 404, got %v", err)
	}
}

// ── reconcile ────────────────────────────────────────────────────────────

func backdate(t *testing.T, db *gorm.DB, id uuid.UUID, status string, age time.Duration) {
	t.Helper()
	if err := db.Exec(`UPDATE bill_orders SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now().Add(-age), id).Error; err != nil {
		t.Fatal(err)
	}
}

func TestReconcileVendingSettles(t *testing.T) {
	r := newBillServiceRig(t)
	r.vendor.vendResult = BillVendResult{OrderID: "903", StatusCode: 201, Status: "ORDER_COMPLETED"}
	q := r.airtimeQuote(t)
	if _, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID); err != nil {
		t.Fatal(err)
	}
	backdate(t, r.db, q.Order.ID, models.BillStatusVending, time.Hour)

	r.vendor.queryResult = BillVendResult{OrderID: "903", StatusCode: 200, Status: "ORDER_COMPLETED", Delivery: "PIN-9"}
	n, err := r.svc.Reconcile(context.Background(), 5*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("reconcile: n=%d err=%v", n, err)
	}
	var o models.BillOrder
	r.db.First(&o, "id = ?", q.Order.ID)
	if o.Status != models.BillStatusDelivered || o.DeliveryPayload != "PIN-9" {
		t.Fatalf("order after reconcile: %+v", o)
	}
	if r.payer.settles != 1 {
		t.Fatalf("settles = %d, want 1", r.payer.settles)
	}
}

func TestReconcileVendingReverses(t *testing.T) {
	r := newBillServiceRig(t)
	r.vendor.vendResult = BillVendResult{OrderID: "904", StatusCode: 300, Status: "ORDER_PROCESSED"}
	q := r.airtimeQuote(t)
	if _, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID); err != nil {
		t.Fatal(err)
	}
	backdate(t, r.db, q.Order.ID, models.BillStatusVending, time.Hour)

	r.vendor.queryResult = BillVendResult{OrderID: "904", StatusCode: 510, Status: "ORDER_CANCELLED"}
	if _, err := r.svc.Reconcile(context.Background(), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	var o models.BillOrder
	r.db.First(&o, "id = ?", q.Order.ID)
	if o.Status != models.BillStatusRefunded {
		t.Fatalf("status = %s, want refunded", o.Status)
	}
	if r.payer.reverses != 1 {
		t.Fatalf("reverses = %d, want 1", r.payer.reverses)
	}
}

func TestReconcilePaidRevendsWhenQueryUnknown(t *testing.T) {
	r := newBillServiceRig(t)
	q := r.airtimeQuote(t)
	// Simulate: debit confirmed, then crash before the vend.
	pay := &models.BillPayment{ID: uuid.New(), UserID: r.userID, OrderID: q.Order.ID,
		WalletAddress: billWallet, McgpAmount: q.McgpAmount, NgnKobo: "100000",
		RateKobo: q.RateKobo, DebitRef: "0xref", Status: models.BillPayStatusDebited}
	r.db.Create(pay)
	backdate(t, r.db, q.Order.ID, models.BillStatusPaid, time.Hour)

	r.vendor.queryErr = fmt.Errorf("ORDER_NOT_FOUND")
	r.vendor.vendResult = BillVendResult{OrderID: "905", StatusCode: 200, Status: "ORDER_COMPLETED"}
	if _, err := r.svc.Reconcile(context.Background(), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	var o models.BillOrder
	r.db.First(&o, "id = ?", q.Order.ID)
	if o.Status != models.BillStatusDelivered {
		t.Fatalf("status = %s, want delivered after re-vend", o.Status)
	}
	if r.vendor.vendCalls != 1 {
		t.Fatalf("vendCalls = %d, want exactly 1 re-vend", r.vendor.vendCalls)
	}
}

func TestReconcileExpiresQuotes(t *testing.T) {
	r := newBillServiceRig(t)
	q := r.airtimeQuote(t)
	r.db.Exec(`UPDATE bill_orders SET quote_expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour), q.Order.ID)
	n, err := r.svc.Reconcile(context.Background(), 5*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var o models.BillOrder
	r.db.First(&o, "id = ?", q.Order.ID)
	if o.Status != models.BillStatusExpired {
		t.Fatalf("status = %s, want expired", o.Status)
	}
}

func TestReconcileRetriesDeferredSettle(t *testing.T) {
	r := newBillServiceRig(t)
	r.payer.settleErr = fmt.Errorf("rpc down")
	r.vendor.vendResult = BillVendResult{OrderID: "906", StatusCode: 200, Status: "ORDER_COMPLETED"}
	q := r.airtimeQuote(t)
	o, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID)
	if err != nil || o.Status != models.BillStatusDelivered {
		t.Fatalf("pay: %v %+v", err, o)
	}
	// Payment row is still `debited` — the settle was deferred.
	r.payer.settleErr = nil
	n, err := r.svc.Reconcile(context.Background(), 5*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var pay models.BillPayment
	r.db.First(&pay, "order_id = ?", q.Order.ID)
	if pay.Status != models.BillPayStatusSettled {
		t.Fatalf("payment = %s, want settled", pay.Status)
	}
}

// ── validate / catalogs ─────────────────────────────────────────────────

func TestBillValidate(t *testing.T) {
	r := newBillServiceRig(t)
	r.vendor.verifyName = "ADA OBI"
	name, err := r.svc.Validate(context.Background(), models.BillCategoryElectricity, "01", "123456")
	if err != nil || name != "ADA OBI" {
		t.Fatalf("electricity: %q %v", name, err)
	}
	if _, err := r.svc.Validate(context.Background(), models.BillCategoryAirtime, "01", "0803"); err == nil {
		t.Fatal("bad phone must fail")
	}
	if _, err := r.svc.Validate(context.Background(), models.BillCategoryAirtime, "01", "08030001111"); err != nil {
		t.Fatalf("good phone: %v", err)
	}
}

func TestBillPlansAppliesDataMarkup(t *testing.T) {
	r := newBillServiceRig(t)
	plans, err := r.svc.Plans(context.Background(), models.BillCategoryData, "01")
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans: %v err=%v", plans, err)
	}
	if plans[0].AmountNGN != 317 { // 307 + 3% rounded up
		t.Fatalf("retail = %d, want 317", plans[0].AmountNGN)
	}
}

func TestBillServiceDisabledWithoutPayer(t *testing.T) {
	r := newBillServiceRig(t)
	r.payer.disabled = true
	if r.svc.IsEnabled() {
		t.Fatal("service must be disabled when the payer is")
	}
	if _, err := r.svc.Quote(context.Background(), BillQuoteInput{}); !errors.Is(err, ErrBillsDisabled) {
		t.Fatalf("want ErrBillsDisabled, got %v", err)
	}
}

// A customer who already has their airtime must not wait out the reconciler's
// grace window: opening the order asks the vendor and settles it.
func TestGetOrderRefreshesVendingFromVendor(t *testing.T) {
	r := newBillServiceRig(t)
	r.vendor.vendResult = BillVendResult{OrderID: "905", StatusCode: 100, Status: "ORDER_RECEIVED"}
	q := r.airtimeQuote(t)
	if _, err := r.svc.Pay(context.Background(), r.userID, billWallet, q.Order.ID); err != nil {
		t.Fatal(err)
	}
	// Inside the throttle window nothing is asked.
	if o, _ := r.svc.GetOrder(context.Background(), r.userID, q.Order.ID); o.Status != models.BillStatusVending || r.vendor.queryCalls != 0 {
		t.Fatalf("fresh order: status=%s queries=%d", o.Status, r.vendor.queryCalls)
	}
	backdate(t, r.db, q.Order.ID, models.BillStatusVending, time.Minute)

	r.vendor.queryResult = BillVendResult{OrderID: "905", StatusCode: 200, Status: "ORDER_COMPLETED"}
	o, err := r.svc.GetOrder(context.Background(), r.userID, q.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != models.BillStatusDelivered || r.payer.settles != 1 {
		t.Fatalf("status=%s settles=%d, want delivered/1", o.Status, r.payer.settles)
	}
}
