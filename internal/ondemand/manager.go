// Package ondemand manages ephemeral live encodes for on-demand playback.
package ondemand

import (
	"context"
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
	"github.com/tckrcr/linearcast/internal/metrics"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/packager"
)

const (
	defaultGraceMs        int64 = 120_000
	defaultTrailingMs     int64 = 30_000
	defaultMaxConcurrent        = 4
	defaultEvictIdleMs    int64 = 10_000
	defaultTailIntervalMs int64 = 300
	defaultStallTimeoutMs int64 = 45_000
	defaultRestartBudget        = 3
	defaultSweepInterval        = 5 * time.Second
)

var ErrAtCapacity = errors.New("on-demand live session capacity reached")
var errAdmissionRetry = errors.New("retry on-demand admission")

type ManagerOptions struct {
	Root           string
	GraceMs        int64
	TrailingMs     int64
	BurstSec       int
	MaxConcurrent  int
	EvictIdleMs    int64
	TailIntervalMs int64
	StallTimeoutMs int64
	RestartBudget  int
	MaxKeepaliveMs int64
	NowFn          func() int64
	Spawn          SpawnFunc
}

type SpawnFunc func(ctx context.Context, spec packager.LiveSessionSpec) (Process, error)

type SessionOptions struct {
	BurnSubtitleStreamIndex int
}

type Process interface {
	Wait() error
}

type SegmentMeta struct {
	SessionID    string
	Index        int64
	MediaStartMs int64
	DurationMs   int64
	Path         string
	InitPath     string
	// BaseSeq is the HLS media sequence number of this session's first segment
	// (Index 0). The manifest numbers each segment BaseSeq+Index, which stays
	// contiguous regardless of copy-mode's irregular segment durations and
	// monotonic across session restarts: a replacement session's BaseSeq is
	// advanced past the prior session's high-water mark, so a given media
	// sequence number never maps to two different segments (which hls.js rejects
	// as a media-sequence mismatch).
	BaseSeq int64
}

type Manager struct {
	root           string
	graceMs        int64
	trailingMs     int64
	burstSec       int
	maxConcurrent  int
	evictIdleMs    int64
	tailInterval   time.Duration
	stallTimeoutMs int64
	restartBudget  int
	maxKeepaliveMs int64
	now            func() int64
	spawn          SpawnFunc

	mu        sync.Mutex
	sessions  map[string]map[string]*session
	byID      map[string]*session
	lastTouch map[string]int64
	restarts  map[string]int
	blacklist map[string]bool
	extraDisc map[string]int64
	// seqHighWater tracks, per entry ID, the highest HLS media sequence number
	// any session for that entry has emitted, so a restarted session can be
	// numbered strictly past it rather than colliding with already-served
	// segments.
	seqHighWater map[string]int64
	seq          int64

	keepaliveUntil map[string]int64
	burnLanguage   map[string]string
}

type sessionState string

const (
	stateStarting sessionState = "starting"
	stateServing  sessionState = "serving"
	stateEnded    sessionState = "ended"
	stateFailed   sessionState = "failed"
	stateStopping sessionState = "stopping"
)

type session struct {
	id                      string
	channelID               string
	entry                   db.ScheduleEntry
	mediaPath               string
	profile                 packageprofile.Profile
	targetMs                int64
	burnSubtitleStreamIndex int
	dir                     string
	initPath                string

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	state            sessionState
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
}

func NewManager(opts ManagerOptions) (*Manager, error) {
	root, err := validateSessionRoot(opts.Root)
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(root); err != nil {
		return nil, fmt.Errorf("wipe session root: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create session root: %w", err)
	}
	keepaliveMs := opts.MaxKeepaliveMs
	if keepaliveMs <= 0 {
		keepaliveMs = 15 * 60 * 1000 // 15 min default ceiling
	}
	m := &Manager{
		root:           root,
		graceMs:        defaultInt64(opts.GraceMs, defaultGraceMs),
		trailingMs:     defaultInt64(opts.TrailingMs, defaultTrailingMs),
		burstSec:       opts.BurstSec,
		maxConcurrent:  defaultInt(opts.MaxConcurrent, defaultMaxConcurrent),
		evictIdleMs:    defaultInt64(opts.EvictIdleMs, defaultEvictIdleMs),
		tailInterval:   time.Duration(defaultInt64(opts.TailIntervalMs, defaultTailIntervalMs)) * time.Millisecond,
		stallTimeoutMs: defaultInt64(opts.StallTimeoutMs, defaultStallTimeoutMs),
		restartBudget:  defaultInt(opts.RestartBudget, defaultRestartBudget),
		maxKeepaliveMs: keepaliveMs,
		now:            opts.NowFn,
		spawn:          opts.Spawn,
		sessions:       make(map[string]map[string]*session),
		byID:           make(map[string]*session),
		lastTouch:      make(map[string]int64),
		restarts:       make(map[string]int),
		blacklist:      make(map[string]bool),
		extraDisc:      make(map[string]int64),
		seqHighWater:   make(map[string]int64),
		keepaliveUntil: make(map[string]int64),
		burnLanguage:   make(map[string]string),
	}
	if m.now == nil {
		m.now = func() int64 { return time.Now().UTC().UnixMilli() }
	}
	if m.spawn == nil {
		m.spawn = m.defaultSpawn
	}
	return m, nil
}

func validateSessionRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("session root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve session root: %w", err)
	}
	clean := filepath.Clean(abs)
	if clean == string(filepath.Separator) {
		return "", fmt.Errorf("refusing to use filesystem root as session root: %s", clean)
	}
	home, _ := os.UserHomeDir()
	if home != "" && clean == filepath.Clean(home) {
		return "", fmt.Errorf("refusing to use home directory as session root: %s", clean)
	}
	parts := strings.Split(strings.Trim(clean, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) < 2 {
		return "", fmt.Errorf("session root must be a dedicated subdirectory, got %s", clean)
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

// KeepAlive extends the session lifetime for a paused client. Returns false
// when the keepalive ceiling has been exceeded and the session should be
// allowed to die. The ceiling prevents an idle paused client from keeping
// an encoder running indefinitely.
func (m *Manager) KeepAlive(channelID string) bool {
	if channelID == "" {
		return false
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastTouch[channelID] = now
	if m.keepaliveUntil[channelID] == 0 {
		m.keepaliveUntil[channelID] = now + m.maxKeepaliveMs
	}
	if now > m.keepaliveUntil[channelID] {
		return false
	}
	return true
}

// ClearKeepalive resets the keepalive timer for a channel when the client
// resumes playback (no longer paused).
func (m *Manager) ClearKeepalive(channelID string) {
	if channelID == "" {
		return
	}
	m.mu.Lock()
	delete(m.keepaliveUntil, channelID)
	m.mu.Unlock()
}

func (m *Manager) EnsureSession(ctx context.Context, channelID string, entry db.ScheduleEntry, mediaPath string, profile packageprofile.Profile, targetSegmentMs int64) error {
	return m.EnsureSessionWithOptions(ctx, channelID, entry, mediaPath, profile, targetSegmentMs, SessionOptions{BurnSubtitleStreamIndex: -1})
}

func (m *Manager) EnsureSessionWithOptions(ctx context.Context, channelID string, entry db.ScheduleEntry, mediaPath string, profile packageprofile.Profile, targetSegmentMs int64, opts SessionOptions) error {
	if channelID == "" || entry.ID == "" {
		return errors.New("channelID and entry.ID are required")
	}
	now := m.now()
	m.Touch(channelID)
	for {
		s, err := m.reserveSession(channelID, entry, mediaPath, profile, targetSegmentMs, opts, now)
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

func (m *Manager) reserveSession(channelID string, entry db.ScheduleEntry, mediaPath string, profile packageprofile.Profile, targetSegmentMs int64, opts SessionOptions, now int64) (*session, error) {
	m.mu.Lock()
	if existing := m.sessions[channelID][entry.ID]; existing != nil {
		if existing.state != stateFailed && existing.burnSubtitleStreamIndex == opts.BurnSubtitleStreamIndex {
			m.mu.Unlock()
			return nil, nil
		}
		delete(m.sessions[channelID], entry.ID)
		delete(m.byID, existing.id)
		go m.stopAndRemove(existing)
	}
	if m.blacklist[entry.ID] {
		m.mu.Unlock()
		return nil, fmt.Errorf("on-demand session restart budget exhausted for entry %s", entry.ID)
	}
	if m.runningLocked() < m.maxConcurrent {
		s := m.newSessionLocked(channelID, entry, mediaPath, profile, targetSegmentMs, opts, now)
		m.mu.Unlock()
		return s, nil
	}
	victims := m.detachEvictionVictimLocked(channelID, now)
	m.mu.Unlock()
	if len(victims) == 0 {
		return nil, ErrAtCapacity
	}
	for _, s := range victims {
		m.stopAndRemove(s)
	}
	return nil, errAdmissionRetry
}

func (m *Manager) newSessionLocked(channelID string, entry db.ScheduleEntry, mediaPath string, profile packageprofile.Profile, targetSegmentMs int64, opts SessionOptions, now int64) *session {
	m.seq++
	id := fmt.Sprintf("%s-%d", sanitizeID(entry.ID), m.seq)
	dir := filepath.Join(m.root, sanitizeID(channelID), id)
	ctx, cancel := context.WithCancel(context.Background())
	// seekMs is the media position the encode starts from (the playhead) and the
	// first segment's nominal media start. Copy-mode encodes physically start at
	// the source keyframe at or before it (see packager.LiveSessionArgs).
	seekMs := entry.OffsetMs
	if now > entry.StartMs {
		seekMs += now - entry.StartMs
	}
	// baseSeq is the HLS media sequence of this session's first segment. Anchor
	// it to the wall-clock grid position, then advance it past any prior session
	// for this entry so a restart never reuses a number the previous session
	// already served (hls.js rejects that as a media-sequence mismatch).
	baseSeq := entry.StartMs/db.ScheduleGridMs + divRound(seekMs-entry.OffsetMs, db.ScheduleGridMs)
	if hw, ok := m.seqHighWater[entry.ID]; ok && baseSeq <= hw {
		baseSeq = hw + 1
	}
	s := &session{
		id:                      id,
		channelID:               channelID,
		entry:                   entry,
		mediaPath:               mediaPath,
		profile:                 profile,
		targetMs:                targetSegmentMs,
		burnSubtitleStreamIndex: opts.BurnSubtitleStreamIndex,
		dir:                     dir,
		initPath:                filepath.Join(dir, "init.mp4"),
		ctx:                     ctx,
		cancel:                  cancel,
		done:                    make(chan struct{}),
		state:                   stateStarting,
		lastProgressMs:          now,
		processRunning:          true,
		baseMediaStartMs:        seekMs,
		baseSeq:                 baseSeq,
	}
	metrics.OnDemandSessions.WithLabelValues("starting").Inc()
	if m.sessions[channelID] == nil {
		m.sessions[channelID] = make(map[string]*session)
	}
	m.sessions[channelID][entry.ID] = s
	m.byID[id] = s
	return s
}

func (m *Manager) startReserved(ctx context.Context, s *session, now int64) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		m.markFailed(s, fmt.Errorf("create session dir: %w", err), false)
		return err
	}
	// seekMs and baseMediaStartMs were fixed when the session was reserved
	// (newSessionLocked) so they stay consistent with the assigned baseSeq.
	seekMs := s.baseMediaStartMs
	entryEnd := s.entry.OffsetMs + s.entry.DurationMs
	limitMs := entryEnd - seekMs
	if limitMs < 0 {
		limitMs = 0
	}
	s.spawnedAt = m.now()
	var burnSubtitleStreamIndex *int
	if s.burnSubtitleStreamIndex >= 0 {
		v := s.burnSubtitleStreamIndex
		burnSubtitleStreamIndex = &v
	}
	proc, err := m.spawn(s.ctx, packager.LiveSessionSpec{
		MediaPath:               s.mediaPath,
		OutDir:                  s.dir,
		SeekMs:                  seekMs,
		LimitMs:                 limitMs,
		TargetSegmentMs:         s.targetMs,
		BurstSec:                m.burstSec,
		Profile:                 s.profile,
		BurnSubtitleStreamIndex: burnSubtitleStreamIndex,
	})
	if err != nil {
		m.markFailed(s, err, false)
		return err
	}
	m.tailOnce(s)
	go m.tailLoop(s)
	go m.waitLoop(s, proc)
	return nil
}

func (m *Manager) defaultSpawn(ctx context.Context, spec packager.LiveSessionSpec) (Process, error) {
	args, err := packager.LiveSessionArgs(ctx, spec)
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
	// session reports a bare "exit status 1" and the restart reason (stall kill
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

func (m *Manager) waitLoop(s *session, proc Process) {
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
	close(s.done)
}

func (m *Manager) tailLoop(s *session) {
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

func (m *Manager) tailOnce(s *session) {
	playlist := filepath.Join(s.dir, "stream.m3u8")
	parsed, err := packager.ParseHLSManifest(playlist)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.state == stateStarting && len(parsed) > 0 {
		s.state = stateServing
		metrics.OnDemandSessions.WithLabelValues("starting").Dec()
		metrics.OnDemandSessions.WithLabelValues("serving").Inc()
		if s.spawnedAt > 0 {
			metrics.OnDemandSessionSpawnLatency.Observe(float64(m.now()-s.spawnedAt) / 1000)
		}
		metrics.OnDemandSessionSpawnsTotal.Inc()
	}
	if len(parsed) <= s.parsedSegments {
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
			SessionID:    s.id,
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
	if hw := s.baseSeq + int64(s.parsedSegments) - 1; hw > m.seqHighWater[s.entry.ID] {
		m.seqHighWater[s.entry.ID] = hw
	}
}

func (m *Manager) SegmentsFrom(channelID, entryID string, mediaPosMs int64, limit int) []SegmentMeta {
	if limit <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[channelID][entryID]
	if s == nil {
		return nil
	}
	first := -1
	for i, seg := range s.segments {
		if seg.MediaStartMs <= mediaPosMs && seg.MediaStartMs+seg.DurationMs > mediaPosMs {
			first = i
			break
		}
	}
	if first == -1 {
		for i, seg := range s.segments {
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
	if end > len(s.segments) {
		end = len(s.segments)
	}
	out := make([]SegmentMeta, end-first)
	copy(out, s.segments[first:end])
	return out
}

func (m *Manager) InitPath(channelID, sessionID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.byID[sessionID]
	if s == nil || s.channelID != channelID {
		return "", false
	}
	if _, err := os.Stat(s.initPath); err != nil {
		return "", false
	}
	return s.initPath, true
}

func (m *Manager) SegmentPath(channelID, sessionID string, index int64) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.byID[sessionID]
	if s == nil || s.channelID != channelID {
		return "", false
	}
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

func (m *Manager) ExtraDiscontinuities(channelID string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.extraDisc[channelID]
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
	var teardown []*session
	m.mu.Lock()
	for entryID, s := range m.sessions[channelID] {
		delete(m.byID, s.id)
		teardown = append(teardown, s)
		if hw := s.baseSeq + int64(s.parsedSegments) - 1; hw > m.seqHighWater[entryID] {
			m.seqHighWater[entryID] = hw
		}
	}
	if len(teardown) > 0 {
		delete(m.sessions, channelID)
		m.extraDisc[channelID]++
	}
	m.mu.Unlock()
	for _, s := range teardown {
		m.stopAndRemove(s)
	}
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	var all []*session
	for _, byEntry := range m.sessions {
		for _, s := range byEntry {
			all = append(all, s)
		}
	}
	m.sessions = make(map[string]map[string]*session)
	m.byID = make(map[string]*session)
	m.mu.Unlock()
	for _, s := range all {
		m.stopAndRemove(s)
	}
	_ = os.RemoveAll(m.root)
}

func (m *Manager) sweep(now int64) {
	var teardown []*session
	m.mu.Lock()
	for channelID, byEntry := range m.sessions {
		idle := now-m.lastTouch[channelID] > m.graceMs
		for entryID, s := range byEntry {
			m.pruneTrailingLocked(s, now)
			entryOver := now > s.entry.StartMs+s.entry.DurationMs+m.trailingMs
			stalled := s.processRunning && now-s.lastProgressMs > m.stallTimeoutMs
			if stalled {
				m.noteFailureLocked(s, fmt.Errorf("session stalled after %dms (last segment %dms ago)", m.stallTimeoutMs, now-s.lastProgressMs))
			}
			if idle || entryOver || stalled {
				log.Printf("INFO on-demand session teardown channel=%s entry=%s session=%s reason=%s state=%s segments=%d untouched_ms=%d",
					channelID, entryID, s.id, teardownReason(idle, entryOver, stalled), s.state, s.parsedSegments, now-m.lastTouch[channelID])
				delete(byEntry, entryID)
				delete(m.byID, s.id)
				// Once the program is over no further session will serve this
				// entry, so its sequence high-water can be forgotten. Keep it for
				// idle/stall teardown — a re-admitted or restarted session for the
				// same entry must still number past what was already served.
				if entryOver {
					delete(m.seqHighWater, entryID)
				}
				teardown = append(teardown, s)
			}
		}
		if len(byEntry) == 0 {
			delete(m.sessions, channelID)
			delete(m.lastTouch, channelID)
			delete(m.extraDisc, channelID)
		}
	}
	m.mu.Unlock()
	for _, s := range teardown {
		m.stopAndRemove(s)
	}
}

func (m *Manager) pruneTrailingLocked(s *session, now int64) {
	playhead := s.entry.OffsetMs + clamp(now-s.entry.StartMs, 0, s.entry.DurationMs)
	cutoff := playhead - m.trailingMs
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

func (m *Manager) detachEvictionVictimLocked(requestingChannel string, now int64) []*session {
	type candidate struct {
		channelID string
		touched   int64
	}
	var candidates []candidate
	for channelID, touched := range m.lastTouch {
		if channelID == requestingChannel || now-touched < m.evictIdleMs {
			continue
		}
		if len(m.sessions[channelID]) == 0 {
			continue
		}
		candidates = append(candidates, candidate{channelID: channelID, touched: touched})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].touched < candidates[j].touched })
	victim := candidates[0].channelID
	metrics.OnDemandSessionEvictionsTotal.Inc()
	var out []*session
	for entryID, s := range m.sessions[victim] {
		log.Printf("INFO on-demand session evicted channel=%s entry=%s session=%s for_channel=%s untouched_ms=%d",
			victim, entryID, s.id, requestingChannel, now-candidates[0].touched)
		delete(m.byID, s.id)
		delete(m.sessions[victim], entryID)
		out = append(out, s)
	}
	delete(m.sessions, victim)
	delete(m.lastTouch, victim)
	delete(m.extraDisc, victim)
	return out
}

func (m *Manager) runningLocked() int {
	n := 0
	for _, s := range m.byID {
		if s.processRunning && (s.state == stateStarting || s.state == stateServing) {
			n++
		}
	}
	return n
}

func (m *Manager) markFailed(s *session, err error, countRestart bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if countRestart {
		m.noteFailureLocked(s, err)
	} else {
		metrics.OnDemandSessions.WithLabelValues("starting").Dec()
		metrics.OnDemandSessions.WithLabelValues("failed").Inc()
		s.state = stateFailed
		s.processRunning = false
		s.waitErr = err
	}
	close(s.done)
}

func (m *Manager) noteFailureLocked(s *session, err error) {
	if s.failCounted {
		prev := s.state
		s.state = stateFailed
		s.processRunning = false
		s.waitErr = err
		switch prev {
		case stateStarting:
			metrics.OnDemandSessions.WithLabelValues("starting").Dec()
		case stateServing:
			metrics.OnDemandSessions.WithLabelValues("serving").Dec()
		}
		metrics.OnDemandSessions.WithLabelValues("failed").Inc()
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
		m.blacklist[s.entry.ID] = true
	}
	switch prev {
	case stateStarting:
		metrics.OnDemandSessions.WithLabelValues("starting").Dec()
	case stateServing:
		metrics.OnDemandSessions.WithLabelValues("serving").Dec()
	}
	metrics.OnDemandSessions.WithLabelValues("failed").Inc()
	metrics.OnDemandSessionRestartsTotal.Inc()
	log.Printf("WARN on-demand session failed channel=%s entry=%s session=%s: %v", s.channelID, s.entry.ID, s.id, err)
}

func (m *Manager) stopAndRemove(s *session) {
	m.mu.Lock()
	prevState := s.state
	if s.state != stateFailed {
		s.state = stateStopping
	}
	s.processRunning = false
	m.mu.Unlock()
	switch prevState {
	case stateStarting:
		metrics.OnDemandSessions.WithLabelValues("starting").Dec()
	case stateServing:
		metrics.OnDemandSessions.WithLabelValues("serving").Dec()
	case stateFailed:
		metrics.OnDemandSessions.WithLabelValues("failed").Dec()
	}
	s.cancel()
	<-s.done
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

// divRound divides v by denom, rounding to the nearest integer (ties away from
// zero). It mirrors the manifest layer's grid bucketing so a session's anchor
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
		return "session"
	}
	return b.String()
}

var _ Process = (*exec.Cmd)(nil)
