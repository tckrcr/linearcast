package packager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/metrics"
	"github.com/tckrcr/linearcast/internal/packageid"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

// Worker keeps package coverage ahead of schedule for every enabled packaged
// channel. It claims jobs by atomically transitioning a media_packages row to
// 'processing' (or inserting one if missing), then drives PackageOne. Multiple
// goroutines can run concurrently; SQLite serializes claims.
type Worker struct {
	DB         *sql.DB
	OutputRoot string
	// WorkDir is a scratch directory on the same filesystem as OutputRoot.
	// Each job encodes here and the result is renamed into OutputRoot on success,
	// so failed encodes leave no debris beside finished packages. Swept clean on
	// startup. Leave empty to encode directly into OutputRoot (legacy).
	WorkDir           string
	PollInterval      time.Duration
	Concurrency       int
	TargetSegmentMs   int64
	Preset            string
	StaleAfter        time.Duration
	IntegrityInterval time.Duration
	MaxAttempts       int
}

// default worker tunables. These used to be env vars; they are now baked-in
// constants because changing them independently would break cross-component
// assumptions (e.g. target segment duration must match the scheduler's 6s grid).
const (
	defaultPollInterval      = 5 * time.Second
	defaultPreset            = "veryfast"
	defaultStaleAfter        = 60 * time.Minute
	defaultIntegrityInterval = 240 * time.Minute
	defaultMaxAttempts       = 5
)

// Validate checks that all required Worker fields are set and applies defaults
// for optional tunables. Call this before Run in tests or when constructing a
// Worker manually.
func (w *Worker) Validate() error {
	if w.DB == nil {
		return errors.New("Worker.DB is required")
	}
	if w.OutputRoot == "" {
		return errors.New("Worker.OutputRoot is required")
	}
	if w.PollInterval <= 0 {
		w.PollInterval = defaultPollInterval
	}
	if w.Concurrency < 0 {
		w.Concurrency = 0
	}
	if w.TargetSegmentMs <= 0 {
		w.TargetSegmentMs = scheduler.TargetSegmentMs
	}
	if w.Preset == "" {
		w.Preset = defaultPreset
	}
	if w.StaleAfter <= 0 {
		w.StaleAfter = defaultStaleAfter
	}
	if w.IntegrityInterval <= 0 {
		w.IntegrityInterval = defaultIntegrityInterval
	}
	if w.MaxAttempts < 0 {
		w.MaxAttempts = defaultMaxAttempts
	}
	return nil
}

// Run starts Concurrency loops and blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	if err := w.Validate(); err != nil {
		log.Fatalf("worker validation failed: %v", err)
	}

	if err := w.recoverStale(ctx); err != nil {
		log.Printf("WARN stale recovery: %v", err)
	}
	// Sweep after recoverStale: any dirs left here are from a previous process
	// run that died mid-encode. Those rows are now back to pending/failed.
	w.sweepWorkDir()
	w.runIntegrityCheck(ctx, "startup")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.integrityLoop(ctx)
	}()
	// Goroutines are spawned up to the number of logical CPUs. The actual
	// concurrency cap is enforced inside ClaimPackage via the DB-stored
	// encoders.concurrency value, which can be changed at runtime without
	// restarting the worker.
	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w.loop(ctx, idx)
		}(i)
	}
	wg.Wait()
}

func (w *Worker) integrityLoop(ctx context.Context) {
	timer := time.NewTimer(w.IntegrityInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			w.runIntegrityCheck(ctx, "periodic")
			timer.Reset(w.IntegrityInterval)
		}
	}
}

func (w *Worker) runIntegrityCheck(ctx context.Context, source string) {
	n, err := CheckReadyPackageIntegrity(ctx, w.DB)
	if err != nil {
		log.Printf("WARN package integrity check source=%s: %v", source, err)
		return
	}
	if n > 0 {
		metrics.PackageRepairRequeuesTotal.WithLabelValues("integrity_" + source).Add(float64(n))
		log.Printf("package integrity reset rows count=%d source=%s", n, source)
	}
}

func (w *Worker) loop(ctx context.Context, idx int) {
	for {
		if ctx.Err() != nil {
			return
		}
		w.recordQueueDepth()
		job, err := w.claimNext(ctx)
		if err != nil {
			log.Printf("worker=%d ERROR claim: %v", idx, err)
		} else if job != nil {
			w.runOne(ctx, idx, *job)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.PollInterval):
		}
	}
}

func (w *Worker) recordQueueDepth() {
	rows, err := db.PackageProfileSummaries(context.Background(), w.DB)
	if err != nil {
		log.Printf("WARN package queue metrics: %v", err)
		return
	}
	for _, row := range rows {
		metrics.PackageQueueDepth.WithLabelValues(row.RenditionProfile, metrics.PackageStatusLabel(row.Status)).Set(float64(row.PackageCount))
	}
}

type claimedJob struct {
	Media   db.Media
	Profile string
}

// claimNext walks discovery candidates in priority order and returns the
// first one this worker successfully transitions to 'processing'. Returns
// (nil, nil) when nothing is claimable. Per-candidate errors are logged and
// skipped — one bad row (e.g. transient claim conflict) should not stop the
// loop from finding a different row to work on.
func (w *Worker) claimNext(ctx context.Context) (*claimedJob, error) {
	cands, err := DiscoverCandidates(ctx, w.DB)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	if len(cands) == 0 {
		return nil, nil
	}
	nowMs := time.Now().UTC().UnixMilli()
	for _, c := range cands {
		ok, err := w.tryClaim(ctx, c.MediaID, c.Profile, nowMs)
		if err != nil {
			log.Printf("WARN claim error media=%s profile=%s: %v", c.MediaID, c.Profile, err)
			continue
		}
		if !ok {
			continue
		}
		m, err := db.MediaByID(ctx, w.DB, c.MediaID)
		if err != nil {
			log.Printf("WARN load claimed media id=%s: %v", c.MediaID, err)
			continue
		}
		if m == nil {
			log.Printf("WARN claimed missing media id=%s; releasing back to failed", c.MediaID)
			_ = w.markFailed(ctx, c.MediaID, c.Profile, errors.New("media row vanished after claim"))
			continue
		}
		return &claimedJob{Media: *m, Profile: c.Profile}, nil
	}
	return nil, nil
}

// tryClaim atomically inserts a new processing row OR transitions an existing
// pending/failed row to processing. Returns true if this caller won the claim.
// 'ready' and other 'processing' rows are left alone. EncoderID is empty for
// the local worker: leases are remote-only in v1, so this worker's "lease" is
// still time-based on media_packages.updated_at_ms via recoverStale.
// remote_only channel policy blocks this claim — by design.
func (w *Worker) tryClaim(ctx context.Context, mediaID, profile string, nowMs int64) (bool, error) {
	return db.ClaimPackage(ctx, w.DB, db.ClaimRequest{
		MediaID:   mediaID,
		Profile:   profile,
		PackageID: packageid.For(mediaID, profile),
		NowMs:     nowMs,
	})
}

func (w *Worker) markFailed(ctx context.Context, mediaID, profile string, cause error) error {
	nowMs := time.Now().UTC().UnixMilli()
	packageID := packageid.For(mediaID, profile)
	reason := ""
	if cause != nil {
		reason = cause.Error()
	}
	_, err := db.MarkPackageFailedWithKind(ctx, w.DB, packageID, "transient", reason, w.MaxAttempts, nowMs)
	return err
}

func (w *Worker) recoverStale(ctx context.Context) error {
	cutoffMs := time.Now().UTC().Add(-w.StaleAfter).UnixMilli()
	nowMs := time.Now().UTC().UnixMilli()
	// A process crash can strand rows in processing. Using transient semantics
	// lets the normal claim path retry them up to MaxAttempts.
	n, err := db.FailStaleProcessingPackages(ctx, w.DB, cutoffMs, nowMs, w.MaxAttempts, "stale processing reset on worker startup")
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("recovered stale processing rows count=%d cutoff_ms=%d", n, cutoffMs)
	}
	return nil
}

func (w *Worker) runOne(ctx context.Context, idx int, job claimedJob) {
	started := time.Now()
	log.Printf("worker=%d packaging media=%s profile=%s path=%s", idx, job.Media.ID, job.Profile, job.Media.Path)
	res, err := PackageOne(ctx, w.DB, Options{
		MediaPath:       job.Media.Path,
		Profile:         job.Profile,
		OutputRoot:      w.OutputRoot,
		WorkDir:         w.WorkDir,
		TargetSegmentMs: w.TargetSegmentMs,
		Preset:          w.Preset,
		FailKind:        "transient",
		MaxAttempts:     w.MaxAttempts,
	})
	if err != nil {
		metrics.PackageJobDuration.WithLabelValues(job.Profile, metrics.PackageResultLabel(err)).Observe(time.Since(started).Seconds())
		log.Printf("worker=%d FAILED media=%s profile=%s elapsed=%s err=%v",
			idx, job.Media.ID, job.Profile, time.Since(started).Round(time.Millisecond), err)
		return
	}
	metrics.PackageJobDuration.WithLabelValues(job.Profile, "ready").Observe(time.Since(started).Seconds())
	log.Printf("worker=%d ready media=%s profile=%s segments=%d duration_ms=%d elapsed=%s",
		idx, res.MediaID, res.RenditionProfile, res.SegmentCount, res.DurationMs,
		time.Since(started).Round(time.Millisecond))
}

// Candidate is one (media, rendition profile) pair returned by DiscoverCandidates
// as a candidate to claim. Both local and remote encoders consume the same
// discovery output so they pick up the same channel-demand-driven backlog.
type Candidate struct {
	MediaID string
	Profile string
}

// DiscoverCandidates returns (media_id, rendition_profile) pairs that need
// packaging. Enabled channel demand is first, ordered by earliest channel
// position. Operator-requested orphan package rows drain after channel demand.
// Failed rows require an explicit operator retry; automatic rediscovery would
// turn durable failures like missing source files into tight retry loops.
func DiscoverCandidates(ctx context.Context, conn *sql.DB) ([]Candidate, error) {
	activeProfiles, err := db.AllPackageProfileNames(ctx, conn)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(activeProfiles))
	profileKinds := make(map[string]packageprofile.MediaKind, len(activeProfiles))
	for _, profile := range activeProfiles {
		active[profile] = true
		profileKinds[profile] = packageprofile.MediaKindVideo
		if p, err := db.GetPackageProfile(ctx, conn, profile); err == nil && p != nil {
			profileKinds[profile] = packageprofile.NormalizeMediaKind(p.MediaKind)
		}
	}
	rows, err := conn.QueryContext(ctx, `
WITH RECURSIVE chain(channel_id, media_id, pos) AS (
    SELECT channel_id, media_id, 0
    FROM channel_media
    WHERE anchor_media_id IS NULL
    UNION ALL
    SELECT cm.channel_id, cm.media_id, chain.pos + 1
    FROM channel_media cm
    JOIN chain ON cm.channel_id = chain.channel_id
              AND cm.anchor_media_id = chain.media_id
),
needed AS (
    SELECT cm.media_id,
           COALESCE(NULLIF(TRIM(c.required_package_profile), ''), ?) AS rendition_profile,
           c.media_kind,
           MIN(cm.channel_id || char(31) || printf('%010d', chain.pos)) AS first_position
    FROM channel_media cm
    JOIN channels c ON c.id = cm.channel_id
    JOIN chain ON chain.channel_id = cm.channel_id AND chain.media_id = cm.media_id
    WHERE c.enabled = 1
      AND c.upstream_hls_url IS NULL
    GROUP BY cm.media_id, COALESCE(NULLIF(TRIM(c.required_package_profile), ''), ?), c.media_kind
)
SELECT media_id, rendition_profile, media_kind
FROM (
    SELECT n.media_id, n.rendition_profile, COALESCE(m.media_kind, 'video') AS media_kind,
           0 AS priority, n.first_position AS sort_key
    FROM needed n
    JOIN media m ON m.id = n.media_id
    LEFT JOIN media_packages p
           ON p.media_id = n.media_id
          AND p.rendition_profile = n.rendition_profile
    WHERE m.codec_check_passed = 1
      AND COALESCE(m.media_kind, 'video') = n.media_kind
      AND (p.status IS NULL OR p.status = 'pending')

    UNION ALL

    SELECT p.media_id, p.rendition_profile, COALESCE(m.media_kind, 'video') AS media_kind,
           1 AS priority, printf('%020d', p.updated_at_ms) || char(31) || p.media_id AS sort_key
    FROM media_packages p
    JOIN media m ON m.id = p.media_id
    WHERE m.codec_check_passed = 1
      AND p.status = 'pending'
      AND NOT EXISTS (
          SELECT 1
          FROM needed n
          WHERE n.media_id = p.media_id
            AND n.rendition_profile = p.rendition_profile
      )
)
ORDER BY priority, sort_key`,
		db.DefaultPackageProfile, db.DefaultPackageProfile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		var mediaKind string
		if err := rows.Scan(&c.MediaID, &c.Profile, &mediaKind); err != nil {
			return nil, err
		}
		if !active[c.Profile] {
			continue
		}
		if profileKinds[c.Profile] != packageprofile.NormalizeMediaKind(packageprofile.MediaKind(mediaKind)) {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// sweepWorkDir removes all subdirectories under WorkDir. Called on startup
// after recoverStale has moved any stranded processing rows back to
// pending/failed, so nothing in the work dir belongs to an active encode.
func (w *Worker) sweepWorkDir() {
	if w.WorkDir == "" {
		return
	}
	entries, err := os.ReadDir(w.WorkDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("WARN sweep work_dir=%s: %v", w.WorkDir, err)
		}
		return
	}
	var removed int
	for _, entry := range entries {
		path := w.WorkDir + "/" + entry.Name()
		if err := os.RemoveAll(path); err != nil {
			log.Printf("WARN sweep work_dir remove %s: %v", path, err)
			continue
		}
		removed++
	}
	if removed > 0 {
		log.Printf("sweep work_dir=%s removed=%d", w.WorkDir, removed)
	}
}
