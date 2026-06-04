package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPlexTokenSettingHelpers(t *testing.T) {
	conn, err := OpenReadWrite(filepath.Join(t.TempDir(), "linearcast.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	exists, err := PlexTokenSettingExists(context.Background(), conn)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if exists {
		t.Fatal("plex token setting should not exist initially")
	}
	if token, err := GetPlexToken(context.Background(), conn); err != nil || token != "" {
		t.Fatalf("initial token=%q err=%v, want empty nil", token, err)
	}

	if err := SeedPlexToken(context.Background(), conn, " env-token "); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if token, err := GetPlexToken(context.Background(), conn); err != nil || token != "env-token" {
		t.Fatalf("seeded token=%q err=%v, want env-token nil", token, err)
	}
	if err := SeedPlexToken(context.Background(), conn, "replacement"); err != nil {
		t.Fatalf("seed replacement: %v", err)
	}
	if token, err := GetPlexToken(context.Background(), conn); err != nil || token != "env-token" {
		t.Fatalf("seed overwrote token=%q err=%v", token, err)
	}

	if err := ClearPlexToken(context.Background(), conn); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if token, err := GetPlexToken(context.Background(), conn); err != nil || token != "" {
		t.Fatalf("cleared token=%q err=%v, want empty nil", token, err)
	}
	exists, err = PlexTokenSettingExists(context.Background(), conn)
	if err != nil {
		t.Fatalf("exists after clear: %v", err)
	}
	if !exists {
		t.Fatal("clear should preserve setting row")
	}
	if err := SeedPlexToken(context.Background(), conn, "env-again"); err != nil {
		t.Fatalf("seed after clear: %v", err)
	}
	if token, err := GetPlexToken(context.Background(), conn); err != nil || token != "" {
		t.Fatalf("seed after clear token=%q err=%v, want empty nil", token, err)
	}
}

func TestJellyfinSettingHelpers(t *testing.T) {
	conn, err := OpenReadWrite(filepath.Join(t.TempDir(), "linearcast.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if got, err := GetJellyfinURL(context.Background(), conn); err != nil || got != "" {
		t.Fatalf("initial url=%q err=%v, want empty nil", got, err)
	}
	if got, err := GetJellyfinAPIKey(context.Background(), conn); err != nil || got != "" {
		t.Fatalf("initial key=%q err=%v, want empty nil", got, err)
	}

	if err := SetJellyfinURL(context.Background(), conn, " http://jellyfin.example/ "); err != nil {
		t.Fatalf("set url: %v", err)
	}
	if err := SetJellyfinAPIKey(context.Background(), conn, " secret "); err != nil {
		t.Fatalf("set key: %v", err)
	}
	if got, err := GetJellyfinURL(context.Background(), conn); err != nil || got != "http://jellyfin.example" {
		t.Fatalf("url=%q err=%v, want normalized url nil", got, err)
	}
	if got, err := GetJellyfinAPIKey(context.Background(), conn); err != nil || got != "secret" {
		t.Fatalf("key=%q err=%v, want trimmed key nil", got, err)
	}

	if err := ClearJellyfinAPIKey(context.Background(), conn); err != nil {
		t.Fatalf("clear key: %v", err)
	}
	if got, err := GetJellyfinAPIKey(context.Background(), conn); err != nil || got != "" {
		t.Fatalf("cleared key=%q err=%v, want empty nil", got, err)
	}
	if got, err := GetJellyfinURL(context.Background(), conn); err != nil || got != "http://jellyfin.example" {
		t.Fatalf("url after clear=%q err=%v, want preserved nil", got, err)
	}
}

func TestSchedulerTunables(t *testing.T) {
	conn, err := OpenReadWrite(filepath.Join(t.TempDir(), "linearcast.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	// Fresh DB: schema seeds the defaults.
	got, err := GetSchedulerTunables(context.Background(), conn)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := SchedulerTunables{HorizonHours: 48, LowWaterHours: 24, TickSeconds: 300}
	if got != want {
		t.Fatalf("defaults=%+v want=%+v", got, want)
	}

	if err := SetSchedulerTunables(context.Background(), conn, SchedulerTunables{HorizonHours: 72, LowWaterHours: 36, TickSeconds: 600}); err != nil {
		t.Fatalf("set valid: %v", err)
	}
	got, err = GetSchedulerTunables(context.Background(), conn)
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if got != (SchedulerTunables{HorizonHours: 72, LowWaterHours: 36, TickSeconds: 600}) {
		t.Fatalf("after set=%+v", got)
	}

	// low_water must be strictly less than horizon.
	if err := SetSchedulerTunables(context.Background(), conn, SchedulerTunables{HorizonHours: 24, LowWaterHours: 24, TickSeconds: 300}); err == nil {
		t.Fatal("expected error when lowWater == horizon")
	}
	if err := SetSchedulerTunables(context.Background(), conn, SchedulerTunables{HorizonHours: 24, LowWaterHours: 48, TickSeconds: 300}); err == nil {
		t.Fatal("expected error when lowWater > horizon")
	}

	// Each field must be positive.
	if err := SetSchedulerTunables(context.Background(), conn, SchedulerTunables{HorizonHours: 0, LowWaterHours: 24, TickSeconds: 300}); err == nil {
		t.Fatal("expected error when horizon=0")
	}
	if err := SetSchedulerTunables(context.Background(), conn, SchedulerTunables{HorizonHours: 48, LowWaterHours: 0, TickSeconds: 300}); err == nil {
		t.Fatal("expected error when lowWater=0")
	}
	if err := SetSchedulerTunables(context.Background(), conn, SchedulerTunables{HorizonHours: 48, LowWaterHours: 24, TickSeconds: -1}); err == nil {
		t.Fatal("expected error when tickSeconds<=0")
	}

	// Rejected writes should not partially commit; previous good values stay.
	got, err = GetSchedulerTunables(context.Background(), conn)
	if err != nil {
		t.Fatalf("get after failed sets: %v", err)
	}
	if got != (SchedulerTunables{HorizonHours: 72, LowWaterHours: 36, TickSeconds: 600}) {
		t.Fatalf("after failed sets=%+v, want last good values", got)
	}

	// Missing rows fall back to defaults (simulate an install that pre-dates
	// the migration but somehow lost the seeded row).
	if _, err := conn.Exec(`DELETE FROM settings WHERE key LIKE 'scheduler_%'`); err != nil {
		t.Fatalf("delete rows: %v", err)
	}
	got, err = GetSchedulerTunables(context.Background(), conn)
	if err != nil {
		t.Fatalf("get with no rows: %v", err)
	}
	if got != want {
		t.Fatalf("fallback=%+v want=%+v", got, want)
	}
}
