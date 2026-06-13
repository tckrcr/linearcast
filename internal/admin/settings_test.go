package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleSubtitleSettingsUpdateStoresSettings(t *testing.T) {
	app, conn := testAdminApp(t)

	req := httptest.NewRequest(http.MethodPut, "/api/subtitle-settings", strings.NewReader(`{
		"subtitleAutoEnable": true,
		"subtitleLanguagePreference": ["ENG", "spa", "eng"]
	}`))
	res := httptest.NewRecorder()

	app.handleSubtitleSettingsUpdate(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body subtitleSettingsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.AutoEnable || len(body.LanguagePreference) != 2 ||
		body.LanguagePreference[0] != "eng" || body.LanguagePreference[1] != "spa" {
		t.Fatalf("unexpected response: %+v", body)
	}
	auto, err := db.GetSubtitleAutoEnable(context.Background(), conn)
	if err != nil {
		t.Fatalf("read auto enable: %v", err)
	}
	langs, err := db.GetSubtitleLanguagePreference(context.Background(), conn)
	if err != nil {
		t.Fatalf("read language preference: %v", err)
	}
	if !auto || len(langs) != 2 || langs[0] != "eng" || langs[1] != "spa" {
		t.Fatalf("settings not stored auto=%v langs=%v", auto, langs)
	}
}

func TestHandleSchedulerTunables(t *testing.T) {
	app, conn := testAdminApp(t)

	// Defaults on fresh DB.
	req := httptest.NewRequest(http.MethodGet, "/api/admin/scheduler-tunables", nil)
	res := httptest.NewRecorder()
	app.handleSchedulerTunables(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", res.Code, res.Body.String())
	}
	var got db.SchedulerTunables
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	want := db.SchedulerTunables{HorizonHours: 24, LowWaterHours: 23, TickSeconds: 300}
	if got != want {
		t.Fatalf("defaults=%+v want=%+v", got, want)
	}

	// Valid update.
	body := `{"horizonHours":72,"lowWaterHours":36,"tickSeconds":600}`
	req = httptest.NewRequest(http.MethodPut, "/api/admin/scheduler-tunables", strings.NewReader(body))
	res = httptest.NewRecorder()
	app.handleSchedulerTunablesUpdate(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", res.Code, res.Body.String())
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode put: %v", err)
	}
	if got != (db.SchedulerTunables{HorizonHours: 72, LowWaterHours: 36, TickSeconds: 600}) {
		t.Fatalf("put response=%+v", got)
	}

	stored, err := db.GetSchedulerTunables(context.Background(), conn)
	if err != nil {
		t.Fatalf("read stored: %v", err)
	}
	if stored != got {
		t.Fatalf("stored=%+v want=%+v", stored, got)
	}
}

func TestHandleSchedulerTunablesUpdateRejectsInvalid(t *testing.T) {
	app, conn := testAdminApp(t)
	if err := db.SetSchedulerTunables(context.Background(), conn, db.SchedulerTunables{HorizonHours: 72, LowWaterHours: 36, TickSeconds: 600}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []string{
		`{"horizonHours":24,"lowWaterHours":24,"tickSeconds":300}`, // lowWater == horizon
		`{"horizonHours":24,"lowWaterHours":48,"tickSeconds":300}`, // lowWater > horizon
		`{"horizonHours":0,"lowWaterHours":24,"tickSeconds":300}`,  // horizon <= 0
		`{"horizonHours":48,"lowWaterHours":0,"tickSeconds":300}`,  // lowWater <= 0
		`{"horizonHours":48,"lowWaterHours":24,"tickSeconds":-1}`,  // tick <= 0
	}
	for i, body := range cases {
		req := httptest.NewRequest(http.MethodPut, "/api/admin/scheduler-tunables", strings.NewReader(body))
		res := httptest.NewRecorder()
		app.handleSchedulerTunablesUpdate(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("case %d: status=%d body=%s, want bad request", i, res.Code, res.Body.String())
		}
		stored, err := db.GetSchedulerTunables(context.Background(), conn)
		if err != nil {
			t.Fatalf("case %d: read stored: %v", i, err)
		}
		if stored != (db.SchedulerTunables{HorizonHours: 72, LowWaterHours: 36, TickSeconds: 600}) {
			t.Fatalf("case %d: invalid request mutated settings: %+v", i, stored)
		}
	}
}

func TestHandleSubtitleSettingsUpdateRejectsInvalidLanguage(t *testing.T) {
	app, conn := testAdminApp(t)

	req := httptest.NewRequest(http.MethodPut, "/api/subtitle-settings", strings.NewReader(`{
		"subtitleAutoEnable": true,
		"subtitleLanguagePreference": ["english"]
	}`))
	res := httptest.NewRecorder()

	app.handleSubtitleSettingsUpdate(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want bad request", res.Code, res.Body.String())
	}
	langs, err := db.GetSubtitleLanguagePreference(context.Background(), conn)
	if err != nil {
		t.Fatalf("read language preference: %v", err)
	}
	if len(langs) != 1 || langs[0] != "eng" {
		t.Fatalf("invalid request mutated settings: %v", langs)
	}
}

func TestHandleEncoderSweeperSettings(t *testing.T) {
	app, conn := testAdminApp(t)

	// Defaults on fresh DB.
	req := httptest.NewRequest(http.MethodGet, "/api/admin/encoder-sweeper-settings", nil)
	res := httptest.NewRecorder()
	app.handleEncoderSweeperSettings(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", res.Code, res.Body.String())
	}
	var got db.EncoderSweeperSettings
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	want := db.EncoderSweeperSettings{SweepIntervalSeconds: 30, MaxAttempts: 5}
	if got != want {
		t.Fatalf("defaults=%+v want=%+v", got, want)
	}

	// Valid update.
	body := `{"sweepIntervalSeconds":60,"maxAttempts":10}`
	req = httptest.NewRequest(http.MethodPut, "/api/admin/encoder-sweeper-settings", strings.NewReader(body))
	res = httptest.NewRecorder()
	app.handleEncoderSweeperSettingsUpdate(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", res.Code, res.Body.String())
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode put: %v", err)
	}
	if got != (db.EncoderSweeperSettings{SweepIntervalSeconds: 60, MaxAttempts: 10}) {
		t.Fatalf("put response=%+v", got)
	}

	stored, err := db.GetEncoderSweeperSettings(context.Background(), conn)
	if err != nil {
		t.Fatalf("read stored: %v", err)
	}
	if stored != got {
		t.Fatalf("stored=%+v want=%+v", stored, got)
	}
}

func TestHandleEncoderSweeperSettingsUpdateRejectsInvalid(t *testing.T) {
	app, conn := testAdminApp(t)
	if err := db.SetEncoderSweeperSettings(context.Background(), conn, db.EncoderSweeperSettings{SweepIntervalSeconds: 60, MaxAttempts: 10}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []string{
		`{"sweepIntervalSeconds":0,"maxAttempts":10}`,  // interval <= 0
		`{"sweepIntervalSeconds":60,"maxAttempts":0}`,  // maxAttempts <= 0
		`{"sweepIntervalSeconds":-1,"maxAttempts":10}`, // interval < 0
	}
	for i, body := range cases {
		req := httptest.NewRequest(http.MethodPut, "/api/admin/encoder-sweeper-settings", strings.NewReader(body))
		res := httptest.NewRecorder()
		app.handleEncoderSweeperSettingsUpdate(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("case %d: status=%d body=%s, want bad request", i, res.Code, res.Body.String())
		}
		stored, err := db.GetEncoderSweeperSettings(context.Background(), conn)
		if err != nil {
			t.Fatalf("case %d: read stored: %v", i, err)
		}
		if stored != (db.EncoderSweeperSettings{SweepIntervalSeconds: 60, MaxAttempts: 10}) {
			t.Fatalf("case %d: invalid request mutated settings: %+v", i, stored)
		}
	}
}
