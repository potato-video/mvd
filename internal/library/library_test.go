package library

import (
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mvd/internal/config"
	"mvd/internal/testutil"
	"mvd/internal/tmdb"
)

func TestParseMovieName(t *testing.T) {
	cases := []struct {
		in      string
		title   string
		year    int
		edition string
		part    int
	}{
		{"The.Thing.1982.1080p.BluRay.x264-GROUP.mkv", "The Thing", 1982, "", 0},
		{"Boogie Nights (1997)", "Boogie Nights", 1997, "", 0},
		{"Boogie Nights", "Boogie Nights", 0, "", 0},
		{"Boogie Nights\uf028", "Boogie Nights", 0, "", 0}, // macOS SMB mapped trailing space
		{"Boogie\u00a0Nights", "Boogie Nights", 0, "", 0},
		{"Blade Runner 2049 (2017)", "Blade Runner 2049", 2017, "", 0},
		{"Blade.Runner.2049.2017.2160p.UHD.mkv", "Blade Runner 2049", 2017, "", 0},
		{"1917 (2019)", "1917", 2019, "", 0},
		{"1917.mkv", "1917", 0, "", 0},
		{"2001 - A Space Odyssey (1968)", "2001 - A Space Odyssey", 1968, "", 0},
		{"FF3 - The Fast and the Furious - Tokyo Drift (2006)", "FF3 - The Fast and the Furious - Tokyo Drift", 2006, "", 0},
		{"[YTS] Monkey Man (2024) [1080p] [WEBRip]", "Monkey Man", 2024, "", 0},
		{"Blade Runner (1982) - Final Cut.mkv", "Blade Runner", 1982, "Final Cut", 0},
		{"Aliens.1986.Directors.Cut.720p.mkv", "Aliens", 1986, "Director's Cut", 0},
		{"Aliens Extended Edition.mkv", "Aliens", 0, "Extended Edition", 0},
		{"Movie.1999.Remastered.2020.1080p", "Movie", 1999, "", 0},
		{"Charlotte's Web (1973)", "Charlotte's Web", 1973, "", 0},
		{"Seven Samurai 1954 CD2.avi", "Seven Samurai", 1954, "", 2},
		{"Harry Potter and the Deathly Hallows Part 1 (2010)", "Harry Potter and the Deathly Hallows Part 1", 2010, "", 0},
		{"Get_Carter_1971.mp4", "Get Carter", 1971, "", 0},
		{"Mr. Nobody (2009)", "Mr. Nobody", 2009, "", 0},
	}
	for _, c := range cases {
		p := parseMovieName(c.in)
		if p.Title != c.title || p.Year != c.year || p.Edition != c.edition || p.Part != c.part {
			t.Errorf("%q: got %+v, want %q %d %q %d", c.in, p, c.title, c.year, c.edition, c.part)
		}
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"Mission: Impossible (1996)":                "Mission - Impossible (1996)",
		"Face/Off (1997)":                           "Face-Off (1997)",
		"What's Up, Doc? (1972)":                    "What's Up, Doc (1972)",
		"Star Wars: Episode IV - A New Hope (1977)": "Star Wars - Episode IV - A New Hope (1977)",
		"Trailing dots...":                          "Trailing dots",
		// Review M2: not a name every scan skips (hidden, or _ and @).
		"...And Justice for All (1979)": "And Justice for All (1979)",
		".45 (2006)":                    "45 (2006)",
		".hack//Sign (2002)":            "hack--Sign (2002)",
		"@Suicide Room":                 "Suicide Room",
		"_Underscored":                  "Underscored",
	}
	for in, want := range cases {
		got := SafeName(in)
		if got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
		if skipName(got) {
			t.Errorf("safeName(%q) = %q, which a scan skips", in, got)
		}
	}
}

func TestTitleSim(t *testing.T) {
	if s := TitleSim("Mission - Impossible", "Mission: Impossible"); s != 1 {
		t.Errorf("mission impossible: %v", s)
	}
	if s := TitleSim("The Fast and the Furious Tokyo Drift", "The Fast and the Furious: Tokyo Drift"); s != 1 {
		t.Errorf("tokyo drift: %v", s)
	}
	if s := TitleSim("Boogie Nights", "Boogie Woogie"); s > 0.7 {
		t.Errorf("boogie woogie too close: %v", s)
	}
}

func newTestScraper(t *testing.T, srv *httptest.Server, root string, o Options) *Refresher {
	cfg := config.Config{APIKey: "k", Language: "en-US", Region: "GB", CacheDir: t.TempDir()}
	tm := tmdb.New(cfg)
	tm.SetBaseURLs(srv.URL+"/3", srv.URL+"/t/p/")
	return NewRefresher(o, cfg, tm, root, false)
}

func TestScrapeLibrary(t *testing.T) {
	srv := testutil.TMDB(t)
	defer srv.Close()
	root := t.TempDir()

	mkv := "\x1a\x45\xdf\xa3 matroska"
	testutil.Write(t, filepath.Join(root, "Boogie Nights\uf028"), mkv) // loose, no extension
	os.Mkdir(filepath.Join(root, "Boogie Nights (1997)"), 0o755)       // empty folder waiting for it
	testutil.Write(t, filepath.Join(root, "Children of Men (2006)/children.of.men.2006.1080p.bluray.x264-grp.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Children of Men (2006)/children.of.men.2006.1080p.bluray.x264-grp.en.srt"), "1")
	testutil.Write(t, filepath.Join(root, "Children of Men (2006)/children.of.men.2006.1080p.bluray.x264-grp.en.forced.srt"), "1")
	testutil.Write(t, filepath.Join(root, "Dune (2021)/Dune.2021.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "FF3 - The Fast and the Furious - Tokyo Drift (2006)/ff3.mp4"), "....ftypisom")
	testutil.Write(t, filepath.Join(root, "Fast 5 (2011)/Fast Five (2011).mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "The Godfather (1972)/The Godfather (1972).mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "the godfather part ii (1974)/godfather2.avi"), "RIFF....AVI ")
	testutil.Write(t, filepath.Join(root, "Get Carter/Get Carter.mkv"), mkv) // ambiguous: 1971 or 2000
	testutil.Write(t, filepath.Join(root, "Mission Impossible 1996.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, ".DS_Store"), "junk")

	// Dry run changes nothing.
	before := testutil.Tree(root)
	s := newTestScraper(t, srv, root, Options{DryRun: true})
	if rc := s.Run(); rc != 0 {
		t.Fatalf("dry run rc=%d", rc)
	}
	if after := testutil.Tree(root); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("dry run changed the tree:\n%s", strings.Join(after, "\n"))
	}

	s = newTestScraper(t, srv, root, Options{})
	if rc := s.Run(); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s.nNew != 8 || s.nSkip != 1 {
		t.Errorf("new=%d skip=%d, want 8 and 1 (Get Carter)", s.nNew, s.nSkip)
	}

	want := []string{
		"Boogie Nights (1997)/Boogie Nights (1997).mkv",
		"Boogie Nights (1997)/Boogie Nights (1997).nfo",
		"Boogie Nights (1997)/poster.jpg",
		"Children of Men (2006)/Children of Men (2006).en.forced.srt",
		"Children of Men (2006)/Children of Men (2006).en.srt",
		"Children of Men (2006)/Children of Men (2006).mkv",
		"Dune (2021)/Dune (2021).mkv",
		"Fast Five (2011)/Fast Five (2011).mkv",
		"Get Carter/Get Carter.mkv",
		"Mission - Impossible (1996)/Mission - Impossible (1996).mkv",
		"The Fast and the Furious - Tokyo Drift (2006)/The Fast and the Furious - Tokyo Drift (2006).mp4",
		"The Godfather (1972)/The Godfather (1972).nfo",
		"The Godfather Part II (1974)/The Godfather Part II (1974).avi",
		"_sets/The Godfather Collection/poster.jpg",
		"_sets/The Fast and the Furious Collection/fanart.jpg",
	}
	for _, w := range want {
		if _, err := os.Stat(filepath.Join(root, w)); err != nil {
			t.Errorf("missing %s", w)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "Boogie Nights\uf028")); err == nil {
		t.Errorf("loose Boogie Nights file not moved")
	}

	nfo, _ := os.ReadFile(filepath.Join(root, "The Godfather (1972)/The Godfather (1972).nfo"))
	for _, frag := range []string{`<uniqueid type="tmdb" default="true">238</uniqueid>`, `<mpaa>15</mpaa>`,
		`<set tmdbcolid="230">`, `<name>The Godfather Collection</name>`, `<director>Some Director</director>`} {
		if !strings.Contains(string(nfo), frag) {
			t.Errorf("godfather nfo lacks %s:\n%s", frag, nfo)
		}
	}

	// A second run finds the NFOs and does nothing.
	s = newTestScraper(t, srv, root, Options{})
	s.Run()
	if s.nNew != 0 || s.nDone != 8 {
		t.Errorf("second run: new=%d done=%d, want 0 and 8", s.nNew, s.nDone)
	}

	t.Logf("final tree:\n%s", strings.Join(testutil.Tree(root), "\n"))
}

func TestInteractivePick(t *testing.T) {
	srv := testutil.TMDB(t)
	defer srv.Close()
	root := t.TempDir()
	testutil.Write(t, filepath.Join(root, "Get Carter/Get Carter.mkv"), "\x1a\x45\xdf\xa3")

	s := newTestScraper(t, srv, root, Options{})
	s.ask = AskTerminal(strings.NewReader("e\nGet Carter (1971)\n\n"), s.writer())
	s.Run()
	if _, err := os.Stat(filepath.Join(root, "Get Carter (1971)/Get Carter (1971).nfo")); err != nil {
		t.Errorf("interactive search didn't produce Get Carter (1971): %v\n%s", err, strings.Join(testutil.Tree(root), "\n"))
	}
}

// No exFAT here, so fake a case-insensitive filesystem's "the new name is
// the same file" with a hard link and check the two-step rename path.
func TestRenameSameFile(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "movies")
	testutil.Write(t, old, "x")
	if err := os.Link(old, filepath.Join(root, "Movies")); err != nil {
		t.Skip(err)
	}
	s := &filer{root: root}
	if err := s.rename(old, filepath.Join(root, "Movies")); err != nil {
		t.Fatal(err)
	}
	// (rename(2) between two links to one inode is a no-op, so the hard-link
	// stand-in leaves the temp name behind; on exFAT the second step lands.)
	if _, err := os.Stat(filepath.Join(root, "movies.mvdtmp")); err != nil {
		t.Errorf("didn't take the two-step path: %v", testutil.Tree(root))
	}
	os.Remove(filepath.Join(root, "movies.mvdtmp"))
	// A different file in the way is never overwritten.
	testutil.Write(t, filepath.Join(root, "a"), "a")
	testutil.Write(t, filepath.Join(root, "b"), "b")
	if err := s.rename(filepath.Join(root, "a"), filepath.Join(root, "b")); err == nil {
		t.Errorf("overwrote b")
	}
}

// Review L1: a number too big for an int is no number, rather than one
// wrapped round (perhaps negative).
func TestAtoiOverflow(t *testing.T) {
	for _, s := range []string{"9223372036854775808", "18446744073709551615", "99999999999999999999999"} {
		if n := Atoi(s); n != 0 {
			t.Errorf("Atoi(%s) = %d", s, n)
		}
	}
	if n := Atoi(strconv.Itoa(math.MaxInt)); n != math.MaxInt {
		t.Errorf("max int: %d", n)
	}
	if n := Atoi("42"); n != 42 {
		t.Errorf("42: %d", n)
	}
}
