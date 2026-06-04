package db

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	if err := ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return path
}

func TestVerifySchemaMatches(t *testing.T) {
	path := newTestDB(t)
	conn, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer conn.Close()
	if err := VerifySchema(context.Background(), conn); err != nil {
		t.Fatalf("verify: %v", err)
	}
}
