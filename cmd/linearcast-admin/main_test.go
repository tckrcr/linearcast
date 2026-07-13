package main

import (
	"strings"
	"testing"
)

func TestLoadStartupConfigRequiresDatabasePath(t *testing.T) {
	_, err := loadStartupConfig(testEnv(map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "LINEARCAST_DB is required") {
		t.Fatalf("expected DB path error, got %v", err)
	}
}

func TestLoadStartupConfigAllowsMissingPassword(t *testing.T) {
	cfg, err := loadStartupConfig(testEnv(map[string]string{
		"LINEARCAST_DB": "/tmp/linearcast.db",
	}))
	if err != nil {
		t.Fatalf("expected success with no env password, got %v", err)
	}
	if cfg.allowNoAuth {
		t.Fatal("expected allowNoAuth=false")
	}
}

func TestLoadStartupConfigAllowsExplicitNoAuth(t *testing.T) {
	cfg, err := loadStartupConfig(testEnv(map[string]string{
		"LINEARCAST_DB":                  "/tmp/linearcast.db",
		"LINEARCAST_ADMIN_ALLOW_NO_AUTH": " true ",
	}))
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if !cfg.allowNoAuth {
		t.Fatal("expected allowNoAuth=true")
	}
}

func TestLoadStartupConfigRejectsMalformedAdminAddr(t *testing.T) {
	for _, addr := range []string{"localhost", ":", ":0", ":abc", "8890", ":99999"} {
		_, err := loadStartupConfig(testEnv(map[string]string{
			"LINEARCAST_DB":         "/tmp/linearcast.db",
			"LINEARCAST_ADMIN_ADDR": addr,
		}))
		if err == nil || !strings.Contains(err.Error(), "LINEARCAST_ADMIN_ADDR") {
			t.Fatalf("addr %q: expected LINEARCAST_ADMIN_ADDR error, got %v", addr, err)
		}
	}
}

func TestLoadStartupConfigAcceptsValidAdminAddr(t *testing.T) {
	cfg, err := loadStartupConfig(testEnv(map[string]string{
		"LINEARCAST_DB":         "/tmp/linearcast.db",
		"LINEARCAST_ADMIN_ADDR": "0.0.0.0:8890",
	}))
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if cfg.addr != "0.0.0.0:8890" {
		t.Fatalf("addr=%q", cfg.addr)
	}
}

func testEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
