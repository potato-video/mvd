package library

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"mvd/internal/testutil"
	"mvd/internal/tmdb"
)

// The library's size is its videos', from the stat the index makes anyway:
// each movie's (every part of a stack), each episode's, and the whole, kept
// across a save and load.
func TestIndexBytes(t *testing.T) {
	root := t.TempDir()
	movies, tv := filepath.Join(root, "movies"), filepath.Join(root, "tv")
	testutil.Write(t, filepath.Join(movies, "Kill Bill (2003)/Kill Bill (2003) - part1.mkv"), "1234567890")
	testutil.Write(t, filepath.Join(movies, "Kill Bill (2003)/Kill Bill (2003) - part2.mkv"), "12345")
	testutil.Write(t, filepath.Join(movies, "Kill Bill (2003)/poster.jpg"), "not counted")
	testutil.Write(t, filepath.Join(tv, "Severance (2022)/Season 01/Severance (2022) - S01E01.mkv"), "1234")

	ix := BuildIndex(movies, tv, nil)
	if len(ix.Movies) != 1 || ix.Movies[0].Bytes != 15 {
		t.Fatalf("movie: %+v", ix.Movies)
	}
	if ep := ix.Shows[0].Seasons[0].Episodes[0]; ep.Bytes != 4 {
		t.Errorf("episode: %d bytes", ep.Bytes)
	}
	if ix.Bytes() != 19 {
		t.Errorf("library: %d bytes, want 19", ix.Bytes())
	}

	path := filepath.Join(root, "index.json")
	if err := ix.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Bytes() != 19 {
		t.Errorf("loaded: %d bytes, want 19", loaded.Bytes())
	}
}

// The NFOs carry TMDB's original_language as Kodi's <originallanguage>, in
// BCP 47: TMDB's "xx" (no language) is "zxx", its "cn" is Cantonese's "yue".
func TestNFOOriginalLanguage(t *testing.T) {
	for tmdbCode, want := range map[string]string{
		"da": "<originallanguage>da</originallanguage>",
		"xx": "<originallanguage>zxx</originallanguage>",
		"cn": "<originallanguage>yue</originallanguage>",
		"":   "",
	} {
		movie := string(buildNFO(&tmdb.Movie{Title: "A", OriginalLanguage: tmdbCode}, nil, "GB", nil))
		show := string(buildShowNFO(&tmdb.TVShow{Name: "A", OriginalLanguage: tmdbCode}, "GB", nil))
		for kind, nfo := range map[string]string{"movie": movie, "show": show} {
			if want == "" && strings.Contains(nfo, "originallanguage") || !strings.Contains(nfo, want) {
				t.Errorf("%s NFO for %q, want %q:\n%s", kind, tmdbCode, want, nfo)
			}
		}
	}
}

// The index reads <originallanguage> as Kodi and other tools write it:
// ISO 639-1 or 639-2, BCP 47 or an English name. One it can't name is left
// out.
func TestIndexOriginalLanguage(t *testing.T) {
	root := t.TempDir()
	movies, tv := filepath.Join(root, "movies"), filepath.Join(root, "tv")
	cases := map[string]string{"da": "da", "fr-CA": "fr", "ger": "de", "Danish": "da", "zxx": "zxx", "qq": "", "": ""}
	i := 0
	for in := range cases {
		i++
		tag := ""
		if in != "" {
			tag = "<originallanguage>" + in + "</originallanguage>"
		}
		dir := fmt.Sprintf("Film %d (2000)", i)
		testutil.Write(t, filepath.Join(movies, dir, dir+".mkv"), "x")
		testutil.Write(t, filepath.Join(movies, dir, dir+".nfo"), "<movie><title>L "+in+"</title>"+tag+"</movie>")
		show := fmt.Sprintf("Show %d (2000)", i)
		testutil.Write(t, filepath.Join(tv, show, "Season 01", show+" - S01E01.mkv"), "x")
		testutil.Write(t, filepath.Join(tv, show, "tvshow.nfo"), "<tvshow><title>L "+in+"</title>"+tag+"</tvshow>")
	}
	ix := BuildIndex(movies, tv, nil)
	if len(ix.Movies) != len(cases) || len(ix.Shows) != len(cases) {
		t.Fatalf("%d movies, %d shows", len(ix.Movies), len(ix.Shows))
	}
	for _, m := range ix.Movies {
		if in := strings.TrimPrefix(m.Title, "L "); m.OriginalLanguage != cases[in] {
			t.Errorf("movie %q: %q, want %q", in, m.OriginalLanguage, cases[in])
		}
	}
	for _, sh := range ix.Shows {
		if in := strings.TrimPrefix(sh.Title, "L "); sh.OriginalLanguage != cases[in] {
			t.Errorf("show %q: %q, want %q", in, sh.OriginalLanguage, cases[in])
		}
	}
}
