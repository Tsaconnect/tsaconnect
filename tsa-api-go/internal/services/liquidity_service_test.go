package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
)

func newLiquidityRig(t *testing.T) (*poolRig, *LiquidityService) {
	t.Helper()
	r := newPoolRig(t)
	if err := r.db.Exec(`CREATE TABLE merchant_requests (id TEXT PRIMARY KEY, user_id TEXT NOT NULL,
		business_type TEXT NOT NULL, business_name TEXT, status TEXT NOT NULL DEFAULT 'pending',
		created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	createSettlementForwardTable(t, r.db)
	stock := NewPoolStock(r.svc.escrows, poolTestTokenAddr)
	return r, NewLiquidityService(r.db, NewPMService(r.db), r.svc, stock, fakePoolOpen(true), "BNB Smart Chain Testnet")
}

func TestLiquidityMe_StatusLadder(t *testing.T) {
	r, svc := newLiquidityRig(t)
	ctx := context.Background()
	if me, _ := svc.Me(ctx, uuid.New()); me.Status != LiquidityStatusNone || me.Wallet != "" || me.Limits.Max != "5000" {
		t.Fatalf("stranger = %+v", me)
	}
	applicant := uuid.New()
	r.db.Exec(`INSERT INTO merchant_requests (id, user_id, business_type, status, created_at) VALUES (?, ?, ?, 'pending', ?)`,
		uuid.New(), applicant, models.BusinessTypeLiquidityProvider, time.Now())
	if me, _ := svc.Me(ctx, applicant); me.Status != LiquidityStatusApplied {
		t.Fatalf("applicant = %s", me.Status)
	}
	stuck := uuid.New() // approved request, LP row never created (review M14)
	r.db.Exec(`INSERT INTO merchant_requests (id, user_id, business_type, status, created_at) VALUES (?, ?, ?, 'approved', ?)`,
		uuid.New(), stuck, models.BusinessTypeLiquidityProvider, time.Now())
	if me, _ := svc.Me(ctx, stuck); me.Status != LiquidityStatusApplied {
		t.Fatalf("approved request without a row = %s, want applied (no dead-end Activate)", me.Status)
	}
	approved := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusApproved, lpWallet)
	if me, _ := svc.Me(ctx, approved.UserID); me.Status != LiquidityStatusApproved || me.Available != "" {
		t.Fatalf("approved = %+v (activate first; no balance yet)", me)
	}
	paused := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusPaused, lpWallet)
	if me, _ := svc.Me(ctx, paused.UserID); me.Status != LiquidityStatusPaused || !me.Paused || !me.BalanceReadable {
		t.Fatalf("paused = %+v", me)
	}
	merchant := seedPMRow(t, r.db, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, lpWallet)
	if me, _ := svc.Me(ctx, merchant.UserID); me.Status != LiquidityStatusNone || me.Wallet != "" {
		t.Fatalf("a payment merchant is never an LP: %+v", me)
	}
}

// Review Focus 5.
func TestLiquidityMe_18dpDecimalStrings(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	avail, _ := new(big.Int).SetString("1234567890123456789012", 10) // 1234.567890123456789012
	r.pool.balance = OnChainBalance{Available: avail, Locked: big.NewInt(1)}
	me, err := svc.Me(context.Background(), lp.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if me.Available != "1234.567890123456789012" || me.Locked != "0.000000000000000001" ||
		me.Stake != "1234.567890123456789013" || me.Headroom != "3765.432109876543210987" ||
		me.Token == nil || me.Token.Decimals != 18 || me.Chain == nil || me.Chain.ID != poolTestChain || me.Chain.Name != "BNB Smart Chain Testnet" {
		t.Fatalf("me = %+v", me)
	}
}

func TestLiquidityMe_VoucherAndOwed(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.db.Model(&models.PaymentMerchant{}).Where("id = ?", lp.ID).Update("available_ngn", "1500.00")
	ad := seedActiveAd(t, r.db, lp.UserID, poolTestChain, &lp.ID, "0", new(big.Int).Mul(usdt18(5000), big.NewInt(100)).String())
	soon := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) // 10 days after the rig clock
	r.db.Model(&models.P2PAd{}).Where("id = ?", ad.ID).Update("voucher_deadline", soon)
	svc.now = func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }
	me, _ := svc.Me(context.Background(), lp.UserID)
	if me.OwedNgn != "1500.00" || me.Voucher == nil || me.Voucher.AdID != ad.ID || !me.Voucher.RenewDue || me.Voucher.Remaining != "500000" {
		t.Fatalf("me = %+v voucher=%+v", me, me.Voucher)
	}
}

func auditRow(t *testing.T, r *poolRig, actor uuid.UUID, action string, meta map[string]any, at time.Time) {
	b, _ := json.Marshal(meta)
	if err := r.db.Create(&models.P2PAuditLog{ID: uuid.New(), ActorID: &actor, Action: action, Metadata: datatypes.JSON(b), CreatedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestLiquidityActivity_DepositsOnPoolChainOnly(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	t0 := time.Now().UTC().Add(-time.Hour)
	auditRow(t, r, lp.UserID, models.P2PAuditDepositConfirmed, map[string]any{"chainId": poolTestChain, "amount": usdt18(7).String(), "txHash": "0xa"}, t0)
	auditRow(t, r, lp.UserID, models.P2PAuditDepositConfirmed, map[string]any{"chainId": 146, "amount": "1000000", "txHash": "0xb"}, t0.Add(time.Minute))
	auditRow(t, r, lp.UserID, models.P2PAuditWithdrawConfirmed, map[string]any{"chainId": poolTestChain, "amount": usdt18(2).String(), "txHash": "0xc"}, t0.Add(2*time.Minute))
	page, err := svc.Activity(context.Background(), lp.UserID, "deposits", "", 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].Amount != "7" || page.Items[0].TxHash != "0xa" {
		t.Fatalf("deposits = %+v %v", page, err)
	}
	page, _ = svc.Activity(context.Background(), lp.UserID, "withdrawals", "", 20)
	if len(page.Items) != 1 || page.Items[0].Amount != "2" {
		t.Fatalf("withdrawals = %+v", page)
	}
}

func TestLiquidityActivity_PayoutsCarryNoBuyer(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.db.Create(&models.P2PSettlementForward{ID: uuid.New(), TradeID: uuid.New(), PMID: lp.ID, ProviderRef: "sess-1",
		Currency: "NGN", Amount: "13615.10", Status: models.P2PForwardSettled, Reference: "tsa-fwd-1", CreatedAt: time.Now().UTC()})
	page, err := svc.Activity(context.Background(), lp.UserID, "payouts", "", 20)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("payouts = %+v %v", page, err)
	}
	it := page.Items[0]
	b, _ := json.Marshal(it)
	if it.AmountNgn != "13615.10" || it.BankLast4 != "6789" || it.Status != models.P2PForwardSettled ||
		strings.Contains(string(b), "trade") || strings.Contains(string(b), "buyer") {
		t.Fatalf("payout item = %s", b)
	}
}

func TestLiquidityActivity_RejectsUnknownTypeAndNonLP(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	if _, err := svc.Activity(context.Background(), lp.UserID, "fills", "", 20); !errors.Is(err, ErrLiquidityActivityType) {
		t.Fatalf("fills (Plan C1): %v", err)
	}
	if _, err := svc.Activity(context.Background(), uuid.New(), "deposits", "", 20); !errors.Is(err, ErrNotLiquidityProvider) {
		t.Fatalf("non-LP: %v", err)
	}
}

type slowEscrow struct {
	*mockEscrow
	d time.Duration
}

func (s slowEscrow) ReadBalance(seller, token string) (*OnChainBalance, error) {
	time.Sleep(s.d)
	return s.mockEscrow.ReadBalance(seller, token)
}

// Review I7: twelve LPs behind a hung RPC still answer fast, each saying so.
func TestLiquidityAdminViews_BoundedAndTimed(t *testing.T) {
	r, _ := newLiquidityRig(t)
	reg := NewP2PEscrowRegistryFromEntries(map[int64]P2PRegistryEntry{
		poolTestChain: {Escrow: slowEscrow{newMockEscrow(), 2 * time.Second}, ChainKey: "bsc", Purpose: config.P2PPurposePool,
			Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 18}},
	}, "2")
	stock := NewPoolStock(reg, poolTestTokenAddr)
	stock.timeout = 50 * time.Millisecond
	svc := NewLiquidityService(r.db, NewPMService(r.db), r.svc, stock, fakePoolOpen(true), "")
	var lps []models.PaymentMerchant
	for i := 0; i < 12; i++ {
		lps = append(lps, *seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000e"+strconv.Itoa(i%10)))
	}
	start := time.Now()
	views := svc.AdminLPViews(context.Background(), lps)
	if el := time.Since(start); el > time.Second {
		t.Fatalf("took %s: reads are not bounded", el)
	}
	if len(views) != 12 {
		t.Fatalf("views = %d", len(views))
	}
	for _, v := range views {
		if v.BalanceError == "" {
			t.Fatalf("a timed-out read must say so: %+v", v)
		}
	}
}

func TestLiquidityAdminView(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.pool.balance = OnChainBalance{Available: usdt18(5200), Locked: usdt18(100)}
	v := svc.AdminLPView(context.Background(), lp)
	if v.Stake != "5000" || v.AboveMax != "300" || v.Locked != "100" || v.BalanceError != "" {
		t.Fatalf("view = %+v", v)
	}
	r.pool.balanceErr = errors.New(`dial https://bsc.example/v1/KEY123: refused`)
	other := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000d9")
	if v := svc.AdminLPView(context.Background(), other); v.BalanceError == "" || strings.Contains(v.BalanceError, "KEY123") {
		t.Fatalf("unreadable balance must say so, redacted: %+v", v)
	}
}

// A pending LP row (approval not finished) cannot be activated yet, so it reads
// as under review, never as an activation that would fail (review M14).
func TestLiquidityMe_PendingRowReadsApplied(t *testing.T) {
	r, svc := newLiquidityRig(t)
	pending := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusPending, lpWallet)
	if me, _ := svc.Me(context.Background(), pending.UserID); me.Status != LiquidityStatusApplied || me.Available != "" {
		t.Fatalf("pending = %+v", me)
	}
	rejected := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusRejected, lpWallet)
	if me, _ := svc.Me(context.Background(), rejected.UserID); me.Status != LiquidityStatusRejected || me.BalanceReadable {
		t.Fatalf("rejected = %+v", me)
	}
}

// An unreadable balance is reported as such, with no RPC text and no amounts.
func TestLiquidityMe_UnreadableBalanceSaysSoWithoutRPCText(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.pool.balanceErr = errors.New(`dial https://bsc.example/v1/KEY123: refused`)
	me, err := svc.Me(context.Background(), lp.UserID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(me)
	if me.Status != LiquidityStatusActive || me.BalanceReadable || me.Available != "" || me.Stake != "" ||
		strings.Contains(string(b), "KEY123") || strings.Contains(string(b), "bsc.example") {
		t.Fatalf("me = %s", b)
	}
}

// §15: activity is the caller's own; another LP's rows never appear.
func TestLiquidityActivity_ScopedToTheCaller(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	other := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000d7")
	t0 := time.Now().UTC().Add(-time.Hour)
	auditRow(t, r, other.UserID, models.P2PAuditDepositConfirmed, map[string]any{"chainId": poolTestChain, "amount": usdt18(9).String(), "txHash": "0xother"}, t0)
	if err := r.db.Create(&models.P2PSettlementForward{ID: uuid.New(), TradeID: uuid.New(), PMID: other.ID, ProviderRef: "sess-o",
		Currency: "NGN", Amount: "99.00", Status: models.P2PForwardSettled, Reference: "tsa-fwd-o", CreatedAt: t0}).Error; err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"deposits", "withdrawals", "payouts"} {
		page, err := svc.Activity(context.Background(), lp.UserID, typ, "", 20)
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("%s = %+v %v (another LP's rows leaked)", typ, page, err)
		}
	}
}

func TestLiquidityActivity_CursorPages(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	t0 := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		auditRow(t, r, lp.UserID, models.P2PAuditDepositConfirmed, map[string]any{"chainId": poolTestChain, "amount": usdt18(int64(i + 1)).String(), "txHash": "0x" + strconv.Itoa(i)}, t0.Add(time.Duration(i)*time.Minute))
		if err := r.db.Create(&models.P2PSettlementForward{ID: uuid.New(), TradeID: uuid.New(), PMID: lp.ID, ProviderRef: "sess-" + strconv.Itoa(i),
			Currency: "NGN", Amount: strconv.Itoa(i+1) + "00.00", Status: models.P2PForwardPending, Reference: "ref-" + strconv.Itoa(i), CreatedAt: t0.Add(time.Duration(i) * time.Minute)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for typ, want := range map[string][]string{"deposits": {"3", "2", "1"}, "payouts": {"300.00", "200.00", "100.00"}} {
		var got []string
		cursor := ""
		for page := 0; page < 3; page++ {
			p, err := svc.Activity(context.Background(), lp.UserID, typ, cursor, 2)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range p.Items {
				got = append(got, it.Amount+it.AmountNgn)
			}
			if cursor = p.NextCursor; cursor == "" {
				break
			}
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s pages = %v, want %v", typ, got, want)
		}
	}
	if _, err := svc.Activity(context.Background(), lp.UserID, "deposits", "not-a-time", 2); !errors.Is(err, ErrLiquidityActivityType) {
		t.Fatalf("bad cursor = %v", err)
	}
}

type countingEscrow struct {
	*mockEscrow
	mu             sync.Mutex
	inFlight, peak int
}

func (c *countingEscrow) ReadBalance(seller, token string) (*OnChainBalance, error) {
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.peak {
		c.peak = c.inFlight
	}
	c.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	return c.mockEscrow.ReadBalance(seller, token)
}

func adminViewsOver(t *testing.T, r *poolRig, esc P2PEscrowAdapter, timeout time.Duration, n int) (*LiquidityService, []models.PaymentMerchant) {
	t.Helper()
	reg := NewP2PEscrowRegistryFromEntries(map[int64]P2PRegistryEntry{
		poolTestChain: {Escrow: esc, ChainKey: "bsc", Purpose: config.P2PPurposePool,
			Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 18}},
	}, "2")
	stock := NewPoolStock(reg, poolTestTokenAddr)
	stock.timeout = timeout
	var lps []models.PaymentMerchant
	for i := 0; i < n; i++ {
		lps = append(lps, *seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, fmt.Sprintf("0x%040x", 0xe00+i)))
	}
	return NewLiquidityService(r.db, NewPMService(r.db), r.svc, stock, fakePoolOpen(true), ""), lps
}

// Review I7: never more than 8 chain reads at once, however many LPs.
func TestLiquidityAdminViews_AtMostEightReadsAtOnce(t *testing.T) {
	r, _ := newLiquidityRig(t)
	esc := &countingEscrow{mockEscrow: newMockEscrow()}
	svc, lps := adminViewsOver(t, r, esc, time.Second, 20)
	views := svc.AdminLPViews(context.Background(), lps)
	if len(views) != 20 || esc.peak > adminLPViewConcurrency || esc.peak < 2 {
		t.Fatalf("views=%d peak=%d", len(views), esc.peak)
	}
	for _, v := range views {
		if v.BalanceError != "" {
			t.Fatalf("view = %+v", v)
		}
	}
}

// The list answers by its deadline even when each read would wait longer.
func TestLiquidityAdminViews_StopAtTheDeadline(t *testing.T) {
	r, _ := newLiquidityRig(t)
	svc, lps := adminViewsOver(t, r, slowEscrow{newMockEscrow(), 2 * time.Second}, 10*time.Second, 12)
	if svc.adminDeadline != 5*time.Second {
		t.Fatalf("deadline = %s, want 5s (review I7)", svc.adminDeadline)
	}
	svc.adminDeadline = 100 * time.Millisecond
	start := time.Now()
	views := svc.AdminLPViews(context.Background(), lps)
	if el := time.Since(start); el > time.Second || len(views) != 12 {
		t.Fatalf("took %s, views=%d", el, len(views))
	}
	for _, v := range views {
		if v.BalanceError == "" || v.OwedNgn == "" {
			t.Fatalf("view = %+v", v)
		}
	}
}

// Held above the max: headroom is zero, never negative.
func TestLiquidityMe_HeadroomNeverNegative(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.pool.balance = OnChainBalance{Available: usdt18(5200), Locked: usdt18(100)}
	me, _ := svc.Me(context.Background(), lp.UserID)
	if me.Headroom != "0" || me.Stake != "5000" || me.AboveMax != "300" {
		t.Fatalf("me = %+v", me)
	}
}

func TestLiquidityActivity_PaymentMerchantIsNotAnLP(t *testing.T) {
	r, svc := newLiquidityRig(t)
	m := seedPMRow(t, r.db, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, lpWallet)
	if _, err := svc.Activity(context.Background(), m.UserID, "payouts", "", 20); !errors.Is(err, ErrNotLiquidityProvider) {
		t.Fatalf("merchant payouts = %v", err)
	}
}
