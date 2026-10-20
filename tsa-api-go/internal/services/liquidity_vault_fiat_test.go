package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// $10 deposited at ₦1,365/$ is recorded as ₦13,650; the claim is then priced
// at the rate of the day and a withdrawal takes its share at the locked rate.
func TestVaultLP_FiatDepositLocksTheRateAndPricesTheClaim(t *testing.T) {
	r := newLPRig(t)
	ctx := context.Background()
	user, wallet := r.activeLP(t)

	if _, err := r.svc.SetFiatMode(ctx, user, LiquidityFiatNGN); !errors.Is(err, ErrLPFiatUnavailable) {
		t.Fatalf("no rate source: err = %v, want ErrLPFiatUnavailable", err)
	}
	if _, err := r.svc.SetFiatMode(ctx, user, "EUR"); !errors.Is(err, ErrLPFiatCurrency) {
		t.Fatalf("EUR: err = %v, want ErrLPFiatCurrency", err)
	}

	rate := big.NewRat(1365, 1)
	rateFn := func(context.Context, string) (*big.Rat, error) { return new(big.Rat).Set(rate), nil }
	r.svc.c.FiatRate = rateFn
	me, err := r.svc.SetFiatMode(ctx, user, LiquidityFiatNGN)
	if err != nil || me.FiatCurrency != LiquidityFiatNGN {
		t.Fatalf("set fiat: %+v %v", me, err)
	}

	// The indexer stamps the deposit with the day's rate.
	x, _, _ := newIndexerRigOn(t, r.db,
		ledgerLog(t, 150, 0, "Deposited", []common.Hash{lpTopic(wallet)}, usdt18(5)))
	x.SetFiat(rateFn, 18)
	x.now = blockClock(150)
	if n, err := x.IndexOnce(ctx); err != nil || n != 1 {
		t.Fatalf("indexed %d %v", n, err)
	}
	var row models.LiquidityLedgerEntry
	if err := r.db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.FiatCurrency != "NGN" || row.FiatAmount == nil || row.FiatRate == nil {
		t.Fatalf("stamp = %q %v %v, want NGN", row.FiatCurrency, row.FiatAmount, row.FiatRate)
	}
	// SQLite may drop trailing zeros that Postgres keeps.
	if a, _ := new(big.Rat).SetString(*row.FiatAmount); a.Cmp(big.NewRat(6825, 1)) != 0 {
		t.Fatalf("fiat amount = %s, want 6825", *row.FiatAmount)
	}

	// Naira weakens to 1,500: the ₦6,825 claim is worth 4.55 USDT of the 5 held.
	r.vault.principal[strings.ToLower(wallet)] = usdt18(5)
	rate.SetInt64(1500)
	me, err = r.svc.Me(ctx, user)
	if err != nil || me.Fiat == nil {
		t.Fatalf("me: %+v %v", me, err)
	}
	f := me.Fiat
	if f.Principal != "6825.00" || f.Units != "5" || f.AvgRate != "1365.00" || f.CurrentUSDT != "4.55" || f.GapUSDT != "-0.45" {
		t.Fatalf("fiat view = %+v", f)
	}
}

// A user who never switched on naira gets no fiat view, and a deposit by a
// non-fiat wallet is not stamped.
func TestVaultLP_UsdtLPIsNotStamped(t *testing.T) {
	r := newLPRig(t)
	ctx := context.Background()
	user, wallet := r.activeLP(t)
	x, _, _ := newIndexerRigOn(t, r.db,
		ledgerLog(t, 150, 0, "Deposited", []common.Hash{lpTopic(wallet)}, usdt18(5)))
	x.SetFiat(func(context.Context, string) (*big.Rat, error) { return big.NewRat(1365, 1), nil }, 18)
	x.now = blockClock(150)
	if _, err := x.IndexOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var row models.LiquidityLedgerEntry
	r.db.First(&row)
	if row.FiatCurrency != "" || row.FiatAmount != nil {
		t.Fatalf("stamped a USDT LP: %+v", row)
	}
	if me, _ := r.svc.Me(ctx, user); me.Fiat != nil {
		t.Fatalf("fiat view for a USDT LP: %+v", me.Fiat)
	}
}

// blockClock is "now" a minute after block n's fake header time.
func blockClock(n uint64) func() time.Time {
	return func() time.Time { return time.Unix(1_760_000_000+int64(n)+60, 0) }
}

func stamped(kind string, units int64, naira string) models.LiquidityLedgerEntry {
	a := usdt18(units).String()
	r := models.LiquidityLedgerEntry{Kind: kind, Amount: &a}
	if naira != "" {
		r.FiatCurrency, r.FiatAmount = "NGN", &naira
	}
	return r
}

// Withdrawals take the stamped share pro rata, so 100 USDT + 10 stamped, then
// 11 withdrawn, leaves 9 stamped at the same locked rate — whichever came out.
func TestFoldFiatLedger_WithdrawalsAreProRata(t *testing.T) {
	pos := foldFiatLedger([]models.LiquidityLedgerEntry{
		stamped(VaultEventDeposited, 100, ""),
		stamped(VaultEventDeposited, 10, "13650.00"),
		stamped(VaultEventWithdrawn, 11, ""),
	})
	if pos.units.Cmp(new(big.Rat).SetInt(usdt18(9))) != 0 || pos.naira.Cmp(big.NewRat(12285, 1)) != 0 {
		t.Fatalf("units %s naira %s, want 9 USDT / ₦12,285", pos.units.FloatString(0), pos.naira.FloatString(2))
	}
}

// Going back to USDT must not hide the naira still owed.
func TestVaultLP_SwitchingBackKeepsTheNairaPosition(t *testing.T) {
	r := newLPRig(t)
	ctx := context.Background()
	user, wallet := r.activeLP(t)
	rateFn := func(context.Context, string) (*big.Rat, error) { return big.NewRat(1365, 1), nil }
	r.svc.c.FiatRate = rateFn
	if _, err := r.svc.SetFiatMode(ctx, user, LiquidityFiatNGN); err != nil {
		t.Fatal(err)
	}
	x, _, _ := newIndexerRigOn(t, r.db, ledgerLog(t, 150, 0, "Deposited", []common.Hash{lpTopic(wallet)}, usdt18(5)))
	x.SetFiat(rateFn, 18)
	x.now = blockClock(150)
	if _, err := x.IndexOnce(ctx); err != nil {
		t.Fatal(err)
	}
	r.vault.principal[strings.ToLower(wallet)] = usdt18(5)
	me, err := r.svc.SetFiatMode(ctx, user, "")
	if err != nil || me.FiatCurrency != "" || me.Fiat == nil || me.Fiat.Units != "5" {
		t.Fatalf("after switching back: %+v %v", me, err)
	}
	fx, err := (&ITService{db: r.db}).fiatExposure(ctx, poolTestChain, testVaultAddr.Hex(), 18)
	if err != nil || fx == nil || fx.LPs != 1 {
		t.Fatalf("exposure = %+v %v, want the one LP", fx, err)
	}
}

// A rate outage stalls a recent deposit (retry) but never prices an old one
// at today's rate.
func TestVaultIndexer_FiatStampBoundedByLag(t *testing.T) {
	r := newLPRig(t)
	ctx := context.Background()
	user, wallet := r.activeLP(t)
	r.svc.c.FiatRate = func(context.Context, string) (*big.Rat, error) { return big.NewRat(1365, 1), nil }
	if _, err := r.svc.SetFiatMode(ctx, user, LiquidityFiatNGN); err != nil {
		t.Fatal(err)
	}
	down := func(context.Context, string) (*big.Rat, error) { return nil, errors.New("rate feed down") }
	x, _, _ := newIndexerRigOn(t, r.db, ledgerLog(t, 150, 0, "Deposited", []common.Hash{lpTopic(wallet)}, usdt18(5)))
	x.SetFiat(down, 18)
	x.now = blockClock(150)
	if _, err := x.IndexOnce(ctx); err == nil {
		t.Fatal("a recent fiat deposit with the rate down should fail the tick")
	}
	x.now = func() time.Time { return time.Unix(1_760_000_150, 0).Add(2 * time.Hour) }
	if n, err := x.IndexOnce(ctx); err != nil || n != 1 {
		t.Fatalf("an old deposit should index unstamped: %d %v", n, err)
	}
	var row models.LiquidityLedgerEntry
	r.db.First(&row)
	if row.FiatAmount != nil {
		t.Fatalf("old deposit stamped at today's rate: %v", *row.FiatAmount)
	}
}

func TestSignedMoney_NeverNegativeZero(t *testing.T) {
	if got := signedMoney(big.NewRat(-1, 1000)); got != "0.00" {
		t.Fatalf("got %q", got)
	}
	if got := signedMoney(big.NewRat(-45, 100)); got != "-0.45" {
		t.Fatalf("got %q", got)
	}
}
