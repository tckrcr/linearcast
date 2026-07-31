package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/tckrcr/linearcast/internal/playback"
)

type playbackStatusProviderFunc func(context.Context) (playback.Status, error)

func (f playbackStatusProviderFunc) PlaybackStatus(ctx context.Context) (playback.Status, error) {
	return f(ctx)
}

func TestFetchUpstreamStatusUsesPlaybackProvider(t *testing.T) {
	app, _ := testAdminApp(t)
	requests := 0
	app.playbackStatus = playbackStatusProviderFunc(func(context.Context) (playback.Status, error) {
		requests++
		return playback.Status{
			StartedAt: "2026-05-26T00:00:00Z",
			Channels: []playback.ChannelStatus{{
				ID:          "ch",
				HasSchedule: true,
			}},
		}, nil
	})

	summary, cacheByChannel := app.fetchUpstreamStatus(context.Background())
	if summary == nil || !summary.Available || summary.StartedAt != "2026-05-26T00:00:00Z" {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if !cacheByChannel["ch"].HasSchedule {
		t.Fatalf("unexpected channel status: %+v", cacheByChannel["ch"])
	}

	_, _ = app.fetchUpstreamStatus(context.Background())
	if requests != 2 {
		t.Fatalf("provider calls=%d, want one direct call per request", requests)
	}
}

func TestFetchUpstreamStatusReportsProviderFailure(t *testing.T) {
	app, _ := testAdminApp(t)
	app.playbackStatus = playbackStatusProviderFunc(func(context.Context) (playback.Status, error) {
		return playback.Status{}, errors.New("status unavailable")
	})

	summary, cacheByChannel := app.fetchUpstreamStatus(context.Background())
	if summary == nil || summary.Available || summary.Error != "status unavailable" {
		t.Fatalf("unexpected failure summary: %+v", summary)
	}
	if cacheByChannel != nil {
		t.Fatalf("cache=%+v, want nil", cacheByChannel)
	}
}

func TestFetchUpstreamStatusReportsMissingProvider(t *testing.T) {
	app, _ := testAdminApp(t)

	summary, _ := app.fetchUpstreamStatus(context.Background())
	if summary == nil || summary.Available || summary.Error == "" {
		t.Fatalf("unexpected missing-provider summary: %+v", summary)
	}
}
