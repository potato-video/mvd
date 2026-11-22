package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestSearchAPI(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()

	var got searchView
	ts.json("GET", "/api/search?q=god+fa", "", &got)
	if len(got.Movies) != 1 || got.Movies[0].ID != "m238" || got.Movies[0].Poster == "" || len(got.Shows)+len(got.Episodes) != 0 {
		t.Errorf("god fa: %+v", got)
	}
	// Its collection and its year find the movie too.
	for _, q := range []string{"godfather+collection", "GODFATHER+1972"} {
		ts.json("GET", "/api/search?q="+q, "", &got)
		if len(got.Movies) != 1 || got.Movies[0].ID != "m238" {
			t.Errorf("%s: %+v", q, got.Movies)
		}
	}

	// A show's name finds the show, then its episodes, in brief with their
	// show's id and title, and their watch state.
	ts.do("POST", "/api/progress/e95396101", `{"watched": true}`, nil).Body.Close()
	ts.json("GET", "/api/search?q=sever", "", &got)
	if len(got.Shows) != 1 || got.Shows[0].ID != "s95396" || got.Shows[0].Episodes != 2 || len(got.Episodes) != 3 {
		t.Fatalf("sever: %+v", got)
	}
	// The special's own title has the word, so it's first; the rest are
	// there for their show's, in order.
	if got.Episodes[0].Title != "Inside Severance" {
		t.Errorf("sever: first episode %+v", got.Episodes[0])
	}
	e := got.Episodes[1]
	if e.ID != "e95396101" || e.ShowID != "s95396" || e.ShowTitle != "Severance" || e.Season != 1 || e.Episode != 1 ||
		!e.Watched || e.Stream == "" || e.Plot != "" {
		t.Errorf("sever: second episode %+v", e)
	}

	// Nothing typed, or nothing found, is three empty lists, not null and not an error.
	for _, q := range []string{"", "?q=", "?q=%27", "?q=zzzz"} {
		code, b := ts.body("GET", "/api/search"+q, "", nil)
		if want := `{"movies":[],"shows":[],"episodes":[]}`; code != 200 || strings.TrimSpace(b) != want {
			t.Errorf("/api/search%s: %d %s", q, code, b)
		}
	}
}

// A search finds nothing a token can't see.
func TestSearchHidden(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	for _, q := range []string{"godfather", "sever", "s"} {
		code, b := ts.body("GET", "/api/search?q="+q, "", bearer(kidsTok))
		for _, id := range []string{"m238", "s95396", "e95396"} {
			if code != http.StatusOK || strings.Contains(b, id) {
				t.Errorf("kids: %s: %d, with %s: %s", q, code, id, b)
			}
		}
		if _, b := ts.body("GET", "/api/search?q="+q, "", bearer(adminTok)); !strings.Contains(b, `"id"`) {
			t.Errorf("admin: %s finds nothing, so this proves nothing: %s", q, b)
		}
	}
}
