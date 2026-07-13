package admin

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// encoderDownloadEntry describes one cross-compiled encoder binary the admin
// server is willing to serve. Filenames are fixed by the build pipeline
// (the Dockerfile build stage) so the UI can list them.
type encoderDownloadEntry struct {
	// Platform is the stable URL slug (e.g. "darwin-arm64"). The download
	// handler resolves this to the on-disk filename.
	Platform string `json:"platform"`
	// Label is the human-friendly description shown in the UI.
	Label string `json:"label"`
	// Filename is the served-as filename. Includes ".exe" for Windows.
	Filename string `json:"filename"`
	// OS is the broad family the UI groups instructions by.
	OS string `json:"os"`
}

// encoderDownloads enumerates the platforms we attempt to publish. Build
// pipelines may not produce every binary on every host; the handler filters
// to what actually exists at request time.
var encoderDownloads = []encoderDownloadEntry{
	{Platform: "darwin-arm64", Label: "macOS (Apple Silicon)", Filename: "linearcast-encoder-darwin-arm64", OS: "darwin"},
	{Platform: "darwin-amd64", Label: "macOS (Intel)", Filename: "linearcast-encoder-darwin-amd64", OS: "darwin"},
	{Platform: "windows-amd64", Label: "Windows (x86_64)", Filename: "linearcast-encoder-windows-amd64.exe", OS: "windows"},
	{Platform: "linux-amd64", Label: "Linux (x86_64)", Filename: "linearcast-encoder-linux-amd64", OS: "linux"},
	{Platform: "linux-arm64", Label: "Linux (ARM64)", Filename: "linearcast-encoder-linux-arm64", OS: "linux"},
}

type encoderDownloadsResponse struct {
	Available []encoderDownloadEntry `json:"available"`
	// DistConfigured reports whether the admin process has a dist dir wired up
	// at all. When false the UI surfaces a configuration hint instead of an
	// empty list.
	DistConfigured bool `json:"distConfigured"`
}

func (a *App) handleEncoderDownloads(w http.ResponseWriter, r *http.Request) {
	out := encoderDownloadsResponse{
		Available:      []encoderDownloadEntry{},
		DistConfigured: a.encoderDistDir != "",
	}
	if a.encoderDistDir == "" {
		writeJSON(w, out)
		return
	}
	for _, entry := range encoderDownloads {
		path := filepath.Join(a.encoderDistDir, entry.Filename)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		out.Available = append(out.Available, entry)
	}
	sort.SliceStable(out.Available, func(i, j int) bool {
		return out.Available[i].Platform < out.Available[j].Platform
	})
	writeJSON(w, out)
}

// handleEncoderDownload streams one of the cross-compiled encoder binaries.
// The platform path segment must match encoderDownloads[].Platform exactly —
// nothing else is treated as a valid file lookup, so traversal is impossible.
func (a *App) handleEncoderDownload(w http.ResponseWriter, r *http.Request) {
	if a.encoderDistDir == "" {
		writeError(w, http.StatusNotFound, "downloads_disabled", "encoder dist directory is not configured on the server")
		return
	}
	platform := strings.TrimSpace(r.PathValue("platform"))
	if platform == "" {
		writeError(w, http.StatusBadRequest, "missing_platform", "platform is required")
		return
	}
	var entry *encoderDownloadEntry
	for i := range encoderDownloads {
		if encoderDownloads[i].Platform == platform {
			entry = &encoderDownloads[i]
			break
		}
	}
	if entry == nil {
		writeError(w, http.StatusNotFound, "unknown_platform", "unsupported encoder platform")
		return
	}
	path := filepath.Join(a.encoderDistDir, entry.Filename)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "binary_missing", "binary for this platform is not built on this server")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+entry.Filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, path)
}
