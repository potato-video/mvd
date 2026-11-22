package server

import (
	"path/filepath"
	"testing"

	"mvd/internal/config"
	"mvd/internal/library"
)

// Once probed, an item's full view has its files' tracks; the lists don't.
func TestServerFiles(t *testing.T) {
	if library.FFprobe() == "" {
		t.Skip("needs ffprobe")
	}
	ts := newTestServer(t, "")
	defer ts.hs.Close()

	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1, e2 := sh.Seasons[0].Episodes[0], sh.Seasons[0].Episodes[1]
	if sh.Seasons[0].Episodes[1].Files != nil {
		t.Error("files in the show's episode list")
	}

	var ep episodeView
	ts.json("GET", "/api/item/"+e1.ID, "", &ep)
	if ep.Files != nil {
		t.Errorf("not a real video, so no files: %+v", ep.Files)
	}
	ts.json("GET", "/api/item/"+e2.ID, "", &ep)
	if len(ep.Files) == 0 {
		t.Skip("needs ffmpeg for a real video")
	}
	f := ep.Files[0]
	if len(ep.Files) != 1 || f.Container != "mkv" || f.Duration == 0 || len(f.Tracks) != 6 {
		t.Fatalf("files: %+v", ep.Files)
	}
	if s := f.Tracks[3]; s.Kind != "subtitle" || !s.Forced || s.Lang != "" {
		t.Errorf("the forced track: %+v", s)
	}

	// A rescan keeps the tracks without probing again.
	before := ts.sv.ix.Load().Episode(e2.ID).Media
	ts.sv.rescan()
	ts.sv.waitProbing()
	if after := ts.sv.ix.Load().Episode(e2.ID).Media; after != before {
		t.Error("probed again after a rescan")
	}
}

// The probe pass saves the index when it's done, so a restart has the tracks.
func TestProbePassSaves(t *testing.T) {
	if library.FFprobe() == "" {
		t.Skip("needs ffprobe")
	}
	movies, tv := testLibrary(t)
	st, _ := loadState(filepath.Join(t.TempDir(), "state.json"))
	index := filepath.Join(t.TempDir(), "index.json")
	sv := &Server{cfg: config.Config{MoviesDir: movies, TVDir: tv, IndexFile: index}, state: st}
	sv.rescan()
	sv.waitProbing()
	ix, err := library.LoadIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	if got := ix.Unprobed(); len(got) != 0 {
		t.Errorf("unprobed in the saved index: %q", got)
	}
}
