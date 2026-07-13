// Package ondemand manages ephemeral live encodes for on-demand playback.
package ondemand

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/metrics"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/packager"
)

const (
	defaultGraceMs           int64 = 120_000
	defaultTrailingMs        int64 = 30_000
	defaultMaxConcurrent           = 4
	defaultEvictIdleMs       int64 = 10_000
	defaultTailIntervalMs    int64 = 300
	defaultStallTimeoutMs    int64 = 45_000
	defaultRestartBudget           = 3
	defaultRestartCooldownMs int64 = 60_000
	defaultSweepInterval           = 5 * time.Second
)

const killGateMs int64 = 15 * 60 * 1000 // 15 minutes

var ErrAtCapacity = errors.New("on-demand channel encoding capacity reached")
var ErrRestartBudgetExhausted = errors.New("on-demand channel encoding restart budget exhausted")
var ErrKillGated = errors.New("channel encoder was manually stopped; resumes in 15 minutes")
var errAdmissionRetry = errors.New("retry on-demand admission")

type RetryAfterError struct {
	Err     error
	UntilMs int64
}

func (e *RetryAfterError) Error() string {
	return fmt.Sprintf("%v until %d", e.Err, e.UntilMs)
}

func (e *RetryAfterError) Unwrap() error {
	return e.Err
}

func RetryAfterSeconds(err error, nowMs int64) (int64, bool) {
	var retryErr *RetryAfterError
	if !errors.As(err, &retryErr) {
		return 0, false
	}
	remainingMs := retryErr.UntilMs - nowMs
	if remainingMs <= 0 {
		return 1, true
	}
	return (remainingMs + 999) / 1000, true
}

type ManagerOptions struct {
	Root                   string
	GraceMs                int64
	TrailingMs             int64
	MinArtifactRetentionMs int64
	BurstSec               int
	MaxConcurrent          int
	RealtimePacing         bool
	PruneFromEncodedTail   bool
	EvictIdleMs            int64
	TailIntervalMs         int64
	StallTimeoutMs         int64
	RestartBudget          int
	RestartCooldownMs      int64
	NowFn                  func() int64
	Spawn                  SpawnFunc
	DB                     *sql.DB
}

type SpawnFunc func(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error)

type EncodingOptions struct {
	BurnSubtitleStreamIndex int
	// SubtitleStreamIndexes are absolute source stream indexes for text-based
	// subtitle tracks the channel encoding should mux to WebVTT segment playlists.
	SubtitleStreamIndexes []int
	// RealtimePacing, when non-nil, overrides the manager default for this
	// encoding. Copy/remux uses realtime pacing; transcode can run uncapped
	// during warmup/catch-up.
	RealtimePacing *bool
	// StartWallMs optionally overrides the wall-clock position used to choose the
	// source seek point for a newly-created channel encoding. Existing encodings are reused
	// by entry/subtitle options and are not reseeked.
	StartWallMs int64
	// TraceID associates a startup trace across the channel encoding lifecycle for
	// correlating ffmpeg spawn, first segment, and ready-gate log events.
	TraceID string
}

type Process interface {
	Wait() error
}

type SegmentMeta struct {
	EncodingID   string
	Index        int64
	MediaStartMs int64
	DurationMs   int64
	Path         string
	InitPath     string
	// BaseSeq is the HLS media sequence number of this channel encoding's first segment
	// (Index 0). The manifest numbers each segment BaseSeq+Index, which stays
	// contiguous regardless of copy-mode's irregular segment durations and
	// monotonic across restarts: a replacement channel encoding's BaseSeq is
	// advanced past the prior encoding's high-water mark, so a given media
	// sequence number never maps to two different segments (which hls.js rejects
	// as a media-sequence mismatch).
	BaseSeq int64
}

type EncodingSnapshot struct {
	EncodingID       string
	State            string
	ProcessRunning   bool
	SpawnedAtMs      int64
	FirstSegmentAtMs int64
	LastProgressMs   int64
	SegmentCount     int
	LastError        string
}

type Manager struct {
	root                   string
	graceMs                int64
	trailingMs             int64
	minArtifactRetentionMs int64
	burstSec               int
	maxConcurrent          int
	realtimePacing         bool
	pruneFromEncodedTail   bool
	evictIdleMs            int64
	db                     *sql.DB
	tailInterval           time.Duration
	stallTimeoutMs         int64
	restartBudget          int
	restartCooldownMs      int64
	now                    func() int64
	spawn                  SpawnFunc

	mu                sync.Mutex
	encodings         map[string]map[string]*channelEncoding
	byID              map[string]*channelEncoding
	retainedEncodings map[string]*retainedChannelEncoding
	lastTouch         map[string]int64
	restarts          map[string]int
	blockedUntil      map[string]int64
	extraDisc         map[string]int64
	// seqHighWater tracks, per entry ID, the highest HLS media sequence number
	// any channel encoding for that entry has emitted, so a restarted encoding can be
	// numbered strictly past it rather than colliding with already-served
	// segments.
	seqHighWater map[string]int64
	seq          int64

	burnLanguage map[string]string
	killedUntil  map[string]int64
}

type channelEncodingState string

const (
	stateStarting channelEncodingState = "starting"
	stateServing  channelEncodingState = "serving"
	stateEnded    channelEncodingState = "ended"
	stateFailed   channelEncodingState = "failed"
	stateStopping channelEncodingState = "stopping"
)

type channelEncoding struct {
	id                      string
	channelID               string
	entry                   db.ScheduleEntry
	mediaPath               string
	profile                 packageprofile.Profile
	targetMs                int64
	burnSubtitleStreamIndex int
	subtitleStreamIndexes   []int
	realtimePacing          bool
	dir                     string
	initPath                string

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	state            channelEncodingState
	segments         []SegmentMeta
	parsedSegments   int
	nextMediaStartMs int64
	lastProgressMs   int64
	waitErr          error
	failCounted      bool
	processRunning   bool
	baseMediaStartMs int64
	baseSeq          int64
	spawnedAt        int64
	firstSegmentAtMs int64
	traceID          string
}

type retainedChannelEncoding struct {
	id               string
	channelID        string
	entryID          string
	mediaID          string
	entryStartMs     int64
	entryOffsetMs    int64
	entryDurationMs  int64
	baseMediaStartMs int64
	dir              string
	initPath         string
	segments         []SegmentMeta
	expiresAt        int64
}

type SubtitleSegmentInfo struct {
	MediaStartMs     int64
	DurationMs       int64
	Sequence         int64
	WallClockStartMs int64
}

// SubtitleInfo holds the metadata needed to serve a live on-demand subtitle
// rendition remuxed by the channel encoding.
type SubtitleInfo struct {
	MediaID          string
	EntryStartMs     int64
	EntryOffsetMs    int64
	EntryDurationMs  int64
	BaseMediaStartMs int64
	Segments         []SubtitleSegmentInfo
}

func NewManager(opts ManagerOptions) (*Manager, error) {
	root, err := validateEncodingRoot(opts.Root)
	if err != nil {
		return nil, err
	}
	if root == "" {
		return nil, errors.New("encoding root resolved to empty path")
	}
	// Clear any previous channel encoding data without removing root itself (tmpfs
	// mounts cannot be removed by unprivileged users). MkdirAll handles both
	// missing and existing directories.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		os.RemoveAll(filepath.Join(root, e.Name()))
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create encoding root: %w", err)
	}
	m := &Manager{
		root:                   root,
		graceMs:                defaultInt64(opts.GraceMs, defaultGraceMs),
		trailingMs:             defaultInt64(opts.TrailingMs, defaultTrailingMs),
		minArtifactRetentionMs: opts.MinArtifactRetentionMs,
		burstSec:               opts.BurstSec,
		maxConcurrent:          defaultInt(opts.MaxConcurrent, defaultMaxConcurrent),
		realtimePacing:         opts.RealtimePacing || opts.BurstSec > 0,
		pruneFromEncodedTail:   opts.PruneFromEncodedTail,
		evictIdleMs:            defaultInt64(opts.EvictIdleMs, defaultEvictIdleMs),
		db:                     opts.DB,
		tailInterval:           time.Duration(defaultInt64(opts.TailIntervalMs, defaultTailIntervalMs)) * time.Millisecond,
		stallTimeoutMs:         defaultInt64(opts.StallTimeoutMs, defaultStallTimeoutMs),
		restartBudget:          defaultInt(opts.RestartBudget, defaultRestartBudget),
		restartCooldownMs:      defaultInt64(opts.RestartCooldownMs, defaultRestartCooldownMs),
		now:                    opts.NowFn,
		spawn:                  opts.Spawn,
		encodings:              make(map[string]map[string]*channelEncoding),
		byID:                   make(map[string]*channelEncoding),
		retainedEncodings:      make(map[string]*retainedChannelEncoding),
		lastTouch:              make(map[string]int64),
		restarts:               make(map[string]int),
		blockedUntil:           make(map[string]int64),
		extraDisc:              make(map[string]int64),
		seqHighWater:           make(map[string]int64),
		burnLanguage:           make(map[string]string),
		killedUntil:            make(map[string]int64),
	}
	if m.now == nil {
		m.now = func() int64 { return time.Now().UTC().UnixMilli() }
	}
	if m.spawn == nil {
		m.spawn = m.defaultSpawn
	}
	if m.db != nil {
		if err := db.ClearOnDemandEncodings(context.Background(), m.db); err != nil {
			return nil, fmt.Errorf("clear stale on-demand encodings: %w", err)
		}
	}
	return m, nil
}

func validateEncodingRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("encoding root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve encoding root: %w", err)
	}
	clean := filepath.Clean(abs)
	if clean == string(filepath.Separator) {
		return "", fmt.Errorf("refusing to use filesystem root as encoding root: %s", clean)
	}
	home, _ := os.UserHomeDir()
	if home != "" && clean == filepath.Clean(home) {
		return "", fmt.Errorf("refusing to use home directory as encoding root: %s", clean)
	}
	parts := strings.Split(strings.Trim(clean, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) < 2 {
		return "", fmt.Errorf("encoding root must be a dedicated subdirectory, got %s", clean)
	}
	return clean, nil
}

func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(defaultSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweep(m.now())
		}
	}
}

func (m *Manager) Touch(channelID string) {
	if channelID == "" {
		return
	}
	m.mu.Lock()
	m.lastTouch[channelID] = m.now()
	m.mu.Unlock()
}

func (m *Manager) EnsureEncoding(ctx context.Context, channelID string, entry db.ScheduleEntry, mediaPath string, profile packageprofile.Profile, targetSegmentMs int64) error {
	return m.EnsureEncodingWithOptions(ctx, channelID, entry, mediaPath, profile, targetSegmentMs, EncodingOptions{BurnSubtitleStreamIndex: -1})
}

func (m *Manager) EnsureEncodingWithOptions(ctx context.Context, channelID string, entry db.ScheduleEntry, mediaPath string, profile packageprofile.Profile, targetSegmentMs int64, opts EncodingOptions) error {
	if channelID == "" || entry.ID == "" {
		return errors.New("channelID and entry.ID are required")
	}
	now := m.now()
	m.Touch(channelID)
	for {
		s, err := m.reserveEncoding(channelID, entry, mediaPath, profile, targetSegmentMs, opts, now)
		if errors.Is(err, errAdmissionRetry) {
			continue
		}
		if err != nil {
			return err
		}
		if s == nil {
			return nil
		}
		if err := m.startReserved(ctx, s, now); err != nil {
			return err
		}
		return nil
	}
}

func (m *Manager) reserveEncoding(channelID string, entry db.ScheduleEntry, mediaPath string, profile packageprofile.Profile, targetSegmentMs int64, opts EncodingOptions, now int64) (*channelEncoding, error) {
	m.mu.Lock()
	if until := m.killedUntil[channelID]; until > now {
		m.mu.Unlock()
		return nil, &RetryAfterError{Err: fmt.Errorf("%w (channel %s)", ErrKillGated, channelID), UntilMs: until}
	} else if until > 0 {
		delete(m.killedUntil, channelID)
	}
	realtimePacing := m.encodingRealtimePacing(opts)
	if existing := m.encodings[channelID][entry.ID]; existing != nil {
		if existing.state != stateFailed &&
			existing.burnSubtitleStreamIndex == opts.BurnSubtitleStreamIndex &&
			sameIntSlice(existing.subtitleStreamIndexes, opts.SubtitleStreamIndexes) &&
			existing.realtimePacing == realtimePacing {
			m.mu.Unlock()
			return nil, nil
		}
		delete(m.encodings[channelID], entry.ID)
		delete(m.byID, existing.id)
		m.retainEncodingLocked(existing, now)
		go m.stop(existing)
	}
	if until := m.blockedUntil[entry.ID]; until > now {
		m.mu.Unlock()
		return nil, &RetryAfterError{Err: fmt.Errorf("%w for entry %s", ErrRestartBudgetExhausted, entry.ID), UntilMs: until}
	} else if until > 0 {
		delete(m.blockedUntil, entry.ID)
		delete(m.restarts, entry.ID)
	}
	if m.runningLocked() < m.maxConcurrent {
		s := m.newEncodingLocked(channelID, entry, mediaPath, profile, targetSegmentMs, opts, now)
		m.mu.Unlock()
		return s, nil
	}
	victims := m.detachEvictionVictimLocked(channelID, now)
	m.mu.Unlock()
	if len(victims) == 0 {
		return nil, ErrAtCapacity
	}
	for _, s := range victims {
		m.stop(s)
	}
	return nil, errAdmissionRetry
}

func (m *Manager) newEncodingLocked(channelID string, entry db.ScheduleEntry, mediaPath string, profile packageprofile.Profile, targetSegmentMs int64, opts EncodingOptions, now int64) *channelEncoding {
	m.seq++
	id := fmt.Sprintf("%s-%d", sanitizeID(entry.ID), m.seq)
	dir := filepath.Join(m.root, sanitizeID(channelID), id)
	ctx, cancel := context.WithCancel(context.Background())
	// seekMs is the media position the encode starts from (the playhead) and the
	// first segment's nominal media start. Copy-mode encodes physically start at
	// the source keyframe at or before it (see packager.LiveEncodingArgs).
	startWallMs := now
	if opts.StartWallMs > 0 {
		startWallMs = opts.StartWallMs
	}
	seekMs := entry.OffsetMs
	if startWallMs > entry.StartMs {
		seekMs += startWallMs - entry.StartMs
	}
	// baseSeq is the HLS media sequence of this channel encoding's first segment.
	// Anchor it to the wall-clock grid position, then advance it past any prior
	// encoding for this entry so a restart never reuses a number the previous one
	// already served (hls.js rejects that as a media-sequence mismatch).
	baseSeq := entry.StartMs/db.ScheduleGridMs + divRound(seekMs-entry.OffsetMs, db.ScheduleGridMs)
	if hw, ok := m.seqHighWater[entry.ID]; ok && baseSeq <= hw {
		baseSeq = hw + 1
	}
	realtimePacing := m.encodingRealtimePacing(opts)
	s := &channelEncoding{
		id:                      id,
		channelID:               channelID,
		entry:                   entry,
		mediaPath:               mediaPath,
		profile:                 profile,
		targetMs:                targetSegmentMs,
		burnSubtitleStreamIndex: opts.BurnSubtitleStreamIndex,
		subtitleStreamIndexes:   append([]int(nil), opts.SubtitleStreamIndexes...),
		realtimePacing:          realtimePacing,
		dir:                     dir,
		initPath:                layout.InitPath(dir),
		ctx:                     ctx,
		cancel:                  cancel,
		done:                    make(chan struct{}),
		state:                   stateStarting,
		lastProgressMs:          now,
		processRunning:          true,
		baseMediaStartMs:        seekMs,
		baseSeq:                 baseSeq,
		traceID:                 opts.TraceID,
	}
	metrics.OnDemandEncodings.WithLabelValues("starting").Inc()
	if m.encodings[channelID] == nil {
		m.encodings[channelID] = make(map[string]*channelEncoding)
	}
	m.encodings[channelID][entry.ID] = s
	m.byID[id] = s
	return s
}

func (m *Manager) encodingRealtimePacing(opts EncodingOptions) bool {
	if opts.RealtimePacing != nil {
		return *opts.RealtimePacing
	}
	return m.realtimePacing
}

func (m *Manager) startReserved(ctx context.Context, s *channelEncoding, now int64) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		// Count channel-encoding dir failures against the restart budget. A failed
		// encoding is deleted and re-reserved on the next manifest poll, so a
		// persistent dir failure (full/read-only cache disk) would otherwise
		// hot-loop spawn attempts at the poll rate with no backoff.
		m.markFailed(s, fmt.Errorf("create encoding dir: %w", err))
		return err
	}
	for _, idx := range s.subtitleStreamIndexes {
		if err := os.MkdirAll(filepath.Join(s.dir, "subs", fmt.Sprintf("s%d", idx)), 0o755); err != nil {
			m.markFailed(s, fmt.Errorf("create subtitle dir: %w", err))
			return err
		}
	}
	// seekMs and baseMediaStartMs were fixed when the channel encoding was reserved
	// (newEncodingLocked) so they stay consistent with the assigned baseSeq.
	seekMs := s.baseMediaStartMs
	entryEnd := s.entry.OffsetMs + s.entry.DurationMs
	limitMs := entryEnd - seekMs
	if limitMs < 0 {
		limitMs = 0
	}
	s.spawnedAt = m.now()
	if s.traceID != "" {
		log.Printf("ondemand ffmpeg start trace_id=%s channel=%s entry=%s spawned_at_ms=%d seek_ms=%d",
			s.traceID, s.channelID, s.entry.ID, s.spawnedAt, s.baseMediaStartMs)
	}
	var burnSubtitleStreamIndex *int
	if s.burnSubtitleStreamIndex >= 0 {
		v := s.burnSubtitleStreamIndex
		burnSubtitleStreamIndex = &v
	}
	proc, err := m.spawn(s.ctx, packager.LiveEncodingSpec{
		MediaPath:               s.mediaPath,
		OutDir:                  s.dir,
		SeekMs:                  seekMs,
		LimitMs:                 limitMs,
		TargetSegmentMs:         s.targetMs,
		RealtimePacing:          s.realtimePacing,
		BurstSec:                m.burstSec,
		Profile:                 s.profile,
		BurnSubtitleStreamIndex: burnSubtitleStreamIndex,
		SubtitleStreamIndexes:   append([]int(nil), s.subtitleStreamIndexes...),
	})
	if err != nil {
		// Count spawn failures against the restart budget for the same reason as
		// channel-encoding dir failures above: a persistent ffmpeg spawn error (missing
		// binary, bad media path, profile error) must back off into a cooldown
		// with Retry-After instead of re-spawning on every poll.
		m.markFailed(s, err)
		return err
	}
	m.recordEncoding(s)
	m.tailOnce(s)
	go m.tailLoop(s)
	go m.waitLoop(s, proc)
	return nil
}

func (m *Manager) defaultSpawn(ctx context.Context, spec packager.LiveEncodingSpec) (Process, error) {
	args, err := packager.LiveEncodingArgs(ctx, spec)
	if err != nil {
		return nil, err
	}
	cmd, err := ffmpegexec.CommandContext(ctx, "ffmpeg", args...)
	if err != nil {
		return nil, err
	}
	setProcessDeathSignal(cmd)
	// ffmpeg runs with -loglevel error, so stderr carries only real failures.
	// Keep a bounded tail and fold it into the Wait error — without it a dead
	// channel encoding reports a bare "exit status 1" and the restart reason (stall kill
	// vs copy/mux error) can't be told apart from the WARN log line.
	tail := &stderrTail{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &ffmpegProcess{cmd: cmd, stderr: tail}, nil
}

type ffmpegProcess struct {
	cmd    *exec.Cmd
	stderr *stderrTail
}

func (p *ffmpegProcess) Wait() error {
	err := p.cmd.Wait()
	if err == nil {
		return nil
	}
	if tail := p.stderr.Tail(); tail != "" {
		return fmt.Errorf("%w; ffmpeg stderr: %s", err, tail)
	}
	return err
}

const stderrTailMax = 4 * 1024

// stderrTail retains the last stderrTailMax bytes written. Write never fails,
// so a chatty ffmpeg can't block on a full pipe.
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - stderrTailMax; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

// Tail returns the retained stderr collapsed to one line for log output.
func (t *stderrTail) Tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(strings.Fields(string(t.buf)), " ")
}

func (m *Manager) waitLoop(s *channelEncoding, proc Process) {
	err := proc.Wait()
	m.mu.Lock()
	if s.state != stateFailed && s.state != stateStopping {
		if err != nil {
			m.noteFailureLocked(s, err)
		} else {
			s.state = stateEnded
			s.processRunning = false
		}
	}
	s.waitErr = err
	s.processRunning = false
	m.mu.Unlock()
	m.recordEncoding(s)
	close(s.done)
}

func (m *Manager) tailLoop(s *channelEncoding) {
	t := time.NewTicker(m.tailInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			m.tailOnce(s)
			return
		case <-s.ctx.Done():
			return
		case <-t.C:
			m.tailOnce(s)
		}
	}
}

func (m *Manager) tailOnce(s *channelEncoding) {
	playlist := layout.PlaylistPath(s.dir)
	parsed, err := packager.ParseHLSManifest(playlist)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		return
	}
	changed := false
	m.mu.Lock()
	if s.state == stateStarting && len(parsed) > 0 {
		s.state = stateServing
		s.firstSegmentAtMs = m.now()
		changed = true
		metrics.OnDemandEncodings.WithLabelValues("starting").Dec()
		metrics.OnDemandEncodings.WithLabelValues("serving").Inc()
		if s.spawnedAt > 0 {
			metrics.OnDemandEncodingSpawnLatency.Observe(float64(m.now()-s.spawnedAt) / 1000)
		}
		metrics.OnDemandEncodingSpawnsTotal.Inc()
		if s.traceID != "" {
			spawnLatency := s.firstSegmentAtMs - s.spawnedAt
			log.Printf("ondemand first segment trace_id=%s channel=%s entry=%s first_segment_at_ms=%d spawn_latency_ms=%d",
				s.traceID, s.channelID, s.entry.ID, s.firstSegmentAtMs, spawnLatency)
		}
	}
	if len(parsed) <= s.parsedSegments {
		m.mu.Unlock()
		if changed {
			m.recordEncoding(s)
		}
		return
	}
	mediaStart := s.nextMediaStartMs
	if s.parsedSegments == 0 {
		mediaStart = s.baseMediaStartMs
	}
	for i := s.parsedSegments; i < len(parsed); i++ {
		seg := parsed[i]
		path := seg.URI
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.dir, path)
		}
		s.segments = append(s.segments, SegmentMeta{
			EncodingID:   s.id,
			Index:        int64(i),
			MediaStartMs: mediaStart,
			DurationMs:   seg.DurationMs,
			Path:         path,
			InitPath:     s.initPath,
			BaseSeq:      s.baseSeq,
		})
		mediaStart += seg.DurationMs
	}
	s.parsedSegments = len(parsed)
	s.nextMediaStartMs = mediaStart
	s.lastProgressMs = m.now()
	changed = true
	if hw := s.baseSeq + int64(s.parsedSegments) - 1; hw > m.seqHighWater[s.entry.ID] {
		m.seqHighWater[s.entry.ID] = hw
	}
	m.mu.Unlock()
	if changed {
		m.recordEncoding(s)
	}
}

func (m *Manager) SegmentsFrom(channelID, entryID string, mediaPosMs int64, limit int) []SegmentMeta {
	if limit <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.encodings[channelID][entryID]
	if s == nil {
		return nil
	}
	return segmentsFromSlice(s.segments, mediaPosMs, limit)
}

// SegmentsFromWithRetained behaves like SegmentsFrom but, when no active encoding
// exists for entryID, falls back to a non-expired retained encoding for that
// entry. This keeps a program's trailing segments servable across the boundary
// after its encode reaches entry_over and is retained — the manifest builder,
// unlike per-segment file serving, otherwise only sees active encodings.
func (m *Manager) SegmentsFromWithRetained(channelID, entryID string, mediaPosMs int64, limit int) []SegmentMeta {
	if limit <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.encodings[channelID][entryID]; s != nil {
		return segmentsFromSlice(s.segments, mediaPosMs, limit)
	}
	now := m.now()
	var best *retainedChannelEncoding
	for _, r := range m.retainedEncodings {
		if r.channelID != channelID || r.entryID != entryID || r.expiresAt <= now {
			continue
		}
		if best == nil || r.expiresAt > best.expiresAt {
			best = r
		}
	}
	if best == nil {
		return nil
	}
	return segmentsFromSlice(best.segments, mediaPosMs, limit)
}

// segmentsFromSlice returns up to limit segments starting at the one covering
// mediaPosMs (or the first at/after it). The caller holds m.mu.
func segmentsFromSlice(segments []SegmentMeta, mediaPosMs int64, limit int) []SegmentMeta {
	first := -1
	for i, seg := range segments {
		if seg.MediaStartMs <= mediaPosMs && seg.MediaStartMs+seg.DurationMs > mediaPosMs {
			first = i
			break
		}
	}
	if first == -1 {
		for i, seg := range segments {
			if seg.MediaStartMs >= mediaPosMs {
				first = i
				break
			}
		}
	}
	if first == -1 {
		return nil
	}
	end := first + limit
	if end > len(segments) {
		end = len(segments)
	}
	out := make([]SegmentMeta, end-first)
	copy(out, segments[first:end])
	return out
}

// LatestSegments returns the newest available segments for an active channel encoding.
// It is used when the wall-clock serve position has briefly outrun the live
// encoder: serving the tail lets the client wait on a valid media playlist
// instead of receiving a 503 level-load error.
func (m *Manager) LatestSegments(channelID, entryID string, limit int) []SegmentMeta {
	if limit <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.encodings[channelID][entryID]
	if s == nil || len(s.segments) == 0 {
		return nil
	}
	start := len(s.segments) - limit
	if start < 0 {
		start = 0
	}
	out := make([]SegmentMeta, len(s.segments)-start)
	copy(out, s.segments[start:])
	return out
}

// EncodingTiming returns the ffmpeg spawn time and first-segment completion time
// for the channel encoding serving the given channel/entry pair. Times are in Unix ms.
// ok is false if no live encoding exists.
func (m *Manager) EncodingTiming(channelID, entryID string) (spawnedAtMs, firstSegmentAtMs int64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.encodings[channelID][entryID]
	if s == nil {
		return 0, 0, false
	}
	return s.spawnedAt, s.firstSegmentAtMs, true
}

// EncodingSnapshot returns read-only runtime state for the active channel
// encoding serving a schedule entry. It is intentionally ephemeral; managers
// with DB disabled still report here.
func (m *Manager) EncodingSnapshot(channelID, entryID string) (EncodingSnapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.encodings[channelID][entryID]
	if s == nil {
		return EncodingSnapshot{}, false
	}
	lastErr := ""
	if s.waitErr != nil {
		lastErr = s.waitErr.Error()
	}
	return EncodingSnapshot{
		EncodingID:       s.id,
		State:            string(s.state),
		ProcessRunning:   s.processRunning,
		SpawnedAtMs:      s.spawnedAt,
		FirstSegmentAtMs: s.firstSegmentAtMs,
		LastProgressMs:   s.lastProgressMs,
		SegmentCount:     s.parsedSegments,
		LastError:        lastErr,
	}, true
}

func (m *Manager) recordEncoding(s *channelEncoding) {
	if m.db == nil || s == nil || s.spawnedAt == 0 {
		return
	}
	row := m.encodingRow(s)
	if err := db.UpsertOnDemandEncoding(context.Background(), m.db, row); err != nil {
		log.Printf("WARN upsert on-demand channel encoding failed encoding_id=%s channel_id=%s err=%v", s.id, s.channelID, err)
	}
}

func (m *Manager) encodingRow(s *channelEncoding) db.OnDemandEncoding {
	m.mu.Lock()
	defer m.mu.Unlock()
	lastErr := ""
	if s.waitErr != nil {
		lastErr = s.waitErr.Error()
	}
	return db.OnDemandEncoding{
		EncodingID:       s.id,
		ChannelID:        s.channelID,
		ScheduleEntryID:  s.entry.ID,
		MediaID:          s.entry.MediaID,
		Profile:          s.profile.Name,
		State:            string(s.state),
		ProcessRunning:   s.processRunning,
		SpawnedAtMs:      s.spawnedAt,
		FirstSegmentAtMs: s.firstSegmentAtMs,
		LastProgressMs:   s.lastProgressMs,
		SegmentCount:     s.parsedSegments,
		UpdatedAtMs:      m.now(),
		LastError:        lastErr,
	}
}

func (m *Manager) deleteEncodingRow(encodingID string) {
	if m.db == nil || encodingID == "" {
		return
	}
	if err := db.DeleteOnDemandEncoding(context.Background(), m.db, encodingID); err != nil {
		log.Printf("WARN delete on-demand channel encoding failed encoding_id=%s err=%v", encodingID, err)
	}
}

func (m *Manager) InitPath(channelID, encodingID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.byID[encodingID]
	if s != nil && s.channelID == channelID {
		if _, err := os.Stat(s.initPath); err != nil {
			return "", false
		}
		return s.initPath, true
	}
	retained := m.retainedEncodings[encodingID]
	if retained == nil || retained.channelID != channelID || retained.expiresAt <= m.now() {
		return "", false
	}
	if _, err := os.Stat(retained.initPath); err != nil {
		return "", false
	}
	return retained.initPath, true
}

// EncodingDir returns the directory for an active or recently-retained channel encoding.
func (m *Manager) EncodingDir(channelID, encodingID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.byID[encodingID]
	if s != nil && s.channelID == channelID {
		return s.dir, true
	}
	retained := m.retainedEncodings[encodingID]
	if retained == nil || retained.channelID != channelID || retained.expiresAt <= m.now() {
		return "", false
	}
	return retained.dir, true
}

func (m *Manager) SegmentPath(channelID, encodingID string, index int64) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.byID[encodingID]
	if s != nil && s.channelID == channelID {
		for _, seg := range s.segments {
			if seg.Index == index {
				if _, err := os.Stat(seg.Path); err != nil {
					return "", false
				}
				return seg.Path, true
			}
		}
		return "", false
	}
	retained := m.retainedEncodings[encodingID]
	if retained == nil || retained.channelID != channelID || retained.expiresAt <= m.now() {
		return "", false
	}
	for _, seg := range retained.segments {
		if seg.Index == index {
			if _, err := os.Stat(seg.Path); err != nil {
				return "", false
			}
			return seg.Path, true
		}
	}
	return "", false
}

func (m *Manager) ExtraDiscontinuities(channelID string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.extraDisc[channelID]
}

// EncodingID returns the live encoding ID for an active or recently-retained
// schedule entry.
func (m *Manager) EncodingID(channelID, entryID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entryID == "" {
		return "", false
	}
	if s := m.encodings[channelID][entryID]; s != nil {
		if s.state == stateFailed || s.state == stateStopping || s.state == stateEnded {
			return "", false
		}
		return s.id, true
	}
	for _, r := range m.retainedEncodings {
		if r.channelID == channelID && r.entryID == entryID && r.expiresAt > m.now() {
			return r.id, true
		}
	}
	return "", false
}

// SubtitleInfo returns the timing metadata needed to synthesize a WebVTT
// playlist for an active or recently-retained channel encoding keyed by encoding ID.
func (m *Manager) SubtitleInfo(channelID, encodingID string) (SubtitleInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if encodingID == "" {
		return SubtitleInfo{}, false
	}
	if s := m.byID[encodingID]; s != nil && s.channelID == channelID {
		return subtitleInfoForEncoding(s.entry, s.baseMediaStartMs, s.segments), true
	}
	r := m.retainedEncodings[encodingID]
	if r == nil || r.channelID != channelID || r.expiresAt <= m.now() {
		return SubtitleInfo{}, false
	}
	entry := db.ScheduleEntry{
		MediaID:    r.mediaID,
		StartMs:    r.entryStartMs,
		OffsetMs:   r.entryOffsetMs,
		DurationMs: r.entryDurationMs,
	}
	return subtitleInfoForEncoding(entry, r.baseMediaStartMs, r.segments), true
}

func (m *Manager) BurnSubtitleLanguage(channelID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.burnLanguage[channelID]
}

func (m *Manager) SetBurnSubtitleLanguage(channelID, language string) {
	language = strings.ToLower(language)
	m.mu.Lock()
	if m.burnLanguage[channelID] == language {
		m.mu.Unlock()
		return
	}
	if language == "" {
		delete(m.burnLanguage, channelID)
	} else {
		m.burnLanguage[channelID] = language
	}
	m.mu.Unlock()
	m.RestartChannel(channelID)
}

func (m *Manager) RestartChannel(channelID string) {
	var teardown []*channelEncoding
	m.mu.Lock()
	for entryID, s := range m.encodings[channelID] {
		delete(m.byID, s.id)
		m.retainEncodingLocked(s, m.now())
		teardown = append(teardown, s)
		if hw := s.baseSeq + int64(s.parsedSegments) - 1; hw > m.seqHighWater[entryID] {
			m.seqHighWater[entryID] = hw
		}
	}
	if len(teardown) > 0 {
		delete(m.encodings, channelID)
		m.extraDisc[channelID]++
	}
	m.mu.Unlock()
	for _, s := range teardown {
		m.stop(s)
	}
}

// KillChannel stops all active encodings for the channel (same as
// RestartChannel) and then blocks new encoding admission for killGateMs. The
// gate prevents the warm-keeper and incoming viewers from immediately
// restarting the encoder after an operator-initiated stop.
func (m *Manager) KillChannel(channelID string) {
	var teardown []*channelEncoding
	m.mu.Lock()
	for entryID, s := range m.encodings[channelID] {
		delete(m.byID, s.id)
		m.retainEncodingLocked(s, m.now())
		teardown = append(teardown, s)
		if hw := s.baseSeq + int64(s.parsedSegments) - 1; hw > m.seqHighWater[entryID] {
			m.seqHighWater[entryID] = hw
		}
	}
	if len(teardown) > 0 {
		delete(m.encodings, channelID)
		m.extraDisc[channelID]++
	}
	m.killedUntil[channelID] = m.now() + killGateMs
	m.mu.Unlock()
	for _, s := range teardown {
		m.stop(s)
	}
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	var all []*channelEncoding
	for _, byEntry := range m.encodings {
		for _, s := range byEntry {
			all = append(all, s)
		}
	}
	m.encodings = make(map[string]map[string]*channelEncoding)
	m.byID = make(map[string]*channelEncoding)
	m.retainedEncodings = make(map[string]*retainedChannelEncoding)
	m.mu.Unlock()
	for _, s := range all {
		m.stopAndRemove(s)
	}
	_ = os.RemoveAll(m.root)
	if m.db != nil {
		if err := db.ClearOnDemandEncodings(context.Background(), m.db); err != nil {
			log.Printf("WARN clear on-demand channel encodings failed err=%v", err)
		}
	}
}

func (m *Manager) sweep(now int64) {
	var teardown []*channelEncoding
	var removeDirs []string
	m.mu.Lock()
	for channelID, byEntry := range m.encodings {
		idle := now-m.lastTouch[channelID] > m.graceMs
		for entryID, s := range byEntry {
			m.pruneTrailingLocked(s, now)
			entryOver := now > s.entry.StartMs+s.entry.DurationMs+m.trailingMs
			stalled := s.processRunning && now-s.lastProgressMs > m.stallTimeoutMs
			if stalled {
				m.noteFailureLocked(s, fmt.Errorf("encoding stalled after %dms (last segment %dms ago)", m.stallTimeoutMs, now-s.lastProgressMs))
			}
			encodingIdle := idle
			if encodingIdle || entryOver || stalled {
				log.Printf("INFO on-demand channel encoding teardown channel_id=%s entry_id=%s encoding_id=%s reason=%s state=%s segments=%d untouched_ms=%d",
					channelID, entryID, s.id, teardownReason(encodingIdle, entryOver, stalled), s.state, s.parsedSegments, now-m.lastTouch[channelID])
				delete(byEntry, entryID)
				delete(m.byID, s.id)
				m.retainEncodingLocked(s, now)
				// Once the program is over no further channel encoding will serve this
				// entry, so its sequence high-water can be forgotten. Keep it for
				// idle/stall teardown — a re-admitted or restarted encoding for the
				// same entry must still number past what was already served.
				if entryOver {
					delete(m.seqHighWater, entryID)
				}
				teardown = append(teardown, s)
			}
		}
		if len(byEntry) == 0 {
			delete(m.encodings, channelID)
			delete(m.lastTouch, channelID)
			delete(m.extraDisc, channelID)
		}
	}
	for id, retained := range m.retainedEncodings {
		if retained.expiresAt <= now {
			removeDirs = append(removeDirs, retained.dir)
			delete(m.retainedEncodings, id)
		}
	}
	m.mu.Unlock()
	for _, s := range teardown {
		m.stop(s)
	}
	for _, dir := range removeDirs {
		_ = os.RemoveAll(dir)
	}
}

func (m *Manager) pruneTrailingLocked(s *channelEncoding, now int64) {
	playhead := s.entry.OffsetMs + clamp(now-s.entry.StartMs, 0, s.entry.DurationMs)
	if m.pruneFromEncodedTail {
		if len(s.segments) == 0 {
			return
		}
		playhead = s.segments[len(s.segments)-1].MediaStartMs + s.segments[len(s.segments)-1].DurationMs
	}
	cutoff := playhead - m.artifactRetentionMs(s)
	keep := s.segments[:0]
	for _, seg := range s.segments {
		if seg.MediaStartMs+seg.DurationMs < cutoff {
			_ = os.Remove(seg.Path)
			continue
		}
		keep = append(keep, seg)
	}
	s.segments = keep
}

func (m *Manager) detachEvictionVictimLocked(requestingChannel string, now int64) []*channelEncoding {
	type candidate struct {
		channelID string
		touched   int64
	}
	var candidates []candidate
	for channelID, touched := range m.lastTouch {
		if channelID == requestingChannel || now-touched < m.evictIdleMs {
			continue
		}
		if len(m.encodings[channelID]) == 0 {
			continue
		}
		candidates = append(candidates, candidate{channelID: channelID, touched: touched})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].touched < candidates[j].touched })
	victim := candidates[0].channelID
	metrics.OnDemandEncodingEvictionsTotal.Inc()
	var out []*channelEncoding
	for entryID, s := range m.encodings[victim] {
		log.Printf("INFO on-demand channel encoding evicted channel_id=%s entry_id=%s encoding_id=%s for_channel_id=%s untouched_ms=%d",
			victim, entryID, s.id, requestingChannel, now-candidates[0].touched)
		delete(m.byID, s.id)
		delete(m.encodings[victim], entryID)
		m.retainEncodingLocked(s, now)
		out = append(out, s)
	}
	delete(m.encodings, victim)
	delete(m.lastTouch, victim)
	delete(m.extraDisc, victim)
	return out
}

// runningLocked counts active channel encodings — the load that competes for the
// MaxConcurrent budget.
func (m *Manager) runningLocked() int {
	n := 0
	for _, s := range m.byID {
		if s.processRunning && (s.state == stateStarting || s.state == stateServing) {
			n++
		}
	}
	return n
}

// markFailed records a startup-time failure (channel-encoding dir create or ffmpeg
// spawn). Both count against the entry restart budget so a persistent failure
// backs off into a cooldown instead of re-spawning on every manifest poll.
func (m *Manager) markFailed(s *channelEncoding, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.noteFailureLocked(s, err)
	close(s.done)
}

func (m *Manager) noteFailureLocked(s *channelEncoding, err error) {
	if s.failCounted {
		prev := s.state
		s.state = stateFailed
		s.processRunning = false
		s.waitErr = err
		switch prev {
		case stateStarting:
			metrics.OnDemandEncodings.WithLabelValues("starting").Dec()
		case stateServing:
			metrics.OnDemandEncodings.WithLabelValues("serving").Dec()
		}
		metrics.OnDemandEncodings.WithLabelValues("failed").Inc()
		return
	}
	prev := s.state
	s.state = stateFailed
	s.processRunning = false
	s.waitErr = err
	s.failCounted = true
	m.restarts[s.entry.ID]++
	m.extraDisc[s.channelID]++
	if m.restarts[s.entry.ID] >= m.restartBudget {
		m.blockedUntil[s.entry.ID] = m.now() + m.restartCooldownMs
	}
	switch prev {
	case stateStarting:
		metrics.OnDemandEncodings.WithLabelValues("starting").Dec()
	case stateServing:
		metrics.OnDemandEncodings.WithLabelValues("serving").Dec()
	}
	metrics.OnDemandEncodings.WithLabelValues("failed").Inc()
	metrics.OnDemandEncodingRestartsTotal.Inc()
	log.Printf("WARN on-demand channel encoding failed channel_id=%s entry_id=%s encoding_id=%s err=%q", s.channelID, s.entry.ID, s.id, err.Error())
}

func (m *Manager) retainEncodingLocked(s *channelEncoding, now int64) {
	if s == nil {
		return
	}
	// Record the retained encoding's highest sequence so any new channel encoding for
	// this entry advances past what was already served. Without this, a
	// replacement encoding can reuse a media sequence number the old encoding
	// already used, causing hls.js media sequence mismatch errors.
	if len(s.segments) > 0 {
		maxSeq := s.baseSeq + int64(len(s.segments)) - 1
		if maxSeq > m.seqHighWater[s.entry.ID] {
			m.seqHighWater[s.entry.ID] = maxSeq
		}
	}
	segments := make([]SegmentMeta, len(s.segments))
	copy(segments, s.segments)
	expiresAt := now + m.artifactRetentionMs(s)
	if existing := m.retainedEncodings[s.id]; existing != nil && existing.expiresAt > expiresAt {
		expiresAt = existing.expiresAt
	}
	m.retainedEncodings[s.id] = &retainedChannelEncoding{
		id:               s.id,
		channelID:        s.channelID,
		entryID:          s.entry.ID,
		mediaID:          s.entry.MediaID,
		entryStartMs:     s.entry.StartMs,
		entryOffsetMs:    s.entry.OffsetMs,
		entryDurationMs:  s.entry.DurationMs,
		baseMediaStartMs: s.baseMediaStartMs,
		dir:              s.dir,
		initPath:         s.initPath,
		segments:         segments,
		expiresAt:        expiresAt,
	}
}

func (m *Manager) artifactRetentionMs(s *channelEncoding) int64 {
	// HLS clients may legally request segment URIs from an older playlist for
	// roughly their live sync depth after Linearcast has moved on. Keep at least
	// three target segments plus slack, never less than the configured drain
	// window, and long enough to cover the on-demand playback lookback.
	retainMs := s.targetMs*3 + 10_000
	if retainMs < m.trailingMs {
		retainMs = m.trailingMs
	}
	if retainMs < m.minArtifactRetentionMs {
		retainMs = m.minArtifactRetentionMs
	}
	return retainMs
}

func (m *Manager) stop(s *channelEncoding) {
	m.mu.Lock()
	prevState := s.state
	if s.state != stateFailed {
		s.state = stateStopping
	}
	s.processRunning = false
	m.mu.Unlock()
	m.recordEncoding(s)
	switch prevState {
	case stateStarting:
		metrics.OnDemandEncodings.WithLabelValues("starting").Dec()
	case stateServing:
		metrics.OnDemandEncodings.WithLabelValues("serving").Dec()
	case stateFailed:
		metrics.OnDemandEncodings.WithLabelValues("failed").Dec()
	}
	s.cancel()
	<-s.done
	m.deleteEncodingRow(s.id)
}

func (m *Manager) stopAndRemove(s *channelEncoding) {
	m.stop(s)
	_ = os.RemoveAll(s.dir)
}

func teardownReason(idle, entryOver, stalled bool) string {
	switch {
	case stalled:
		return "stalled"
	case entryOver:
		return "entry_over"
	case idle:
		return "idle"
	}
	return "unknown"
}

func defaultInt64(v, fallback int64) int64 {
	if v <= 0 {
		return fallback
	}
	return v
}

func defaultInt(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

func sameIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func subtitleInfoForEncoding(entry db.ScheduleEntry, baseMediaStartMs int64, segments []SegmentMeta) SubtitleInfo {
	out := make([]SubtitleSegmentInfo, 0, len(segments))
	for _, seg := range segments {
		out = append(out, SubtitleSegmentInfo{
			MediaStartMs:     seg.MediaStartMs,
			DurationMs:       seg.DurationMs,
			Sequence:         seg.BaseSeq + seg.Index,
			WallClockStartMs: entry.StartMs + (seg.MediaStartMs - entry.OffsetMs),
		})
	}
	return SubtitleInfo{
		MediaID:          entry.MediaID,
		EntryStartMs:     entry.StartMs,
		EntryOffsetMs:    entry.OffsetMs,
		EntryDurationMs:  entry.DurationMs,
		BaseMediaStartMs: baseMediaStartMs,
		Segments:         out,
	}
}

// divRound divides v by denom, rounding to the nearest integer (ties away from
// zero). It mirrors the manifest layer's grid bucketing so an encoding's anchor
// sequence matches what onDemandMediaSequence would compute for its first segment.
func divRound(v, denom int64) int64 {
	if denom <= 0 {
		return 0
	}
	if v >= 0 {
		return (v + denom/2) / denom
	}
	return (v - denom/2) / denom
}

func clamp(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "encoding"
	}
	return b.String()
}

var _ Process = (*exec.Cmd)(nil)
