package mediasource

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tckrcr/linearcast/internal/lcingest"
)

// LocalMediaSourceClient implements MediaSourceClient for local filesystem sources.
type LocalMediaSourceClient struct {
	dbConn *sql.DB
	source *LocalMediaSource
}

// LocalMediaSource represents a configured local media source.
type LocalMediaSource struct {
	ID        string
	Name      string
	MediaKind string
	Paths     []string
}

// NewLocalMediaSourceClient creates a client for scanning local media sources.
func NewLocalMediaSourceClient(dbConn *sql.DB, source *LocalMediaSource) *LocalMediaSourceClient {
	return &LocalMediaSourceClient{
		dbConn: dbConn,
		source: source,
	}
}

// Name returns the source kind identifier.
func (c *LocalMediaSourceClient) Name() string {
	return "local"
}

// Status returns local source status (always connected).
func (c *LocalMediaSourceClient) Status(ctx context.Context) (Status, error) {
	for _, p := range c.source.Paths {
		if _, err := os.Stat(p); err != nil {
			return Status{
				Connected:  false,
				ServerName: c.source.Name,
				URL:        "local",
			}, err
		}
	}
	return Status{
		Connected:  true,
		ServerName: c.source.Name,
		URL:        "local",
	}, nil
}

// Libraries returns the configured paths as "libraries".
func (c *LocalMediaSourceClient) Libraries(ctx context.Context) ([]Library, error) {
	libs := make([]Library, 0, len(c.source.Paths))
	for i, p := range c.source.Paths {
		name := filepath.Base(p)
		if name == "" || name == "." {
			name = p
		}
		libs = append(libs, Library{
			ID:   fmt.Sprintf("path-%d", i),
			Name: name,
			Type: c.source.MediaKind,
		})
	}
	return libs, nil
}

// Items returns media items from a path, using lcingest to probe files.
func (c *LocalMediaSourceClient) Items(ctx context.Context, libraryID string, opts ScanOptions) ([]Item, error) {
	// Parse libraryID to get path index
	var pathIndex int
	_, err := fmt.Sscanf(libraryID, "path-%d", &pathIndex)
	if err != nil || pathIndex < 0 || pathIndex >= len(c.source.Paths) {
		return nil, fmt.Errorf("invalid library ID: %s", libraryID)
	}

	path := c.source.Paths[pathIndex]
	mediaKind := opts.MediaKind
	if mediaKind == "" {
		mediaKind = c.source.MediaKind
	}

	var files []string
	var countErr error

	if mediaKind == "music" {
		files, countErr = walkMusicFiles(path)
	} else if mediaKind == "filler" {
		files, countErr = walkFillerFiles(path)
	} else {
		files, countErr = walkMediaFiles(path)
	}
	if countErr != nil {
		return nil, countErr
	}

	items := make([]Item, 0, len(files))
	for _, f := range files {
		if mediaKind == "music" {
			mp, probeErr := lcingest.FFProbeMusicFileForPath(ctx, f)
			if probeErr != nil {
				continue
			}
			items = append(items, Item{
				ID:        lcingest.MediaIDFor(f),
				Title:     lcingest.DeriveMusicTitle(f, mp),
				Type:      "track",
				Path:      f,
				SourceRef: f,
			})
			continue
		}

		probe, _, probeErr := lcingest.FFProbeFileForPath(ctx, f)
		if probeErr != nil {
			continue
		}
		items = append(items, Item{
			ID:         lcingest.MediaIDFor(f),
			Title:      lcingest.DeriveTitle(f),
			Type:       itemTypeForMediaKind(mediaKind),
			Path:       f,
			Resolution: resolutionFromHeight(probe.VideoHeight),
			Height:     int(probe.VideoHeight),
			SourceRef:  f,
		})
	}
	return items, nil
}

// Close releases any resources.
func (c *LocalMediaSourceClient) Close() error {
	return nil
}

// compile-time interface check
var _ MediaSourceClient = (*LocalMediaSourceClient)(nil)

// Helper functions

func walkMediaFiles(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".mkv" || ext == ".mp4" || ext == ".webm" {
			paths = append(paths, p)
		}
		return nil
	})
	return paths, err
}

func walkMusicFiles(dir string) ([]string, error) {
	musicExts := map[string]bool{
		".flac": true, ".mp3": true, ".wav": true,
		".m4a": true, ".aiff": true, ".aif": true,
		".dsf": true, ".dff": true, ".ape": true,
		".ogg": true, ".opus": true,
	}
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if musicExts[strings.ToLower(filepath.Ext(p))] {
			paths = append(paths, p)
		}
		return nil
	})
	return paths, err
}

func walkFillerFiles(dir string) ([]string, error) {
	fillerExts := map[string]bool{
		".mkv": true, ".mp4": true, ".webm": true,
		".ts": true, ".mov": true, ".avi": true,
	}
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if fillerExts[strings.ToLower(filepath.Ext(p))] {
			paths = append(paths, p)
		}
		return nil
	})
	return paths, err
}

func itemTypeForMediaKind(kind string) string {
	switch kind {
	case "movies":
		return "movie"
	case "shows":
		return "episode"
	case "music":
		return "track"
	case "filler":
		return "movie"
	default:
		return "movie"
	}
}

func resolutionFromHeight(height int64) string {
	switch {
	case height >= 2160:
		return "4k"
	case height >= 1080:
		return "1080"
	case height >= 720:
		return "720"
	default:
		return ""
	}
}

// We need to expose some lcingest functions. For now, we'll need to add them to lcingest.