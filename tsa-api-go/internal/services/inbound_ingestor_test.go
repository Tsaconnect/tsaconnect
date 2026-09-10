package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupInboundDB builds the SQLite rig for inbound_credits. Raw DDL rather than
// AutoMigrate because the model carries a gen_random_uuid() default SQLite has
// no idea about; the unique index on provider_ref is the piece under test, so
// it is spelled out here exactly as AutoMigrate emits it.
func setupInboundDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec(`CREATE TABLE inbound_credits (
		id TEXT PRIMARY KEY,
		provider TEXT NOT NULL,
		provider_ref TEXT NOT NULL,
		currency TEXT NOT NULL,
		amount_kobo INTEGER NOT NULL,
		fee_kobo INTEGER NOT NULL DEFAULT 0,
		gross_kobo INTEGER NOT NULL,
		sender_raw TEXT NOT NULL,
		sender_bank TEXT,
		sender_name TEXT,
		transaction_date DATETIME NOT NULL,
		seen_at DATETIME NOT NULL,
		status TEXT NOT NULL DEFAULT 'unmatched',
		hold_reason TEXT,
		matched_claim_type TEXT,
		matched_claim_id TEXT,
		matched_user_id TEXT,
		matched_at DATETIME,
		match_strength TEXT,
		resolved_by TEXT,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_inbound_credits_provider_ref
		ON inbound_credits (provider_ref)`).Error; err != nil {
		t.Fatalf("index ddl: %v", err)
	}
	return db
}

// fakeStatementSource replays a fixed statement page, counting fetches.
type fakeStatementSource struct {
	rows    []FWStatementEntry
	err     error
	calls   int
	lastCur string
	from    time.Time
	to      time.Time
}

func (f *fakeStatementSource) FetchWalletStatement(_ context.Context, currency string, from, to time.Time) ([]FWStatementEntry, error) {
	f.calls++
	f.lastCur, f.from, f.to = currency, from, to
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func stmtTime(s string) FWStatementTime {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return FWStatementTime{Time: ts}
}

// fundingRow is the measured production shape of a customer credit.
func fundingRow(ref string, amount float64, remarks string, at string) FWStatementEntry {
	return FWStatementEntry{
		Direction:       "credit",
		Type:            "funding",
		Amount:          FWQuoteAmount(amount),
		Balance:         FWStatementBalance{Before: 1000, After: FWQuoteAmount(1000 + amount)},
		Reference:       ref,
		Remarks:         remarks,
		Currency:        "NGN",
		TransactionDate: stmtTime(at),
	}
}

func TestParseSenderRemark(t *testing.T) {
	tests := []struct {
		name     string
		remark   string
		wantBank string
		wantName string
	}{
		{"measured production shape", "Received Money from GTBANK|ADEMU EMMANUEL O.", "GTBANK", "ADEMU EMMANUEL O."},
		{"case-insensitive prefix", "received money from KUDA MFB|JANE DOE", "KUDA MFB", "JANE DOE"},
		{"surrounding whitespace", "  Received Money from  UBA | JOHN  A  ", "UBA", "JOHN  A"},
		{"second pipe belongs to the name", "Received Money from OPAY|DOE|JANE", "OPAY", "DOE|JANE"},
		{"no pipe at all", "Received Money from GTBANK ADEMU EMMANUEL", "", ""},
		{"prefix changed by provider", "Inward transfer GTBANK|ADEMU EMMANUEL", "", ""},
		{"empty bank half", "Received Money from |ADEMU EMMANUEL", "", ""},
		{"empty name half", "Received Money from GTBANK|", "", ""},
		{"empty remark", "", "", ""},
		{"prefix only", "Received Money from ", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bank, name := parseSenderRemark(tc.remark)
			if bank != tc.wantBank || name != tc.wantName {
				t.Errorf("parseSenderRemark(%q) = (%q, %q), want (%q, %q)",
					tc.remark, bank, name, tc.wantBank, tc.wantName)
			}
		})
	}
}

// A credit whose remark does not parse must still be recorded — losing the
// money is never the price of an unrecognised string.
func TestIngestRows_UnparsedRemarkStillRecorded(t *testing.T) {
	db := setupInboundDB(t)
	ing := NewInboundIngestor(db, &fakeStatementSource{}, "NGN", time.Hour)

	res, err := ing.IngestRows(context.Background(), []FWStatementEntry{
		fundingRow("100000000000000000000000000001", 2000, "some new format nobody told us about", "2026-09-02T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("IngestRows: %v", err)
	}
	if res.Inserted != 1 || res.Unparsed != 1 {
		t.Fatalf("res = %+v, want 1 inserted / 1 unparsed", res)
	}
	var got models.InboundCredit
	if err := db.First(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.SenderRaw != "some new format nobody told us about" {
		t.Errorf("SenderRaw = %q, want the remark verbatim", got.SenderRaw)
	}
	if got.SenderBank != "" || got.SenderName != "" {
		t.Errorf("parsed fields = (%q, %q), want both empty", got.SenderBank, got.SenderName)
	}
	if got.GrossKobo != 200000 {
		t.Errorf("GrossKobo = %d, want 200000", got.GrossKobo)
	}
	if got.Status != models.InboundCreditUnmatched {
		t.Errorf("Status = %q, want unmatched", got.Status)
	}
}

// The same statement row seen twice — the steady state, since poll windows
// overlap — must leave exactly one row.
func TestIngestRows_IdempotentOnProviderRef(t *testing.T) {
	db := setupInboundDB(t)
	ing := NewInboundIngestor(db, &fakeStatementSource{}, "NGN", time.Hour)
	rows := []FWStatementEntry{
		fundingRow("100000000000000000000000000002", 2000, "Received Money from GTBANK|ADEMU EMMANUEL O.", "2026-09-02T10:00:00Z"),
	}

	first, err := ing.IngestRows(context.Background(), rows)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	second, err := ing.IngestRows(context.Background(), rows)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if first.Inserted != 1 || first.Duplicates != 0 {
		t.Errorf("first = %+v, want 1 inserted", first)
	}
	if second.Inserted != 0 || second.Duplicates != 1 {
		t.Errorf("second = %+v, want 1 duplicate", second)
	}
	var n int64
	db.Model(&models.InboundCredit{}).Count(&n)
	if n != 1 {
		t.Fatalf("row count = %d, want exactly 1", n)
	}
}

// Everything that is not a customer funding row is our own money moving and
// must never become a claimable credit.
func TestIngestRows_SkipsNonFundingTypes(t *testing.T) {
	db := setupInboundDB(t)
	ing := NewInboundIngestor(db, &fakeStatementSource{}, "NGN", time.Hour)

	rows := []FWStatementEntry{
		{Type: "transfer", Direction: "debit", Amount: 5000, Reference: "r-transfer", TransactionDate: stmtTime("2026-09-02T10:00:00Z")},
		{Type: "fee", Direction: "debit", Amount: 28, Reference: "r-fee", TransactionDate: stmtTime("2026-09-02T10:00:00Z")},
		{Type: "settlement", Direction: "credit", Amount: 9000, Reference: "r-settle", TransactionDate: stmtTime("2026-09-02T10:00:00Z")},
		{Type: "reversal", Direction: "credit", Amount: 100, Reference: "r-rev", TransactionDate: stmtTime("2026-09-02T10:00:00Z")},
		{Type: "bill", Direction: "debit", Amount: 500, Reference: "r-bill", TransactionDate: stmtTime("2026-09-02T10:00:00Z")},
		{Type: "chargeback", Direction: "debit", Amount: 100, Reference: "r-cb", TransactionDate: stmtTime("2026-09-02T10:00:00Z")},
		{Type: "card", Direction: "credit", Amount: 100, Reference: "r-card", TransactionDate: stmtTime("2026-09-02T10:00:00Z")},
		// A `funding` row flagged as a debit is not customer money either.
		{Type: "funding", Direction: "debit", Amount: 100, Reference: "r-fund-debit", TransactionDate: stmtTime("2026-09-02T10:00:00Z")},
		fundingRow("100000000000000000000000000003", 2000, "Received Money from GTBANK|ADEMU EMMANUEL O.", "2026-09-02T10:00:00Z"),
	}
	res, err := ing.IngestRows(context.Background(), rows)
	if err != nil {
		t.Fatalf("IngestRows: %v", err)
	}
	if res.Funding != 1 || res.Inserted != 1 {
		t.Errorf("res = %+v, want exactly 1 funding row ingested", res)
	}
	if res.Skipped != 8 {
		t.Errorf("Skipped = %d, want 8", res.Skipped)
	}
	var refs []string
	db.Model(&models.InboundCredit{}).Pluck("provider_ref", &refs)
	if len(refs) != 1 || refs[0] != "100000000000000000000000000003" {
		t.Fatalf("stored refs = %v, want only the funding row's NIP session id", refs)
	}
}

// A credit with no reference cannot be deduped on the provider's key, but
// discarding it would lose the money. It gets a content-derived key instead,
// and that key must be stable across polls and distinct between credits.
func TestIngestRows_SynthesizesRefWhenProviderOmitsIt(t *testing.T) {
	db := setupInboundDB(t)
	ing := NewInboundIngestor(db, &fakeStatementSource{}, "NGN", time.Hour)

	a := fundingRow("", 2000, "Received Money from GTBANK|ADEMU EMMANUEL O.", "2026-09-02T10:00:00Z")
	b := fundingRow("", 2000, "Received Money from GTBANK|ADEMU EMMANUEL O.", "2026-09-02T10:00:00Z")
	// Two identical-looking credits still land on different running balances.
	b.Balance = FWStatementBalance{Before: 3000, After: 5000}

	if _, err := ing.IngestRows(context.Background(), []FWStatementEntry{a, b}); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	res, err := ing.IngestRows(context.Background(), []FWStatementEntry{a, b})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Duplicates != 2 || res.Inserted != 0 {
		t.Errorf("replay = %+v, want both rows recognised as duplicates", res)
	}
	var n int64
	db.Model(&models.InboundCredit{}).Count(&n)
	if n != 2 {
		t.Fatalf("row count = %d, want 2 distinct credits", n)
	}
}

// Oversized free text must be truncated, not rejected: a Postgres varchar
// overflow would fail the INSERT and lose the credit.
func TestIngestRows_TruncatesOversizedRemark(t *testing.T) {
	db := setupInboundDB(t)
	ing := NewInboundIngestor(db, &fakeStatementSource{}, "NGN", time.Hour)

	long := "Received Money from GTBANK|"
	for len(long) < 400 {
		long += "ADEMU EMMANUEL OLUWASEUN "
	}
	if _, err := ing.IngestRows(context.Background(), []FWStatementEntry{
		fundingRow("100000000000000000000000000004", 2000, long, "2026-09-02T10:00:00Z"),
	}); err != nil {
		t.Fatalf("IngestRows: %v", err)
	}
	var got models.InboundCredit
	if err := db.First(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got.SenderRaw) > inboundSenderRawMax {
		t.Errorf("SenderRaw is %d bytes, want <= %d", len(got.SenderRaw), inboundSenderRawMax)
	}
	if len(got.SenderName) > inboundSenderNameMax {
		t.Errorf("SenderName is %d bytes, want <= %d", len(got.SenderName), inboundSenderNameMax)
	}
}

// ─── Fee pairing: BOTH unverified provider behaviours ──────────────────────

// Behaviour A — the provider NETS its 1.40% off the credit and books the fee
// as a memo row that never moved the wallet. The credit row understates what
// the sender transferred, so the fee must be added back or every match fails.
func TestPairFundingFees_NettedFeeIsAddedBack(t *testing.T) {
	db := setupInboundDB(t)
	ing := NewInboundIngestor(db, &fakeStatementSource{}, "NGN", time.Hour)

	credit := fundingRow("100000000000000000000000000005", 1972, "Received Money from GTBANK|ADEMU EMMANUEL O.", "2026-09-02T10:00:00Z")
	fee := FWStatementEntry{
		Type:      "fee",
		Direction: "debit",
		Amount:    28,
		// Netted: the wallet never saw this value, so the balance does not move.
		Balance:         FWStatementBalance{Before: 2972, After: 2972},
		Reference:       "100000000000000000000000000005",
		Remarks:         "Wallet funding fee",
		TransactionDate: stmtTime("2026-09-02T10:00:01Z"),
	}

	if _, err := ing.IngestRows(context.Background(), []FWStatementEntry{credit, fee}); err != nil {
		t.Fatalf("IngestRows: %v", err)
	}
	var got models.InboundCredit
	if err := db.First(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.AmountKobo != 197200 {
		t.Errorf("AmountKobo = %d, want the statement's own 197200", got.AmountKobo)
	}
	if got.FeeKobo != 2800 {
		t.Errorf("FeeKobo = %d, want 2800 (netted off the credit)", got.FeeKobo)
	}
	if got.GrossKobo != 200000 {
		t.Errorf("GrossKobo = %d, want 200000 — what the sender actually transferred", got.GrossKobo)
	}
}

// Behaviour B — the provider bills its charge SEPARATELY as its own wallet
// debit. The credit row is already gross, so adding the fee back would inflate
// every amount by 1.4% and fail matching just as badly.
func TestPairFundingFees_SeparatelyBilledFeeIsNotAddedBack(t *testing.T) {
	db := setupInboundDB(t)
	ing := NewInboundIngestor(db, &fakeStatementSource{}, "NGN", time.Hour)

	credit := fundingRow("100000000000000000000000000006", 2000, "Received Money from GTBANK|ADEMU EMMANUEL O.", "2026-09-02T10:00:00Z")
	fee := FWStatementEntry{
		Type:      "fee",
		Direction: "debit",
		Amount:    28,
		// Billed separately: the wallet balance moves on the fee's own row.
		Balance:         FWStatementBalance{Before: 3000, After: 2972},
		Reference:       "100000000000000000000000000006",
		Remarks:         "Wallet funding fee",
		TransactionDate: stmtTime("2026-09-02T10:00:01Z"),
	}

	if _, err := ing.IngestRows(context.Background(), []FWStatementEntry{credit, fee}); err != nil {
		t.Fatalf("IngestRows: %v", err)
	}
	var got models.InboundCredit
	if err := db.First(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.FeeKobo != 0 {
		t.Errorf("FeeKobo = %d, want 0 (fee was its own wallet debit)", got.FeeKobo)
	}
	if got.GrossKobo != 200000 {
		t.Errorf("GrossKobo = %d, want 200000 — the credit row was already gross", got.GrossKobo)
	}
}

func TestPairFundingFees(t *testing.T) {
	netted := func(ref string, amount float64, at string) FWStatementEntry {
		return FWStatementEntry{
			Type: "fee", Direction: "debit", Amount: FWQuoteAmount(amount),
			Balance:         FWStatementBalance{Before: 5000, After: 5000},
			Reference:       ref,
			TransactionDate: stmtTime(at),
		}
	}

	tests := []struct {
		name string
		rows []FWStatementEntry
		want map[int]int64
	}{
		{
			name: "no fee row at all — nothing to add back",
			rows: []FWStatementEntry{
				fundingRow("ref-a", 2000, "Received Money from GTBANK|A B", "2026-09-02T10:00:00Z"),
			},
			want: map[int]int64{},
		},
		{
			name: "shared reference pairs even when the fee row is far away in time",
			rows: []FWStatementEntry{
				fundingRow("ref-b", 2000, "Received Money from GTBANK|A B", "2026-09-02T10:00:00Z"),
				netted("ref-b", 28, "2026-09-02T18:00:00Z"),
			},
			want: map[int]int64{0: 2800},
		},
		{
			name: "no shared reference — paired by proximity and plausible size",
			rows: []FWStatementEntry{
				fundingRow("ref-c", 2000, "Received Money from GTBANK|A B", "2026-09-02T10:00:00Z"),
				netted("", 28, "2026-09-02T10:00:02Z"),
			},
			want: map[int]int64{0: 2800},
		},
		{
			name: "fee too large to be a 1.40% funding charge is some other charge",
			rows: []FWStatementEntry{
				fundingRow("ref-d", 2000, "Received Money from GTBANK|A B", "2026-09-02T10:00:00Z"),
				netted("", 500, "2026-09-02T10:00:02Z"),
			},
			want: map[int]int64{},
		},
		{
			name: "fee outside the pairing window belongs to nothing here",
			rows: []FWStatementEntry{
				fundingRow("ref-e", 2000, "Received Money from GTBANK|A B", "2026-09-02T10:00:00Z"),
				netted("", 28, "2026-09-02T10:30:00Z"),
			},
			want: map[int]int64{},
		},
		{
			name: "two identical credits make the fee ambiguous — pair nothing rather than guess",
			rows: []FWStatementEntry{
				fundingRow("ref-f1", 2000, "Received Money from GTBANK|A B", "2026-09-02T10:00:00Z"),
				fundingRow("ref-f2", 2000, "Received Money from KUDA|C D", "2026-09-02T10:00:00Z"),
				netted("", 28, "2026-09-02T10:00:01Z"),
			},
			want: map[int]int64{},
		},
		{
			name: "different-sized credits disambiguate by expected fee",
			rows: []FWStatementEntry{
				fundingRow("ref-g1", 2000, "Received Money from GTBANK|A B", "2026-09-02T10:00:00Z"),
				fundingRow("ref-g2", 50000, "Received Money from KUDA|C D", "2026-09-02T10:00:00Z"),
				netted("", 700, "2026-09-02T10:00:01Z"),
			},
			want: map[int]int64{1: 70000},
		},
		{
			name: "a fee row is never paired with another fee row",
			rows: []FWStatementEntry{
				netted("", 28, "2026-09-02T10:00:00Z"),
				netted("", 28, "2026-09-02T10:00:01Z"),
			},
			want: map[int]int64{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pairFundingFees(tc.rows)
			if len(got) != len(tc.want) {
				t.Fatalf("pairFundingFees = %v, want %v", got, tc.want)
			}
			for idx, want := range tc.want {
				if got[idx] != want {
					t.Errorf("fee for row %d = %d, want %d", idx, got[idx], want)
				}
			}
		})
	}
}

func TestRunOnce_WindowsAndCurrency(t *testing.T) {
	db := setupInboundDB(t)
	src := &fakeStatementSource{rows: []FWStatementEntry{
		fundingRow("100000000000000000000000000007", 2000, "Received Money from GTBANK|ADEMU EMMANUEL O.", "2026-09-02T10:00:00Z"),
	}}
	ing := NewInboundIngestor(db, src, "ngn", 6*time.Hour)
	pinned := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	ing.now = func() time.Time { return pinned }

	res, err := ing.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("res = %+v, want 1 inserted", res)
	}
	if src.lastCur != "NGN" {
		t.Errorf("currency = %q, want normalized NGN", src.lastCur)
	}
	if !src.from.Equal(pinned.Add(-6*time.Hour)) || !src.to.Equal(pinned) {
		t.Errorf("window = [%s, %s], want a 6h lookback ending now", src.from, src.to)
	}
}

// A provider error must surface, not be swallowed into an empty tick that
// looks like "no money arrived".
func TestRunOnce_FetchErrorSurfaces(t *testing.T) {
	db := setupInboundDB(t)
	src := &fakeStatementSource{err: errors.New("flutterwave 503")}
	ing := NewInboundIngestor(db, src, "NGN", time.Hour)

	if _, err := ing.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce succeeded, want the fetch error propagated")
	}
}

func TestFetchWalletStatement(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/wallets/statement", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok_abc" {
			t.Errorf("missing bearer, got %q", got)
		}
		if got := r.URL.Query().Get("currency"); got != "NGN" {
			t.Errorf("currency = %q, want NGN", got)
		}
		if got := r.URL.Query().Get("from"); got != "2026-09-01" {
			t.Errorf("from = %q, want 2026-09-01", got)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":[
			{"direction":"credit","type":"funding","amount":"1972.00",
			 "balance":{"before":1000,"after":2972},
			 "reference":"100000000000000000000000000008",
			 "remarks":"Received Money from GTBANK|ADEMU EMMANUEL O.",
			 "transaction_date":"2026-09-02T10:00:00Z"},
			{"direction":"debit","type":"fee","amount":28,
			 "balance":{"before":2972,"after":2972},
			 "reference":"100000000000000000000000000008",
			 "transaction_date":"not a date the docs mention"}
		]}`))
	})
	c, _ := newFWTestServer(t, mux)

	rows, err := c.FetchWalletStatement(context.Background(), "ngn",
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FetchWalletStatement: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Amount arrives as a string on one row and a number on the other; both
	// must decode (measured shapes vary field by field).
	if rows[0].Amount.Float64() != 1972 || rows[1].Amount.Float64() != 28 {
		t.Errorf("amounts = %v / %v", rows[0].Amount, rows[1].Amount)
	}
	if rows[0].TransactionDate.IsZero() {
		t.Error("valid transaction_date failed to decode")
	}
	// An unparseable date must not fail the page — that would drop a credit.
	if !rows[1].TransactionDate.IsZero() {
		t.Error("garbage transaction_date should decode to the zero time")
	}
}

func TestFetchWalletStatement_ProviderError(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/wallets/statement", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "failed", "message": "wallet unavailable"})
	})
	c, _ := newFWTestServer(t, mux)

	if _, err := c.FetchWalletStatement(context.Background(), "NGN", time.Time{}, time.Time{}); err == nil {
		t.Fatal("want an error on a 503")
	}
}
