package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tckrcr/linearcast/internal/playback"
)

func TestRoutesComposeAdminAndPlaybackOnOneHandler(t *testing.T) {
	adminHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/test" {
			t.Fatalf("admin path=%q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	playbackHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/channels/test" {
			t.Fatalf("playback path=%q", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
	})
	handler := composeRoutes(
		playbackHandler,
		adminHandler,
		readinessCheckerFunc(func(context.Context) error { return nil }),
		statusProviderFunc(func(context.Context) (playback.Status, error) { return playback.Status{}, nil }),
	)

	adminResponse := httptest.NewRecorder()
	handler.ServeHTTP(adminResponse, httptest.NewRequest(http.MethodGet, "/api/test", nil))
	if adminResponse.Code != http.StatusNoContent {
		t.Fatalf("admin status=%d", adminResponse.Code)
	}

	playbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(playbackResponse, httptest.NewRequest(http.MethodGet, "/channels/test", nil))
	if playbackResponse.Code != http.StatusAccepted {
		t.Fatalf("playback status=%d", playbackResponse.Code)
	}

	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if healthResponse.Code != http.StatusOK || healthResponse.Body.String() != "ok\n" {
		t.Fatalf("health status=%d body=%q", healthResponse.Code, healthResponse.Body.String())
	}

	wrongMethod := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status=%d, want %d", wrongMethod.Code, http.StatusMethodNotAllowed)
	}
}

func TestServiceEndpointsAdaptPlaybackContracts(t *testing.T) {
	service := serviceEndpoints{
		readiness: readinessCheckerFunc(func(context.Context) error {
			return errors.New("channel ch packaged manifest not ready")
		}),
		status: statusProviderFunc(func(context.Context) (playback.Status, error) {
			return playback.Status{NowMs: 42, StartedAt: "start"}, nil
		}),
	}

	readyResponse := httptest.NewRecorder()
	service.handleReady(readyResponse, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readyResponse.Code != http.StatusServiceUnavailable ||
		readyResponse.Body.String() != "channel ch packaged manifest not ready\n" {
		t.Fatalf("ready status=%d body=%q", readyResponse.Code, readyResponse.Body.String())
	}

	service.readiness = readinessCheckerFunc(func(context.Context) error { return nil })
	readyResponse = httptest.NewRecorder()
	service.handleReady(readyResponse, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readyResponse.Code != http.StatusOK || readyResponse.Body.String() != "ready\n" {
		t.Fatalf("ready status=%d body=%q", readyResponse.Code, readyResponse.Body.String())
	}

	statusResponse := httptest.NewRecorder()
	service.handleStatus(statusResponse, httptest.NewRequest(http.MethodGet, "/status", nil))
	if statusResponse.Code != http.StatusOK ||
		statusResponse.Header().Get("Cache-Control") != "no-cache" ||
		statusResponse.Body.String() != "{\"nowMs\":42,\"startedAt\":\"start\",\"channels\":null}\n" {
		t.Fatalf("status=%d headers=%v body=%q", statusResponse.Code, statusResponse.Header(), statusResponse.Body.String())
	}
}

type readinessCheckerFunc func(context.Context) error

func (f readinessCheckerFunc) CheckPlaybackReadiness(ctx context.Context) error {
	return f(ctx)
}

type statusProviderFunc func(context.Context) (playback.Status, error)

func (f statusProviderFunc) PlaybackStatus(ctx context.Context) (playback.Status, error) {
	return f(ctx)
}
