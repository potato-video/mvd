//go:build webui

package server

import (
	"fmt"
	"net/http"
	"strings"
)

// searchData is the Search page: what was typed, and what it found, as
// the grids' cards.
type searchData struct {
	Q        string
	Typed    bool // something to search for: not empty, or all punctuation
	Movies   []card
	Shows    []card
	Episodes []card
}

func (d searchData) Found() bool { return len(d.Movies)+len(d.Shows)+len(d.Episodes) > 0 }

// uiLibrarySearch is /search?q=: the same search as /api/search, called
// directly. The form works without JavaScript; with it, htmx asks again
// after each pause in typing and swaps in just the results.
func (sv *Server) uiLibrarySearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	d := searchData{Q: q, Typed: strings.TrimSpace(q) != ""}
	found := sv.pageShelf(r).search(q)
	for _, m := range found.Movies {
		d.Movies = append(d.Movies, movieCard(m))
	}
	for _, sh := range found.Shows {
		d.Shows = append(d.Shows, showCard(sh))
	}
	for _, e := range found.Episodes {
		d.Episodes = append(d.Episodes, episodeResult(e))
	}
	// A clear redraws the results, so what's left of a title goes too.
	d.Movies = keyed(d.Movies, "sm", "results")
	d.Shows = keyed(d.Shows, "ss", "results")
	d.Episodes = keyed(d.Episodes, "se", "results")
	sv.render(w, r, "search", page{Title: "Search", Nav: "search", Data: d})
}

// episodeResult is an episode a search found: its still, the show's title
// and its code. It opens the show's page at that episode: on its season,
// at the row's anchor.
func episodeResult(e episodeView) card {
	c := episodeCard(e)
	c.Href = fmt.Sprintf("/tv/%s?season=%d#c-ep-%s", e.ShowID, e.Season, e.ID)
	c.PlayHref = ""
	c.Watched = e.Watched
	if e.Watched {
		c.Pill = "watched"
	}
	return c
}
