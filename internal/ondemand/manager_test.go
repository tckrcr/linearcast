package ondemand

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/packager"
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
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
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
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := onlySession(t, m, "ch1", "e1")
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

func TestSweepPrunesTrailingAndTearsDownIdle(t *testing.T) {
	clock := &fakeClock{now: 0}
	var proc *fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
		proc = newFakeProcess()
		go func() {
			<-ctx.Done()
			proc.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, []int64{6000, 6000, 6000, 6000, 6000, 6000}, false)
		return proc, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := onlySession(t, m, "ch1", "e1")
	m.tailOnce(s)
	oldPath := s.segments[0].Path

	clock.Set(42_000)
	m.sweep(clock.Now())
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old trailing segment should be removed, stat err=%v", err)
	}
	if got := m.SegmentsFrom("ch1", "e1", 30_000, 10); len(got) == 0 {
		t.Fatalf("newer segments should remain")
	}

	clock.Set(130_000)
	proc.finish(nil)
	m.sweep(clock.Now())
	if got := m.SegmentsFrom("ch1", "e1", 30_000, 10); len(got) != 0 {
		t.Fatalf("idle session should be gone, got %+v", got)
	}
}

func TestTailDoesNotDuplicateAfterTrailingPrune(t *testing.T) {
	clock := &fakeClock{now: 0}
	var proc *fakeProcess
	var outDir string
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
		proc = newFakeProcess()
		go func() {
			<-ctx.Done()
			proc.finish(ctx.Err())
		}()
		outDir = spec.OutDir
		writeLivePlaylist(t, spec.OutDir, []int64{6000, 6000, 6000, 6000}, false)
		return proc, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := onlySession(t, m, "ch1", "e1")
	m.tailOnce(s)

	clock.Set(42_000)
	m.sweep(clock.Now())
	writeLivePlaylist(t, outDir, []int64{6000, 6000, 6000, 6000, 6000}, false)
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
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, []int64{6000}, false)
		return p, nil
	})
	m.restartBudget = 2
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	for i := 0; i < 2; i++ {
		if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
		procs[i].finish(errors.New("boom"))
		<-onlySession(t, m, "ch1", "e1").done
	}
	if got := m.ExtraDiscontinuities("ch1"); got != 2 {
		t.Fatalf("extra discontinuities = %d, want 2", got)
	}
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err == nil {
		t.Fatalf("expected budget exhaustion error")
	}
}

func TestSetBurnSubtitleLanguageRestartsWithoutFailureBudget(t *testing.T) {
	clock := &fakeClock{now: 0}
	var procs []*fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, []int64{6000, 6000}, false)
		return p, nil
	})
	m.restartBudget = 1
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s1 := onlySession(t, m, "ch1", "e1")
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
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure after voluntary restart: %v", err)
	}
	if len(procs) != 2 {
		t.Fatalf("spawn count = %d, want 2", len(procs))
	}
}

func TestDetachedSessionArtifactsRemainReadableUntilRetentionExpires(t *testing.T) {
	clock := &fakeClock{now: 0}
	var procs []*fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, []int64{6000, 6000}, false)
		return p, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s1 := onlySession(t, m, "ch1", "e1")
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
		t.Fatalf("retained session dir should be removed after expiry, stat err=%v", err)
	}

	for _, p := range procs {
		p.finish(nil)
	}
}

func TestRestartBudgetCooldownExpires(t *testing.T) {
	clock := &fakeClock{now: 0}
	var procs []*fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, []int64{6000}, false)
		return p, nil
	})
	m.restartBudget = 1
	m.restartCooldownMs = 10_000
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 120_000)
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure 1: %v", err)
	}
	procs[0].finish(errors.New("boom"))
	<-onlySession(t, m, "ch1", "e1").done
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err == nil {
		t.Fatalf("expected budget cooldown error")
	}

	clock.Set(10_001)
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure after cooldown: %v", err)
	}
	if len(procs) != 2 {
		t.Fatalf("spawn count=%d, want 2", len(procs))
	}
}

func TestRestartNumbersSegmentsPastPriorSession(t *testing.T) {
	clock := &fakeClock{now: 0}
	var procs []*fakeProcess
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
		p := newFakeProcess()
		procs = append(procs, p)
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		// Irregular, non-6s segments (copy-mode keyframe splits). The first
		// session's media-sequence span is wider than the wall clock the restart
		// advances, which is exactly when a naive grid/base+index restart reuses
		// already-served numbers.
		writeLivePlaylist(t, spec.OutDir, []int64{6800, 5300, 6400, 5900, 5800, 5800, 6800, 5700, 5900}, false)
		return p, nil
	})
	defer m.Shutdown()

	entry := testEntry("e1", "ch1", 0, 0, 600_000)
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure 1: %v", err)
	}
	s1 := onlySession(t, m, "ch1", "e1")
	m.tailOnce(s1)
	first := m.SegmentsFrom("ch1", "e1", 0, 100)
	if len(first) == 0 {
		t.Fatalf("first session produced no segments")
	}
	var maxFirst int64
	for _, seg := range first {
		if n := seg.BaseSeq + seg.Index; n > maxFirst {
			maxFirst = n
		}
	}

	// Fail the first session, then restart ~14s later — its media coverage
	// overlaps what the first session already served.
	procs[0].finish(errors.New("boom"))
	<-s1.done
	clock.Set(14_000)
	if err := m.EnsureSession(context.Background(), "ch1", entry, "/media.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure 2: %v", err)
	}
	s2 := onlySession(t, m, "ch1", "e1")
	m.tailOnce(s2)
	second := m.SegmentsFrom("ch1", "e1", 14_000, 100)
	if len(second) == 0 {
		t.Fatalf("restarted session produced no segments")
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
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
		p := newFakeProcess()
		procs[spec.OutDir] = p
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		events = append(events, "spawn:"+filepath.Base(filepath.Dir(spec.OutDir)))
		writeLivePlaylist(t, spec.OutDir, []int64{6000}, false)
		return p, nil
	})
	m.maxConcurrent = 1
	m.evictIdleMs = 10_000
	defer m.Shutdown()

	if err := m.EnsureSession(context.Background(), "a", testEntry("e1", "a", 0, 0, 60_000), "/a.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure a: %v", err)
	}
	clock.Set(11_000)
	if err := m.EnsureSession(context.Background(), "b", testEntry("e2", "b", 0, 0, 60_000), "/b.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure b: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want two spawns, got %v", events)
	}
	if got := m.SegmentsFrom("a", "e1", 0, 1); len(got) != 0 {
		t.Fatalf("evicted channel should not serve segments, got %+v", got)
	}
}

func TestAdmissionRejectsWhenAllSessionsFresh(t *testing.T) {
	clock := &fakeClock{now: 0}
	m := newTestManager(t, clock, func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
		p := newFakeProcess()
		go func() {
			<-ctx.Done()
			p.finish(ctx.Err())
		}()
		writeLivePlaylist(t, spec.OutDir, []int64{6000}, false)
		return p, nil
	})
	m.maxConcurrent = 1
	m.evictIdleMs = 10_000
	defer m.Shutdown()

	if err := m.EnsureSession(context.Background(), "a", testEntry("e1", "a", 0, 0, 60_000), "/a.mkv", testProfile(), 6000); err != nil {
		t.Fatalf("ensure a: %v", err)
	}
	clock.Set(5_000)
	err := m.EnsureSession(context.Background(), "b", testEntry("e2", "b", 0, 0, 60_000), "/b.mkv", testProfile(), 6000)
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
		Root:           filepath.Join(t.TempDir(), "sessions"),
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

func onlySession(t *testing.T, m *Manager, channelID, entryID string) *session {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[channelID][entryID]
	if s == nil {
		t.Fatalf("missing session channel=%s entry=%s", channelID, entryID)
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
	playlist := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:6\n#EXT-X-MAP:URI=\"init.mp4\"\n"
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
