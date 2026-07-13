package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleMediaDeleteBlocksEnabledChannelReferences(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	req := httptest.NewRequest(http.MethodDelete, "/api/media/m1", nil)
	req.SetPathValue("mediaID", "m1")
	res := httptest.NewRecorder()
	app.handleMediaDelete(res, req)
	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body mediaDeleteResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Blockers) == 0 {
		t.Fatalf("expected blockers, got %+v", body)
	}
}

func TestHandleMediaDeleteBlocksDisabledChannelReferences(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, false)

	req := httptest.NewRequest(http.MethodDelete, "/api/media/m1", nil)
	req.SetPathValue("mediaID", "m1")
	res := httptest.NewRecorder()
	app.handleMediaDelete(res, req)
	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body mediaDeleteResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Blockers) == 0 {
		t.Fatalf("expected blockers, got %+v", body)
	}
}

func TestHandleMediaDeleteRemovesMetadataAndPackagesOnly(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m2", 18000)
	pkgDir := t.TempDir()
	segmentDir := filepath.Join(pkgDir, "segments")
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		t.Fatalf("mkdir segments: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "init.mp4"), []byte("init"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(segmentDir, "0.m4s"), []byte("seg"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	initPath := filepath.Join(pkgDir, "init.mp4")
	pkgDur := int64(18000)
	if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
		ID:                 "pkg-m2",
		MediaID:            "m2",
		RenditionProfile:   db.DefaultPackageProfile,
		Status:             db.PackageStatusReady,
		PackageRoot:        &pkgDir,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur,
		CreatedAtMs:        0,
		UpdatedAtMs:        0,
	}); err != nil {
		t.Fatalf("insert package: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/media/m2", nil)
	req.SetPathValue("mediaID", "m2")
	res := httptest.NewRecorder()
	app.handleMediaDelete(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}

	assertCount(t, conn, `SELECT COUNT(*) FROM media WHERE id = 'm2'`, 0)
	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages WHERE media_id = 'm2'`, 0)
	if _, err := os.Stat(filepath.Join(pkgDir, "init.mp4")); !os.IsNotExist(err) {
		t.Fatalf("expected package file deleted, stat err=%v", err)
	}
}
