package linearcastlog

import (
	"bytes"
	"io"
	"log"
	"log/slog"
	"os"
)

// SetupJSON configures the process for structured JSON logging on stderr.
// It creates a JSON slog handler as the default, and redirects the standard
// library log package through it so existing log.Printf/Fatal calls still
// appear as JSON (level=INFO, single msg field). Call once at startup.
func SetupJSON() {
	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(h))
	log.SetFlags(0)
	log.SetOutput(&writer{logger: slog.Default()})
}

// writer adapts the standard library log package to output through slog,
// producing JSON like {"msg":"original log line"} for unconverted call sites.
type writer struct {
	logger *slog.Logger
}

func (w *writer) Write(p []byte) (int, error) {
	w.logger.Info(string(bytes.TrimRight(p, "\n\r")))
	return len(p), nil
}

// Discard returns an io.WriteCloser that drops all writes — useful when a
// struct field requires a *log.Logger but the caller already set up slog
// and doesn't need the legacy path.
func Discard() io.WriteCloser { return nopWriter{} }

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriter) Close() error                 { return nil }
