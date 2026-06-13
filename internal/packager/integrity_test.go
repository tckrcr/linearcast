package packager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestCheckReadyPackageIntegrityResetsMissingFiles(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()

	root := t.TempDir()
	goodRoot := filepath.Join(root, "good")
	badRoot := filepath.Join(root, "bad")
	if err := os.MkdirAll(goodRoot, 0o755); err != nil {
		t.Fatalf("mkdir good: %v", err)
	}
	if err := os.MkdirAll(badRoot, 0o755); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}
	writePackageFiles(t, goodRoot, true)
	writePackageFiles(t, badRoot, false)

	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('good', '/tmp/good.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('bad', '/tmp/bad.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	pkgDur := int64(12000)
	for _, pkg := range []db.MediaPackage{
		{
			ID:                 "pkg-good",
			MediaID:            "good",
			RenditionProfile:   db.DefaultPackageProfile,
			Status:             db.PackageStatusReady,
			PackageRoot:        &goodRoot,
			InitSegmentPath:    func() *string { s := filepath.Join(goodRoot, "init.mp4"); return &s }(),
			PackagedDurationMs: &pkgDur,
			CreatedAtMs:        1,
			UpdatedAtMs:        1,
		},
		{
			ID:                 "pkg-bad",
			MediaID:            "bad",
			RenditionProfile:   db.DefaultPackageProfile,
			Status:             db.PackageStatusReady,
			PackageRoot:        &badRoot,
			InitSegmentPath:    func() *string { s := filepath.Join(badRoot, "init.mp4"); return &s }(),
			PackagedDurationMs: &pkgDur,
			CreatedAtMs:        1,
			UpdatedAtMs:        1,
		},
	} {
		if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
		if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
			{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr(filepath.Join(*pkg.PackageRoot, "seg0.m4s"))},
			{PackageID: pkg.ID, SegmentNumber: 1, MediaStartMs: 6000, DurationMs: 6000, Path: strptr(filepath.Join(*pkg.PackageRoot, "seg1.m4s"))},
		}); err != nil {
			t.Fatalf("insert segments: %v", err)
		}
	}

	res, err := CheckReadyPackageIntegrity(context.Background(), conn)
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	if res.FileReset != 1 {
		t.Fatalf("file reset count=%d, want 1", res.FileReset)
	}
	if res.DurationReset != 0 {
		t.Fatalf("duration reset count=%d, want 0", res.DurationReset)
	}

	good, err := db.MediaPackageByID(context.Background(), conn, "pkg-good")
	if err != nil || good == nil {
		t.Fatalf("lookup good: pkg=%v err=%v", good, err)
	}
	if good.Status != db.PackageStatusReady {
		t.Fatalf("good package status=%s, want ready", good.Status)
	}
	bad, err := db.MediaPackageByID(context.Background(), conn, "pkg-bad")
	if err != nil || bad == nil {
		t.Fatalf("lookup bad: pkg=%v err=%v", bad, err)
	}
	if bad.Status != db.PackageStatusPending || bad.Error == nil || bad.PackagedDurationMs != nil {
		t.Fatalf("bad package after reset=%+v, want pending with reason and no duration", bad)
	}
	segs, err := db.PackagedSegments(context.Background(), conn, "pkg-bad")
	if err != nil {
		t.Fatalf("bad segments: %v", err)
	}
	if len(segs) != 0 {
		t.Fatalf("bad segments should be cleared, got %+v", segs)
	}
}

func TestCheckReadyPackageIntegrityRequeuesTruncatedDurations(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()

	// Three ready packages with all files present, so only the duration check
	// can fault them: one full-length (stays ready), one truncated by minutes
	// (requeued), one with an unknown source duration (skipped, left ready).
	root := t.TempDir()
	cases := []struct {
		mediaID     string
		sourceMs    int64
		packagedMs  int64
		packagedNil bool // legacy ready row with no recorded packaged duration
	}{
		{mediaID: "full", sourceMs: 600000, packagedMs: 600000},  // within tolerance
		{mediaID: "trunc", sourceMs: 600000, packagedMs: 450000}, // 150s short, far beyond max(2s, 0.5%)
		{mediaID: "nodur", sourceMs: 600000, packagedNil: true},  // unverifiable: no packaged duration
	}
	for _, c := range cases {
		pkgRoot := filepath.Join(root, c.mediaID)
		if err := os.MkdirAll(pkgRoot, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", c.mediaID, err)
		}
		writePackageFiles(t, pkgRoot, true)
		if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
			video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
			VALUES (?, ?, '/tmp', ?, 'mkv', 'h264', 1080, 'aac', 1, 0)`,
			c.mediaID, "/tmp/"+c.mediaID+".mkv", c.sourceMs); err != nil {
			t.Fatalf("insert media %s: %v", c.mediaID, err)
		}
		var pkgDur *int64
		if !c.packagedNil {
			d := c.packagedMs
			pkgDur = &d
		}
		pkgID := "pkg-" + c.mediaID
		if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
			ID:                 pkgID,
			MediaID:            c.mediaID,
			RenditionProfile:   db.DefaultPackageProfile,
			Status:             db.PackageStatusReady,
			PackageRoot:        &pkgRoot,
			InitSegmentPath:    func() *string { s := filepath.Join(pkgRoot, "init.mp4"); return &s }(),
			PackagedDurationMs: pkgDur,
			CreatedAtMs:        1,
			UpdatedAtMs:        1,
		}); err != nil {
			t.Fatalf("upsert package %s: %v", pkgID, err)
		}
		if err := db.ReplacePackagedSegments(context.Background(), conn, pkgID, []db.PackagedSegment{
			{PackageID: pkgID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr(filepath.Join(pkgRoot, "seg0.m4s"))},
			{PackageID: pkgID, SegmentNumber: 1, MediaStartMs: 6000, DurationMs: 6000, Path: strptr(filepath.Join(pkgRoot, "seg1.m4s"))},
		}); err != nil {
			t.Fatalf("insert segments %s: %v", pkgID, err)
		}
	}

	// Read-only audit (the maint command's data source) surfaces the truncated
	// and unknown packages, but not the full-length one.
	offenders, err := AuditReadyPackageDurations(context.Background(), conn)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	byMedia := map[string]DurationShortfall{}
	for _, o := range offenders {
		byMedia[o.MediaID] = o
	}
	if _, ok := byMedia["full"]; ok {
		t.Fatalf("full-length package should not be flagged: %+v", byMedia["full"])
	}
	if tr, ok := byMedia["trunc"]; !ok || tr.UnknownSource || tr.ShortfallMs != 150000 {
		t.Fatalf("trunc audit row=%+v ok=%v, want flagged shortfall=150000", tr, ok)
	}
	if nd, ok := byMedia["nodur"]; !ok || !nd.UnknownSource {
		t.Fatalf("nodur audit row=%+v ok=%v, want UnknownSource", nd, ok)
	}

	// The sweep requeues only the truncated package; full and unknown stay ready.
	res, err := CheckReadyPackageIntegrity(context.Background(), conn)
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	if res.FileReset != 0 || res.DurationReset != 1 || res.DurationSkipped != 1 {
		t.Fatalf("result=%+v, want FileReset=0 DurationReset=1 DurationSkipped=1", res)
	}

	full, _ := db.MediaPackageByID(context.Background(), conn, "pkg-full")
	if full == nil || full.Status != db.PackageStatusReady {
		t.Fatalf("full package status=%v, want ready", full)
	}
	nodur, _ := db.MediaPackageByID(context.Background(), conn, "pkg-nodur")
	if nodur == nil || nodur.Status != db.PackageStatusReady {
		t.Fatalf("nodur package status=%v, want ready (skipped)", nodur)
	}
	trunc, _ := db.MediaPackageByID(context.Background(), conn, "pkg-trunc")
	if trunc == nil || trunc.Status != db.PackageStatusPending || trunc.Error == nil || trunc.PackagedDurationMs != nil {
		t.Fatalf("trunc package after sweep=%+v, want pending with reason and cleared duration", trunc)
	}
	segs, err := db.PackagedSegments(context.Background(), conn, "pkg-trunc")
	if err != nil {
		t.Fatalf("trunc segments: %v", err)
	}
	if len(segs) != 0 {
		t.Fatalf("trunc segments should be cleared, got %+v", segs)
	}
}

func TestInspectMediaPackages(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()

	root := t.TempDir()
	goodRoot := filepath.Join(root, "good")
	missRoot := filepath.Join(root, "miss")
	if err := os.MkdirAll(goodRoot, 0o755); err != nil {
		t.Fatalf("mkdir good: %v", err)
	}
	if err := os.MkdirAll(missRoot, 0o755); err != nil {
		t.Fatalf("mkdir miss: %v", err)
	}
	writePackageFiles(t, goodRoot, true)  // every segment present
	writePackageFiles(t, missRoot, false) // seg1.m4s absent

	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('good', '/tmp/good.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('miss', '/tmp/miss.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('pend', '/tmp/pend.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	pkgDur := int64(12000)
	for _, pkg := range []db.MediaPackage{
		{ID: "pkg-good", MediaID: "good", RenditionProfile: db.DefaultPackageProfile, Status: db.PackageStatusReady, PackageRoot: &goodRoot, InitSegmentPath: strptr(filepath.Join(goodRoot, "init.mp4")), PackagedDurationMs: &pkgDur, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-miss", MediaID: "miss", RenditionProfile: db.DefaultPackageProfile, Status: db.PackageStatusReady, PackageRoot: &missRoot, InitSegmentPath: strptr(filepath.Join(missRoot, "init.mp4")), PackagedDurationMs: &pkgDur, CreatedAtMs: 1, UpdatedAtMs: 1},
		// Non-ready package: listed for a single-media query but skipped (Checked=false) and absent from the sweep.
		{ID: "pkg-pend", MediaID: "pend", RenditionProfile: db.DefaultPackageProfile, Status: db.PackageStatusPending, CreatedAtMs: 1, UpdatedAtMs: 1},
	} {
		if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
			t.Fatalf("upsert %s: %v", pkg.ID, err)
		}
	}

	// Single-media: the intact ladder reports OK with both segments present.
	good, err := InspectMediaPackages(context.Background(), conn, "good")
	if err != nil {
		t.Fatalf("inspect good: %v", err)
	}
	if len(good) != 1 || !good[0].OK || !good[0].InitPresent || good[0].SegmentCount != 2 || len(good[0].MissingSegments) != 0 {
		t.Fatalf("good inspection=%+v, want 1 OK package, init present, 2 segments, none missing", good)
	}

	// Single-media: the package missing seg1.m4s is flagged with that exact URI.
	miss, err := InspectMediaPackages(context.Background(), conn, "miss")
	if err != nil {
		t.Fatalf("inspect miss: %v", err)
	}
	if len(miss) != 1 || miss[0].OK || miss[0].SegmentCount != 2 ||
		len(miss[0].MissingSegments) != 1 || miss[0].MissingSegments[0] != "seg1.m4s" {
		t.Fatalf("miss inspection=%+v, want 1 not-OK package missing seg1.m4s", miss)
	}
	if !miss[0].InitPresent || !miss[0].ManifestPresent {
		t.Fatalf("miss inspection=%+v, want init+manifest present", miss[0])
	}

	// Single-media: a non-ready package is listed but not file-checked.
	pend, err := InspectMediaPackages(context.Background(), conn, "pend")
	if err != nil {
		t.Fatalf("inspect pend: %v", err)
	}
	if len(pend) != 1 || pend[0].Checked || pend[0].Status != string(db.PackageStatusPending) {
		t.Fatalf("pend inspection=%+v, want 1 unchecked pending package", pend)
	}

	// Sweep: only ready packages, so the pending one is excluded.
	all, err := InspectMediaPackages(context.Background(), conn, "")
	if err != nil {
		t.Fatalf("inspect all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("sweep len=%d, want 2 ready packages", len(all))
	}
}

func writePackageFiles(t *testing.T, root string, includeAllSegments bool) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "init.mp4"), []byte("init"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	manifest := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6.000,\nseg0.m4s\n#EXTINF:6.000,\nseg1.m4s\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(filepath.Join(root, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "seg0.m4s"), []byte("seg0"), 0o644); err != nil {
		t.Fatalf("write seg0: %v", err)
	}
	if includeAllSegments {
		if err := os.WriteFile(filepath.Join(root, "seg1.m4s"), []byte("seg1"), 0o644); err != nil {
			t.Fatalf("write seg1: %v", err)
		}
	}
}

func strptr(s string) *string { return &s }
