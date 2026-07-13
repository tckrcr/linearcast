package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/metrics"
)

// cacheSampleInterval is how often the playback process re-walks the package
// cache to refresh the size gauge. Coarse on purpose: the cache is
// operator-paced (it only grows when channels/media/profiles change), so a
// few-minute sample is plenty to drive a disk-usage alert, and a full tree walk
// is too heavy to run on the hot per-minute metrics loop.
const cacheSampleInterval = 5 * time.Minute

// sampleCacheMetricsLoop periodically records the on-disk size of the package
// cache (<cacheDir>/packages) into linearcast_package_cache_bytes so cache
// growth is alertable instead of only visible on demand in the admin cache
// view. Reclamation stays operator-driven through admin orphan/unreferenced
// cleanup. An empty
// cache root (CACHE_DIR unset, or tests) disables the sampler.
func sampleCacheMetricsLoop(ctx context.Context, cache layout.Cache) {
	if cache.Root() == "" {
		return
	}
	packageRoot := cache.PackagesDir()
	sampleCacheBytes(packageRoot)
	t := time.NewTicker(cacheSampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sampleCacheBytes(packageRoot)
		}
	}
}

func sampleCacheBytes(packageRoot string) {
	n, err := dirSizeBytes(packageRoot)
	if err != nil {
		slog.Warn("package cache size sample failed", "root", packageRoot, "err", err)
		return
	}
	metrics.PackageCacheBytes.Set(float64(n))
}

// dirSizeBytes sums the sizes of all regular files under root. A missing root
// (fresh cache before the first encode) reports zero, not an error, and entries
// that vanish mid-walk — a concurrent encode finishing or an operator reclaim —
// are skipped rather than failing the whole sample.
func dirSizeBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
