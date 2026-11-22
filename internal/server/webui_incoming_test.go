//go:build webui && files

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mvd/internal/library"
	"mvd/internal/testutil"
)

// The Incoming page: the pickers, the uploads the server holds, and the
// import folder's items; /files and /downloads are gone.
func TestUIIncoming(t *testing.T) {
	ts, dir := uploadServer(t)
	h := body(t, ts.do("GET", "/incoming", "", nil))
	for _, want := range []string{`id="pick"`, `id="pickdir"`, `data-exts="`, `.mkv`, `/static/incoming.js`,
		`href="/incoming" aria-current="page"`, "Nothing coming in. Drop files here."} {
		if !strings.Contains(h, want) {
			t.Errorf("incoming page lacks %s", want)
		}
	}
	// TMDB's credit, at the foot of the page (as on every page but the player's).
	if !inOrder(h, `<footer class="credit">`, `href="https://www.themoviedb.org/"`, `src="/static/tmdb.svg`,
		"This product uses the TMDB API but is not endorsed or certified by TMDB.") {
		t.Error("no TMDB credit")
	}
	if r := ts.do("GET", "/static/tmdb.svg", "", nil); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/svg+xml" {
		t.Errorf("logo: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	for _, gone := range []string{"/files", "/downloads"} {
		if r := ts.do("GET", gone, "", nil); r.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", gone, r.StatusCode)
		}
	}

	ts.startUpload(`{"name": "Going.mkv", "size": 100}`, 201)
	old := ts.startUpload(`{"name": "Stalled.mkv", "size": 100}`, 201)
	ts.put(old.ID, 0, make([]byte, 30))
	then := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, ".uploads", old.ID+".part"), then, then)
	h = body(t, ts.do("GET", "/ui/incoming", "", nil))
	if !inOrder(h, "Stalled.mkv", "1 KB from another computer · stopped an hour ago", `data-cancel-upload="`+old.ID+`"`,
		"Going.mkv", "1 KB from another computer · starting") {
		t.Errorf("uploads:\n%s", h)
	}
	h = body(t, ts.do("DELETE", "/ui/uploads/"+old.ID, "", nil))
	if strings.Contains(h, "Stalled.mkv") {
		t.Errorf("not canceled:\n%s", h)
	}

	// Something in the import folder: waiting for an import, and its 🗑.
	testutil.Write(t, filepath.Join(dir, "Copied.mkv"), "12345")
	os.Chtimes(filepath.Join(dir, "Copied.mkv"), then, then)
	h = body(t, ts.do("GET", "/ui/incoming", "", nil))
	if !inOrder(h, "Copied.mkv", "1 KB · waiting for import", `name="item" value="Copied.mkv"`) {
		t.Errorf("waiting item:\n%s", h)
	}
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	h = body(t, ts.do("POST", "/ui/incoming/trash", "item=Copied.mkv", form))
	// Review L7: with no trash (as here), it's deleted, and says so.
	if !strings.Contains(h, "Deleted Copied.mkv.") || strings.Contains(h, "waiting for import") {
		t.Errorf("trashed:\n%s", h)
	}
	// With a trash, moved there.
	testutil.Write(t, filepath.Join(dir, "Trashed.mkv"), "12345")
	os.Chtimes(filepath.Join(dir, "Trashed.mkv"), then, then)
	ts.sv.cfg.TrashDir, ts.sv.cfg.TrashMaxAge, ts.sv.cfg.TrashMaxSize = t.TempDir(), time.Hour, 1<<30
	h = body(t, ts.do("POST", "/ui/incoming/trash", "item=Trashed.mkv", form))
	ts.sv.cfg.TrashDir = ""
	if !strings.Contains(h, "Moved Trashed.mkv to the trash.") {
		t.Errorf("trashed:\n%s", h)
	}
	for _, bad := range []string{"item=../outside", "item=", "item=.uploads"} {
		if h := body(t, ts.do("POST", "/ui/incoming/trash", bad, form)); !strings.Contains(h, "flash bad") {
			t.Errorf("%s: trashed?\n%s", bad, h)
		}
	}

	// Uploads turned off: no pickers.
	ts.sv.cfg.Uploads = false
	if h := body(t, ts.do("GET", "/incoming", "", nil)); strings.Contains(h, `id="pick"`) || !strings.Contains(h, "Uploads are off") {
		t.Errorf("uploads off, still an uploader")
	}
}

// Review L12: an mvd import at a terminal holds import.lock, not the
// server's own lock, and the website's trash actions wait for it too.
func TestTrashWaitsForTerminalImport(t *testing.T) {
	ts, dir := uploadServer(t)
	ts.sv.cfg.DataDir = t.TempDir()
	ts.sv.cfg.TrashDir, ts.sv.cfg.TrashMaxAge, ts.sv.cfg.TrashMaxSize = t.TempDir(), time.Hour, 1<<30
	testutil.Write(t, filepath.Join(dir, "Busy.mkv"), "12345")
	then := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, "Busy.mkv"), then, then)
	release, err := library.LockImport(ts.sv.cfg) // mvd import, running
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	if h := body(t, ts.do("POST", "/ui/incoming/trash", "item=Busy.mkv", form)); !strings.Contains(h, "flash bad") {
		t.Errorf("trashed during an import:\n%s", h)
	}
	testutil.MustExist(t, dir, "Busy.mkv")
	if h := body(t, ts.do("POST", "/ui/incoming/empty-trash", "", nil)); !strings.Contains(h, "flash bad") {
		t.Errorf("emptied the trash during an import:\n%s", h)
	}
}

// Review L13: polls from every open tab share one look at the import
// folder and Transmission for a few seconds; an answer to a button looks
// afresh.
func TestIncomingPollsShareALook(t *testing.T) {
	ts, dir := uploadServer(t)
	now := time.Now()
	ts.sv.now = func() time.Time { return now }
	ts.sv.pollShare = incomingShare
	then := now.Add(-time.Hour)
	add := func(name string) {
		testutil.Write(t, filepath.Join(dir, name), "x")
		os.Chtimes(filepath.Join(dir, name), then, then)
	}
	add("First.mkv")
	if h := body(t, ts.do("GET", "/ui/incoming", "", nil)); !strings.Contains(h, "First.mkv") {
		t.Fatalf("first poll:\n%s", h)
	}
	add("Second.mkv")
	if h := body(t, ts.do("GET", "/ui/incoming", "", nil)); strings.Contains(h, "Second.mkv") {
		t.Error("a poll a moment later looked again")
	}
	now = now.Add(incomingShare)
	if h := body(t, ts.do("GET", "/ui/incoming", "", nil)); !strings.Contains(h, "Second.mkv") {
		t.Error("a later poll didn't look again")
	}
	add("Third.mkv")
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	if h := body(t, ts.do("POST", "/ui/incoming/trash", "item=First.mkv", form)); !strings.Contains(h, "Third.mkv") {
		t.Errorf("a button's answer didn't look afresh:\n%s", h)
	}
}
