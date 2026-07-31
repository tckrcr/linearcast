package playback

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerOwnsChannelRoutesOnly(t *testing.T) {
	handler := (&Runtime{}).Handler()

	restart := httptest.NewRecorder()
	handler.ServeHTTP(restart, httptest.NewRequest(http.MethodPost, "/channels/ch/ondemand/restart", nil))
	if restart.Code != http.StatusNoContent {
		t.Fatalf("restart status=%d, want %d", restart.Code, http.StatusNoContent)
	}

	for _, path := range []string{"/healthz", "/readyz", "/status", "/metrics", "/api/test"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s status=%d, want %d", path, response.Code, http.StatusNotFound)
			}
		})
	}
}
