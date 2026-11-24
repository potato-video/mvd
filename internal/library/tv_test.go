package library

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mvd/internal/testutil"
)

func TestScrapeTV(t *testing.T) {
	srv := testutil.TMDBTV(t)
	defer srv.Close()
	root := t.TempDir()
	mkv := "\x1a\x45\xdf\xa3 matroska"

	testutil.Write(t, filepath.Join(root, "Severance/Season 1/Severance.S01E01.Good.News.About.Hell.1080p.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Severance/Season 1/Severance.S01E01.Good.News.About.Hell.1080p.en.srt"), "1")
	testutil.Write(t, filepath.Join(root, "Severance/Season 1/severance.s01e02.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Severance/Season 1/Severance S01E99.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Severance/Season 1/random clip.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Severance/Specials/Severance S00E01.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Severance/Extras/Behind the scenes.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Severance.S01E03.In.Perpetuity.1080p.WEB-DL.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "The Office (US)/Series 2/02 - Something.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Doctor Who (2005)/Doctor.Who.2005.1x01.Rose.avi"), "RIFF....AVI ")
	testutil.Write(t, filepath.Join(root, "Doctor Who (2005)/Doctor.Who.2005.S01E02-E03.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Firefly/Episode 4.mkv"), mkv)

	before := strings.Join(testutil.Tree(root), "\n")
	s := newTestScraper(t, srv, root, Options{DryRun: true})
	s.tv = true
	if rc := s.RunTV(); rc != 0 {
		t.Fatalf("dry run rc=%d", rc)
	}
	if after := strings.Join(testutil.Tree(root), "\n"); after != before {
		t.Fatalf("dry run changed the tree:\n%s", after)
	}

	s = newTestScraper(t, srv, root, Options{})
	s.tv = true
	if rc := s.RunTV(); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s.nNew != 4 || s.nEps != 9 {
		t.Errorf("shows=%d eps=%d, want 4 and 9", s.nNew, s.nEps)
	}

	want := []string{
		"Severance (2022)/tvshow.nfo",
		"Severance (2022)/poster.jpg",
		"Severance (2022)/season01-poster.jpg",
		"Severance (2022)/season-specials-poster.jpg",
		"Severance (2022)/Season 01/Severance (2022) - S01E01 - Good News About Hell.mkv",
		"Severance (2022)/Season 01/Severance (2022) - S01E01 - Good News About Hell.en.srt",
		"Severance (2022)/Season 01/Severance (2022) - S01E01 - Good News About Hell.nfo",
		"Severance (2022)/Season 01/Severance (2022) - S01E01 - Good News About Hell-thumb.jpg",
		"Severance (2022)/Season 01/Severance (2022) - S01E02 - Half Loop.mkv",
		"Severance (2022)/Season 01/Severance (2022) - S01E03 - In Perpetuity.mkv",
		"Severance (2022)/Season 00/Severance (2022) - S00E01 - Inside Severance.mkv",
		"Severance (2022)/Season 1/Severance S01E99.mkv", // not on TMDB: left alone
		"Severance (2022)/Season 1/random clip.mkv",
		"Severance (2022)/Extras/Behind the scenes.mkv",
		"The Office (2005)/Season 02/The Office (2005) - S02E02 - Sexual Harassment.mkv",
		"Doctor Who (2005)/Season 01/Doctor Who (2005) - S01E01 - Rose.avi",
		"Doctor Who (2005)/Season 01/Doctor Who (2005) - S01E02-E03 - The End of the World & The Unquiet Dead.mkv",
		"Firefly (2002)/Season 01/Firefly (2002) - S01E04 - Jaynestown.mkv",
	}
	for _, w := range want {
		if !Exists(filepath.Join(root, w)) {
			t.Errorf("missing %s", w)
		}
	}
	for _, gone := range []string{"The Office (2005)/Series 2", "Severance (2022)/Specials",
		"Severance.S01E03.In.Perpetuity.1080p.WEB-DL.mkv"} {
		if Exists(filepath.Join(root, gone)) {
			t.Errorf("%s should have gone", gone)
		}
	}

	nfo, _ := os.ReadFile(filepath.Join(root, "Doctor Who (2005)/Season 01/Doctor Who (2005) - S01E02-E03 - The End of the World & The Unquiet Dead.nfo"))
	if strings.Count(string(nfo), "<episodedetails>") != 2 || !strings.Contains(string(nfo), "<episode>3</episode>") {
		t.Errorf("multi-episode nfo:\n%s", nfo)
	}
	show, _ := os.ReadFile(filepath.Join(root, "Severance (2022)/tvshow.nfo"))
	for _, frag := range []string{`<uniqueid type="tmdb" default="true">95396</uniqueid>`,
		`<uniqueid type="tvdb">102396</uniqueid>`, `<mpaa>15</mpaa>`, `<namedseason number="0">Specials</namedseason>`} {
		if !strings.Contains(string(show), frag) {
			t.Errorf("tvshow.nfo lacks %s:\n%s", frag, show)
		}
	}

	// Second run: nothing new.
	s = newTestScraper(t, srv, root, Options{})
	s.tv = true
	s.RunTV()
	if s.nNew != 0 || s.nDone != 4 {
		t.Errorf("second run: new=%d done=%d, want 0 and 4", s.nNew, s.nDone)
	}

	// A new episode dropped in the root joins the renamed show.
	testutil.Write(t, filepath.Join(root, "Severance.S01E04.1080p.mkv"), mkv)
	s = newTestScraper(t, srv, root, Options{})
	s.tv = true
	s.RunTV()
	if s.nNew != 1 || s.nEps != 1 || s.nDone != 3 {
		t.Errorf("new episode run: new=%d eps=%d done=%d, want 1, 1, 3", s.nNew, s.nEps, s.nDone)
	}
	if !Exists(filepath.Join(root, "Severance (2022)/Season 01/Severance (2022) - S01E04 - The You You Are.mkv")) {
		t.Errorf("new episode not filed:\n%s", strings.Join(testutil.Tree(root), "\n"))
	}

	t.Logf("final tree:\n%s", strings.Join(testutil.Tree(root), "\n"))
}

func TestScrapeTVNoRename(t *testing.T) {
	srv := testutil.TMDBTV(t)
	defer srv.Close()
	root := t.TempDir()
	testutil.Write(t, filepath.Join(root, "Firefly/S1/firefly.s01e02.mkv"), "\x1a\x45\xdf\xa3")
	s := newTestScraper(t, srv, root, Options{NoRename: true})
	s.tv = true
	s.RunTV()
	for _, w := range []string{"Firefly/S1/firefly.s01e02.mkv", "Firefly/S1/firefly.s01e02.nfo", "Firefly/tvshow.nfo"} {
		if !Exists(filepath.Join(root, w)) {
			t.Errorf("missing %s:\n%s", w, strings.Join(testutil.Tree(root), "\n"))
		}
	}
}

// A rename that only changes case finds "the same file" at the target on a
// case-insensitive filesystem (exFAT); that's a rename, not a collision.
// Simulated with a hard link, which os.Stat also reports as the same file.
func TestExistsOther(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "show - s01e01 - rose.mkv")
	testutil.Write(t, a, "x")
	linked := filepath.Join(root, "Show - S01E01 - Rose.mkv")
	if err := os.Link(a, linked); err != nil {
		t.Skip(err)
	}
	if existsOther(linked, a) {
		t.Errorf("the same file (by inode) counted as a collision")
	}
	b := filepath.Join(root, "other.mkv")
	testutil.Write(t, b, "y")
	if !existsOther(b, a) {
		t.Errorf("a different file in the way wasn't seen")
	}
	if existsOther(filepath.Join(root, "missing.mkv"), a) {
		t.Errorf("a missing target counted as a collision")
	}
}

// refresh -all brings an older show NFO (no genres, say) up to date, and
// the index reads what it now says.
func TestRefreshTVAllRewritesShow(t *testing.T) {
	srv := testutil.TMDBTV(t)
	defer srv.Close()
	root := t.TempDir()
	mkv := "\x1a\x45\xdf\xa3 matroska"
	testutil.Write(t, filepath.Join(root, "Severance (2022)/Season 01/Severance (2022) - S01E01 - Good News About Hell.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Severance (2022)/tvshow.nfo"), `<?xml version="1.0" encoding="UTF-8" standalone="yes" ?>
<tvshow>
  <title>Severance</title>
  <year>2022</year>
  <uniqueid type="tmdb" default="true">95396</uniqueid>
</tvshow>
`)
	s := newTestScraper(t, srv, root, Options{Refresh: true})
	s.tv = true
	if rc := s.RunTV(); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s.Stats().Changed == 0 {
		t.Errorf("rewrote the NFO but says nothing changed, so a running server isn't told to rescan")
	}
	b, _ := os.ReadFile(filepath.Join(root, "Severance (2022)/tvshow.nfo"))
	if !strings.Contains(string(b), "<genre>Mystery</genre>") || !strings.Contains(string(b), "Returning Series") {
		t.Errorf("tvshow.nfo not refreshed:\n%s", b)
	}
	ix := BuildIndex("", root, nil)
	if len(ix.Shows) != 1 || strings.Join(ix.Shows[0].Genres, ",") != "Drama,Mystery" || ix.Shows[0].Status != "Returning Series" ||
		ix.Shows[0].OriginalLanguage != "en" {
		t.Errorf("index: %+v", ix.Shows)
	}
}

// A long season is listed once, not once an episode: with an NFO and a
// still beside each video, relisting the folder for every episode's
// subtitles made a scan's garbage grow with the square of the season.
func TestIndexLongSeason(t *testing.T) {
	root := t.TempDir()
	const n = 120
	dir := filepath.Join(root, "Batman - The Animated Series (1992)", "Season 01")
	for i := 1; i <= n; i++ {
		base := filepath.Join(dir, fmt.Sprintf("Batman - The Animated Series (1992) - S01E%02d - Episode %d", i, i))
		testutil.Write(t, base+".mkv", "\x1a\x45\xdf\xa3 matroska")
		testutil.Write(t, base+"-thumb.jpg", "jpg")
		testutil.Write(t, base+".nfo", fmt.Sprintf("<episodedetails><title>Episode %d</title><season>1</season>"+
			"<episode>%d</episode><plot>Something happens.</plot><aired>1992-09-06</aired></episodedetails>", i, i))
	}
	testutil.Write(t, filepath.Join(dir, "Batman - The Animated Series (1992) - S01E07 - Episode 7.en.srt"), "1")

	ix := BuildIndex("", root, nil)
	eps := ix.Shows[0].Seasons[0].Episodes
	if len(eps) != n || len(eps[6].Subs) != 1 || eps[6].Subs[0].Lang != "en" || len(eps[7].Subs) != 0 || eps[0].Thumb == "" {
		t.Fatalf("%d episodes; 7: %+v; 8: %+v", len(eps), eps[6].Subs, eps[7].Subs)
	}
	if testutil.Race {
		return
	}
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	BuildIndex("", root, nil)
	runtime.ReadMemStats(&b)
	per := (b.TotalAlloc - a.TotalAlloc) / n
	if per > 20<<10 {
		t.Errorf("%d KB of garbage an episode", per>>10)
	}
	t.Logf("%d KB of garbage an episode", per>>10)
}

// -season limits a refresh to one season of the show: the others' files
// aren't touched, and their missing NFOs stay missing.
func TestRefreshTVSeason(t *testing.T) {
	srv := testutil.TMDBTV(t)
	defer srv.Close()
	root := t.TempDir()
	mkv := "\x1a\x45\xdf\xa3 matroska"
	testutil.Write(t, filepath.Join(root, "Severance/Season 1/severance.s01e01.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Severance/Season 1/severance.s01e02.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Severance/Specials/severance.s00e01.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Firefly/Episode 4.mkv"), mkv) // no season number, and one regular season
	run := func(o Options) (*Refresher, string) {
		var out strings.Builder
		s := newTestScraper(t, srv, root, o)
		s.tv, s.out = true, &out
		if rc := s.RunTV(); rc != 0 {
			t.Fatalf("%+v: rc=%d\n%s", o, rc, out.String())
		}
		return s, out.String()
	}
	tree := func() string { return strings.Join(testutil.Tree(root), "\n") }
	season := func(n int) *int { return &n }

	s, _ := run(Options{Name: "severance", Season: season(0)})
	if s.nEps != 1 || !Exists(filepath.Join(root, "Severance (2022)/Season 00/Severance (2022) - S00E01 - Inside Severance.nfo")) {
		t.Errorf("season 0: eps=%d\n%s", s.nEps, tree())
	}
	if !Exists(filepath.Join(root, "Severance (2022)/Season 1/severance.s01e01.mkv")) ||
		Exists(filepath.Join(root, "Severance (2022)/Season 01")) || Exists(filepath.Join(root, "Severance (2022)/season01-poster.jpg")) {
		t.Errorf("season 0 touched season 1:\n%s", tree())
	}
	if Exists(filepath.Join(root, "Firefly (2002)")) {
		t.Errorf("-show severance touched Firefly:\n%s", tree())
	}

	s, _ = run(Options{Name: "severance", Season: season(1)})
	if s.nEps != 2 || !Exists(filepath.Join(root, "Severance (2022)/Season 01/Severance (2022) - S01E02 - Half Loop.nfo")) {
		t.Errorf("season 1: eps=%d\n%s", s.nEps, tree())
	}

	// -all with -season fetches that season again, and only that season.
	thumb0 := filepath.Join(root, "Severance (2022)/Season 00/Severance (2022) - S00E01 - Inside Severance-thumb.jpg")
	os.WriteFile(thumb0, []byte("old"), 0o644)
	s, _ = run(Options{Refresh: true, Name: "severance", Season: season(1)})
	if s.nEps != 2 {
		t.Errorf("season 1 -all: eps=%d, want 2", s.nEps)
	}
	if b, _ := os.ReadFile(thumb0); string(b) != "old" {
		t.Errorf("season 1 -all redid season 0's thumbnail")
	}

	// An episode with no season number counts as the show's only season.
	s, _ = run(Options{Name: "firefly", Season: season(1)})
	if s.nEps != 1 || !Exists(filepath.Join(root, "Firefly (2002)/Season 01/Firefly (2002) - S01E04 - Jaynestown.mkv")) {
		t.Errorf("firefly season 1: eps=%d\n%s", s.nEps, tree())
	}

	// A season with nothing on disk says so.
	s, out := run(Options{Name: "severance", Season: season(3)})
	if s.nEps != 0 || !strings.Contains(out, "no season 3 on disk") {
		t.Errorf("season 3: eps=%d\n%s", s.nEps, out)
	}
}

// A name that matches nothing is an error, and points the other way.
func TestRefreshNoMatch(t *testing.T) {
	srv := testutil.TMDBTV(t)
	defer srv.Close()
	root := t.TempDir()
	testutil.Write(t, filepath.Join(root, "Firefly/Season 1/firefly.s01e02.mkv"), "\x1a\x45\xdf\xa3")
	var out strings.Builder
	s := newTestScraper(t, srv, root, Options{Name: "godfather"})
	s.tv, s.out = true, &out
	if rc := s.RunTV(); rc != 1 || !strings.Contains(out.String(), `no show matches "godfather" (for a movie, use -movie)`) {
		t.Errorf("rc=%d\n%s", rc, out.String())
	}
	s = newTestScraper(t, srv, root, Options{Name: "firefly"})
	s.out = &out
	if rc := s.Run(); rc != 1 || !strings.Contains(out.String(), `no movie matches "firefly" (for a show, use -show)`) {
		t.Errorf("rc=%d\n%s", rc, out.String())
	}
}
