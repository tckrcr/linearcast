// Package subtitlepolicy resolves requested subtitle behavior into the concrete
// action an encoder/playback backend should apply for one media item.
package subtitlepolicy

import (
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

type Mode string

const (
	ModeOff        Mode = "off"
	ModeForcedBurn Mode = "forced_burn"
)

type Action string

const (
	ActionNone Action = "none"
	ActionBurn Action = "burn"
)

type Reason string

const (
	ReasonModeOff           Reason = "mode_off"
	ReasonNoLanguage        Reason = "no_language"
	ReasonProfileCannotBurn Reason = "profile_cannot_burn"
	ReasonNoForcedTrack     Reason = "no_forced_track"
	ReasonForcedDisposition Reason = "forced_disposition"
	ReasonUnsupportedMode   Reason = "unsupported_mode"
)

type Request struct {
	Mode     Mode
	Language string
}

type Decision struct {
	Action      Action
	StreamIndex int
	Language    string
	// Source distinguishes how a burn composites: embedded_bitmap tracks
	// overlay as video, embedded_text tracks render via libass.
	Source db.TrackSource
	Reason Reason
}

func None(reason Reason) Decision {
	return Decision{Action: ActionNone, StreamIndex: -1, Reason: reason}
}

// Resolve returns the concrete subtitle action for one media/profile pair.
// The decision is a deterministic effect of the source's tracks and the
// profile, not a separate package-identity input: (media, profile) alone
// identify the package, and the same pair always resolves to the same
// decision and the same bytes.
func Resolve(req Request, profile packageprofile.Profile, tracks []db.PackageTrack) Decision {
	lang := strings.ToLower(strings.TrimSpace(req.Language))
	switch req.Mode {
	case "", ModeOff:
		return None(ReasonModeOff)
	case ModeForcedBurn:
		if lang == "" {
			return None(ReasonNoLanguage)
		}
		if profile.Video.Mode != packageprofile.VideoModeTranscode {
			return None(ReasonProfileCannotBurn)
		}
		// A bitmap (PGS/VOBSUB) forced track outranks a text one: it is the
		// as-mastered presentation, and keeping it first leaves existing
		// bitmap-burn decisions unchanged.
		var text *db.PackageTrack
		for i, t := range tracks {
			if t.Kind != "subtitle" || !t.Forced || !strings.EqualFold(t.Language, lang) {
				continue
			}
			switch t.Source {
			case db.TrackSourceEmbeddedBitmap:
				return burnDecision(t)
			case db.TrackSourceEmbedded:
				if text == nil {
					text = &tracks[i]
				}
			}
		}
		if text != nil {
			return burnDecision(*text)
		}
		return None(ReasonNoForcedTrack)
	default:
		return None(ReasonUnsupportedMode)
	}
}

func burnDecision(t db.PackageTrack) Decision {
	return Decision{
		Action:      ActionBurn,
		StreamIndex: t.StreamIndex,
		Language:    strings.ToLower(t.Language),
		Source:      t.Source,
		Reason:      ReasonForcedDisposition,
	}
}

// BurnsForcedLanguage reports whether profile bakes a forced track of the given
// language into the video, so callers must not also surface it as a soft forced
// subtitle rendition. It mirrors the ModeForcedBurn arm of Resolve without a
// track list: Resolve additionally requires the forced track to exist, which a
// caller advertising a rendition establishes by holding one.
func BurnsForcedLanguage(profile packageprofile.Profile, language string) bool {
	if Mode(profile.Subtitles.Mode) != ModeForcedBurn || profile.Video.Mode != packageprofile.VideoModeTranscode {
		return false
	}
	lang := strings.ToLower(strings.TrimSpace(language))
	return lang != "" && lang == strings.ToLower(strings.TrimSpace(profile.Subtitles.Language))
}
