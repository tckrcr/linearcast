package packager

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

// CheckReadyPackageIntegrity verifies ready package filesystem outputs and
// moves broken rows back to pending so the normal worker claim path re-encodes.
func CheckReadyPackageIntegrity(ctx context.Context, conn *sql.DB) (int64, error) {
	packages, err := db.ReadyMediaPackages(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("list ready packages: %w", err)
	}

	var reset int64
	nowMs := time.Now().UTC().UnixMilli()
	for _, pkg := range packages {
		if ctx.Err() != nil {
			return reset, ctx.Err()
		}
		if err := validateReadyPackageFiles(pkg); err != nil {
			reason := fmt.Sprintf("package integrity check failed: %v", err)
			changed, markErr := db.MarkReadyPackagePendingForReencode(ctx, conn, pkg.ID, nowMs, reason)
			if markErr != nil {
				return reset, fmt.Errorf("mark package %s pending: %w", pkg.ID, markErr)
			}
			if changed {
				reset++
				log.Printf("package integrity reset id=%s media=%s profile=%s reason=%q",
					pkg.ID, pkg.MediaID, pkg.RenditionProfile, reason)
			}
		}
	}
	return reset, nil
}

func validateReadyPackageFiles(pkg db.MediaPackage) error {
	if pkg.InitSegmentPath == nil || *pkg.InitSegmentPath == "" {
		return fmt.Errorf("missing init_segment_path")
	}
	if err := requireRegularFile(*pkg.InitSegmentPath); err != nil {
		return fmt.Errorf("init segment %s: %w", *pkg.InitSegmentPath, err)
	}

	if pkg.PackageRoot == nil || *pkg.PackageRoot == "" {
		return fmt.Errorf("missing package_root")
	}
	playlist := filepath.Join(*pkg.PackageRoot, "stream.m3u8")
	if err := requireRegularFile(playlist); err != nil {
		return fmt.Errorf("manifest %s: %w", playlist, err)
	}
	segments, err := parseHLSManifest(playlist)
	if err != nil {
		return fmt.Errorf("parse manifest %s: %w", playlist, err)
	}
	if len(segments) == 0 {
		return fmt.Errorf("manifest %s contains no segments", playlist)
	}
	for _, seg := range segments {
		segmentPath := filepath.Join(*pkg.PackageRoot, filepath.FromSlash(seg.URI))
		if err := requireRegularFile(segmentPath); err != nil {
			return fmt.Errorf("segment %s: %w", segmentPath, err)
		}
	}
	return nil
}

func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory")
	}
	return nil
}
