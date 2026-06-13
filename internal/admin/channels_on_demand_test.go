package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

// Creating an on-demand channel persists prefill_mode and, crucially, does NOT
// eagerly queue packages — that deferral is the entire point of on-demand.
func TestCreateChannelOnDemandDoesNotEagerlyQueue(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1800000)

	res := postCreateChannel(t, app, `{
		"displayName": "OnDem",
		"packageProfile": "h264-main-1080p",
		"prefillMode": "on_demand",
		"mediaIds": ["show1"]
	}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}

	ch, err := db.ChannelByID(context.Background(), conn, "ondem")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if ch == nil || ch.PrefillMode != "on_demand" {
		t.Fatalf("channel prefill_mode = %+v, want on_demand", ch)
	}

	var pkgCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM media_packages`).Scan(&pkgCount); err != nil {
		t.Fatalf("count packages: %v", err)
	}
	if pkgCount != 0 {
		t.Fatalf("on-demand create must not eagerly queue packages, got %d", pkgCount)
	}

	var body struct {
		Queued []string `json:"queued"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Queued) != 0 {
		t.Fatalf("queued=%v, want empty for on-demand", body.Queued)
	}
}

func TestCreateChannelRejectsInvalidPrefillMode(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1800000)

	res := postCreateChannel(t, app, `{
		"displayName": "Bad",
		"packageProfile": "h264-main-1080p",
		"prefillMode": "sometimes",
		"mediaIds": ["show1"]
	}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", res.Code)
	}
	if code := errorCode(t, res); code != "invalid_prefill_mode" {
		t.Fatalf("error code=%q, want invalid_prefill_mode", code)
	}
}
