package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

const (
	fiatDesk   = "0x62bA000000000000000000000000000000001b96"
	fiatLPAddr = "0x00000000000000000000000000000000000000cc"
	fiatTxA    = "0x" + "aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11"
	fiatTxB    = "0x" + "bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22"
	fiatTxC    = "0x" + "cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33"
)

// usdt6 is whole × 10^6 (the tests run a 6-dp token so SQLite keeps it exact).
func usdt6(whole int64) *big.Int { return new(big.Int).Mul(big.NewInt(whole), big.NewInt(1_000_000)) }

type fakeFiatVerifier struct {
	// by tx hash: the amount credited, or an error
	amount map[string]*big.Int
	err    map[string]error
	// last from/to asked, to assert direction
	from, to string
}

func (f *fakeFiatVerifier) VerifyTransferFrom(_ context.Context, tx, _ string, from, to string) (*big.Int, error) {
	f.from, f.to = from, to
	if e := f.err[tx]; e != nil {
		return nil, e
	}
	return f.amount[tx], nil
}

type fiatRig struct {
	db   *gorm.DB
	v    *fakeFiatVerifier
	svc  *FiatLPService
	rate *big.Rat // NGN per $1
	kes  *big.Rat // KES per $1
	now  time.Time
}

func newFiatRig(t *testing.T) *fiatRig {
	t.Helper()
	db := setupTradeDB(t)
	if err := db.AutoMigrate(&models.LiquidityLedgerEntry{}, &models.FiatLPDeposit{}, &models.FiatLPWithdrawal{}, &models.P2PAuditLog{}); err != nil {
		t.Fatal(err)
	}
	r := &fiatRig{db: db, v: &fakeFiatVerifier{amount: map[string]*big.Int{}, err: map[string]error{}}, rate: big.NewRat(1365, 1), kes: big.NewRat(129, 1),
		now: time.Unix(1_760_000_000, 0).UTC()}
	svc, err := NewFiatLPService(FiatLPConfig{DB: db, Vault: testVaultAddr.Hex(), Verifier: r.v,
		Token:      PoolToken{ChainID: 56, ChainKey: "bsc", Symbol: "USDT", Address: poolTestUSDT, Decimals: 6},
		Desk:       func(context.Context) (string, error) { return fiatDesk, nil },
		Currencies: func(context.Context) []string { return []string{"NGN", "KES", "XXX"} },
		Rate: func(_ context.Context, c string) (*big.Rat, error) {
			switch c {
			case "NGN":
				return new(big.Rat).Set(r.rate), nil
			case "KES":
				return new(big.Rat).Set(r.kes), nil
			}
			return nil, errors.New("no rate") // XXX is listed but unpriced
		},
		TxTime: func(context.Context, string) (time.Time, error) { return r.now, nil },
		Now:    func() time.Time { return r.now }})
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	return r
}

func (r *fiatRig) deposit(t *testing.T, user uuid.UUID, tx string, whole int64) *models.FiatLPDeposit {
	t.Helper()
	r.v.amount[tx] = usdt6(whole)
	d, err := r.svc.ConfirmDeposit(context.Background(), user, fiatLPAddr, tx, "NGN")
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}
	return d
}

// $10 sent to the desk is recorded as ₦13,650, once per transaction.
func TestFiatLP_DepositLocksTheRateAndIsIdempotent(t *testing.T) {
	r := newFiatRig(t)
	ctx := context.Background()
	user := uuid.New()
	d := r.deposit(t, user, fiatTxA, 10)
	if d.Fiat != "13650.00" || d.Units != usdt6(10).String() {
		t.Fatalf("deposit = %+v, want ₦13,650 for 10 USDT", d)
	}
	if !strings.EqualFold(r.v.to, fiatDesk) || !strings.EqualFold(r.v.from, fiatLPAddr) {
		t.Fatalf("verified %s → %s, want LP → desk", r.v.from, r.v.to)
	}
	again, err := r.svc.ConfirmDeposit(ctx, user, fiatLPAddr, fiatTxA, "NGN")
	if err != nil || again.ID != d.ID {
		t.Fatalf("repeat should return the same row: %v", err)
	}
	if _, err := r.svc.ConfirmDeposit(ctx, uuid.New(), fiatLPAddr, fiatTxA, "NGN"); !errors.Is(err, ErrFiatLPTaken) {
		t.Fatalf("another user's claim on the same tx: %v, want ErrFiatLPTaken", err)
	}
	r.rate.SetInt64(1500) // the rate moves: the locked claim does not
	pos, err := r.svc.Position(ctx, user)
	if err != nil || len(pos.Positions) != 1 || pos.Positions[0].Principal != "13650.00" || pos.Positions[0].AvgRate != "1365.00" || pos.Positions[0].TodayUSDT != "9.10" {
		t.Fatalf("position = %+v %v", pos, err)
	}
}

func TestFiatLP_DepositRefusals(t *testing.T) {
	r := newFiatRig(t)
	ctx := context.Background()
	user := uuid.New()
	if _, err := r.svc.ConfirmDeposit(ctx, user, fiatLPAddr, "0x12", "NGN"); !errors.Is(err, ErrFiatLPTxHash) {
		t.Fatalf("bad hash: %v", err)
	}
	r.v.err[fiatTxA] = ErrTransferNotFound
	if _, err := r.svc.ConfirmDeposit(ctx, user, fiatLPAddr, fiatTxA, "NGN"); !errors.Is(err, ErrFiatLPUnproven) {
		t.Fatalf("not mined: %v", err)
	}
	r.v.err[fiatTxB] = ErrTransferReverted
	if _, err := r.svc.ConfirmDeposit(ctx, user, fiatLPAddr, fiatTxB, "NGN"); !errors.Is(err, ErrFiatLPRefuted) {
		t.Fatalf("reverted: %v", err)
	}
	r.v.amount[fiatTxC] = big.NewInt(500_000) // $0.50
	if _, err := r.svc.ConfirmDeposit(ctx, user, fiatLPAddr, fiatTxC, "NGN"); !errors.Is(err, ErrFiatLPTooSmall) {
		t.Fatalf("dust: %v", err)
	}
}

// A principal request reserves its naira and its share of the earning units
// at once; a rejection gives both back; asking for more than owed is refused.
func TestFiatLP_PrincipalRequestReservesAndRejectReturns(t *testing.T) {
	r := newFiatRig(t)
	ctx := context.Background()
	user := uuid.New()
	r.deposit(t, user, fiatTxA, 10)
	if _, err := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindPrincipal, "NGN", "13650.01"); !errors.Is(err, ErrFiatLPAmount) {
		t.Fatalf("over-ask: %v", err)
	}
	w, err := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindPrincipal, "NGN", "6825")
	if err != nil || *w.Fiat != "6825.00" || *w.UnitsReleased != usdt6(5).String() {
		t.Fatalf("half: %+v %v", w, err)
	}
	pos, _ := r.svc.Position(ctx, user)
	if pos.Positions[0].Principal != "6825.00" || pos.Positions[0].Units != "5" {
		t.Fatalf("after reserving half: %+v", pos)
	}
	if _, err := r.svc.Reject(ctx, uuid.New(), w.ID, "wrong wallet"); err != nil {
		t.Fatal(err)
	}
	if pos, _ = r.svc.Position(ctx, user); pos.Positions[0].Principal != "13650.00" || pos.Positions[0].Units != "10" {
		t.Fatalf("after reject: %+v", pos)
	}
	if _, err := r.svc.Reject(ctx, uuid.New(), w.ID, ""); !errors.Is(err, ErrFiatLPState) {
		t.Fatalf("second reject: %v", err)
	}
}

// The admin pays at the day's rate: ₦6,825 at ₦1,500 is 4.55 USDT. Paying
// more than 1% short is refused; a proper payment closes it once.
func TestFiatLP_MarkPaidAtTheDaysRate(t *testing.T) {
	r := newFiatRig(t)
	ctx := context.Background()
	user, admin := uuid.New(), uuid.New()
	r.deposit(t, user, fiatTxA, 10)
	w, _ := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindPrincipal, "NGN", "6825")
	r.rate.SetInt64(1500)
	_, list, err := r.svc.AdminOverview(ctx, models.FiatLPRequested)
	if err != nil || len(list) != 1 || list[0].QuoteUSDT != "4.55" || list[0].Rate != "1500.00" {
		t.Fatalf("quote = %+v %v", list, err)
	}
	r.v.amount[fiatTxB] = usdt6(4) // short by far more than 1%
	if _, err := r.svc.MarkPaid(ctx, admin, w.ID, fiatTxB); !errors.Is(err, ErrFiatLPShort) {
		t.Fatalf("short payment: %v", err)
	}
	if !strings.EqualFold(r.v.from, fiatDesk) || !strings.EqualFold(r.v.to, fiatLPAddr) {
		t.Fatalf("verified %s → %s, want desk → LP wallet", r.v.from, r.v.to)
	}
	r.v.amount[fiatTxC] = big.NewInt(4_550_000)
	got, err := r.svc.MarkPaid(ctx, admin, w.ID, fiatTxC)
	if err != nil || got.Status != models.FiatLPPaid || got.PaidUnits == nil {
		t.Fatalf("paid: %+v %v", got, err)
	}
	if _, err := r.svc.MarkPaid(ctx, admin, w.ID, fiatTxC); !errors.Is(err, ErrFiatLPState) {
		t.Fatalf("paying twice: %v", err)
	}
	var audits int64
	r.db.Model(&models.P2PAuditLog{}).Where("action = ?", fiatAuditPaid).Count(&audits)
	if audits != 1 {
		t.Fatalf("audit rows = %d, want 1", audits)
	}
}

func ledgerRow(kind, account string, block uint64, at time.Time, amount, spread *int64) models.LiquidityLedgerEntry {
	e := models.LiquidityLedgerEntry{ID: uuid.New(), ChainID: 56, Vault: strings.ToLower(testVaultAddr.Hex()), TxHash: uuid.NewString(),
		LogIndex: uint(block), Block: block, Kind: kind, Account: account, BlockTime: at}
	if amount != nil {
		s := big.NewInt(*amount).String()
		e.Amount = &s
	}
	if spread != nil {
		s := big.NewInt(*spread).String()
		e.Spread = &s
	}
	return e
}

// Earnings are the pool share the LP's USDT would have earned: a fill's spread
// × their units over the pool's principal with the desk's real position
// replaced by the fiat LPs' units. 10 of 100 virtual earns a tenth of 2 USDT.
func TestFiatLP_EarningsAreTheVirtualPoolShare(t *testing.T) {
	r := newFiatRig(t)
	ctx := context.Background()
	user := uuid.New()
	ninety, two := usdt6(90).Int64(), usdt6(2).Int64()
	r.db.Create(&[]models.LiquidityLedgerEntry{
		ledgerRow(VaultEventDeposited, "0x00000000000000000000000000000000000000dd", 10, r.now.Add(-2*time.Hour), &ninety, nil),
		ledgerRow(VaultEventFilled, "", 30, r.now.Add(time.Hour), nil, &two),
	})
	r.deposit(t, user, fiatTxA, 10) // confirmed at r.now, before the fill
	r.now = r.now.Add(2 * time.Hour)
	r.svc.forgetEarnings()
	pos, err := r.svc.Position(ctx, user)
	if err != nil || pos.Earned != "0.2" || pos.EarningsAvailable != "0.2" || pos.EarningsValue["NGN"] != "273.00" {
		t.Fatalf("earnings = %+v %v, want 0.2 USDT / ₦273", pos, err)
	}
	// Ask for part of it; the rest stays available; below the minimum is refused.
	if _, err := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindEarnings, "NGN", "0.2"); !errors.Is(err, ErrFiatLPAmount) {
		t.Fatalf("below the 0.50 minimum: %v", err)
	}
}

// A desk that really holds the position must not be counted twice: the real
// desk units are swapped out for the fiat LPs' units.
func TestFiatLP_DeskPositionIsNotCountedTwice(t *testing.T) {
	r := newFiatRig(t)
	ctx := context.Background()
	user := uuid.New()
	ninety, ten, two := usdt6(90).Int64(), usdt6(10).Int64(), usdt6(2).Int64()
	r.db.Create(&[]models.LiquidityLedgerEntry{
		ledgerRow(VaultEventDeposited, "0x00000000000000000000000000000000000000dd", 10, r.now.Add(-2*time.Hour), &ninety, nil),
		ledgerRow(VaultEventDeposited, fiatDesk, 20, r.now.Add(time.Minute), &ten, nil), // the desk deploys the 10
		ledgerRow(VaultEventFilled, "", 30, r.now.Add(time.Hour), nil, &two),
	})
	r.deposit(t, user, fiatTxA, 10)
	r.now = r.now.Add(2 * time.Hour)
	r.svc.forgetEarnings()
	pos, err := r.svc.Position(ctx, user)
	if err != nil || pos.Earned != "0.2" {
		t.Fatalf("earned = %+v %v, want 0.2 (10/100 of 2), not 10/110", pos, err)
	}
}

// Confirming long after the transfer would let an LP pick a better rate: refused.
func TestFiatLP_StaleTransferIsNotRecordedAtTodaysRate(t *testing.T) {
	r := newFiatRig(t)
	var minted time.Time
	r.svc.c.TxTime = func(context.Context, string) (time.Time, error) { return minted, nil }
	r.v.amount[fiatTxA] = usdt6(10)
	minted = r.now.Add(-time.Hour)
	if _, err := r.svc.ConfirmDeposit(context.Background(), uuid.New(), fiatLPAddr, fiatTxA, "NGN"); !errors.Is(err, ErrFiatLPStale) {
		t.Fatalf("an hour-old transfer: %v, want ErrFiatLPStale", err)
	}
	minted = r.now.Add(-2 * time.Minute)
	if _, err := r.svc.ConfirmDeposit(context.Background(), uuid.New(), fiatLPAddr, fiatTxA, "NGN"); err != nil {
		t.Fatalf("a fresh transfer: %v", err)
	}
}

// An LP who is no longer eligible (KYC reset) cannot add USDT but can always
// ask for their money back: the custody promise holds here too.
func TestFiatLP_ExitIsNeverGatedOnEligibility(t *testing.T) {
	r := newFiatRig(t)
	ctx := context.Background()
	user := uuid.New()
	r.deposit(t, user, fiatTxA, 10)
	r.svc.c.CanLP = func(context.Context, uuid.UUID) error { return ErrLPNotEligible }
	r.v.amount[fiatTxB] = usdt6(5)
	if _, err := r.svc.ConfirmDeposit(ctx, user, fiatLPAddr, fiatTxB, "NGN"); !errors.Is(err, ErrLPNotEligible) {
		t.Fatalf("deposit while ineligible: %v", err)
	}
	pos, err := r.svc.Position(ctx, user)
	if err != nil || pos.Eligible {
		t.Fatalf("position = %+v %v, want Eligible=false", pos, err)
	}
	if _, err := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindPrincipal, "NGN", "13650"); err != nil {
		t.Fatalf("exit while ineligible: %v", err)
	}
}

// A transfer that already closed one request cannot close another.
func TestFiatLP_OnePaymentCannotCloseTwoRequests(t *testing.T) {
	r := newFiatRig(t)
	ctx := context.Background()
	user, admin := uuid.New(), uuid.New()
	r.deposit(t, user, fiatTxA, 20)
	w1, _ := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindPrincipal, "NGN", "1365")
	w2, _ := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindPrincipal, "NGN", "1365")
	r.v.amount[fiatTxC] = usdt6(1)
	if _, err := r.svc.MarkPaid(ctx, admin, w1.ID, fiatTxC); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.MarkPaid(ctx, admin, w2.ID, fiatTxC); !errors.Is(err, ErrFiatLPTaken) {
		t.Fatalf("second request on the same tx: %v, want ErrFiatLPTaken", err)
	}
}

// A position can be held in any priced currency; each is locked at its own
// rate and withdrawn from separately. An unpriced or unknown currency is refused.
func TestFiatLP_PositionsPerCurrency(t *testing.T) {
	r := newFiatRig(t)
	ctx := context.Background()
	user := uuid.New()
	r.deposit(t, user, fiatTxA, 10) // NGN
	r.v.amount[fiatTxB] = usdt6(20)
	d, err := r.svc.ConfirmDeposit(ctx, user, fiatLPAddr, fiatTxB, "kes") // case-insensitive
	if err != nil || d.Currency != "KES" || d.Fiat != "2580.00" {
		t.Fatalf("KES deposit = %+v %v, want 20 USDT = KSh 2,580", d, err)
	}
	r.v.amount[fiatTxC] = usdt6(5)
	if _, err := r.svc.ConfirmDeposit(ctx, user, fiatLPAddr, fiatTxC, "JPY"); !errors.Is(err, ErrFiatLPCurrency) {
		t.Fatalf("unsupported currency: %v", err)
	}
	if _, err := r.svc.ConfirmDeposit(ctx, user, fiatLPAddr, fiatTxC, "XXX"); !errors.Is(err, ErrFiatLPUnavailable) {
		t.Fatalf("listed but unpriced currency: %v, want ErrFiatLPUnavailable", err)
	}
	pos, err := r.svc.Position(ctx, user)
	if err != nil || len(pos.Positions) != 2 || len(pos.Currencies) != 2 {
		t.Fatalf("position = %+v %v, want NGN and KES lines and two priced currencies", pos, err)
	}
	if pos.Positions[0].Currency != "KES" || pos.Positions[1].Currency != "NGN" {
		t.Fatalf("lines not sorted by code: %+v", pos.Positions)
	}
	// A KES exit cannot take from the NGN balance, and is quoted at the KES rate.
	if _, err := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindPrincipal, "KES", "2580.01"); !errors.Is(err, ErrFiatLPAmount) {
		t.Fatalf("over the KES balance: %v", err)
	}
	if _, err := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindPrincipal, "GHS", "1"); !errors.Is(err, ErrFiatLPAmount) {
		t.Fatalf("a currency with no balance: %v", err)
	}
	w, err := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindPrincipal, "KES", "1290")
	if err != nil || w.Currency != "KES" || *w.UnitsReleased != usdt6(10).String() {
		t.Fatalf("KES half exit: %+v %v", w, err)
	}
	sum, list, err := r.svc.AdminOverview(ctx, models.FiatLPRequested)
	if err != nil || len(sum.Currencies) != 2 || len(list) != 1 || list[0].QuoteUSDT != "10" || list[0].Rate != "129.00" {
		t.Fatalf("overview = %+v %+v %v", sum, list, err)
	}
	if sum.Currencies[0].Currency != "KES" || sum.Currencies[0].Fiat != "1290.00" || sum.Currencies[0].Units != "10" {
		t.Fatalf("KES summary = %+v", sum.Currencies[0])
	}
}

// An exit written by the naira-only version has no currency: it still counts
// against the NGN balance, so the same naira cannot be withdrawn twice.
func TestFiatLP_LegacyExitWithoutCurrencyNetsAgainstNGN(t *testing.T) {
	r := newFiatRig(t)
	ctx := context.Background()
	user := uuid.New()
	r.deposit(t, user, fiatTxA, 10) // ₦13,650
	fiat, rel := "13650.00", usdt6(10).String()
	r.db.Create(&models.FiatLPWithdrawal{ID: uuid.New(), UserID: user, Wallet: fiatLPAddr, Kind: models.FiatLPKindPrincipal,
		Status: models.FiatLPRequested, Fiat: &fiat, UnitsReleased: &rel, RequestedAt: r.now}) // no Currency
	if _, err := r.svc.RequestWithdrawal(ctx, user, fiatLPAddr, models.FiatLPKindPrincipal, "NGN", "1"); !errors.Is(err, ErrFiatLPAmount) {
		t.Fatalf("a second exit of money already reserved: %v, want ErrFiatLPAmount", err)
	}
	sum, list, err := r.svc.AdminOverview(ctx, models.FiatLPRequested)
	if err != nil || len(sum.Currencies) != 1 || sum.Currencies[0].Fiat != "0.00" || len(list) != 1 || list[0].QuoteUSDT != "10" {
		t.Fatalf("overview = %+v %+v %v, want NGN owed 0 and the legacy exit quoted at the NGN rate", sum, list, err)
	}
}
