package lcingest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// epRe matches `<show>...SnnEnn`. Show name is the prefix before the season
// marker, with `.`/`_`/`-` separators normalized to spaces. Year markers
// like `(2007)` and quality tags are stripped.
var (
	epRe          = regexp.MustCompile(`(?i)^(?P<show>.*?)[\s._\-]+s(?P<season>\d{1,2})e(?P<ep>\d{1,3})\b`)
	episodeCodeRe = regexp.MustCompile(`(?i)\bs(?P<season>\d{1,2})e(?P<ep>\d{1,3})\b`)
	yearTagRe     = regexp.MustCompile(`\s*\(?\d{4}\)?\s*$`)
	multiSpaceRe  = regexp.MustCompile(`\s+`)
	titleSepRe    = regexp.MustCompile(`[._]+`)
	// leadingYearRe matches a release year some scene names place right after
	// SxxExx (e.g. "...S03E01.2013.1080p..."), where DeriveTitle would otherwise
	// take it as the episode title. Constrained to 19xx/20xx to avoid eating a
	// title that legitimately starts with four digits.
	leadingYearRe = regexp.MustCompile(`^\(?(?:19|20)\d{2}\)?(?:\b|$)`)

	// junkMarkerRe matches the first quality / codec / source / release-group
	// token that signals the end of the human-readable title. Anything from
	// the match onward is dropped by stripJunk.
	junkMarkerRe = regexp.MustCompile(`(?i)\b(?:480p|576p|720p|1080p|2160p|4k|uhd|hdr10|hdr|dv|sdr|10bit|8bit|x264|x265|h264|h265|hevc|avc|xvid|divx|bluray|brrip|bdrip|web-dl|webdl|webrip|hdtv|hdrip|dvdrip|remux|dts-hd|dts-x|dts|truehd|atmos|eac3|ac3|aac|flac|amzn|nf|hulu|dsnp|repack|proper|extended|directors|uncut|unrated)\b`)
)

const movieGroupPrefix = "movie:"

// IsMovieGroup reports whether g was produced by DeriveSchedulingGroup for a
// movie file (as opposed to a TV episode or music track).
func IsMovieGroup(g string) bool { return strings.HasPrefix(g, movieGroupPrefix) }

// MovieGroupTitle strips the internal prefix and returns the human-readable
// title. Returns g unchanged if g is not a movie group.
func MovieGroupTitle(g string) string {
	if IsMovieGroup(g) {
		return g[len(movieGroupPrefix):]
	}
	return g
}

// DeriveSchedulingGroup returns a scheduling group string for a path:
//   - TV episodes ("...SnnEnn..."): "<Show>"
//   - Movies / non-episode files: "movie:<DerivedTitle>" (non-empty title only)
//   - Returns "" when no group can be derived (e.g. empty title, unparseable).
func DeriveSchedulingGroup(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	stem = titleSepRe.ReplaceAllString(stem, " ")
	stem = multiSpaceRe.ReplaceAllString(stem, " ")
	stem = strings.TrimSpace(stem)

	m := epRe.FindStringSubmatch(stem)
	if m == nil {
		if title := DeriveTitle(path); title != "" {
			return movieGroupPrefix + title
		}
		return ""
	}
	show := strings.TrimSpace(m[epRe.SubexpIndex("show")])
	show = strings.Trim(show, "-")
	show = yearTagRe.ReplaceAllString(show, "")
	show = strings.TrimSpace(show)
	if show == "" {
		return ""
	}

	if _, _, ok := ParseEpisodeCode(stem); !ok {
		return ""
	}

	return titleCase(show)
}

// ParseEpisodeCode extracts a season / episode code from a title or path.
func ParseEpisodeCode(s string) (season int, episode int, ok bool) {
	m := episodeCodeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	season, err := strconv.Atoi(m[episodeCodeRe.SubexpIndex("season")])
	if err != nil || season <= 0 {
		return 0, 0, false
	}
	episode, err = strconv.Atoi(m[episodeCodeRe.SubexpIndex("ep")])
	if err != nil || episode <= 0 {
		return 0, 0, false
	}
	return season, episode, true
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
		// Drop a release year sitting where the episode title would be, so
		// "...S03E01.2013.1080p..." titles as "Show S03E01", not "… — 2013".
		rest = leadingYearRe.ReplaceAllString(rest, "")
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
