package lcingest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// halfBoundaryEpisode splits a season into halves at this episode number.
// E1..E6 -> H1; E7+ -> H2. Deterministic and easy to override per-media via
// the maintenance `set-group` command when a season's actual midpoint differs.
const halfBoundaryEpisode = 6

// epRe matches `<show>...SnnEnn`. Show name is the prefix before the season
// marker, with `.`/`_`/`-` separators normalized to spaces. Year markers
// like `(2007)` and quality tags are stripped.
var (
	epRe         = regexp.MustCompile(`(?i)^(?P<show>.*?)[\s._\-]+s(?P<season>\d{1,2})e(?P<ep>\d{1,3})\b`)
	yearTagRe    = regexp.MustCompile(`\s*\(?\d{4}\)?\s*$`)
	multiSpaceRe = regexp.MustCompile(`\s+`)
	titleSepRe   = regexp.MustCompile(`[._]+`)

	// schedGroupRe matches the persisted scheduling_group format produced by
	// DeriveSchedulingGroup: "<Show> S<NN> H<1|2>". The show capture is greedy
	// so trailing spaces are trimmed by the caller.
	schedGroupRe = regexp.MustCompile(`^(?P<show>.+) S(?P<season>\d{2}) H(?P<half>[12])$`)

	// junkMarkerRe matches the first quality / codec / source / release-group
	// token that signals the end of the human-readable title. Anything from
	// the match onward is dropped by stripJunk.
	junkMarkerRe = regexp.MustCompile(`(?i)\b(?:480p|576p|720p|1080p|2160p|4k|uhd|hdr10|hdr|dv|sdr|10bit|8bit|x264|x265|h264|h265|hevc|avc|xvid|divx|bluray|brrip|bdrip|web-dl|webdl|webrip|hdtv|hdrip|dvdrip|remux|dts-hd|dts-x|dts|truehd|atmos|eac3|ac3|aac|flac|amzn|nf|hulu|dsnp|repack|proper|extended|directors|uncut|unrated)\b`)
)

// DeriveSchedulingGroup returns a "<Show> S<NN> H<1|2>" group string for a
// path like `.../Mad.Men.S03E07.Title.mkv`, or "" when the filename doesn't
// look like a TV episode (movies, unparseable names).
func DeriveSchedulingGroup(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	stem = titleSepRe.ReplaceAllString(stem, " ")
	stem = multiSpaceRe.ReplaceAllString(stem, " ")
	stem = strings.TrimSpace(stem)

	m := epRe.FindStringSubmatch(stem)
	if m == nil {
		return ""
	}
	show := strings.TrimSpace(m[epRe.SubexpIndex("show")])
	show = strings.Trim(show, "-")
	show = yearTagRe.ReplaceAllString(show, "")
	show = strings.TrimSpace(show)
	if show == "" {
		return ""
	}

	season, err := strconv.Atoi(m[epRe.SubexpIndex("season")])
	if err != nil || season <= 0 {
		return ""
	}
	episode, err := strconv.Atoi(m[epRe.SubexpIndex("ep")])
	if err != nil || episode <= 0 {
		return ""
	}

	half := 1
	if episode > halfBoundaryEpisode {
		half = 2
	}
	return fmt.Sprintf("%s S%02d H%d", titleCase(show), season, half)
}

// ParseSchedulingGroup splits a "<Show> S<NN> H<1|2>" group string back into
// its show / season / half parts. Returns ok=false if the string isn't in
// the expected format (e.g. a singleton/movie row that uses the raw path).
func ParseSchedulingGroup(group string) (show string, season int, half int, ok bool) {
	m := schedGroupRe.FindStringSubmatch(strings.TrimSpace(group))
	if m == nil {
		return "", 0, 0, false
	}
	show = strings.TrimSpace(m[schedGroupRe.SubexpIndex("show")])
	if show == "" {
		return "", 0, 0, false
	}
	season, err := strconv.Atoi(m[schedGroupRe.SubexpIndex("season")])
	if err != nil || season <= 0 {
		return "", 0, 0, false
	}
	half, err = strconv.Atoi(m[schedGroupRe.SubexpIndex("half")])
	if err != nil || (half != 1 && half != 2) {
		return "", 0, 0, false
	}
	return show, season, half, true
}

// DeriveTitle returns a human-friendly title for a media path.
//
//   - TV episodes ("...S01E03..."): "Show S01E03 — Episode Name"
//     (the " — Episode Name" suffix is omitted when the filename has no
//     episode title between SnnEnn and the first quality marker).
//   - Movies / unparseable: quality / codec / release-group tokens are
//     stripped, and a 4-digit year tag at the end is re-attached as
//     "Title (YYYY)".
//   - Fallback: if nothing parses cleanly, returns the bare filename stem
//     with separators normalized to spaces.
func DeriveTitle(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	stem = titleSepRe.ReplaceAllString(stem, " ")
	stem = multiSpaceRe.ReplaceAllString(stem, " ")
	stem = strings.TrimSpace(stem)
	if stem == "" {
		return ""
	}

	if loc := epRe.FindStringSubmatchIndex(stem); loc != nil {
		showRaw := stem[loc[2]:loc[3]]
		showRaw = strings.Trim(showRaw, " -")
		showRaw = yearTagRe.ReplaceAllString(showRaw, "")
		showRaw = strings.TrimSpace(showRaw)
		show := titleCase(showRaw)

		season, sErr := strconv.Atoi(stem[loc[4]:loc[5]])
		ep, eErr := strconv.Atoi(stem[loc[6]:loc[7]])

		rest := stripJunk(stem[loc[1]:])
		rest = strings.Trim(rest, " -()[]{}·.,")
		epTitle := titleCase(rest)

		if sErr == nil && eErr == nil && season > 0 && ep > 0 {
			seCode := fmt.Sprintf("S%02dE%02d", season, ep)
			switch {
			case show != "" && epTitle != "":
				return fmt.Sprintf("%s %s — %s", show, seCode, epTitle)
			case show != "":
				return fmt.Sprintf("%s %s", show, seCode)
			case epTitle != "":
				return fmt.Sprintf("%s — %s", seCode, epTitle)
			default:
				return seCode
			}
		}
		// Season/ep parse failed; fall through to movie-style cleanup.
	}

	cleaned := stripJunk(stem)
	cleaned = strings.Trim(cleaned, " -()[]{}·.,")
	cleaned = multiSpaceRe.ReplaceAllString(cleaned, " ")
	if cleaned == "" {
		return titleCase(stem)
	}

	var year string
	if m := yearTagRe.FindString(cleaned); m != "" {
		year = strings.Trim(m, " ()")
		cleaned = yearTagRe.ReplaceAllString(cleaned, "")
		cleaned = strings.TrimSpace(cleaned)
	}
	title := titleCase(cleaned)
	if year != "" && title != "" {
		return fmt.Sprintf("%s (%s)", title, year)
	}
	if title == "" {
		return titleCase(stem)
	}
	return title
}

// stripJunk truncates the string at the first quality / codec / release-group
// marker, returning everything before it.
func stripJunk(s string) string {
	if loc := junkMarkerRe.FindStringIndex(s); loc != nil {
		return s[:loc[0]]
	}
	return s
}

// titleCase capitalizes the first letter of each space-separated word.
// Preserves all-uppercase tokens (e.g. "MCU", "II") that are already cased.
func titleCase(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		if p == "" {
			continue
		}
		if p == strings.ToUpper(p) && len(p) > 1 {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	return strings.Join(parts, " ")
}
