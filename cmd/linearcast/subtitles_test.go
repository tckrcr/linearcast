package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

func newSubtitleSelectionTestApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.OpenReadWrite(filepath.Join(dir, "linearcast.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
	})
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO media
		(id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 1000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
		ID:               "pkg-m1",
		MediaID:          "m1",
		RenditionProfile: packageprofile.DefaultName,
		Status:           db.PackageStatusReady,
	}); err != nil {
		t.Fatalf("upsert package: %v", err)
	}

	writeVTT := func(name, cue string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\n"+cue+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	forcedPath := writeVTT("s2.vtt", "FORCED NARRATIVE")
	dialoguePath := writeVTT("s3.vtt", "FULL DIALOGUE")
	sdhPath := writeVTT("s4.vtt", "SDH DIALOGUE")

	for _, tr := range []db.PackageTrack{
		{PackageID: "pkg-m1", Kind: "subtitle", StreamIndex: 2, Language: "eng", Title: "Forced", Codec: "webvtt", Source: db.TrackSourceEmbedded, Forced: true, Path: &forcedPath},
		{PackageID: "pkg-m1", Kind: "subtitle", StreamIndex: 3, Language: "eng", Codec: "webvtt", Source: db.TrackSourceEmbedded, Path: &dialoguePath},
		{PackageID: "pkg-m1", Kind: "subtitle", StreamIndex: 4, Language: "eng", Title: "SDH", Codec: "webvtt", Source: db.TrackSourceEmbedded, HearingImpaired: true, Path: &sdhPath},
	} {
		if err := db.UpsertPackageTrack(context.Background(), conn, tr); err != nil {
			t.Fatalf("upsert track %d: %v", tr.StreamIndex, err)
		}
	}

	return &app{dbConn: conn, packagedProfile: packageprofile.DefaultName}
}

func requestPackagedSubtitleVTT(t *testing.T, a *app, name string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/hls/channels/ch/streams/h264-1080p-8mbps/subs/pkg-m1/"+name, nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", packageprofile.DefaultName)
	req.SetPathValue("packageID", "pkg-m1")
	req.SetPathValue("name", name)

	res := httptest.NewRecorder()
	a.handleSubtitleVTT(res, req)
	return res.Code, res.Body.String()
}

func newPackagedSubtitleSegmentTestApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.OpenReadWrite(filepath.Join(dir, "linearcast.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
	})
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media
		(id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 72000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := ((time.Now().UTC().UnixMilli() - 10_000) / db.ScheduleGridMs) * db.ScheduleGridMs
	if _, err := conn.Exec(`INSERT INTO schedule_entries
		(id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('se1', 'ch', ?, 'm1', 0, 72000, 0)`, startMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	vttPath := filepath.Join(dir, "s3.vtt")
	vtt := strings.Join([]string{
		"WEBVTT",
		"",
		"00:00:01.000 --> 00:00:03.000",
		"opening cue",
		"",
		"00:00:08.000 --> 00:00:10.000",
		"middle cue",
		"",
		"00:01:08.000 --> 00:01:10.000",
		"after cue",
		"",
	}, "\n")
	if err := os.WriteFile(vttPath, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	pkgDur := int64(72_000)
	if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   packageprofile.DefaultName,
		Status:             db.PackageStatusReady,
		PackagedDurationMs: &pkgDur,
	}); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.UpsertPackageTrack(context.Background(), conn, db.PackageTrack{
		PackageID:   "pkg-m1",
		Kind:        "subtitle",
		StreamIndex: 3,
		Language:    "eng",
		Codec:       "webvtt",
		Source:      db.TrackSourceEmbedded,
		Path:        &vttPath,
	}); err != nil {
		t.Fatalf("upsert track: %v", err)
	}
	seg0Path := filepath.Join(dir, "0.m4s")
	seg1Path := filepath.Join(dir, "1.m4s")
	seg2Path := filepath.Join(dir, "2.m4s")
	if err := db.ReplacePackagedSegments(context.Background(), conn, "pkg-m1", []db.PackagedSegment{
		{PackageID: "pkg-m1", SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: &seg0Path},
		{PackageID: "pkg-m1", SegmentNumber: 1, MediaStartMs: 6000, DurationMs: 60000, Path: &seg1Path},
		{PackageID: "pkg-m1", SegmentNumber: 2, MediaStartMs: 66000, DurationMs: 6000, Path: &seg2Path},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	return &app{
		dbConn:          conn,
		packagedProfile: packageprofile.DefaultName,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				DisplayName:            "Channel",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: packageprofile.DefaultName,
				PrefillMode:            "eager",
			},
		},
	}
}

// TestHandleSubtitleVTTServesDialogueNotForced reproduces the Rings of Power
// S01E01 shape end-to-end: a media item with a forced narrative track at the
// lowest stream index, plus full dialogue and SDH. Requesting eng.vtt must
// serve the full dialogue file, not the near-empty forced track that made the
// CC button look broken.
func TestHandleSubtitleVTTServesDialogueNotForced(t *testing.T) {
	a := newSubtitleSelectionTestApp(t)
	code, body := requestPackagedSubtitleVTT(t, a, "eng.vtt")

	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, "FULL DIALOGUE") {
		t.Fatalf("served track is not full dialogue:\n%s", body)
	}
	if strings.Contains(body, "FORCED NARRATIVE") {
		t.Fatalf("served the forced track instead of dialogue:\n%s", body)
	}
}

func TestHandleSubtitleVTTServesForcedRendition(t *testing.T) {
	a := newSubtitleSelectionTestApp(t)
	code, body := requestPackagedSubtitleVTT(t, a, "eng-forced.vtt")

	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, "FORCED NARRATIVE") {
		t.Fatalf("served track is not forced narrative:\n%s", body)
	}
	if strings.Contains(body, "FULL DIALOGUE") {
		t.Fatalf("forced rendition served full dialogue:\n%s", body)
	}
}

func TestHandleSubtitlePlaylistMirrorsPackagedSegments(t *testing.T) {
	a := newPackagedSubtitleSegmentTestApp(t)

	req := httptest.NewRequest("GET", "/hls/channels/ch/streams/h264-1080p-8mbps/subs/eng/playlist.m3u8", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", packageprofile.DefaultName)
	req.SetPathValue("language", "eng")
	res := httptest.NewRecorder()

	a.handleSubtitlePlaylist(res, req)

	body := res.Body.String()
	if res.Code != 200 {
		t.Fatalf("status = %d, want 200:\n%s", res.Code, body)
	}
	if strings.Contains(body, "#EXT-X-PLAYLIST-TYPE:VOD") || strings.Contains(body, "#EXT-X-ENDLIST") {
		t.Fatalf("playlist must stay live-shaped:\n%s", body)
	}
	for _, want := range []string{
		"#EXT-X-TARGETDURATION:60\n",
		"#EXT-X-MEDIA-SEQUENCE:1\n",
		"../pkg-m1/eng.vtt?start=6000&dur=60000\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("playlist missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "../pkg-m1/eng.vtt\n") {
		t.Fatalf("playlist served whole subtitle sidecar instead of clipped segment URI:\n%s", body)
	}
}

func TestHandleSubtitleVTTClipsPackagedSegmentWindow(t *testing.T) {
	a := newPackagedSubtitleSegmentTestApp(t)

	req := httptest.NewRequest("GET", "/hls/channels/ch/streams/h264-1080p-8mbps/subs/pkg-m1/eng.vtt?start=6000&dur=60000", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", packageprofile.DefaultName)
	req.SetPathValue("packageID", "pkg-m1")
	req.SetPathValue("name", "eng.vtt")
	res := httptest.NewRecorder()

	a.handleSubtitleVTT(res, req)

	body := res.Body.String()
	if res.Code != 200 {
		t.Fatalf("status = %d, want 200:\n%s", res.Code, body)
	}
	if !strings.Contains(body, "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:540000") {
		t.Fatalf("clipped VTT missing MPEGTS map:\n%s", body)
	}
	if strings.Contains(body, "opening cue") || strings.Contains(body, "after cue") {
		t.Fatalf("clipped VTT included cues outside the segment window:\n%s", body)
	}
	if !strings.Contains(body, "00:00:02.000 --> 00:00:04.000\nmiddle cue") {
		t.Fatalf("clipped VTT did not rebase the in-window cue:\n%s", body)
	}
}

func TestWriteCachedSubtitlePlaylistIsLiveAligned(t *testing.T) {
	info := subtitleInfo{
		MediaID:         "m1",
		EntryStartMs:    1_000_000,
		EntryOffsetMs:   60_000,
		EntryDurationMs: 120_000,
		Segments: []subtitleSegmentInfo{
			{
				MediaStartMs:     66_000,
				DurationMs:       6_000,
				Sequence:         100,
				WallClockStartMs: 1_006_000,
			},
		},
	}

	res := httptest.NewRecorder()
	writeCachedSubtitlePlaylist(res, info)
	body := res.Body.String()

	if strings.Contains(body, "#EXT-X-PLAYLIST-TYPE:VOD") {
		t.Fatalf("subtitle playlist must not be VOD-shaped:\n%s", body)
	}
	if strings.Contains(body, "#EXT-X-ENDLIST") {
		t.Fatalf("subtitle playlist must stay live-shaped for hls.js PDT alignment:\n%s", body)
	}

	wantPDT := "#EXT-X-PROGRAM-DATE-TIME:" + time.UnixMilli(1_006_000).UTC().Format(pdtLayout)
	if !strings.Contains(body, wantPDT) {
		t.Fatalf("playlist missing PDT anchor %q:\n%s", wantPDT, body)
	}
	if !strings.Contains(body, "#EXT-X-MEDIA-SEQUENCE:100\n") {
		t.Fatalf("playlist missing media sequence:\n%s", body)
	}
	if !strings.Contains(body, "#EXTINF:6.000,\nseg/66000-6000.vtt\n") {
		t.Fatalf("playlist missing segment VTT URI:\n%s", body)
	}
}

func TestOnDemandMasterWithSubtitlesAdvertisesWebVTTCodec(t *testing.T) {
	a := &app{}
	entries := []db.ScheduleEntry{{ID: "entry-1"}}
	variants := []masterVariant{{
		Profile: "hevc-copy-source",
		BPS:     24_000_000,
		Codecs:  "hvc1.2.4.L153.B0,mp4a.40.2",
	}}
	subEntries := []subtitleEntry{{
		EncodingID: "sess-1",
		Lang:       "eng",
		Name:       "English",
		Slug:       "s2",
	}}

	res := httptest.NewRecorder()
	a.writeMasterManifest(context.Background(), res, "ch", variants, entries, subEntries)
	body := res.Body.String()

	if !strings.Contains(body, `SUBTITLES="subs"`) {
		t.Fatalf("master missing subtitle group:\n%s", body)
	}
	if !strings.Contains(body, `CODECS="hvc1.2.4.L153.B0,mp4a.40.2,wvtt"`) {
		t.Fatalf("master missing wvtt codec:\n%s", body)
	}
	if !strings.Contains(body, `URI="streams/hevc-copy-source/subs-channel-encoding/s2/playlist.m3u8"`) {
		t.Fatalf("master missing on-demand subtitle URI:\n%s", body)
	}
}

func TestOnDemandMasterAdvertisesForcedSubtitlesAsForced(t *testing.T) {
	a := &app{}
	entries := []db.ScheduleEntry{{ID: "entry-1"}}
	variants := []masterVariant{{
		Profile: packageprofile.HEVCCopySourceName,
		BPS:     24_000_000,
		Codecs:  "hvc1.2.4.L153.B0,mp4a.40.2",
	}}
	subEntries := []subtitleEntry{{
		EncodingID: "sess-1",
		Lang:       "eng",
		Name:       "English Forced",
		Slug:       "s2",
		Forced:     true,
		Default:    true,
	}}

	res := httptest.NewRecorder()
	a.writeMasterManifest(context.Background(), res, "ch", variants, entries, subEntries)
	body := res.Body.String()

	if !strings.Contains(body, `DEFAULT=NO,AUTOSELECT=YES,FORCED=YES`) {
		t.Fatalf("forced subtitle row is not spec-correct:\n%s", body)
	}
	if !strings.Contains(body, `URI="streams/hevc-copy-source/subs-channel-encoding/s2/playlist.m3u8"`) {
		t.Fatalf("master missing forced on-demand subtitle URI:\n%s", body)
	}
}

func TestOnDemandMasterWithoutSubtitlesDoesNotAdvertiseCC(t *testing.T) {
	a := &app{}
	variants := []masterVariant{{
		Profile: "hevc-copy-source",
		BPS:     24_000_000,
		Codecs:  "hvc1.2.4.L153.B0,mp4a.40.2",
	}}

	res := httptest.NewRecorder()
	a.writeMasterManifest(context.Background(), res, "ch", variants, nil, []subtitleEntry{})
	body := res.Body.String()

	if strings.Contains(body, `SUBTITLES="subs"`) || strings.Contains(body, "wvtt") || strings.Contains(body, "EXT-X-MEDIA:TYPE=SUBTITLES") {
		t.Fatalf("master advertised subtitles without ready entries:\n%s", body)
	}
}

func newPackagedSubtitleMasterTestApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.OpenReadWrite(filepath.Join(dir, "linearcast.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
	})
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media
		(id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 1000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	forcedPath := filepath.Join(dir, "s2.vtt")
	dialoguePath := filepath.Join(dir, "s3.vtt")
	for _, pkg := range []struct {
		id      string
		profile string
	}{
		{"pkg-m1-copy", packageprofile.HEVCCopySourceName},
		{"pkg-m1-default", packageprofile.DefaultName},
	} {
		if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
			ID:               pkg.id,
			MediaID:          "m1",
			RenditionProfile: pkg.profile,
			Status:           db.PackageStatusReady,
		}); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.id, err)
		}
		for _, tr := range []db.PackageTrack{
			{PackageID: pkg.id, Kind: "subtitle", StreamIndex: 2, Language: "eng", Title: "Forced", Codec: "webvtt", Source: db.TrackSourceEmbedded, Forced: true, Path: &forcedPath},
			{PackageID: pkg.id, Kind: "subtitle", StreamIndex: 3, Language: "eng", Codec: "webvtt", Source: db.TrackSourceEmbedded, Path: &dialoguePath},
		} {
			if err := db.UpsertPackageTrack(context.Background(), conn, tr); err != nil {
				t.Fatalf("upsert track %d: %v", tr.StreamIndex, err)
			}
		}
	}
	return &app{dbConn: conn}
}

func TestPackagedMasterAdvertisesForcedSubtitleRendition(t *testing.T) {
	a := newPackagedSubtitleMasterTestApp(t)
	variants := []masterVariant{{
		Profile: packageprofile.HEVCCopySourceName,
		BPS:     24_000_000,
		Codecs:  "hvc1.2.4.L153.B0,mp4a.40.2",
	}}
	entries := []db.ScheduleEntry{{MediaID: "m1"}}

	res := httptest.NewRecorder()
	a.writeMasterManifest(context.Background(), res, "ch", variants, entries, nil)
	body := res.Body.String()

	if !strings.Contains(body, `URI="streams/hevc-copy-source/subs/eng/playlist.m3u8"`) {
		t.Fatalf("master missing plain subtitle rendition:\n%s", body)
	}
	if !strings.Contains(body, `NAME="English (Forced)",LANGUAGE="eng",DEFAULT=NO,AUTOSELECT=YES,FORCED=YES,URI="streams/hevc-copy-source/subs/eng-forced/playlist.m3u8"`) {
		t.Fatalf("master missing forced subtitle rendition:\n%s", body)
	}
	if !strings.Contains(body, `SUBTITLES="subs"`) || !strings.Contains(body, `CODECS="hvc1.2.4.L153.B0,mp4a.40.2,wvtt"`) {
		t.Fatalf("master did not attach subtitle group/codecs:\n%s", body)
	}
}

func TestPackagedMasterSuppressesForcedRenditionWhenProfileBurnsIt(t *testing.T) {
	a := newPackagedSubtitleMasterTestApp(t)
	variants := []masterVariant{{
		Profile: packageprofile.DefaultName,
		BPS:     8_000_000,
		Codecs:  "avc1.4d401f,mp4a.40.2",
	}}
	entries := []db.ScheduleEntry{{MediaID: "m1"}}

	res := httptest.NewRecorder()
	a.writeMasterManifest(context.Background(), res, "ch", variants, entries, nil)
	body := res.Body.String()

	if !strings.Contains(body, `URI="streams/h264-1080p-8mbps/subs/eng/playlist.m3u8"`) {
		t.Fatalf("master missing plain subtitle rendition:\n%s", body)
	}
	if strings.Contains(body, `eng-forced`) || strings.Contains(body, `FORCED=YES`) {
		t.Fatalf("master advertised forced rendition even though profile burns it:\n%s", body)
	}
}

func TestServeCachedSubtitleSegmentClipsToSegmentLocalTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s3.vtt")
	vtt := strings.Join([]string{
		"WEBVTT",
		"",
		"00:01:04.000 --> 00:01:05.000",
		"before",
		"",
		"cue-id",
		"00:01:07.000 --> 00:01:09.000 line:90%",
		"inside",
		"",
		"00:01:11.000 --> 00:01:14.000",
		"tail",
		"",
		"00:01:20.000 --> 00:01:21.000",
		"after",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	res := httptest.NewRecorder()
	serveCachedSubtitleSegment(res, path, 66_000, 6_000, 0)

	body := res.Body.String()
	if !strings.Contains(body, "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0") {
		t.Fatalf("segment VTT missing X-TIMESTAMP-MAP:\n%s", body)
	}
	if strings.Contains(body, "before") || strings.Contains(body, "after") {
		t.Fatalf("segment VTT included non-overlapping cues:\n%s", body)
	}
	for _, want := range []string{
		"cue-id\n00:00:01.000 --> 00:00:03.000 line:90%\ninside",
		"00:00:05.000 --> 00:00:06.000\ntail",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("segment VTT missing %q:\n%s", want, body)
		}
	}
}

func TestClipWebVTTWithNonZeroMPEGTS(t *testing.T) {
	vtt := "WEBVTT\n\n00:01:05.000 --> 00:01:07.000\nhello\n"
	baseMediaStartMs := int64(0)
	mediaStartMs := int64(66_000)
	mpegts := (mediaStartMs - baseMediaStartMs) * 90

	body := clipWebVTT([]byte(vtt), mediaStartMs, 6_000, mpegts)
	bodyStr := string(body)

	wantMap := "MPEGTS:5940000"
	if !strings.Contains(bodyStr, wantMap) {
		t.Fatalf("missing %q in X-TIMESTAMP-MAP:\n%s", wantMap, bodyStr)
	}
	wantCue := "00:00:00.000 --> 00:00:01.000"
	if !strings.Contains(bodyStr, wantCue) {
		t.Fatalf("missing cue %q (should be relative to segment start):\n%s", wantCue, bodyStr)
	}
}

func TestClipWebVTTBoundaryCues(t *testing.T) {
	vtt := strings.Join([]string{
		"WEBVTT",
		"",
		"00:01:04.000 --> 00:01:05.000",
		"before",
		"",
		"00:01:05.000 --> 00:01:06.500",
		"straddle-start",
		"",
		"00:01:09.500 --> 00:01:11.000",
		"straddle-end",
		"",
		"00:01:06.000 --> 00:01:09.000",
		"inside",
		"",
		"00:01:11.000 --> 00:01:14.000",
		"after",
		"",
	}, "\n")

	body := clipWebVTT([]byte(vtt), 65_000, 6_000, 0)
	bodyStr := string(body)

	if strings.Contains(bodyStr, "before") {
		t.Fatal("should exclude cue ending before segment start")
	}
	if strings.Contains(bodyStr, "after") {
		t.Fatal("should exclude cue starting after segment end")
	}
	if !strings.Contains(bodyStr, "straddle-start") {
		t.Fatal("should include cue straddling segment start")
	}
	if !strings.Contains(bodyStr, "straddle-end") {
		t.Fatal("should include cue straddling segment end")
	}
	if !strings.Contains(bodyStr, "inside") {
		t.Fatal("should include cue fully inside segment")
	}
}

func TestClipWebVTTEmptyInput(t *testing.T) {
	body := clipWebVTT([]byte("WEBVTT\n\n"), 0, 6_000, 0)
	if string(body) != "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0\n\n" {
		t.Fatalf("empty clip produced unexpected output:\n%s", body)
	}
}

func TestClipWebVTTCarriageReturns(t *testing.T) {
	vtt := "WEBVTT\r\n\r\n00:01:00.000 --> 00:01:06.000\r\nhello\r\n"
	body := clipWebVTT([]byte(vtt), 60_000, 6_000, 0)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "hello") {
		t.Fatalf("CRLF input should be handled:\n%s", bodyStr)
	}
}

func TestParseSubtitleSegmentName(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantStart    int64
		wantDuration int64
		wantOK       bool
	}{
		{"valid with .vtt", "66000-6000.vtt", 66000, 6000, true},
		{"valid without .vtt", "66000-6000", 66000, 6000, true},
		{"zero start", "0-6000.vtt", 0, 6000, true},
		{"no separator", "66000", 0, 0, false},
		{"empty start", "-6000.vtt", 0, 0, false},
		{"non-numeric start", "abc-6000.vtt", 0, 0, false},
		{"zero duration", "66000-0.vtt", 0, 0, false},
		{"negative duration", "66000--1.vtt", 0, 0, false},
		{"negative start", "-1-6000.vtt", 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, dur, ok := parseSubtitleSegmentName(tc.input)
			if start != tc.wantStart || dur != tc.wantDuration || ok != tc.wantOK {
				t.Fatalf("parseSubtitleSegmentName(%q) = (%d,%d,%v), want (%d,%d,%v)",
					tc.input, start, dur, ok, tc.wantStart, tc.wantDuration, tc.wantOK)
			}
		})
	}
}

func TestIsSubtitleSlug(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"s0", true},
		{"s1", true},
		{"s123", true},
		{"s999999", true},
		{"", false},
		{"s", false},
		{"S1", false},
		{"0", false},
		{"abc", false},
		{"s-1", false},
		{"s1/", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := isSubtitleSlug(tc.input); got != tc.want {
				t.Fatalf("isSubtitleSlug(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestSubtitleSegmentExists(t *testing.T) {
	info := subtitleInfo{
		Segments: []subtitleSegmentInfo{
			{MediaStartMs: 66000, DurationMs: 6000},
			{MediaStartMs: 72000, DurationMs: 6000},
		},
	}
	if !subtitleSegmentExists(info, 66000, 6000) {
		t.Fatal("should find existing segment")
	}
	if subtitleSegmentExists(info, 66000, 7000) {
		t.Fatal("should not match different duration")
	}
	if subtitleSegmentExists(info, 99999, 6000) {
		t.Fatal("should not match non-existent media start")
	}
	if !subtitleSegmentExists(info, 72000, 6000) {
		t.Fatal("should match second entry")
	}
}

func TestParseWebVTTCueTiming(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantStart    int64
		wantEnd      int64
		wantSettings string
		wantOK       bool
	}{
		{"no settings", "00:01:00.000 --> 00:01:06.000", 60_000, 66_000, "", true},
		{"with settings", "00:01:00.000 --> 00:01:06.000 line:90%", 60_000, 66_000, "line:90%", true},
		{"no arrow", "00:01:00.000 00:01:06.000", 0, 0, "", false},
		{"malformed start", "abc --> 00:01:06.000", 0, 0, "", false},
		{"malformed end", "00:01:00.000 --> abc", 0, 0, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end, settings, ok := parseWebVTTCueTiming(tc.input)
			if start != tc.wantStart || end != tc.wantEnd || settings != tc.wantSettings || ok != tc.wantOK {
				t.Fatalf("parseWebVTTCueTiming(%q) = (%d,%d,%q,%v), want (%d,%d,%q,%v)",
					tc.input, start, end, settings, ok, tc.wantStart, tc.wantEnd, tc.wantSettings, tc.wantOK)
			}
		})
	}
}

func TestParseWebVTTTime(t *testing.T) {
	tests := []struct {
		input  string
		want   int64
		wantOK bool
	}{
		{"01:00.000", 60_000, true},
		{"00:00.000", 0, true},
		{"01:02.500", 62_500, true},
		{"00:00:01.000", 1_000, true},
		{"01:02:03.456", 3_723_456, true},
		{"", 0, false},
		{"abc", 0, false},
		{"01:00.00", 0, false},
		{"00:00:00.000", 0, true},
		{"01:00:00.000", 3_600_000, true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, ok := parseWebVTTTime(tc.input)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("parseWebVTTTime(%q) = (%d,%v), want (%d,%v)",
					tc.input, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestFormatWebVTTTime(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "00:00:00.000"},
		{1, "00:00:00.001"},
		{60_000, "00:01:00.000"},
		{3_600_000, "01:00:00.000"},
		{3_723_456, "01:02:03.456"},
		{-100, "00:00:00.000"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := formatWebVTTTime(tc.ms); got != tc.want {
				t.Fatalf("formatWebVTTTime(%d) = %q, want %q", tc.ms, got, tc.want)
			}
		})
	}
}

func TestSafeSubtitleRouteToken(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"playlist.m3u8", true},
		{"s2/playlist.m3u8", true},
		{"seg_000001.vtt", true},
		{"seg/66000-6000.vtt", true},
		{"", false},
		{"/", false},
		{"/foo", false},
		{".", false},
		{"..", false},
		{"foo/../bar", false},
		{"foo/./bar", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := safeSubtitleRouteToken(tc.input); got != tc.want {
				t.Fatalf("safeSubtitleRouteToken(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsEnglishSubtitleLanguage(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"eng", true},
		{"en", true},
		{"English", true},
		{"spa", false},
		{"und", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := isEnglishSubtitleLanguage(tc.input); got != tc.want {
				t.Fatalf("isEnglishSubtitleLanguage(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestCodecsWithWebVTT(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hvc1.1.6.L153.B0,mp4a.40.2", "hvc1.1.6.L153.B0,mp4a.40.2,wvtt"},
		{"hvc1.1.6.L153.B0,mp4a.40.2,wvtt", "hvc1.1.6.L153.B0,mp4a.40.2,wvtt"},
		{"", "wvtt"},
		{"wvtt", "wvtt"},
		{"avc1.64001f,mp4a.40.2", "avc1.64001f,mp4a.40.2,wvtt"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := codecsWithWebVTT(tc.input); got != tc.want {
				t.Fatalf("codecsWithWebVTT(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestCurrentSubtitleEntryID(t *testing.T) {
	if got := currentSubtitleEntryID(nil); got != "" {
		t.Fatalf("nil entries = %q, want empty", got)
	}
	if got := currentSubtitleEntryID([]db.ScheduleEntry{}); got != "" {
		t.Fatalf("empty entries = %q, want empty", got)
	}
	entries := []db.ScheduleEntry{{ID: "entry-1"}, {ID: "entry-2"}}
	if got := currentSubtitleEntryID(entries); got != "entry-1" {
		t.Fatalf("currentSubtitleEntryID = %q, want entry-1", got)
	}
}
