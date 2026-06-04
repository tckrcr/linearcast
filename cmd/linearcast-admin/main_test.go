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
	if cfg.adminPassword != "" {
		t.Fatalf("expected empty adminPassword, got %q", cfg.adminPassword)
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
	if cfg.adminPassword != "" {
		t.Fatalf("expected auth disabled, got password %q", cfg.adminPassword)
	}
	if !cfg.allowNoAuth {
		t.Fatal("expected allowNoAuth=true")
	}
}

func TestLoadStartupConfigAcceptsPresentPassword(t *testing.T) {
	cfg, err := loadStartupConfig(testEnv(map[string]string{
		"LINEARCAST_DB":             "/tmp/linearcast.db",
		"LINEARCAST_ADMIN_PASSWORD": " secret ",
	}))
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if cfg.adminPassword != "secret" {
		t.Fatalf("expected trimmed password, got %q", cfg.adminPassword)
	}
	if cfg.allowNoAuth {
		t.Fatal("expected allowNoAuth=false")
	}
}

func TestLoadStartupConfigWhitespacePasswordTreatedAsEmpty(t *testing.T) {
	cfg, err := loadStartupConfig(testEnv(map[string]string{
		"LINEARCAST_DB":             "/tmp/linearcast.db",
		"LINEARCAST_ADMIN_PASSWORD": " \t\n ",
	}))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if cfg.adminPassword != "" {
		t.Fatalf("expected empty adminPassword, got %q", cfg.adminPassword)
	}
}

func testEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
