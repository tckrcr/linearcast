package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLocalMediaSourceCRUD(t *testing.T) {
	conn, err := OpenReadWrite(filepath.Join(t.TempDir(), "linearcast.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	created, err := UpsertLocalMediaSource(context.Background(), conn, LocalMediaSource{
		Name:      "Local Movies",
		MediaKind: "movies",
		Paths:     []string{"/media/movies", "/media/movies", "/media/more"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.Name != "Local Movies" || created.MediaKind != "movies" {
		t.Fatalf("created=%+v", created)
	}
	if got := created.Paths; len(got) != 2 || got[0] != "/media/movies" || got[1] != "/media/more" {
		t.Fatalf("paths=%v", got)
	}

	updated, err := UpsertLocalMediaSource(context.Background(), conn, LocalMediaSource{
		ID:        created.ID,
		Name:      "TV",
		MediaKind: "shows",
		Paths:     []string{"/media/tv"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.CreatedAtMs != created.CreatedAtMs {
		t.Fatalf("created_at changed: %d -> %d", created.CreatedAtMs, updated.CreatedAtMs)
	}
	if updated.Name != "TV" || updated.Paths[0] != "/media/tv" {
		t.Fatalf("updated=%+v", updated)
	}

	list, err := ListLocalMediaSources(context.Background(), conn)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list=%+v", list)
	}

	deleted, err := DeleteLocalMediaSource(context.Background(), conn, created.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatal("delete returned false")
	}
	list, err = ListLocalMediaSources(context.Background(), conn)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list after delete=%+v", list)
	}
}
