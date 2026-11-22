package server

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"mvd/internal/config"
	"mvd/internal/testutil"
	"mvd/internal/tmdb"
)

// A server with an import folder, auto_import on, and the fake TMDB.
func newImportServer(t *testing.T) (*testServer, testutil.Lib) {
	tmdbSrv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	st, _ := loadState(filepath.Join(t.TempDir(), "state.json"))
	cfg := config.Config{APIKey: "k", Language: "en-US", Region: "GB", CacheDir: t.TempDir(),
		MoviesDir: l.Movies, TVDir: l.TV, ImportDir: l.Import, TrashDir: l.Trash, DataDir: l.Data,
		TrashMaxAge: 1e15, TrashMaxSize: 1 << 40, AutoImport: true, Uploads: true}
	sv := &Server{cfg: cfg, state: st}
	sv.rescan()
	sv.newTMDB = func(c config.Config) *tmdb.Client {
		tm := tmdb.New(c)
		tm.SetBaseURLs(tmdbSrv.URL+"/3", tmdbSrv.URL+"/t/p/")
		return tm
	}
	ts := &testServer{t, httptest.NewServer(sv.routes()), sv}
	t.Cleanup(ts.hs.Close)
	return ts, l
}

// Something still arriving is tried again a minute later; an empty import
// folder never runs an import.
func TestAutoImportWaits(t *testing.T) {
	ts, l := newImportServer(t)
	if ts.sv.autoImportOnce() {
		t.Error("ran on an empty import folder")
	}
	testutil.Write(t, filepath.Join(l.Import, "Fast.Five.2011.mkv"), "\x1a\x45\xdf\xa3")
	if !ts.sv.autoImportOnce() {
		t.Fatal("didn't run for a new file")
	}
	testutil.MustExist(t, l.Import, "Fast.Five.2011.mkv") // still settling
	if ts.sv.autoImportOnce() {
		t.Error("retried at once")
	}
	ts.sv.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if !ts.sv.autoImportOnce() {
		t.Error("didn't retry a minute on")
	}
}

// Off when set off.
func TestAutoImportOff(t *testing.T) {
	ts, l := newImportServer(t)
	ts.sv.cfg.AutoImport = false
	testutil.Write(t, filepath.Join(l.Import, "Fast.Five.2011.mkv"), "\x1a\x45\xdf\xa3")
	if ts.sv.autoImportOnce() {
		t.Error("ran with auto_import off")
	}
}
