package services

import (
	"context"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newITTestService(t *testing.T) *ITService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.ITRate{}, &models.PaymentMerchant{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	swap := NewMCGPSwapService(nil, &config.Config{Env: "development", MCGPSwapBAddress: "0x00000000000000000000000000000000000bEEF0"})
	swap.SetOperatorSigner(&fakeReleaseSigner{addr: "0x000000000000000000000000000000000000A11CE", hash: "0xdead"})
	return NewITService(db, swap)
}

func TestRateMath_RoundsDown(t *testing.T) {
	e18 := oneE18
	// buy: ngnKobo * 1e18 / buyRateKobo. 5000 kobo (₦50) at 100 kobo/MCGP = 50 MCGP.
	got := mcgpForNgn(big.NewInt(5000), big.NewInt(100))
	want := new(big.Int).Mul(big.NewInt(50), e18)
	if got.Cmp(want) != 0 {
		t.Fatalf("mcgpForNgn = %s want %s", got, want)
	}
	// non-divisible rounds down: 101 kobo / 100 = 1.01 MCGP -> floor to 1.01e18 - dust.
	g2 := mcgpForNgn(big.NewInt(101), big.NewInt(100))
	exp := new(big.Int).Div(new(big.Int).Mul(big.NewInt(101), e18), big.NewInt(100))
	if g2.Cmp(exp) != 0 {
		t.Fatalf("rounding mismatch %s vs %s", g2, exp)
	}
	// sell: mcgpWei * sellRateKobo / 1e18. 50 MCGP at 90 kobo = 4500 kobo (₦45).
	sell := ngnForMcgp(new(big.Int).Mul(big.NewInt(50), e18), big.NewInt(90))
	if sell.Cmp(big.NewInt(4500)) != 0 {
		t.Fatalf("ngnForMcgp = %s want 4500", sell)
	}
}

func TestSetRate_Validation(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	admin := uuid.New()

	if _, err := s.SetRate(ctx, admin, "0", "100"); err == nil {
		t.Fatal("expected invalid buy rate")
	}
	if _, err := s.SetRate(ctx, admin, "100", "200"); err == nil {
		t.Fatal("expected sell>buy rejection")
	}
	r, err := s.SetRate(ctx, admin, "20000", "18000")
	if err != nil {
		t.Fatalf("set rate: %v", err)
	}
	if !r.Active {
		t.Fatal("rate should be active")
	}
	// Setting again deactivates the old one.
	if _, err := s.SetRate(ctx, admin, "21000", "18500"); err != nil {
		t.Fatalf("set rate 2: %v", err)
	}
	active, err := s.GetActiveRate(ctx)
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if active.BuyRateKobo != "21000" {
		t.Fatalf("active buy = %s want 21000", active.BuyRateKobo)
	}
	var count int64
	s.db.Model(&models.ITRate{}).Where("active = ?", true).Count(&count)
	if count != 1 {
		t.Fatalf("exactly one active rate expected, got %d", count)
	}
}

func TestCreateBuyIntent_LocksRateAndComputesMcgp(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	if _, err := s.SetRate(ctx, uuid.New(), "100", "90"); err != nil { // 100 kobo (₦1) per MCGP
		t.Fatalf("set rate: %v", err)
	}
	seedCompanyPM(t, s) // IT requires a configured pay-into PM
	user := uuid.New()
	intent, err := s.CreateBuyIntent(ctx, user, "0xWALLET", "5000", nil) // ₦50
	if err != nil {
		t.Fatalf("create buy: %v", err)
	}
	want := new(big.Int).Mul(big.NewInt(50), oneE18).String()
	if intent.McgpAmount != want {
		t.Fatalf("mcgp = %s want %s", intent.McgpAmount, want)
	}
	if intent.RateKobo != "100" {
		t.Fatalf("rate not locked: %s", intent.RateKobo)
	}
	if intent.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %s", intent.Status)
	}
	if len(intent.PaymentRef) == 0 {
		t.Fatal("paymentRef missing")
	}
}

func TestItRef_DomainSeparated(t *testing.T) {
	id := uuid.New()
	credit := itRef("IT:credit:", id)
	debit := itRef("IT:debit:", id)
	if credit == debit {
		t.Fatal("credit and debit refs must differ for the same intent")
	}
	if itRef("IT:credit:", id) != credit {
		t.Fatal("itRef must be deterministic")
	}
}

func TestCreateBuyIntent_NoRate(t *testing.T) {
	s := newITTestService(t)
	if _, err := s.CreateBuyIntent(context.Background(), uuid.New(), "0xWALLET", "1000", nil); err == nil {
		t.Fatal("expected ErrITNoRate when no rate set")
	}
}

// seedCompanyPM inserts an active PM with an inbound account and pins it as the
// IT company PM (IT_PM_ID). Returns the PM id.
func seedCompanyPM(t *testing.T, s *ITService) uuid.UUID {
	t.Helper()
	pm := &models.PaymentMerchant{
		ID: uuid.New(), UserID: uuid.New(), Name: "Company PM",
		Status: models.PaymentMerchantStatusActive, InboundAccountNumber: "2998287753", InboundBank: "Nomba",
	}
	if err := s.db.Create(pm).Error; err != nil {
		t.Fatalf("seed pm: %v", err)
	}
	s.SetCompanyPMID(pm.ID.String())
	return pm.ID
}

func TestCompanyPM_AlwaysConfiguredNoFallback(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	// Another active PM exists, but IT must NOT pick it.
	other := &models.PaymentMerchant{ID: uuid.New(), UserID: uuid.New(), Name: "Other", Status: models.PaymentMerchantStatusActive, InboundAccountNumber: "111"}
	s.db.Create(other)

	// IT_PM_ID unset -> nil (no fallback to `other`).
	pm, err := s.CompanyPM(ctx)
	if err != nil || pm != nil {
		t.Fatalf("expected nil when IT_PM_ID unset, got pm=%v err=%v", pm, err)
	}
	// IT_PM_ID pinned -> exactly that PM.
	id := seedCompanyPM(t, s)
	pm, err = s.CompanyPM(ctx)
	if err != nil || pm == nil || pm.ID != id {
		t.Fatalf("expected the configured PM, got pm=%v err=%v", pm, err)
	}
}

func TestCreateBuyIntent_NoPM(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	if _, err := s.SetRate(ctx, uuid.New(), "100", "90"); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	// Rate set but no IT_PM_ID configured -> buy fails fast (no orphan intent).
	if _, err := s.CreateBuyIntent(ctx, uuid.New(), "0xWALLET", "1000", nil); err != ErrITNoPM {
		t.Fatalf("expected ErrITNoPM, got %v", err)
	}
	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatalf("no intent should be created when PM missing, got %d", n)
	}
}
