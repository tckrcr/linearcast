package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestUIAPIContract(t *testing.T) {
	app, conn := testAdminApp(t)
	app.now = func() time.Time { return time.UnixMilli(6000).UTC() }
	insertMedia(t, conn, "contract-media", 18000)

	loader := openapi3.NewLoader()
	contract, err := loader.LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("load UI API contract: %v", err)
	}
	if err := contract.Validate(context.Background()); err != nil {
		t.Fatalf("validate UI API contract: %v", err)
	}

	tests := []struct {
		name         string
		method       string
		contractPath string
		requestPath  string
		body         string
		wantStatus   int
	}{
		{"auth status", http.MethodGet, "/api/auth/status", "/api/auth/status", "", http.StatusOK},
		{"playable sources", http.MethodGet, "/api/playable-sources", "/api/playable-sources", "", http.StatusOK},
		{"guide", http.MethodGet, "/api/guide", "/api/guide?from=0&hours=6", "", http.StatusOK},
		{"guide error", http.MethodGet, "/api/guide", "/api/guide?hours=invalid", "", http.StatusBadRequest},
		{"subtitle settings", http.MethodGet, "/api/subtitle-settings", "/api/subtitle-settings", "", http.StatusOK},
		{"admin now", http.MethodGet, "/api/now", "/api/now", "", http.StatusOK},
		{"channel list", http.MethodGet, "/api/channels", "/api/channels", "", http.StatusOK},
		{"media source status", http.MethodGet, "/api/admin/media-sources/status", "/api/admin/media-sources/status", "", http.StatusOK},
		{"package profiles", http.MethodGet, "/api/media/package-profiles", "/api/media/package-profiles", "", http.StatusOK},
		{"package candidates", http.MethodGet, "/api/media/package-candidates", "/api/media/package-candidates?profile=h264-1080p-8mbps", "", http.StatusOK},
		{"shows", http.MethodGet, "/api/media/shows", "/api/media/shows", "", http.StatusOK},
		{"movies", http.MethodGet, "/api/media/movies", "/api/media/movies", "", http.StatusOK},
		{"albums", http.MethodGet, "/api/media/albums", "/api/media/albums", "", http.StatusOK},
		{"media group", http.MethodGet, "/api/media/by-group", "/api/media/by-group?group=missing", "", http.StatusOK},
		{"filler candidates", http.MethodGet, "/api/filler-assets/candidates", "/api/filler-assets/candidates?profile=h264-1080p-8mbps", "", http.StatusOK},
		{"create channel error", http.MethodPost, "/api/channels", "/api/channels", `{}`, http.StatusBadRequest},
		{
			"create channel",
			http.MethodPost,
			"/api/channels",
			"/api/channels",
			`{"displayName":"Contract Channel","packageProfile":"h264-1080p-8mbps","mediaIds":["contract-media"],"prefillMode":"on_demand"}`,
			http.StatusCreated,
		},
	}

	handler := app.Handler()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.requestPath, strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d; body=%s", res.Code, tt.wantStatus, res.Body.String())
			}
			assertResponseMatchesUIContract(t, contract, tt.method, tt.contractPath, res)
		})
	}
}

func assertResponseMatchesUIContract(
	t *testing.T,
	contract *openapi3.T,
	method string,
	path string,
	res *httptest.ResponseRecorder,
) {
	t.Helper()
	pathItem := contract.Paths.Find(path)
	if pathItem == nil {
		t.Fatalf("contract path %s not found", path)
	}
	operation := pathItem.GetOperation(method)
	if operation == nil {
		t.Fatalf("contract operation %s %s not found", method, path)
	}
	response := operation.Responses.Status(res.Code)
	if response == nil || response.Value == nil {
		t.Fatalf("contract response %s %s status %d not found", method, path, res.Code)
	}
	mediaType := response.Value.Content.Get("application/json")
	if mediaType == nil || mediaType.Schema == nil || mediaType.Schema.Value == nil {
		t.Fatalf("contract JSON schema %s %s status %d not found", method, path, res.Code)
	}
	if got := res.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type=%q, want application/json", got)
	}
	var value any
	if err := json.Unmarshal(res.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if err := mediaType.Schema.Value.VisitJSON(value); err != nil {
		t.Fatalf("response violates UI API contract: %v\nbody=%s", fmt.Errorf("%s %s: %w", method, path, err), res.Body.String())
	}
}
