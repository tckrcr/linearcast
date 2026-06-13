package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

func queryChannels(ctx context.Context, conn Execer, suffix string, args ...any) ([]Channel, error) {
	rows, err := queryRows(ctx, conn, scanValue(scanChannel), channelSelectSQL()+" "+suffix, args...)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []Channel{}, nil
	}
	return rows, nil
}

// EnabledChannels returns enabled channels ordered by id.
func EnabledChannels(ctx context.Context, conn *sql.DB) ([]Channel, error) {
	return queryChannels(ctx, conn, `WHERE enabled = 1 ORDER BY id`)
}

// EnabledGuideChannels returns enabled channels that should appear in public
// guide/lineup style listings. Hidden channels remain enabled for direct
// stream callers that already know the channel id.
func EnabledGuideChannels(ctx context.Context, conn *sql.DB) ([]Channel, error) {
	return queryChannels(ctx, conn, `WHERE enabled = 1 AND hidden_from_guide = 0 ORDER BY id`)
}

// AllChannelsOrderedByDisplayName returns every channel row ordered by
// display_name (case-insensitive). Used by admin listings.
func AllChannelsOrderedByDisplayName(ctx context.Context, conn *sql.DB) ([]Channel, error) {
	return queryChannels(ctx, conn, `ORDER BY display_name COLLATE NOCASE`)
}

// ChannelByID returns the channel row whether enabled or not. Returns
// (nil, nil) if no row exists.
func ChannelByID(ctx context.Context, conn Execer, id string) (*Channel, error) {
	return scanChannel(conn.QueryRowContext(ctx, channelSelectSQL()+` WHERE id = ?`, id))
}

// nullString maps a Go string to a nullable column value: empty becomes SQL
// NULL, preserving the on-disk NULL/” distinction the de-leaked Channel fields
// no longer carry in the struct.
func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// nullableInt64 maps a *int64 to sql.NullInt64: nil → invalid, non-nil → valid.
func nullableInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

// nullableString maps a *string to sql.NullString: nil → invalid, non-nil → valid.
func nullableString(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}

// nonEmptyStr returns nil if s is empty, otherwise returns &s.
// Used when converting string fields to *string for nullable write paths.
func nonEmptyStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func normalizeChannelWrite(c ChannelWrite) ChannelWrite {
	c.MediaKind = NormalizeMediaKind(c.MediaKind)
	if c.PrefillMode != "on_demand" {
		c.PrefillMode = "eager"
	}
	if c.ScheduleMode == "" {
		c.ScheduleMode = "back_to_back"
	}
	if c.ScheduleMode == "slot_grid" && c.SlotDurationMs == nil {
		v := int64(30 * 60 * 1000)
		c.SlotDurationMs = &v
	}
	if c.PlaybackMode == "" || c.PlaybackMode == PlaybackModeGenerated {
		c.PlaybackMode = PlaybackModePackaged
	}
	if c.UpstreamHLSURL != nil {
		c.SourceDirectory = ""
		c.RequiredPackageProfile = ""
		c.ABRLadder = nil
		c.PackagePrefillMs = nil
		// On-demand packaging is meaningless for an external HLS proxy — it owns
		// no packaged media. Keep these eager so they never enter demand tracking.
		c.PrefillMode = "eager"
	}
	if c.PlaybackMode == PlaybackModePlexRelay {
		c.SourceDirectory = ""
		c.RequiredPackageProfile = ""
		c.ABRLadder = nil
		c.PackagePrefillMs = nil
		c.PrefillMode = "eager"
	}
	if c.PlaybackMode == PlaybackModePackaged && strings.TrimSpace(c.RequiredPackageProfile) == "" {
		if c.UpstreamHLSURL == nil {
			c.RequiredPackageProfile = DefaultPackageProfileForMediaKind(c.MediaKind)
		}
	}
	if c.PlaybackMode == PlaybackModePackaged && c.UpstreamHLSURL == nil {
		c.ABRLadder = NormalizeABRLadder(c.RequiredPackageProfile, mustMarshalStringSlice(c.ABRLadder))
	}
	if c.CreatedAtMs == 0 {
		c.CreatedAtMs = time.Now().UTC().UnixMilli()
	}
	return c
}

// InsertChannel creates an enabled packaged-playback channel. Generated mode is
// no longer a supported write policy; legacy values are normalized to packaged.
func InsertChannel(ctx context.Context, conn *sql.DB, c ChannelWrite) error {
	c = normalizeChannelWrite(c)
	_, err := conn.ExecContext(ctx, `
		INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, abr_ladder_json, package_prefill_ms, media_kind, schedule_mode, slot_duration_ms, upstream_hls_url, prefill_mode
		)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.DisplayName, c.SourceDirectory, c.Ordering, c.CreatedAtMs,
		string(c.PlaybackMode), nullString(c.RequiredPackageProfile), abrLadderValue(c.RequiredPackageProfile, c.ABRLadder), c.PackagePrefillMs, string(c.MediaKind), c.ScheduleMode, c.SlotDurationMs, c.UpstreamHLSURL, c.PrefillMode)
	return err
}

// CloneChannel copies channel configuration and curated media membership into
// a new disabled channel. Runtime state and schedule_entries are intentionally
// left behind; the clone starts with no materialized schedule.
func CloneChannel(ctx context.Context, conn *sql.DB, id string, createdAtMs int64) (*Channel, error) {
	if createdAtMs == 0 {
		createdAtMs = time.Now().UTC().UnixMilli()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	src, err := scanChannel(tx.QueryRowContext(ctx, channelSelectSQL()+` WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, sql.ErrNoRows
	}

	names, err := channelDisplayNameSet(ctx, tx)
	if err != nil {
		return nil, err
	}
	ids, err := channelIDSet(ctx, tx)
	if err != nil {
		return nil, err
	}
	cloneID := nextCloneID(src.ID, ids)
	cloneName := nextCloneDisplayName(src.DisplayName, names)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			description, hidden_from_guide, artwork_url, playback_mode, required_package_profile, abr_ladder_json, package_prefill_ms,
			media_kind, schedule_mode, slot_duration_ms, upstream_hls_url, prefill_mode
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cloneID, cloneName, src.SourceDirectory, src.Ordering, 0, createdAtMs,
		nullString(src.Description), src.HiddenFromGuide, nullString(src.ArtworkURL), string(src.PlaybackMode), nullString(src.RequiredPackageProfile), abrLadderValue(src.RequiredPackageProfile, src.ABRLadder), src.PackagePrefillMs,
		string(src.MediaKind), src.ScheduleMode, src.SlotDurationMs, src.UpstreamHLSURL, src.PrefillMode); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		SELECT ?, media_id, anchor_media_id, added_at_ms
		FROM channel_media
		WHERE channel_id = ?`, cloneID, src.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ChannelByID(ctx, conn, cloneID)
}

func channelDisplayNameSet(ctx context.Context, conn Execer) (map[string]bool, error) {
	names, err := queryRows(ctx, conn, scanString, `SELECT display_name FROM channels`)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, name := range names {
		out[strings.ToLower(strings.TrimSpace(name))] = true
	}
	return out, nil
}

func channelIDSet(ctx context.Context, conn Execer) (map[string]bool, error) {
	ids, err := queryRows(ctx, conn, scanString, `SELECT id FROM channels`)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

var cloneDisplaySuffixRE = regexp.MustCompile(`(?i)\s+copy(?:\s+\d+)?$`)
var cloneIDSuffixRE = regexp.MustCompile(`(?i)-copy(?:-\d+)?$`)

func nextCloneDisplayName(name string, existing map[string]bool) string {
	base := strings.TrimSpace(cloneDisplaySuffixRE.ReplaceAllString(name, ""))
	if base == "" {
		base = "Channel"
	}
	first := base + " Copy"
	if !existing[strings.ToLower(first)] {
		return first
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s Copy %d", base, i)
		if !existing[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

func nextCloneID(id string, existing map[string]bool) string {
	base := strings.TrimSpace(cloneIDSuffixRE.ReplaceAllString(id, ""))
	if base == "" {
		base = "channel"
	}
	first := base + "-copy"
	if !existing[first] {
		return first
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-copy-%d", base, i)
		if !existing[candidate] {
			return candidate
		}
	}
}

// OverwriteChannelWithPolicy clears schedule rows and replaces channel metadata
// plus playback policy. This is used by import flows that intentionally replace
// a channel definition.
func OverwriteChannelWithPolicy(ctx context.Context, conn *sql.DB, c ChannelWrite) error {
	c = normalizeChannelWrite(c)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_entries WHERE channel_id = ?`, c.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE channels
		SET display_name = ?, source_directory = ?, ordering = ?, enabled = 1,
		    playback_mode = ?, required_package_profile = ?, abr_ladder_json = ?, package_prefill_ms = ?, media_kind = ?
		WHERE id = ?`,
		c.DisplayName, c.SourceDirectory, c.Ordering, string(c.PlaybackMode),
		nullString(c.RequiredPackageProfile), abrLadderValue(c.RequiredPackageProfile, c.ABRLadder), c.PackagePrefillMs, string(c.MediaKind), c.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// OverwriteChannelMetadata clears schedule rows and replaces channel metadata
// while preserving playback policy. Plex import uses this because the import
// path does not carry playback-mode fields.
func OverwriteChannelMetadata(ctx context.Context, conn *sql.DB, id, displayName, sourceDir, ordering string) error {
	return WithTx(ctx, conn, func(tx Execer) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_entries WHERE channel_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE channels SET display_name = ?, source_directory = ?, ordering = ?, enabled = 1
			WHERE id = ?`,
			displayName, sourceDir, ordering, id)
		return err
	})
}

func DeleteChannel(ctx context.Context, conn *sql.DB, id string) (int64, error) {
	res, err := conn.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func UpdateChannelPlaybackPolicy(ctx context.Context, conn *sql.DB, id string, mode PlaybackMode, profile string, abrLadder []string, prefillMs *int64, mediaKind MediaKind) (bool, error) {
	if mode != PlaybackModePackaged {
		return false, fmt.Errorf("unsupported playback mode %q: only packaged playback is supported", mode)
	}
	mediaKind = NormalizeMediaKind(mediaKind)
	if strings.TrimSpace(profile) == "" {
		profile = DefaultPackageProfileForMediaKind(mediaKind)
	}
	if prefillMs != nil && *prefillMs <= 0 {
		return false, fmt.Errorf("package prefill ms must be positive")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var oldKind MediaKind
	if err := tx.QueryRowContext(ctx, `SELECT media_kind FROM channels WHERE id = ?`, id).Scan(&oldKind); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if oldKind != mediaKind {
		if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_entries WHERE channel_id = ?`, id); err != nil {
			return false, err
		}
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE channels
		SET playback_mode = ?, required_package_profile = ?, abr_ladder_json = ?, package_prefill_ms = ?, media_kind = ?
		WHERE id = ?`,
		string(mode), nullString(profile), abrLadderValue(profile, abrLadder), prefillMs, string(mediaKind), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

func NormalizeChannelsToPackaged(ctx context.Context, conn *sql.DB, profile string) (int64, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = DefaultPackageProfile
	}
	res, err := conn.ExecContext(ctx, `
		UPDATE channels
		SET playback_mode = 'packaged',
		    required_package_profile = COALESCE(NULLIF(TRIM(required_package_profile), ''), ?)
		WHERE playback_mode IS NULL
		   OR TRIM(playback_mode) = ''
		   OR playback_mode = 'generated'
		   OR (playback_mode = 'packaged' AND (
		       required_package_profile IS NULL OR TRIM(required_package_profile) = ''
		   ))`, profile)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetChannelEnabled is the shared transition for enabling/disabling a channel.
// It deliberately does not mutate channel_media or schedule_entries; playback
// observes the flag on refresh and existing schedule state remains available
// if the channel is re-enabled.
func SetChannelEnabled(ctx context.Context, conn *sql.DB, id string, enabled bool) (bool, error) {
	flag := 0
	if enabled {
		flag = 1
	}
	res, err := conn.ExecContext(ctx, `UPDATE channels SET enabled = ? WHERE id = ?`, flag, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SetChannelHiddenFromGuide updates whether a channel is omitted from public
// guide/lineup listings. It deliberately does not affect enabled state or
// runtime scheduling.
func SetChannelHiddenFromGuide(ctx context.Context, conn *sql.DB, id string, hidden bool) (bool, error) {
	flag := 0
	if hidden {
		flag = 1
	}
	res, err := conn.ExecContext(ctx, `UPDATE channels SET hidden_from_guide = ? WHERE id = ?`, flag, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SetChannelArtworkURL stores an optional operator-managed artwork URL for a
// channel. Passing an invalid NullString clears the artwork override.
func SetChannelUpstreamHLSURL(ctx context.Context, conn *sql.DB, id string, rawURL string) (bool, error) {
	res, err := conn.ExecContext(ctx, `UPDATE channels SET upstream_hls_url = ? WHERE id = ? AND upstream_hls_url IS NOT NULL`, rawURL, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func SetChannelArtworkURL(ctx context.Context, conn *sql.DB, id string, artworkURL string) (bool, error) {
	res, err := conn.ExecContext(ctx, `UPDATE channels SET artwork_url = ? WHERE id = ?`, nullString(strings.TrimSpace(artworkURL)), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
