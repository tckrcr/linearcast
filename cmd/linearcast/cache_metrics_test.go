package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirSizeBytesSumsRegularFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "init.mp4"), 100)
	mustWrite(t, filepath.Join(root, "media", "profile", "seg0.m4s"), 250)
	mustWrite(t, filepath.Join(root, "media", "profile", "seg1.m4s"), 150)

	got, err := dirSizeBytes(root)
	if err != nil {
		t.Fatalf("dirSizeBytes: %v", err)
	}
	if want := int64(500); got != want {
		t.Fatalf("dirSizeBytes=%d, want %d", got, want)
	}
}

func TestDirSizeBytesMissingRootIsZero(t *testing.T) {
	got, err := dirSizeBytes(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("dirSizeBytes on missing root: %v", err)
	}
	if got != 0 {
		t.Fatalf("dirSizeBytes=%d, want 0 for missing root", got)
	}
}

func mustWrite(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
