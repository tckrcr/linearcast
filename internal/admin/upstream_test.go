package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchUpstreamStatusCachesSuccessfulResponse(t *testing.T) {
	var requests int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		if r.URL.Path != "/status" {
			t.Fatalf("path=%s, want /status", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"currentSegmentIndex": 7,
			"startedAt": "2026-05-26T00:00:00Z",
			"cacheRoot": "/cache",
			"workerCount": 2,
			"channels": [{
				"id": "ch",
				"format": "fmp4",
				"hasSchedule": true,
				"cacheSize": 4,
				"lookaheadDepthSegments": 3,
				"latestGeneratedIndex": 9
			}]
		}`))
	}))
	defer upstream.Close()

	app, _ := testAdminApp(t)
	now := time.Unix(0, 0).UTC()
	app.now = func() time.Time { return now }
	app.upstreamURL = upstream.URL
	app.httpClient = upstream.Client()

	summary, cacheByChannel := app.fetchUpstreamStatus(context.Background())
	if summary == nil || !summary.Available || summary.WorkerCount != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if cacheByChannel["ch"].LookaheadDepthSeconds == nil || *cacheByChannel["ch"].LookaheadDepthSeconds != 18 {
		t.Fatalf("unexpected channel cache: %+v", cacheByChannel["ch"])
	}

	summary, _ = app.fetchUpstreamStatus(context.Background())
	if summary == nil || !summary.Available {
		t.Fatalf("second summary unavailable: %+v", summary)
	}
	if got := atomic.LoadInt64(&requests); got != 1 {
		t.Fatalf("requests=%d, want cached single request", got)
	}

	now = now.Add(upstreamStatusSuccessTTL + time.Millisecond)
	_, _ = app.fetchUpstreamStatus(context.Background())
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("requests=%d, want refetch after ttl", got)
	}
}

func TestFetchUpstreamStatusBacksOffAfterFailure(t *testing.T) {
	var requests int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		http.Error(w, "offline", http.StatusBadGateway)
	}))
	defer upstream.Close()

	app, _ := testAdminApp(t)
	now := time.Unix(0, 0).UTC()
	app.now = func() time.Time { return now }
	app.upstreamURL = upstream.URL
	app.httpClient = upstream.Client()

	summary, _ := app.fetchUpstreamStatus(context.Background())
	if summary == nil || summary.Available || summary.Error != "status 502" {
		t.Fatalf("unexpected first failure summary: %+v", summary)
	}
	if got := atomic.LoadInt64(&requests); got != 1 {
		t.Fatalf("requests=%d, want first probe", got)
	}

	now = now.Add(upstreamStatusFailureBase - time.Millisecond)
	summary, _ = app.fetchUpstreamStatus(context.Background())
	if summary == nil || summary.Available || summary.Error != "status 502" {
		t.Fatalf("unexpected cooldown summary: %+v", summary)
	}
	if got := atomic.LoadInt64(&requests); got != 1 {
		t.Fatalf("requests=%d, want no probe during cooldown", got)
	}

	now = now.Add(2 * time.Millisecond)
	_, _ = app.fetchUpstreamStatus(context.Background())
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("requests=%d, want retry after cooldown", got)
	}

	now = now.Add((2 * upstreamStatusFailureBase) - time.Millisecond)
	_, _ = app.fetchUpstreamStatus(context.Background())
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("requests=%d, want second failure cooldown to double", got)
	}
}

func TestFetchUpstreamStatusReturnsStaleCacheDuringFailure(t *testing.T) {
	var fail atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "offline", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"currentSegmentIndex": 7,
			"channels": [{
				"id": "ch",
				"format": "fmp4",
				"cacheSize": 4,
				"lookaheadDepthSegments": 3
			}]
		}`))
	}))
	defer upstream.Close()

	app, _ := testAdminApp(t)
	now := time.Unix(0, 0).UTC()
	app.now = func() time.Time { return now }
	app.upstreamURL = upstream.URL
	app.httpClient = upstream.Client()

	summary, cacheByChannel := app.fetchUpstreamStatus(context.Background())
	if summary == nil || !summary.Available || cacheByChannel["ch"].CacheSize != 4 {
		t.Fatalf("unexpected initial status summary=%+v cache=%+v", summary, cacheByChannel)
	}

	fail.Store(true)
	now = now.Add(upstreamStatusSuccessTTL + time.Millisecond)
	summary, cacheByChannel = app.fetchUpstreamStatus(context.Background())
	if summary == nil || summary.Available || summary.Error != "status 502" {
		t.Fatalf("unexpected failure summary: %+v", summary)
	}
	if cacheByChannel["ch"].CacheSize != 4 {
		t.Fatalf("stale cache missing after failure: %+v", cacheByChannel)
	}
}
