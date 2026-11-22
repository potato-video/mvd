//go:build files

package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mvd/internal/upload"
)

// flaky drops every third piece on the floor, as a bad Wi-Fi connection
// would: some of it may have arrived, the client hears nothing.
type flaky struct {
	mu sync.Mutex
	n  int
}

func (f *flaky) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "PATCH" {
		f.mu.Lock()
		f.n++
		drop := f.n%3 == 0
		f.mu.Unlock()
		if drop {
			buf := make([]byte, 1000) // part of the piece gets there
			resp, err := http.DefaultTransport.RoundTrip(cutShort(r, buf))
			if err == nil {
				resp.Body.Close()
			}
			return nil, errors.New("connection reset")
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

// cutShort sends only the first len(buf) bytes of r's body, as if the
// connection went.
func cutShort(r *http.Request, buf []byte) *http.Request {
	n, _ := r.Body.Read(buf)
	r2 := r.Clone(r.Context())
	r2.Body = &errAfter{data: buf[:n]}
	return r2
}

type errAfter struct{ data []byte }

func (e *errAfter) Read(p []byte) (int, error) {
	if len(e.data) == 0 {
		return 0, errors.New("gone")
	}
	n := copy(p, e.data)
	e.data = e.data[n:]
	return n, nil
}
func (e *errAfter) Close() error { return nil }

// The upload client (mvd upload) sends a file and a folder over a
// connection that keeps dropping, and they arrive whole; sent again, it
// all counts as in already.
func TestUploadClient(t *testing.T) {
	ts, dir := uploadServer(t)
	src := t.TempDir()
	mk := func(p string, n int) {
		os.MkdirAll(filepath.Dir(filepath.Join(src, p)), 0o777)
		os.WriteFile(filepath.Join(src, p), []byte(strings.Repeat(string(rune('a'+n%26)), n)), 0o644)
	}
	mk("Dune (2021).mkv", 50000)
	mk("Show S01/Show.S01E01.mkv", 30000)
	mk("Show S01/Subs/Show.S01E01.en.srt", 200)
	mk("Show S01/notes.txt", 10) // left behind
	mk("Show S01/.DS_Store", 10) // left behind
	items, err := upload.Plan([]string{filepath.Join(src, "Dune (2021).mkv"), filepath.Join(src, "Show S01")})
	if err != nil || len(items) != 3 {
		t.Fatalf("plan: %+v %v", items, err)
	}
	c := &upload.Client{Base: ts.hs.URL, HTTP: &http.Client{Transport: &flaky{}}, Piece: 7000, Wait: 10 * time.Millisecond}
	var notes []string
	c.Progress = func(p upload.Progress) {
		if p.Note != "" {
			notes = append(notes, p.Note)
		}
	}
	start := time.Now()
	for _, it := range items {
		if err := c.Send(it, "tv"); err != nil {
			t.Fatalf("send %s: %v", it.Name, err)
		}
	}
	if len(notes) == 0 {
		t.Error("never had to retry: the test isn't testing that")
	}
	if time.Since(start) > 30*time.Second {
		t.Errorf("slow: %s", time.Since(start))
	}
	for _, p := range []string{"Dune (2021).mkv", "Show S01/Show.S01E01.mkv", "Show S01/Subs/Show.S01E01.en.srt"} {
		want, _ := os.ReadFile(filepath.Join(src, p))
		got, err := os.ReadFile(filepath.Join(dir, "tv", p))
		if err != nil || string(got) != string(want) {
			t.Errorf("%s: %v, %d of %d bytes", p, err, len(got), len(want))
		}
	}
	// Sent again, it's all in already: nothing goes twice.
	if upload.GroupID(items[1:]) != items[1].Group {
		t.Errorf("group id not stable")
	}
	already := 0
	c.Progress = func(p upload.Progress) {
		if p.Already {
			already++
		}
	}
	for _, it := range items {
		if err := c.Send(it, "tv"); err != nil {
			t.Fatalf("send again %s: %v", it.Name, err)
		}
	}
	if already != 3 {
		t.Errorf("sent again: %d of 3 in already", already)
	}
	if _, err := os.Stat(filepath.Join(dir, "tv", "Show S01 (2)")); err == nil {
		t.Errorf("the folder went twice")
	}
	// A token-protected server, without the token.
	tok := newTestServer(t, "sekrit")
	defer tok.hs.Close()
	tok.sv.cfg.ImportDir, tok.sv.cfg.Uploads = t.TempDir(), true
	if err := (&upload.Client{Base: tok.hs.URL}).Send(items[0], ""); !errors.Is(err, upload.ErrToken) {
		t.Errorf("no token: %v", err)
	}
	if err := (&upload.Client{Base: tok.hs.URL, Token: "sekrit"}).Send(items[0], ""); err != nil {
		t.Errorf("with the token: %v", err)
	}
}

// A viewer's token can't upload, and mvd upload says why.
func TestUploadClientViewer(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	ts.sv.cfg.ImportDir, ts.sv.cfg.Uploads = t.TempDir(), true
	src := filepath.Join(t.TempDir(), "Film (2020).mkv")
	os.WriteFile(src, []byte("video"), 0o644)
	items, err := upload.Plan([]string{src})
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	err = (&upload.Client{Base: ts.hs.URL, Token: denTok}).Send(items[0], "")
	if err == nil || !strings.Contains(err.Error(), "this token can only watch") {
		t.Errorf("viewer: %v", err)
	}
}
