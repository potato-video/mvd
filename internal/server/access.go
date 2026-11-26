package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mvd/internal/config"
	"mvd/internal/library"
)

// Access levels. The [server] token is the admin's and can do everything;
// each [[server.tokens]] entry is a viewer's, which can browse, play and
// keep its own progress, limited to some certifications if it says so.
// With no token configured, everyone is the admin.

// access is what a request is allowed: worked out once, in auth, from its
// credential, and kept in the request's context (who).
type access struct {
	name    string // "admin", or the entry's name; "" with no token configured
	admin   bool
	token   string          // what it's matched against; "" when there's no token at all
	ratings map[string]bool // lower-cased; nil: no limit
	unrated bool            // with a limit: whether it sees things with no certification
	state   *State          // its own watch progress
	keys                    // derived from token (credentials.go); zero with no token
}

// allowed says whether this access sees something with that certification.
func (a *access) allowed(rating string) bool {
	if a.ratings == nil {
		return true
	}
	rating = strings.ToLower(strings.TrimSpace(rating))
	if rating == "" {
		return a.unrated
	}
	return a.ratings[rating]
}

// newAccesses builds one access per token, the admin's first, loading
// each viewer's progress from beside the admin's (state-kids.json). With
// no token there's one, open to everyone.
func newAccesses(cfg config.Config, admin *State) ([]*access, error) {
	accs := []*access{{name: config.AdminName, admin: true, token: cfg.Token, state: admin}}
	if cfg.Token == "" {
		accs[0].name = ""
	} else {
		accs[0].keys = deriveKeys(cfg.Token)
	}
	dir := filepath.Dir(admin.path)
	for _, t := range cfg.Tokens {
		path := filepath.Join(dir, "state-"+config.TokenSlug(t.Name)+".json")
		st, err := loadState(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		st.resumeAfter = admin.resumeAfter
		a := &access{name: t.Name, token: t.Token, unrated: t.Unrated, state: st, keys: deriveKeys(t.Token)}
		if t.Limited {
			a.ratings = map[string]bool{}
			for _, r := range t.Ratings {
				a.ratings[strings.ToLower(strings.TrimSpace(r))] = true
			}
		}
		accs = append(accs, a)
	}
	return accs, nil
}

// setupAccess builds the accesses, once, if Run hasn't.
func (sv *Server) setupAccess() error {
	if sv.accesses != nil {
		return nil
	}
	if sv.state == nil {
		sv.state = &State{m: map[string]Progress{}}
	}
	accs, err := newAccesses(sv.cfg, sv.state)
	if err != nil {
		return err
	}
	sv.accesses = accs
	sv.kids = map[string]*access{}
	for _, a := range accs {
		if a.kid != "" {
			sv.kids[a.kid] = a
		}
	}
	return nil
}

// flushEvery writes every token's progress every d, from one goroutine.
func (sv *Server) flushEvery(d time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			sv.flushAll()
		case <-stop:
			return
		}
	}
}

func (sv *Server) flushAll() {
	for _, a := range sv.accesses {
		if err := a.state.Flush(); err != nil {
			log.Printf("mvd: saving progress (%s): %v", a.state.path, err)
		}
	}
}

type accessKey struct{}

// who is the request's access. A request auth didn't see (a public route)
// has one that sees nothing.
func (sv *Server) who(r *http.Request) *access {
	if a, ok := r.Context().Value(accessKey{}).(*access); ok {
		return a
	}
	return &nobody
}

var nobody = access{ratings: map[string]bool{}, state: &State{m: map[string]Progress{}}}

// match is the access whose token got is, comparing against every one
// in constant time, with no early exit; nil for none.
func (sv *Server) match(got string) *access {
	var found *access
	b := []byte(got)
	for _, a := range sv.accesses {
		if a.token != "" && subtle.ConstantTimeCompare(b, []byte(a.token)) == 1 {
			found = a
		}
	}
	return found
}

// asHeader makes an admin's request a viewer's: that token's progress and
// certification limits, so the admin sees and changes exactly what it has.
const asHeader = "X-Mvd-As"

// --- who can use which route ---

type level int

const (
	viewer level = iota // any token
	admin               // the [server] token only
	public              // no token needed
)

// routeLevels is every pattern the server registers (in any build), and
// who may use it. A pattern that isn't here is refused: see auth.
var routeLevels = map[string]level{
	"GET /login":       public,
	"POST /login":      public,
	"GET /static/":     public,
	"GET /favicon.ico": public,
	"POST /logout":     public,

	// Pairing: a device asks, and collects; anyone with a token lets it
	// in, as themselves (pair.go). What's waiting is the admin's to see.
	"POST /api/pair":                public,
	"GET /api/pair/{code}":          public,
	"GET /api/pair":                 admin,
	"POST /api/pair/{code}/approve": viewer,
	"DELETE /api/pair/{code}":       viewer,
	"GET /pair":                     viewer,
	"POST /ui/pair":                 viewer,

	"GET /api/status":               viewer,
	"GET /api/up-next":              viewer,
	"POST /api/up-next/{id}/remove": viewer,
	"GET /api/movies":               viewer,
	"GET /api/movies/{id}":          viewer,
	"GET /api/shows":                viewer,
	"GET /api/episodes/new":         viewer,
	"GET /api/shows/{id}":           viewer,
	"GET /api/item/{id}":            viewer,
	"GET /api/search":               viewer,
	"POST /api/progress/{id}":       viewer,
	"GET /stream/{id}":              viewer,
	"GET /subs/{id}/{n}":            viewer,
	"GET /art/{id}/{kind}":          viewer,
	"GET /{$}":                      viewer,
	"GET /movies":                   viewer,
	"GET /movies/{id}":              viewer,
	"GET /tv":                       viewer,
	"GET /tv/{id}":                  viewer,
	"GET /search":                   viewer,
	"GET /watch/{id}":               viewer,
	"GET /play/{file}":              viewer,
	"POST /ui/watched/{id}":         viewer,
	"POST /ui/progress/{id}/clear":  viewer,
	"POST /ui/up-next/{id}/remove":  viewer,

	"POST /api/rescan":                   admin,
	"POST /api/import":                   admin,
	"GET /api/import":                    admin,
	"GET /api/import/search":             admin,
	"GET /api/import/details":            admin,
	"GET /api/import/poster/{kind}/{id}": admin,
	"POST /api/import/choose":            admin,
	"POST /api/import/retry":             admin,
	"POST /api/import/trash":             admin,
	"GET /api/downloads":                 admin,
	"POST /api/downloads":                admin,
	"POST /api/downloads/{id}/resume":    admin,
	"POST /api/downloads/{id}/pause":     admin,
	"POST /api/downloads/{id}/verify":    admin,
	"DELETE /api/downloads/{id}":         admin,
	"POST /api/downloads/{id}/move":      admin,
	"GET /api/uploads":                   admin,
	"POST /api/uploads":                  admin,
	"GET /api/uploads/{id}":              admin,
	"PATCH /api/uploads/{id}":            admin,
	"DELETE /api/uploads/{id}":           admin,
	"GET /incoming":                      admin,
	"GET /ui/incoming":                   admin,
	"POST /ui/incoming/trash":            admin,
	"POST /ui/incoming/empty-trash":      admin,
	"POST /ui/import":                    admin,
	"DELETE /ui/uploads/{id}":            admin,
	"GET /ui/import/search":              admin,
	"GET /ui/import/details":             admin,
	"POST /ui/import/choose":             admin,
	"POST /ui/import/retry":              admin,
	"POST /ui/downloads":                 admin,
	"POST /ui/downloads/{id}/resume":     admin,
	"POST /ui/downloads/{id}/pause":      admin,
	"POST /ui/downloads/{id}/verify":     admin,
	"DELETE /ui/downloads/{id}":          admin,
	"POST /ui/downloads/{id}/trash":      admin,
	"POST /ui/downloads/{id}/move":       admin,
}

// auth works out each request's access from its credential and puts it
// in the request's context, then lets it through if its route allows that
// access. See credentialed for the credentials. A route not in
// routeLevels is refused (fail closed); a path that matches no route is
// left to the mux's 404.
func (sv *Server) auth(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		lv, known := routeLevels[pattern]
		if known && lv == public {
			mux.ServeHTTP(w, r)
			return
		}
		acc := sv.accesses[0] // with no token, everyone's
		if sv.cfg.Token != "" {
			if acc = sv.credentialed(w, r, pattern); acc == nil {
				return
			}
		}
		if as := r.Header.Get(asHeader); as != "" {
			if !acc.admin {
				http.Error(w, asHeader+" is for the admin's token", http.StatusForbidden)
				return
			}
			acc = sv.accessNamed(as)
			if acc == nil {
				http.Error(w, fmt.Sprintf("there's no token named %q; there's %s", as, sv.tokenNames()), http.StatusBadRequest)
				return
			}
		}
		switch {
		case pattern != "" && !known:
			http.Error(w, "refused: this route has no access level", http.StatusForbidden)
			return
		case lv == admin && !acc.admin:
			http.Error(w, "refused: this token can only watch", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessKey{}, acc)))
	})
}

// credentialed is the access a request's credential gives, or nil when it
// has answered the request itself (a 401, a 413, a 429). The credential is
// the first of these the request has; one that's there and wrong is a
// 401, and the next isn't tried:
//
//  1. Authorization: MVD1 (a signed request, from the Apple apps)
//  2. Authorization: Bearer <token>
//  3. ?token=<token>
//  4. ?k=…&sig=… (a signed link), on the media routes only
//  5. the website's mvd_session cookie
//
// Wrong ones are counted by address (guesses), and an address that's
// made too many waits, whatever it presents.
func (sv *Server) credentialed(w http.ResponseWriter, r *http.Request, pattern string) *access {
	addr, now := sv.clientAddr(r), sv.clock()
	h, q := r.Header.Get("Authorization"), r.URL.Query()
	signed := strings.HasPrefix(h, requestScheme+" ")
	bearer := strings.HasPrefix(h, "Bearer ")
	link := linkRoutes[pattern] && (q.Has("k") || q.Has("sig"))
	cookie, _ := r.Cookie(sessionCookie)
	if !signed && !bearer && !q.Has("token") && !link && cookie == nil {
		sv.unauthorized(w, r, "token needed")
		return nil
	}
	if wait := sv.guesses.wait(addr, now); wait > 0 {
		tooMany(w, wait)
		return nil
	}

	var acc *access
	switch {
	case signed:
		var stale, tooBig bool
		acc, stale, tooBig = sv.signedRequest(r, h[len(requestScheme)+1:], now)
		switch {
		case tooBig:
			http.Error(w, fmt.Sprintf("a signed request's body can be at most %d KB: send this one with Bearer", formBodyMax>>10),
				http.StatusRequestEntityTooLarge)
			return nil
		case stale:
			// Not a guess: the signature was good. The client takes the
			// server's time from Date, and signs again.
			w.Header().Set("Date", now.UTC().Format(http.TimeFormat))
			w.Header().Set("WWW-Authenticate", requestScheme+` error="stale"`)
			http.Error(w, "the request's time is too far from the server's: sign it again with the time in Date", http.StatusUnauthorized)
			return nil
		}
	case bearer:
		acc = sv.match(h[len("Bearer "):])
	case q.Has("token"):
		acc = sv.match(q.Get("token"))
	case link:
		target, kid, sig := linkTarget(r)
		if a := sv.kids[kid]; a != nil && hmac.Equal([]byte(sig), []byte(linkSig(a.link, target))) {
			acc = a
		}
	default:
		kid, _, _ := strings.Cut(cookie.Value, ".")
		if a := sv.kids[kid]; a != nil && hmac.Equal([]byte(cookie.Value), []byte(a.cookie)) {
			acc = a
		} else {
			// A browser with an old cookie (the token changed): forget it,
			// so signing in again isn't counted against it page by page.
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
		}
	}
	if acc == nil {
		sv.guesses.fail(addr, now)
		sv.unauthorized(w, r, "wrong token")
		return nil
	}
	sv.guesses.ok(addr)
	return acc
}

// signedRequest checks an Authorization: MVD1 header's value: the access
// whose request key signed it, or nil. stale is a good signature with a
// time outside the window; tooBig a body too big to sign.
func (sv *Server) signedRequest(r *http.Request, v string, now time.Time) (acc *access, stale, tooBig bool) {
	kid, t, sig, ok := parseMVD1(v)
	a := sv.kids[kid]
	if !ok || a == nil {
		return nil, false, false
	}
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, formBodyMax+1))
		if err != nil || len(b) > formBodyMax {
			return nil, false, true
		}
		body = b
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	target := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	want := requestSig(a.request, r.Method, target, t, sha256.Sum256(body))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return nil, false, false
	}
	if !fresh(t, now) {
		return nil, true, false
	}
	return a, false, false
}

// unauthorized answers a request without a good credential: a browser's
// page goes to sign in (and comes back after), anything else is a 401
// that says the server knows signed requests.
func (sv *Server) unauthorized(w http.ResponseWriter, r *http.Request, msg string) {
	if r.Method == http.MethodGet && isPage(r.URL.Path) {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	w.Header().Set("WWW-Authenticate", requestScheme)
	http.Error(w, msg, http.StatusUnauthorized)
}

// accessNamed is the viewer's access with that name, in any case; nil if
// there's none.
func (sv *Server) accessNamed(name string) *access {
	for _, a := range sv.accesses[1:] {
		if strings.EqualFold(a.name, name) {
			return a
		}
	}
	return nil
}

// tokenNames lists the viewers' names, for a message.
func (sv *Server) tokenNames() string {
	var names []string
	for _, a := range sv.accesses[1:] {
		names = append(names, fmt.Sprintf("%q", a.name))
	}
	if len(names) == 0 {
		return "none"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// --- what an access can see ---

// shelf is the library as one access sees it: anything with a
// certification it isn't allowed is missing, as if it weren't there. The
// views and lists a request answers with are built from it, with that
// access's progress, and with media paths signed for its token.
type shelf struct {
	sv   *Server
	ix   *library.Index
	acc  *access
	sign *linkSigner // nil: paths as they are
}

// shelf is the request's view of the library, for the API: every media
// path in it (artwork, streams, subtitles) is a signed link for the
// request's token, when there is one, for a client to use as it is.
func (sv *Server) shelf(r *http.Request) shelf {
	s := sv.pageShelf(r)
	if s.acc.kid != "" {
		s.sign = &linkSigner{kid: s.acc.kid, key: s.acc.link}
	}
	return s
}

// pageShelf is the request's view of the library for the website's pages,
// whose media paths aren't signed: the browser has the cookie, and a page
// of posters needn't be a page of signatures.
func (sv *Server) pageShelf(r *http.Request) shelf {
	return shelf{sv: sv, ix: sv.ix.Load(), acc: sv.who(r)}
}

// link is a media path as this shelf hands it out.
func (s shelf) link(path string) string {
	if s.sign == nil {
		return path
	}
	return s.sign.sign(path)
}

func (s shelf) state() *State { return s.acc.state }

func (s shelf) Movie(id string) *library.Movie {
	if m := s.ix.Movie(id); m != nil && s.acc.allowed(m.Rating) {
		return m
	}
	return nil
}

func (s shelf) Show(id string) *library.Show {
	if sh := s.ix.Show(id); sh != nil && s.acc.allowed(sh.Rating) {
		return sh
	}
	return nil
}

// Episode goes by its show's certification: an episode has none of its own.
func (s shelf) Episode(id string) *library.Episode {
	if e := s.ix.Episode(id); e != nil && s.acc.allowed(e.Show.Rating) {
		return e
	}
	return nil
}

// Movies is the movies this access sees, by title.
func (s shelf) Movies() []*library.Movie {
	if s.acc.ratings == nil {
		return s.ix.Movies
	}
	var out []*library.Movie
	for _, m := range s.ix.Movies {
		if s.acc.allowed(m.Rating) {
			out = append(out, m)
		}
	}
	return out
}

// Shows is the shows this access sees, by title.
func (s shelf) Shows() []*library.Show {
	if s.acc.ratings == nil {
		return s.ix.Shows
	}
	var out []*library.Show
	for _, sh := range s.ix.Shows {
		if s.acc.allowed(sh.Rating) {
			out = append(out, sh)
		}
	}
	return out
}

// counts is the library's size, as this access sees it.
func (s shelf) counts() libraryCounts {
	if s.acc.ratings == nil {
		return libraryCounts{len(s.ix.Movies), len(s.ix.Shows), s.ix.EpisodeCount(), s.ix.Bytes()}
	}
	var c libraryCounts
	for _, m := range s.Movies() {
		c.Movies++
		c.Bytes += m.Bytes
	}
	for _, sh := range s.Shows() {
		c.Shows++
		for _, se := range sh.Seasons {
			c.Episodes += len(se.Episodes)
			for _, e := range se.Episodes {
				c.Bytes += e.Bytes
			}
		}
	}
	return c
}
