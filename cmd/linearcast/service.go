package main

import (
	"encoding/json"
	"net/http"

	"github.com/tckrcr/linearcast/internal/playback"
)

type serviceEndpoints struct {
	readiness playback.ReadinessChecker
	status    playback.StatusProvider
}

func (s serviceEndpoints) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s serviceEndpoints) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.readiness.CheckPlaybackReadiness(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ready\n"))
}

func (s serviceEndpoints) handleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.status.PlaybackStatus(r.Context())
	if err != nil {
		http.Error(w, "playback status unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(status)
}
