package admin

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

// sweeperEnv mirrors the shape of testAdminApp but builds the minimum schema
// needed to exercise lease expiry: one media row, one channel referencing it,
// one registered encoder, and a logger that captures everything for assertions.
type sweeperEnv struct {
	conn      *sql.DB
	encoderID string
	logBuf    *bytes.Buffer
	logger    *log.Logger
}

func newSweeperEnv(t *testing.T) *sweeperEnv {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sweeper.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch1', 'Test', '/tmp', 'linear', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch1', 'm1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}
	id, _, err := db.RegisterEncoder(context.Background(), conn, "sweeper-test", "{}", 1_000)
	if err != nil {
		t.Fatalf("register encoder: %v", err)
	}
	var buf bytes.Buffer
	return &sweeperEnv{
		conn:      conn,
		encoderID: id,
		logBuf:    &buf,
		logger:    log.New(&buf, "", 0),
	}
}

func (e *sweeperEnv) claim(t *testing.T, leaseTTL time.Duration, nowMs int64) {
	t.Helper()
	ok, err := db.ClaimPackage(context.Background(), e.conn, db.ClaimRequest{
		MediaID:   "m1",
		Profile:   "h264-main-1080p",
		PackageID: "pkg-m1",
		EncoderID: e.encoderID,
		LeaseTTL:  leaseTTL,
		NowMs:     nowMs,
	})
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
}

func (e *sweeperEnv) packageStatus(t *testing.T) db.PackageStatus {
	t.Helper()
	pkg, err := db.MediaPackageByID(context.Background(), e.conn, "pkg-m1")
	if err != nil || pkg == nil {
		t.Fatalf("lookup pkg: pkg=%v err=%v", pkg, err)
	}
	return pkg.Status
}

func TestSweeper_TransitionsExpiredLeaseToPending(t *testing.T) {
	env := newSweeperEnv(t)
	env.claim(t, 1*time.Second, 1_000)

	s := &Sweeper{
		DB:          env.conn,
		Interval:    30 * time.Second,
		MaxAttempts: 5,
		Now:         func() time.Time { return time.UnixMilli(5_000) },
		Logger:      env.logger,
	}
	s.sweepOnce(context.Background(), "test")

	if got := env.packageStatus(t); got != db.PackageStatusPending {
		t.Fatalf("status=%s, want pending", got)
	}
	logs := env.logBuf.String()
	if !strings.Contains(logs, "expired=1") || !strings.Contains(logs, "pending=1") {
		t.Fatalf("log missing expected counters:\n%s", logs)
	}
}

func TestSweeper_PromotesToFailedAtCap(t *testing.T) {
	env := newSweeperEnv(t)
	const cap = 2
	for i := 0; i < cap; i++ {
		nowMs := int64(1_000 * (i + 1))
		env.claim(t, 100*time.Millisecond, nowMs)
		s := &Sweeper{
			DB:          env.conn,
			MaxAttempts: cap,
			Now:         func() time.Time { return time.UnixMilli(nowMs + 200) },
			Logger:      env.logger,
		}
		s.sweepOnce(context.Background(), "test")
	}
	if got := env.packageStatus(t); got != db.PackageStatusFailed {
		t.Fatalf("status=%s after %d sweeps, want failed", got, cap)
	}
	if !strings.Contains(env.logBuf.String(), "failed=1") {
		t.Fatalf("expected failed=1 in logs:\n%s", env.logBuf.String())
	}
}

func TestSweeper_NoOpWhenNothingExpired(t *testing.T) {
	env := newSweeperEnv(t)
	env.claim(t, 60*time.Second, 1_000)

	s := &Sweeper{
		DB:          env.conn,
		MaxAttempts: 5,
		Now:         func() time.Time { return time.UnixMilli(2_000) },
		Logger:      env.logger,
	}
	s.sweepOnce(context.Background(), "test")

	if got := env.packageStatus(t); got != db.PackageStatusProcessing {
		t.Fatalf("status=%s, want processing (lease not yet expired)", got)
	}
	if env.logBuf.Len() != 0 {
		t.Fatalf("expected silent no-op, got logs:\n%s", env.logBuf.String())
	}
}

func TestSweeper_RunsImmediateSweepOnStartup(t *testing.T) {
	env := newSweeperEnv(t)
	env.claim(t, 1*time.Second, 1_000)

	// Interval is long so the only way the package transitions is the
	// startup sweep — confirms Run does an initial pass before sleeping.
	s := &Sweeper{
		DB:          env.conn,
		Interval:    10 * time.Hour,
		MaxAttempts: 5,
		Now:         func() time.Time { return time.UnixMilli(5_000) },
		Logger:      env.logger,
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Run(ctx)
	}()

	// Poll until the startup sweep lands. 1s is generous; the actual work
	// is one SQLite tx.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if env.packageStatus(t) == db.PackageStatusPending {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := env.packageStatus(t); got != db.PackageStatusPending {
		t.Fatalf("status=%s after startup sweep, want pending", got)
	}
	cancel()
	wg.Wait()
}

func TestSweeper_RespectsContextCancellation(t *testing.T) {
	env := newSweeperEnv(t)
	s := &Sweeper{
		DB:          env.conn,
		Interval:    1 * time.Hour, // long enough that we exit via ctx, not tick
		MaxAttempts: 5,
		Now:         time.Now,
		Logger:      env.logger,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Give the startup sweep a moment, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("Run did not exit after cancellation")
	}
}
