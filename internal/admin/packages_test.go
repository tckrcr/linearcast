package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

func TestHandleMediaPackageQueuesArbitraryMedia(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m2", 12000)
	insertMedia(t, conn, "ready", 12000)
	insertReadyPackage(t, conn, "ready", 12000)
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, codec_check_reason, ingested_at_ms)
		VALUES ('bad-codec', '/tmp/bad-codec.mkv', '/tmp', 12000, 'mkv', 'hevc', 2160, 'aac', 0, 'unsupported video codec', 0)`); err != nil {
		t.Fatalf("insert bad codec media: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/media/package",
		bytes.NewBufferString(`{"mediaIds":["m2","ready","bad-codec","no-such"]}`))
	res := httptest.NewRecorder()

	app.handleMediaPackage(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Profile      string `json:"profile"`
		Queued       []string
		AlreadyReady []string `json:"alreadyReady"`
		Failed       []struct {
			MediaID string `json:"mediaId"`
			Code    string `json:"code"`
		} `json:"failed"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Profile != db.DefaultPackageProfile {
		t.Fatalf("unexpected profile: %+v", body)
	}
	if len(body.Queued) != 1 || body.Queued[0] != "m2" {
		t.Fatalf("queued=%v, want m2", body.Queued)
	}
	if len(body.AlreadyReady) != 1 || body.AlreadyReady[0] != "ready" {
		t.Fatalf("alreadyReady=%v, want ready", body.AlreadyReady)
	}
	if len(body.Failed) != 2 || body.Failed[0].Code != "codec_check_failed" || body.Failed[1].Code != "not_found" {
		t.Fatalf("failed=%+v, want codec_check_failed then not_found", body.Failed)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages WHERE media_id='m2' AND rendition_profile='h264-1080p-8mbps' AND status='pending'`, 1)
}

func TestHandleMediaPackageRejectsCopyProfileOverBrowserHLSBitrateCeiling(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "high", 12000)
	if _, err := conn.Exec(`UPDATE media SET video_codec='hevc', video_bitrate_bps=41000000 WHERE id='high'`); err != nil {
		t.Fatalf("set source bitrate: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/media/package",
		bytes.NewBufferString(`{"mediaIds":["high"],"profile":"hevc-copy-source"}`))
	res := httptest.NewRecorder()

	app.handleMediaPackage(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if got := res.Body.String(); !strings.Contains(got, "copy_profile_browser_hls_bitrate_ceiling") {
		t.Fatalf("body=%s, want copy_profile_browser_hls_bitrate_ceiling", got)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages WHERE media_id='high'`, 0)
}

func TestHandleMediaPackageCancelAll(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "pending", 12000)
	insertMedia(t, conn, "processing", 12000)
	for _, pkg := range []db.MediaPackage{
		{ID: "pkg-pending", MediaID: "pending", RenditionProfile: db.DefaultPackageProfile, Status: db.PackageStatusPending, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-processing", MediaID: "processing", RenditionProfile: db.DefaultPackageProfile, Status: db.PackageStatusProcessing, CreatedAtMs: 1, UpdatedAtMs: 1},
	} {
		if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
			t.Fatalf("insert package %s: %v", pkg.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/media/package/cancel",
		bytes.NewBufferString(`{"profile":"h264-1080p-8mbps","all":true}`))
	res := httptest.NewRecorder()

	app.handleMediaPackageCancel(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		CanceledPending    int64 `json:"canceledPending"`
		CanceledProcessing int64 `json:"canceledProcessing"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.CanceledPending != 1 || body.CanceledProcessing != 1 {
		t.Fatalf("response=%+v, want one pending and one processing canceled", body)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages WHERE status='failed' AND error='cancelled by operator'`, 2)
}

func TestHandleMediaPackageCandidatesListsAccurateStatus(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "missing", 12000)
	insertMedia(t, conn, "failed", 12000)
	insertMedia(t, conn, "ready", 12000)
	errStr := "encode failed"
	if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
		ID:               "pkg-failed",
		MediaID:          "failed",
		RenditionProfile: db.DefaultPackageProfile,
		Status:           db.PackageStatusFailed,
		Error:            &errStr,
		CreatedAtMs:      1,
		UpdatedAtMs:      1,
	}); err != nil {
		t.Fatalf("insert failed package: %v", err)
	}
	insertReadyPackage(t, conn, "ready", 12000)

	req := httptest.NewRequest(http.MethodGet, "/api/media/package-candidates", nil)
	res := httptest.NewRecorder()

	app.handleMediaPackageCandidates(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Profile      string `json:"profile"`
		Count        int64  `json:"count"`
		StatusCounts []struct {
			Status string `json:"status"`
			Count  int64  `json:"count"`
		} `json:"statusCounts"`
		Media []struct {
			MediaID       string `json:"mediaId"`
			PackageStatus string `json:"packageStatus"`
			PackageError  string `json:"packageError"`
			Selectable    bool   `json:"selectable"`
		} `json:"media"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Profile != db.DefaultPackageProfile || body.Count != 2 {
		t.Fatalf("unexpected response: %+v", body)
	}
	counts := map[string]int64{}
	for _, row := range body.StatusCounts {
		counts[row.Status] = row.Count
	}
	if counts["missing"] != 1 || counts[string(db.PackageStatusFailed)] != 1 || counts[string(db.PackageStatusReady)] != 1 {
		t.Fatalf("statusCounts=%+v, want missing=1 failed=1 ready=1", counts)
	}
	byID := map[string]struct {
		status     string
		selectable bool
		err        string
	}{}
	for _, item := range body.Media {
		byID[item.MediaID] = struct {
			status     string
			selectable bool
			err        string
		}{item.PackageStatus, item.Selectable, item.PackageError}
	}
	if byID["missing"].status != "missing" || !byID["missing"].selectable {
		t.Fatalf("missing row mismatch: %+v", byID["missing"])
	}
	if byID["failed"].status != "failed" || !byID["failed"].selectable || byID["failed"].err != "encode failed" {
		t.Fatalf("failed row mismatch: %+v", byID["failed"])
	}
	if _, ok := byID["ready"]; ok {
		t.Fatalf("ready row should not be listed: %+v", byID)
	}
}

func TestHandleMediaPackageCandidatesListsAllProfilesReadOnly(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "ready-main", 12000)
	insertMedia(t, conn, "ready-custom", 12000)
	pkgDur := int64(12000)
	for _, pkg := range []db.MediaPackage{
		{
			ID:                 "pkg-ready-main",
			MediaID:            "ready-main",
			RenditionProfile:   db.DefaultPackageProfile,
			Status:             db.PackageStatusReady,
			PackagedDurationMs: &pkgDur,
			CreatedAtMs:        1,
			UpdatedAtMs:        1,
		},
		{
			ID:                 "pkg-ready-custom",
			MediaID:            "ready-custom",
			RenditionProfile:   "custom-videotoolbox-1080p",
			Status:             db.PackageStatusReady,
			PackagedDurationMs: &pkgDur,
			CreatedAtMs:        2,
			UpdatedAtMs:        2,
		},
	} {
		if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
			t.Fatalf("insert package %s: %v", pkg.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/media/package-candidates?profile=all&status=ready", nil)
	res := httptest.NewRecorder()

	app.handleMediaPackageCandidates(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Profile string `json:"profile"`
		Count   int64  `json:"count"`
		Media   []struct {
			MediaID        string `json:"mediaId"`
			PackageStatus  string `json:"packageStatus"`
			PackageProfile string `json:"packageProfile"`
			Selectable     bool   `json:"selectable"`
		} `json:"media"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Profile != allPackageProfiles || body.Count != 2 || len(body.Media) != 2 {
		t.Fatalf("unexpected response: %+v", body)
	}
	byID := map[string]struct {
		status     string
		profile    string
		selectable bool
	}{}
	for _, item := range body.Media {
		byID[item.MediaID] = struct {
			status     string
			profile    string
			selectable bool
		}{item.PackageStatus, item.PackageProfile, item.Selectable}
	}
	for mediaID, wantProfile := range map[string]string{
		"ready-main":   db.DefaultPackageProfile,
		"ready-custom": "custom-videotoolbox-1080p",
	} {
		got := byID[mediaID]
		if got.status != string(db.PackageStatusReady) || got.profile != wantProfile || got.selectable {
			t.Fatalf("%s=%+v, want ready %s non-selectable", mediaID, got, wantProfile)
		}
	}
}

func TestHandleMediaPackageProfilesExcludesTypoPackageRows(t *testing.T) {
	app, conn := testAdminApp(t)
	if err := db.SetDefaultPackagedProfile(context.Background(), conn, "removed-profile"); err != nil {
		t.Fatalf("set default profile: %v", err)
	}
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)
	if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
		ID:               "pkg-typo",
		MediaID:          "m2",
		RenditionProfile: "h264-maindfdfd-1080p",
		Status:           db.PackageStatusPending,
		CreatedAtMs:      1,
		UpdatedAtMs:      1,
	}); err != nil {
		t.Fatalf("insert typo package: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/media/package-profiles", nil)
	res := httptest.NewRecorder()

	app.handleMediaPackageProfiles(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Profiles       []string `json:"profiles"`
		DefaultProfile string   `json:"defaultProfile"`
		ProfileDetails []struct {
			Name  string `json:"name"`
			Video struct {
				Mode string `json:"mode"`
			} `json:"video"`
			Audio struct {
				Mode string `json:"mode"`
			} `json:"audio"`
		} `json:"profileDetails"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Profiles) == 0 {
		t.Fatal("profiles empty, want built-in profiles")
	}
	if body.Profiles[0] != db.DefaultPackageProfile {
		t.Fatalf("profiles[0]=%q, want default profile first", body.Profiles[0])
	}
	if body.DefaultProfile != db.DefaultPackageProfile {
		t.Fatalf("defaultProfile=%q, want seeded default when stored profile is unavailable", body.DefaultProfile)
	}
	if len(body.ProfileDetails) != len(body.Profiles) {
		t.Fatalf("profileDetails=%d, want one detail per profile (%d)", len(body.ProfileDetails), len(body.Profiles))
	}
	if body.ProfileDetails[0].Video.Mode == "" || body.ProfileDetails[0].Audio.Mode == "" {
		t.Fatalf("profileDetails missing nested modes: %+v", body.ProfileDetails)
	}
}

func TestHandleMediaPackageProfilesSeparatesActiveAndDisabled(t *testing.T) {
	app, conn := testAdminApp(t)
	if err := db.DisablePackageProfile(context.Background(), conn, db.DefaultPackageProfile); err != nil {
		t.Fatalf("disable profile: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/media/package-profiles", nil)
	res := httptest.NewRecorder()

	app.handleMediaPackageProfiles(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Profiles       []string `json:"profiles"`
		DefaultProfile string   `json:"defaultProfile"`
		ProfileDetails []struct {
			Name     string `json:"name"`
			Disabled bool   `json:"disabled"`
		} `json:"profileDetails"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.Contains(strings.Join(body.Profiles, ","), db.DefaultPackageProfile) {
		t.Fatalf("profiles=%v should exclude disabled default profile", body.Profiles)
	}
	if body.DefaultProfile != db.DefaultPackageProfile {
		t.Fatalf("defaultProfile=%q should fall back to default when no active profiles remain", body.DefaultProfile)
	}
	foundDisabled := false
	for _, detail := range body.ProfileDetails {
		if detail.Name == db.DefaultPackageProfile {
			foundDisabled = detail.Disabled
		}
	}
	if !foundDisabled {
		t.Fatalf("profileDetails=%+v should include disabled profile for inspection", body.ProfileDetails)
	}
}

func TestHandlePackageProfileDeleteSoftDisablesReferencedProfile(t *testing.T) {
	app, conn := testAdminApp(t)
	profile := packageprofile.Profile{
		Name:        "custom-h264-1080p",
		Label:       "Custom H.264 1080p",
		Description: "custom",
		Video:       packageprofile.VideoSettings{Mode: packageprofile.VideoModeTranscode, Codec: "libx264", Profile: "main"},
		Audio:       packageprofile.AudioSettings{Mode: packageprofile.AudioModeTranscode, Codec: "aac"},
	}
	if err := db.UpsertPackageProfile(context.Background(), conn, profile); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	insertMedia(t, conn, "m-custom", 12000)
	if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
		ID:               "pkg-custom",
		MediaID:          "m-custom",
		RenditionProfile: profile.Name,
		Status:           db.PackageStatusReady,
		CreatedAtMs:      1,
		UpdatedAtMs:      1,
	}); err != nil {
		t.Fatalf("insert package: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/package-profiles/custom-h264-1080p", nil)
	req.SetPathValue("name", profile.Name)
	res := httptest.NewRecorder()

	app.handlePackageProfileDelete(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Deleted  bool `json:"deleted"`
		Disabled bool `json:"disabled"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Deleted || !body.Disabled {
		t.Fatalf("response=%+v, want soft disabled", body)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM package_profiles WHERE name='custom-h264-1080p' AND disabled=1`, 1)
	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages WHERE rendition_profile='custom-h264-1080p'`, 1)
}

func TestHandlePackageProfileDeleteHardDeletesUnusedCustomProfile(t *testing.T) {
	app, conn := testAdminApp(t)
	profile := packageprofile.Profile{
		Name:        "unused-h264-1080p",
		Label:       "Unused H.264 1080p",
		Description: "unused",
		Video:       packageprofile.VideoSettings{Mode: packageprofile.VideoModeTranscode, Codec: "libx264", Profile: "main"},
		Audio:       packageprofile.AudioSettings{Mode: packageprofile.AudioModeTranscode, Codec: "aac"},
	}
	if err := db.UpsertPackageProfile(context.Background(), conn, profile); err != nil {
		t.Fatalf("insert profile: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/package-profiles/unused-h264-1080p", nil)
	req.SetPathValue("name", profile.Name)
	res := httptest.NewRecorder()

	app.handlePackageProfileDelete(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM package_profiles WHERE name='unused-h264-1080p'`, 0)
}

func TestHandlePackageProfileDeleteHardDeletesDisabledUnusedCustomProfile(t *testing.T) {
	app, conn := testAdminApp(t)
	profile := packageprofile.Profile{
		Name:        "disabled-unused-h264-1080p",
		Label:       "Disabled unused H.264 1080p",
		Description: "disabled unused",
		Video:       packageprofile.VideoSettings{Mode: packageprofile.VideoModeTranscode, Codec: "libx264", Profile: "main"},
		Audio:       packageprofile.AudioSettings{Mode: packageprofile.AudioModeTranscode, Codec: "aac"},
	}
	if err := db.UpsertPackageProfile(context.Background(), conn, profile); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	if err := db.DisablePackageProfile(context.Background(), conn, profile.Name); err != nil {
		t.Fatalf("disable profile: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/package-profiles/disabled-unused-h264-1080p", nil)
	req.SetPathValue("name", profile.Name)
	res := httptest.NewRecorder()

	app.handlePackageProfileDelete(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM package_profiles WHERE name='disabled-unused-h264-1080p'`, 0)
}

func TestHandlePackageProfileEnableReactivatesDisabledProfile(t *testing.T) {
	app, conn := testAdminApp(t)
	if err := db.DisablePackageProfile(context.Background(), conn, packageprofile.HEVCCopySourceName); err != nil {
		t.Fatalf("disable profile: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/package-profiles/hevc-copy-source/enable", nil)
	req.SetPathValue("name", packageprofile.HEVCCopySourceName)
	res := httptest.NewRecorder()

	app.handlePackageProfileEnable(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Name != packageprofile.HEVCCopySourceName || !body.Enabled {
		t.Fatalf("response=%+v, want enabled hevc-copy-source", body)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM package_profiles WHERE name='hevc-copy-source' AND disabled=0`, 1)
}

func TestHandlePackageProfileEnableAlreadyActiveIsIdempotent(t *testing.T) {
	app, conn := testAdminApp(t)

	req := httptest.NewRequest(http.MethodPost, "/api/package-profiles/hevc-copy-source/enable", nil)
	req.SetPathValue("name", packageprofile.HEVCCopySourceName)
	res := httptest.NewRecorder()

	app.handlePackageProfileEnable(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM package_profiles WHERE name='hevc-copy-source' AND disabled=0`, 1)
}

func TestHandleMediaPackageAcceptsCustomProfile(t *testing.T) {
	app, conn := testAdminApp(t)
	profile := packageprofile.Profile{
		Name:        "custom-copy-1080p",
		Label:       "Custom copy 1080p",
		Description: "custom",
		Video:       packageprofile.VideoSettings{Mode: packageprofile.VideoModeCopy, CodecRequired: "h264"},
		Audio:       packageprofile.AudioSettings{Mode: packageprofile.AudioModeTranscode, Codec: "aac"},
	}
	if err := db.UpsertPackageProfile(context.Background(), conn, profile); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	insertMedia(t, conn, "m2", 12000)
	req := httptest.NewRequest(http.MethodPost, "/api/media/package",
		bytes.NewBufferString(`{"mediaIds":["m2"],"profile":"custom-copy-1080p"}`))
	res := httptest.NewRecorder()

	app.handleMediaPackage(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages WHERE media_id='m2' AND rendition_profile='custom-copy-1080p' AND status='pending'`, 1)
}

func TestHandleMediaPackageCapsMediaIDs(t *testing.T) {
	app, _ := testAdminApp(t)
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = fmt.Sprintf("m%d", i)
	}
	body, err := json.Marshal(map[string]any{"mediaIds": ids})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/media/package", bytes.NewReader(body))
	res := httptest.NewRecorder()

	app.handleMediaPackage(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want bad request", res.Code, res.Body.String())
	}
}

func TestHandleMediaPackageRejectsUnavailableProfile(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m2", 12000)
	req := httptest.NewRequest(http.MethodPost, "/api/media/package",
		bytes.NewBufferString(`{"mediaIds":["m2"],"profile":"h264-maindfdfd-1080p"}`))
	res := httptest.NewRecorder()

	app.handleMediaPackage(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want bad request", res.Code, res.Body.String())
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages WHERE media_id='m2'`, 0)
}

func TestHandleMediaPackageCandidatesRejectsUnavailableProfile(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/media/package-candidates?profile=h264-maindfdfd-1080p", nil)
	res := httptest.NewRecorder()

	app.handleMediaPackageCandidates(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want bad request", res.Code, res.Body.String())
	}
}
