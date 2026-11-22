package library

import (
	"fmt"
	"testing"
)

func TestParseShowName(t *testing.T) {
	cases := []struct {
		in, title string
		year      int
		country   string
	}{
		{"Severance", "Severance", 0, ""},
		{"Severance (2022)", "Severance", 2022, ""},
		{"Severance.S01.1080p.ATVP.WEB-DL", "Severance", 0, ""},
		{"Severance.S01E03.In.Perpetuity.1080p.mkv", "Severance", 0, ""},
		{"Doctor.Who.2005.S01E02-E03.mkv", "Doctor Who", 2005, ""},
		{"Doctor Who (2005)", "Doctor Who", 2005, ""},
		{"The Office (US)", "The Office", 0, "US"},
		{"The Office (US) (2005)", "The Office", 2005, "US"},
		{"The Office UK", "The Office", 0, "UK"},
		{"Shameless Season 3", "Shameless", 0, ""},
		{"Blackadder Complete", "Blackadder", 0, ""},
		{"24", "24", 0, ""},
	}
	for _, c := range cases {
		p := parseShowName(c.in)
		if p.Title != c.title || p.Year != c.year || p.Country != c.country {
			t.Errorf("%q: got %q %d %q", c.in, p.Title, p.Year, p.Country)
		}
	}
}

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		in     string
		folder int
		want   string // "s:e,e" or "-" for no match
	}{
		{"Severance.S01E01.Good.News.About.Hell.1080p.mkv", -1, "1:1"},
		{"severance.s1e2.mkv", -1, "1:2"},
		{"Show S01E01E02.mkv", -1, "1:1,2"},
		{"Show S01E01-E03.mkv", -1, "1:1,2,3"},
		{"Show.S01E01-02.mkv", -1, "1:1,2"},
		{"Show.S01E01-720p.mkv", -1, "1:1"},
		{"Show.S01E09.1080p.x264.mkv", -1, "1:9"},
		{"Doctor.Who.1x01.Rose.avi", -1, "1:1"},
		{"Show 2x03-04.mkv", -1, "2:3,4"},
		{"Show.1920x1080.mkv", -1, "-"},
		{"Show Season 2 Episode 5.mkv", -1, "2:5"},
		{"02 - Halloween.mkv", 2, "2:2"},
		{"02 - Halloween.mkv", -1, "-"},
		{"Episode 4.mkv", 3, "3:4"},
		{"Ep04.mkv", -1, "-1:4"},
		{"Show S00E01 Special.mkv", -1, "0:1"},
		{"Behind the scenes.mkv", 1, "-"},
	}
	for _, c := range cases {
		s, eps, ok := parseEpisode(c.in, c.folder)
		got := "-"
		if ok {
			got = fmt.Sprintf("%d:", s)
			for i, e := range eps {
				if i > 0 {
					got += ","
				}
				got += fmt.Sprint(e)
			}
		}
		if got != c.want {
			t.Errorf("%q (folder %d): got %s, want %s", c.in, c.folder, got, c.want)
		}
	}
}

func TestFolderSeason(t *testing.T) {
	cases := map[string]int{
		"Season 1": 1, "Season 01": 1, "Series 2": 2, "S03": 3, "Staffel 4": 4,
		"Specials": 0, "Severance.S02.1080p.WEB": 2, "Severance Season 2 (2025)": 2,
		"Extras": -1, "Severance": -1,
	}
	for in, want := range cases {
		if got := folderSeason(in); got != want {
			t.Errorf("folderSeason(%q) = %d, want %d", in, got, want)
		}
	}
}
