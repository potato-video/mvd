// Package server is mvd's HTTP side: the JSON API, streaming with watch
// progress, and (with the webui tag) the web UI.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mvd/internal/config"
	"mvd/internal/library"
	"mvd/internal/tmdb"
)

// Server is "mpd for video": a JSON API over the library, plus streaming.
// No transcoding, no database: files go out as they are, via sendfile.
type Server struct {
	cfg      config.Config
	ix       atomic.Pointer[library.Index]
	state    *State             // the admin's progress
	accesses []*access          // one per token, the admin's first (access.go)
	kids     map[string]*access // the accesses with a token, by key id
	guesses  guesses            // wrong credentials, by address (credentials.go)
	pairs    pairing            // devices waiting to be let in (pair.go)
	scanMu   sync.Mutex         // one scan at a time; also guards swapping in probes
	probes   probing
	tpl      uiTemplates
	dl       downloadsState // the downloads feature's, when built in
	rx       remuxState     // and the remux's
	ups      uploads
	auto     autoImport
	imports  imports

	newTMDB   func(config.Config) *tmdb.Client // for imports; tests swap it
	now       func() time.Time                 // tests set the clock; nil is time.Now
	mux       *http.ServeMux                   // the routes, for re-rendering a page in place
	polled    sharedLook                       // the Incoming page's last look, shared by its polls
	pollShare time.Duration                    // for this long (0: not shared)
	started   time.Time                        // for /api/status's uptime
	cpu       cpuSampler                       // and its CPU share
}

// incomingShare is how long the Incoming page's polls share one look at
// the import folder, the uploads and Transmission: each tab polls every 5
// seconds, and each look stats the whole import folder on a USB drive and
// asks Transmission for every torrent's tracker stats.
const incomingShare = 4 * time.Second

// sharedLook is something expensive to work out, and when it was, for
// requests a moment apart to share.
type sharedLook struct {
	mu sync.Mutex
	v  any
	at time.Time
}

func (p *sharedLook) keep(v any, at time.Time) {
	p.mu.Lock()
	p.v, p.at = v, at
	p.mu.Unlock()
}

func (sv *Server) clock() time.Time {
	if sv.now != nil {
		return sv.now()
	}
	return time.Now()
}

// Run serves the library until it's told to stop.
func Run(cfg config.Config) int {
	if cfg.MoviesDir == "" && cfg.TVDir == "" {
		fmt.Fprintln(os.Stderr, "mvd: nothing to serve: set video_dir (or movies_dir/tv_dir) under [paths] in ~/.config/mvd/config.toml")
		return 2
	}
	warnings, err := cfg.CheckTokens()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mvd: config: %v\n", err)
		return 2
	}
	for _, w := range warnings {
		log.Printf("mvd: %s", w)
	}
	st, err := loadState(cfg.StateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mvd: %s: %v\n", cfg.StateFile, err)
		return 1
	}

	if config.SecretsExposed(cfg) {
		log.Printf("mvd: %s holds secrets and other users can read it: chmod 600 it", config.Path())
	}
	st.resumeAfter = cfg.ResumeAfter
	sv := &Server{cfg: cfg, state: st, pollShare: incomingShare}
	if err := sv.setupAccess(); err != nil {
		fmt.Fprintf(os.Stderr, "mvd: %v\n", err)
		return 1
	}
	// Start from the saved index if there is one (quick, even on a router
	// with a big library), then refresh it in the background.
	ix, err := library.LoadIndex(cfg.IndexFile)
	if err == nil {
		sv.ix.Store(ix)
		go sv.rescan()
	} else {
		ix = sv.rescan()
	}
	// No WriteTimeout: a stream runs as long as the movie. IdleTimeout stops
	// idle keep-alive connections (every TV and phone that ever looked)
	// piling up on a small router.
	hs := &http.Server{Addr: cfg.Listen(), Handler: sv.routes(), ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 2 * time.Minute}

	stop := make(chan struct{})
	go sv.flushEvery(5*time.Second, stop)
	sv.startAutoImport(stop)
	go sv.pruneCacheEvery(24*time.Hour, stop)
	go sv.sweepUploadsEvery(time.Hour, stop)
	go sv.sampleCPUEvery(stop)
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(ctx)
		sv.stopRemux()
	}()

	ln, err := net.Listen("tcp", cfg.Listen())
	if err != nil {
		close(stop)
		log.Printf("mvd: %v", err)
		return 1
	}
	log.Printf("mvd: %d movies, %d shows; listening on %s", len(ix.Movies), len(ix.Shows), cfg.Listen())
	stopBonjour := advertise(cfg) // once there's something to find
	err = hs.Serve(ln)
	stopBonjour()
	close(stop)
	sv.flushAll()
	if err != nil && err != http.ErrServerClosed {
		log.Printf("mvd: %v", err)
		return 1
	}
	return 0
}

// pruneCacheEvery deletes TMDB's expired answers now and every d, as the
// commands do after a run: the server's imports, searches and posters add
// to the cache for as long as it runs.
func (sv *Server) pruneCacheEvery(d time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		tmdb.New(sv.cfg).PruneCache()
		select {
		case <-t.C:
		case <-stop:
			return
		}
	}
}

func (sv *Server) rescan() *library.Index {
	sv.scanMu.Lock()
	defer sv.scanMu.Unlock()
	// Keep what's been probed, so only new or replaced files are probed again.
	known := library.Probed{}
	if old := sv.ix.Load(); old != nil {
		known = old.Probed()
	}
	for f, mf := range sv.takePending() {
		known[f] = mf
	}
	ix := library.BuildIndex(sv.cfg.MoviesDir, sv.cfg.TVDir, known)
	sv.ix.Store(ix)
	sv.saveIndex(ix)
	sv.startProbing(ix)
	return ix
}

// Version is this build's, set by main; every response says it
// (X-Mvd-Version), so "mvd version" can tell when the server running isn't
// the binary just installed.
var Version = "dev"

// APILevel is the API's, which only grows: it goes up by one with each
// addition a client may rely on (a route, a field, a value), and nothing
// is ever taken away or changed in meaning. Every response says it
// (X-Mvd-Api), as do /api/status and the Bonjour TXT, so a client can tell
// what a server has without making sense of its build. A server without
// it is level 1. api/openapi.yaml's info.version is the same number.
const APILevel = 1

var apiLevel = strconv.Itoa(APILevel)

func versioned(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Mvd-Version", Version)
		w.Header().Set("X-Mvd-Api", apiLevel)
		next.ServeHTTP(w, r)
	})
}

func (sv *Server) routes() http.Handler {
	sv.started = sv.clock()
	if err := sv.setupAccess(); err != nil {
		log.Printf("mvd: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", sv.apiStatus)
	mux.HandleFunc("GET /api/up-next", sv.apiUpNext)
	mux.HandleFunc("POST /api/up-next/{id}/remove", sv.apiRemoveFromUpNext)
	mux.HandleFunc("GET /api/movies", sv.movies)
	mux.HandleFunc("GET /api/movies/{id}", sv.item)
	mux.HandleFunc("GET /api/shows", sv.shows)
	mux.HandleFunc("GET /api/episodes/new", sv.apiNewEpisodes)
	mux.HandleFunc("GET /api/shows/{id}", sv.item)
	mux.HandleFunc("GET /api/item/{id}", sv.item)
	mux.HandleFunc("GET /api/search", sv.librarySearch)
	mux.HandleFunc("POST /api/progress/{id}", sv.progress)
	mux.HandleFunc("POST /api/rescan", sv.rescanHandler)
	mux.HandleFunc("POST /api/import", sv.apiImport)
	sv.pairRoutes(mux)
	sv.needsRoutes(mux)
	sv.downloadRoutes(mux)
	sv.uploadRoutes(mux)
	mux.HandleFunc("GET /stream/{id}", sv.stream)
	mux.HandleFunc("GET /subs/{id}/{n}", sv.subs)
	mux.HandleFunc("GET /art/{id}/{kind}", sv.art)
	sv.uiRoutes(mux)
	sv.remuxRoutes(mux)
	sv.mux = mux
	return versioned(sv.guard(limitBodies(sv.auth(mux))))
}

const (
	formBodyMax    = 64 << 10 // any form or JSON body
	torrentBodyMax = 8 << 20  // adding a download: a .torrent file
)

// limitBodies caps every request's body before anything reads it, signed in
// or not. A handler's r.FormValue parses a multipart body, keeping 32 MB in
// memory and writing the rest to a file in /tmp (RAM on OpenWRT) with no
// limit, so multipart is refused except where a .torrent is added. Upload
// pieces stream to the drive and have no cap.
func limitBodies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		max, ok := bodyLimit(r)
		if !ok {
			http.Error(w, "multipart bodies aren't accepted here", http.StatusUnsupportedMediaType)
			return
		}
		if max >= 0 && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		next.ServeHTTP(w, r)
	})
}

// bodyLimit is the most a request's body may be (-1 for no limit), or false
// if it's multipart where that isn't wanted.
func bodyLimit(r *http.Request) (int64, bool) {
	if r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/uploads/") {
		return -1, true
	}
	if r.Method == http.MethodPost && (r.URL.Path == "/api/downloads" || r.URL.Path == "/ui/downloads") {
		return torrentBodyMax, true
	}
	ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if strings.EqualFold(strings.TrimSpace(ct), "multipart/form-data") {
		return 0, false
	}
	return formBodyMax, true
}

// --- JSON views ---

type subView struct {
	Lang   string `json:"lang,omitempty"`
	Forced bool   `json:"forced,omitempty"`
	SDH    bool   `json:"sdh,omitempty"`
	Format string `json:"format"`
	URL    string `json:"url"`
	VTT    string `json:"vtt,omitempty"` // WebVTT version, for browsers
}

type movieView struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Year    int    `json:"year,omitempty"`
	Set     string `json:"set,omitempty"`
	Poster  string `json:"poster,omitempty"`
	Fanart  string `json:"fanart,omitempty"`
	Watched bool   `json:"watched,omitempty"`
	New     bool   `json:"new,omitempty"` // added in the last newFor, not watched or started
	// For sorting: when it was added, and last played or marked watched.
	Added       time.Time `json:"added,omitzero"`
	LastWatched time.Time `json:"last_watched,omitzero"`
	Position    float64   `json:"position,omitempty"`
	Duration    float64   `json:"duration,omitempty"`
	Plot        string    `json:"plot,omitempty"`
	Tagline     string    `json:"tagline,omitempty"`
	Runtime     int       `json:"runtime,omitempty"`
	Rating      string    `json:"rating,omitempty"`
	Genres      []string  `json:"genres,omitempty"`
	Directors   []string  `json:"directors,omitempty"`
	// The language it was made in: ISO 639-1 ("da"), or zxx for none.
	OriginalLanguage string     `json:"original_language,omitempty"`
	Streams          []string   `json:"streams,omitempty"`
	Files            []fileView `json:"files,omitempty"` // one a stream, once probed
	Subs             []subView  `json:"subs,omitempty"`
}

type episodeView struct {
	Kind        string     `json:"kind"`
	ID          string     `json:"id"`
	ShowID      string     `json:"show_id"`
	ShowTitle   string     `json:"show_title"`
	Season      int        `json:"season"`
	Episode     int        `json:"episode"`
	LastEpisode int        `json:"last_episode,omitempty"` // multi-episode files
	Title       string     `json:"title,omitempty"`
	Plot        string     `json:"plot,omitempty"`
	Aired       string     `json:"aired,omitempty"`
	Runtime     int        `json:"runtime,omitempty"`
	Thumb       string     `json:"thumb,omitempty"`
	Stream      string     `json:"stream"`
	Files       []fileView `json:"files,omitempty"` // once probed
	Subs        []subView  `json:"subs,omitempty"`
	Watched     bool       `json:"watched,omitempty"`
	Position    float64    `json:"position,omitempty"`
	Duration    float64    `json:"duration,omitempty"`
	// The show's, so a client showing an episode on its own (in Up Next,
	// say) has them without fetching the show.
	Genres           []string `json:"genres,omitempty"`
	Rating           string   `json:"rating,omitempty"`
	OriginalLanguage string   `json:"original_language,omitempty"`
	Poster           string   `json:"poster,omitempty"` // the season's, else the show's
	Fanart           string   `json:"fanart,omitempty"`
}

type seasonView struct {
	Number   int           `json:"number"`
	Name     string        `json:"name,omitempty"`
	Poster   string        `json:"poster,omitempty"`
	Episodes []episodeView `json:"episodes"`
}

type showView struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	Poster    string `json:"poster,omitempty"`
	Fanart    string `json:"fanart,omitempty"`
	Episodes  int    `json:"episode_count"`
	Unwatched int    `json:"unwatched"`
	NewCount  int    `json:"new_count,omitempty"` // of those, added in the last newFor
	// For sorting: its newest episode's Added, and the latest episode's
	// LastWatched, specials included.
	Added       time.Time `json:"added,omitzero"`
	LastWatched time.Time `json:"last_watched,omitzero"`
	Plot        string    `json:"plot,omitempty"`
	Rating      string    `json:"rating,omitempty"`
	Status      string    `json:"status,omitempty"`
	Genres      []string  `json:"genres,omitempty"`
	// The language it was made in: ISO 639-1 ("da"), or zxx for none.
	OriginalLanguage string       `json:"original_language,omitempty"`
	Seasons          []seasonView `json:"seasons,omitempty"`
	// The one to play next, as Up Next has it (showNext); with the seasons.
	NextEpisode string `json:"next_episode_id,omitempty"`
}

// art is the path of a picture ("" if there isn't one), as the shelf
// hands paths out.
func (s shelf) art(id, kind, path string) string {
	if path == "" {
		return ""
	}
	return s.link("/art/" + id + "/" + kind)
}

func (s shelf) subViews(id string, subs []library.Sub) []subView {
	var out []subView
	for i, sub := range subs {
		u := fmt.Sprintf("/subs/%s/%d", id, i)
		v := subView{Lang: sub.Lang, Forced: sub.Forced, SDH: sub.SDH, Format: sub.Format, URL: s.link(u)}
		if sub.Format == "srt" || sub.Format == "vtt" {
			v.VTT = s.link(u + ".vtt")
		}
		out = append(out, v)
	}
	return out
}

func (s shelf) movieView(m *library.Movie, full bool) movieView {
	p := s.state().Get(m.ID)
	v := movieView{Kind: "movie", ID: m.ID, Title: m.Title, Year: m.Year, Set: m.Set,
		Poster: s.art(m.ID, "poster", m.Poster), Fanart: s.art(m.ID, "fanart", m.Fanart),
		Watched: p.Watched, Position: p.Position, Duration: p.Duration, Runtime: m.Runtime,
		Added: m.Added, LastWatched: p.LastPlayed()}
	v.New = !p.Watched && p.Position == 0 && m.Added.After(s.sv.clock().Add(-newFor))
	if full {
		v.Plot, v.Tagline, v.Rating = m.Plot, m.Tagline, m.Rating
		v.Genres, v.Directors, v.Subs = m.Genres, m.Directors, s.subViews(m.ID, m.Subs)
		v.OriginalLanguage = m.OriginalLanguage
		for i := range m.Files {
			if i == 0 {
				v.Streams = append(v.Streams, s.link("/stream/"+m.ID))
			} else {
				v.Streams = append(v.Streams, s.link(fmt.Sprintf("/stream/%s?part=%d", m.ID, i+1)))
			}
		}
		v.Files = fileViews(m.Media, len(m.Files))
	}
	return v
}

func (s shelf) episodeView(e *library.Episode, full bool) episodeView {
	p := s.state().Get(e.ID)
	v := episodeView{Kind: "episode", ID: e.ID, ShowID: e.Show.ID, ShowTitle: e.Show.Title,
		Season: e.Season, Episode: e.Episodes[0], Title: e.Title, Aired: e.Aired, Runtime: e.Runtime,
		Thumb: s.art(e.ID, "thumb", e.Thumb), Stream: s.link("/stream/" + e.ID),
		Watched: p.Watched, Position: p.Position, Duration: p.Duration,
		Genres: e.Show.Genres, Rating: e.Show.Rating, OriginalLanguage: e.Show.OriginalLanguage,
		Fanart: s.art(e.Show.ID, "fanart", e.Show.Fanart)}
	for _, se := range e.Show.Seasons {
		if se.Number == e.Season && se.Poster != "" {
			v.Poster = s.art(e.Show.ID, fmt.Sprintf("season-%d", se.Number), se.Poster)
		}
	}
	if v.Poster == "" {
		v.Poster = s.art(e.Show.ID, "poster", e.Show.Poster)
	}
	if len(e.Episodes) > 1 {
		v.LastEpisode = e.Episodes[len(e.Episodes)-1]
	}
	if full {
		v.Plot, v.Subs = e.Plot, s.subViews(e.ID, e.Subs)
		if e.Media != nil {
			v.Files = fileViews([]*library.MediaFile{e.Media}, 1)
		}
	}
	return v
}

func (s shelf) showView(sh *library.Show, full bool) showView {
	v := showView{Kind: "show", ID: sh.ID, Title: sh.Title, Year: sh.Year,
		Poster: s.art(sh.ID, "poster", sh.Poster)}
	cutoff := s.sv.clock().Add(-newFor)
	for _, se := range sh.Seasons {
		for _, e := range se.Episodes {
			p := s.state().Get(e.ID)
			if e.Added.After(v.Added) {
				v.Added = e.Added
			}
			if t := p.LastPlayed(); t.After(v.LastWatched) {
				v.LastWatched = t
			}
			if se.Number == 0 {
				continue // specials aren't counted
			}
			v.Episodes++
			if !p.Watched {
				v.Unwatched++
				if e.Added.After(cutoff) {
					v.NewCount++
				}
			}
		}
	}
	if full {
		v.Fanart = s.art(sh.ID, "fanart", sh.Fanart)
		v.Plot, v.Rating, v.Status, v.Genres = sh.Plot, sh.Rating, sh.Status, sh.Genres
		v.OriginalLanguage = sh.OriginalLanguage
		for _, se := range sh.Seasons {
			sn := seasonView{Number: se.Number, Name: se.Name,
				Poster: s.art(sh.ID, fmt.Sprintf("season-%d", se.Number), se.Poster)}
			for _, e := range se.Episodes {
				ev := s.episodeView(e, false)
				ev.Plot = e.Plot
				sn.Episodes = append(sn.Episodes, ev)
			}
			v.Seasons = append(v.Seasons, sn)
		}
		v.NextEpisode = s.nextEpisodeID(sh)
	}
	return v
}

// nextEpisodeID is the episode a show's page plays: showNext's, else (after
// the last episode, with some skipped) the first unwatched one. "" for none.
func (s shelf) nextEpisodeID(sh *library.Show) string {
	all := s.state().All()
	if e, _, _, _ := showNext(sh, all); e != nil {
		return e.ID
	}
	for _, e := range sh.InOrder() {
		if !all[e.ID].Watched {
			return e.ID
		}
	}
	return ""
}

// --- handlers ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// movies lists the movies, by title. ?since=30d keeps those added in that
// time (newest first), and ?unwatched=true those not yet watched.
func (sv *Server) movies(w http.ResponseWriter, r *http.Request) {
	f, err := listFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := sv.shelf(r).movieList(f)
	if out == nil {
		out = []movieView{}
	}
	writeJSON(w, out)
}

func (sv *Server) shows(w http.ResponseWriter, r *http.Request) {
	s := sv.shelf(r)
	shows := s.Shows()
	out := make([]showView, 0, len(shows))
	for _, sh := range shows {
		out = append(out, s.showView(sh, false))
	}
	writeJSON(w, out)
}

func (sv *Server) item(w http.ResponseWriter, r *http.Request) {
	s := sv.shelf(r)
	id := r.PathValue("id")
	switch {
	case s.Movie(id) != nil:
		writeJSON(w, s.movieView(s.Movie(id), true))
	case s.Show(id) != nil:
		writeJSON(w, s.showView(s.Show(id), true))
	case s.Episode(id) != nil:
		writeJSON(w, s.episodeView(s.Episode(id), true))
	default:
		http.NotFound(w, r)
	}
}

// progress takes {"position": s, "duration": s} from a player, or
// {"watched": true|false}. A show id with "watched" marks every episode.
func (sv *Server) progress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Position *float64 `json:"position"`
		Duration float64  `json:"duration"`
		Watched  *bool    `json:"watched"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad JSON", http.StatusBadRequest)
		return
	}
	s := sv.shelf(r)
	id := r.PathValue("id")
	if sh := s.Show(id); sh != nil && body.Watched != nil {
		for _, se := range sh.Seasons {
			for _, e := range se.Episodes {
				s.state().SetWatched(e.ID, *body.Watched)
			}
		}
		if !*body.Watched {
			s.state().SetWatched(sh.ID, false) // and out of Up Next no longer
		}
		writeJSON(w, s.showView(sh, false))
		return
	}
	if s.Movie(id) == nil && s.Episode(id) == nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case body.Watched != nil:
		writeJSON(w, s.state().SetWatched(id, *body.Watched))
	case body.Position != nil:
		writeJSON(w, s.state().Report(id, *body.Position, body.Duration))
	default:
		http.Error(w, "need position or watched", http.StatusBadRequest)
	}
}

func (sv *Server) rescanHandler(w http.ResponseWriter, r *http.Request) {
	ix := sv.rescan()
	writeJSON(w, map[string]int{"movies": len(ix.Movies), "shows": len(ix.Shows), "episodes": ix.EpisodeCount()})
}

var videoMIME = map[string]string{
	".mkv": "video/x-matroska", ".webm": "video/webm", ".mp4": "video/mp4", ".m4v": "video/mp4",
	".mov": "video/quicktime", ".avi": "video/x-msvideo", ".wmv": "video/x-ms-wmv",
	".mpg": "video/mpeg", ".mpeg": "video/mpeg", ".ts": "video/mp2t", ".m2ts": "video/mp2t",
	".ogv": "video/ogg", ".vob": "video/mpeg", ".divx": "video/x-msvideo", ".iso": "application/octet-stream",
}

// stream sends a video as-is. ServeContent does the work: Range requests
// (every seek is one), If-Range, HEAD, and sendfile from disk to socket.
func (sv *Server) stream(w http.ResponseWriter, r *http.Request) {
	ix := sv.shelf(r)
	id := r.PathValue("id")
	path := ""
	if m := ix.Movie(id); m != nil {
		part := max(library.Atoi(r.URL.Query().Get("part")), 1)
		if part > len(m.Files) {
			http.NotFound(w, r)
			return
		}
		path = m.Files[part-1]
	} else if e := ix.Episode(id); e != nil {
		path = e.File
	} else {
		http.NotFound(w, r)
		return
	}
	sendFile(w, r, path, videoMIME[strings.ToLower(filepath.Ext(path))], "")
}

func (sv *Server) subs(w http.ResponseWriter, r *http.Request) {
	ix := sv.shelf(r)
	id, n := r.PathValue("id"), r.PathValue("n")
	vtt := strings.HasSuffix(n, ".vtt")
	n = strings.TrimSuffix(n, ".vtt")
	var subs []library.Sub
	if m := ix.Movie(id); m != nil {
		subs = m.Subs
	} else if e := ix.Episode(id); e != nil {
		subs = e.Subs
	}
	i := library.Atoi(n)
	if n == "" || i < 0 || i >= len(subs) || (i == 0 && n != "0") {
		http.NotFound(w, r)
		return
	}
	sub := subs[i]
	switch {
	case vtt && sub.Format == "srt":
		// Read whole to convert, so not past a size no subtitles reach.
		b, err := readCapped(sub.Path, maxVTTSource)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Write(srtToVTT(b))
	case vtt && sub.Format != "vtt":
		http.Error(w, "no WebVTT version of this subtitle format", http.StatusNotFound)
	default:
		types := map[string]string{"srt": "application/x-subrip", "vtt": "text/vtt", "ass": "text/x-ssa"}
		ct := types[sub.Format]
		if ct == "" {
			ct = "application/octet-stream"
		}
		sendFile(w, r, sub.Path, ct, "")
	}
}

// maxVTTSource is the biggest .srt converted to WebVTT: a feature's
// subtitles are a few hundred KB.
const maxVTTSource = 8 << 20

// readCapped reads a file of at most max bytes.
func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(b)) > max {
		err = fmt.Errorf("%s: over %d bytes", filepath.Base(path), max)
	}
	return b, err
}

func (sv *Server) art(w http.ResponseWriter, r *http.Request) {
	ix := sv.shelf(r)
	id, kind := r.PathValue("id"), r.PathValue("kind")
	path := ""
	switch {
	case ix.Movie(id) != nil && kind == "poster":
		path = ix.Movie(id).Poster
	case ix.Movie(id) != nil && kind == "fanart":
		path = ix.Movie(id).Fanart
	case ix.Show(id) != nil && kind == "poster":
		path = ix.Show(id).Poster
	case ix.Show(id) != nil && kind == "fanart":
		path = ix.Show(id).Fanart
	case ix.Show(id) != nil && strings.HasPrefix(kind, "season-"):
		n := library.Atoi(strings.TrimPrefix(kind, "season-"))
		for _, se := range ix.Show(id).Seasons {
			if se.Number == n {
				path = se.Poster
			}
		}
	case ix.Episode(id) != nil && kind == "thumb":
		path = ix.Episode(id).Thumb
	}
	if path == "" {
		http.NotFound(w, r)
		return
	}
	sendFile(w, r, path, "image/jpeg", "max-age=3600")
}

func sendFile(w http.ResponseWriter, r *http.Request, path, contentType, cache string) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "file has gone; try a rescan", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if cache != "" {
		w.Header().Set("Cache-Control", cache)
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, st.Size(), st.ModTime().Unix()))
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}
