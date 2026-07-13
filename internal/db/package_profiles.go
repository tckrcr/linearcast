package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

// ErrProfileNotFound is returned when a package profile name does not exist.
// Wrapped via %w so the message is preserved while callers match with errors.Is.
var ErrProfileNotFound = errors.New("not found")

// UpsertPackageProfile inserts or replaces a custom (non-built-in) profile.
// The profile is stored as JSON in the package_profiles table.
func UpsertPackageProfile(ctx context.Context, conn *sql.DB, p packageprofile.Profile) error {
	p.MediaKind = packageprofile.NormalizeMediaKind(p.MediaKind)
	jsonBytes, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal profile: %w", err)
	}
	nowMs := time.Now().UTC().UnixMilli()
	_, err = conn.ExecContext(ctx, `
		INSERT INTO package_profiles (name, is_builtin, disabled, profile_json, created_at_ms, updated_at_ms)
		VALUES (?, 0, 0, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			profile_json = excluded.profile_json,
			updated_at_ms = excluded.updated_at_ms
	`, p.Name, string(jsonBytes), nowMs, nowMs)
	return err
}

// DeletePackageProfile removes an unreferenced custom profile by name.
func DeletePackageProfile(ctx context.Context, conn *sql.DB, name string) error {
	res, err := conn.ExecContext(ctx, `
		DELETE FROM package_profiles
		WHERE name = ? AND is_builtin = 0`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("profile %q not found or built-in", name)
	}
	return nil
}

func DisablePackageProfile(ctx context.Context, conn *sql.DB, name string) error {
	nowMs := time.Now().UTC().UnixMilli()
	res, err := conn.ExecContext(ctx, `
		UPDATE package_profiles
		SET disabled = 1, updated_at_ms = ?
		WHERE name = ? AND disabled = 0`, nowMs, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("profile %q not found or already disabled", name)
	}
	return nil
}

func EnablePackageProfile(ctx context.Context, conn *sql.DB, name string) error {
	nowMs := time.Now().UTC().UnixMilli()
	res, err := conn.ExecContext(ctx, `
		UPDATE package_profiles
		SET disabled = 0, updated_at_ms = ?
		WHERE name = ? AND disabled = 1`, nowMs, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("profile %q not found or already enabled", name)
	}
	return nil
}

// GetPackageProfile returns a single profile by name, or (nil, nil) if not found.
func GetPackageProfile(ctx context.Context, conn Execer, name string) (*packageprofile.Profile, error) {
	row := conn.QueryRowContext(ctx, `
		SELECT profile_json FROM package_profiles
		WHERE name = ? AND disabled = 0`, name)
	var jsonStr string
	if err := row.Scan(&jsonStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var p packageprofile.Profile
	if err := json.Unmarshal([]byte(jsonStr), &p); err != nil {
		return nil, fmt.Errorf("unmarshal profile %s: %w", name, err)
	}
	p.MediaKind = packageprofile.NormalizeMediaKind(p.MediaKind)
	return &p, nil
}

// AllPackageProfiles returns active profiles (built-in + custom) with details.
func AllPackageProfiles(ctx context.Context, conn *sql.DB) ([]packageprofile.Profile, error) {
	records, err := AllPackageProfileRecords(ctx, conn)
	if err != nil {
		return nil, err
	}
	profiles := make([]packageprofile.Profile, 0, len(records))
	for _, record := range records {
		if !record.Disabled {
			profiles = append(profiles, record.Profile)
		}
	}
	return profiles, nil
}

// AllPackageProfileRecords returns active and disabled profiles for inspection.
func AllPackageProfileRecords(ctx context.Context, conn *sql.DB) ([]PackageProfileRecord, error) {
	records, err := queryRows(ctx, conn, scanPackageProfileRecordRow, `
		SELECT name, is_builtin, disabled, profile_json FROM package_profiles
		ORDER BY is_builtin DESC, name`)
	if err != nil {
		return nil, err
	}

	byName := map[string]PackageProfileRecord{}
	customNames := []string{}
	for _, row := range records {
		byName[row.name] = row.record
		if !row.record.IsBuiltin {
			customNames = append(customNames, row.name)
		}
	}

	out := make([]PackageProfileRecord, 0, len(byName))
	for _, builtin := range packageprofile.BuiltIns() {
		record, ok := byName[builtin.Name]
		if !ok {
			record = PackageProfileRecord{Profile: builtin, IsBuiltin: true}
		}
		record.Profile = builtin
		record.IsBuiltin = true
		out = append(out, record)
	}
	for _, name := range customNames {
		out = append(out, byName[name])
	}
	return out, nil
}

type packageProfileRecordRow struct {
	name   string
	record PackageProfileRecord
}

func scanPackageProfileRecordRow(row scanner) (packageProfileRecordRow, error) {
	var name, jsonStr string
	var builtin, disabled int
	if err := row.Scan(&name, &builtin, &disabled, &jsonStr); err != nil {
		return packageProfileRecordRow{}, err
	}
	var p packageprofile.Profile
	if err := json.Unmarshal([]byte(jsonStr), &p); err != nil {
		return packageProfileRecordRow{}, fmt.Errorf("unmarshal profile %s: %w", name, err)
	}
	p.MediaKind = packageprofile.NormalizeMediaKind(p.MediaKind)
	return packageProfileRecordRow{name: name, record: PackageProfileRecord{
		Profile:   p,
		IsBuiltin: builtin == 1,
		Disabled:  disabled == 1,
	}}, nil
}

func PackageProfileByName(ctx context.Context, conn *sql.DB, name string) (*PackageProfileRecord, error) {
	records, err := AllPackageProfileRecords(ctx, conn)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.Profile.Name == name {
			copy := record
			return &copy, nil
		}
	}
	return nil, nil
}

// AllPackageProfileNames returns all profile names (built-in + custom).
func AllPackageProfileNames(ctx context.Context, conn *sql.DB) ([]string, error) {
	profiles, err := AllPackageProfiles(ctx, conn)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names, nil
}

// IsBuiltinProfile returns true if the profile is a built-in profile.
func IsBuiltinProfile(ctx context.Context, conn *sql.DB, name string) (bool, error) {
	if packageprofile.Known(name) {
		return true, nil
	}
	row := conn.QueryRowContext(ctx, `
		SELECT is_builtin FROM package_profiles WHERE name = ?`, name)
	var builtin int
	if err := row.Scan(&builtin); err != nil {
		if err == sql.ErrNoRows {
			return false, fmt.Errorf("profile %q %w", name, ErrProfileNotFound)
		}
		return false, err
	}
	return builtin == 1, nil
}

func PackageProfileReferencesForName(ctx context.Context, conn *sql.DB, name string) (PackageProfileReferences, error) {
	var refs PackageProfileReferences
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_packages WHERE rendition_profile = ?`, name).Scan(&refs.MediaPackages); err != nil {
		return refs, err
	}
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM channels c
		WHERE c.upstream_hls_url IS NULL
		  AND (
		  	COALESCE(NULLIF(TRIM(c.required_package_profile), ''), ?) = ?
		  	OR EXISTS (
		  		SELECT 1
		  		FROM json_each(
		  			CASE
		  				WHEN json_valid(c.abr_ladder_json) AND json_type(c.abr_ladder_json) = 'array' THEN c.abr_ladder_json
		  				ELSE json_array(COALESCE(NULLIF(TRIM(c.required_package_profile), ''), ?))
		  			END
		  		)
		  		WHERE TRIM(json_each.value) = ?
		  	)
		  )`,
		DefaultPackageProfile, name, DefaultPackageProfile, name).Scan(&refs.Channels); err != nil {
		return refs, err
	}
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schedule_entries se
		JOIN channels c ON c.id = se.channel_id
		WHERE c.upstream_hls_url IS NULL
		  AND (
		  	COALESCE(NULLIF(TRIM(c.required_package_profile), ''), ?) = ?
		  	OR EXISTS (
		  		SELECT 1
		  		FROM json_each(
		  			CASE
		  				WHEN json_valid(c.abr_ladder_json) AND json_type(c.abr_ladder_json) = 'array' THEN c.abr_ladder_json
		  				ELSE json_array(COALESCE(NULLIF(TRIM(c.required_package_profile), ''), ?))
		  			END
		  		)
		  		WHERE TRIM(json_each.value) = ?
		  	)
		  )`,
		DefaultPackageProfile, name, DefaultPackageProfile, name).Scan(&refs.ScheduleEntries); err != nil {
		return refs, err
	}
	return refs, nil
}

func (r PackageProfileReferences) HasAny() bool {
	return r.MediaPackages > 0 || r.Channels > 0 || r.ScheduleEntries > 0
}
