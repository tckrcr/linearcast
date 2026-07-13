package admin

import (
	"context"
	"database/sql"
	"log/slog"
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
	Logger      *slog.Logger

	// StaleProcessingTimeout is the age past which a `processing` package with
	// no encoder_jobs row at all is treated as stranded and reset. This is the
	// one case the lease sweeper cannot see (it only expires existing lease
	// rows); db.FailStaleProcessingPackages scopes the reset to leaseless rows,
	// so live and expired-lease rows alike are left to the lease sweeper. The
	// generous timeout is just margin against any transient leaseless window.
	// Zero disables the pass (lease expiry still runs).
	StaleProcessingTimeout time.Duration
}

// NewSweeper applies defaults: 30s interval, 5 max attempts, 15m stale-processing
// timeout, time.Now, slog.Default(). Override any field on the returned struct
// before calling Run.
func NewSweeper(conn *sql.DB) *Sweeper {
	return &Sweeper{
		DB:                     conn,
		Interval:               30 * time.Second,
		MaxAttempts:            5,
		StaleProcessingTimeout: 15 * time.Minute,
		Now:                    time.Now,
		Logger:                 slog.Default(),
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
	s.sweepStaleProcessing(ctx, source, nowMs)
	results, err := db.LeaseExpiredJobs(ctx, s.DB, nowMs, s.MaxAttempts)
	if err != nil {
		s.Logger.Error("sweeper error", "source", source, "err", err)
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
		s.Logger.Info("sweeper reclaim",
			"source", source,
			"package", r.PackageID,
			"encoder", r.EncoderID,
			"new_status", string(r.NewStatus),
			"attempts", r.Attempts,
		)
	}
	s.Logger.Info("sweeper pass complete",
		"source", source,
		"expired", len(results),
		"pending", pending,
		"failed", failed,
	)
}

// sweepStaleProcessing resets `processing` packages older than
// StaleProcessingTimeout that have no encoder_jobs row — the case
// LeaseExpiredJobs can't see because there is no lease to expire. The leaseless
// guard lives in db.FailStaleProcessingPackages, so this never disturbs a
// lease-bearing row (active encode or one the lease sweeper still owns).
func (s *Sweeper) sweepStaleProcessing(ctx context.Context, source string, nowMs int64) {
	if s.StaleProcessingTimeout <= 0 {
		return
	}
	cutoffMs := nowMs - s.StaleProcessingTimeout.Milliseconds()
	n, err := db.FailStaleProcessingPackages(ctx, s.DB, cutoffMs, nowMs, s.MaxAttempts, "stale processing backstop")
	if err != nil {
		s.Logger.Error("sweeper stale-processing error", "source", source, "err", err)
		return
	}
	if n > 0 {
		s.Logger.Info("sweeper stale-processing reset", "source", source, "reset", n)
	}
}
