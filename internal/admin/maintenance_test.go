package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleMaintenanceMissingMedia(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	presentPath := filepath.Join(dir, "present.mkv")
	missingPath := filepath.Join(dir, "missing.mkv")
	if err := os.WriteFile(presentPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write present: %v", err)
	}

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	mustExec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
		playback_mode, required_package_profile, hidden_from_guide)
		VALUES ('ch', 'Ch', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p', 0)`)
	mustExec(`INSERT INTO media (id, path, directory, title, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('present', ?, ?, 'Present', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`,
		presentPath, dir)
	mustExec(`INSERT INTO media (id, path, directory, title, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('gone', ?, ?, 'Gone', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`,
		missingPath, dir)
	mustExec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch', 'gone', NULL, 0)`)
	mustExec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('sched-1', 'ch', 0, 'gone', 0, 18000, 0)`)
	mustExec(`INSERT INTO play_history (channel_id, schedule_entry_id, media_id, started_at, ended_at, duration_ms)
		VALUES ('ch', 'sched-1', 'gone', 0, 18000, 18000)`)
	mustExec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('pkg-gone', 'gone', 'h264-main-1080p', 'ready', 0, 0)`)

	app := New(Config{
		DB:  conn,
		Now: func() time.Time { return time.UnixMilli(0).UTC() },
	})

	// Dry run: should report 'gone' but not delete it.
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/maintenance/missing-media?dry-run=true", nil)
	res := httptest.NewRecorder()
	app.handleMaintenanceMissingMedia(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("dry-run status=%d body=%s", res.Code, res.Body.String())
	}
	var dry missingMediaResponse
	if err := json.NewDecoder(res.Body).Decode(&dry); err != nil {
		t.Fatalf("decode dry-run: %v", err)
	}
	if !dry.DryRun {
		t.Errorf("expected dryRun=true")
	}
	if dry.Checked != 2 {
		t.Errorf("checked=%d, want 2", dry.Checked)
	}
	if len(dry.Missing) != 1 || dry.Missing[0].ID != "gone" {
		t.Errorf("missing=%+v, want one entry for 'gone'", dry.Missing)
	}
	if dry.Deleted != 0 {
		t.Errorf("dry-run deleted=%d, want 0", dry.Deleted)
	}
	// Row should still be there after dry run.
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM media WHERE id = 'gone'`).Scan(&n); err != nil {
		t.Fatalf("count after dry-run: %v", err)
	}
	if n != 1 {
		t.Errorf("after dry-run media 'gone' count=%d, want 1", n)
	}

	// Real run: should delete.
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/maintenance/missing-media?dry-run=false", nil)
	res = httptest.NewRecorder()
	app.handleMaintenanceMissingMedia(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("real-run status=%d body=%s", res.Code, res.Body.String())
	}
	var run missingMediaResponse
	if err := json.NewDecoder(res.Body).Decode(&run); err != nil {
		t.Fatalf("decode real-run: %v", err)
	}
	if run.DryRun {
		t.Errorf("expected dryRun=false")
	}
	if run.Deleted != 1 {
		t.Errorf("deleted=%d, want 1", run.Deleted)
	}

	// Verify cascade and the explicit cleanups landed.
	checks := map[string]string{
		"media (gone)":            `SELECT COUNT(*) FROM media WHERE id = 'gone'`,
		"channel_media (gone)":    `SELECT COUNT(*) FROM channel_media WHERE media_id = 'gone'`,
		"schedule_entries (gone)": `SELECT COUNT(*) FROM schedule_entries WHERE media_id = 'gone'`,
		"play_history (gone)":     `SELECT COUNT(*) FROM play_history WHERE media_id = 'gone'`,
		"media_packages (gone)":   `SELECT COUNT(*) FROM media_packages WHERE media_id = 'gone'`,
	}
	for label, q := range checks {
		var c int
		if err := conn.QueryRow(q).Scan(&c); err != nil {
			t.Fatalf("count %s: %v", label, err)
		}
		if c != 0 {
			t.Errorf("after clean %s count=%d, want 0", label, c)
		}
	}

	// And the present row should be untouched.
	if err := conn.QueryRow(`SELECT COUNT(*) FROM media WHERE id = 'present'`).Scan(&n); err != nil {
		t.Fatalf("count present: %v", err)
	}
	if n != 1 {
		t.Errorf("present media missing after clean: count=%d", n)
	}
}

func TestHandleMaintenanceOrphanPackages(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	pkgRoot := filepath.Join(cacheDir, "packages")
	dbPath := filepath.Join(dir, "linearcast.db")

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	// Layout:
	//   packages/keep/h264-main-1080p          → in DB and referenced by a schedule entry (must remain)
	//   packages/orphan-row/h264-main-1080p    → in DB but no schedule reference (clean up DB+disk)
	//   packages/never-in-db/h264-main-1080p   → only on disk, no DB row (orphan dir)
	//   packages/other-leftover                → empty dir, no DB row (should be pruned)
	makeDir := func(rel string) string {
		full := filepath.Join(pkgRoot, rel)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(filepath.Join(full, "init.mp4"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write init: %v", err)
		}
		return filepath.Clean(full)
	}
	keepRoot := makeDir("keep/h264-main-1080p")
	orphanRowRoot := makeDir("orphan-row/h264-main-1080p")
	orphanDiskRoot := makeDir("never-in-db/h264-main-1080p")
	if err := os.MkdirAll(filepath.Join(pkgRoot, "other-leftover"), 0o755); err != nil {
		t.Fatalf("mkdir leftover: %v", err)
	}

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	mustExec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
		playback_mode, required_package_profile, hidden_from_guide)
		VALUES ('ch', 'Ch', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p', 0)`)
	mustExec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('keep', '/tmp/keep.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('orphan-row', '/tmp/orphan.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`)
	mustExec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, package_root, created_at_ms, updated_at_ms)
		VALUES ('pkg-keep', 'keep', 'h264-main-1080p', 'ready', ?, 0, 0),
		       ('pkg-orphan-row', 'orphan-row', 'h264-main-1080p', 'ready', ?, 0, 0)`,
		keepRoot, orphanRowRoot)
	mustExec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('sched-1', 'ch', 0, 'keep', 0, 18000, 0)`)

	app := New(Config{
		DB:       conn,
		CacheDir: cacheDir,
		Now:      func() time.Time { return time.UnixMilli(0).UTC() },
	})

	// Dry run.
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/maintenance/orphan-packages?dry-run=true", nil)
	res := httptest.NewRecorder()
	app.handleMaintenanceOrphanPackages(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("dry-run status=%d body=%s", res.Code, res.Body.String())
	}
	var dry orphanPackagesResponse
	if err := json.NewDecoder(res.Body).Decode(&dry); err != nil {
		t.Fatalf("decode dry-run: %v", err)
	}
	if !dry.DryRun {
		t.Errorf("expected dryRun=true")
	}
	if len(dry.Unreferenced) != 1 || dry.Unreferenced[0].ID != "pkg-orphan-row" {
		t.Errorf("unreferenced=%+v, want one entry pkg-orphan-row", dry.Unreferenced)
	}
	// One real orphan dir; the empty 'other-leftover' has no profile subdir so it isn't listed.
	if len(dry.OrphanDirs) != 1 || dry.OrphanDirs[0].Path != orphanDiskRoot {
		t.Errorf("orphanDirs=%+v, want one entry %s", dry.OrphanDirs, orphanDiskRoot)
	}
	if dry.DeletedRows != 0 || dry.DeletedDirs != 0 {
		t.Errorf("dry-run modified state: rows=%d dirs=%d", dry.DeletedRows, dry.DeletedDirs)
	}
	// Disk state should still be intact.
	if _, err := os.Stat(orphanRowRoot); err != nil {
		t.Errorf("dry-run removed pkg-orphan-row dir: %v", err)
	}
	if _, err := os.Stat(orphanDiskRoot); err != nil {
		t.Errorf("dry-run removed orphan-disk dir: %v", err)
	}

	// Real run.
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/maintenance/orphan-packages?dry-run=false", nil)
	res = httptest.NewRecorder()
	app.handleMaintenanceOrphanPackages(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("real-run status=%d body=%s", res.Code, res.Body.String())
	}
	var run orphanPackagesResponse
	if err := json.NewDecoder(res.Body).Decode(&run); err != nil {
		t.Fatalf("decode real-run: %v", err)
	}
	if run.DeletedRows != 1 {
		t.Errorf("deletedRows=%d, want 1", run.DeletedRows)
	}
	if run.DeletedDirs != 1 {
		t.Errorf("deletedDirs=%d, want 1", run.DeletedDirs)
	}

	// DB row gone.
	var c int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM media_packages WHERE id = 'pkg-orphan-row'`).Scan(&c); err != nil {
		t.Fatalf("count pkg-orphan-row: %v", err)
	}
	if c != 0 {
		t.Errorf("pkg-orphan-row still in DB: count=%d", c)
	}
	// Disk dirs gone.
	if _, err := os.Stat(orphanRowRoot); !os.IsNotExist(err) {
		t.Errorf("pkg-orphan-row dir still exists: err=%v", err)
	}
	if _, err := os.Stat(orphanDiskRoot); !os.IsNotExist(err) {
		t.Errorf("never-in-db dir still exists: err=%v", err)
	}
	// Keep dir still there.
	if _, err := os.Stat(keepRoot); err != nil {
		t.Errorf("keep dir removed: %v", err)
	}
	// Empty 'other-leftover' should have been pruned.
	if _, err := os.Stat(filepath.Join(pkgRoot, "other-leftover")); !os.IsNotExist(err) {
		t.Errorf("empty leftover dir still exists: err=%v", err)
	}
}

// TestHandleMaintenanceOrphanPackagesPreservesExtraFiles verifies that files
// not matching known generated patterns (init.mp4, stream.m3u8, seg*.m4s) are
// never removed, and that the package directory itself survives when non-generated
// files remain in it.
func TestHandleMaintenanceOrphanPackagesPreservesExtraFiles(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	pkgRoot := filepath.Join(cacheDir, "packages")
	dbPath := filepath.Join(dir, "linearcast.db")

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	// Package dir that has generated files AND an unrecognised file.
	pkgDir := filepath.Join(pkgRoot, "media1", "h264-main-1080p")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	extraFile := filepath.Join(pkgDir, "source.mkv")
	for _, f := range []string{filepath.Join(pkgDir, "init.mp4"), filepath.Join(pkgDir, "stream.m3u8"), filepath.Join(pkgDir, "seg001.m4s"), extraFile} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	pkgDir = filepath.Clean(pkgDir)

	// DB row exists but is unreferenced (no schedule entry).
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, package_root, created_at_ms, updated_at_ms)
		VALUES ('pkg1', 'm1', 'h264-main-1080p', 'ready', ?, 0, 0)`, pkgDir); err != nil {
		t.Fatalf("insert package: %v", err)
	}

	app := New(Config{
		DB:       conn,
		CacheDir: cacheDir,
		Now:      func() time.Time { return time.UnixMilli(0).UTC() },
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/maintenance/orphan-packages?dry-run=false", nil)
	res := httptest.NewRecorder()
	app.handleMaintenanceOrphanPackages(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var run orphanPackagesResponse
	if err := json.NewDecoder(res.Body).Decode(&run); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if run.DeletedRows != 1 {
		t.Errorf("deletedRows=%d, want 1", run.DeletedRows)
	}

	// Generated files must be gone.
	for _, name := range []string{"init.mp4", "stream.m3u8", "seg001.m4s"} {
		if _, err := os.Stat(filepath.Join(pkgDir, name)); !os.IsNotExist(err) {
			t.Errorf("generated file %s still exists after cleanup", name)
		}
	}
	// The unrecognised file and its directory must still be present.
	if _, err := os.Stat(extraFile); err != nil {
		t.Errorf("extra file %s was removed: %v", extraFile, err)
	}
	if _, err := os.Stat(pkgDir); err != nil {
		t.Errorf("package dir %s was removed despite containing extra files: %v", pkgDir, err)
	}
}

func TestHandleMaintenancePackageDelete(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	pkgRoot := filepath.Join(cacheDir, "packages")
	dbPath := filepath.Join(dir, "linearcast.db")

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	makeDir := func(rel string) string {
		full := filepath.Join(pkgRoot, rel)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(filepath.Join(full, "init.mp4"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write init: %v", err)
		}
		return filepath.Clean(full)
	}
	refRoot := makeDir("ref/h264-main-1080p")
	unrefRoot := makeDir("unref/h264-main-1080p")

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	mustExec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
		playback_mode, required_package_profile, hidden_from_guide)
		VALUES ('ch', 'Ch', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p', 0)`)
	mustExec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('ref', '/tmp/ref.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('unref', '/tmp/unref.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`)
	mustExec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, package_root, created_at_ms, updated_at_ms)
		VALUES ('pkg-ref', 'ref', 'h264-main-1080p', 'ready', ?, 0, 0),
		       ('pkg-unref', 'unref', 'h264-main-1080p', 'ready', ?, 0, 0)`, refRoot, unrefRoot)
	// 'ref' is scheduled (referenced); 'unref' is used by no channel.
	mustExec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('sched-1', 'ch', 0, 'ref', 0, 18000, 0)`)

	app := New(Config{
		DB:       conn,
		CacheDir: cacheDir,
		Now:      func() time.Time { return time.UnixMilli(0).UTC() },
	})

	call := func(query string) encodeReclaimResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, "/api/admin/maintenance/packages?"+query, nil)
		res := httptest.NewRecorder()
		app.handleMaintenancePackageDelete(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
		}
		var body encodeReclaimResponse
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body
	}
	pkgCount := func(id string) int {
		t.Helper()
		var c int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM media_packages WHERE id = ?`, id).Scan(&c); err != nil {
			t.Fatalf("count %s: %v", id, err)
		}
		return c
	}

	// Missing media param -> 400.
	{
		req := httptest.NewRequest(http.MethodDelete, "/api/admin/maintenance/packages", nil)
		res := httptest.NewRecorder()
		app.handleMaintenancePackageDelete(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("missing media: status=%d, want 400", res.Code)
		}
	}

	// Dry-run on the unreferenced media: reports one candidate, deletes nothing.
	dry := call("media=unref&dry-run=true")
	if !dry.DryRun || dry.Candidates != 1 || dry.DeletedRows != 0 {
		t.Fatalf("dry-run=%+v, want dryRun, 1 candidate, 0 deleted", dry)
	}
	if pkgCount("pkg-unref") != 1 {
		t.Fatalf("dry-run deleted the row")
	}
	if _, err := os.Stat(unrefRoot); err != nil {
		t.Fatalf("dry-run removed unref dir: %v", err)
	}

	// Real run on the unreferenced media: row and dir gone.
	run := call("media=unref&dry-run=false")
	if run.DeletedRows != 1 || run.SkippedRows != 0 {
		t.Fatalf("unref run=%+v, want 1 deleted, 0 skipped", run)
	}
	if pkgCount("pkg-unref") != 0 {
		t.Fatalf("pkg-unref still present")
	}
	if _, err := os.Stat(unrefRoot); !os.IsNotExist(err) {
		t.Fatalf("unref dir still exists: err=%v", err)
	}

	// Referenced media without force: skipped, left intact.
	skip := call("media=ref&dry-run=false")
	if skip.DeletedRows != 0 || skip.SkippedRows != 1 || len(skip.Items) != 1 || !skip.Items[0].Skipped || !skip.Items[0].Referenced {
		t.Fatalf("ref no-force=%+v, want 1 skipped referenced item, 0 deleted", skip)
	}
	if pkgCount("pkg-ref") != 1 {
		t.Fatalf("pkg-ref deleted without force")
	}
	if _, err := os.Stat(refRoot); err != nil {
		t.Fatalf("ref dir removed without force: %v", err)
	}

	// Referenced media WITH force: deleted.
	forced := call("media=ref&dry-run=false&force=true")
	if forced.DeletedRows != 1 || forced.SkippedRows != 0 {
		t.Fatalf("ref force=%+v, want 1 deleted, 0 skipped", forced)
	}
	if pkgCount("pkg-ref") != 0 {
		t.Fatalf("pkg-ref still present after force")
	}
	if _, err := os.Stat(refRoot); !os.IsNotExist(err) {
		t.Fatalf("ref dir still exists after force: err=%v", err)
	}
}

func TestHandleMaintenanceOptimizeDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "linearcast.db")

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	// Generate some churn so VACUUM has something to reclaim. Insert and delete
	// a batch of rows in a small enough volume that the test stays quick.
	for i := 0; i < 100; i++ {
		if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
			video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
			VALUES (?, ?, '/x', 1000, 'mkv', 'h264', 1080, 'aac', 1, 0)`,
			"m"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('0'+i%10)),
			"/x/"+string(rune('a'+i%26))+".mkv"); err != nil {
			// Fall back to a unique path on collisions; we just need bulk.
			continue
		}
	}
	if _, err := conn.Exec(`DELETE FROM media`); err != nil {
		t.Fatalf("delete: %v", err)
	}

	app := New(Config{
		DB:     conn,
		DBPath: dbPath,
		Now:    func() time.Time { return time.UnixMilli(0).UTC() },
	})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/maintenance/optimize-db", nil)
	res := httptest.NewRecorder()
	app.handleMaintenanceOptimizeDB(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body optimizeDBResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SizeBefore == 0 || body.SizeAfter == 0 {
		t.Errorf("size before/after both nonzero expected; got before=%d after=%d", body.SizeBefore, body.SizeAfter)
	}
	if body.DurationMs < 0 {
		t.Errorf("durationMs negative: %d", body.DurationMs)
	}
}

func TestHandleMaintenanceOptimizeDBMissingPath(t *testing.T) {
	conn, err := db.OpenReadWrite(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	app := New(Config{DB: conn}) // no DBPath
	req := httptest.NewRequest(http.MethodPost, "/api/admin/maintenance/optimize-db", nil)
	res := httptest.NewRecorder()
	app.handleMaintenanceOptimizeDB(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when DBPath unset, got %d", res.Code)
	}
}
