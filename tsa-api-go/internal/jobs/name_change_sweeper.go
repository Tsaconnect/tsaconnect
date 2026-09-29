package jobs

import (
	"context"
	"log"
	"time"
)

// nameChangeSweeper is the slice of *services.NameChangeService this worker needs.
type nameChangeSweeper interface {
	Sweep(ctx context.Context) (int, error)
}

// NameChangeSweeper drives name changes the requests left unfinished: MCGP fee
// debits whose outcome was unknown, changes out of attempts or time (lapsed,
// their fee kept or returned), and fees a failed settle, reverse or refund
// left hanging. Every move in Sweep is guarded on the status it read, so a
// missed tick only delays one.
//
// Its silent death leaves users' MCGP held and refunds unsent, so it is
// registered with AlertOnFailureStreak.
type NameChangeSweeper struct {
	svc      nameChangeSweeper
	interval time.Duration
}

func NewNameChangeSweeper(svc nameChangeSweeper, interval time.Duration) *NameChangeSweeper {
	if interval <= 0 {
		interval = time.Minute
	}
	return &NameChangeSweeper{svc: svc, interval: interval}
}

func (w *NameChangeSweeper) Name() string            { return "name-change-sweeper" }
func (w *NameChangeSweeper) Interval() time.Duration { return w.interval }

func (w *NameChangeSweeper) Run(ctx context.Context) error {
	n, err := w.svc.Sweep(ctx)
	if n > 0 {
		log.Printf("[name-change] sweep moved=%d", n)
	}
	return err
}
