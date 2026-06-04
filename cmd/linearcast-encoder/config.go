package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		AdminURL: strings.TrimSpace(getenv(envAdminURL)),
		APIKey:   strings.TrimSpace(getenv(envAPIKey)),
		WorkDir:  strings.TrimSpace(getenv(envWorkDir)),
	}
	if cfg.AdminURL == "" {
		return config{}, fmt.Errorf("%s is required", envAdminURL)
	}
	if cfg.APIKey == "" {
		return config{}, fmt.Errorf("%s is required", envAPIKey)
	}
	u, err := url.Parse(cfg.AdminURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return config{}, fmt.Errorf("%s must be an absolute http(s) URL", envAdminURL)
	}
	cfg.AdminURL = strings.TrimRight(cfg.AdminURL, "/")
	if cfg.WorkDir == "" {
		cfg.WorkDir = "."
	}
	if s := strings.TrimSpace(getenv(envConcurrency)); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return config{}, fmt.Errorf("%s must be a positive integer, got %q", envConcurrency, s)
		}
		cfg.Concurrency = n
	}
	return cfg, nil
}
