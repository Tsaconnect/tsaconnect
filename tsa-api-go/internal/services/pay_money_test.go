package services

import (
	"errors"
	"testing"
)

func TestPayAmounts_SplitsTheOnePercentByShare(t *testing.T) {
	for _, tc := range []struct {
		share         int
		total, net, f int64
	}{
		{100, 5_050_506, 5_000_000, 50_506}, // customer bears it all: merchant gets the full ₦50,000
		{50, 5_025_253, 4_975_000, 50_253},
		{0, 5_000_000, 4_950_000, 50_000}, // merchant absorbs it
	} {
		got, err := PayAmounts(5_000_000, 100, tc.share)
		if err != nil {
			t.Fatal(err)
		}
		if got.CustomerTotalKobo != tc.total || got.MerchantNetKobo != tc.net || got.FeeKobo != tc.f {
			t.Fatalf("share %d: got %+v, want C=%d N=%d fee=%d", tc.share, got, tc.total, tc.net, tc.f)
		}
	}
}

// At the default share the merchant must get the price to the kobo, for every
// amount — not only round ones.
func TestPayAmounts_FullShareAlwaysPaysTheMerchantThePrice(t *testing.T) {
	for p := int64(100_000); p < 110_000; p++ {
		got, err := PayAmounts(p, 100, 100)
		if err != nil || got.MerchantNetKobo != p {
			t.Fatalf("P=%d: N=%d err=%v", p, got.MerchantNetKobo, err)
		}
	}
	if got, err := PayAmounts(MaxPayAmountKobo, 9_999, 100); err != nil || got.MerchantNetKobo != MaxPayAmountKobo {
		t.Fatalf("max amount: %+v %v", got, err)
	}
}

// Whatever the share: the merchant never gets more than the price, the
// customer never pays less than the merchant gets, and a deposit worth C at
// fee F always covers N.
func TestPayAmounts_Invariants(t *testing.T) {
	for _, share := range []int{0, 1, 33, 50, 99, 100} {
		for p := int64(100_000); p < 101_000; p++ {
			got, _ := PayAmounts(p, 100, share)
			if got.MerchantNetKobo > p || got.CustomerTotalKobo < got.MerchantNetKobo || got.FeeKobo != got.CustomerTotalKobo-got.MerchantNetKobo {
				t.Fatalf("share %d P=%d: %+v", share, p, got)
			}
			if got.CustomerTotalKobo*(10_000-100)/10_000 < got.MerchantNetKobo {
				t.Fatalf("share %d P=%d: a deposit of C pays out less than N", share, p)
			}
		}
	}
}

func TestPayAmounts_ZeroFeeIsIdentity(t *testing.T) {
	got, _ := PayAmounts(123_457, 0, 100)
	if got.CustomerTotalKobo != 123_457 || got.MerchantNetKobo != 123_457 || got.FeeKobo != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestPayAmounts_RejectsBadInputs(t *testing.T) {
	for name, call := range map[string]func() error{
		"zero amount":  func() error { _, err := PayAmounts(0, 100, 100); return err },
		"negative":     func() error { _, err := PayAmounts(-1, 100, 100); return err },
		"too large":    func() error { _, err := PayAmounts(MaxPayAmountKobo+1, 100, 100); return err },
		"share > 100":  func() error { _, err := PayAmounts(100, 100, 101); return err },
		"share < 0":    func() error { _, err := PayAmounts(100, 100, -1); return err },
		"fee >= 100%":  func() error { _, err := PayAmounts(100, 10_000, 100); return err },
		"negative fee": func() error { _, err := PayAmounts(100, -1, 100); return err },
	} {
		if err := call(); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
	if _, err := PayAmounts(0, 100, 100); !errors.Is(err, ErrPayAmountInvalid) {
		t.Fatalf("zero amount: err = %v, want ErrPayAmountInvalid", err)
	}
}
