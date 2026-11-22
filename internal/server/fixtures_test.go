package server

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The Apple apps (mvd-apple) decode these exact responses in their tests,
// so a change to the API that would break them shows up there. Regenerate
// with mvd-apple's scripts/fixtures.sh (MVD_FIXTURES=dir go test -run
// TestWriteAppleFixtures).
func TestWriteAppleFixtures(t *testing.T) {
	dir := os.Getenv("MVD_FIXTURES")
	if dir == "" {
		t.Skip("set MVD_FIXTURES to a folder to write the Apple apps' test fixtures")
	}
	ts := newTestServer(t, "")
	defer ts.hs.Close()

	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1, e2 := sh.Seasons[0].Episodes[0].ID, sh.Seasons[0].Episodes[1].ID

	// Some watch history, so Up Next has each kind of entry.
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	ts.do("POST", "/api/progress/"+e1, `{"position": 3200, "duration": 3300}`, nil).Body.Close()

	// In this order: the progress report last, so it doesn't change the rest.
	fixtures := []struct{ name, method, path, body string }{
		{"up-next.json", "GET", "/api/up-next", ""},
		{"movies.json", "GET", "/api/movies", ""},
		{"shows.json", "GET", "/api/shows", ""},
		{"movie.json", "GET", "/api/item/m238", ""},
		{"show.json", "GET", "/api/item/s95396", ""},
		{"episode.json", "GET", "/api/item/" + e2, ""},
		{"search.json", "GET", "/api/search?q=s", ""},
		{"progress.json", "POST", "/api/progress/" + e2, `{"position": 120, "duration": 3300}`},
	}

	for _, f := range fixtures {
		name, method, path, body := f.name, f.method, f.path, f.body
		resp := ts.do(method, path, body, nil)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s %s: %d", method, path, resp.StatusCode)
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		pretty, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, name), append(pretty, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
