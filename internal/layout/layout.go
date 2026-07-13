// Package layout builds deterministic media package IDs and describes the
// filesystem layout shared by playback, admin, encoder, and packaging code.
package layout

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Well-known names of the artifacts every fMP4/HLS package contains. These are
// the single source of truth: code that reads, writes, validates, or globs
// package files must reference these rather than re-spelling the literals.
const (
	// InitName is the fMP4 initialization segment (moov/codec config).
	InitName = "init.mp4"
	// PlaylistName is the HLS media playlist.
	PlaylistName = "stream.m3u8"
	// SegmentPattern is the ffmpeg output template for media segments.
	SegmentPattern = "seg%06d.m4s"
	// SegmentGlob matches every media segment in a package root.
	SegmentGlob = "seg*.m4s"
)

// packagesSubdir is the cache-root subdirectory holding all package output.
// Unexported because callers join it through Cache methods rather than
// referencing the literal directly.
const packagesSubdir = "packages"

// SubtitlesDirName is the package-owned directory holding extracted WebVTT
// subtitle sidecars: <cacheDir>/packages/<mediaID>/<profile>/subtitles/s{N}.vtt.
const SubtitlesDirName = "subtitles"

var unsafe = regexp.MustCompile(`[^a-z0-9]+`)

// Cache is the filesystem layout rooted at CACHE_DIR.
type Cache struct {
	root string
}

// NewCache binds helpers to a cache root.
func NewCache(root string) Cache {
	return Cache{root: strings.TrimSpace(root)}
}

// Root returns the cache root.
func (c Cache) Root() string {
	return c.root
}

// PackagesDir is the directory holding finalized package roots, laid out as
// <cacheDir>/packages/<mediaID>/<profile>.
func (c Cache) PackagesDir() string {
	return filepath.Join(c.root, packagesSubdir)
}

// SubtitleSlug is the rendition slug / sidecar filename stem for a source
// subtitle stream.
func SubtitleSlug(streamIndex int) string {
	return "s" + strconv.Itoa(streamIndex)
}

// SubtitleFileName is the WebVTT sidecar filename for a source subtitle stream.
func SubtitleFileName(streamIndex int) string {
	return SubtitleSlug(streamIndex) + ".vtt"
}

// PackageRoot is the directory a package occupies under the default cache
// package root.
func (c Cache) PackageRoot(mediaID, profile string) string {
	return PackageRoot(c.PackagesDir(), mediaID, profile)
}

// PackageSubtitleDir is the package-owned subtitle sidecar directory.
func (c Cache) PackageSubtitleDir(mediaID, profile string) string {
	return PackageSubtitleDir(c.PackageRoot(mediaID, profile))
}

// PackageRoot is the directory a package occupies under outputRoot. Package
// output lives at <outputRoot>/<mediaID>/<profile>, the same pair that
// determines the package ID (see ID).
func PackageRoot(outputRoot, mediaID, profile string) string {
	return filepath.Join(outputRoot, mediaID, profile)
}

// PackageSubtitleDir is the subtitle sidecar directory inside packageRoot.
func PackageSubtitleDir(packageRoot string) string {
	return filepath.Join(packageRoot, SubtitlesDirName)
}

// IsSegment reports whether name is a media segment file (the in-memory
// equivalent of matching SegmentGlob), for callers inspecting names rather than
// globbing a directory.
func IsSegment(name string) bool {
	return strings.HasPrefix(name, "seg") && strings.HasSuffix(name, ".m4s")
}

// InitPath locates the init segment inside a package root.
func InitPath(root string) string { return filepath.Join(root, InitName) }

// PlaylistPath locates the HLS playlist inside a package root.
func PlaylistPath(root string) string { return filepath.Join(root, PlaylistName) }

// ID returns the stable package ID for a media/profile pair. The pair fully
// determines the package output, so it is the complete package identity: a
// given media item encoded under a given profile always produces the same
// bytes, including any forced-subtitle burn the profile resolves to.
func ID(mediaID, profile string) string {
	id := unsafe.ReplaceAllString(strings.ToLower(mediaID+"-"+profile), "-")
	id = strings.Trim(id, "-")
	if len(id) > 128 {
		id = id[:128]
	}
	return id
}
