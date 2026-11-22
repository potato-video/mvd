//go:build downloads

package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"path/filepath"
	"strings"
	"testing"

	"mvd/internal/testutil"
	"mvd/internal/torrent"
)

func TestDownloadsAPI(t *testing.T) {
	ts, f, l := newDownloadsServer(t)
	defer ts.hs.Close()

	// Add by magnet, as JSON; it's set to finish in import/tv.
	var added struct {
		Torrent   torrent.Torrent `json:"torrent"`
		Duplicate bool            `json:"duplicate"`
	}
	r := ts.do("POST", "/api/downloads", `{"source": "magnet:?xt=urn:btih:abc&dn=Severance.S01E02.mkv", "kind": "tv"}`,
		map[string]string{"Content-Type": "application/json"})
	json.NewDecoder(r.Body).Decode(&added)
	r.Body.Close()
	_, ratioSet := f.Ratios[added.Torrent.ID]
	if r.StatusCode != 200 || added.Torrent.Name != "Severance.S01E02.mkv" ||
		f.Added[0]["download_dir"] != filepath.Join(l.Import, "tv") || ratioSet { // seed_ratio 0: no per-torrent limit
		t.Fatalf("add: %d %+v %v", r.StatusCode, added, f.Added)
	}

	// Add a .torrent file, as a form upload.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("torrent", "movie.torrent")
	fw.Write([]byte("d8:announce3:urle"))
	mw.WriteField("kind", "movie")
	mw.Close()
	r = ts.do("POST", "/api/downloads", buf.String(), map[string]string{"Content-Type": mw.FormDataContentType()})
	body(t, r)
	if r.StatusCode != 200 || f.Added[1]["metainfo"] == nil || f.Added[1]["download_dir"] != filepath.Join(l.Import, "movies") {
		t.Errorf("upload: %d %v", r.StatusCode, f.Added)
	}
	if r := ts.do("POST", "/api/downloads", `{"source": "not a link"}`, map[string]string{"Content-Type": "application/json"}); r.StatusCode != 400 {
		t.Errorf("bad source: %d", r.StatusCode)
	}

	// The list.
	var dl downloadsView
	ts.json("GET", "/api/downloads", "", &dl)
	if !dl.Configured || len(dl.Torrents) != 2 || !dl.Torrents[0].InImport {
		t.Errorf("list: %+v", dl)
	}

	// The download finishes into import/tv; an import files it and removes it.
	testutil.Write(t, filepath.Join(l.Import, "tv/Severance.S01E02.mkv"), "\x1a\x45\xdf\xa3")
	f.Mu.Lock()
	for i := range f.Torrents {
		if f.Torrents[i].Name == "Severance.S01E02.mkv" {
			f.Torrents[i].PercentDone, f.Torrents[i].Status = 1, torrent.Seeding
		}
	}
	f.Mu.Unlock()
	var res importResult
	ts.json("POST", "/api/import", "", &res)
	if res.Episodes != 1 || !strings.Contains(res.Output, "Half Loop") {
		t.Errorf("import: %+v", res)
	}
	testutil.MustExist(t, l.TV, "Severance (2022)/Season 01/Severance (2022) - S01E02 - Half Loop.mkv")
	var shows []showView
	ts.json("GET", "/api/shows", "", &shows)
	if len(shows) != 1 {
		t.Errorf("server didn't rescan after import: %+v", shows)
	}
}

func TestResumeAPI(t *testing.T) {
	ts, f, _ := newDownloadsServer(t)
	defer ts.hs.Close()
	f.Torrents = []testutil.Torrent{
		{ID: 7, Name: "A", Status: torrent.Stopped, PercentDone: 0.3},
		{ID: 8, Name: "B", Status: torrent.Stopped, PercentDone: 0.5},
	}
	var dl downloadsView
	ts.json("GET", "/api/downloads", "", &dl)
	if !dl.Torrents[0].Paused {
		t.Errorf("not shown as paused: %+v", dl.Torrents[0])
	}
	var res map[string]int
	ts.json("POST", "/api/downloads/7/resume", "", &res)
	if res["resumed"] != 1 || len(f.Started) != 1 || f.Started[0] != 7 {
		t.Errorf("resume one: %v %v", res, f.Started)
	}
	ts.json("POST", "/api/downloads/all/resume", "", &res)
	if res["resumed"] != 1 || f.Started[1] != 8 {
		t.Errorf("resume all (only B is still paused): %v %v", res, f.Started)
	}
}

func TestPauseAPI(t *testing.T) {
	ts, f, _ := newDownloadsServer(t)
	defer ts.hs.Close()
	f.Torrents = []testutil.Torrent{
		{ID: 7, Name: "A", Status: torrent.Downloading, PercentDone: 0.3},
		{ID: 8, Name: "B", Status: torrent.Seeding, PercentDone: 1},
		{ID: 9, Name: "C", Status: torrent.Stopped, PercentDone: 0.5},
	}
	var dl downloadsView
	ts.json("GET", "/api/downloads", "", &dl)
	if !dl.Torrents[0].Active || dl.Torrents[2].Active {
		t.Errorf("active wrong: %+v", dl.Torrents)
	}
	var res map[string]int
	ts.json("POST", "/api/downloads/7/pause", "", &res)
	if res["paused"] != 1 || len(f.Stopped) != 1 || f.Stopped[0] != 7 {
		t.Errorf("pause one: %v %v", res, f.Stopped)
	}
	ts.json("POST", "/api/downloads/all/pause", "", &res)
	if res["paused"] != 1 || f.Stopped[1] != 8 {
		t.Errorf("pause all (only B is still going): %v %v", res, f.Stopped)
	}
	// Paused while seeding, short of its ratio: it can be resumed.
	ts.json("GET", "/api/downloads", "", &dl)
	for _, d := range dl.Torrents {
		if d.Active || !d.Paused {
			t.Errorf("after pausing all: %+v", d)
		}
	}
}

func TestRemoveDownloadAPI(t *testing.T) {
	ts, f, _ := newDownloadsServer(t)
	defer ts.hs.Close()
	f.Torrents = []testutil.Torrent{
		{ID: 7, Name: "Half", Status: torrent.Downloading, PercentDone: 0.3},
		{ID: 8, Name: "Whole", Status: torrent.Seeding, PercentDone: 1},
		{ID: 78, Name: "Other", Status: torrent.Downloading, PercentDone: 0.1},
	}
	var res struct {
		Name        string `json:"name"`
		DeletedData bool   `json:"deleted_data"`
	}
	// Unfinished: its partial data goes too.
	ts.json("DELETE", "/api/downloads/7", "", &res)
	if res.Name != "Half" || !res.DeletedData || len(f.Deleted) != 1 || f.Deleted[0] != 7 {
		t.Errorf("remove unfinished: %+v deleted %v", res, f.Deleted)
	}
	// Finished: its files stay, for import.
	ts.json("DELETE", "/api/downloads/8", "", &res)
	if res.Name != "Whole" || res.DeletedData || len(f.Deleted) != 1 || len(f.Torrents) != 1 {
		t.Errorf("remove finished: %+v deleted %v left %v", res, f.Deleted, f.Torrents)
	}
	// Only an ID: not a name, nor part of another ID.
	for _, id := range []string{"7", "Other", "all"} {
		if r := ts.do("DELETE", "/api/downloads/"+id, "", nil); r.StatusCode != 404 {
			t.Errorf("DELETE %s: %d, want 404", id, r.StatusCode)
		}
	}
	if len(f.Torrents) != 1 {
		t.Errorf("removed too much: %v", f.Torrents)
	}
}

func TestQueueOrder(t *testing.T) {
	ts, f, _ := newDownloadsServer(t)
	defer ts.hs.Close()
	f.Torrents = []testutil.Torrent{
		{ID: 1, Name: "Second", Status: torrent.DownloadWait, QueuePosition: 1, Added: 200},
		{ID: 2, Name: "Third", Status: torrent.DownloadWait, QueuePosition: 2, Added: 300},
		{ID: 3, Name: "First", Status: torrent.Downloading, QueuePosition: 0, Added: 100},
	}
	names := func() string {
		var dl downloadsView
		ts.json("GET", "/api/downloads", "", &dl)
		var n []string
		for _, d := range dl.Torrents {
			n = append(n, d.Name)
		}
		return strings.Join(n, " ")
	}
	// In queue order: the oldest, first to go, at the top.
	if got := names(); got != "First Second Third" {
		t.Errorf("order: %s", got)
	}
	// Third to the top: its place in the list is its place in the queue.
	var res downloadsView // the list, in its new order
	ts.json("POST", "/api/downloads/2/move", `{"position": 0}`, &res)
	if res.Torrents[0].Name != "Third" {
		t.Errorf("move's answer: %+v", res.Torrents)
	}
	if got := names(); got != "Third First Second" || len(f.Moves) != 1 || f.Moves[0] != [2]int{2, 0} {
		t.Errorf("after moving Third to the top: %s %v", got, f.Moves)
	}
	// Past the end: the end.
	ts.json("POST", "/api/downloads/2/move", `{"position": 9}`, &res)
	if got := names(); got != "First Second Third" {
		t.Errorf("after moving Third past the end: %s", got)
	}
	for body, want := range map[string]int{`{"position": -1}`: 400, `nope`: 400} {
		if r := ts.do("POST", "/api/downloads/2/move", body, nil); r.StatusCode != want {
			t.Errorf("move %s: %d, want %d", body, r.StatusCode, want)
		}
	}
	if r := ts.do("POST", "/api/downloads/99/move", `{"position": 0}`, nil); r.StatusCode != 404 {
		t.Errorf("move of nothing: %d", r.StatusCode)
	}
}

// Review L9: Transmission numbers its torrents afresh each time it starts,
// so a download is named by its hash, which doesn't change: a request made
// from a list fetched before a restart acts on the one it meant.
func TestDownloadsByHash(t *testing.T) {
	ts, f, _ := newDownloadsServer(t)
	defer ts.hs.Close()
	f.Mu.Lock()
	f.Torrents = []testutil.Torrent{
		{ID: 1, Name: "Wanted", Hash: "aaaa000000000000000000000000000000000001", PercentDone: 0.5},
		{ID: 2, Name: "Keep", Hash: "bbbb000000000000000000000000000000000002", PercentDone: 0.5},
	}
	f.Mu.Unlock()
	var dl downloadsView
	ts.json("GET", "/api/downloads", "", &dl)
	// Transmission restarts and numbers them the other way round.
	f.Mu.Lock()
	f.Torrents[0].ID, f.Torrents[1].ID = 2, 1
	f.Mu.Unlock()
	r := ts.do("DELETE", "/api/downloads/aaaa000000000000000000000000000000000001", "", nil)
	r.Body.Close()
	if r.StatusCode != 200 || len(f.Deleted) != 1 || f.Deleted[0] != 2 {
		t.Errorf("deleted %v (%d), want the one now numbered 2", f.Deleted, r.StatusCode)
	}
	if r := ts.do("POST", "/api/downloads/bbbb000000000000000000000000000000000002/pause", "", nil); r.StatusCode != 200 {
		t.Errorf("pause by hash: %d", r.StatusCode)
	}
}
