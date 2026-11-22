package library

import (
	"path/filepath"
	"testing"

	"mvd/internal/testutil"
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
