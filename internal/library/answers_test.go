package library

import (
	"os"
	"path/filepath"
	"testing"

	"mvd/internal/config"
	"mvd/internal/testutil"
	"mvd/internal/tmdb"
)

func TestPlanItemSummary(t *testing.T) {
	dir := t.TempDir()
	mkv := "\x1a\x45\xdf\xa3"
	testutil.Write(t, filepath.Join(dir, "Severance.S01/Severance.S01E02.mkv"), mkv)
	testutil.Write(t, filepath.Join(dir, "Severance.S01/Severance.S01E01.mkv"), mkv)
	testutil.Write(t, filepath.Join(dir, "Severance.S01/Severance.S01E08E09.mkv"), mkv)
	testutil.Write(t, filepath.Join(dir, "Dune.2021.mkv"), mkv)

	sum, err := PlanItem(filepath.Join(dir, "Severance.S01"), "")
	if err != nil || sum.Episodes != 4 || sum.Movies != 0 || sum.First != "S01E01" || sum.Last != "S01E09" {
		t.Errorf("season: %+v %v", sum, err)
	}
	if sum, err := PlanItem(filepath.Join(dir, "Dune.2021.mkv"), ""); err != nil || sum.Movies != 1 || sum.Episodes != 0 {
		t.Errorf("movie: %+v %v", sum, err)
	}
}

func TestSearchAndLookup(t *testing.T) {
	srv := testutil.TMDBAll(t)
	cfg := config.Config{APIKey: "k", Language: "en-US", CacheDir: t.TempDir()}
	tm := tmdb.New(cfg)
	tm.SetBaseURLs(srv.URL+"/3", srv.URL+"/t/p/")

	cs, err := Search(tm, "movie", "Dune (1984)")
	if err != nil || len(cs) == 0 || cs[0].ID != 841 {
		t.Errorf("search: %v %v", cs, err)
	}
	if cs, err := Search(tm, "tv", "Severance"); err != nil || len(cs) == 0 || cs[0].ID != 95396 {
		t.Errorf("search tv: %v %v", cs, err)
	}

	for ref, want := range map[string]struct {
		kind, gotKind string
		id            int
	}{
		"87108":       {"tv", "tv", 87108},
		"tmdb:438631": {"movie", "movie", 438631},
		"https://www.themoviedb.org/tv/87108-chernobyl": {"", "tv", 87108},
		"tt1438631":                             {"", "movie", 438631},
		"https://www.imdb.com/title/tt1438631/": {"movie", "movie", 438631},
	} {
		id, kind, err := Lookup(tm, want.kind, ref)
		if err != nil || id != want.id || kind != want.gotKind {
			t.Errorf("%s: %d %q %v, want %d %q", ref, id, kind, err, want.id, want.gotKind)
		}
	}
	if _, _, err := Lookup(tm, "", "87108"); err == nil {
		t.Errorf("a bare id with no kind: no error")
	}
	if _, _, err := Lookup(tm, "movie", "Dune"); err == nil {
		t.Errorf("not a reference: no error")
	}
}

func TestWriteHint(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Dune.mkv")
	folder := filepath.Join(dir, "BTAS")
	testutil.Write(t, file, "x")
	testutil.Write(t, filepath.Join(folder, "e1.mkv"), "x")
	testutil.Write(t, filepath.Join(folder, ".mvd"), "order = \"dvd\"\n")

	if err := WriteHint(file, "movie", 841); err != nil {
		t.Fatal(err)
	}
	if h := readHint(file, false); h.kind != "movie" || h.tmdb != 841 {
		t.Errorf("file: %+v", h)
	}
	if err := WriteHint(folder, "tv", 2098); err != nil {
		t.Fatal(err)
	}
	if h := readHint(folder, true); h.kind != "tv" || h.tmdb != 2098 || h.order != "dvd" {
		t.Errorf("folder: %+v", h)
	}
	if err := WriteHint(file, "film", 1); err == nil {
		t.Errorf("a bad kind: no error")
	}
	if _, err := os.Stat(filepath.Join(dir, "Dune.mvd")); err != nil {
		t.Errorf("no Dune.mvd: %v", err)
	}
}
