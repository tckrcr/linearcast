package db

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var halfSeasonRe = regexp.MustCompile(`\s+S\d+\s+H[12]$`)

const movieCollectionPrefix = "movie:"

func collectionID(kind, name string) string {
	sum := sha1.Sum([]byte(kind + "\x00" + name))
	return kind + "_" + hex.EncodeToString(sum[:10])
}

func normalizeCollectionName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, movieCollectionPrefix)
	return strings.TrimSpace(name)
}

func collectionKindForGroup(group string, mediaKind MediaKind) string {
	group = strings.TrimSpace(group)
	switch {
	case strings.HasPrefix(group, movieCollectionPrefix):
		return "movie"
	case NormalizeMediaKind(mediaKind) == MediaKindMusic:
		return "album"
	default:
		return "show"
	}
}

func collectionDisplayName(group string) string {
	return normalizeCollectionName(group)
}

func collectionSchedulingLabel(kind, name string) string {
	if kind == "movie" {
		return movieCollectionPrefix + name
	}
	return name
}

func UpsertCollection(ctx context.Context, exec Execer, name, kind, source string) (string, error) {
	name = normalizeCollectionName(name)
	kind = strings.TrimSpace(kind)
	source = strings.TrimSpace(source)
	if name == "" {
		return "", fmt.Errorf("collection name is required")
	}
	switch kind {
	case "show", "movie", "album", "artist", "custom":
	default:
		return "", fmt.Errorf("unsupported collection kind %q", kind)
	}
	switch source {
	case "manual", "filename", "plex", "jellyfin":
	default:
		return "", fmt.Errorf("unsupported collection source %q", source)
	}

	id := collectionID(kind, name)
	nowMs := time.Now().UTC().UnixMilli()
	_, err := exec.ExecContext(ctx, `INSERT INTO collections (id, name, kind, source, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, name) DO UPDATE SET
			updated_at_ms = excluded.updated_at_ms`,
		id, name, kind, source, nowMs, nowMs)
	if err != nil {
		return "", err
	}
	return id, nil
}

func UpdateCollectionGenres(ctx context.Context, exec Execer, collectionID string, genres []string) error {
	cleaned := normalizeGenres(genres)
	var encoded any
	if len(cleaned) > 0 {
		b, err := json.Marshal(cleaned)
		if err != nil {
			return err
		}
		encoded = string(b)
	}
	_, err := exec.ExecContext(ctx, `UPDATE collections SET genres_json = ?, updated_at_ms = ? WHERE id = ?`, encoded, time.Now().UTC().UnixMilli(), collectionID)
	return err
}

func normalizeGenres(genres []string) []string {
	out := make([]string, 0, len(genres))
	seen := map[string]struct{}{}
	for _, genre := range genres {
		genre = strings.TrimSpace(genre)
		if genre == "" {
			continue
		}
		key := strings.ToLower(genre)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, genre)
	}
	return out
}

func CollectionByID(ctx context.Context, conn Execer, id string) (*Collection, error) {
	return scanCollection(conn.QueryRowContext(ctx, `SELECT id, name, kind, source, genres_json, created_at_ms, updated_at_ms FROM collections WHERE id = ?`, id))
}

func ensureCollectionsTable(ctx context.Context, conn *sql.DB) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS collections (
		id            TEXT PRIMARY KEY,
		name          TEXT NOT NULL,
		kind          TEXT NOT NULL,
		source        TEXT NOT NULL,
		created_at_ms INTEGER NOT NULL,
		updated_at_ms INTEGER NOT NULL,
		UNIQUE (kind, name),
		CHECK (kind IN ('show', 'movie', 'album', 'artist', 'custom')),
		CHECK (source IN ('manual', 'filename', 'plex', 'jellyfin'))
	)`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "collections", "genres_json", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "media", "collection_id", "TEXT REFERENCES collections(id) ON DELETE SET NULL"); err != nil {
		return err
	}
	if err := backfillCollections(ctx, conn); err != nil {
		return err
	}
	return normalizeHalfSeasonCollections(ctx, conn)
}

func ensureColumn(ctx context.Context, conn *sql.DB, table, column, definition string) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+definition)
	return err
}

func backfillCollections(ctx context.Context, conn *sql.DB) error {
	rows, err := conn.QueryContext(ctx, `SELECT id, scheduling_group, COALESCE(media_kind, 'video') FROM media
		WHERE collection_id IS NULL AND scheduling_group IS NOT NULL AND scheduling_group != ''`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		mediaID string
		group   string
		kind    MediaKind
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.mediaID, &r.group, &r.kind); err != nil {
			return err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	return WithTx(ctx, conn, func(tx Execer) error {
		for _, r := range pending {
			kind := collectionKindForGroup(r.group, r.kind)
			name := collectionDisplayName(r.group)
			id, err := UpsertCollection(ctx, tx, name, kind, "filename")
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE media SET collection_id = ? WHERE id = ?`, id, r.mediaID); err != nil {
				return err
			}
		}
		return nil
	})
}

// normalizeHalfSeasonCollections rewrites show-level collection names that
// still carry the old half-season suffix (e.g. "Show S01 H1" → "Show") and
// merges H1/H2 fragments into one collection. Idempotent — safe to call on
// every startup.
func normalizeHalfSeasonCollections(ctx context.Context, conn *sql.DB) error {
	rows, err := conn.QueryContext(ctx, `SELECT id, name FROM collections WHERE kind = 'show'`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type rename struct {
		id   string
		name string
	}
	var renames []rename
	for rows.Next() {
		var r rename
		if err := rows.Scan(&r.id, &r.name); err != nil {
			return err
		}
		normalized := strings.TrimSpace(halfSeasonRe.ReplaceAllString(r.name, ""))
		if normalized != r.name && normalized != "" {
			renames = append(renames, rename{id: r.id, name: normalized})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(renames) == 0 {
		return nil
	}

	return WithTx(ctx, conn, func(tx Execer) error {
		for _, r := range renames {
			newID, err := UpsertCollection(ctx, tx, r.name, "show", "filename")
			if err != nil {
				return err
			}
			if newID == r.id {
				continue // already normalized (e.g. H2 renamed after H1 ran)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE media SET collection_id = ? WHERE collection_id = ?`, newID, r.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM collections WHERE id = ?`, r.id); err != nil {
				return err
			}
		}
		return nil
	})
}

func scanCollection(row scanner) (*Collection, error) {
	var c Collection
	var genresJSON sql.NullString
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.Source, &genresJSON, &c.CreatedAtMs, &c.UpdatedAtMs); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if genresJSON.Valid && genresJSON.String != "" {
		_ = json.Unmarshal([]byte(genresJSON.String), &c.Genres)
	}
	return &c, nil
}
