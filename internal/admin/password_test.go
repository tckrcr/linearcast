package admin

import (
	"context"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestEnsurePasswordSeedsAndReusesPersistedPassword(t *testing.T) {
	_, conn := testAdminApp(t)
	hash, mustChange, err := EnsurePassword(context.Background(), conn)
	if err != nil {
		t.Fatalf("seed password: %v", err)
	}
	if !mustChange || bcrypt.CompareHashAndPassword([]byte(hash), []byte(defaultAdminPassword)) != nil {
		t.Fatalf("unexpected seeded password state: mustChange=%v", mustChange)
	}

	again, againMustChange, err := EnsurePassword(context.Background(), conn)
	if err != nil {
		t.Fatalf("reload password: %v", err)
	}
	if again != hash || !againMustChange {
		t.Fatalf("persisted state changed: hashEqual=%v mustChange=%v", again == hash, againMustChange)
	}
}
