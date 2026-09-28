package services

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// Every other test in this package runs on SQLite, and SQLite's type affinity
// happily accepts an empty string into a numeric column. PostgreSQL does not:
// `SELECT ''::numeric` is an error (SQLSTATE 22P02), and GORM puts every field
// without a column `default:` tag into the INSERT whether or not it was set.
// That combination made a whole class of defect structurally invisible to this
// suite — CreateStableSellIntent could not insert a single row in production
// while the suite stayed green.
//
// This file is the counterweight: it exercises the real write path against a
// real PostgreSQL. It skips cleanly when DATABASE_URL is absent so CI without a
// database still passes.

// newStablePostgresService builds an ITService on a THROWAWAY schema inside the
// database DATABASE_URL points at, or skips the test when there is none.
//
// The temporary schema is the whole point: AutoMigrate is a DDL operation, and
// it must not touch the developer's real `it_intents` table. Everything the
// test creates is dropped with the schema.
func newStablePostgresService(t *testing.T) *ITService {
	t.Helper()
	db := testutil.TempPostgres(t, "it_stable_test_", &models.ITIntent{}, &models.FiatVirtualAccount{})

	s := NewITService(db, nil, stubFx{ngn: 1500})
	s.SetStableSpreadBps(155)
	s.SetStableRateLockTTL(15 * time.Minute)
	// CreateStableSellIntent fails closed without a configured token contract
	// (see ErrStableNoTokenAddress) — wire one so this fixture exercises the
	// write path, not the refusal.
	s.SetStableVerifier(nil, nil, map[string]string{"USDC": testToken, "USDT": testToken})
	return s
}

func TestCreateStableSellIntent_InsertsOnPostgres(t *testing.T) {
	// The regression that shipped: six numeric columns went out as '' and
	// PostgreSQL rejected the whole INSERT with SQLSTATE 22P02, so no user
	// could create a sell at all. Nothing about this is visible on SQLite.
	s := newStablePostgresService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)

	q := testSellQuote()
	q.FeeMinor = big.NewInt(255789) // the fee the quote genuinely deducted

	intent, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), q, "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("CreateStableSellIntent on PostgreSQL: %v", err)
	}

	got, err := s.GetIntent(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	// The fee must be recorded as the fee actually charged. Writing "0" here
	// would balance the row arithmetically and make the ledger understate what
	// the user paid.
	if got.FeeNgnKobo != "255789" {
		t.Fatalf("feeNgnKobo = %q, want the quote's fee 255789", got.FeeNgnKobo)
	}
	if got.FeeMinor != "255789" {
		t.Fatalf("feeMinor = %q — BeforeSave's mirror left the minor twin wrong", got.FeeMinor)
	}
	if got.McgpAmount != "0" {
		t.Fatalf("mcgpAmount = %q, want 0 (a stablecoin sell has no MCGP leg)", got.McgpAmount)
	}
	if got.NetNgnKobo != q.NetMinor.String() || got.NetMinor != q.NetMinor.String() {
		t.Fatalf("net = %q/%q, want %s", got.NetNgnKobo, got.NetMinor, q.NetMinor)
	}
	if got.AssetAmount != q.AssetAmount.String() {
		t.Fatalf("assetAmount = %q, want %s", got.AssetAmount, q.AssetAmount)
	}
}

func TestCreateStableSellIntent_LeavesNoNumericColumnEmptyOnPostgres(t *testing.T) {
	// The mechanical version of the test above: enumerate every numeric column
	// on it_intents and assert the row carries a value in each. Doing it from
	// information_schema rather than a hand-written list means a numeric column
	// added later is covered the day it lands, which is what stops this class
	// of defect from recurring.
	s := newStablePostgresService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)

	intent, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("CreateStableSellIntent on PostgreSQL: %v", err)
	}

	var columns []string
	if err := s.db.Raw(`
	    SELECT column_name FROM information_schema.columns
	    WHERE table_name = 'it_intents' AND data_type = 'numeric'
	      AND table_schema = current_schema()
	    ORDER BY column_name`).Scan(&columns).Error; err != nil {
		t.Fatalf("read numeric columns: %v", err)
	}
	if len(columns) == 0 {
		t.Fatal("no numeric columns found — the query is not testing anything")
	}

	for _, col := range columns {
		var isNull bool
		if err := s.db.Raw(
			fmt.Sprintf("SELECT %s IS NULL FROM it_intents WHERE id = ?", col),
			intent.ID,
		).Scan(&isNull).Error; err != nil {
			t.Fatalf("read %s: %v", col, err)
		}
		if isNull {
			t.Errorf("numeric column %q is NULL — a reader parsing it as a number gets nothing", col)
		}
	}
}

func TestCreateStableBuyIntent_InsertsOnPostgres(t *testing.T) {
	// The sell leg shipped a broken INSERT because PlatformKeptKobo was left
	// unset and GORM sent '' for it. The buy literal has the same shape and the
	// same exposure — this is the only test in the package that can see it.
	s := newStablePostgresService(t)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	wireStableBuyPrereqs(s)

	userID := uuid.New()
	va := &models.FiatVirtualAccount{
		ID: uuid.New(), UserID: userID, Currency: "NGN",
		AccountNumber: "1234567890", BankName: "Wema Bank", AccountName: "TSA / Ada Okafor",
		Reference: "ITVA-" + userID.String(),
	}
	if err := s.db.Create(va).Error; err != nil {
		t.Fatalf("seed va: %v", err)
	}

	intent, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("INSERT failed on PostgreSQL: %v", err)
	}
	if intent.ID == uuid.Nil || pay.AccountNumber == "" {
		t.Fatal("intent or pay-into details not populated")
	}
}
