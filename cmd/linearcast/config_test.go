package main

import (
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/playback"
)

func TestLoadStartupConfigRequiresDatabasePath(t *testing.T) {
	_, err := loadStartupConfig(linearcastTestEnv(map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "LINEARCAST_DB is required") {
		t.Fatalf("expected DB path error, got %v", err)
	}
}

func TestLoadStartupConfigReadsCacheDir(t *testing.T) {
	cfg, err := loadStartupConfig(linearcastTestEnv(map[string]string{
		"LINEARCAST_DB": "/tmp/linearcast.db",
		"CACHE_DIR":     " /tmp/cache ",
	}))
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if cfg.cacheDir != "/tmp/cache" {
		t.Fatalf("cacheDir=%q, want trimmed /tmp/cache", cfg.cacheDir)
	}
}

func TestLoadStartupConfigCacheDirOptional(t *testing.T) {
	cfg, err := loadStartupConfig(linearcastTestEnv(map[string]string{
		"LINEARCAST_DB": "/tmp/linearcast.db",
	}))
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if cfg.cacheDir != "" {
		t.Fatalf("cacheDir=%q, want empty when CACHE_DIR unset", cfg.cacheDir)
	}
}

func TestLoadStartupConfigOnDemandMaxConcurrent(t *testing.T) {
	cfg, err := loadStartupConfig(linearcastTestEnv(map[string]string{
		"LINEARCAST_DB": "/tmp/linearcast.db",
	}))
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if cfg.onDemandMaxConcurrent != defaultOnDemandMaxConcurrent {
		t.Fatalf("blank env: onDemandMaxConcurrent=%d, want default %d", cfg.onDemandMaxConcurrent, defaultOnDemandMaxConcurrent)
	}

	cfg, err = loadStartupConfig(linearcastTestEnv(map[string]string{
		"LINEARCAST_DB":                       "/tmp/linearcast.db",
		"LINEARCAST_ON_DEMAND_MAX_CONCURRENT": " 8 ",
	}))
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if cfg.onDemandMaxConcurrent != 8 {
		t.Fatalf("onDemandMaxConcurrent=%d, want 8", cfg.onDemandMaxConcurrent)
	}

	for _, raw := range []string{"0", "-1", "abc"} {
		if _, err := loadStartupConfig(linearcastTestEnv(map[string]string{
			"LINEARCAST_DB":                       "/tmp/linearcast.db",
			"LINEARCAST_ON_DEMAND_MAX_CONCURRENT": raw,
		})); err == nil || !strings.Contains(err.Error(), "LINEARCAST_ON_DEMAND_MAX_CONCURRENT") {
			t.Fatalf("value %q: expected LINEARCAST_ON_DEMAND_MAX_CONCURRENT error, got %v", raw, err)
		}
	}
}

func TestLoadStartupConfigOnDemandTiming(t *testing.T) {
	cfg, err := loadStartupConfig(linearcastTestEnv(map[string]string{
		"LINEARCAST_DB": "/tmp/linearcast.db",
	}))
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if cfg.onDemandPlaybackLagMs != playback.DefaultOnDemandPlaybackLagMs {
		t.Fatalf("blank lag=%d, want default %d", cfg.onDemandPlaybackLagMs, playback.DefaultOnDemandPlaybackLagMs)
	}
	if cfg.onDemandWarmupMs != playback.DefaultOnDemandWarmupMs {
		t.Fatalf("blank warmup=%d, want default %d", cfg.onDemandWarmupMs, playback.DefaultOnDemandWarmupMs)
	}

	cfg, err = loadStartupConfig(linearcastTestEnv(map[string]string{
		"LINEARCAST_DB":                        "/tmp/linearcast.db",
		"LINEARCAST_ON_DEMAND_PLAYBACK_LAG_MS": " 12000 ",
		"LINEARCAST_ON_DEMAND_WARMUP_MS":       "6000",
	}))
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if cfg.onDemandPlaybackLagMs != 12_000 {
		t.Fatalf("onDemandPlaybackLagMs=%d, want 12000", cfg.onDemandPlaybackLagMs)
	}
	if cfg.onDemandWarmupMs != 6_000 {
		t.Fatalf("onDemandWarmupMs=%d, want 6000", cfg.onDemandWarmupMs)
	}

	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "zero lag", key: "LINEARCAST_ON_DEMAND_PLAYBACK_LAG_MS", value: "0"},
		{name: "negative lag", key: "LINEARCAST_ON_DEMAND_PLAYBACK_LAG_MS", value: "-1"},
		{name: "bad lag", key: "LINEARCAST_ON_DEMAND_PLAYBACK_LAG_MS", value: "abc"},
		{name: "zero warmup", key: "LINEARCAST_ON_DEMAND_WARMUP_MS", value: "0"},
		{name: "negative warmup", key: "LINEARCAST_ON_DEMAND_WARMUP_MS", value: "-1"},
		{name: "bad warmup", key: "LINEARCAST_ON_DEMAND_WARMUP_MS", value: "abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadStartupConfig(linearcastTestEnv(map[string]string{
				"LINEARCAST_DB": "/tmp/linearcast.db",
				tc.key:          tc.value,
			}))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("expected %s error, got %v", tc.key, err)
			}
		})
	}
}

func TestLoadStartupConfigInvalidBools(t *testing.T) {
	// parseConfigBool is shared by all boolean env vars; one representative
	// rejection is enough.
	if _, err := loadStartupConfig(linearcastTestEnv(map[string]string{
		"LINEARCAST_DB":                       "/tmp/linearcast.db",
		"LINEARCAST_ON_DEMAND_MAX_CONCURRENT": "maybe",
	})); err == nil || !strings.Contains(err.Error(), "LINEARCAST_ON_DEMAND_MAX_CONCURRENT") {
		t.Fatalf("expected error for invalid max concurrent, got %v", err)
	}
}

func TestLoadStartupConfigRejectsMalformedAddr(t *testing.T) {
	for _, addr := range []string{"localhost", ":", ":0", ":abc", "8888", ":99999"} {
		_, err := loadStartupConfig(linearcastTestEnv(map[string]string{
			"LINEARCAST_DB":   "/tmp/linearcast.db",
			"LINEARCAST_ADDR": addr,
		}))
		if err == nil || !strings.Contains(err.Error(), "LINEARCAST_ADDR") {
			t.Fatalf("addr %q: expected LINEARCAST_ADDR error, got %v", addr, err)
		}
	}
}

func TestLoadStartupConfigAcceptsValidAddr(t *testing.T) {
	for _, addr := range []string{":8888", "0.0.0.0:8888", "127.0.0.1:65535"} {
		cfg, err := loadStartupConfig(linearcastTestEnv(map[string]string{
			"LINEARCAST_DB":   "/tmp/linearcast.db",
			"LINEARCAST_ADDR": addr,
		}))
		if err != nil {
			t.Fatalf("addr %q: unexpected error %v", addr, err)
		}
		if cfg.addr != addr {
			t.Fatalf("addr %q: got %q", addr, cfg.addr)
		}
	}
}

func TestLoadStartupConfigAdminSecurity(t *testing.T) {
	cfg, err := loadStartupConfig(linearcastTestEnv(map[string]string{
		"LINEARCAST_DB":                  "/tmp/linearcast.db",
		"LINEARCAST_ADMIN_ALLOW_NO_AUTH": " yes ",
		"LINEARCAST_ADMIN_COOKIE_SECURE": "true",
	}))
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if !cfg.adminAllowNoAuth || !cfg.adminCookieSecure {
		t.Fatalf("admin flags not loaded: %+v", cfg)
	}
}

func TestLoadStartupConfigRejectsInvalidAdminSecurity(t *testing.T) {
	_, err := loadStartupConfig(linearcastTestEnv(map[string]string{
		"LINEARCAST_DB":                  "/tmp/linearcast.db",
		"LINEARCAST_ADMIN_ALLOW_NO_AUTH": "sometimes",
	}))
	if err == nil || !strings.Contains(err.Error(), "LINEARCAST_ADMIN_ALLOW_NO_AUTH") {
		t.Fatalf("expected admin boolean error, got %v", err)
	}
}

func linearcastTestEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
