package mediasource

import (
	"context"
)

// Library represents a media library/section from a media source.
type Library struct {
	ID   string
	Name string
	Type string // "movies", "shows", "music", "filler"
}

// Item represents a single media item from a library.
type Item struct {
	ID            string
	Title         string
	Type          string // "movie", "episode", "track"
	SeriesName    string
	SeasonNumber  int
	EpisodeNumber int
	Year          int
	Description   string
	ThumbnailPath string
	ContentRating string
	Genres        []string
	Path          string
	Resolution    string // e.g. "1080", "720", "4k"
	Height        int    // video height in pixels, 0 if unknown
	SourceRef     string // e.g. "plex://123", "jellyfin://456", or local path
}

// ScanOptions controls the scanning behavior.
type ScanOptions struct {
	MaxResolution string // "", "1080", "720", etc.
	MediaKind     string // "movies", "shows", "music", "filler"
}

// MediaSourceClient defines the interface for media source clients.
// Implementations: Plex, Jellyfin, Local.
type MediaSourceClient interface {
	// Name returns the source kind identifier (e.g. "plex", "jellyfin", "local").
	Name() string

	// Status checks connectivity and returns server info.
	Status(ctx context.Context) (Status, error)

	// Libraries returns all available libraries/sections.
	Libraries(ctx context.Context) ([]Library, error)

	// Items returns items in a library, filtered by options.
	Items(ctx context.Context, libraryID string, opts ScanOptions) ([]Item, error)

	// Close releases any resources held by the client.
	Close() error
}

// Status represents server connection status.
type Status struct {
	Connected  bool
	ServerName string
	Username   string
	Version    string
	ID         string
	URL        string
	PathMap    string
}

// PathMapper defines path rewriting behavior.
type PathMapper interface {
	Map(path string) string
}
