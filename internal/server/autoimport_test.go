package server

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
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

// Something that arrives while an import runs (a download Transmission has
// just moved in) is looked at a minute on, although the folder hasn't
// changed since that import finished.
func TestAutoImportArrivedDuringImport(t *testing.T) {
	ts, l := newImportServer(t)
	tmdbSrv := testutil.TMDBAll(t)
	var once sync.Once
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { // the import has listed the folder, and is matching
			os.WriteFile(filepath.Join(l.Import, "Arrived.2011.mkv"), []byte("\x1a\x45\xdf\xa3"), 0o666)
		})
		httputil.NewSingleHostReverseProxy(mustURL(t, tmdbSrv.URL)).ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	ts.sv.newTMDB = func(c config.Config) *tmdb.Client {
		tm := tmdb.New(c)
		tm.SetBaseURLs(proxy.URL+"/3", proxy.URL+"/t/p/")
		return tm
	}

	ts.uploadFile("Fast.Five.2011.mkv")
	if !ts.sv.autoImportOnce() {
		t.Fatal("didn't run for a new upload")
	}
	testutil.MustExist(t, l.Import, "Arrived.2011.mkv")
	if _, ok := ts.resultFor("Arrived.2011.mkv"); ok {
		t.Fatal("the import saw Arrived: the test didn't make it arrive mid-import")
	}
	ts.sv.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if !ts.sv.autoImportOnce() {
		t.Error("didn't run a minute on for an item that arrived during the last import")
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
