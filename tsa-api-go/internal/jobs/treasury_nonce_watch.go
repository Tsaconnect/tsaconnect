package jobs

import (
	"context"
	"time"
)

// NonceChecker is services.TreasuryNonceWatch.
type NonceChecker interface {
	Check(ctx context.Context) error
}

// TreasuryNonceWatchJob checks the pool lane's nonce every interval (F24). A
// stuck nonce is a failing tick; registered with AlertOnFailureStreak it pages
// on-call (spec §22 "pages on-call").
type TreasuryNonceWatchJob struct {
	w        NonceChecker
	interval time.Duration
}

func NewTreasuryNonceWatchJob(w NonceChecker, interval time.Duration) *TreasuryNonceWatchJob {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &TreasuryNonceWatchJob{w: w, interval: interval}
}

func (j *TreasuryNonceWatchJob) Name() string                  { return "liquidity-treasury-nonce-watch" }
func (j *TreasuryNonceWatchJob) Interval() time.Duration       { return j.interval }
func (j *TreasuryNonceWatchJob) Run(ctx context.Context) error { return j.w.Check(ctx) }
