//go:build files

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mvd/internal/library"
	"mvd/internal/testutil"
)

// leftNow is GET /api/import's items.
func (ts *testServer) leftNow() []leftItem {
	ts.t.Helper()
	var out struct {
		Items   []leftItem
		Stopped string
	}
	ts.json("GET", "/api/import", "", &out)
	return out.Items
}

func (ts *testServer) post(path, body string) (int, string) {
	ts.t.Helper()
	r := ts.do("POST", path, body, map[string]string{"Content-Type": "application/json"})
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

// What the import left, a search, a title's details, and an answer that
// files it at the next import.
func TestNeedsAnswer(t *testing.T) {
	ts, l := newImportServer(t)
	ts.uploadFile("Dune.mkv")
	if !ts.sv.autoImportOnce() {
		t.Fatal("didn't import")
	}
	left := ts.leftNow()
	if len(left) != 1 {
		t.Fatalf("left: %+v", left)
	}
	d := left[0]
	if d.Item != "Dune.mkv" || !d.Answerable || d.Kind != "movie" || d.Why != "Couldn't tell which this is." ||
		len(d.Units) != 1 || len(d.Units[0].Candidates) < 2 || d.Sig == "" {
		t.Fatalf("Dune: %+v", d)
	}
	if c := d.Units[0].Candidates[0]; c.Score == 0 || c.URL != tmdbURL("movie", c.TMDB) || c.Overview == "" {
		t.Errorf("candidate: %+v", c)
	}

	var res searchResult
	ts.json("GET", "/api/import/search?kind=movie&q=Get+Carter+(2000)", "", &res)
	if len(res.Candidates) == 0 || res.Candidates[0].TMDB != 10477 {
		t.Errorf("search: %+v", res)
	}
	res = searchResult{}
	ts.json("GET", "/api/import/search?q=https://www.themoviedb.org/movie/438631-dune", "", &res)
	if res.TMDB != 438631 || res.Kind != "movie" {
		t.Errorf("reference: %+v", res)
	}
	if r := ts.do("GET", "/api/import/search?q=Dune", "", nil); r.StatusCode != 400 {
		t.Errorf("words without a kind: %d", r.StatusCode)
	}

	var det titleDetails
	ts.json("GET", "/api/import/details?item=Dune.mkv&kind=movie&tmdb=438631", "", &det)
	if det.Title != "Dune" || det.Year != 2021 || det.Into != "Into Movies" || det.Poster != "/api/import/poster/movie/438631" ||
		len(det.Facts) == 0 || det.Facts[0] != "2h" {
		t.Errorf("details: %+v", det)
	}
	if r := ts.do("GET", det.Poster, "", nil); r.StatusCode != 200 || r.Header.Get("Content-Type") == "" {
		t.Errorf("poster: %d", r.StatusCode)
	}
	if r := ts.do("GET", "/api/import/details?item=Nope.mkv&kind=movie&tmdb=438631", "", nil); r.StatusCode != 404 {
		t.Errorf("details for nothing: %d", r.StatusCode)
	}

	// Answered elsewhere: an answer about the item as it was is refused.
	if code, b := ts.post("/api/import/choose", `{"item": "Dune.mkv", "sig": "123", "kind": "movie", "tmdb": 438631}`); code != 409 {
		t.Errorf("stale answer: %d %s", code, b)
	}
	if code, _ := ts.post("/api/import/choose", `{"item": "../Dune.mkv", "sig": "`+d.Sig+`", "kind": "movie", "tmdb": 438631}`); code != 404 {
		t.Errorf("outside the import folder: %d", code)
	}
	testutil.MustNotExist(t, l.Import, "Dune.mvd")

	if code, b := ts.post("/api/import/choose", `{"item": "Dune.mkv", "sig": "`+d.Sig+`", "kind": "movie", "tmdb": 438631}`); code != 200 {
		t.Fatalf("choose: %d %s", code, b)
	}
	// The answer starts an import (here, with no auto-import loop running,
	// one of its own).
	for i := 0; i < 100 && !library.Exists(filepath.Join(l.Movies, "Dune (2021)", "Dune (2021).mkv")); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	importMu.Lock() // that import is done
	importMu.Unlock()
	testutil.MustExist(t, l.Movies, "Dune (2021)/Dune (2021).mkv")
	if left := ts.leftNow(); len(left) != 0 {
		t.Errorf("still left: %+v", left)
	}
	if code, _ := ts.post("/api/import/choose", `{"item": "Dune.mkv", "sig": "`+d.Sig+`", "kind": "movie", "tmdb": 438631}`); code != 404 {
		t.Errorf("answered twice: %d", code)
	}
}

func TestNeedsAnswerRejectsChangedHint(t *testing.T) {
	ts, l := newImportServer(t)
	ts.uploadFile("Dune.mkv")
	if _, err := ts.sv.runImport(); err != nil {
		t.Fatal(err)
	}
	left := ts.leftNow()
	if len(left) != 1 {
		t.Fatalf("left: %+v", left)
	}
	d := left[0]

	if err := library.WriteHint(filepath.Join(l.Import, "Dune.mkv"), "movie", 438631); err != nil {
		t.Fatal(err)
	}
	if code, b := ts.post("/api/import/choose", `{"item":"Dune.mkv","sig":"`+d.Sig+`","kind":"movie","tmdb":438631}`); code != 409 {
		t.Errorf("stale answer after hint changed: %d %s", code, b)
	}
}

// Something that can't be told movie or TV asks which first; an item with
// several questions can't be answered from one hint; 🗑 takes one away.
func TestNeedsKinds(t *testing.T) {
	ts, l := newImportServer(t)
	testutil.Write(t, filepath.Join(l.Import, "Mixed", "Dune.mkv"), "\x1a\x45\xdf\xa3")
	testutil.Write(t, filepath.Join(l.Import, "Mixed", "Get.Carter.mkv"), "\x1a\x45\xdf\xa3")
	testutil.Write(t, filepath.Join(l.Import, "Notes", "readme.txt"), "x")
	// As if uploaded: whole, so the import doesn't wait for them to settle.
	ts.sv.ups.mu.Lock()
	if ts.sv.ups.finished == nil {
		ts.sv.ups.finished = map[string]time.Time{}
	}
	ts.sv.ups.finished[filepath.Join(l.Import, "Mixed")] = time.Now()
	ts.sv.ups.finished[filepath.Join(l.Import, "Notes")] = time.Now()
	ts.sv.ups.mu.Unlock()
	if _, err := ts.sv.runImport(); err != nil {
		t.Fatal(err)
	}
	byItem := map[string]leftItem{}
	for _, x := range ts.leftNow() {
		byItem[x.Item] = x
	}
	if m := byItem["Mixed"]; m.Answerable || len(m.Units) != 2 {
		t.Errorf("two questions: %+v", m)
	}
	if n := byItem["Notes"]; n.Problem != "no-videos" || n.Why != "No videos in it." {
		t.Errorf("no videos: %+v", n)
	}
	if code, b := ts.post("/api/import/trash", `{"item": "Notes"}`); code != 200 {
		t.Errorf("trash: %d %s", code, b)
	}
	testutil.MustNotExist(t, l.Import, "Notes")
	if code, _ := ts.post("/api/import/trash", `{"item": "Notes"}`); code != 404 {
		t.Errorf("trash twice: %d", code)
	}
}

// A show's videos the import left: each with why, the episodes to say
// which one it is from, an answer that files it, and one left out.
func TestNeedsEpisodes(t *testing.T) {
	ts, l := newImportServer(t)
	item := "Ghosts.2019.S02.720p.WEB-DL"
	ts.uploadFile(item + "/Ghosts.2019.S02E01.The.Grey.Lady.720p.WEB-DL.mkv")
	ts.uploadFile(item + "/Ghosts.2019.S02E04.Bloopers.720p.WEB-DL.mkv") // two specials it might be
	ts.uploadFile(item + "/Ghosts.2019.S02E05.720p.WEB-DL.mkv")          // no title
	ts.sv.ups.mu.Lock()
	ts.sv.ups.finished[filepath.Join(l.Import, item)] = time.Now() // a folder upload, all in
	ts.sv.ups.mu.Unlock()
	if _, err := ts.sv.runImport(); err != nil {
		t.Fatal(err)
	}
	testutil.MustExist(t, l.TV, "Ghosts (2019)/Season 02/Ghosts (2019) - S02E01 - The Grey Lady.mkv")
	left := ts.leftNow()
	if len(left) != 1 || len(left[0].Files) != 2 {
		t.Fatalf("left: %+v", left)
	}
	d := left[0]
	bloopers, untitled := d.Files[0], d.Files[1]
	if bloopers.Path != item+"/Ghosts.2019.S02E04.Bloopers.720p.WEB-DL.mkv" || bloopers.Why != "TMDB has no S02E04" ||
		bloopers.Show != 17174 || !strings.Contains(d.Why, "say which episode each is") {
		t.Fatalf("files: %+v (%s)", d.Files, d.Why)
	}

	var eps episodeList
	ts.json("GET", "/api/import/episodes?item="+url.QueryEscape(d.Item)+"&video="+url.QueryEscape(bloopers.Path)+"&tmdb=17174", "", &eps)
	if eps.Title != "Ghosts (2019)" || len(eps.Seasons) != 3 || eps.Seasons[0].Season != 0 || eps.Seasons[2].Season != 2 ||
		len(eps.Seasons[0].Episodes) != 4 || len(eps.Suggested) != 2 || eps.Suggested[0].Code != "S00E03" {
		t.Errorf("episodes: %+v", eps)
	}
	if r := ts.do("GET", "/api/import/episodes?item="+url.QueryEscape(d.Item)+"&video=../x.mkv&tmdb=17174", "", nil); r.StatusCode != 400 {
		t.Errorf("a video outside the item: %d", r.StatusCode)
	}

	body := fmt.Sprintf(`{"item": %q, "sig": %q, "video": %q, "tmdb": 17174, "season": 0, "episode": 4}`, d.Item, d.Sig, bloopers.Path)
	if code, b := ts.post("/api/import/episode", body); code != 200 {
		t.Fatalf("episode: %d %s", code, b)
	}
	want := "Ghosts (2019)/Season 00/Ghosts (2019) - S00E04 - Series 2 Bloopers.mkv"
	for i := 0; i < 100 && !library.Exists(filepath.Join(l.TV, want)); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	importMu.Lock() // that import is done
	importMu.Unlock()
	testutil.MustExist(t, l.TV, want)

	// Left out: the last of it goes, and the item with it.
	if code, b := ts.post("/api/import/trash", fmt.Sprintf(`{"item": %q, "video": %q}`, d.Item, untitled.Path)); code != 200 {
		t.Fatalf("trash: %d %s", code, b)
	}
	for i := 0; i < 100 && library.Exists(filepath.Join(l.Import, item)); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	importMu.Lock()
	importMu.Unlock()
	testutil.MustNotExist(t, l.Import, item)
	if left := ts.leftNow(); len(left) != 0 {
		t.Errorf("still left: %+v", left)
	}
}

// GET /api/trash says how much is in the trash, once an import has looked;
// DELETE empties it of what mvd put there, and nothing else, and not while
// an import is running.
func TestAPITrash(t *testing.T) {
	ts, l := newImportServer(t)
	testutil.Write(t, filepath.Join(l.Trash, "2026-11-01 120000 Old.Thing", "a.nfo"), "12345")
	testutil.Write(t, filepath.Join(l.Trash, "not mvd's.txt"), "x")
	size := func(method string) (code int, bytes *int64) {
		t.Helper()
		r := ts.do(method, "/api/trash", "", nil)
		defer r.Body.Close()
		var out struct{ Bytes *int64 }
		if r.StatusCode == 200 {
			if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
		}
		return r.StatusCode, out.Bytes
	}
	if code, n := size("GET"); code != 200 || n != nil {
		t.Errorf("before an import has looked: %d, %v", code, n)
	}
	ts.sv.imports.trashed(5<<30, false)
	if code, n := size("GET"); code != 200 || n == nil || *n != 5<<30 {
		t.Errorf("after: %d, %v", code, n)
	}

	importMu.Lock()
	code, _ := size("DELETE")
	importMu.Unlock()
	if code != 409 {
		t.Errorf("emptied during an import: %d", code)
	}
	release, err := library.LockImport(ts.sv.cfg) // mvd import, at a terminal
	if err != nil {
		t.Fatal(err)
	}
	code, _ = size("DELETE")
	release()
	if code != 409 {
		t.Errorf("emptied during an import at a terminal: %d", code)
	}
	testutil.MustExist(t, l.Trash, "2026-11-01 120000 Old.Thing")

	if code, n := size("DELETE"); code != 200 || n == nil || *n != 5<<30 {
		t.Errorf("empty: %d, %v", code, n)
	}
	testutil.MustNotExist(t, l.Trash, "2026-11-01 120000 Old.Thing")
	testutil.MustExist(t, l.Trash, "not mvd's.txt")
	if code, n := size("GET"); code != 200 || n == nil || *n != 0 {
		t.Errorf("emptied: %d, %v", code, n)
	}
}
