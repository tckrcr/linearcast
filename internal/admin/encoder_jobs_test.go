package admin

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

// TestClassifyJobOpErrorMapping locks the HTTP status each encoder job-op
// sentinel maps to. Errors are wrapped with %w exactly as the db and admin
// helpers return them, so errors.Is resolves the sentinel through the chain.
// Pairs with db.TestEncoderJobOpErrorWireText, which proves the real helpers
// actually wrap these sentinels (and preserve the wire text).
func TestClassifyJobOpErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, http.StatusOK},
		{"not registered", fmt.Errorf("encoder x %w", db.ErrEncoderNotRegistered), http.StatusUnauthorized},
		{"not registered or revoked", fmt.Errorf("encoder x %w or revoked", db.ErrEncoderNotRegistered), http.StatusUnauthorized},
		{"revoked", fmt.Errorf("encoder x is %w", db.ErrEncoderRevoked), http.StatusForbidden},
		{"no active lease", fmt.Errorf("%w for package p", db.ErrNoActiveLease), http.StatusConflict},
		{"leased by other", fmt.Errorf("package p is %w y, not z", db.ErrPackageLeasedByOther), http.StatusConflict},
		{"not processing", fmt.Errorf("package p is %w", db.ErrPackageNotProcessing), http.StatusConflict},
		{"package not found", fmt.Errorf("package p %w", db.ErrPackageNotFound), http.StatusConflict},
		{"unknown", errors.New("some other failure"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := classifyJobOpError(tc.err); got != tc.want {
			t.Errorf("%s: classifyJobOpError = %d, want %d", tc.name, got, tc.want)
		}
	}
}
