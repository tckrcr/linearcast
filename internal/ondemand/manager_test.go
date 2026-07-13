package ondemand

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/packager"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

type fakeClock struct {
	mu  sync.Mutex
	now int64
}

func (c *fakeClock) Now() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(v int64) {
	c.mu.Lock()
	c.now = v
	c.mu.Unlock()
}

type fakeProcess struct {
	done chan struct{}
	mu   sync.Mutex
	err  error
	once sync.Once
}

func newFakeProcess() *fakeProcess {
	return &fakeProcess{done: make(chan struct{})}
}

func (p *fakeProcess) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *fakeProcess) finish(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
	p.once.Do(func() { close(p.done) })
}

func TestSegmentsFromMirrorsPackagedSemantics(t *testing.T) {
	clock := &fakeClock{now: 12_000}
	procs := map[string]*fakeProcess{}
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		p := newFakeProcess()
		procs[spec.OutDir] = p
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, []int64{6006, 5964, 6030}, false)
		return p, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 30_000, 120_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s)

	got := m.SegmentsFrom("ch1", "e1", 43_000, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 segments, got %+v", got)
	}
	if got[0].MediaStartMs != 42_000 || got[0].DurationMs != 6006 {
		t.Fatalf("first segment should cover media position, got %+v", got[0])
	}
	got = m.SegmentsFrom("ch1", "e1", 48_006, 2)
	if len(got) != 2 || got[0].MediaStartMs != 48_006 {
		t.Fatalf("exact boundary should start at next segment, got %+v", got)
	}

	for _, p := range procs {
		p.finish(nil)
	}
}

func TestEncodingRowsMirrorActiveProcessLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	clock := &fakeClock{now: 12_000}
	m, err := NewManager(ManagerOptions{
		Root:          filepath.Join(t.TempDir(), "encodings"),
		MaxConcurrent: 4,
		NowFn:         clock.Now,
		DB:            conn,
		Spawn: func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
			p := newFakeProcess()
			go func() {
				<-ctx.Done()
				p.finish(ctx.Err())
			}()
			writeLivePlaylist(t, spec.OutDir, makeTargetDurations(2), false)
			return p, nil
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	rows, err := db.ListOnDemandEncodings(context.Background(), conn)
	if err != nil {
		t.Fatalf("list encodings: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].State != string(stateServing) || !rows[0].ProcessRunning || rows[0].SegmentCount != 2 {
		t.Fatalf("unexpected encoding row: %+v", rows[0])
	}

	m.Shutdown()
	rows, err = db.ListOnDemandEncodings(context.Background(), conn)
	if err != nil {
		t.Fatalf("list after shutdown: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows after shutdown, want 0", len(rows))
	}
}

func TestSweepPrunesTrailingAndTearsDownIdle(t *testing.T) {
	clock := &fakeClock{now: 0}
	var proc *fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		proc = newFakeProcess()
		go func() {
			<-ctx.Done()
			proc.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(6), false)
		return proc, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s)
	oldPath := s.segments[0].Path

	clock.Set(42_000)
	m.sweep(clock.Now())
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old trailing segment should be removed, stat err=%v", err)
	}
	if got := m.SegmentsFrom("ch1", "e1", 6_000, 10); len(got) == 0 {
		t.Fatalf("newer segments should remain")
	}

	clock.Set(130_000)
	proc.finish(nil)
	m.sweep(clock.Now())
	if got := m.SegmentsFrom("ch1", "e1", 6_000, 10); len(got) != 0 {
		t.Fatalf("idle encoding should be gone, got %+v", got)
	}
}

func TestTailDoesNotDuplicateAfterTrailingPrune(t *testing.T) {
	clock := &fakeClock{now: 0}
	var proc *fakeProcess
	var outDir string
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		proc = newFakeProcess()
		go func() {
			<-ctx.Done()
			proc.finish(ctx.Err())
		}()
		outDir = spec.OutDir
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(4), false)
		return proc, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s)

	clock.Set(42_000)
	m.sweep(clock.Now())
	writeLivePlaylist(t, outDir, makeTargetDurations(5), false)
	m.tailOnce(s)

	got := m.SegmentsFrom("ch1", "e1", 0, 10)
	seen := map[int64]bool{}
	for _, seg := range got {
		if seen[seg.Index] {
			t.Fatalf("duplicate segment index after prune: %+v", got)
		}
		seen[seg.Index] = true
	}
	if !seen[4] {
		t.Fatalf("new segment was not appended after prune, got %+v", got)
	}
}

func TestFailureRespawnsUntilBudgetAndTracksDiscontinuities(t *testing.T) {
	clock := &fakeClock{now: 0}
	var procs []*fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(1), false)
		return p, nil
	})
	m.restartBudget = 2
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	for i := 0; i < 2; i++ {
		if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
		procs[i].finish(errors.New("boom"))
		<-onlyEncoding(t, m, "ch1", "e1").done
	}
	if got := m.ExtraDiscontinuities("ch1"); got != 2 {
		t.Fatalf("extra discontinuities = %d, want 2", got)
	}
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err == nil {
		t.Fatalf("expected budget exhaustion error")
	}
}

// TestSpawnFailureCountsAgainstRestartBudget proves a persistent ffmpeg spawn
// failure backs off into a cooldown with Retry-After instead of re-spawning on
// every manifest poll. Before the fix, startReserved did not count spawn
// failures, so a failed encoding was recreated each poll with no backoff.
func TestSpawnFailureCountsAgainstRestartBudget(t *testing.T) {
	clock := &fakeClock{now: 0}
	var spawns int
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		spawns++
		return nil, errors.New("ffmpeg: no such file")
	})
	m.restartBudget = 3
	m.restartCooldownMs = 10_000
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	// Each attempt re-spawns because the prior encoding failed; none should be a
	// budget error yet.
	for i := 0; i < 3; i++ {
		err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs)
		if err == nil {
			t.Fatalf("attempt %d: expected spawn failure error", i)
		}
		if errors.Is(err, ErrRestartBudgetExhausted) {
			t.Fatalf("attempt %d: budget exhausted before %d attempts", i, m.restartBudget)
		}
	}
	if spawns != 3 {
		t.Fatalf("spawns=%d after budget attempts, want 3", spawns)
	}

	// Budget exhausted: now cool down with Retry-After and do not spawn again.
	err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs)
	if !errors.Is(err, ErrRestartBudgetExhausted) {
		t.Fatalf("err=%v, want ErrRestartBudgetExhausted after budget", err)
	}
	if retryAfter, ok := RetryAfterSeconds(err, clock.Now()); !ok || retryAfter != 10 {
		t.Fatalf("RetryAfterSeconds=(%d,%v), want 10,true", retryAfter, ok)
	}
	if spawns != 3 {
		t.Fatalf("cooldown re-spawned ffmpeg: spawns=%d, want 3 (no hot loop)", spawns)
	}

	// After the cooldown expires a fresh attempt is admitted again.
	clock.Set(10_001)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err == nil {
		t.Fatalf("post-cooldown attempt should retry the spawn (and fail), got nil")
	}
	if spawns != 4 {
		t.Fatalf("spawns=%d after cooldown expiry, want 4", spawns)
	}
}

func TestSetBurnSubtitleLanguageRestartsWithoutFailureBudget(t *testing.T) {
	clock := &fakeClock{now: 0}
	var procs []*fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(2), false)
		return p, nil
	})
	m.restartBudget = 1
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s1 := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s1)
	m.SetBurnSubtitleLanguage("ch1", "eng")

	if got := m.ExtraDiscontinuities("ch1"); got != 1 {
		t.Fatalf("extra discontinuities = %d, want 1", got)
	}
	if got := m.BurnSubtitleLanguage("ch1"); got != "eng" {
		t.Fatalf("burn language = %q, want eng", got)
	}
	if until := m.blockedUntil["e1"]; until != 0 {
		t.Fatalf("voluntary restart should not cooldown entry, blockedUntil=%d", until)
	}
	clock.Set(6_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure after voluntary restart: %v", err)
	}
	if len(procs) != 2 {
		t.Fatalf("spawn count = %d, want 2", len(procs))
	}
}

func TestDetachedEncodingArtifactsRemainReadableUntilRetentionExpires(t *testing.T) {
	clock := &fakeClock{now: 0}
	var procs []*fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(2), false)
		return p, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s1 := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s1)
	oldID := s1.id
	oldSeg := s1.segments[0]

	m.RestartChannel("ch1")
	if _, ok := m.InitPath("ch1", oldID); !ok {
		t.Fatalf("detached init should remain readable during retention")
	}
	if path, ok := m.SegmentPath("ch1", oldID, oldSeg.Index); !ok || path != oldSeg.Path {
		t.Fatalf("detached segment path=(%q,%v), want %q,true", path, ok, oldSeg.Path)
	}
	if _, err := os.Stat(oldSeg.Path); err != nil {
		t.Fatalf("retained segment missing on disk: %v", err)
	}

	clock.Set(31_000)
	m.sweep(clock.Now())
	if _, ok := m.SegmentPath("ch1", oldID, oldSeg.Index); ok {
		t.Fatalf("detached segment should expire after retention window")
	}
	if _, err := os.Stat(oldSeg.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained encoding dir should be removed after expiry, stat err=%v", err)
	}

	for _, p := range procs {
		p.finish(nil)
	}
}

func TestRestartBudgetCooldownExpires(t *testing.T) {
	clock := &fakeClock{now: 0}
	var procs []*fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(1), false)
		return p, nil
	})
	m.restartBudget = 1
	m.restartCooldownMs = 10_000
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure 1: %v", err)
	}
	procs[0].finish(errors.New("boom"))
	<-onlyEncoding(t, m, "ch1", "e1").done
	err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs)
	if err == nil {
		t.Fatalf("expected budget cooldown error")
	}
	if !errors.Is(err, ErrRestartBudgetExhausted) {
		t.Fatalf("err=%v, want ErrRestartBudgetExhausted", err)
	}
	if retryAfter, ok := RetryAfterSeconds(err, clock.Now()); !ok || retryAfter != 10 {
		t.Fatalf("RetryAfterSeconds=(%d,%v), want 10,true", retryAfter, ok)
	}

	clock.Set(10_001)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure after cooldown: %v", err)
	}
	if len(procs) != 2 {
		t.Fatalf("spawn count=%d, want 2", len(procs))
	}
}

func TestRestartNumbersSegmentsPastPriorEncoding(t *testing.T) {
	clock := &fakeClock{now: 0}
	var procs []*fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		// Irregular, non-6s segments (copy-mode keyframe splits). The first
		// encoding's media-sequence span is wider than the wall clock the restart
		// advances, which is exactly when a naive grid/base+index restart reuses
		// already-served numbers.
		writeLivePlaylist(t, spec.OutDir, []int64{6800, 5300, 6400, 5900, 5800, 5800, 6800, 5700, 5900}, false)
		return p, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 600_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure 1: %v", err)
	}
	s1 := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s1)
	first := m.SegmentsFrom("ch1", "e1", 0, 100)
	if len(first) == 0 {
		t.Fatalf("first encoding produced no segments")
	}
	var maxFirst int64
	for _, seg := range first {
		if n := seg.BaseSeq + seg.Index; n > maxFirst {
			maxFirst = n
		}
	}

	// Fail the first encoding, then restart ~14s later — its media coverage
	// overlaps what the first encoding already served.
	procs[0].finish(errors.New("boom"))
	<-s1.done
	clock.Set(14_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure 2: %v", err)
	}
	s2 := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s2)
	second := m.SegmentsFrom("ch1", "e1", 14_000, 100)
	if len(second) == 0 {
		t.Fatalf("restarted encoding produced no segments")
	}
	for _, seg := range second {
		if n := seg.BaseSeq + seg.Index; n <= maxFirst {
			t.Fatalf("restart reused media sequence %d (<= prior max %d); hls.js rejects this as a mismatch", n, maxFirst)
		}
	}
}

func TestAdmissionEvictsIdleBeforeSpawningNext(t *testing.T) {
	clock := &fakeClock{now: 0}
	var events []string
	procs := map[string]*fakeProcess{}
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		p := newFakeProcess()
		procs[spec.OutDir] = p
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		events = append(events, "spawn:"+filepath.Base(filepath.Dir(spec.OutDir)))
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(1), false)
		return p, nil
	})
	m.maxConcurrent = 1
	m.evictIdleMs = 10_000
	defer m.Shutdown()

	if err := m.EnsureEncoding(context.Background(), "a", testEntry("e1", "a", 0, 0, 60_000), "/a.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure a: %v", err)
	}
	clock.Set(11_000)
	if err := m.EnsureEncoding(context.Background(), "b", testEntry("e2", "b", 0, 0, 60_000), "/b.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure b: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want two spawns, got %v", events)
	}
	if got := m.SegmentsFrom("a", "e1", 0, 1); len(got) != 0 {
		t.Fatalf("evicted channel should not serve segments, got %+v", got)
	}
}

func TestAdmissionRejectsWhenAllEncodingsFresh(t *testing.T) {
	clock := &fakeClock{now: 0}
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		p := newFakeProcess()
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(1), false)
		return p, nil
	})
	m.maxConcurrent = 1
	m.evictIdleMs = 10_000
	defer m.Shutdown()

	if err := m.EnsureEncoding(context.Background(), "a", testEntry("e1", "a", 0, 0, 60_000), "/a.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure a: %v", err)
	}
	clock.Set(5_000)
	err := m.EnsureEncoding(context.Background(), "b", testEntry("e2", "b", 0, 0, 60_000), "/b.mkv", testProfile(), scheduler.TargetSegmentMs)
	if !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("want ErrAtCapacity, got %v", err)
	}
}

func TestNewManagerRejectsDangerousRoots(t *testing.T) {
	if _, err := NewManager(ManagerOptions{Root: ""}); err == nil {
		t.Fatalf("empty root should be rejected")
	}
	if _, err := NewManager(ManagerOptions{Root: string(filepath.Separator)}); err == nil {
		t.Fatalf("filesystem root should be rejected")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if _, err := NewManager(ManagerOptions{Root: home}); err == nil {
			t.Fatalf("home directory should be rejected")
		}
	}
}

func newTestManager(t *testing.T, clock *fakeClock, spawn SpawnFunc) *Manager {
	t.Helper()
	m, err := NewManager(ManagerOptions{
		Root:           filepath.Join(t.TempDir(), "encodings"),
		GraceMs:        120_000,
		TrailingMs:     30_000,
		MaxConcurrent:  4,
		EvictIdleMs:    10_000,
		TailIntervalMs: 10,
		StallTimeoutMs: 45_000,
		RestartBudget:  3,
		NowFn:          clock.Now,
		Spawn:          spawn,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return m
}

func testEntry(id, channelID string, startMs, offsetMs, durationMs int64) db.ScheduleEntry {
	return db.ScheduleEntry{ID: id, ChannelID: channelID, StartMs: startMs, OffsetMs: offsetMs, DurationMs: durationMs, MediaID: "m1"}
}

func testProfile() packageprofile.Profile {
	p, _ := packageprofile.Lookup(packageprofile.DefaultName)
	return p
}

func onlyEncoding(t *testing.T, m *Manager, channelID, entryID string) *channelEncoding {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.encodings[channelID][entryID]
	if s == nil {
		t.Fatalf("missing encoding channel=%s entry=%s", channelID, entryID)
	}
	return s
}

func writeLivePlaylist(t *testing.T, dir string, durations []int64, absolute bool) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), []byte("init"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	targetDuration := int64(1)
	for _, d := range durations {
		if td := (d + 999) / 1000; td > targetDuration {
			targetDuration = td
		}
	}
	playlist := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:" + strconv.FormatInt(targetDuration, 10) + "\n#EXT-X-MAP:URI=\"init.mp4\"\n"
	for i, d := range durations {
		name := filepath.Join(dir, "seg"+formatIndex(int64(i))+".m4s")
		if err := os.WriteFile(name, []byte("seg"), 0o644); err != nil {
			t.Fatalf("write seg: %v", err)
		}
		uri := filepath.Base(name)
		if absolute {
			uri = name
		}
		playlist += "#EXTINF:" + formatExtinf(d) + ",\n" + uri + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(playlist), 0o644); err != nil {
		t.Fatalf("write playlist: %v", err)
	}
}

func makeTargetDurations(n int) []int64 {
	d := make([]int64, n)
	for i := range d {
		d[i] = scheduler.TargetSegmentMs
	}
	return d
}

func formatIndex(i int64) string {
	return string([]byte{
		byte('0' + (i/100000)%10),
		byte('0' + (i/10000)%10),
		byte('0' + (i/1000)%10),
		byte('0' + (i/100)%10),
		byte('0' + (i/10)%10),
		byte('0' + i%10),
	})
}

func formatExtinf(ms int64) string {
	whole := ms / 1000
	frac := ms % 1000
	return string(rune('0'+whole)) + "." + string([]byte{
		byte('0' + (frac/100)%10),
		byte('0' + (frac/10)%10),
		byte('0' + frac%10),
	})
}

func TestFFmpegProcessWaitIncludesStderrTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh helper is POSIX-only")
	}
	cmd := exec.Command("sh", "-c", `echo "boom: codec mismatch" >&2; exit 3`)
	tail := &stderrTail{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	p := &ffmpegProcess{cmd: cmd, stderr: tail}
	err := p.Wait()
	if err == nil {
		t.Fatal("Wait()=nil, want exit error")
	}
	if !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "ffmpeg stderr: boom: codec mismatch") {
		t.Fatalf("Wait()=%q, want exit status and stderr tail", err)
	}
}

func TestFFmpegProcessWaitOmitsStderrOnSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh helper is POSIX-only")
	}
	cmd := exec.Command("sh", "-c", `echo "harmless warning" >&2; exit 0`)
	tail := &stderrTail{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	p := &ffmpegProcess{cmd: cmd, stderr: tail}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait()=%v, want nil", err)
	}
}

func TestStderrTailBoundsAndCollapses(t *testing.T) {
	tail := &stderrTail{}
	for i := 0; i < 1000; i++ {
		if _, err := tail.Write([]byte("line with  spaces\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	out := tail.Tail()
	if len(out) > stderrTailMax {
		t.Fatalf("Tail() length=%d, want <= %d", len(out), stderrTailMax)
	}
	if strings.Contains(out, "\n") || strings.Contains(out, "  ") {
		t.Fatalf("Tail()=%q, want whitespace collapsed to single spaces", out[:80])
	}
}

// TestMidEncodingProcessExitRestartsAndNumbersPastPriorEncoding verifies that
// a process which exits mid-stream (after producing segments) triggers a
// counted failure, increments extra discontinuities, and the replacement
// encoding numbers its segments past the served sequence range.
func TestMidEncodingProcessExitRestartsAndNumbersPastPriorEncoding(t *testing.T) {
	clock := &fakeClock{now: 0}
	var procs []*fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(5), false)
		return p, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure 1: %v", err)
	}
	s1 := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s1)

	// Mid-stream: encoding has segments and is serving. Kill the process.
	procs[0].finish(errors.New("broken pipe"))
	<-s1.done

	if s1.state != stateFailed {
		t.Fatalf("encoding state=%s, want failed", s1.state)
	}
	if m.restarts["e1"] != 1 {
		t.Fatalf("restarts=%d, want 1", m.restarts["e1"])
	}
	if m.extraDisc["ch1"] != 1 {
		t.Fatalf("extra discontinuities=%d, want 1", m.extraDisc["ch1"])
	}

	// Replacement encoding should number past what the first encoding served.
	clock.Set(10_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure 2: %v", err)
	}
	s2 := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s2)

	var maxFirst int64
	for _, seg := range s1.segments {
		if n := seg.BaseSeq + seg.Index; n > maxFirst {
			maxFirst = n
		}
	}
	for _, seg := range s2.segments {
		if n := seg.BaseSeq + seg.Index; n <= maxFirst {
			t.Fatalf("replacement segment sequence %d <= prior max %d", n, maxFirst)
		}
	}
	if s2.baseSeq <= s1.baseSeq+int64(len(s1.segments))-1 {
		t.Fatalf("baseSeq=%d should advance past prior encoding's last sequence", s2.baseSeq)
	}
}

// TestSweepDetectsStalledSegmentProduction verifies the stall timeout tears
// down a channel encoding that stops producing segments while the process is still
// running, and that the failure counts against the entry restart budget.
func TestSweepDetectsStalledSegmentProduction(t *testing.T) {
	clock := &fakeClock{now: 0}
	var proc *fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		proc = newFakeProcess()
		go func() {
			<-ctx.Done()
			proc.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(2), false)
		return proc, nil
	})
	m.stallTimeoutMs = 45_000
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s)

	// The encoding has produced segments; advance past the stall timeout
	// without adding any new segments (no further tailOnce calls).
	clock.Set(60_000)
	m.sweep(clock.Now())

	// The encoding should be torn down as stalled.
	if got := m.SegmentsFrom("ch1", "e1", 0, 10); len(got) != 0 {
		t.Fatalf("stalled encoding should not serve segments after teardown, got %+v", got)
	}
	// The process should have been cancelled.
	proc.finish(nil)

	// The failure should count against the restart budget. extraDisc is
	// reset per-channel when the last encoding is removed, so only restarts
	// persist.
	if m.restarts["e1"] != 1 {
		t.Fatalf("restarts for entry e1 = %d, want 1", m.restarts["e1"])
	}
	if _, ok := m.extraDisc["ch1"]; ok {
		t.Fatalf("extraDisc should be deleted when channel has no encodings")
	}
}

// TestEncodingIDForActiveEncoding verifies that the manager exposes the live
// encoding identifier and directory for an active entry.
func TestEncodingIDForActiveEncoding(t *testing.T) {
	clock := &fakeClock{now: 100_000}
	var proc *fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		proc = newFakeProcess()
		go func() {
			<-ctx.Done()
			proc.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, []int64{6006, 5964, 6030}, false)
		return proc, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 100_000, 0, 60_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s)

	id, ok := m.EncodingID("ch1", "e1")
	if !ok {
		t.Fatal("EncodingID returned false for active encoding")
	}
	if id != s.id {
		t.Fatalf("EncodingID=%q, want %q", id, s.id)
	}
	if dir, ok := m.EncodingDir("ch1", s.id); !ok || dir != s.dir {
		t.Fatalf("EncodingDir=(%q,%v), want %q,true", dir, ok, s.dir)
	}

	proc.finish(nil)
}

func TestEncodingIDForRetainedEncoding(t *testing.T) {
	clock := &fakeClock{now: 100_000}
	var proc *fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		proc = newFakeProcess()
		go func() {
			<-ctx.Done()
			proc.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(2), false)
		return proc, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 100_000, 10_000, 120_000)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s)

	// Use RestartChannel to move the encoding to retained without relying on
	// sweep entry-over timing (which would prune all segments before retain).
	m.RestartChannel("ch1")
	<-s.done

	id, ok := m.EncodingID("ch1", "e1")
	if !ok {
		t.Fatal("EncodingID returned false for retained encoding")
	}
	if id != s.id {
		t.Fatalf("EncodingID=%q, want %q", id, s.id)
	}
	if dir, ok := m.EncodingDir("ch1", s.id); !ok || dir != s.dir {
		t.Fatalf("EncodingDir=(%q,%v), want %q,true", dir, ok, s.dir)
	}

	// Advance past retention window.
	retentionMs := m.artifactRetentionMs(s)
	clock.Set(clock.Now() + retentionMs + 1000)
	m.sweep(clock.Now())

	if _, ok := m.EncodingID("ch1", "e1"); ok {
		t.Fatal("EncodingID should return false after retention expires")
	}
}

func TestPruneTrailingKeepsPlaybackLookbackArtifacts(t *testing.T) {
	m, err := NewManager(ManagerOptions{
		Root:                   filepath.Join(t.TempDir(), "encodings"),
		TrailingMs:             30_000,
		MinArtifactRetentionMs: 50_000,
		NowFn:                  (&fakeClock{now: 0}).Now,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer m.Shutdown()

	segPath := filepath.Join(m.root, "seg.m4s")
	if err := os.WriteFile(segPath, []byte("segment"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	s := &channelEncoding{
		entry:    testEntry("e1", "ch1", 0, 0, 180_000),
		targetMs: scheduler.TargetSegmentMs,
		segments: []SegmentMeta{{
			MediaStartMs: 66_000,
			DurationMs:   10_000,
			Path:         segPath,
		}},
	}

	m.pruneTrailingLocked(s, 110_000)

	if len(s.segments) != 1 {
		t.Fatalf("segment was pruned inside playback lookback window")
	}
	if _, err := os.Stat(segPath); err != nil {
		t.Fatalf("segment file should remain: %v", err)
	}
}

func TestPruneFromEncodedTailKeepsSlowBufferedArtifacts(t *testing.T) {
	m, err := NewManager(ManagerOptions{
		Root:                   filepath.Join(t.TempDir(), "encodings"),
		MinArtifactRetentionMs: 180_000,
		PruneFromEncodedTail:   true,
		NowFn:                  (&fakeClock{now: 0}).Now,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer m.Shutdown()

	segPath := filepath.Join(m.root, "seg.m4s")
	if err := os.WriteFile(segPath, []byte("segment"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	s := &channelEncoding{
		entry:    testEntry("e1", "ch1", 0, 0, 1_800_000),
		targetMs: scheduler.TargetSegmentMs,
		segments: []SegmentMeta{{
			MediaStartMs: 138_000,
			DurationMs:   2_000,
			Path:         segPath,
		}},
	}

	// Wall-clock pruning would compute a cutoff around 420s here and delete the
	// freshly-written 138s segment. Buffered pruning anchors retention to the
	// encoded tail, so a slow encoder does not erase its own output.
	m.pruneTrailingLocked(s, 600_000)

	if len(s.segments) != 1 {
		t.Fatalf("segment was pruned despite being at encoded tail")
	}
	if _, err := os.Stat(segPath); err != nil {
		t.Fatalf("segment file should remain: %v", err)
	}
}

// TestPruneFromEncodedTailKeepsBufferedTrailingWindow exercises PruneFromEncodedTail
// retention math with an encoder prewarmed ~120s ahead (encoded tail at media
// now+120s). With retention of 180s measured back from that tail, the cutoff lands
// at media now-60s: a segment read at the trailing edge (now-30s) is kept, while a
// segment 90s behind now is reclaimed.
func TestPruneFromEncodedTailKeepsBufferedTrailingWindow(t *testing.T) {
	const (
		lead      = 120_000 // bufferedLeadMs
		trailing  = 30_000  // bufferedTrailingWindowMs
		retention = 180_000 // bufferedRetentionMs
		nowMedia  = 200_000 // media position airing at wall-clock now
	)
	m, err := NewManager(ManagerOptions{
		Root:                   filepath.Join(t.TempDir(), "encodings"),
		MinArtifactRetentionMs: retention,
		PruneFromEncodedTail:   true,
		NowFn:                  (&fakeClock{now: 0}).Now,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer m.Shutdown()

	writeSeg := func(name string) string {
		p := filepath.Join(m.root, name)
		if err := os.WriteFile(p, []byte("segment"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	deepPath := writeSeg("deep.m4s") // 90s behind now → outside retention
	edgePath := writeSeg("edge.m4s") // trailing-window edge (now-30s) → must survive
	tailPath := writeSeg("tail.m4s") // encoded tail (now+lead) → the prune anchor

	s := &channelEncoding{
		entry:    testEntry("e1", "ch1", 0, 0, 1_800_000),
		targetMs: scheduler.TargetSegmentMs,
		segments: []SegmentMeta{
			{MediaStartMs: nowMedia - 90_000, DurationMs: 2_000, Path: deepPath},
			{MediaStartMs: nowMedia - trailing, DurationMs: 2_000, Path: edgePath},
			{MediaStartMs: nowMedia + lead - 2_000, DurationMs: 2_000, Path: tailPath},
		},
	}

	m.pruneTrailingLocked(s, 0)

	if _, err := os.Stat(edgePath); err != nil {
		t.Fatalf("trailing-window edge segment (now-%dms) was pruned: %v", trailing, err)
	}
	if _, err := os.Stat(tailPath); err != nil {
		t.Fatalf("encoded-tail segment was pruned: %v", err)
	}
	if _, err := os.Stat(deepPath); !os.IsNotExist(err) {
		t.Fatalf("segment 90s behind now should have been reclaimed, stat err=%v", err)
	}
	if len(s.segments) != 2 {
		t.Fatalf("segments kept=%d, want 2 (edge + tail)", len(s.segments))
	}
}

func TestSubtitleInfoIncludesMediaIDForActiveAndRetainedEncoding(t *testing.T) {
	clock := &fakeClock{now: 100_000}
	var proc *fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		proc = newFakeProcess()
		go func() {
			<-ctx.Done()
			proc.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, makeTargetDurations(1), false)
		return proc, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 100_000, 10_000, 120_000)
	entry.MediaID = "media-1"
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := onlyEncoding(t, m, "ch1", "e1")
	m.tailOnce(s)

	info, ok := m.SubtitleInfo("ch1", s.id)
	if !ok {
		t.Fatal("SubtitleInfo returned false for active encoding")
	}
	if info.MediaID != "media-1" {
		t.Fatalf("active SubtitleInfo MediaID=%q, want media-1", info.MediaID)
	}

	m.RestartChannel("ch1")
	<-s.done

	info, ok = m.SubtitleInfo("ch1", s.id)
	if !ok {
		t.Fatal("SubtitleInfo returned false for retained encoding")
	}
	if info.MediaID != "media-1" {
		t.Fatalf("retained SubtitleInfo MediaID=%q, want media-1", info.MediaID)
	}
}

func TestEncodingIDEmptyEntryID(t *testing.T) {
	m := newTestManager(t, &fakeClock{now: 0}, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		return newFakeProcess(), nil
	})
	defer m.Shutdown()

	if _, ok := m.EncodingID("ch1", ""); ok {
		t.Fatal("EncodingID with empty entryID should return false")
	}
}

func TestEncodingIDMissingEntry(t *testing.T) {
	m := newTestManager(t, &fakeClock{now: 0}, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		return newFakeProcess(), nil
	})
	defer m.Shutdown()

	if _, ok := m.EncodingID("ch1", "no-such-entry"); ok {
		t.Fatal("EncodingID for non-existent entry should return false")
	}
}

func TestEncodingDirFailureCountsAgainstBudget(t *testing.T) {
	clock := &fakeClock{now: 0}
	var spawns int
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
		spawns++
		return newFakeProcess(), nil
	})
	m.restartBudget = 2
	m.restartCooldownMs = 10_000
	defer m.Shutdown()

	// Place a file where the channel's encoding directory would be created,
	// so MkdirAll inside startResolved fails with ENOTDIR.
	blockingFile := filepath.Join(m.root, "ch1")
	if err := os.WriteFile(blockingFile, []byte("block"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	// Each attempt fails at MkdirAll and counts against the budget.
	for i := 0; i < 2; i++ {
		err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs)
		if err == nil {
			t.Fatalf("attempt %d: expected failure", i)
		}
		if errors.Is(err, ErrRestartBudgetExhausted) {
			t.Fatalf("attempt %d: budget exhausted early", i)
		}
	}
	if spawns != 0 {
		t.Fatalf("spawns=%d, want 0 (dir failure should not reach spawn)", spawns)
	}
	if m.restarts["e1"] != 2 {
		t.Fatalf("restarts=%d, want 2", m.restarts["e1"])
	}

	// Budget exhausted: cooldown without touching spawn.
	err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs)
	if !errors.Is(err, ErrRestartBudgetExhausted) {
		t.Fatalf("err=%v, want ErrRestartBudgetExhausted", err)
	}
	if spawns != 0 {
		t.Fatalf("cooldown spawned: spawns=%d, want 0", spawns)
	}

	// After cooldown expires a fresh attempt should hit the same dir failure.
	clock.Set(10_001)
	if err := m.EnsureEncoding(context.Background(), "ch1", entry, "/media.mkv", testProfile(), scheduler.TargetSegmentMs); err == nil {
		t.Fatalf("post-cooldown should still fail at MkdirAll")
	}
	if spawns != 0 {
		t.Fatalf("post-cooldown spawned: spawns=%d, want 0", spawns)
	}
}
