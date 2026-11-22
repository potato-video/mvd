package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mvd/internal/testutil"
)

func TestDVDOrder(t *testing.T) {
	srv := testutil.TMDBTV(t)
	defer srv.Close()
	root := t.TempDir()
	mkv := "\x1a\x45\xdf\xa3"
	// Files numbered in DVD order: E01 is really On Leather Wings.
	testutil.Write(t, filepath.Join(root, "Batman - The Animated Series (1992)/Season 01/btas.s01e01.mkv"), mkv+"leather")
	testutil.Write(t, filepath.Join(root, "Batman - The Animated Series (1992)/Season 01/btas.s01e02.mkv"), mkv+"joker")
	testutil.Write(t, filepath.Join(root, "Batman - The Animated Series (1992)/Season 01/btas.s01e03.mkv"), mkv+"cat")
	dir := filepath.Join(root, "Batman - The Animated Series (1992)/Season 01")
	run := func(o Options, input string) *Refresher {
		s := newTestScraper(t, srv, root, o)
		s.tv = true
		if input != "" {
			s.ask = AskTerminal(strings.NewReader(input), s.writer())
		}
		s.RunTV()
		return s
	}

	// First, aired order gets the titles wrong (where you are now).
	run(Options{}, "")
	if !Exists(filepath.Join(dir, "Batman - The Animated Series (1992) - S01E01 - The Cat and the Claw (1).mkv")) {
		t.Fatalf("setup:\n%s", strings.Join(testutil.Tree(root), "\n"))
	}

	// Then the fix: two DVD orders exist, so it asks; pick the first.
	s := run(Options{Name: "batman", Order: "dvd", Refresh: true}, "1\n")
	if s.nNew != 1 || s.nEps != 3 {
		t.Errorf("re-file: shows=%d eps=%d, want 1 and 3", s.nNew, s.nEps)
	}
	want := map[string]string{
		"Batman - The Animated Series (1992) - S01E01 - On Leather Wings.mkv":         "leather",
		"Batman - The Animated Series (1992) - S01E02 - Christmas with the Joker.mkv": "joker",
		"Batman - The Animated Series (1992) - S01E03 - The Cat and the Claw (1).mkv": "cat",
	}
	for name, content := range want {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !strings.HasSuffix(string(b), content) {
			t.Errorf("%s: %q %v\n%s", name, b, err, strings.Join(testutil.Tree(root), "\n"))
		}
	}
	nfo, _ := os.ReadFile(filepath.Join(dir, "Batman - The Animated Series (1992) - S01E01 - On Leather Wings.nfo"))
	if !strings.Contains(string(nfo), "<title>On Leather Wings</title>") || !strings.Contains(string(nfo), "<episode>1</episode>") ||
		!strings.Contains(string(nfo), "<director>A Director</director>") { // full details from the aired season
		t.Errorf("nfo:\n%s", nfo)
	}
	settings := readShowSettings(filepath.Join(root, "Batman - The Animated Series (1992)"))
	if settings["order"] != "dvd" || settings["group"] != "aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf(".mvd: %v", settings)
	}
	if n := len(testutil.Tree(dir)); n != 3*3 { // video, nfo, thumb each; no stale extras
		t.Errorf("season folder has %d files:\n%s", n, strings.Join(testutil.Tree(dir), "\n"))
	}

	// Later runs remember the order and have nothing to do.
	s = run(Options{}, "")
	if s.nNew != 0 || s.nDone != 1 {
		t.Errorf("remembered order: new=%d done=%d", s.nNew, s.nDone)
	}

	// And -order aired puts it back.
	run(Options{Name: "batman", Order: "aired", Refresh: true}, "")
	if !Exists(filepath.Join(dir, "Batman - The Animated Series (1992) - S01E01 - The Cat and the Claw (1).mkv")) ||
		readShowSettings(filepath.Join(root, "Batman - The Animated Series (1992)"))["order"] != "aired" {
		t.Errorf("back to aired:\n%s", strings.Join(testutil.Tree(root), "\n"))
	}
}
