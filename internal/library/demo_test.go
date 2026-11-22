package library

import (
	"os"
	"path/filepath"
	"testing"

	"mvd/internal/testutil"
)

// MVD_DEMO=/some/dir go test -run TestDemoLibrary builds a scraped demo
// library there (against the fake TMDBs), for looking at the web UI.
func TestDemoLibrary(t *testing.T) {
	root := os.Getenv("MVD_DEMO")
	if root == "" {
		t.Skip("set MVD_DEMO to build the demo library")
	}
	movies, tv := filepath.Join(root, "movies"), filepath.Join(root, "tv")
	mkv := "\x1a\x45\xdf\xa3 demo"
	for _, f := range []string{
		"The Godfather (1972)/The Godfather (1972).mkv", "The Godfather Part II (1974)/gf2.mkv",
		"Children of Men (2006)/com.mkv", "Dune (2021)/Dune.2021.mkv", "Boogie Nights (1997)/bn.mkv",
		"Fast 5 (2011)/Fast Five (2011).mkv", "Get Carter (1971)/gc.mkv", "Mission Impossible (1996)/mi.mkv",
		"FF3 - The Fast and the Furious - Tokyo Drift (2006)/ff3.mkv", "Some Home Video (2019)/hv.mkv",
	} {
		testutil.Write(t, filepath.Join(movies, f), mkv)
	}
	testutil.Write(t, filepath.Join(movies, "Children of Men (2006)/com.en.srt"), "1\n00:00:01,000 --> 00:00:03,000\nHello\n")
	for _, f := range []string{
		"Severance/Season 1/Severance.S01E01.mkv", "Severance/Season 1/Severance.S01E02.mkv",
		"Severance/Season 1/Severance.S01E03.mkv", "Severance/Season 1/Severance.S01E04.mkv",
		"Severance/Season 1/Severance.S01E05.mkv", "Severance/Specials/Severance.S00E01.mkv",
		"Doctor Who (2005)/Doctor.Who.2005.S01E01.mkv", "Doctor Who (2005)/Doctor.Who.2005.S01E02-E03.mkv",
		"Firefly/Firefly.S01E01.mkv", "Firefly/Firefly.S01E02.mkv", "Firefly/Firefly.S01E04.mkv",
		"The Office (US)/Series 2/The.Office.S02E01.mkv", "The Office (US)/Series 2/The.Office.S02E02.mkv",
	} {
		testutil.Write(t, filepath.Join(tv, f), mkv)
	}
	ms := testutil.TMDB(t)
	defer ms.Close()
	newTestScraper(t, ms, movies, Options{}).Run()
	ts := testutil.TMDBTV(t)
	defer ts.Close()
	s := newTestScraper(t, ts, tv, Options{})
	s.tv = true
	s.RunTV()
}
