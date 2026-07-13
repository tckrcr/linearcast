package liveproxy

import (
	"strings"
	"testing"
)

func TestRewriteManifestRewritesLinesAndURIAttributes(t *testing.T) {
	in := []byte(strings.Join([]string{
		"#EXTM3U",
		`#EXT-X-MAP:URI="init.mp4?token=secret"`,
		"#EXTINF:6.000,",
		"segment-000.ts?token=secret",
		"#EXT-X-STREAM-INF:BANDWIDTH=800000",
		"variants/low/index.m3u8",
		"",
	}, "\n"))

	out := string(RewriteManifest(in, "", func(raw, sourcePath string) (string, bool) {
		rel, ok := RelativePath(raw, sourcePath)
		if !ok {
			return "", false
		}
		return "/proxy/" + rel, true
	}))

	for _, want := range []string{
		`#EXT-X-MAP:URI="/proxy/init.mp4?token=secret"`,
		"/proxy/segment-000.ts?token=secret",
		"/proxy/variants/low/index.m3u8",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rewritten manifest missing %q:\n%s", want, out)
		}
	}
}

func TestRelativePathResolvesAgainstSourceManifest(t *testing.T) {
	got, ok := RelativePath("../seg/000.ts?x=1", "variants/low/index.m3u8")
	if !ok {
		t.Fatalf("relative path was rejected")
	}
	if got != "variants/seg/000.ts?x=1" {
		t.Fatalf("relative path=%q", got)
	}
}

func TestRelativeReference(t *testing.T) {
	cases := []struct {
		name    string
		baseDir string
		target  string
		want    string
	}{
		{"root base returns target", "", "session/a/base/index.m3u8", "session/a/base/index.m3u8"},
		{"dot base treated as root", ".", "proxy/seg.ts", "proxy/seg.ts"},
		{"sibling file in same dir", "session/a/base", "session/a/base/00000.ts", "00000.ts"},
		{"sibling directory walks up", "proxy/variants/low", "proxy/variants/seg/seg.ts", "../seg/seg.ts"},
		{"leading slashes trimmed", "/proxy/a/", "/proxy/a/b/c.ts", "b/c.ts"},
		{"query preserved on last segment", "proxy/a", "proxy/a/b.ts?seg=1", "b.ts?seg=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RelativeReference(tc.baseDir, tc.target); got != tc.want {
				t.Fatalf("RelativeReference(%q, %q)=%q, want %q", tc.baseDir, tc.target, got, tc.want)
			}
		})
	}
}

func TestRelativePathRejectsUnsafePaths(t *testing.T) {
	for _, raw := range []string{"", "../secret.ts", "http://example.test/seg.ts", `bad\path.ts`} {
		if got, ok := RelativePath(raw, ""); ok {
			t.Fatalf("RelativePath(%q)=%q, want reject", raw, got)
		}
	}
}
