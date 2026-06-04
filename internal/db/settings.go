package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	plexTokenSettingKey       = "plex_token"
	plexURLSettingKey         = "plex_url"
	plexPathMapSettingKey     = "plex_path_map"
	jellyfinURLSettingKey     = "jellyfin_url"
	jellyfinAPIKeySettingKey  = "jellyfin_api_key"
	jellyfinPathMapSettingKey = "jellyfin_path_map"
)

func getSetting(ctx context.Context, conn *sql.DB, key string) (string, bool, error) {
	var v string
	err := conn.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get setting %s: %w", key, err)
	}
	return v, true, nil
}

func setSetting(ctx context.Context, conn *sql.DB, key, value string) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

// PlexTokenSettingExists reports whether the Plex token setting row exists.
// An existing empty value means the operator explicitly signed out.
func PlexTokenSettingExists(ctx context.Context, conn *sql.DB) (bool, error) {
	_, ok, err := getSetting(ctx, conn, plexTokenSettingKey)
	return ok, err
}

// SeedPlexToken stores token only when no Plex token setting exists yet.
// This migrates env-configured installs without undoing an explicit sign-out.
func SeedPlexToken(ctx context.Context, conn *sql.DB, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	exists, err := PlexTokenSettingExists(ctx, conn)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return SetPlexToken(ctx, conn, token)
}

// GetPlexToken returns the stored Plex token. It returns an empty string when
// the token is unset or has been explicitly cleared.
func GetPlexToken(ctx context.Context, conn *sql.DB) (string, error) {
	raw, ok, err := getSetting(ctx, conn, plexTokenSettingKey)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	var token string
	if err := json.Unmarshal([]byte(raw), &token); err != nil {
		return "", fmt.Errorf("parse %s: %w", plexTokenSettingKey, err)
	}
	return token, nil
}

// SetPlexToken stores token after trimming surrounding whitespace.
func SetPlexToken(ctx context.Context, conn *sql.DB, token string) error {
	token = strings.TrimSpace(token)
	b, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshal plex token: %w", err)
	}
	return setSetting(ctx, conn, plexTokenSettingKey, string(b))
}

// ClearPlexToken preserves the setting row with an empty value so env bootstrap
// does not silently reconnect after an operator signs out.
func ClearPlexToken(ctx context.Context, conn *sql.DB) error {
	return SetPlexToken(ctx, conn, "")
}

// GetPlexURL returns the stored Plex base URL. Empty means callers should use
// their configured env/default URL.
func GetPlexURL(ctx context.Context, conn *sql.DB) (string, error) {
	raw, ok, err := getSetting(ctx, conn, plexURLSettingKey)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("parse %s: %w", plexURLSettingKey, err)
	}
	return strings.TrimRight(strings.TrimSpace(value), "/"), nil
}

func SetPlexURL(ctx context.Context, conn *sql.DB, value string) error {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal plex url: %w", err)
	}
	return setSetting(ctx, conn, plexURLSettingKey, string(b))
}

// GetPlexPathMap returns the stored Plex path map string. Empty means no
// mapping is configured.
func GetPlexPathMap(ctx context.Context, conn *sql.DB) (string, error) {
	raw, ok, err := getSetting(ctx, conn, plexPathMapSettingKey)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("parse %s: %w", plexPathMapSettingKey, err)
	}
	return strings.TrimSpace(value), nil
}

func SetPlexPathMap(ctx context.Context, conn *sql.DB, value string) error {
	value = strings.TrimSpace(value)
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal plex path map: %w", err)
	}
	return setSetting(ctx, conn, plexPathMapSettingKey, string(b))
}

// GetJellyfinURL returns the stored Jellyfin URL. Empty means callers should
// use their configured env/default URL.
func GetJellyfinURL(ctx context.Context, conn *sql.DB) (string, error) {
	raw, ok, err := getSetting(ctx, conn, jellyfinURLSettingKey)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("parse %s: %w", jellyfinURLSettingKey, err)
	}
	return strings.TrimRight(strings.TrimSpace(value), "/"), nil
}

func SetJellyfinURL(ctx context.Context, conn *sql.DB, value string) error {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal jellyfin url: %w", err)
	}
	return setSetting(ctx, conn, jellyfinURLSettingKey, string(b))
}

func GetJellyfinAPIKey(ctx context.Context, conn *sql.DB) (string, error) {
	raw, ok, err := getSetting(ctx, conn, jellyfinAPIKeySettingKey)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("parse %s: %w", jellyfinAPIKeySettingKey, err)
	}
	return strings.TrimSpace(value), nil
}

func SetJellyfinAPIKey(ctx context.Context, conn *sql.DB, value string) error {
	value = strings.TrimSpace(value)
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal jellyfin api key: %w", err)
	}
	return setSetting(ctx, conn, jellyfinAPIKeySettingKey, string(b))
}

func ClearJellyfinAPIKey(ctx context.Context, conn *sql.DB) error {
	return SetJellyfinAPIKey(ctx, conn, "")
}

// GetJellyfinPathMap returns the stored Jellyfin path map string.
func GetJellyfinPathMap(ctx context.Context, conn *sql.DB) (string, error) {
	raw, ok, err := getSetting(ctx, conn, jellyfinPathMapSettingKey)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("parse %s: %w", jellyfinPathMapSettingKey, err)
	}
	return strings.TrimSpace(value), nil
}

func SetJellyfinPathMap(ctx context.Context, conn *sql.DB, value string) error {
	value = strings.TrimSpace(value)
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal jellyfin path map: %w", err)
	}
	return setSetting(ctx, conn, jellyfinPathMapSettingKey, string(b))
}

// GetSubtitleLanguagePreference returns the ordered list of ISO 639-2 language
// codes the operator wants subtitles for. Defaults to ["eng"] if not set.
func GetSubtitleLanguagePreference(ctx context.Context, conn *sql.DB) ([]string, error) {
	raw, ok, err := getSetting(ctx, conn, "subtitle_language_preference")
	if err != nil {
		return nil, err
	}
	if !ok {
		return []string{"eng"}, nil
	}
	var langs []string
	if err := json.Unmarshal([]byte(raw), &langs); err != nil {
		return nil, fmt.Errorf("parse subtitle_language_preference: %w", err)
	}
	return langs, nil
}

// SetSubtitleLanguagePreference stores an ordered list of ISO 639-2 language
// codes. Pass nil to reset to default ["eng"].
func SetSubtitleLanguagePreference(ctx context.Context, conn *sql.DB, langs []string) error {
	if langs == nil {
		langs = []string{"eng"}
	}
	b, err := json.Marshal(langs)
	if err != nil {
		return fmt.Errorf("marshal langs: %w", err)
	}
	return setSetting(ctx, conn, "subtitle_language_preference", string(b))
}

// GetSubtitleAutoEnable returns whether the player should automatically enable
// the top-preference subtitle track. Defaults to false.
func GetSubtitleAutoEnable(ctx context.Context, conn *sql.DB) (bool, error) {
	raw, ok, err := getSetting(ctx, conn, "subtitle_auto_enable")
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return false, fmt.Errorf("parse subtitle_auto_enable: %w", err)
	}
	return b, nil
}

// SetSubtitleAutoEnable stores the player auto-enable flag.
func SetSubtitleAutoEnable(ctx context.Context, conn *sql.DB, enabled bool) error {
	b, _ := json.Marshal(enabled)
	return setSetting(ctx, conn, "subtitle_auto_enable", string(b))
}

const (
	adminPasswordHashKey       = "admin_password_hash"
	adminPasswordMustChangeKey = "admin_password_must_change"
)

// GetAdminPasswordHash returns the stored bcrypt hash and whether the setting
// row exists. An empty hash means auth is disabled.
func GetAdminPasswordHash(ctx context.Context, conn *sql.DB) (string, bool, error) {
	return getSetting(ctx, conn, adminPasswordHashKey)
}

// SetAdminPasswordHash stores a bcrypt hash of the admin password.
func SetAdminPasswordHash(ctx context.Context, conn *sql.DB, hash string) error {
	return setSetting(ctx, conn, adminPasswordHashKey, hash)
}

// AdminPasswordMustChange returns whether the operator must change the admin
// password on next login (true for the packaged default).
func AdminPasswordMustChange(ctx context.Context, conn *sql.DB) (bool, error) {
	raw, ok, err := getSetting(ctx, conn, adminPasswordMustChangeKey)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return false, fmt.Errorf("parse %s: %w", adminPasswordMustChangeKey, err)
	}
	return b, nil
}

// SetAdminPasswordMustChange stores the must-change flag.
func SetAdminPasswordMustChange(ctx context.Context, conn *sql.DB, v bool) error {
	b, _ := json.Marshal(v)
	return setSetting(ctx, conn, adminPasswordMustChangeKey, string(b))
}

const (
	defaultPackagedProfileSettingKey = "default_packaged_profile"

	schedulerHorizonHoursSettingKey  = "scheduler_horizon_hours"
	schedulerLowWaterHoursSettingKey = "scheduler_low_water_hours"
	schedulerTickSecondsSettingKey   = "scheduler_tick_seconds"

	encoderSweepIntervalSecondsSettingKey = "encoder_sweep_interval_seconds"
	encoderMaxAttemptsSettingKey          = "encoder_max_attempts"
)

// Default values for the scheduler tunables. Mirrored in schema.sql's settings
// seed so fresh installs and migrations land on the same numbers.
const (
	DefaultSchedulerHorizonHours  = 48
	DefaultSchedulerLowWaterHours = 24
	DefaultSchedulerTickSeconds   = 300
)

// GetDefaultPackagedProfile returns the configured default profile name used
// for new channels and as the playback fallback. Falls back to the built-in
// default name on missing/unparseable rows; callers should still validate the
// returned name exists in package_profiles before using it (the profile may
// have been deleted since the setting was last written).
func GetDefaultPackagedProfile(ctx context.Context, conn *sql.DB) (string, error) {
	raw, ok, err := getSetting(ctx, conn, defaultPackagedProfileSettingKey)
	if err != nil {
		return "", err
	}
	if !ok {
		return DefaultPackageProfile, nil
	}
	var name string
	if err := json.Unmarshal([]byte(raw), &name); err != nil {
		return "", fmt.Errorf("parse %s: %w", defaultPackagedProfileSettingKey, err)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return DefaultPackageProfile, nil
	}
	return name, nil
}

// SetDefaultPackagedProfile persists the default profile name. Does not
// validate that the profile exists — admin handlers do that gate so the DB
// helper stays simple and reusable.
func SetDefaultPackagedProfile(ctx context.Context, conn *sql.DB, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("default packaged profile name is required")
	}
	b, _ := json.Marshal(name)
	return setSetting(ctx, conn, defaultPackagedProfileSettingKey, string(b))
}

func getPositiveIntSetting(ctx context.Context, conn *sql.DB, key string, fallback int) (int, error) {
	raw, ok, err := getSetting(ctx, conn, key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return fallback, nil
	}
	var n int
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if n <= 0 {
		return fallback, nil
	}
	return n, nil
}

func setPositiveIntSetting(ctx context.Context, conn *sql.DB, key string, n int) error {
	if n <= 0 {
		return fmt.Errorf("%s must be > 0 (got %d)", key, n)
	}
	b, _ := json.Marshal(n)
	return setSetting(ctx, conn, key, string(b))
}

// SchedulerTunables bundles the three scheduler-extender knobs that used to
// live in env. Always returned and written as a unit because the constraint
// (low_water < horizon) spans two of the three values.
type SchedulerTunables struct {
	HorizonHours  int `json:"horizonHours"`
	LowWaterHours int `json:"lowWaterHours"`
	TickSeconds   int `json:"tickSeconds"`
}

// GetSchedulerTunables returns the scheduler-extender tunables. Missing or
// invalid rows fall back to package defaults; the cross-field constraint is
// not enforced on read so a partially-set DB is still usable.
func GetSchedulerTunables(ctx context.Context, conn *sql.DB) (SchedulerTunables, error) {
	horizon, err := getPositiveIntSetting(ctx, conn, schedulerHorizonHoursSettingKey, DefaultSchedulerHorizonHours)
	if err != nil {
		return SchedulerTunables{}, err
	}
	lowWater, err := getPositiveIntSetting(ctx, conn, schedulerLowWaterHoursSettingKey, DefaultSchedulerLowWaterHours)
	if err != nil {
		return SchedulerTunables{}, err
	}
	tick, err := getPositiveIntSetting(ctx, conn, schedulerTickSecondsSettingKey, DefaultSchedulerTickSeconds)
	if err != nil {
		return SchedulerTunables{}, err
	}
	return SchedulerTunables{
		HorizonHours:  horizon,
		LowWaterHours: lowWater,
		TickSeconds:   tick,
	}, nil
}

// SetSchedulerTunables validates the cross-field constraint and persists all
// three values. Callers should pass the full struct: partial updates aren't
// supported so the constraint stays trivially enforceable.
func SetSchedulerTunables(ctx context.Context, conn *sql.DB, t SchedulerTunables) error {
	if t.HorizonHours <= 0 {
		return fmt.Errorf("horizonHours must be > 0 (got %d)", t.HorizonHours)
	}
	if t.LowWaterHours <= 0 {
		return fmt.Errorf("lowWaterHours must be > 0 (got %d)", t.LowWaterHours)
	}
	if t.TickSeconds <= 0 {
		return fmt.Errorf("tickSeconds must be > 0 (got %d)", t.TickSeconds)
	}
	if t.LowWaterHours >= t.HorizonHours {
		return fmt.Errorf("lowWaterHours (%d) must be < horizonHours (%d)", t.LowWaterHours, t.HorizonHours)
	}
	if err := setPositiveIntSetting(ctx, conn, schedulerHorizonHoursSettingKey, t.HorizonHours); err != nil {
		return err
	}
	if err := setPositiveIntSetting(ctx, conn, schedulerLowWaterHoursSettingKey, t.LowWaterHours); err != nil {
		return err
	}
	return setPositiveIntSetting(ctx, conn, schedulerTickSecondsSettingKey, t.TickSeconds)
}

// Default values for encoder sweeper tunables.
const (
	DefaultEncoderSweepIntervalSeconds = 30
	DefaultEncoderMaxAttempts          = 5
)

// EncoderSweeperSettings bundles the two encoder-sweeper knobs that used to
// live in env. Both values are positive integers.
type EncoderSweeperSettings struct {
	SweepIntervalSeconds int `json:"sweepIntervalSeconds"`
	MaxAttempts          int `json:"maxAttempts"`
}

// GetEncoderSweeperSettings returns the encoder sweeper tunables. Missing or
// invalid rows fall back to package defaults.
func GetEncoderSweeperSettings(ctx context.Context, conn *sql.DB) (EncoderSweeperSettings, error) {
	interval, err := getPositiveIntSetting(ctx, conn, encoderSweepIntervalSecondsSettingKey, DefaultEncoderSweepIntervalSeconds)
	if err != nil {
		return EncoderSweeperSettings{}, err
	}
	maxAttempts, err := getPositiveIntSetting(ctx, conn, encoderMaxAttemptsSettingKey, DefaultEncoderMaxAttempts)
	if err != nil {
		return EncoderSweeperSettings{}, err
	}
	return EncoderSweeperSettings{
		SweepIntervalSeconds: interval,
		MaxAttempts:          maxAttempts,
	}, nil
}

// SetEncoderSweeperSettings validates and persists both sweeper values.
func SetEncoderSweeperSettings(ctx context.Context, conn *sql.DB, s EncoderSweeperSettings) error {
	if s.SweepIntervalSeconds <= 0 {
		return fmt.Errorf("sweepIntervalSeconds must be > 0 (got %d)", s.SweepIntervalSeconds)
	}
	if s.MaxAttempts <= 0 {
		return fmt.Errorf("maxAttempts must be > 0 (got %d)", s.MaxAttempts)
	}
	if err := setPositiveIntSetting(ctx, conn, encoderSweepIntervalSecondsSettingKey, s.SweepIntervalSeconds); err != nil {
		return err
	}
	return setPositiveIntSetting(ctx, conn, encoderMaxAttemptsSettingKey, s.MaxAttempts)
}
