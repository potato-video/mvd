package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mvd/internal/config"
	"mvd/internal/library"
	"mvd/internal/testutil"
	"mvd/internal/tmdb"
)

func newDownloadsServer(t *testing.T) (*testServer, *testutil.Transmission, testutil.Lib) {
	tmdbSrv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	f := testutil.NewTransmission(false)
	trSrv := f.Server(t)
	st, _ := loadState(filepath.Join(t.TempDir(), "state.json"))
	cfg := config.Config{APIKey: "k", Language: "en-US", Region: "GB", CacheDir: t.TempDir(),
		MoviesDir: l.Movies, TVDir: l.TV, ImportDir: l.Import, TrashDir: l.Trash, DataDir: l.Data,
		TrashMaxAge: 1e15, TrashMaxSize: 1 << 40, Transmission: trSrv.URL, SeedRatio: 0}
	sv := &Server{cfg: cfg, state: st}
	sv.rescan()
	// Point the server's imports at the fake TMDB.
	sv.newTMDB = func(c config.Config) *tmdb.Client {
		tm := tmdb.New(c)
		tm.SetBaseURLs(tmdbSrv.URL+"/3", tmdbSrv.URL+"/t/p/")
		return tm
	}
	return &testServer{t, httptest.NewServer(sv.routes()), sv}, f, l
}

// The server's import respects a lock taken by the command line.
func TestServerImportLock(t *testing.T) {
	ts, _, _ := newDownloadsServer(t)
	defer ts.hs.Close()
	release, _ := library.LockImport(ts.sv.cfg)
	defer release()
	if _, err := ts.sv.runImport(); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("server import ran alongside another: %v", err)
	}
}

// A small scraped library: movies and shows via the fake TMDBs.
func testLibrary(t *testing.T) (movies, tv string) {
	video := t.TempDir()
	movies, tv = filepath.Join(video, "movies"), filepath.Join(video, "tv")
	mkv := "\x1a\x45\xdf\xa3 0123456789 matroska"
	testutil.Write(t, filepath.Join(movies, "The Godfather (1972)/The Godfather (1972).mkv"), mkv)
	testutil.Write(t, filepath.Join(movies, "The Godfather (1972)/The Godfather (1972).en.srt"),
		"1\r\n00:00:01,000 --> 00:00:02,500\r\nCaf\xe9 \x93quoted\x94\r\n")
	testutil.Write(t, filepath.Join(movies, "Unscraped Thing (1999)/thing.mkv"), mkv)
	testutil.Write(t, filepath.Join(tv, "Severance/Season 1/Severance.S01E01.mkv"), mkv)
	// A real one when ffmpeg's installed, for its tracks (the fixtures' episode).
	if e2 := filepath.Join(tv, "Severance/Season 1/Severance.S01E02.mkv"); !testutil.WriteClip(t, e2) {
		testutil.Write(t, e2, mkv)
	}
	testutil.Write(t, filepath.Join(tv, "Severance/Specials/Severance.S00E01.mkv"), mkv)

	ms := testutil.TMDB(t)
	defer ms.Close()
	testRefresher(t, ms, movies, library.Options{Name: "godfather"}, false).Run()
	ts := testutil.TMDBTV(t)
	defer ts.Close()
	testRefresher(t, ts, tv, library.Options{}, true).RunTV()
	return movies, tv
}

// testRefresher is a refresher of root that asks the fake TMDB srv.
func testRefresher(t *testing.T, srv *httptest.Server, root string, o library.Options, tv bool) *library.Refresher {
	cfg := config.Config{APIKey: "k", Language: "en-US", Region: "GB", CacheDir: t.TempDir()}
	tm := tmdb.New(cfg)
	tm.SetBaseURLs(srv.URL+"/3", srv.URL+"/t/p/")
	s := library.NewRefresher(o, cfg, tm, root, tv)
	return s
}

type testServer struct {
	t  *testing.T
	hs *httptest.Server
	sv *Server
}

func newTestServer(t *testing.T, token string) *testServer {
	movies, tv := testLibrary(t)
	st, _ := loadState(filepath.Join(t.TempDir(), "state.json"))
	sv := &Server{cfg: config.Config{MoviesDir: movies, TVDir: tv, Token: token, DataDir: t.TempDir(), CacheDir: t.TempDir()}, state: st}
	sv.rescan()
	sv.waitProbing()
	t.Cleanup(sv.stopRemux)
	return &testServer{t, httptest.NewServer(sv.routes()), sv}
}

func (ts *testServer) do(method, path, body string, hdr map[string]string) *http.Response {
	req, _ := http.NewRequest(method, ts.hs.URL+path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	return resp
}

func (ts *testServer) json(method, path, body string, out any) {
	resp := ts.do(method, path, body, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		ts.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		ts.t.Fatalf("%s %s: %v", method, path, err)
	}
}

func TestServerLibrary(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()

	var movies []movieView
	ts.json("GET", "/api/movies", "", &movies)
	if len(movies) != 2 || movies[0].ID != "m238" || movies[0].Poster != "/art/m238/poster" ||
		movies[0].Set != "The Godfather Collection" {
		t.Fatalf("movies: %+v", movies)
	}
	// The runtime too, so a client can say how long a movie is before it's been played.
	if movies[0].Runtime != 120 {
		t.Errorf("movie runtime in the list: %d", movies[0].Runtime)
	}
	if !strings.HasPrefix(movies[1].ID, "f") || movies[1].Title != "Unscraped Thing" {
		t.Errorf("unscraped movie: %+v", movies[1])
	}

	var m movieView
	ts.json("GET", "/api/item/m238", "", &m)
	if m.Plot == "" || len(m.Streams) != 1 || len(m.Subs) != 1 || m.Subs[0].Lang != "en" || m.Subs[0].VTT != "/subs/m238/0.vtt" {
		t.Errorf("movie detail: %+v", m)
	}

	var shows []showView
	ts.json("GET", "/api/shows", "", &shows)
	if len(shows) != 1 || shows[0].ID != "s95396" || shows[0].Episodes != 2 || shows[0].Unwatched != 2 {
		t.Fatalf("shows: %+v", shows)
	}
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	if len(sh.Seasons) != 2 || sh.Seasons[0].Number != 1 || sh.Seasons[1].Number != 0 ||
		sh.Seasons[0].Episodes[1].Title != "Half Loop" || sh.Seasons[0].Poster == "" {
		t.Errorf("show detail: %+v", sh)
	}
	ep := sh.Seasons[0].Episodes[0]
	if ep.ID != "e9539600101" && !strings.HasPrefix(ep.ID, "e") {
		t.Errorf("episode id %q", ep.ID)
	}
	// Artwork comes through as JPEG.
	resp := ts.do("GET", m.Poster, "", nil)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("poster: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if r := ts.do("GET", "/api/item/nope", "", nil); r.StatusCode != 404 {
		t.Errorf("missing item: %d", r.StatusCode)
	}
}

func TestServerStreaming(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()

	// A seek: bytes 4-13 of the file.
	resp := ts.do("GET", "/stream/m238", "", map[string]string{"Range": "bytes=4-13"})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(body) != " 012345678" ||
		resp.Header.Get("Content-Range") != "bytes 4-13/24" || resp.Header.Get("Content-Type") != "video/x-matroska" {
		t.Errorf("range: %d %q %q %q", resp.StatusCode, body, resp.Header.Get("Content-Range"), resp.Header.Get("Content-Type"))
	}

	// The tail, as players do for MKV cues.
	resp = ts.do("GET", "/stream/m238", "", map[string]string{"Range": "bytes=-8"})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "matroska" {
		t.Errorf("suffix range: %q", body)
	}

	// HEAD for size, and If-Range with a stale ETag gets the whole file.
	resp = ts.do("HEAD", "/stream/m238", "", nil)
	resp.Body.Close()
	if resp.ContentLength != 24 || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("head: %d %q", resp.ContentLength, resp.Header.Get("Accept-Ranges"))
	}
	resp = ts.do("GET", "/stream/m238", "", map[string]string{"Range": "bytes=4-13", "If-Range": `"stale"`})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("stale If-Range: %d", resp.StatusCode)
	}

	// Subtitles, raw and as WebVTT (from Windows-1252).
	resp = ts.do("GET", "/subs/m238/0.vtt", "", nil)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\nCafé “quoted”\n"
	if !strings.HasPrefix(string(body), want) {
		t.Errorf("vtt:\n%q\nwant\n%q", body, want)
	}
	if r := ts.do("GET", "/subs/m238/0", "", nil); r.Header.Get("Content-Type") != "application/x-subrip" {
		t.Errorf("srt type %q", r.Header.Get("Content-Type"))
	}
	if r := ts.do("GET", "/subs/m238/7", "", nil); r.StatusCode != 404 {
		t.Errorf("missing sub: %d", r.StatusCode)
	}
}

func TestServerProgress(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1, e2 := sh.Seasons[0].Episodes[0].ID, sh.Seasons[0].Episodes[1].ID

	var p Progress
	ts.json("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, &p)
	if p.Position != 600 || p.Watched {
		t.Errorf("progress: %+v", p)
	}
	p = Progress{} // decoding into a used struct would keep its old fields
	ts.json("POST", "/api/progress/"+e1, `{"position": 3200, "duration": 3300}`, &p)
	if !p.Watched || p.Position != 0 {
		t.Errorf("near the end should count as watched: %+v", p)
	}

	// Up Next: the movie part-watched, Severance's next episode, and the
	// other movie, new.
	var up []map[string]any
	ts.json("GET", "/api/up-next", "", &up)
	ids := map[any]any{}
	for _, it := range up {
		ids[it["id"]] = it["reason"]
	}
	if len(up) != 3 || ids["m238"] != "continue" || ids[e2] != "next" {
		t.Errorf("up next: %+v", up)
	}

	// Marking the whole show watched, then the state survives a restart.
	var shv showView
	ts.json("POST", "/api/progress/s95396", `{"watched": true}`, &shv)
	if shv.Unwatched != 0 {
		t.Errorf("show watched: %+v", shv)
	}
	if err := ts.sv.state.Flush(); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(ts.sv.state.path)
	if err != nil || !st.Get(e2).Watched || st.Get("m238").Position != 600 {
		t.Errorf("reloaded state: %v %+v", err, st.All())
	}

	if r := ts.do("POST", "/api/progress/nope", `{"watched": true}`, nil); r.StatusCode != 404 {
		t.Errorf("unknown id: %d", r.StatusCode)
	}
	var counts map[string]int
	ts.json("POST", "/api/rescan", "", &counts)
	if counts["movies"] != 2 || counts["shows"] != 1 || counts["episodes"] != 3 {
		t.Errorf("rescan: %v", counts)
	}
}

func TestServerToken(t *testing.T) {
	ts := newTestServer(t, "sekrit")
	defer ts.hs.Close()
	if r := ts.do("GET", "/api/movies", "", nil); r.StatusCode != 401 {
		t.Errorf("no token: %d", r.StatusCode)
	}
	if r := ts.do("GET", "/api/movies?token=sekrit", "", nil); r.StatusCode != 200 {
		t.Errorf("query token: %d", r.StatusCode)
	}
	if r := ts.do("GET", "/stream/m238", "", map[string]string{"Authorization": "Bearer sekrit"}); r.StatusCode != 200 {
		t.Errorf("bearer token: %d", r.StatusCode)
	}
}

func TestIndexRoundTrip(t *testing.T) {
	movies, tv := testLibrary(t)
	ix := library.BuildIndex(movies, tv, nil)
	path := filepath.Join(t.TempDir(), "index.json")
	if err := ix.Save(path); err != nil {
		t.Fatal(err)
	}
	st1, _ := os.Stat(path)
	ix.Save(path) // unchanged: no write
	if st2, _ := os.Stat(path); !st2.ModTime().Equal(st1.ModTime()) {
		t.Errorf("rewrote an unchanged index")
	}
	got, err := library.LoadIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Movies) != 2 || len(got.Shows) != 1 || got.EpisodeCount() != 3 || got.Movie("m238").Subs[0].Path == "" {
		t.Fatalf("loaded: %d movies %d shows %d eps", len(got.Movies), len(got.Shows), got.EpisodeCount())
	}
	for _, se := range got.Shows[0].Seasons {
		for _, e := range se.Episodes {
			if e = got.Episode(e.ID); e == nil || e.Show == nil || e.Show.ID != "s95396" {
				t.Errorf("episode %v lost its show", e)
			}
		}
	}
}

func body(t *testing.T, r *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

// Every episode carries its show's genres and rating, so a client showing
// one on its own (an Up Next entry, say) has them.
func TestEpisodesCarryShowGenres(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	if len(sh.Genres) == 0 || sh.Rating == "" {
		t.Fatalf("the show has none to carry: %+v", sh)
	}
	want := strings.Join(sh.Genres, ",") + " " + sh.Rating
	for _, se := range sh.Seasons {
		for _, e := range se.Episodes {
			if got := strings.Join(e.Genres, ",") + " " + e.Rating; got != want {
				t.Errorf("%s: %q, want %q", e.ID, got, want)
			}
		}
	}
	var e episodeView
	ts.json("GET", "/api/item/"+sh.Seasons[0].Episodes[0].ID, "", &e)
	if strings.Join(e.Genres, ",")+" "+e.Rating != want {
		t.Errorf("item: %+v", e)
	}
	var up []map[string]any
	ts.json("GET", "/api/up-next", "", &up)
	for _, it := range up {
		if it["show_id"] == "s95396" && (it["rating"] != sh.Rating || it["genres"] == nil) {
			t.Errorf("up next: %+v", it)
		}
	}
}

// Every episode carries a poster, its season's else its show's, and its
// show's fanart, so a client showing one on its own has 2:3 and 16:9 art.
func TestEpisodesCarryShowArt(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	// Season 1 has no poster of its own; the specials keep theirs.
	for _, se := range ts.sv.ix.Load().Show("s95396").Seasons {
		if se.Number == 1 {
			se.Poster = ""
		}
	}
	want := map[int]string{0: "/art/s95396/season-0", 1: "/art/s95396/poster"}
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	for _, se := range sh.Seasons {
		for _, e := range se.Episodes {
			if e.Poster != want[e.Season] || e.Fanart != "/art/s95396/fanart" {
				t.Errorf("%s: poster %q fanart %q, want %q", e.ID, e.Poster, e.Fanart, want[e.Season])
			}
			var it episodeView
			ts.json("GET", "/api/item/"+e.ID, "", &it)
			if it.Poster != e.Poster || it.Fanart != e.Fanart {
				t.Errorf("item %s: %+v", e.ID, it)
			}
		}
	}
	var up []map[string]any
	ts.json("GET", "/api/up-next", "", &up)
	for _, it := range up {
		if it["show_id"] == "s95396" && (it["poster"] == nil || it["fanart"] == nil) {
			t.Errorf("up next: %+v", it)
		}
	}
}

// uploadServer is a test server with an import folder and uploads on.
func uploadServer(t *testing.T) (*testServer, string) {
	ts := newTestServer(t, "")
	t.Cleanup(ts.hs.Close)
	dir := t.TempDir()
	ts.sv.cfg.ImportDir, ts.sv.cfg.Uploads = dir, true
	return ts, dir
}

// Review M1: the server prunes TMDB's cache, as the commands do, rather
// than letting it grow for as long as it runs.
func TestServerPrunesTMDBCache(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "tmdb", "old.json")
	fresh := filepath.Join(dir, "tmdb", "fresh.json")
	testutil.Write(t, old, "{}")
	testutil.Write(t, fresh, "{}")
	long := time.Now().Add(-60 * 24 * time.Hour)
	os.Chtimes(old, long, long)
	sv := &Server{cfg: config.Config{CacheDir: dir}}
	stop := make(chan struct{})
	defer close(stop)
	go sv.pruneCacheEvery(time.Hour, stop)
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(old); err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("an expired answer wasn't pruned")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh answer was pruned")
	}
}

// Review L1: a subtitle number too big for an int is not found, not a
// panic (atoi wrapped round to a negative index).
func TestSubsNumberOverflow(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	for _, n := range []string{"9223372036854775808", "18446744073709551615", "2147483648", "99999999999999999999999"} {
		req, _ := http.NewRequest("GET", ts.hs.URL+"/subs/m238/"+n, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("%s: %v", n, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", n, resp.StatusCode)
		}
	}
}

// Review L10: a huge file named .srt isn't read whole to convert to WebVTT.
func TestSubsVTTSizeLimit(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	sub := ts.sv.ix.Load().Movie("m238").Subs[0].Path
	f, _ := os.OpenFile(sub, os.O_WRONLY, 0)
	f.Truncate(maxVTTSource + 1)
	f.Close()
	r := ts.do("GET", "/subs/m238/0.vtt", "", nil)
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("%d", r.StatusCode)
	}
}
