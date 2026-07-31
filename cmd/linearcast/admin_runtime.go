package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/admin"
	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/playback"
)

func newAdminRuntime(ctx context.Context, cfg startupConfig, conn *sql.DB, status playback.StatusProvider, control playback.Controller, degraded playback.DegradedReader) (*admin.App, *admin.Sweeper, error) {
	if issues, err := db.ValidateChannelMediaChains(ctx, conn); err != nil {
		slog.Warn("chain integrity check failed to run", "err", err)
	} else if len(issues) > 0 {
		slog.Warn("chain integrity issues found", "count", len(issues))
		for _, issue := range issues {
			slog.Warn("chain integrity issue", "channel", issue.ChannelID, "kind", issue.Kind, "media", issue.MediaIDs, "detail", issue.Detail)
		}
	} else {
		slog.Info("chain integrity: all channel_media chains intact")
	}

	settings, err := db.GetEncoderSweeperSettings(ctx, conn)
	if err != nil {
		return nil, nil, fmt.Errorf("read encoder sweeper settings: %w", err)
	}

	var passwordHash string
	var passwordMustChange bool
	if !cfg.adminAllowNoAuth {
		passwordHash, passwordMustChange, err = admin.EnsurePassword(ctx, conn)
		if err != nil {
			return nil, nil, fmt.Errorf("admin password setup: %w", err)
		}
	}

	app := admin.New(admin.Config{
		DB:                      conn,
		DBPath:                  cfg.dbPath,
		HTTPClient:              &http.Client{Timeout: 2 * time.Second},
		PlaybackStatus:          status,
		PlaybackControl:         control,
		DegradedReader:          degraded,
		CacheDir:                cfg.cacheDir,
		MediaRoot:               os.Getenv("LINEARCAST_MEDIA_ROOT"),
		PlexURL:                 os.Getenv("PLEX_URL"),
		PlexPathMap:             os.Getenv("PLEX_PATH_MAP"),
		JellyfinURL:             os.Getenv("JELLYFIN_URL"),
		JellyfinPathMap:         os.Getenv("JELLYFIN_PATH_MAP"),
		AdminPasswordHash:       passwordHash,
		AdminPasswordMustChange: passwordMustChange,
		AdminCookieSecure:       cfg.adminCookieSecure,
		EncoderDistDir:          encoderDistDir(os.Getenv("LINEARCAST_ENCODER_DIST_DIR")),
	})

	sweeper := admin.NewSweeper(conn)
	sweeper.Interval = time.Duration(settings.SweepIntervalSeconds) * time.Second
	sweeper.MaxAttempts = settings.MaxAttempts
	authMode := "password"
	if cfg.adminAllowNoAuth {
		authMode = "disabled"
	}
	slog.Info("admin module initialized",
		"auth", authMode,
		"allow_no_auth", cfg.adminAllowNoAuth,
		"must_change", passwordMustChange,
	)
	slog.Info("encoder sweeper configured",
		"interval", sweeper.Interval.String(),
		"max_attempts", sweeper.MaxAttempts,
		"stale_processing_timeout", sweeper.StaleProcessingTimeout.String(),
	)
	return app, sweeper, nil
}

func encoderDistDir(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "/opt/linearcast/encoder-dist"
}
