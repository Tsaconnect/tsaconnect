package jobs

import (
	"context"
	"log"
	"time"
)

// billFloatReader reads the prepaid ClubKonnect wallet balance (whole ₦).
type billFloatReader interface {
	WalletBalance(ctx context.Context) (int64, error)
	IsEnabled() bool
}

// BillFloatMonitor watches the company's prepaid Naira float at ClubKonnect.
// Bills silently stop vending when it empties (codes 412/415/416/417), and the
// money to top it up comes from treasury, not from the bill payments themselves
// — so an early, loud alert is the whole point of this worker. Alert-only: it
// never mutates anything.
type BillFloatMonitor struct {
	vendor    billFloatReader
	threshold int64 // whole ₦
	interval  time.Duration
}

func NewBillFloatMonitor(vendor billFloatReader, thresholdNgn int64, interval time.Duration) *BillFloatMonitor {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &BillFloatMonitor{vendor: vendor, threshold: thresholdNgn, interval: interval}
}

func (w *BillFloatMonitor) Name() string            { return "bill-float-monitor" }
func (w *BillFloatMonitor) Interval() time.Duration { return w.interval }

func (w *BillFloatMonitor) Run(ctx context.Context) error {
	if !w.vendor.IsEnabled() || w.threshold <= 0 {
		return nil
	}
	balance, err := w.vendor.WalletBalance(ctx)
	if err != nil {
		return err // logged by the scheduler; transient API failures self-heal
	}
	if balance < w.threshold {
		log.Printf("[bill-float-monitor] ALERT bill_float_low_total=1 balance_ngn=%d threshold_ngn=%d — top up the ClubKonnect wallet", balance, w.threshold)
	}
	return nil
}
