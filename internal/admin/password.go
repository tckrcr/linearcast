package admin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"golang.org/x/crypto/bcrypt"

	"github.com/tckrcr/linearcast/internal/db"
)

const defaultAdminPassword = "linearcast"

// EnsurePassword seeds the packaged password for a fresh database and returns
// the persisted hash and must-change flag used by the composed admin runtime.
func EnsurePassword(ctx context.Context, conn *sql.DB) (hash string, mustChange bool, err error) {
	existing, exists, err := db.GetAdminPasswordHash(ctx, conn)
	if err != nil {
		return "", false, fmt.Errorf("read admin password hash: %w", err)
	}
	if !exists {
		h, err := bcrypt.GenerateFromPassword([]byte(defaultAdminPassword), bcryptCost)
		if err != nil {
			return "", false, fmt.Errorf("hash admin password: %w", err)
		}
		if err := db.SetAdminPasswordHash(ctx, conn, string(h)); err != nil {
			return "", false, err
		}
		if err := db.SetAdminPasswordMustChange(ctx, conn, true); err != nil {
			return "", false, err
		}
		slog.Info("first-run: default admin password set — sign in and change it immediately")
		return string(h), true, nil
	}
	mustChange, err = db.AdminPasswordMustChange(ctx, conn)
	if err != nil {
		return "", false, fmt.Errorf("read must-change flag: %w", err)
	}
	return existing, mustChange, nil
}
