package lcingest

import "testing"

func TestDeriveSchedulingGroup(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/media/Harbor.Lights.S03E07.The.Title.mkv", "Harbor Lights"},
		{"/media/Harbor Lights - S03E04 - Title.mkv", "Harbor Lights"},
		{"/media/Harbor_Lights_S01E06_Title.mkv", "Harbor Lights"},
		{"/media/Harbor_Lights_S01E07_Title.mkv", "Harbor Lights"},
		{"/media/city.watch.s02e15.dinner.party.1080p.mkv", "City Watch"},
		// Year tag stripped from show name.
		{"/media/Northern.Skies.2005.S04E01.mkv", "Northern Skies"},
		// Movies get a "movie:<title>" group derived from their filename.
		{"/media/Dream.Circuit.2010.1080p.mkv", "movie:Dream Circuit (2010)"},
		// Files with no parseable title fall back to titlecased stem.
		{"/media/random.mkv", "movie:Random"},
	}
	for _, c := range cases {
		got := DeriveSchedulingGroup(c.path)
		if got != c.want {
			t.Errorf("DeriveSchedulingGroup(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestParseEpisodeCode(t *testing.T) {
	cases := []struct {
		input      string
		wantSeason int
		wantEp     int
		wantOK     bool
	}{
		{"Harbor Lights S03E07 — The Title", 3, 7, true},
		{"/media/city.watch.s02e15.dinner.party.1080p.mkv", 2, 15, true},
		{"Northern Skies S10E02", 10, 2, true},
		{"Movie Bucket", 0, 0, false},
		{"Harbor Lights S3 H2", 0, 0, false},
	}
	for _, c := range cases {
		gotSeason, gotEp, gotOK := ParseEpisodeCode(c.input)
		if gotSeason != c.wantSeason || gotEp != c.wantEp || gotOK != c.wantOK {
			t.Errorf("ParseEpisodeCode(%q) = (%d, %d, %v), want (%d, %d, %v)",
				c.input, gotSeason, gotEp, gotOK, c.wantSeason, c.wantEp, c.wantOK)
		}
	}
}

func TestDeriveTitle(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		// Nested show/season path.
		{
			"/data/media/tv/ShowName/Season 1/ShowName.S01E03.Episode.Name.1080p.mkv",
			"Showname S01E03 — Episode Name",
		},
		// Standard episode patterns.
		{"/m/Harbor.Lights.S03E07.The.Lighthouse.1080p.BluRay.x264-GROUP.mkv", "Harbor Lights S03E07 — The Lighthouse"},
		{"/m/city.watch.s02e15.dinner.party.720p.HDTV.x264.mkv", "City Watch S02E15 — Dinner Party"},
		{"/m/Harbor Lights - S03E04 - The Reunion.mkv", "Harbor Lights S03E04 — The Reunion"},
		// Episode with no human title between SnnEnn and quality marker.
		{"/m/ShowName.S01E03.1080p.mkv", "Showname S01E03"},
		{"/m/ShowName.S01E03.mkv", "Showname S01E03"},
		// Release year sits where the episode title would be — must not become
		// the title.
		{"/srv/media/tv/Example.Show.S03E01.2013.1080p.WEB-DL.x264.DDP5.1-Scene.mkv", "Example Show S03E01"},
		// Year followed by a real title: strip only the year, keep the title.
		{"/m/ShowName.S02E05.2014.The.Real.Title.1080p.mkv", "Showname S02E05 — The Real Title"},
		// Show name carries a year tag.
		{"/m/Northern.Skies.2005.S04E01.Old.Friends.1080p.mkv", "Northern Skies S04E01 — Old Friends"},
		// Show with year in parens; entire quality block also parenthesized.
		{"/m/Signal Keepers (2019) S01E01 (2160p NF WEB-DL Hybrid H265 DV HDR DDP Atmos 5.1 English - GROUP).mkv", "Signal Keepers S01E01"},
		// Movies: junk stripped, year promoted to "(YYYY)".
		{"/m/Dream.Circuit.2010.1080p.BluRay.x264-GROUP.mkv", "Dream Circuit (2010)"},
		{"/m/Road.Through.Ember.2015.1080p.BluRay.x264.DTS-HD.MA.7.1-GROUP.mkv", "Road Through Ember (2015)"},
		// Movie without quality markers — falls back to titlecasing the stem.
		{"/m/random.mkv", "Random"},
	}
	for _, c := range cases {
		got := DeriveTitle(c.path)
		if got != c.want {
			t.Errorf("DeriveTitle(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
