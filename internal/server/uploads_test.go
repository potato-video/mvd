//go:build files

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mvd/internal/testutil"
)

// start begins an upload, failing the test unless it gets the status wanted.
func (ts *testServer) startUpload(body string, want int) uploadView {
	ts.t.Helper()
	r := ts.do("POST", "/api/uploads", body, map[string]string{"Content-Type": "application/json"})
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != want {
		ts.t.Fatalf("start %s: %d %s", body, r.StatusCode, b)
	}
	var u uploadView
	json.Unmarshal(b, &u)
	return u
}

// put sends one piece, from offset, and returns the status and the offset
// the server says it has.
func (ts *testServer) put(id string, offset int64, piece []byte) (int, int64, bool) {
	ts.t.Helper()
	req, _ := http.NewRequest("PATCH", ts.hs.URL+"/api/uploads/"+id, bytes.NewReader(piece))
	req.Header.Set("Upload-Offset", fmt.Sprint(offset))
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer r.Body.Close()
	var u uploadView
	json.NewDecoder(r.Body).Decode(&u)
	return r.StatusCode, u.Offset, u.Done
}

// An upload in pieces lands in the import folder (in tv/ when it's TV),
// only once it's all there; until then it's hidden in .uploads, where the
// import doesn't look.
func TestUploadInPieces(t *testing.T) {
	ts, dir := uploadServer(t)
	data := bytes.Repeat([]byte("0123456789"), 1000) // 10,000 bytes
	u := ts.startUpload(`{"name": "Severance.S01E01.mkv", "size": 10000, "kind": "tv", "fingerprint": "f1"}`, 201)
	if u.ID == "" || u.Offset != 0 {
		t.Fatalf("start: %+v", u)
	}
	for off := 0; off < len(data); off += 3000 {
		end := min(off+3000, len(data))
		code, got, done := ts.put(u.ID, int64(off), data[off:end])
		if code != 200 || got != int64(end) || done != (end == len(data)) {
			t.Fatalf("piece at %d: %d, offset %d, done %v", off, code, got, done)
		}
		if !done {
			if _, err := os.Stat(filepath.Join(dir, "tv", "Severance.S01E01.mkv")); err == nil {
				t.Fatalf("in the import folder before it's complete")
			}
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "tv", "Severance.S01E01.mkv"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("finished file: %v, %d bytes", err, len(got))
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, ".uploads")); len(ents) != 1 || ents[0].Name() != "done" {
		t.Errorf("left behind in .uploads: %v", ents) // (done: finished ones, remembered)
	}
	var list []uploadView
	ts.json("GET", "/api/uploads", "", &list)
	if len(list) != 0 {
		t.Errorf("still listed: %+v", list)
	}
}

// A connection that drops part way through a piece keeps what arrived;
// sending from the wrong place is refused with where to carry on from; and
// starting the same file again (same fingerprint) picks up where it was.
func TestUploadResumes(t *testing.T) {
	ts, dir := uploadServer(t)
	data := bytes.Repeat([]byte("abcdefgh"), 1000) // 8,000 bytes
	start := `{"name": "Dune (2021).mkv", "size": 8000, "kind": "movie", "fingerprint": "dune-8000-1"}`
	u := ts.startUpload(start, 201)

	// A piece that only half arrives: the client says 4,000 bytes but the
	// connection goes after 2,500.
	req, _ := http.NewRequest("PATCH", ts.hs.URL+"/api/uploads/"+u.ID, io.MultiReader(bytes.NewReader(data[:2500]), errReader{}))
	req.Header.Set("Upload-Offset", "0")
	req.ContentLength = 4000
	if r, err := http.DefaultClient.Do(req); err == nil {
		r.Body.Close()
	}
	// The client gives up before the server has finished with what came.
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var v uploadView
		if ts.json("GET", "/api/uploads/"+u.ID, "", &v); v.Offset == 2500 {
			break
		}
	}

	again := ts.startUpload(start, 200) // the same file, dropped again
	if again.ID != u.ID || again.Offset != 2500 {
		t.Fatalf("resume: %+v (want offset 2500)", again)
	}
	if code, off, _ := ts.put(u.ID, 4000, data[4000:]); code != http.StatusConflict || off != 2500 {
		t.Errorf("wrong offset: %d, says %d", code, off)
	}
	if code, off, done := ts.put(u.ID, 2500, data[2500:]); code != 200 || off != 8000 || !done {
		t.Fatalf("rest: %d %d %v", code, off, done)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "movies", "Dune (2021).mkv")); !bytes.Equal(got, data) {
		t.Errorf("resumed file differs: %d bytes", len(got))
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// Names are cleaned to a plain file name; only video and subtitles; no
// sending more than was promised; and a name already there gets a number.
func TestUploadRules(t *testing.T) {
	ts, dir := uploadServer(t)
	for _, bad := range []string{
		`{"name": "../../etc/passwd.mkv", "size": 10}`, // cleaned, not refused: see below
	} {
		u := ts.startUpload(bad, 201)
		if strings.Contains(u.Name, "/") || strings.Contains(u.Name, "..") {
			t.Errorf("name not cleaned: %q", u.Name)
		}
	}
	for _, bad := range []string{
		`{"name": "notes.exe", "size": 10}`,
		`{"name": ".hidden.mkv", "size": 10}`,
		`{"name": "x.mkv", "size": 0}`,
		`{"name": "x.mkv", "size": -5}`,
		`{"name": "x.mkv", "size": 10, "kind": "music"}`,
		`not json`,
	} {
		r := ts.do("POST", "/api/uploads", bad, nil)
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, r.StatusCode)
		}
	}
	u := ts.startUpload(`{"name": "Movie.srt", "size": 4}`, 201)
	if code, _, _ := ts.put(u.ID, 0, []byte("toolong")); code != http.StatusRequestEntityTooLarge {
		t.Errorf("more than promised: %d", code)
	}
	os.WriteFile(filepath.Join(dir, "Movie.srt"), []byte("old"), 0o644)
	if code, _, done := ts.put(u.ID, 0, []byte("subs")); code != 200 || !done {
		t.Fatalf("finish: %d %v", code, done)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "Movie (2).srt")); string(got) != "subs" {
		t.Errorf("clash: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "Movie.srt")); string(got) != "old" {
		t.Errorf("overwrote: %q", got)
	}
	if code, _, _ := ts.put("nope", 0, []byte("x")); code != http.StatusNotFound {
		t.Errorf("unknown upload: %d", code)
	}
	ts.sv.cfg.ImportDir = ""
	r := ts.do("POST", "/api/uploads", `{"name": "x.mkv", "size": 10}`, nil)
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("no import folder: %d", r.StatusCode)
	}
}

// Unfinished uploads are listed, can be cancelled, and go by themselves
// after a week untouched.
func TestUploadCancelAndSweep(t *testing.T) {
	ts, dir := uploadServer(t)
	a := ts.startUpload(`{"name": "A.mkv", "size": 100, "fingerprint": "a"}`, 201)
	b := ts.startUpload(`{"name": "B.mkv", "size": 100, "fingerprint": "b"}`, 201)
	ts.put(a.ID, 0, make([]byte, 40))

	var list []uploadView
	ts.json("GET", "/api/uploads", "", &list)
	if len(list) != 2 {
		t.Fatalf("list: %+v", list)
	}
	r := ts.do("DELETE", "/api/uploads/"+b.ID, "", nil)
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Errorf("cancel: %d", r.StatusCode)
	}

	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, f := range []string{a.ID + ".part", a.ID + ".json"} {
		os.Chtimes(filepath.Join(dir, ".uploads", f), old, old)
	}
	list = nil
	ts.json("GET", "/api/uploads", "", &list)
	if len(list) != 0 {
		t.Errorf("after cancel, an expired one listed: %+v", list)
	}
	ts.sv.sweepUploads()
	if ents, _ := os.ReadDir(filepath.Join(dir, ".uploads")); len(ents) != 0 {
		t.Errorf("files left after a sweep: %v", ents)
	}
}

// Review M3: listing uploads (the API, /api/status, the Incoming page's
// poll) writes nothing, so it can't delete the .part of an upload that's
// being started, before its record is written. The sweep does the
// clearing up, and not while an upload is starting.
func TestListingUploadsDeletesNothing(t *testing.T) {
	ts, dir := uploadServer(t)
	part := filepath.Join(dir, ".uploads", "0123456789abcdef.part")
	testutil.Write(t, part, "starting")
	var list []uploadView
	ts.json("GET", "/api/uploads", "", &list)
	ts.json("GET", "/api/status", "", &map[string]any{})
	if _, err := os.Stat(part); err != nil {
		t.Fatal("listing uploads deleted a starting upload's data")
	}
	release := ts.sv.ups.lock("") // an upload starting
	swept := make(chan struct{})
	go func() { ts.sv.sweepUploads(); close(swept) }()
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(part); err != nil {
		t.Error("the sweep deleted a starting upload's data")
	}
	release()
	<-swept
	if _, err := os.Stat(part); err == nil {
		t.Error("the sweep left data with no record")
	}
}

// Turned off ([server] uploads = false): none of it.
func TestUploadsOff(t *testing.T) {
	ts, _ := uploadServer(t)
	ts.sv.cfg.Uploads = false
	for _, m := range []string{"GET /api/uploads", "POST /api/uploads", "GET /api/uploads/0123456789abcdef"} {
		method, path, _ := strings.Cut(m, " ")
		r := ts.do(method, path, `{"name": "x.mkv", "size": 10}`, nil)
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound || !strings.Contains(string(b), "turned off") {
			t.Errorf("%s: %d %s", m, r.StatusCode, b)
		}
	}
}

// Preallocated ([server] upload_preallocate = true): the file has its full size
// from the start, so the offset is kept in the record, not read off the
// file; resuming and finishing work the same.
func TestUploadPreallocated(t *testing.T) {
	ts, dir := uploadServer(t)
	ts.sv.cfg.UploadPreallocate = true
	ts.sv.cfg.UploadBuffer = 4 << 10
	data := bytes.Repeat([]byte("preallocated"), 1000) // 12,000 bytes
	u := ts.startUpload(`{"name": "Pre.mkv", "size": 12000, "fingerprint": "pre"}`, 201)
	if st, err := os.Stat(filepath.Join(dir, ".uploads", u.ID+".part")); err != nil || st.Size() != 12000 {
		t.Fatalf("not preallocated: %v %v", st, err)
	}
	if u.Offset != 0 {
		t.Errorf("offset at the start: %d", u.Offset)
	}
	if code, off, _ := ts.put(u.ID, 0, data[:5000]); code != 200 || off != 5000 {
		t.Fatalf("first piece: %d %d", code, off)
	}
	if again := ts.startUpload(`{"name": "Pre.mkv", "size": 12000, "fingerprint": "pre"}`, 200); again.Offset != 5000 {
		t.Errorf("resume: %+v", again)
	}
	if code, off, _ := ts.put(u.ID, 6000, data[6000:]); code != http.StatusConflict || off != 5000 {
		t.Errorf("wrong offset: %d %d", code, off)
	}
	if code, _, done := ts.put(u.ID, 5000, data[5000:]); code != 200 || !done {
		t.Fatalf("rest: %d %v", code, done)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "Pre.mkv")); !bytes.Equal(got, data) {
		t.Errorf("finished file differs: %d bytes", len(got))
	}
}

// A folder (a season with its subtitles, say) goes as a group: each file
// under its path in the folder, held back until the last of them is in,
// then the folder moves into the import folder whole, so an import never
// takes half of it.
func TestUploadFolder(t *testing.T) {
	ts, dir := uploadServer(t)
	const g = "00112233aabbccdd"
	files := map[string]string{
		"Severance S01/Severance.S01E01.mkv":         "episode one",
		"Severance S01/Severance.S01E02.mkv":         "episode two!",
		"Severance S01/Subs/Severance.S01E01.en.srt": "subs",
	}
	start := func(path string, want int) uploadView {
		return ts.startUpload(fmt.Sprintf(`{"name": %q, "size": %d, "kind": "tv", "group": %q, "group_files": 3, "fingerprint": %q}`,
			path, len(files[path]), g, path), want)
	}
	order := []string{"Severance S01/Severance.S01E01.mkv", "Severance S01/Subs/Severance.S01E01.en.srt", "Severance S01/Severance.S01E02.mkv"}
	for i, p := range order {
		u := start(p, 201)
		if u.Name != p {
			t.Errorf("name: %q", u.Name)
		}
		code, _, done := ts.put(u.ID, 0, []byte(files[p]))
		if code != 200 || !done {
			t.Fatalf("%s: %d %v", p, code, done)
		}
		_, err := os.Stat(filepath.Join(dir, "tv", "Severance S01"))
		if last := i == len(order)-1; last != (err == nil) {
			t.Fatalf("after %d of 3: folder in the import folder = %v", i+1, err == nil)
		}
		if i == 0 {
			// The same file again (the tab was closed): already there.
			if again := start(p, 200); !again.Done {
				t.Errorf("finished file not recognised: %+v", again)
			}
			var list []uploadView
			ts.json("GET", "/api/uploads", "", &list)
			if len(list) != 1 || list[0].Group != g || list[0].GroupDone != 1 || list[0].GroupFiles != 3 {
				t.Errorf("folder in the unfinished list: %+v", list)
			}
		}
	}
	for p, want := range files {
		if got, _ := os.ReadFile(filepath.Join(dir, "tv", p)); string(got) != want {
			t.Errorf("%s: %q", p, got)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, ".uploads")); len(ents) != 1 || ents[0].Name() != "done" {
		t.Errorf("left behind: %v", ents)
	}
	if again := start(order[0], 200); !again.Done || !again.Already || again.Path != filepath.Join(dir, "tv", "Severance S01") {
		t.Errorf("the folder again: %+v", again)
	}
	if c := ts.sv.ups.complete(); len(c) != 1 || c[0] != filepath.Join(dir, "tv", "Severance S01") {
		t.Errorf("complete: %v", c)
	}
}

// Paths in a folder are checked part by part; without a group, a path is
// just its file name.
func TestUploadFolderPaths(t *testing.T) {
	ts, _ := uploadServer(t)
	for _, bad := range []string{
		`{"name": "Show/../../x.mkv", "size": 3, "group": "00112233aabbccdd", "group_files": 1}`,
		`{"name": "Show/.hidden/x.mkv", "size": 3, "group": "00112233aabbccdd", "group_files": 1}`,
		`{"name": "x.mkv", "size": 3, "group": "00112233aabbccdd", "group_files": 1}`, // a folder needs a folder
		`{"name": "Show/x.mkv", "size": 3, "group": "nothex", "group_files": 1}`,
		`{"name": "Show/x.mkv", "size": 3, "group": "00112233aabbccdd"}`, // how many?
	} {
		r := ts.do("POST", "/api/uploads", bad, nil)
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, r.StatusCode)
		}
	}
	if u := ts.startUpload(`{"name": "Show/Season 1/x.mkv", "size": 3}`, 201); u.Name != "x.mkv" {
		t.Errorf("no group: %q", u.Name)
	}
}

// Cancelling a folder waits for a file of it that's being put by, and that
// file, finishing after the cancel, leaves nothing behind: before, it made
// the group's folder again, with no record, and no sweep ever cleared it.
func TestUploadFolderCancelWhileFinishing(t *testing.T) {
	ts, dir := uploadServer(t)
	const g = "00112233aabbccdd"
	start := func(path string) uploadView {
		return ts.startUpload(fmt.Sprintf(`{"name": %q, "size": 3, "group": %q, "group_files": 2}`, path, g), 201)
	}
	a := start("Show/A.mkv")
	if code, _, done := ts.put(a.ID, 0, []byte("aaa")); code != 200 || !done {
		t.Fatalf("A: %d %v", code, done)
	}
	b := start("Show/B.mkv")
	b.Offset = 3 // all of it in, as it is when it's finished

	release := ts.sv.ups.lock("group " + g) // B being put by
	cancelled := make(chan int, 1)
	go func() {
		r := ts.do("DELETE", "/api/uploads/"+g, "", nil)
		r.Body.Close()
		cancelled <- r.StatusCode
	}()
	time.Sleep(50 * time.Millisecond)
	if len(cancelled) > 0 {
		t.Error("the cancel didn't wait for a file being put by")
	}
	release()
	if code := <-cancelled; code != http.StatusNoContent {
		t.Fatalf("cancel: %d", code)
	}
	if _, err := ts.sv.finishInGroup(b); err == nil {
		t.Error("a file of a cancelled folder was put by")
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, ".uploads")); len(ents) != 0 {
		t.Errorf("left behind: %v", ents)
	}
}

// A group's folder with no record (from before the fix above) is swept
// away.
func TestSweepOrphanGroupFolder(t *testing.T) {
	ts, dir := uploadServer(t)
	testutil.Write(t, filepath.Join(dir, ".uploads", "00112233aabbccdd.d", "Show", "A.mkv"), "aaa")
	ts.sv.sweepUploads()
	if ents, _ := os.ReadDir(filepath.Join(dir, ".uploads")); len(ents) != 0 {
		t.Errorf("left behind: %v", ents)
	}
}

// A finished upload is imported straight away (the server knows it's
// whole, so no waiting for it to settle), and nothing runs again until
// something changes.
func TestAutoImportUpload(t *testing.T) {
	ts, l := newImportServer(t)
	data := append([]byte("\x1a\x45\xdf\xa3"), bytes.Repeat([]byte("x"), 996)...)
	u := ts.startUpload(`{"name": "Fast.Five.2011.mkv", "size": 1000, "kind": "movie"}`, 201)
	if code, _, done := ts.put(u.ID, 0, data); code != 200 || !done {
		t.Fatalf("upload: %d %v", code, done)
	}
	if !ts.sv.autoImportOnce() {
		t.Fatal("didn't run")
	}
	testutil.MustExist(t, l.Movies, "Fast Five (2011)/Fast Five (2011).mkv")
	if ts.sv.autoImportOnce() {
		t.Error("ran again with nothing new")
	}
}

// Review L6: the per-upload locks don't build up: a lock goes when
// nothing holds it.
func TestUploadMapsDontGrow(t *testing.T) {
	ts, _ := uploadServer(t)
	for i := 0; i < 50; i++ {
		r := ts.do("PATCH", fmt.Sprintf("/api/uploads/%016x", i), "x", map[string]string{"Upload-Offset": "0"})
		r.Body.Close()
	}
	ts.sv.ups.mu.Lock()
	n := len(ts.sv.ups.busy)
	ts.sv.ups.mu.Unlock()
	if n != 0 {
		t.Errorf("%d locks kept for uploads that don't exist", n)
	}
}

// Review L15: a name too long for the drive is refused at the start, not
// after the whole file has been sent.
func TestUploadNameTooLong(t *testing.T) {
	ts, _ := uploadServer(t)
	long := strings.Repeat("a", 250)
	ts.startUpload(`{"name": "`+long+`.mkv", "size": 10}`, 400)
	ts.startUpload(`{"name": "`+long+`/x.mkv", "size": 10, "group": "0123456789abcdef", "group_files": 1}`, 400)
	ts.startUpload(`{"name": "`+long[:200]+`.mkv", "size": 10}`, 201)
}
