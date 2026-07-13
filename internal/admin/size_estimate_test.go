package admin

import (
	"context"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

func TestExpectedVideoBpsForProfileUsesRecordedPackageBytes(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 8000)
	insertMedia(t, conn, "m2", 8000)

	insertReadyPackage := func(id, mediaID string, bytes int64) {
		t.Helper()
		dur := int64(8000)
		pkg := db.MediaPackage{
			ID:                 id,
			MediaID:            mediaID,
			RenditionProfile:   "custom-crf",
			Status:             db.PackageStatusReady,
			PackagedDurationMs: &dur,
			PackageBytes:       &bytes,
			CreatedAtMs:        0,
			UpdatedAtMs:        0,
		}
		if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
			t.Fatalf("insert package %s: %v", id, err)
		}
	}
	insertReadyPackage("pkg-1", "m1", 5_000_000)
	insertReadyPackage("pkg-2", "m2", 7_000_000)

	p := packageprofile.Profile{
		Name: "custom-crf",
		Video: packageprofile.VideoSettings{
			Mode: packageprofile.VideoModeTranscode,
			CRF:  23,
		},
		Audio: packageprofile.AudioSettings{
			Mode:    packageprofile.AudioModeTranscode,
			Bitrate: "256k",
		},
	}
	gotBps, gotN := app.expectedVideoBpsForProfile(context.Background(), p)
	if gotBps != 5_744_000 || gotN != 2 {
		t.Fatalf("expectedVideoBpsForProfile = %d/%d, want 5744000/2", gotBps, gotN)
	}
}

func TestExpectedVideoBpsForProfileSkipsNonQualityModes(t *testing.T) {
	app, _ := testAdminApp(t)
	p := packageprofile.Profile{
		Name: "copy",
		Video: packageprofile.VideoSettings{
			Mode: packageprofile.VideoModeCopy,
		},
	}
	if bps, n := app.expectedVideoBpsForProfile(context.Background(), p); bps != 0 || n != 0 {
		t.Fatalf("copy profile estimate = %d/%d, want 0/0", bps, n)
	}
}
