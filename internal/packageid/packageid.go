// Package packageid builds deterministic media package IDs shared by DB and
// packaging code.
package packageid

import (
	"regexp"
	"strings"
)

var unsafe = regexp.MustCompile(`[^a-z0-9]+`)

// For returns the stable package ID for a media/profile pair.
func For(mediaID, profile string) string {
	id := unsafe.ReplaceAllString(strings.ToLower(mediaID+"-"+profile), "-")
	id = strings.Trim(id, "-")
	if len(id) > 128 {
		id = id[:128]
	}
	return id
}
