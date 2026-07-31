package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/tckrcr/linearcast/internal/playback"
)

func composeRoutes(
	playbackHandler http.Handler,
	adminHandler http.Handler,
	readiness playback.ReadinessChecker,
	status playback.StatusProvider,
) http.Handler {
	service := serviceEndpoints{readiness: readiness, status: status}
	public := http.NewServeMux()
	public.HandleFunc("GET /healthz", service.handleHealth)
	public.HandleFunc("GET /readyz", service.handleReady)
	public.HandleFunc("GET /status", service.handleStatus)
	public.Handle("GET /metrics", promhttp.Handler())
	public.Handle("/channels/", playbackHandler)

	composed := http.NewServeMux()
	composed.Handle("/api/", adminHandler)
	composed.Handle("/", requestLogMiddleware(public))
	return composed
}

// requestLogMiddleware logs every HTTP request with method, path, status, and
// duration as structured JSON fields for Loki.
func requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		crw := &captureResponse{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(crw, r)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", crw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type captureResponse struct {
	http.ResponseWriter
	status int
}

func (c *captureResponse) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}
