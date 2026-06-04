package admin

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

// Sweeper periodically transitions expired encoder leases. It is the single
// authoritative caller of db.LeaseExpiredJobs so there's exactly one place
// that decides "this encoder went silent, kick the package back to pending or
// fail it." Run it once per process; concurrent sweepers would race each
// other's transactions and emit duplicate transition logs.
//
// The sweeper is intentionally thin: all lease semantics live in
// db.LeaseExpiredJobs. This struct just times the calls, surfaces results to
// logs, and respects ctx cancellation.
type Sweeper struct {
	DB          *sql.DB
	Interval    time.Duration
	MaxAttempts int
	Now         func() time.Time
	Logger      *log.Logger
}

// NewSweeper applies defaults: 30s interval, 5 max attempts, time.Now,
// log.Default(). Override any field on the returned struct before calling Run.
func NewSweeper(conn *sql.DB) *Sweeper {
	return &Sweeper{
		DB:          conn,
		Interval:    30 * time.Second,
		MaxAttempts: 5,
		Now:         time.Now,
		Logger:      log.Default(),
	}
}

// Run sweeps once immediately (to clean up leases stranded by a process
// crash, the same role recoverStale played in the packager worker startup),
// then ticks on s.Interval until ctx is cancelled. Returns ctx.Err() on exit.
// Errors from a single sweep are logged but do not abort the loop; an
// intermittent DB fault should not stop expiry processing forever.
func (s *Sweeper) Run(ctx context.Context) error {
	s.sweepOnce(ctx, "startup")
	timer := time.NewTimer(s.Interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			s.sweepOnce(ctx, "tick")
			timer.Reset(s.Interval)
		}
	}
}

func (s *Sweeper) sweepOnce(ctx context.Context, source string) {
	nowMs := s.Now().UTC().UnixMilli()
	results, err := db.LeaseExpiredJobs(ctx, s.DB, nowMs, s.MaxAttempts)
	if err != nil {
		s.Logger.Printf("encoder sweeper source=%s ERROR: %v", source, err)
		return
	}
	if len(results) == 0 {
		return
	}
	var pending, failed int
	for _, r := range results {
		switch r.NewStatus {
		case db.PackageStatusPending:
			pending++
		case db.PackageStatusFailed:
			failed++
		}
		s.Logger.Printf("encoder sweeper source=%s package=%s encoder=%s -> %s attempts=%d",
			source, r.PackageID, r.EncoderID, r.NewStatus, r.Attempts)
	}
	s.Logger.Printf("encoder sweeper source=%s expired=%d pending=%d failed=%d",
		source, len(results), pending, failed)
}
