package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestScheduleServiceUpsertEntryReturnsDomainErrorsDirectly(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)

	_, err := app.schedule.UpsertEntry(context.Background(), "ch", scheduleEntryWriteRequest{
		MediaID: "m2",
		StartMs: 18000,
	})
	if !errors.Is(err, errMediaNotInChannel) {
		t.Fatalf("err=%v, want errMediaNotInChannel", err)
	}

	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m2", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}
	_, err = app.schedule.UpsertEntry(context.Background(), "ch", scheduleEntryWriteRequest{
		MediaID: "m2",
		StartMs: 18000,
	})
	if !errors.Is(err, errPackageNotReady) {
		t.Fatalf("err=%v, want errPackageNotReady", err)
	}
}

func TestScheduleServiceErrorMapsSentinels(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "media not in channel",
			err:        errMediaNotInChannel,
			wantStatus: http.StatusBadRequest,
			wantCode:   "media_not_in_channel",
		},
		{
			name:       "package not ready",
			err:        errPackageNotReady,
			wantStatus: http.StatusConflict,
			wantCode:   "package_not_ready",
		},
		{
			name:       "stale entry",
			err:        errEntryNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   "not_found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := httptest.NewRecorder()

			scheduleServiceError(res, tt.err)

			if res.Code != tt.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", res.Code, res.Body.String(), tt.wantStatus)
			}
			var body map[string]string
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["error"] != tt.wantCode {
				t.Fatalf("error=%q, want %q", body["error"], tt.wantCode)
			}
			if body["hint"] == "" {
				t.Fatalf("hint is empty for %s: %+v", tt.name, body)
			}
		})
	}
}

func TestWithImmediateTxRollsBackOnError(t *testing.T) {
	_, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	err := db.WithImmediateTx(context.Background(), conn, func(tx db.Execer) error {
		if _, clearErr := db.ClearScheduleAfter(context.Background(), tx, "ch", 0); clearErr != nil {
			return clearErr
		}
		return scheduleServiceTestErr("after clear")
	})
	if !errors.Is(err, scheduleServiceTestErr("after clear")) {
		t.Fatalf("err=%v, want after clear", err)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'ch'`, 1)
}

type scheduleServiceTestErr string

func (e scheduleServiceTestErr) Error() string { return string(e) }
