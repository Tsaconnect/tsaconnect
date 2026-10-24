package jobs

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

type recordingStockNotifier struct{ got []services.StockLowAlert }

func (r *recordingStockNotifier) NotifyStockLow(_ context.Context, a services.StockLowAlert) error {
	r.got = append(r.got, a)
	return nil
}

type mutableFloats struct {
	floats []services.SponsorFloat
	err    error
}

func (m *mutableFloats) Enabled() bool { return true }
func (m *mutableFloats) FloatBalances(context.Context) ([]services.SponsorFloat, error) {
	return m.floats, m.err
}

// §5.3: below $50 free → ALERT + email, at most daily; one recovery email.
func TestGasFloatMonitor_TokenLowAlertsDailyAndRecovers(t *testing.T) {
	src := &mutableFloats{floats: []services.SponsorFloat{{Chain: "base", Address: "0xw",
		BalanceWei: big.NewInt(10_000), FloorWei: big.NewInt(1_000), Tokens: map[string]float64{"USDC": 41.2, "USDT": 900}}}}
	n := &recordingStockNotifier{}
	m := NewGasFloatMonitorNamed("ramp_company_wallet", src, 5*time.Minute).WithTokenLowAlert(50, n)
	start := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	clock := start
	m.now = func() time.Time { return clock }
	ctx := context.Background()

	out := captureLog(t, func() { _ = m.Run(ctx) })
	if !strings.Contains(out, "ALERT ramp_company_wallet_stock_low chain=base token=USDC free_usd=41.20") || strings.Contains(out, "token=USDT") {
		t.Fatalf("log:\n%s", out)
	}
	if len(n.got) != 1 || n.got[0].Asset != "USDC" || n.got[0].Recovered {
		t.Fatalf("emails = %+v", n.got)
	}
	clock = start.Add(61 * time.Minute)
	_ = m.Run(ctx)
	if len(n.got) != 1 {
		t.Fatal("re-emailed inside the day")
	}
	clock = start.Add(25 * time.Hour)
	_ = m.Run(ctx)
	if len(n.got) != 2 {
		t.Fatalf("no daily repeat: %d", len(n.got))
	}
	src.floats[0].Tokens["USDC"] = 250 // refilled
	_ = m.Run(ctx)
	if len(n.got) != 3 || !n.got[2].Recovered {
		t.Fatalf("no recovery email: %+v", n.got)
	}
	_ = m.Run(ctx)
	if len(n.got) != 3 {
		t.Fatal("a second recovery email")
	}
}

// §5.3: a low native balance keeps its float lines and is emailed too.
func TestGasFloatMonitor_NativeLowIsEmailedWhenTheTokenAlertIsOn(t *testing.T) {
	src := &mutableFloats{floats: []services.SponsorFloat{{Chain: "base", Address: "0xw",
		BalanceWei: big.NewInt(1_500), FloorWei: big.NewInt(1_000), Tokens: map[string]float64{"USDC": 900}}}}
	n := &recordingStockNotifier{}
	m := NewGasFloatMonitorNamed("ramp_company_wallet", src, 0).WithTokenLowAlert(50, n)
	out := captureLog(t, func() { _ = m.Run(context.Background()) })
	if !strings.Contains(out, "ramp_company_wallet_float_low_total=1 chain=base") {
		t.Fatalf("log:\n%s", out)
	}
	if len(n.got) != 1 || n.got[0].Asset != "native gas" {
		t.Fatalf("emails = %+v", n.got)
	}
}

// A read error never fails the tick: it is logged redacted and what was
// readable is still alerted on.
func TestGasFloatMonitor_TokenAlertTickSurvivesAReadError(t *testing.T) {
	src := &mutableFloats{
		floats: []services.SponsorFloat{{Chain: "base", Address: "0xw", Tokens: map[string]float64{"USDC": 1}}},
		err:    errors.New("dial https://rpc.example/v2/SECRETKEY: refused"),
	}
	n := &recordingStockNotifier{}
	m := NewGasFloatMonitorNamed("ramp_company_wallet", src, 0).WithTokenLowAlert(50, n)
	var runErr error
	out := captureLog(t, func() { runErr = m.Run(context.Background()) })
	if runErr != nil {
		t.Fatalf("tick failed: %v", runErr)
	}
	if strings.Contains(out, "SECRETKEY") || !strings.Contains(out, "float read") || len(n.got) != 1 {
		t.Fatalf("log:\n%s\nemails=%+v", out, n.got)
	}
}

// Every other registration is unchanged: no Tokens, no emails.
func TestGasFloatMonitor_WithoutTheTokenAlertSendsNothing(t *testing.T) {
	m := NewGasFloatMonitor(fakeFloats{enabled: true, floats: []services.SponsorFloat{
		{Chain: "base", Address: "0xa", BalanceWei: big.NewInt(1_500), FloorWei: big.NewInt(1_000)}}}, 0)
	if out := captureLog(t, func() { _ = m.Run(context.Background()) }); strings.Contains(out, "stock_low") {
		t.Fatalf("log:\n%s", out)
	}
}
