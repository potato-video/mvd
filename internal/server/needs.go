//go:build files

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"mvd/internal/library"
	"mvd/internal/tmdb"
)

// Needs you: what an import left for a person, and answering it. The
// import never asks here (it has nobody to ask): it leaves the item with
// its candidates, the server keeps them (imports in import.go), and an
// answer writes the item's .mvd hint for the next import, which files it
// under its lock. The page never files anything itself.
//
//	GET  /api/import                 what's left, and why
//	GET  /api/import/search          TMDB, by words or by a reference
//	GET  /api/import/details         a title, and what importing would file
//	GET  /api/import/poster/{kind}/{id}  its poster, through the server
//	POST /api/import/choose          settle an item: {item, sig, kind, tmdb}
//	POST /api/import/retry           try a failed item again
//	POST /api/import/trash           an item to the trash

func (sv *Server) needsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/import", sv.apiLeft)
	mux.HandleFunc("GET /api/import/search", sv.apiSearch)
	mux.HandleFunc("GET /api/import/details", sv.apiDetails)
	mux.HandleFunc("GET /api/import/poster/{kind}/{id}", sv.apiPoster)
	mux.HandleFunc("POST /api/import/choose", sv.apiChoose)
	mux.HandleFunc("POST /api/import/retry", sv.apiRetry)
	mux.HandleFunc("POST /api/import/trash", sv.apiTrash)
}

// leftItem is an item in the import folder waiting for a person.
type leftItem struct {
	Item       string     `json:"item"` // its path in the import folder: what the answers name it by
	Name       string     `json:"name"`
	Size       int64      `json:"size"`
	Sig        string     `json:"sig"`               // the item as it is: an answer names it, so a change since is caught
	Problem    string     `json:"problem,omitempty"` // about the whole item: no-videos, cant-unpack, not-unpackable, missing-volume
	Why        string     `json:"why"`               // what's wrong, in words
	Kind       string     `json:"kind,omitempty"`    // movie or tv; "" when the import couldn't tell
	Answerable bool       `json:"answerable"`        // one question in it: choosing a title settles it
	Failures   int        `json:"failures,omitempty"`
	Units      []leftUnit `json:"units"`
	Unknown    []string   `json:"unknown,omitempty"` // videos it couldn't tell are movies or episodes
	Unfiled    []string   `json:"unfiled,omitempty"` // videos not filed, when some of the item was
}

// leftUnit is one movie or show in it.
type leftUnit struct {
	Label      string      `json:"label"`
	Kind       string      `json:"kind"`
	State      string      `json:"state"` // choose, duplicate, not-filed, failed, filed
	Title      string      `json:"title,omitempty"`
	TMDB       int         `json:"tmdb,omitempty"`
	Episodes   int         `json:"episodes,omitempty"`
	Duplicates int         `json:"duplicates,omitempty"`
	Candidates []candidate `json:"candidates,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// candidate is a title it might be.
type candidate struct {
	TMDB          int    `json:"tmdb"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	Year          int    `json:"year,omitempty"`
	OriginalTitle string `json:"original_title,omitempty"` // when it differs
	Overview      string `json:"overview,omitempty"`
	Score         int    `json:"score,omitempty"` // how well it matched, 0 to 100 (a search's results have none)
	URL           string `json:"url"`             // its page on TMDB
}

func tmdbURL(kind string, id int) string {
	return fmt.Sprintf("https://www.themoviedb.org/%s/%d", kind, id)
}

func candidates(kind string, cs []library.Candidate) []candidate {
	out := make([]candidate, 0, len(cs))
	for _, c := range cs {
		x := candidate{TMDB: c.ID, Kind: kind, Title: c.Title, Year: c.Year(), Overview: c.Overview,
			Score: int(c.Score*100 + 0.5), URL: tmdbURL(kind, c.ID)}
		if c.OriginalTitle != c.Title {
			x.OriginalTitle = c.OriginalTitle
		}
		out = append(out, x)
	}
	return out
}

// leftItemOf describes an item an import left, as it is now.
func (sv *Server) leftItemOf(it scannedItem, r library.ItemResult, fails int) leftItem {
	l := leftItem{Item: sv.importRel(it.Path), Name: it.Name, Size: it.Size, Sig: strconv.FormatUint(it.Sig, 16),
		Problem: string(r.Problem), Kind: it.Kind, Units: []leftUnit{}}
	if fails >= failLimit {
		l.Failures = fails
	}
	questions := 0
	for _, u := range r.Units {
		lu := leftUnit{Label: u.Label, Kind: u.Kind, State: string(u.State), Title: u.Title, TMDB: u.TMDB,
			Episodes: u.Episodes, Duplicates: u.Duplicates, Error: u.Err, Candidates: candidates(u.Kind, u.Candidates)}
		l.Units = append(l.Units, lu)
		if u.State == library.UnitChoose {
			questions++
		}
	}
	for _, v := range r.Unknown {
		l.Unknown = append(l.Unknown, sv.importRel(v))
	}
	for _, v := range r.Unfiled {
		l.Unfiled = append(l.Unfiled, sv.importRel(v))
	}
	// One hint, one answer: it settles a TV item (its episodes become one
	// show) or one movie. So an item whose question is the only thing in
	// it, or that holds only videos the import couldn't place.
	switch {
	case len(r.Units) == 1 && questions == 1 && len(r.Unknown) == 0:
		l.Answerable, l.Kind = true, r.Units[0].Kind
	case len(r.Units) == 0 && len(r.Unknown) > 0 && r.Problem == "":
		l.Answerable = true
	}
	l.Why = whyLeft(r, l, fails)
	return l
}

// whyLeft is what's wrong, in words.
func whyLeft(r library.ItemResult, l leftItem, fails int) string {
	switch {
	case fails >= failLimit:
		return fmt.Sprintf("Failed %d times: %s", fails, failedErr(r))
	case r.Problem == library.CantUnpack:
		return "Couldn't unpack this: " + r.Reason() + "."
	case r.Problem != "":
		return capitalize(r.Reason()) + "."
	case len(r.Units) == 0 && len(r.Unknown) > 0:
		return "Couldn't tell what this is."
	}
	var whys []string
	for _, u := range r.Units {
		switch u.State {
		case library.UnitChoose:
			if len(u.Candidates) == 0 {
				whys = append(whys, "Couldn't find "+quoteIf(u.Label, len(r.Units) > 1)+"on TMDB.")
			} else {
				whys = append(whys, "Couldn't tell which "+quoteIf(u.Label, len(r.Units) > 1)+"this is.")
			}
		case library.UnitDuplicate:
			if u.Kind == "movie" {
				whys = append(whys, u.Title+" is already in the library.")
			}
		case library.UnitNotFiled:
			whys = append(whys, "Matched "+u.Title+", but couldn't file any of it.")
		}
		if u.Kind == "tv" && u.Duplicates > 0 {
			whys = append(whys, fmt.Sprintf("%d of %s's episodes are already in the library.", u.Duplicates, u.Title))
		}
	}
	if len(r.Unfiled) > 0 {
		whys = append(whys, fmt.Sprintf("%d of its videos weren't filed.", len(r.Unfiled)))
	}
	if len(r.Unknown) > 0 {
		whys = append(whys, fmt.Sprintf("Couldn't tell what %d of its videos are.", len(r.Unknown)))
	}
	if len(whys) == 0 {
		return "Left for you to sort out."
	}
	return strings.Join(whys, " ")
}

func quoteIf(s string, yes bool) string {
	if !yes {
		return ""
	}
	return s + " "
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// leftItems is everything the last import left for a person that's still
// there.
func (sv *Server) leftItems() []leftItem {
	out := []leftItem{}
	if sv.cfg.ImportDir == "" {
		return out
	}
	results, _ := sv.lastImport()
	sc := scanImport(sv.cfg.ImportDir)
	for _, r := range results {
		it := sc.item(r.Path)
		if it == nil {
			continue
		}
		fails := sv.failCount(r.Path)
		if needsPerson(r, fails) {
			out = append(out, sv.leftItemOf(*it, r, fails))
		}
	}
	return out
}

// needsPerson: an import left it, or it's failed too often to try again.
func needsPerson(r library.ItemResult, fails int) bool {
	if hasFailed(r) {
		return fails >= failLimit
	}
	return r.State == library.ItemLeft
}

// failedErr is why an item's last import failed.
func failedErr(r library.ItemResult) string {
	for _, u := range r.Units {
		if u.State == library.UnitFailed {
			return u.Err
		}
	}
	return ""
}

// importRel is an item's path in the import folder, as the page names it.
func (sv *Server) importRel(p string) string {
	rel, err := filepath.Rel(sv.cfg.ImportDir, p)
	if err != nil {
		return filepath.Base(p)
	}
	return filepath.ToSlash(rel)
}

// importItem finds an item in the import folder by the path the page
// names it by, as an import would see it.
func (sv *Server) importItem(rel string) (scannedItem, bool) {
	if sv.cfg.ImportDir == "" || rel == "" {
		return scannedItem{}, false
	}
	p := filepath.Join(sv.cfg.ImportDir, filepath.FromSlash(rel))
	if !library.Within(sv.cfg.ImportDir, p) || p == filepath.Clean(sv.cfg.ImportDir) {
		return scannedItem{}, false
	}
	it := scanImport(sv.cfg.ImportDir).item(p)
	if it == nil {
		return scannedItem{}, false
	}
	return *it, true
}

// --- answers ---

var errGone = errors.New("it's gone from the import folder: imported elsewhere, or moved")

// errStale is an answer to an item that's changed since it was asked about.
var errStale = errors.New("it's changed since this was shown: look again")

// current finds an item as it was when it was asked about.
func (sv *Server) current(rel, sig string) (scannedItem, error) {
	it, ok := sv.importItem(rel)
	switch {
	case !ok:
		return it, errGone
	case sig != "" && sig != strconv.FormatUint(it.Sig, 16):
		return it, errStale
	}
	return it, nil
}

func (sv *Server) tmdbClient() *tmdb.Client {
	if sv.newTMDB != nil {
		return sv.newTMDB(sv.cfg)
	}
	return tmdb.New(sv.cfg)
}

// searchResult is a search's answer: candidates, or the one title a
// reference names.
type searchResult struct {
	Kind       string      `json:"kind,omitempty"`
	Candidates []candidate `json:"candidates,omitempty"`
	TMDB       int         `json:"tmdb,omitempty"` // a reference's title
}

// search is the card's one field, as mvd import's [e] and [i] together:
// words search TMDB ("Title" or "Title (Year)"), which needs a kind; a TMDB
// or IMDb id or address names one title (a bare TMDB id needs a kind too).
func (sv *Server) search(kind, q string) (searchResult, error) {
	q = strings.TrimSpace(q)
	if kind != "movie" && kind != "tv" {
		kind = ""
	}
	if q == "" {
		return searchResult{}, errors.New("type a title, or a TMDB or IMDb id or address")
	}
	if sv.cfg.APIKey == "" {
		return searchResult{}, errors.New("no TMDB key: set api_key under [tmdb] in config.toml")
	}
	tm := sv.tmdbClient()
	if library.IsReference(q) {
		id, k, err := library.Lookup(tm, kind, q)
		if err != nil {
			return searchResult{}, err
		}
		return searchResult{Kind: k, TMDB: id}, nil
	}
	if kind == "" {
		return searchResult{}, errors.New("say whether it's a movie or TV first")
	}
	cs, err := library.Search(tm, kind, q)
	if err != nil {
		return searchResult{}, err
	}
	out := candidates(kind, cs[:min(len(cs), 8)])
	for i := range out {
		out[i].Score = 0 // a search's order says enough
	}
	return searchResult{Kind: kind, Candidates: out}, nil
}

// titleDetails is what the dialog shows before an import: the title, as
// TMDB has it, and what importing the item as it would file.
type titleDetails struct {
	TMDB          int      `json:"tmdb"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title"`
	Year          int      `json:"year,omitempty"`
	OriginalTitle string   `json:"original_title,omitempty"`
	Overview      string   `json:"overview,omitempty"`
	Facts         []string `json:"facts"`            // a movie's length and director, a show's seasons and network
	Poster        string   `json:"poster,omitempty"` // through the server: /api/import/poster/...
	URL           string   `json:"url"`
	Into          string   `json:"into"` // "Into Movies", "8 episodes into TV, S01E01 to S01E08"
}

// details looks a title up (the import that follows fetches the same, so
// it's cached for it) and reads what the item would file, without TMDB.
func (sv *Server) details(rel, kind string, id int) (titleDetails, error) {
	it, err := sv.current(rel, "")
	if err != nil {
		return titleDetails{}, err
	}
	if kind != "movie" && kind != "tv" || id <= 0 {
		return titleDetails{}, errors.New("which title? (a kind and a TMDB id)")
	}
	tm := sv.tmdbClient()
	d := titleDetails{TMDB: id, Kind: kind, URL: tmdbURL(kind, id), Facts: []string{}}
	poster := ""
	if kind == "movie" {
		m, err := tm.Movie(id)
		if err != nil {
			return d, err
		}
		d.Title, d.Year, d.Overview, poster = m.Title, tmdb.YearOf(m.ReleaseDate), m.Overview, m.PosterPath
		if m.OriginalTitle != m.Title {
			d.OriginalTitle = m.OriginalTitle
		}
		if m.Runtime > 0 {
			d.Facts = append(d.Facts, minutes(m.Runtime))
		}
		var directors []string
		for _, c := range m.Credits.Crew {
			if c.Job == "Director" {
				directors = append(directors, c.Name)
			}
		}
		if len(directors) > 0 {
			d.Facts = append(d.Facts, "Directed by "+strings.Join(directors, " and "))
		}
	} else {
		sh, err := tm.TV(id)
		if err != nil {
			return d, err
		}
		d.Title, d.Year, d.Overview, poster = sh.Name, tmdb.YearOf(sh.FirstAirDate), sh.Overview, sh.PosterPath
		if sh.OriginalName != sh.Name {
			d.OriginalTitle = sh.OriginalName
		}
		seasons := 0
		for _, s := range sh.Seasons {
			if s.SeasonNumber > 0 {
				seasons++
			}
		}
		switch seasons {
		case 0:
		case 1:
			d.Facts = append(d.Facts, "1 season")
		default:
			d.Facts = append(d.Facts, fmt.Sprintf("%d seasons", seasons))
		}
		var nets []string
		for _, n := range sh.Networks {
			nets = append(nets, n.Name)
		}
		if len(nets) > 0 {
			d.Facts = append(d.Facts, strings.Join(nets, ", "))
		}
	}
	if poster != "" {
		d.Poster = fmt.Sprintf("/api/import/poster/%s/%d", kind, id)
	}
	d.Into = into(it.Path, kind)
	return d, nil
}

// into says what importing an item as kind would file.
func into(path, kind string) string {
	sum, err := library.PlanItem(path, kind)
	if err != nil {
		return ""
	}
	switch {
	case kind == "tv" && sum.Episodes == 1 && sum.First != "":
		return "1 episode into TV, " + sum.First
	case kind == "tv" && sum.Episodes > 1 && sum.First != "":
		return fmt.Sprintf("%d episodes into TV, %s to %s", sum.Episodes, sum.First, sum.Last)
	case kind == "tv":
		return "Into TV"
	case sum.Movies > 1:
		return fmt.Sprintf("%d movies into Movies", sum.Movies)
	}
	return "Into Movies"
}

// minutes is a length: "1h 57m".
func minutes(m int) string {
	switch {
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}

// choose settles an item: its .mvd hint, for the next import, which is
// started now. Only if the item is as it was shown (sig), and no import is
// running, here or at a terminal: whichever answers first wins.
func (sv *Server) choose(rel, sig, kind string, id int) (string, error) {
	name, err := sv.writeAnswer(rel, sig, kind, id)
	if err == nil {
		sv.importSoon() // once the locks are let go, or it would find them taken
	}
	return name, err
}

func (sv *Server) writeAnswer(rel, sig, kind string, id int) (string, error) {
	if kind != "movie" && kind != "tv" || id <= 0 {
		return "", errors.New("which title? (a kind and a TMDB id)")
	}
	release, err := sv.lockImports()
	if err != nil {
		return "", err
	}
	defer release()
	it, err := sv.current(rel, sig)
	if err != nil {
		return "", err
	}
	if err := library.WriteHint(it.Path, kind, id); err != nil {
		return "", err
	}
	return it.Name, nil
}

// importSoon has the next import happen now: auto-import's, if it's on
// (a hint written changes the folder), or one of its own if not.
func (sv *Server) importSoon() {
	if sv.cfg.AutoImport && sv.auto.kick != nil {
		sv.auto.mu.Lock()
		sv.auto.retryAt = sv.clock() // even if the folder looks the same
		sv.auto.mu.Unlock()
		sv.kickAutoImport()
		return
	}
	go sv.runImport()
}

// retry tries a failed item again: its count starts over, and an import runs.
func (sv *Server) retry(rel string) (string, error) {
	it, ok := sv.importItem(rel)
	if !ok {
		return "", errGone
	}
	sv.imports.mu.Lock()
	delete(sv.imports.fails, it.Path)
	sv.imports.mu.Unlock()
	sv.importSoon()
	return it.Name, nil
}

// trashItem moves an item in the import folder to the trash: not while an
// import is running, which might be filing it.
func (sv *Server) trashItem(rel string) (name string, deleted bool, err error) {
	it, ok := sv.importItem(rel)
	if !ok {
		return "", false, errGone
	}
	release, err := sv.lockImports()
	if err != nil {
		return "", false, err
	}
	grew, deleted, err := library.TrashItem(sv.cfg, it.Path)
	release()
	if err != nil {
		return "", false, err
	}
	sv.imports.trashed(grew, true)
	sv.kickAutoImport() // the folder's changed: what the page says about it should too
	return it.Name, deleted, nil
}

// trashedText is what the website says once an item's gone: moved, or
// deleted when there's no trash to move it to.
func trashedText(name string, deleted bool) string {
	if deleted {
		return "Deleted " + name + "."
	}
	return "Moved " + name + " to the trash."
}

// --- the API ---

func apiError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// answerCode is the status for an answer that didn't take.
func answerCode(err error) int {
	switch err {
	case errGone:
		return http.StatusNotFound
	case errStale, errBusy:
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func (sv *Server) apiLeft(w http.ResponseWriter, r *http.Request) {
	_, stopped := sv.lastImport()
	writeJSON(w, map[string]any{"items": sv.leftItems(), "stopped": stopped})
}

func (sv *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	res, err := sv.search(r.FormValue("kind"), r.FormValue("q"))
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, res)
}

func (sv *Server) apiDetails(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.FormValue("tmdb"))
	d, err := sv.details(r.FormValue("item"), r.FormValue("kind"), id)
	if err != nil {
		apiError(w, answerCode(err), err)
		return
	}
	writeJSON(w, d)
}

// apiPoster streams a title's poster from TMDB at a small size, through
// the server (so TMDB sees the server, not the visitor), keeping nothing.
func (sv *Server) apiPoster(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	kind := r.PathValue("kind")
	tm := sv.tmdbClient()
	path := ""
	switch kind {
	case "movie":
		if m, err := tm.Movie(id); err == nil {
			path = m.PosterPath
		}
	case "tv":
		if sh, err := tm.TV(id); err == nil {
			path = sh.PosterPath
		}
	}
	body, ctype, err := tm.OpenImage(path, "w185")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer body.Close()
	if ctype == "" {
		ctype = "image/jpeg"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "max-age=86400")
	io.Copy(w, body)
}

// answer reads an answer's JSON body.
func answer(r *http.Request) (struct {
	Item, Sig, Kind string
	TMDB            int
}, error) {
	var b struct {
		Item, Sig, Kind string
		TMDB            int
	}
	err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<10)).Decode(&b)
	if err != nil {
		err = errors.New("bad JSON")
	}
	return b, err
}

func (sv *Server) apiChoose(w http.ResponseWriter, r *http.Request) {
	b, err := answer(r)
	if err == nil {
		_, err = sv.choose(b.Item, b.Sig, b.Kind, b.TMDB)
	}
	if err != nil {
		apiError(w, answerCode(err), err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (sv *Server) apiRetry(w http.ResponseWriter, r *http.Request) {
	b, err := answer(r)
	if err == nil {
		_, err = sv.retry(b.Item)
	}
	if err != nil {
		apiError(w, answerCode(err), err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (sv *Server) apiTrash(w http.ResponseWriter, r *http.Request) {
	b, err := answer(r)
	if err == nil {
		_, _, err = sv.trashItem(b.Item)
	}
	if err != nil {
		apiError(w, answerCode(err), err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
