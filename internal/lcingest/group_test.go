package lcingest

import "testing"

func TestDeriveSchedulingGroup(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/media/Mad.Men.S03E07.The.Title.mkv", "Mad Men S03 H2"},
		{"/media/Mad Men - S03E04 - Title.mkv", "Mad Men S03 H1"},
		{"/media/Mad_Men_S01E06_Title.mkv", "Mad Men S01 H1"},
		{"/media/Mad_Men_S01E07_Title.mkv", "Mad Men S01 H2"},
		{"/media/the.office.s02e15.dinner.party.1080p.mkv", "The Office S02 H2"},
		// Year tag stripped from show name.
		{"/media/Doctor.Who.2005.S04E01.mkv", "Doctor Who S04 H1"},
		// Movies / unparseable filenames return "" → solo bucket.
		{"/media/Inception.2010.1080p.mkv", ""},
		{"/media/random.mkv", ""},
	}
	for _, c := range cases {
		got := DeriveSchedulingGroup(c.path)
		if got != c.want {
			t.Errorf("DeriveSchedulingGroup(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestParseSchedulingGroup(t *testing.T) {
	cases := []struct {
		group      string
		wantShow   string
		wantSeason int
		wantHalf   int
		wantOK     bool
	}{
		{"Mad Men S03 H2", "Mad Men", 3, 2, true},
		{"The Office S02 H1", "The Office", 2, 1, true},
		{"Doctor Who S10 H2", "Doctor Who", 10, 2, true},
		{"Movie Bucket", "", 0, 0, false},
		{"Mad Men S3 H2", "", 0, 0, false},
		{"Mad Men S03 H3", "", 0, 0, false},
	}
	for _, c := range cases {
		gotShow, gotSeason, gotHalf, gotOK := ParseSchedulingGroup(c.group)
		if gotShow != c.wantShow || gotSeason != c.wantSeason || gotHalf != c.wantHalf || gotOK != c.wantOK {
			t.Errorf("ParseSchedulingGroup(%q) = (%q, %d, %d, %v), want (%q, %d, %d, %v)",
				c.group, gotShow, gotSeason, gotHalf, gotOK, c.wantShow, c.wantSeason, c.wantHalf, c.wantOK)
		}
	}
}

func TestDeriveTitle(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		// Real ingest path the user hit.
		{
			"/data/media/tv/ShowName/Season 1/ShowName.S01E03.Episode.Name.1080p.mkv",
			"Showname S01E03 — Episode Name",
		},
		// Standard episode patterns.
		{"/m/Mad.Men.S03E07.The.Suitcase.1080p.BluRay.x264-FLEET.mkv", "Mad Men S03E07 — The Suitcase"},
		{"/m/the.office.s02e15.dinner.party.720p.HDTV.x264.mkv", "The Office S02E15 — Dinner Party"},
		{"/m/Mad Men - S03E04 - The Arrangements.mkv", "Mad Men S03E04 — The Arrangements"},
		// Episode with no human title between SnnEnn and quality marker.
		{"/m/ShowName.S01E03.1080p.mkv", "Showname S01E03"},
		{"/m/ShowName.S01E03.mkv", "Showname S01E03"},
		// Show name carries a year tag.
		{"/m/Doctor.Who.2005.S04E01.Partners.in.Crime.1080p.mkv", "Doctor Who S04E01 — Partners In Crime"},
		// Show with year in parens; entire quality block also parenthesized.
		{"/m/The Witcher (2019) S01E01 (2160p NF WEB-DL Hybrid H265 DV HDR DDP Atmos 5.1 English - HONE).mkv", "The Witcher S01E01"},
		// Movies: junk stripped, year promoted to "(YYYY)".
		{"/m/Inception.2010.1080p.BluRay.x264-RARBG.mkv", "Inception (2010)"},
		{"/m/Mad.Max.Fury.Road.2015.1080p.BluRay.x264.DTS-HD.MA.7.1-FGT.mkv", "Mad Max Fury Road (2015)"},
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
