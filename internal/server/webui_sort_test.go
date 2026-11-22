//go:build webui

package server

import (
	"slices"
	"strings"
	"testing"
)

// The grids sort as the Apple apps do (MVDKit's LibraryOrder): each sort's
// natural direction, ties in title order, and anything with nothing to sort
// on last, either way.
func TestSortGrid(t *testing.T) {
	type item struct {
		name string
		year int
	}
	// In title order, as the index has them.
	items := []item{{"a", 1999}, {"b", 0}, {"c", 2010}, {"d", 1999}, {"e", 0}}
	key := func(it item, s string) (int64, bool) { return int64(it.year), it.year > 0 }
	names := func(o gridOrder) string {
		var out []string
		for _, it := range sortGrid(slices.Clone(items), o, key) {
			out = append(out, it.name)
		}
		return strings.Join(out, "")
	}
	for _, c := range []struct {
		order string
		want  string
	}{
		{"title", "abcde"},
		{"-title", "edcba"},
		{"year", "cadbe"},  // newest first, ties by title, no year last
		{"-year", "adcbe"}, // oldest first, no year still last
	} {
		o, ok := parseOrder(c.order, movieSorts)
		if !ok {
			t.Fatalf("%s: not parsed", c.order)
		}
		if got := names(o); got != c.want {
			t.Errorf("%s: %s, want %s", c.order, got, c.want)
		}
	}

	// Runtime runs shortest first unless reversed; the others the other way.
	for word, desc := range map[string]bool{"title": true, "added": true, "runtime": false, "-runtime": true, "-year": false} {
		if o, _ := parseOrder(word, movieSorts); o.descending() != desc {
			t.Errorf("%s: descending %v", word, o.descending())
		}
	}
	// Each grid takes only its own sorts.
	for _, c := range []struct {
		word  string
		sorts []gridSort
		ok    bool
	}{
		{"runtime", movieSorts, true}, {"runtime", showSorts, false},
		{"-unwatched", showSorts, true}, {"unwatched", movieSorts, false},
		{"", movieSorts, false}, {"-", movieSorts, false}, {"--added", movieSorts, false}, {"Added", movieSorts, false},
	} {
		if _, ok := parseOrder(c.word, c.sorts); ok != c.ok {
			t.Errorf("%q: ok %v", c.word, ok)
		}
	}
	if o, _ := parseOrder("-added", movieSorts); o.String() != "-added" || o.reverse().String() != "added" {
		t.Errorf("round trip: %s, reversed %s", o, o.reverse())
	}
}

// ?sort= orders the grid and is remembered, per grid, in a cookie; the
// select shows the order and the arrow reverses it.
func TestUIGridSort(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	// Play the second movie, so last watched puts it first.
	var movies []movieView
	ts.json("GET", "/api/movies", "", &movies)
	other := movies[1].ID
	ts.do("POST", "/api/progress/"+other, `{"position": 60, "duration": 600}`, nil).Body.Close()

	h := body(t, ts.do("GET", "/movies", "", nil))
	if !inOrder(h, `id="c-l-m238"`, `id="c-l-`+other+`"`) || !strings.Contains(h, `<option value="title" selected>Alphabetically</option>`) ||
		strings.Contains(h, "Unwatched Episodes") || !strings.Contains(h, `href="/movies?sort=-title"`) {
		t.Errorf("title order:\n%s", h)
	}

	r := ts.do("GET", "/movies?sort=watched", "", nil)
	h = body(t, r)
	if !inOrder(h, `id="c-l-`+other+`"`, `id="c-l-m238"`) || !strings.Contains(h, `<option value="watched" selected>Last Watched</option>`) ||
		!strings.Contains(h, `href="/movies?sort=-watched"`) {
		t.Errorf("last watched:\n%s", h)
	}
	var cookie string
	for _, c := range r.Cookies() {
		if c.Name == "mvd_sort_movies" {
			cookie = c.Value
		}
	}
	if cookie != "watched" {
		t.Fatalf("cookie: %q", cookie)
	}

	// The cookie stands in for ?sort=, and doesn't sort the TV grid.
	jar := map[string]string{"Cookie": "mvd_sort_movies=-watched"}
	h = body(t, ts.do("GET", "/movies", "", jar))
	if !inOrder(h, `id="c-l-`+other+`"`, `id="c-l-m238"`) || !strings.Contains(h, `href="/movies?sort=watched"`) {
		t.Errorf("from the cookie, reversed (never watched still last):\n%s", h)
	}
	// Year: 1999 before 1972, reversed 1972 first.
	if h = body(t, ts.do("GET", "/movies?sort=year", "", nil)); !inOrder(h, `id="c-l-`+other+`"`, `id="c-l-m238"`) {
		t.Errorf("by year:\n%s", h)
	}
	if h = body(t, ts.do("GET", "/movies?sort=-year", "", nil)); !inOrder(h, `id="c-l-m238"`, `id="c-l-`+other+`"`) {
		t.Errorf("by year, reversed:\n%s", h)
	}
	h = body(t, ts.do("GET", "/tv", "", jar))
	if !strings.Contains(h, `<option value="title" selected>`) || !strings.Contains(h, "Unwatched Episodes") || strings.Contains(h, ">Runtime<") {
		t.Errorf("tv:\n%s", h)
	}
	// Nonsense is the title order, and isn't remembered.
	r = ts.do("GET", "/movies?sort=nonsense", "", nil)
	if h = body(t, r); !strings.Contains(h, `<option value="title" selected>`) || len(r.Cookies()) != 0 {
		t.Errorf("nonsense: %v\n%s", r.Cookies(), h)
	}
}
