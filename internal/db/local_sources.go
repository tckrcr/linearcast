package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type LocalMediaSource struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	MediaKind   string   `json:"mediaKind"`
	Paths       []string `json:"paths"`
	CreatedAtMs int64    `json:"createdAtMs"`
	UpdatedAtMs int64    `json:"updatedAtMs"`
}

func ListLocalMediaSources(ctx context.Context, conn *sql.DB) ([]LocalMediaSource, error) {
	out, err := queryRows(ctx, conn, scanLocalMediaSource, `
		SELECT id, name, media_kind, created_at_ms, updated_at_ms
		FROM local_media_sources
		ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list local sources: %w", err)
	}
	for i := range out {
		paths, err := LocalMediaSourcePaths(ctx, conn, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Paths = paths
	}
	return out, nil
}

func GetLocalMediaSource(ctx context.Context, conn *sql.DB, id string) (*LocalMediaSource, error) {
	row := conn.QueryRowContext(ctx, `
		SELECT id, name, media_kind, created_at_ms, updated_at_ms
		FROM local_media_sources
		WHERE id = ?`, id)
	var s LocalMediaSource
	if err := row.Scan(&s.ID, &s.Name, &s.MediaKind, &s.CreatedAtMs, &s.UpdatedAtMs); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get local source: %w", err)
	}
	paths, err := LocalMediaSourcePaths(ctx, conn, s.ID)
	if err != nil {
		return nil, err
	}
	s.Paths = paths
	return &s, nil
}

func UpsertLocalMediaSource(ctx context.Context, conn *sql.DB, s LocalMediaSource) (*LocalMediaSource, error) {
	s.ID = strings.TrimSpace(s.ID)
	s.Name = strings.TrimSpace(s.Name)
	s.MediaKind = strings.TrimSpace(s.MediaKind)
	if s.ID == "" {
		s.ID = uuid.New().String()
	}
	if s.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if s.MediaKind != "movies" && s.MediaKind != "shows" && s.MediaKind != "music" {
		return nil, fmt.Errorf("mediaKind must be movies, shows, or music")
	}
	paths := normalizeLocalSourcePaths(s.Paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one path is required")
	}

	now := time.Now().UTC().UnixMilli()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO local_media_sources (id, name, media_kind, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			media_kind = excluded.media_kind,
			updated_at_ms = excluded.updated_at_ms`,
		s.ID, s.Name, s.MediaKind, now, now); err != nil {
		return nil, fmt.Errorf("upsert local source: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM local_media_source_paths WHERE source_id = ?`, s.ID); err != nil {
		return nil, fmt.Errorf("replace local source paths: %w", err)
	}
	for i, path := range paths {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO local_media_source_paths (source_id, path, sort_key)
			VALUES (?, ?, ?)`, s.ID, path, i); err != nil {
			return nil, fmt.Errorf("insert local source path: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return GetLocalMediaSource(ctx, conn, s.ID)
}

func DeleteLocalMediaSource(ctx context.Context, conn *sql.DB, id string) (bool, error) {
	res, err := conn.ExecContext(ctx, `DELETE FROM local_media_sources WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return false, fmt.Errorf("delete local source: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func LocalMediaSourcePaths(ctx context.Context, conn *sql.DB, id string) ([]string, error) {
	paths, err := queryRows(ctx, conn, scanString, `
		SELECT path
		FROM local_media_source_paths
		WHERE source_id = ?
		ORDER BY sort_key, path`, id)
	if err != nil {
		return nil, fmt.Errorf("local source paths: %w", err)
	}
	return paths, nil
}

func scanLocalMediaSource(row scanner) (LocalMediaSource, error) {
	var s LocalMediaSource
	err := row.Scan(&s.ID, &s.Name, &s.MediaKind, &s.CreatedAtMs, &s.UpdatedAtMs)
	return s, err
}

func normalizeLocalSourcePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
