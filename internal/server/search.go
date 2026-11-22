package server

import (
	"net/http"

	"mvd/internal/library"
)

// searchView is what a search found: movies and shows as the lists have
// them, and episodes in brief, each best first.
type searchView struct {
	Movies   []movieView   `json:"movies"`
	Shows    []showView    `json:"shows"`
	Episodes []episodeView `json:"episodes"`
}

// search is the library's search (library.Search does the matching and
// ranking) over what this access sees, as views. The API and the website
// both answer with it.
func (s shelf) search(q string) searchView {
	var allow func(string) bool
	if s.acc.ratings != nil {
		allow = s.acc.allowed
	}
	found := s.ix.Search(q, library.SearchLimit, allow)
	v := searchView{Movies: []movieView{}, Shows: []showView{}, Episodes: []episodeView{}}
	for _, m := range found.Movies {
		v.Movies = append(v.Movies, s.movieView(m, false))
	}
	for _, sh := range found.Shows {
		v.Shows = append(v.Shows, s.showView(sh, false))
	}
	for _, e := range found.Episodes {
		v.Episodes = append(v.Episodes, s.episodeView(e, false))
	}
	return v
}

// librarySearch: ?q=god+fa. Nothing typed is three empty lists, not an error.
func (sv *Server) librarySearch(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, sv.shelf(r).search(r.URL.Query().Get("q")))
}
