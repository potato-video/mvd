//go:build webui && files

package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Needs you on the Incoming page (needs.go has the answers): a card's
// search, the dialog that confirms a title, Import, which episode a video
// is, and Try again.

func (sv *Server) uiNeedsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ui/import/search", sv.uiSearch)
	mux.HandleFunc("GET /ui/import/details", sv.uiDetails)
	mux.HandleFunc("POST /ui/import/choose", sv.uiChoose)
	mux.HandleFunc("GET /ui/import/episodes", sv.uiEpisodes)
	mux.HandleFunc("POST /ui/import/episode", sv.uiEpisode)
	mux.HandleFunc("POST /ui/import/retry", sv.uiRetry)
}

// dialogData is the import dialog: a title's details and the item it would
// settle, or why it can't.
type dialogData struct {
	titleDetails
	Item, Sig, Name string
	Error           string
}

// candsData is a card's list of titles: the import's, or a search's.
type candsData struct {
	Item, Sig  string
	Candidates []candidate
	Error      string
}

// uiSearch is a card's search field. Words answer with the card's new list
// of titles; a reference opens the dialog for its title straight away.
func (sv *Server) uiSearch(w http.ResponseWriter, r *http.Request) {
	item, sig := r.FormValue("item"), r.FormValue("sig")
	res, err := sv.search(r.FormValue("kind"), r.FormValue("q"))
	switch {
	case err != nil:
		sv.partial(w, "incoming", "cands", candsData{Item: item, Sig: sig, Error: err.Error()})
	case res.TMDB != 0:
		w.Header().Set("HX-Retarget", "#import-dialog-body")
		w.Header().Set("HX-Reswap", "innerHTML")
		sv.renderDialog(w, item, sig, res.Kind, res.TMDB)
	case len(res.Candidates) == 0:
		sv.partial(w, "incoming", "cands", candsData{Item: item, Sig: sig, Error: "Nothing on TMDB by that name."})
	default:
		sv.partial(w, "incoming", "cands", candsData{Item: item, Sig: sig, Candidates: res.Candidates})
	}
}

func (sv *Server) uiDetails(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.FormValue("tmdb"))
	sv.renderDialog(w, r.FormValue("item"), r.FormValue("sig"), r.FormValue("kind"), id)
}

func (sv *Server) renderDialog(w http.ResponseWriter, item, sig, kind string, id int) {
	d := dialogData{Item: item, Sig: sig}
	if it, ok := sv.importItem(item); ok {
		d.Name = it.Name
	}
	var err error
	d.titleDetails, err = sv.details(item, kind, id)
	if err != nil {
		d.Error = capitalize(err.Error()) + "."
	}
	sv.partial(w, "incoming", "dialog", d)
}

// uiChoose is the dialog's Import: the list comes back, and the dialog
// closes; if the item's gone or changed, the dialog says so instead.
func (sv *Server) uiChoose(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.FormValue("tmdb"))
	name, err := sv.choose(r.FormValue("item"), r.FormValue("sig"), r.FormValue("kind"), id)
	if err != nil {
		w.Header().Set("HX-Retarget", "#import-dialog-err")
		w.Header().Set("HX-Reswap", "innerHTML")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		sv.partial(w, "incoming", "dialogerr", "Nothing was imported: "+err.Error()+".")
		return
	}
	w.Header().Set("HX-Trigger", "import-chosen")
	title := r.FormValue("title")
	if title == "" {
		title = "the title chosen"
	}
	sv.renderIncoming(w, "Importing "+name+" as "+title+".", false)
}

// episodesData is the list a person says which episode a video is from.
type episodesData struct {
	Item, Sig string
	List      episodeList
	Error     string
}

func (sv *Server) uiEpisodes(w http.ResponseWriter, r *http.Request) {
	show, _ := strconv.Atoi(r.FormValue("tmdb"))
	d := episodesData{Item: r.FormValue("item"), Sig: r.FormValue("sig")}
	var err error
	if d.List, err = sv.episodes(d.Item, r.FormValue("video"), show); err != nil {
		d.Error = "Couldn't list the episodes: " + err.Error() + "."
	}
	sv.partial(w, "incoming", "episodes", d)
}

// uiEpisode is the episode list's File it: the list comes back.
func (sv *Server) uiEpisode(w http.ResponseWriter, r *http.Request) {
	show, _ := strconv.Atoi(r.FormValue("tmdb"))
	se, ep, _ := strings.Cut(r.FormValue("ep"), ":")
	season, err1 := strconv.Atoi(se)
	episode, err2 := strconv.Atoi(ep)
	if err1 != nil || err2 != nil {
		sv.renderIncoming(w, "Choose an episode first.", true)
		return
	}
	name, err := sv.chooseEpisode(r.FormValue("item"), r.FormValue("sig"), r.FormValue("video"), show, season, episode)
	if err != nil {
		sv.renderIncoming(w, "Nothing was filed: "+err.Error()+".", true)
		return
	}
	sv.renderIncoming(w, fmt.Sprintf("Filing %s as S%02dE%02d.", name, season, episode), false)
}

func (sv *Server) uiRetry(w http.ResponseWriter, r *http.Request) {
	name, err := sv.retry(r.FormValue("item"))
	if err != nil {
		sv.renderIncoming(w, "Couldn't try it again: "+err.Error(), true)
		return
	}
	sv.renderIncoming(w, "Trying "+name+" again.", false)
}

func init() {
	// cands is a card's titles from the import, for the list a search replaces.
	uiFuncs["cands"] = func(l *leftItem, cs []candidate) candsData {
		return candsData{Item: l.Item, Sig: l.Sig, Candidates: cs}
	}
	// needfile is one of a card's videos, with the item it's in.
	uiFuncs["needfile"] = func(l *leftItem, f leftFile) needFileData {
		return needFileData{Item: l.Item, Sig: l.Sig, File: f}
	}
}

type needFileData struct {
	Item, Sig string
	File      leftFile
}
