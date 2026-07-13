package layout

import (
	"path/filepath"
	"testing"
)

func TestCacheLayout(t *testing.T) {
	c := NewCache("/tmp/cache")
	if got, want := c.PackagesDir(), "/tmp/cache/packages"; got != want {
		t.Fatalf("PackagesDir = %q, want %q", got, want)
	}
	if got, want := c.PackageRoot("m1", "h264-1080p-8mbps"), "/tmp/cache/packages/m1/h264-1080p-8mbps"; got != want {
		t.Fatalf("PackageRoot = %q, want %q", got, want)
	}
	if got, want := c.PackageSubtitleDir("m1", "h264-1080p-8mbps"), "/tmp/cache/packages/m1/h264-1080p-8mbps/subtitles"; got != want {
		t.Fatalf("PackageSubtitleDir = %q, want %q", got, want)
	}
}

func TestPackageSubtitleDirIsInsidePackageRoot(t *testing.T) {
	c := NewCache("/tmp/cache")
	pkgRoot := c.PackageRoot("m1", "h264-1080p-8mbps")
	subDir := c.PackageSubtitleDir("m1", "h264-1080p-8mbps")
	if filepath.Dir(subDir) != pkgRoot {
		t.Fatalf("PackageSubtitleDir parent %q != PackageRoot %q", filepath.Dir(subDir), pkgRoot)
	}
	if filepath.Base(subDir) != SubtitlesDirName {
		t.Fatalf("PackageSubtitleDir base = %q, want SubtitlesDirName %q", filepath.Base(subDir), SubtitlesDirName)
	}
}

func TestSubtitleSlugAndFileName(t *testing.T) {
	tests := []struct {
		index    int
		slug     string
		fileName string
	}{
		{0, "s0", "s0.vtt"},
		{1, "s1", "s1.vtt"},
		{123, "s123", "s123.vtt"},
	}
	for _, tc := range tests {
		if got := SubtitleSlug(tc.index); got != tc.slug {
			t.Errorf("SubtitleSlug(%d) = %q, want %q", tc.index, got, tc.slug)
		}
		if got := SubtitleFileName(tc.index); got != tc.fileName {
			t.Errorf("SubtitleFileName(%d) = %q, want %q", tc.index, got, tc.fileName)
		}
	}
}
