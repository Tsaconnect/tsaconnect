package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A sell refused with "Instant Transfer pricing is temporarily unavailable"
// left nothing in the logs: EffectiveKoboRates folded every rate-lookup failure
// into that one sentinel and dropped the cause. The user's message has to stay
// generic (the sell handler echoes err.Error()), so the cause belongs in the
// log — otherwise an operator cannot tell a dead feed from a missing market
// anchor from a transient blip.
func TestEffectiveKoboRates_LogsWhyPricingIsUnavailable(t *testing.T) {
	s := newITTestService(t)
	if _, err := s.SetRate(context.Background(), uuid.New(), 20000, 0); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	s.fx = stubFx{err: errors.New("NGN rate unavailable")}
	buf := captureLog(t)

	_, _, _, err := s.EffectiveKoboRates(context.Background())
	if !errors.Is(err, ErrITFxUnavailable) {
		t.Fatalf("err = %v, want ErrITFxUnavailable", err)
	}
	if strings.Contains(err.Error(), "NGN rate unavailable") {
		t.Errorf("cause leaked into the returned error %q — the sell handler shows err.Error() to the user", err)
	}
	out := buf.String()
	if !strings.Contains(out, "it_fx_unavailable_total=1") || !strings.Contains(out, "NGN rate unavailable") {
		t.Fatalf("log did not record the cause:\n%s", out)
	}
}

// The app polls the rate endpoint, so an outage would log on every poll
// without a limit.
func TestEffectiveKoboRates_LogsTheCauseOncePerInterval(t *testing.T) {
	s := newITTestService(t)
	if _, err := s.SetRate(context.Background(), uuid.New(), 20000, 0); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	s.fx = stubFx{err: errors.New("feed down")}
	buf := captureLog(t)

	for i := 0; i < 5; i++ {
		_, _, _, _ = s.EffectiveKoboRates(context.Background())
	}
	if n := strings.Count(buf.String(), "it_fx_unavailable_total=1"); n != 1 {
		t.Errorf("logged %d times across 5 calls, want 1", n)
	}
}

// A zero or negative rate is its own failure and must say so, not read as an
// error with an empty cause.
func TestEffectiveKoboRates_LogsANonPositiveRate(t *testing.T) {
	s := newITTestService(t)
	if _, err := s.SetRate(context.Background(), uuid.New(), 20000, 0); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	s.fx = stubFx{ngn: 0}
	buf := captureLog(t)

	if _, _, _, err := s.EffectiveKoboRates(context.Background()); !errors.Is(err, ErrITFxUnavailable) {
		t.Fatalf("err = %v, want ErrITFxUnavailable", err)
	}
	if !strings.Contains(buf.String(), "non-positive") {
		t.Errorf("log did not say the rate was non-positive:\n%s", buf.String())
	}
}

// The stale branch refuses money paths while the display tolerates the rate,
// which is the likeliest reason a sale fails with a healthy-looking feed. It
// must say so too.
func TestCreateBuyIntent_LogsAStaleRateRefusal(t *testing.T) {
	s, user, _ := newVATestService(t)
	s.fx = stubFx{ngn: 1500, stale: true}
	buf := captureLog(t)

	_, err := s.CreateBuyIntent(context.Background(), user.ID, user.WalletAddress, "100000", nil)
	if !errors.Is(err, ErrITFxUnavailable) {
		t.Fatalf("err = %v, want ErrITFxUnavailable", err)
	}
	if !strings.Contains(buf.String(), "it_fx_unavailable_total=1") || !strings.Contains(buf.String(), "stale") {
		t.Errorf("stale refusal not logged:\n%s", buf.String())
	}
}

// The sell's stale check moved out of CreateSellIntent into the shared sell
// pricing (sellRate, used by the submit and both quote directions). The log
// has to live there too, or the sell — the path this exists for — refuses
// silently again.
func TestQuoteSell_LogsAStaleRateRefusal(t *testing.T) {
	s := newITTestService(t)
	if _, err := s.SetRate(context.Background(), uuid.New(), 20000, 0); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	s.fx = stubFx{ngn: 1500, stale: true}
	buf := captureLog(t)

	oneMCGP := new(big.Int).Set(oneE18)
	_, err := s.QuoteSell(context.Background(), oneMCGP, "NGN")
	if !errors.Is(err, ErrITFxUnavailable) {
		t.Fatalf("err = %v, want ErrITFxUnavailable", err)
	}
	if !strings.Contains(buf.String(), "it_fx_unavailable_total=1") || !strings.Contains(buf.String(), "sell refused") {
		t.Errorf("stale sell refusal not logged:\n%s", buf.String())
	}
}
