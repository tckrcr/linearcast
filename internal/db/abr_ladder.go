package db

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

var StandardVideoABRLadder = []string{
	packageprofile.DefaultName,
	packageprofile.H2641080p20MbpsName,
}

// StandardVideoHDRABRLadder is the ABR ladder for HDR-capable channels. The
// hevc-copy-source rung preserves HDR metadata (PQ/HLG); H.264 rungs provide
// browser-safe SDR fallbacks.
var StandardVideoHDRABRLadder = []string{
	packageprofile.HEVCCopySourceName,
	packageprofile.HEVC2160p40MbpsHDRName,
	packageprofile.DefaultName,
}

// NormalizeABRLadder returns the ordered, de-duplicated profile list used for
// packaging and HLS variants. Invalid or empty JSON collapses to the required
// profile; callers do not support a legacy dual-shape ladder contract.
func NormalizeABRLadder(requiredProfile, rawJSON string) []string {
	requiredProfile = strings.TrimSpace(requiredProfile)
	if requiredProfile == "" {
		requiredProfile = DefaultPackageProfile
	}
	var raw []string
	_ = json.Unmarshal([]byte(strings.TrimSpace(rawJSON)), &raw)
	out := make([]string, 0, len(raw)+1)
	seen := map[string]bool{}
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if !seen[requiredProfile] {
		out = append(out, requiredProfile)
	}
	return out
}

func abrLadderValue(requiredProfile string, ladder []string) sql.NullString {
	normalized := NormalizeABRLadder(requiredProfile, mustMarshalStringSlice(ladder))
	if len(normalized) <= 1 && normalized[0] == strings.TrimSpace(requiredProfile) {
		return sql.NullString{}
	}
	b, _ := json.Marshal(normalized)
	return sql.NullString{String: string(b), Valid: true}
}

func ABRLadderEnabled(requiredProfile string, ladder []string) bool {
	normalized := NormalizeABRLadder(requiredProfile, mustMarshalStringSlice(ladder))
	return len(normalized) > 1
}

func mustMarshalStringSlice(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}
