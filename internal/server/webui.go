//go:build webui

package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"mvd/internal/lang"
	"mvd/internal/library"
	"mvd/internal/tmdb"
)

// The web UI: server-rendered pages made lively with htmx, served from the
// same port as the API so the player can stream and report progress
// without any cross-origin fuss. Everything is embedded in the binary.

//go:embed ui
var uiFiles embed.FS

var assetVersion = fmt.Sprint(time.Now().Unix()) // busts browser caches per start

type page struct {
	Title     string
	Nav       string
	BodyClass string
	Bare      bool
	Data      any
	Downloads bool   // show the Downloads link
	Needs     int    // how many things need a person: a count beside Incoming
	Incoming  bool   // the nav has Incoming (the files feature, and the admin's token)
	SignedIn  string // the token's name, for the footer; "" with no token
	Pair      bool   // the footer links to Pair (signed in with a token)
}

// card is anything shown as a poster or a landscape tile.
type card struct {
	Href    string
	Img     string
	Title   string
	Sub     string
	Pct     int
	Watched bool
	Pill    string // "new", "3 new", "watched": the corner of the poster
	PillNew bool   // red, not cornflower

	ID       string  // the movie or episode, for clearing its progress
	Pos, Dur float64 // seconds: how far in, of how long
	PlayHref string  // the picture plays this (the home page); "" for Href
	Key      string  // unique on the page: the card is element c-Key
	Target   string  // what a clear or remove re-renders: c-Key, or a whole section
}

// bandData drives the "5m of 1h 32m · Clear progress" strip along the bottom
// of a picture in progress.
type bandData struct {
	ID, Title string
	Pos, Dur  float64
	Target    string // what clearing re-renders
	OOB       string // and another element with it, if any
}

// toggleData is a watched toggle. Marking something watched forgets how
// far in it was, so the toggle re-renders Target (and OOB) from the page,
// not just itself: the Resume button and the progress line change too.
type toggleData struct {
	ID          string
	Watched     bool
	Small       bool
	Target, OOB string
}

// heroData is what's over the backdrop on Up Next's feature and the movie
// and show pages, as the Apple TV app's HeroView: the poster (not on Up Next),
// Up Next's pill, the title with its certificate after it, an episode's
// line, the facts, the plot, then how far in, the buttons and the link.
type heroData struct {
	Poster        string
	WithPoster    bool   // a poster column, with a stand-in if there's no Poster
	Pills         []pill // Up Next's
	Title, Href   string // Href: the title opens its page (Up Next)
	Rating        string
	Line          string // an episode's code and name, under the show's title
	Facts, Genres []string
	Plot          string
	Play          *playData // nil for no buttons: a show with nothing next
}

// playData is the foot of a hero: how far in (click to clear), the gold
// Play or Resume, at most one other button, and the .m3u's address.
type playData struct {
	ID, Title   string // what plays
	Href, Label string // "/watch/…"; "Play", "Resume", "Resume S01E04"
	Pct         int
	Left        float64 // seconds
	Toggle      *toggleData
	Remove      bool   // Remove from Up Next
	PlayURL     string // the .m3u
	Target, OOB string // what clearing progress (or removing) re-renders
}

// playFor is the play block for a movie or episode; code is an episode's
// "S01E04", for a show's page.
func (sv *Server) playFor(r *http.Request, id, title string, pos, dur float64, runtime int, code, target, oob string) *playData {
	label := "Play"
	if pos > 0 {
		label = "Resume"
	}
	if code != "" {
		label += " " + code
	}
	return &playData{ID: id, Title: title, Href: "/watch/" + id, Label: label, Pct: pct(pos, dur),
		Left: secondsLeft(pos, dur, runtime), PlayURL: sv.playLink(r, id), Target: target, OOB: oob}
}

var langNames = map[string]string{
	"en": "English", "eng": "English", "fr": "French", "fre": "French", "fra": "French",
	"de": "German", "ger": "German", "deu": "German", "es": "Spanish", "spa": "Spanish",
	"it": "Italian", "ita": "Italian", "nl": "Dutch", "pt": "Portuguese", "ja": "Japanese",
	"jpn": "Japanese", "zh": "Chinese", "ko": "Korean", "sv": "Swedish", "da": "Danish",
	"no": "Norwegian", "fi": "Finnish", "pl": "Polish", "ru": "Russian", "ar": "Arabic",
}

func langName(code string) string {
	if n := langNames[strings.ToLower(code)]; n != "" {
		return n
	}
	if code == "" {
		return "Unknown"
	}
	return code
}

var uiFuncs = template.FuncMap{
	"ver":        func() string { return assetVersion },
	"mvdver":     func() string { return Version },
	"tmdbnotice": func() string { return tmdb.Notice },
	"join":       strings.Join,
	"inc":        func(i int) int { return i + 1 },
	"pct":        pct,
	"timeleft":   timeLeft,
	"date": func(d string) string {
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			return d
		}
		return t.Format("2 Jan 2006")
	},
	"epcode": epcode,
	"toggle": func(id string, watched, small bool, target, oob string) toggleData {
		return toggleData{id, watched, small, target, oob}
	},
	"pctf": func(f float64) int { return max(0, min(100, int(f*100))) },
	"band": func(id, title string, pos, dur float64, target, oob string) bandData {
		return bandData{id, title, pos, dur, target, oob}
	},
	"short": short,
	"size":  sizeStr,
	"ago":   ago,
	"faq":   func() string { return faqURL },
	"float": func(n int64) float64 { return float64(n) },
	"sub":   func(a, b float64) float64 { return max(a-b, 0) },
}

// faqURL is where the web UI sends people with a problem it can't fix.
const faqURL = "https://github.com/potato-video/mvd/blob/main/docs/FAQ.md"

// short is a length, everywhere the UI gives one: "5m", "1h", "1h 57m".
func short(sec float64) string {
	m := int(sec / 60)
	switch {
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}

func pct(pos, dur float64) int {
	if dur <= 0 || pos <= 0 {
		return 0
	}
	return max(1, min(100, int(pos/dur*100)))
}

func epcode(season, ep, last int) string {
	c := fmt.Sprintf("S%02dE%02d", season, ep)
	if last > 0 {
		c += fmt.Sprintf("–E%02d", last)
	}
	return c
}

type uiTemplates map[string]*template.Template

// pageTemplates are the pages, each parsed with the base and the partials.
// (The files feature adds Incoming.)
var pageTemplates = []string{"home", "list", "movie", "show", "search", "watch", "login", "pair"}

func loadTemplates() uiTemplates {
	t := uiTemplates{}
	for _, name := range pageTemplates {
		t[name] = template.Must(template.New("").Funcs(uiFuncs).ParseFS(uiFiles,
			"ui/templates/base.html", "ui/templates/partials.html", "ui/templates/"+name+".html"))
	}
	return t
}

func (sv *Server) render(w http.ResponseWriter, r *http.Request, name string, p page) {
	acc := sv.who(r)
	p.SignedIn = acc.name
	p.Pair = acc.kid != ""
	if acc.admin {
		p.Downloads = sv.hasDownloads()
		p.Incoming = incomingBuilt
		if p.Needs == 0 {
			p.Needs = sv.needsCount()
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := sv.tpl[name].ExecuteTemplate(w, "base", p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (sv *Server) uiRoutes(mux *http.ServeMux) {
	sv.tpl = loadTemplates()
	static, _ := fs.Sub(uiFiles, "ui/static")
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".woff2"):
			w.Header().Set("Content-Type", "font/woff2")
		case strings.HasSuffix(r.URL.Path, ".svg"):
			w.Header().Set("Content-Type", "image/svg+xml")
		}
		w.Header().Set("Cache-Control", "max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		b, _ := uiFiles.ReadFile("ui/static/favicon.ico")
		w.Header().Set("Content-Type", "image/x-icon")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(b)
	})
	mux.HandleFunc("GET /{$}", sv.uiHome)
	mux.HandleFunc("GET /movies", sv.uiMovies)
	mux.HandleFunc("GET /movies/{id}", sv.uiMovie)
	mux.HandleFunc("GET /tv", sv.uiShows)
	mux.HandleFunc("GET /tv/{id}", sv.uiShow)
	mux.HandleFunc("GET /search", sv.uiLibrarySearch)
	mux.HandleFunc("GET /watch/{id}", sv.uiWatch)
	mux.HandleFunc("GET /play/{file}", sv.uiM3U)
	mux.HandleFunc("POST /ui/watched/{id}", sv.uiWatched)
	mux.HandleFunc("POST /ui/progress/{id}/clear", sv.uiClearProgress)
	mux.HandleFunc("POST /ui/up-next/{id}/remove", sv.uiRemoveFromUpNext)
	sv.uiIncomingRoutes(mux)
	mux.HandleFunc("GET /login", sv.uiLogin)
	mux.HandleFunc("POST /login", sv.uiLogin)
	mux.HandleFunc("POST /logout", sv.uiLogout)
	sv.uiPairRoutes(mux)
}

// --- cards ---

// movieCard: the year and the length, or what's left of it; "new" or
// "watched" in the corner, as on the Apple TV.
func movieCard(m movieView) card {
	c := card{Href: "/movies/" + m.ID, Img: m.Poster, Title: m.Title,
		Sub: strings.Join(nonEmpty(yearStr(m.Year), timeLeft(m.Position, m.Duration, m.Runtime)), " · "),
		Pct: pct(m.Position, m.Duration), Watched: m.Watched, ID: m.ID, Pos: m.Position, Dur: m.Duration}
	switch {
	case m.New:
		c.Pill, c.PillNew = "new", true
	case m.Watched:
		c.Pill = "watched"
	}
	return c
}

// keyed gives each card an id on the page, and says what a clear on it
// re-renders: the card itself, or (when target is set) a whole section.
func keyed(cs []card, prefix, target string) []card {
	for i := range cs {
		cs[i].Key = prefix + "-" + cs[i].ID
		cs[i].Target = target
		if target == "" {
			cs[i].Target = "c-" + cs[i].Key
		}
	}
	return cs
}

func episodeCard(e episodeView) card {
	title := e.Title
	if title == "" {
		title = fmt.Sprintf("Episode %d", e.Episode)
	}
	// The picture plays it; the name goes to the show, at its season.
	return card{Href: fmt.Sprintf("/tv/%s?season=%d", e.ShowID, e.Season), PlayHref: "/watch/" + e.ID,
		Img: e.Thumb, Title: e.ShowTitle, Sub: epcode(e.Season, e.Episode, e.LastEpisode) + " " + title,
		Pct: pct(e.Position, e.Duration), ID: e.ID, Pos: e.Position, Dur: e.Duration}
}

// showCard: how much is left to watch; "3 new" or "all watched" in the
// corner, as on the Apple TV.
func showCard(v showView) card {
	c := card{Href: "/tv/" + v.ID, Img: v.Poster, Title: v.Title}
	switch {
	case v.Episodes == 0:
	case v.Unwatched == 0:
		c.Watched, c.Sub = true, "All watched"
	case v.Unwatched == v.Episodes:
		c.Sub = fmt.Sprintf("%d episodes", v.Episodes)
	default:
		c.Sub = fmt.Sprintf("%d of %d to watch", v.Unwatched, v.Episodes)
	}
	switch {
	case v.NewCount > 0:
		c.Pill, c.PillNew = fmt.Sprintf("%d new", v.NewCount), true
	case c.Watched:
		c.Pill = "all watched"
	}
	c.ID = v.ID
	return c
}

func yearStr(y int) string {
	if y == 0 {
		return ""
	}
	return fmt.Sprint(y)
}

// --- pages ---

// homeItem is one Up Next entry on the home page: a row in the list, and
// the feature beside it (Hero) when it's selected.
type homeItem struct {
	Key, ID         string
	Pills           []pill
	Title           string // the movie or show
	Sub             string // the row's second line
	Thumb, Backdrop string
	PlayHref        string
	Pct             int
	Pos, Dur        float64
	Hero            heroData
}

// pill is one of Up Next's: its reason gives it its color.
type pill struct{ Reason, Label string }

var upNextLabels = map[string]string{"continue": "Continue watching", "next": "Next episode", "new": "New"}

// upNextPills, as the Apple apps': the reason's, then for a show you're
// watching, a red one too if new episodes wait. A show's new pill says
// how many ("3 new").
func upNextPills(it upNextItem) []pill {
	np := pill{"new", upNextLabels["new"]}
	if _, ok := it.View.(episodeView); ok {
		np.Label = fmt.Sprintf("%d new", it.NewCount)
	}
	switch {
	case it.Reason == "new":
		return []pill{np}
	case it.NewCount > 0:
		return []pill{{it.Reason, upNextLabels[it.Reason]}, np}
	}
	return []pill{{it.Reason, upNextLabels[it.Reason]}}
}

// length is how long a movie or episode is: the NFO's runtime (minutes),
// else the length played before; 0 if neither is known. As the Apple apps
// have it.
func length(dur float64, runtime int) float64 {
	if runtime > 0 {
		return float64(runtime) * 60
	}
	return max(dur, 0)
}

// secondsLeft is what's left of its length, by the share already watched,
// so it agrees with the length shown.
func secondsLeft(pos, dur float64, runtime int) float64 {
	switch {
	case pos <= 0:
		return length(dur, runtime)
	case dur <= pos:
		return 0
	}
	return length(dur, runtime) * (1 - pos/dur)
}

// timeLeft: "13m left" part-way through, else how long it is ("45m"):
// never both.
func timeLeft(pos, dur float64, runtime int) string {
	switch {
	case length(dur, runtime) <= 0:
		return ""
	case pos > 0 && dur > pos:
		return short(secondsLeft(pos, dur, runtime)) + " left"
	}
	return short(length(dur, runtime))
}

func (sv *Server) homeItems(r *http.Request, s shelf, up []upNextItem) []homeItem {
	out := make([]homeItem, 0, len(up))
	viewer := viewerLang(r)
	for _, it := range up {
		h := homeItem{Pills: upNextPills(it)}
		hero := heroData{Pills: h.Pills}
		var rt int // the runtime, for what's left
		art := upNextPictures(it.View)
		h.Backdrop, h.Thumb = art.Backdrop, art.RowThumb
		switch v := it.View.(type) {
		case movieView:
			h.ID, h.Title = v.ID, v.Title
			h.Pos, h.Dur, rt = v.Position, v.Duration, v.Runtime
			left := timeLeft(v.Position, v.Duration, v.Runtime)
			h.Sub = strings.Join(nonEmpty(yearStr(v.Year), left), " · ")
			hero.Href, hero.Plot, hero.Rating, hero.Genres = "/movies/"+v.ID, v.Plot, v.Rating, v.Genres
			hero.Facts = movieFacts(v, viewer)
		case episodeView:
			title := v.Title
			if title == "" {
				title = fmt.Sprintf("Episode %d", v.Episode)
			}
			code := epcode(v.Season, v.Episode, v.LastEpisode)
			left := timeLeft(v.Position, v.Duration, v.Runtime)
			h.ID, h.Title = v.ID, v.ShowTitle
			h.Pos, h.Dur, rt = v.Position, v.Duration, v.Runtime
			h.Sub = strings.Join(nonEmpty(code, title, left), " · ")
			hero.Href = fmt.Sprintf("/tv/%s?season=%d", v.ShowID, v.Season)
			// The episode's own synopsis: the show's would be the same for every episode.
			hero.Plot = v.Plot
			if sh := s.Show(v.ShowID); sh != nil {
				hero.Rating, hero.Genres = sh.Rating, sh.Genres
			}
			hero.Line = code + " · " + title
			// How long; the progress line says what's left.
			hero.Facts = nonEmpty(langFact(v.OriginalLanguage, viewer), timeLeft(0, v.Duration, v.Runtime))
			if it.NewCount > 1 {
				hero.Facts = append(hero.Facts, fmt.Sprintf("%d new episodes", it.NewCount))
			}
		}
		h.Key = "up-" + h.ID
		h.PlayHref = "/watch/" + h.ID
		h.Pct = pct(h.Pos, h.Dur)
		hero.Title = h.Title
		hero.Play = sv.playFor(r, h.ID, h.Title, h.Pos, h.Dur, rt, "", "home", "")
		hero.Play.Remove = true
		h.Hero = hero
		out = append(out, h)
	}
	return out
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// uiHome: Up Next (upnext.go), as a list with the selected entry featured
// beside it. A change on the page (a clear, a removal) re-renders the lot,
// since it can move things up or down the list.
func (sv *Server) uiHome(w http.ResponseWriter, r *http.Request) {
	s := sv.pageShelf(r)
	sv.render(w, r, "home", page{Title: "mvd", Nav: "home", Data: map[string]any{
		"Items": sv.homeItems(r, s, s.upNext()),
		"Empty": len(s.Movies()) == 0 && len(s.Shows()) == 0,
	}})
}

// listData is a library page: the posters, as the Apple TV's grids, with
// no heading or search (the nav says where you are).
type listData struct {
	Empty string // "No movies yet"
	Cards []card
	Sort  *sortBar // webui_sort.go
}

func (sv *Server) uiMovies(w http.ResponseWriter, r *http.Request) {
	d := listData{Empty: "No movies yet"}
	o := gridOrderFor(w, r, "movies", movieSorts)
	d.Sort = newSortBar("/movies", o, movieSorts)
	var vs []movieView
	s := sv.pageShelf(r)
	for _, m := range s.Movies() {
		vs = append(vs, s.movieView(m, false))
	}
	for _, v := range sortGrid(vs, o, movieKey) {
		d.Cards = append(d.Cards, movieCard(v))
	}
	d.Cards = keyed(d.Cards, "l", "")
	sv.render(w, r, "list", page{Title: "Movies", Nav: "movies", Data: d})
}

func (sv *Server) uiShows(w http.ResponseWriter, r *http.Request) {
	d := listData{Empty: "No shows yet"}
	o := gridOrderFor(w, r, "tv", showSorts)
	d.Sort = newSortBar("/tv", o, showSorts)
	var vs []showView
	s := sv.pageShelf(r)
	for _, sh := range s.Shows() {
		vs = append(vs, s.showView(sh, false))
	}
	for _, v := range sortGrid(vs, o, showKey) {
		d.Cards = append(d.Cards, showCard(v))
	}
	d.Cards = keyed(d.Cards, "l", "")
	sv.render(w, r, "list", page{Title: "TV", Nav: "tv", Data: d})
}

func (sv *Server) uiMovie(w http.ResponseWriter, r *http.Request) {
	s := sv.pageShelf(r)
	m := s.Movie(r.PathValue("id"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	v := s.movieView(m, true)
	// One Play for all its parts: the player goes on from one to the next.
	play := sv.playFor(r, v.ID, v.Title, v.Position, v.Duration, v.Runtime, "", "hero", "")
	play.Toggle = &toggleData{ID: v.ID, Watched: v.Watched, Target: "hero"}
	sv.render(w, r, "movie", page{Title: m.Title, Nav: "movies", Data: map[string]any{
		"Fanart": v.Fanart,
		"Hero": heroData{Poster: v.Poster, WithPoster: true, Title: v.Title, Rating: v.Rating,
			Facts: movieFacts(v, viewerLang(r)), Genres: v.Genres, Plot: v.Plot, Play: play},
	}})
}

// movieFacts: the language if it isn't the viewer's, the year and the
// length (not what's left: the progress line says that).
func movieFacts(v movieView, viewer string) []string {
	return nonEmpty(langFact(v.OriginalLanguage, viewer), yearStr(v.Year), timeLeft(0, v.Duration, v.Runtime))
}

// viewerLang is the language the viewer reads: the first in the request's
// Accept-Language, without its region ("en-GB" is "en"); English without one.
func viewerLang(r *http.Request) string {
	first, _, _ := strings.Cut(r.Header.Get("Accept-Language"), ",")
	first, _, _ = strings.Cut(first, ";")
	if c := lang.Code(first); c != "" {
		return c
	}
	return "en"
}

// langFact names the language a title was made in ("Danish", "Silent"),
// "" if it's the viewer's or one without a name.
func langFact(code, viewer string) string {
	if code == viewer {
		return ""
	}
	return lang.Name(code)
}

func (sv *Server) uiShow(w http.ResponseWriter, r *http.Request) {
	s := sv.pageShelf(r)
	sh := s.Show(r.PathValue("id"))
	if sh == nil {
		http.NotFound(w, r)
		return
	}
	v := s.showView(sh, true)
	next := s.nextEpisode(sh)

	// The season shown: as asked, else the one with the next episode in it.
	var season seasonView
	want := -1
	if s := r.URL.Query().Get("season"); s != "" {
		want = library.Atoi(s)
	} else if next != nil {
		want = next.Season
	}
	for _, se := range v.Seasons {
		if se.Number == want {
			season = se
		}
	}
	if season.Episodes == nil && len(v.Seasons) > 0 {
		season = v.Seasons[0]
	}
	regular := 0
	for _, se := range v.Seasons {
		if se.Number > 0 {
			regular++
		}
	}
	links := map[string]string{}
	for _, e := range season.Episodes {
		links[e.ID] = sv.playLink(r, e.ID)
	}
	facts := nonEmpty(langFact(v.OriginalLanguage, viewerLang(r)), yearStr(v.Year))
	switch {
	case regular == 1:
		facts = append(facts, "1 season")
	case regular > 1:
		facts = append(facts, fmt.Sprintf("%d seasons", regular))
	}
	facts = append(facts, nonEmpty(v.Status)...)
	hero := heroData{Poster: v.Poster, WithPoster: true, Title: v.Title, Rating: v.Rating,
		Facts: facts, Genres: v.Genres, Plot: v.Plot}
	if next != nil {
		// Its row redraws with the hero, if it's in the season shown.
		code := epcode(next.Season, next.Episode, next.LastEpisode)
		hero.Play = sv.playFor(r, next.ID, code+" "+next.Title, next.Position, next.Duration, next.Runtime, code, "hero", "c-ep-"+next.ID)
	}
	sv.render(w, r, "show", page{Title: sh.Title, Nav: "tv", Data: map[string]any{
		"S": v, "Season": season, "Links": links, "Hero": hero,
	}})
}

// nextEpisode is the one to play next: the show's next episode as Up Next
// has it (showNext), else, past the last one watched with some skipped,
// the first unwatched. Specials aside.
func (s shelf) nextEpisode(sh *library.Show) *episodeView {
	if id := s.nextEpisodeID(sh); id != "" {
		v := s.episodeView(s.ix.Episode(id), false)
		return &v
	}
	return nil
}

type watchSub struct {
	VTT, Lang, Label string
	Forced           bool
}

type nextLink struct{ ID, Label string }

func (sv *Server) uiWatch(w http.ResponseWriter, r *http.Request) {
	ix := sv.pageShelf(r)
	id := r.PathValue("id")
	d := map[string]any{"ID": id}
	var subs []subView
	var start float64

	if m := ix.Movie(id); m != nil {
		v := ix.movieView(m, true)
		part := max(library.Atoi(r.URL.Query().Get("part")), 1)
		if part > len(v.Streams) {
			http.NotFound(w, r)
			return
		}
		// From the part asked for (the first, normally) on to the last, as
		// the Apple TV plays them; only the first part's place is kept.
		d["Parts"], d["Part"], d["Title"], d["Back"] = v.Streams, part-1, v.Title, "/movies/"+id
		d["Media"] = browserMedia(m.Media, len(v.Streams))
		subs, start = v.Subs, v.Position
		if part > 1 {
			start = 0
		}
	} else if e := ix.Episode(id); e != nil {
		v := ix.episodeView(e, true)
		d["Parts"], d["Part"], d["Back"] = []string{v.Stream}, 0, "/tv/"+v.ShowID+fmt.Sprintf("?season=%d", v.Season)
		d["Media"] = browserMedia([]*library.MediaFile{e.Media}, 1)
		d["Title"] = v.ShowTitle + "  " + epcode(v.Season, v.Episode, v.LastEpisode) + " " + v.Title
		subs, start = v.Subs, v.Position
		eps := e.Show.InOrder()
		for i, x := range eps {
			if x == e && i+1 < len(eps) {
				n := ix.episodeView(eps[i+1], false)
				d["Next"] = nextLink{n.ID, epcode(n.Season, n.Episode, n.LastEpisode) + " " + n.Title}
			}
		}
	} else {
		http.NotFound(w, r)
		return
	}

	var ws []watchSub
	for _, s := range subs {
		label := langName(s.Lang)
		if s.Forced {
			label += " (forced)"
		} else if s.SDH {
			label += " (SDH)"
		}
		ws = append(ws, watchSub{VTT: s.VTT, Lang: s.Lang, Label: label, Forced: s.Forced})
	}
	d["Subs"], d["Start"], d["PlayURL"] = ws, start, sv.playLink(r, id)
	sv.render(w, r, "watch", page{Title: fmt.Sprint(d["Title"]), Bare: true, BodyClass: "watch", Data: d})
}

// browserMedia is what the watch page asks the browser about each part, as
// JSON: null for a part that hasn't been probed.
func browserMedia(media []*library.MediaFile, n int) string {
	out := make([]*library.Browser, n)
	for i := range out {
		if i < len(media) {
			out[i] = media[i].Browser()
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// uiM3U hands the stream to a real player: browsers open .m3u files with
// VLC, mpv, IINA or whatever is set up for playlists, and those play
// anything. VLC also picks up the first subtitle file.
func (sv *Server) uiM3U(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("file"), ".m3u")
	ix := sv.shelf(r) // its paths signed for the request's token
	base := baseURL(r)
	abs := func(u string) string { return base + u }

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	title := ""
	if m := ix.Movie(id); m != nil {
		v := ix.movieView(m, true)
		title = library.TitleYear(v.Title, v.Year)
		for i, s := range v.Streams {
			if len(v.Subs) > 0 {
				fmt.Fprintf(&b, "#EXTVLCOPT:sub-file=%s\n", abs(v.Subs[0].URL))
			}
			name := title
			if len(v.Streams) > 1 {
				name = fmt.Sprintf("%s, part %d", title, i+1)
			}
			fmt.Fprintf(&b, "#EXTINF:%d,%s\n%s\n", v.Runtime*60, oneLine(name), abs(s))
		}
	} else if e := ix.Episode(id); e != nil {
		v := ix.episodeView(e, true)
		title = v.ShowTitle + " " + epcode(v.Season, v.Episode, v.LastEpisode)
		if len(v.Subs) > 0 {
			fmt.Fprintf(&b, "#EXTVLCOPT:sub-file=%s\n", abs(v.Subs[0].URL))
		}
		fmt.Fprintf(&b, "#EXTINF:%d,%s\n%s\n", v.Runtime*60, oneLine(title+" "+v.Title), abs(v.Stream))
	} else {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.m3u"`, library.SafeName(title)))
	w.Write([]byte(b.String()))
}

// oneLine is s with line breaks and other control characters as spaces:
// in a playlist, a new line is a new entry or option.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
}

func (sv *Server) uiWatched(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s := sv.pageShelf(r)
	if s.Movie(id) == nil && s.Episode(id) == nil {
		http.NotFound(w, r)
		return
	}
	s.state().SetWatched(id, r.FormValue("watched") == "1")
	sv.rerender(w, r)
}

// uiLogin signs a browser in: the right token sets the mvd_session cookie
// (made from the token, not the token) and goes on to where the browser
// was going (next).
func (sv *Server) uiLogin(w http.ResponseWriter, r *http.Request) {
	next := localPath(r.FormValue("next"))
	type loginData struct {
		Wrong  bool
		Wait   int // seconds, after too many wrong tries
		Next   string
		Device string // on the way to the Pair page: the device signing in lets in
	}
	device := ""
	if u, err := url.Parse(next); err == nil && u.Path == "/pair" {
		if req, ok := sv.pairWaiting(u.Query().Get("code")); ok {
			if device = req.device; device == "" {
				device = "a device"
			}
		}
	}
	if r.Method != http.MethodPost {
		sv.render(w, r, "login", page{Title: "Sign in", Bare: true, Data: loginData{Next: next, Device: device}})
		return
	}
	if sv.cfg.Token == "" {
		http.Redirect(w, r, next, http.StatusSeeOther) // nothing to sign in to
		return
	}
	addr, now := sv.clientAddr(r), sv.clock()
	if wait := sv.guesses.wait(addr, now); wait > 0 {
		secs := int((wait + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", fmt.Sprint(secs))
		w.WriteHeader(http.StatusTooManyRequests)
		sv.render(w, r, "login", page{Title: "Sign in", Bare: true, Data: loginData{Wait: secs, Next: next, Device: device}})
		return
	}
	acc := sv.match(r.FormValue("token"))
	if acc == nil {
		sv.guesses.fail(addr, now)
		w.Header().Set("WWW-Authenticate", requestScheme)
		w.WriteHeader(http.StatusUnauthorized)
		sv.render(w, r, "login", page{Title: "Sign in", Bare: true, Data: loginData{Wrong: true, Next: next, Device: device}})
		return
	}
	sv.guesses.ok(addr)
	// Secure when the browser reached us over HTTPS (directly, or via a
	// reverse proxy).
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: acc.cookie, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600})
	http.SetCookie(w, &http.Cookie{Name: tokenCookie, Path: "/", MaxAge: -1}) // the old one held the token
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// uiLogout signs the browser out.
func (sv *Server) uiLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: tokenCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// localPath is next if it's a path on this server ("/pair?code=…"), else
// "/": signing in mustn't send anyone to another site.
func localPath(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return next
}

// isPage tells a browser page (sent to sign in) from an API call or a
// stream (given a plain 401).
func isPage(p string) bool {
	switch {
	case p == "/":
		return true
	case strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, "/stream/"), strings.HasPrefix(p, "/subs/"),
		strings.HasPrefix(p, "/art/"), strings.HasPrefix(p, "/ui/"):
		return false
	}
	return path.Ext(p) == ""
}

func importSummary(r importResult) string {
	var parts []string
	add := func(n int, one, many string) {
		switch {
		case n == 1:
			parts = append(parts, one)
		case n > 1:
			parts = append(parts, fmt.Sprintf(many, n))
		}
	}
	add(r.Movies, "1 movie filed", "%d movies filed")
	add(r.Episodes, "1 episode filed", "%d episodes filed")
	add(r.Seeding, "1 download still seeding", "%d downloads still seeding")
	add(r.Waiting, "1 thing still arriving", "%d things still arriving")
	add(r.Duplicates, "1 already in the library", "%d already in the library")
	add(r.Left, "1 thing left for you to sort out", "%d things left for you to sort out")
	add(r.Failed, "1 problem", "%d problems")
	if len(parts) == 0 {
		return "Nothing to import."
	}
	s := strings.Join(parts, ", ") + "."
	return strings.ToUpper(s[:1]) + s[1:]
}

func (sv *Server) partial(w http.ResponseWriter, set, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := sv.tpl[set].ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// uiClearProgress forgets where you'd got to in a movie or episode, then
// answers with the page it was asked from, re-rendered, for the button's
// hx-select to take what changed.
func (sv *Server) uiClearProgress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s := sv.pageShelf(r)
	if s.Movie(id) == nil && s.Episode(id) == nil {
		http.NotFound(w, r)
		return
	}
	s.state().ClearPosition(id)
	sv.rerender(w, r)
}

func (sv *Server) uiRemoveFromUpNext(w http.ResponseWriter, r *http.Request) {
	if !sv.pageShelf(r).removeFromUpNext(r.PathValue("id")) {
		http.NotFound(w, r)
		return
	}
	sv.rerender(w, r)
}

// rerender serves the page an htmx request came from (htmx says which, in
// HX-Current-URL) as it is now. Without it, the page reloads.
func (sv *Server) rerender(w http.ResponseWriter, r *http.Request) {
	u, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if err != nil || u.Path == "" || !isPage(u.Path) || sv.mux == nil {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.RequestURI(), nil)
	if err != nil {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	req.Header["Cookie"] = r.Header["Cookie"] // a grid's order, say
	sv.mux.ServeHTTP(w, req)
}

// baseURL is this server as the browser reached it.
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// playLink is the address of an item's .m3u, to open in VLC or mpv: a
// signed link for the request's token, when there is one, since a player
// can't sign in. Everything in the .m3u is signed the same way.
func (sv *Server) playLink(r *http.Request, id string) string {
	u := "/play/" + id + ".m3u"
	if a := sv.who(r); a.kid != "" {
		u = (&linkSigner{kid: a.kid, key: a.link}).sign(u)
	}
	return baseURL(r) + u
}

// ago is how long ago, roughly: "just now", "5 minutes ago", "3 days ago".
func ago(t time.Time) string { return agoAt(t, time.Now()) }

// agoAt is ago, seen from now.
func agoAt(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < 2*time.Minute:
		return "a minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 2*time.Hour:
		return "an hour ago"
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
