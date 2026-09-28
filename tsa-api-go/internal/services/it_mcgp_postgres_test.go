package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
)

// The MCGP legs of the same defect it_stable_postgres_test.go was written for.
//
// GORM puts every field without a column `default:` tag into the INSERT whether
// or not the caller set it, so an unset Go string reaches PostgreSQL as
// ''::numeric and the whole statement is rejected (SQLSTATE 22P02). SQLite's
// type affinity swallows it, so the rest of this package cannot see it: MCGP
// buy and MCGP sell were both un-creatable in production while every SQLite
// test stayed green.
//
// These tests drive the real creation paths against a real PostgreSQL and skip
// when DATABASE_URL is absent.

// newITPostgresDB opens a THROWAWAY schema inside the database DATABASE_URL
// points at and migrates dst into it, or skips when there is no database.
//
// The temporary schema is the point: AutoMigrate is DDL and must not touch the
// developer's real it_intents table. Everything is dropped with the schema.
func newITPostgresDB(t *testing.T, dst ...any) *gorm.DB {
	t.Helper()
	return testutil.TempPostgres(t, "it_mcgp_test_", dst...)
}

// newITPostgresService mirrors newITTestService (the SQLite fixture) on
// PostgreSQL: same rate, same FX, same fee engine, so the intents these tests
// write are the ones production writes.
func newITPostgresService(t *testing.T) *ITService {
	t.Helper()
	db := newITPostgresDB(t,
		&models.ITIntent{}, &models.ITRate{}, &models.PaymentMerchant{},
		&models.FiatVirtualAccount{}, &models.User{},
	)
	swap := NewMCGPSwapService(nil, &config.Config{Env: "development", MCGPSwapBAddress: testSwapAddr})
	swap.SetOperatorSigner(&fakeReleaseSigner{addr: "0x000000000000000000000000000000000000A11CE", hash: "0xdead"})
	s := NewITService(db, swap, stubFx{ngn: 1500})
	s.SetFeeQuoter(feeQuoterForTest())
	if _, err := s.SetRate(context.Background(), uuid.New(), 20000, 500); err != nil { // $0.02, 5% sell discount
		t.Fatalf("set rate: %v", err)
	}
	return s
}

const testSwapAddr = "0x00000000000000000000000000000000000bEEF0"

// assertNoEmptyNumericColumn is the mechanical guard: enumerate it_intents'
// numeric columns from information_schema and assert the row carries a value in
// every one. Reading the list from the catalogue rather than hand-writing it
// means a numeric column added later is covered the day it lands — which is
// what stops this class of defect recurring rather than just fixing today's
// instance.
func assertNoEmptyNumericColumn(t *testing.T, db *gorm.DB, intentID uuid.UUID) {
	t.Helper()
	var columns []string
	if err := db.Raw(`
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
		if err := db.Raw(
			fmt.Sprintf("SELECT %s IS NULL FROM it_intents WHERE id = ?", col),
			intentID,
		).Scan(&isNull).Error; err != nil {
			t.Fatalf("read %s: %v", col, err)
		}
		if isNull {
			t.Errorf("numeric column %q is NULL — a reader parsing it as a number gets nothing", col)
		}
	}
}

func TestCreateBuyIntent_InsertsOnPostgres(t *testing.T) {
	// The production failure: "invalid input syntax for type numeric" on every
	// MCGP buy, because received_amount and locked_rate — added by the
	// stablecoin work — were never set by this literal and went out as ''.
	s := newITPostgresService(t)
	seedCompanyPM(t, s)

	intent, err := s.CreateBuyIntent(context.Background(), uuid.New(), "0xWALLET", "500000", nil) // ₦5000
	if err != nil {
		t.Fatalf("CreateBuyIntent on PostgreSQL: %v", err)
	}

	got, err := s.GetIntent(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.NgnKobo != "500000" {
		t.Errorf("ngnKobo = %q, want the gross 500000", got.NgnKobo)
	}
	if got.RateKobo == "" || got.McgpAmount == "" {
		t.Errorf("quote not persisted: rate=%q mcgp=%q", got.RateKobo, got.McgpAmount)
	}
	// The columns the literal never set must come back as the database default,
	// not as an empty string and not as NULL.
	if got.ReceivedAmount != "0" || got.LockedRate != "0" {
		t.Errorf("receivedAmount/lockedRate = %q/%q, want 0/0 from the column default",
			got.ReceivedAmount, got.LockedRate)
	}
	assertNoEmptyNumericColumn(t, s.db, intent.ID)
}

func TestCreateSellIntent_InsertsOnPostgres(t *testing.T) {
	// The reported failure, verbatim: "Sale failed — ERROR: invalid input
	// syntax for type numeric" at MCGP sell intent creation.
	//
	// CreateSellIntent reads the on-chain premium balance BEFORE it inserts, so
	// the test serves that one call from a stub RPC. Everything after the
	// INSERT (the operator debit broadcast and its event verification) has no
	// chain to talk to and fails, which is why this asserts on the persisted
	// row rather than on a returned intent — the INSERT is what is under test.
	s := newITPostgresService(t)
	s.swap = newStubChainSwap(t, oneE30)

	userID := uuid.New()
	bank := &ITSellBank{Code: "044", Number: "0123456789", Name: "ADA OKAFOR"}
	// 100 MCGP: at $0.02 less the 5% sell discount and ₦1500/$ that is ₦2850,
	// comfortably over the ₦100 payout floor the create path enforces.
	hundredMcgp := new(big.Int).Mul(big.NewInt(100), oneE18).String()
	_, err := s.CreateSellIntent(context.Background(), userID, testSellWallet, hundredMcgp, bank)
	if err == nil {
		t.Fatal("expected the operator debit to fail against the stub RPC")
	}

	var rows []models.ITIntent
	if qErr := s.db.Where("user_id = ?", userID).Find(&rows).Error; qErr != nil {
		t.Fatalf("load intents: %v", qErr)
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 sell intent inserted, got %d (create error was: %v)", len(rows), err)
	}
	got := rows[0]
	if got.NgnKobo != "285000" {
		t.Errorf("ngnKobo = %q, want 285000 (100 MCGP at the discounted sell rate)", got.NgnKobo)
	}
	if got.FeeNgnKobo == "" || got.NetNgnKobo == "" {
		t.Errorf("fees not recorded: fee=%q net=%q", got.FeeNgnKobo, got.NetNgnKobo)
	}
	if got.ReceivedAmount != "0" || got.LockedRate != "0" {
		t.Errorf("receivedAmount/lockedRate = %q/%q, want 0/0 from the column default",
			got.ReceivedAmount, got.LockedRate)
	}
	assertNoEmptyNumericColumn(t, s.db, got.ID)
}

func TestAutoCreditFromVA_InsertsOnPostgres(t *testing.T) {
	// The VA settlement path mints its own intent when the payer has no open
	// buy order. It sets three numerics and leaves the rest — including the fee
	// columns, whose values it had already computed — so on PostgreSQL a charge
	// landing against a user with no open order could not be recorded at all.
	s := newITPostgresService(t)
	user, va := seedPostgresVA(t, s)

	// No chain in tests: the credit broadcast fails, but attribution and the
	// INSERT are what this covers.
	_ = s.AutoCreditFromVA(context.Background(), va.FwCustomerID, va.Reference, "chg_pg_0001", "5000")

	rows := buyIntents(t, s, user.ID)
	if len(rows) != 1 {
		t.Fatalf("want 1 intent, got %d", len(rows))
	}
	got := rows[0]
	if got.NgnKobo != "500000" {
		t.Errorf("ngnKobo = %q, want 500000 (the transferred amount)", got.NgnKobo)
	}
	// The credited MCGP is net of fees, so the fee columns must say what was
	// taken. A silent "0" here would make the row read as a fee-free purchase.
	if got.FeeNgnKobo == "" || got.FeeNgnKobo == "0" {
		t.Errorf("feeNgnKobo = %q — the deposit fee was deducted but not recorded", got.FeeNgnKobo)
	}
	if got.NetNgnKobo == "" || got.NetNgnKobo == got.NgnKobo {
		t.Errorf("netNgnKobo = %q, want the gross less the fee", got.NetNgnKobo)
	}
	assertNoEmptyNumericColumn(t, s.db, got.ID)
}

func TestAutoCreditFromVA_StaleFxParkInsertsOnPostgres(t *testing.T) {
	// The park branch: money landed while FX was stale, so the intent is stored
	// with no rate and no MCGP for an operator to reprice. It sets the fewest
	// numerics of any creation path, which makes it the most exposed — and
	// losing this INSERT would lose the record of money already received.
	s := newITPostgresService(t)
	user, va := seedPostgresVA(t, s)
	s.fx = stubFx{ngn: 1500, stale: true}

	if err := s.AutoCreditFromVA(context.Background(), va.FwCustomerID, va.Reference, "chg_pg_0002", "5000"); err != nil {
		t.Fatalf("stale-FX park on PostgreSQL: %v", err)
	}

	rows := buyIntents(t, s, user.ID)
	if len(rows) != 1 {
		t.Fatalf("want 1 parked intent, got %d", len(rows))
	}
	got := rows[0]
	if got.Status != models.ITStatusAwaitingRate {
		t.Errorf("status = %q, want %q", got.Status, models.ITStatusAwaitingRate)
	}
	if got.NgnKobo != "500000" {
		t.Errorf("ngnKobo = %q, want 500000 — the park exists to record what landed", got.NgnKobo)
	}
	assertNoEmptyNumericColumn(t, s.db, got.ID)
}

// ── fixtures ──

const (
	testSellWallet = "0x000000000000000000000000000000000000dEaD"
	// oneE30 is a premium balance large enough to clear any sell these tests
	// raise, so the balance check is never what fails.
	oneE30 = "0x000000000000000000000000000000000000000c9f2c9cd04674edea40000000"
)

// seedPostgresVA creates the user and the active virtual account
// AutoCreditFromVA resolves a charge against.
func seedPostgresVA(t *testing.T, s *ITService) (*models.User, *models.FiatVirtualAccount) {
	t.Helper()
	user := &models.User{
		ID:            uuid.New(),
		Email:         "ada+" + uuid.New().String()[:8] + "@example.com",
		Name:          "Ada Lovelace",
		WalletAddress: testSellWallet,
	}
	if err := s.db.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	va := &models.FiatVirtualAccount{
		UserID: user.ID, Currency: "NGN", Provider: models.FiatVAProviderFlutterwave,
		FwCustomerID: "cus_" + uuid.New().String()[:8], Reference: "ref-" + uuid.New().String()[:8],
		AccountNumber: "1234567890", BankName: "Test Bank", AccountName: "Ada Lovelace",
		Status: models.FiatVAStatusActive,
	}
	if err := s.db.Create(va).Error; err != nil {
		t.Fatalf("seed va: %v", err)
	}
	return user, va
}

// newStubChainSwap returns an MCGPSwapService whose reads are served by a local
// JSON-RPC server that answers every eth_call with result. Writes (the nonce,
// gas and broadcast calls behind OperatorDebitSpendable) get a JSON-RPC error,
// so the sell fails after its INSERT — which is exactly the seam these tests
// need, since the INSERT is what production was failing on.
func newStubChainSwap(t *testing.T, result string) *MCGPSwapService {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "eth_call" {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%q}`, req.ID, result)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"stub RPC serves eth_call only"}}`, req.ID)
	}))
	t.Cleanup(srv.Close)

	client, err := blockchain.NewEVMClient(srv.URL, 146)
	if err != nil {
		t.Fatalf("stub rpc client: %v", err)
	}
	swap := NewMCGPSwapService(client, &config.Config{Env: "development", MCGPSwapBAddress: testSwapAddr})
	swap.SetOperatorSigner(&fakeReleaseSigner{addr: "0x000000000000000000000000000000000000A11CE", hash: "0xdead"})
	return swap
}

// TestAutoMigrateAppliesNumericDefaultsToAnExistingTable covers the deploy, not
// a code path. The fix relies on GORM omitting a zero-value field that has a
// column `default:` tag — but production's it_intents already exists WITHOUT
// those defaults, so an omitted column would land as NULL unless the migration
// actually alters the live table. It does; this pins that.
//
// It also pins the deliberate exclusions: the quote columns and net_ngn_kobo
// must stay default-less so an unset value is rejected rather than invented.
func TestAutoMigrateAppliesNumericDefaultsToAnExistingTable(t *testing.T) {
	db := newITPostgresDB(t)
	// The pre-fix production shape: numeric columns with no default at all.
	if err := db.Exec(`CREATE TABLE it_intents (
		id uuid PRIMARY KEY,
		received_amount numeric(78,0),
		locked_rate numeric(38,0),
		net_ngn_kobo numeric(38,0))`).Error; err != nil {
		t.Fatalf("seed pre-fix table: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	columnDefault := func(col string) string {
		var def *string
		if err := db.Raw(`SELECT column_default FROM information_schema.columns
		    WHERE table_name = 'it_intents' AND column_name = ? AND table_schema = current_schema()`,
			col).Scan(&def).Error; err != nil {
			t.Fatalf("read default for %s: %v", col, err)
		}
		if def == nil {
			return ""
		}
		return *def
	}

	for _, col := range []string{"received_amount", "locked_rate", "fee_ngn_kobo", "platform_kept_kobo", "asset_amount"} {
		if got := columnDefault(col); got == "" {
			t.Errorf("%s has no default after migration — an omitted field would land as NULL", col)
		}
	}
	for _, col := range []string{"ngn_kobo", "rate_kobo", "mcgp_amount", "net_ngn_kobo"} {
		if got := columnDefault(col); got != "" {
			t.Errorf("%s gained the default %q — an unset quote/payout amount must fail the INSERT, not be invented", col, got)
		}
	}
}
