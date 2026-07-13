package admin

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/routes"
)

// IPTV / EPG publishing. These two endpoints expose the same enabled-guide
// channel set as /api/guide and /api/playable-sources in the standard formats
// that IPTV apps and DVR frontends consume:
//
//	GET /api/m3u   — an #EXTM3U playlist (one entry per channel + stream URL)
//	GET /api/xmltv — an XMLTV electronic program guide
//
// Both are public viewer-tier routes (see isPublicRoute): the whole viewer
// surface — guide, playable-sources, and the /hls playback server — is already
// unauthenticated, so an external client can fetch the playlist, the guide, and
// the streams without a session. tvg-id in the M3U matches the channel id in the
// XMLTV so clients link the two automatically.

// requestBaseURL reconstructs the scheme://host the client used to reach this
// request. nginx forwards both Host and X-Forwarded-Proto to /api/, so behind
// the reverse proxy this yields the public origin (e.g. https://tv.example.com)
// that external IPTV/EPG clients can resolve. Stream URLs are emitted absolute
// because IPTV/DVR tuners (Jellyfin Live TV, Plex DVR) don't reliably resolve
// relative manifest references in an M3U.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		// X-Forwarded-Proto may be a comma-separated chain; the first hop is ours.
		scheme = strings.TrimSpace(strings.SplitN(proto, ",", 2)[0])
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// channelManifestPath returns the channel's HLS manifest path, branching on the
// external/VOD split the same way playable-sources does.
func channelManifestPath(ch db.Channel) string {
	if ch.UpstreamHLSURL != nil {
		return routes.ExternalHLSManifest(ch.ID)
	}
	return routes.HLSManifest(ch.ID)
}

// handleM3U emits an #EXTM3U playlist of all enabled, non-hidden channels.
func (a *App) handleM3U(w http.ResponseWriter, r *http.Request) {
	channels, err := db.EnabledGuideChannels(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	base := requestBaseURL(r)

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, ch := range channels {
		name := ch.DisplayName
		if name == "" {
			name = ch.ID
		}
		b.WriteString("#EXTINF:-1")
		b.WriteString(" tvg-id=" + m3uAttr(ch.ID))
		b.WriteString(" tvg-name=" + m3uAttr(name))
		if ch.ArtworkURL != "" {
			b.WriteString(" tvg-logo=" + m3uAttr(ch.ArtworkURL))
		}
		b.WriteString(" group-title=" + m3uAttr("linearcast"))
		b.WriteString("," + m3uText(name) + "\n")
		b.WriteString(base + channelManifestPath(ch) + "\n")
	}

	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Disposition", `inline; filename="linearcast.m3u"`)
	_, _ = io.WriteString(w, b.String())
}

// m3uAttr quotes an #EXTINF attribute value. The M3U directory format has no
// escape syntax for the double-quote delimiter, so embedded quotes are demoted
// to apostrophes and line breaks flattened to spaces.
func m3uAttr(s string) string {
	return `"` + m3uReplacer.Replace(s) + `"`
}

// m3uText sanitizes free text that follows the EXTINF comma (the track title).
func m3uText(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}

var m3uReplacer = strings.NewReplacer(`"`, "'", "\n", " ", "\r", " ")

// XMLTV document model. encoding/xml handles all escaping; field order places
// every <channel> before every <programme>, as the DTD requires.
type xmltvDoc struct {
	XMLName    xml.Name         `xml:"tv"`
	Generator  string           `xml:"generator-info-name,attr"`
	Channels   []xmltvChannel   `xml:"channel"`
	Programmes []xmltvProgramme `xml:"programme"`
}

type xmltvChannel struct {
	ID          string     `xml:"id,attr"`
	DisplayName string     `xml:"display-name"`
	Icon        *xmltvIcon `xml:"icon,omitempty"`
}

type xmltvIcon struct {
	Src string `xml:"src,attr"`
}

type xmltvProgramme struct {
	Start      string          `xml:"start,attr"`
	Stop       string          `xml:"stop,attr"`
	Channel    string          `xml:"channel,attr"`
	Title      string          `xml:"title"`
	SubTitle   string          `xml:"sub-title,omitempty"`
	Desc       string          `xml:"desc,omitempty"`
	Icon       *xmltvIcon      `xml:"icon,omitempty"`
	Categories []string        `xml:"category,omitempty"`
	Rating     *xmltvRating    `xml:"rating,omitempty"`
	EpisodeNum *xmltvEpisodeID `xml:"episode-num,omitempty"`
}

type xmltvRating struct {
	Value string `xml:"value"`
}

type xmltvEpisodeID struct {
	System string `xml:"system,attr"`
	Value  string `xml:",chardata"`
}

// handleXMLTV emits an XMLTV guide: a <channel> per enabled channel followed by
// a <programme> per scheduled entry within the window. ?hours=N (default and
// max guideMaxHours) bounds the horizon, mirroring handleGuide. External
// channels appear as <channel> only — they have no built linearcast schedule.
func (a *App) handleXMLTV(w http.ResponseWriter, r *http.Request) {
	fromMs := a.now().UTC().UnixMilli()
	horizonHours := guideMaxHours
	if h := r.URL.Query().Get("hours"); h != "" {
		if _, err := fmt.Sscanf(h, "%d", &horizonHours); err != nil || horizonHours <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_hours", "hours must be a positive integer")
			return
		}
	}
	if horizonHours > guideMaxHours {
		horizonHours = guideMaxHours
	}
	toMs := fromMs + int64(horizonHours)*3600*1000
	base := requestBaseURL(r)

	channels, err := db.EnabledGuideChannels(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	doc := xmltvDoc{Generator: "linearcast"}
	for _, ch := range channels {
		name := ch.DisplayName
		if name == "" {
			name = ch.ID
		}
		entry := xmltvChannel{ID: ch.ID, DisplayName: name}
		if ch.ArtworkURL != "" {
			entry.Icon = &xmltvIcon{Src: ch.ArtworkURL}
		}
		doc.Channels = append(doc.Channels, entry)
	}
	for _, ch := range channels {
		if ch.UpstreamHLSURL != nil {
			continue
		}
		raw, err := db.ScheduleWindowEnriched(r.Context(), a.dbConn, ch.ID, fromMs, toMs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		for _, e := range raw {
			prog := xmltvProgramme{
				Start:   xmltvTime(e.StartMs),
				Stop:    xmltvTime(e.StartMs + e.DurationMs),
				Channel: ch.ID,
				Title:   e.Title,
				Desc:    e.Description,
			}
			// For episodic media the collection is the series; the per-entry
			// title is the episode. Movies carry no collection, so the title
			// stays as-is with no sub-title.
			if e.CollectionName != "" {
				prog.Title = e.CollectionName
				prog.SubTitle = e.Title
			}
			if num := xmltvEpisodeNum(e.SeasonNumber, e.EpisodeNumber); num != "" {
				prog.EpisodeNum = &xmltvEpisodeID{System: "onscreen", Value: num}
			}
			if e.ThumbPath != "" {
				prog.Icon = &xmltvIcon{Src: mediaArtworkURL(base, e.MediaID)}
			}
			prog.Categories = append(prog.Categories, e.Genres...)
			if e.ContentRating != "" {
				prog.Rating = &xmltvRating{Value: e.ContentRating}
			}
			doc.Programmes = append(doc.Programmes, prog)
		}
	}

	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "xml_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Disposition", `inline; filename="linearcast.xml"`)
	_, _ = io.WriteString(w, xml.Header)
	_, _ = w.Write(out)
	_, _ = io.WriteString(w, "\n")
}

func mediaArtworkURL(base, mediaID string) string {
	return strings.TrimRight(base, "/") + routes.MediaArtwork(mediaID)
}

// xmltvTime formats a unix-ms instant as XMLTV's "YYYYMMDDHHMMSS +0000".
func xmltvTime(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("20060102150405 -0700")
}

// xmltvEpisodeNum renders an "onscreen" SxxExx label from optional season and
// episode numbers; returns "" when neither is known.
func xmltvEpisodeNum(season, episode *int64) string {
	var b strings.Builder
	if season != nil {
		fmt.Fprintf(&b, "S%02d", *season)
	}
	if episode != nil {
		fmt.Fprintf(&b, "E%02d", *episode)
	}
	return b.String()
}
