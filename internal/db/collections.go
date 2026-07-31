package db

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

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
