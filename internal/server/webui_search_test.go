//go:build webui

package server

import (
	"strings"
	"testing"
)

func TestUISearch(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()

	page := func(path string) string {
		r := ts.do("GET", path, "", nil)
		b := body(t, r)
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d", path, r.StatusCode)
		}
		return b
	}
	has := func(path, b string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(b, w) {
				t.Errorf("%s: missing %q", path, w)
			}
		}
	}

	// Every page's header links to it.
	has("/movies", page("/movies"), `href="/search" aria-label="Search"`)

	// Nothing typed: the field and a hint. A plain form, live with htmx.
	b := page("/search")
	has("/search", b, `<form class="searchbar" action="/search" method="get"`, `name="q" value=""`,
		`hx-trigger="input changed delay:300ms, search"`, "Search your library", `aria-current="page"`)

	// A movie, as the grid's card.
	b = page("/search?q=god+fa")
	has("/search?q=god+fa", b, `value="god fa"`, "<h2>Movies</h2>", `href="/movies/m238"`, "The Godfather")
	if strings.Contains(b, "<h2>TV</h2>") || strings.Contains(b, "<h2>Episodes</h2>") {
		t.Errorf("god fa: sections with nothing in them")
	}

	// A show, then its episodes, each a link to the show's page at that
	// episode: its season, and its row's anchor.
	b = page("/search?q=severance")
	has("/search?q=severance", b, "<h2>TV</h2>", `href="/tv/s95396"`, "<h2>Episodes</h2>",
		`href="/tv/s95396?season=1#c-ep-e95396102"`, "S01E02 Half Loop", `href="/tv/s95396?season=0#c-ep-e95396001"`)
	has("/tv/s95396?season=1", page("/tv/s95396?season=1"), `id="c-ep-e95396102"`)

	// No matches says so, with what was typed, escaped.
	b = page("/search?q=%3Cb%3Ezzz")
	has("/search?q=<b>zzz", b, "No results for “&lt;b&gt;zzz”")

	// Clearing progress on a result redraws the results.
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	b = page("/search?q=godfather")
	has("/search?q=godfather", b, `hx-post="/ui/progress/m238/clear" hx-target="#results"`)
	r := ts.do("POST", "/ui/progress/m238/clear", "", map[string]string{"HX-Current-URL": ts.hs.URL + "/search?q=godfather"})
	if b := body(t, r); r.StatusCode != 200 || !strings.Contains(b, `href="/movies/m238"`) || strings.Contains(b, "Clear progress") {
		t.Errorf("clear: %d, results not redrawn without the progress", r.StatusCode)
	}
}

// The website's search hides what the token can't see, as the API's does.
func TestUISearchHidden(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	code, b := ts.body("GET", "/search?q=godfather", "", bearer(kidsTok))
	if code != 200 || strings.Contains(b, "m238") || !strings.Contains(b, "No results") {
		t.Errorf("kids: %d %s", code, b)
	}
	if _, b := ts.body("GET", "/search?q=godfather", "", bearer(adminTok)); !strings.Contains(b, "m238") {
		t.Errorf("admin: no Godfather")
	}
}
