//go:build webui

package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestUIPages(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	ep := sh.Seasons[0].Episodes[0].ID

	for path, want := range map[string]string{
		"/":                    "Up Next",
		"/movies":              "The Godfather",
		"/movies/m238":         `class="hero-in with-poster"`,
		"/tv":                  "Severance",
		"/tv/s95396":           "Half Loop",
		"/tv/s95396?season=0":  "Inside Severance",
		"/watch/m238":          `src="/subs/m238/0.vtt"`,
		"/watch/" + ep:         "Next: S01E02 Half Loop",
		"/static/style.css":    "--mvd-gold",
		"/static/htmx.min.js":  "htmx",
		"/static/home.js":      "aria-selected",
		"/static/favicon.svg":  "#d4af37",
		"/static/mvd-logo.svg": `aria-label="mvd"`,
		"/favicon.ico":         "\x00\x00\x01\x00",
	} {
		r := ts.do("GET", path, "", nil)
		if b := body(t, r); r.StatusCode != 200 || !strings.Contains(b, want) {
			t.Errorf("%s: %d, missing %q", path, r.StatusCode, want)
		}
	}
	// The top bar's name is the gold logo, as in the apps and on potato.video.
	if b := body(t, ts.do("GET", "/", "", nil)); !strings.Contains(b, `<img src="/static/mvd-logo.svg`) {
		t.Errorf("top bar has no logo:\n%s", b)
	}
	if r := ts.do("GET", "/movies/nope", "", nil); r.StatusCode != 404 {
		t.Errorf("missing movie: %d", r.StatusCode)
	}
	// The library pages: the grid, no heading or search (the nav says where).
	if b := body(t, ts.do("GET", "/movies", "", nil)); strings.Contains(b, `class="search"`) || strings.Contains(b, "<h1>") {
		t.Errorf("library page still has a heading or search:\n%s", b)
	}

	// The watched toggle answers with the page it's on, re-rendered.
	r := ts.do("POST", "/ui/watched/m238", "watched=1", map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "HX-Current-URL": ts.hs.URL + "/movies/m238"})
	if b := body(t, r); !strings.Contains(b, `aria-pressed="true"`) || !ts.sv.state.Get("m238").Watched {
		t.Errorf("toggle: %s", b)
	}

	// The playlist points a real player at the stream, with subtitles.
	r = ts.do("GET", "/play/m238.m3u", "", nil)
	b := body(t, r)
	if !strings.Contains(b, "#EXTVLCOPT:sub-file="+ts.hs.URL+"/subs/m238/0") || !strings.Contains(b, ts.hs.URL+"/stream/m238") ||
		!strings.Contains(r.Header.Get("Content-Disposition"), "The Godfather (1972).m3u") {
		t.Errorf("m3u:\n%s", b)
	}
}

func TestUISignIn(t *testing.T) {
	ts := newTestServer(t, "sekrit")
	defer ts.hs.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	r, _ := noFollow.Get(ts.hs.URL + "/movies")
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/login?next=%2Fmovies" {
		t.Errorf("page without token: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r, _ := noFollow.Get(ts.hs.URL + "/static/style.css"); r.StatusCode != 200 {
		t.Errorf("static should be open: %d", r.StatusCode)
	}
	if r, _ := noFollow.Get(ts.hs.URL + "/api/movies"); r.StatusCode != 401 {
		t.Errorf("api without token: %d", r.StatusCode)
	}
	r, _ = noFollow.PostForm(ts.hs.URL+"/login", url.Values{"token": {"wrong"}})
	if r.StatusCode != 401 || !strings.Contains(body(t, r), "isn't right") {
		t.Errorf("wrong token: %d", r.StatusCode)
	}
	r, _ = noFollow.PostForm(ts.hs.URL+"/login", url.Values{"token": {"sekrit"}})
	if r.StatusCode != http.StatusSeeOther || len(r.Cookies()) != 2 || r.Cookies()[0].Name != sessionCookie {
		t.Fatalf("sign in: %d %v", r.StatusCode, r.Cookies())
	}
	if strings.Contains(r.Cookies()[0].Value, "sekrit") || r.Cookies()[1].Name != tokenCookie || r.Cookies()[1].MaxAge >= 0 {
		t.Errorf("the cookies: %v", r.Cookies())
	}
	req, _ := http.NewRequest("GET", ts.hs.URL+"/movies", nil)
	req.AddCookie(r.Cookies()[0])
	if r, _ := noFollow.Do(req); r.StatusCode != 200 {
		t.Errorf("with cookie: %d", r.StatusCode)
	}
	// The m3u's links are signed so VLC can stream, and don't have the token.
	req, _ = http.NewRequest("GET", ts.hs.URL+"/play/m238.m3u", nil)
	req.AddCookie(r.Cookies()[0])
	if b := body(t, mustDo(t, noFollow, req)); !strings.Contains(b, "/stream/m238?k="+deriveKeys("sekrit").kid+"&sig=") ||
		strings.Contains(b, "sekrit") {
		t.Errorf("m3u:\n%s", b)
	}
}

// Behind an HTTPS reverse proxy the sign-in cookie is Secure, so the token
// never travels over plain HTTP; served plainly (the LAN), it can't be.
func TestUISignInSecureCookie(t *testing.T) {
	ts := newTestServer(t, "sekrit")
	defer ts.hs.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	signIn := func(proto string) *http.Cookie {
		req, _ := http.NewRequest("POST", ts.hs.URL+"/login", strings.NewReader("token=sekrit"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if proto != "" {
			req.Header.Set("X-Forwarded-Proto", proto)
		}
		r, err := noFollow.Do(req)
		if err != nil || len(r.Cookies()) == 0 {
			t.Fatalf("sign in (%s): %v %v", proto, err, r)
		}
		return r.Cookies()[0]
	}
	if c := signIn("https"); !c.Secure {
		t.Errorf("behind HTTPS: cookie not Secure")
	}
	if c := signIn(""); c.Secure {
		t.Errorf("plain HTTP: cookie Secure, so the browser would never send it back")
	}
}

// The logo carries the server's build, to hover and copy.
func TestUIVersion(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	old := Version
	Version = "v1.2.3-4-gabcdef0"
	defer func() { Version = old }()
	r := ts.do("GET", "/movies", "", nil)
	if h := body(t, r); !strings.Contains(h, `data-version="mvd v1.2.3-4-gabcdef0"`) || r.Header.Get("X-Mvd-Version") != Version {
		t.Errorf("no version: %v", r.Header)
	}
}

func TestShort(t *testing.T) {
	for sec, want := range map[float64]string{59: "0m", 300: "5m", 3600: "1h", 3660: "1h 1m", 7020: "1h 57m", 7200: "2h"} {
		if got := short(sec); got != want {
			t.Errorf("short(%v) = %q, want %q", sec, got, want)
		}
	}
}

// timeLeft, as the Apple apps work it out: the runtime is the length, else
// the length played; part-way, what's left is that length less the share
// watched.
func TestTimeLeft(t *testing.T) {
	for _, c := range []struct {
		pos, dur float64
		runtime  int
		want     string
	}{
		{0, 0, 0, ""},
		{0, 2820, 0, "47m"},
		{0, 2820, 48, "48m"},
		{1410, 2820, 48, "24m left"},
		{1410, 2820, 0, "23m left"},
		{2820, 2820, 48, "48m"},
	} {
		if got := timeLeft(c.pos, c.dur, c.runtime); got != c.want {
			t.Errorf("timeLeft(%v, %v, %d) = %q, want %q", c.pos, c.dur, c.runtime, got, c.want)
		}
	}
}

// Up Next's pills, as the Apple apps': a show's new entry says how many
// ("2 new"); a show you're watching with new episodes waiting gets a red
// pill beside its reason, in the row and the feature.
func TestUIHomePills(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	h := body(t, ts.do("GET", "/", "", nil))
	if !strings.Contains(h, `<span class="pill new">2 new</span>`) || !strings.Contains(h, `<span class="pill new">New</span>`) {
		t.Errorf("fresh library:\n%s", h)
	}

	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	ts.do("POST", "/api/progress/"+sh.Seasons[0].Episodes[0].ID, `{"watched": true}`, nil).Body.Close()
	h = body(t, ts.do("GET", "/", "", nil))
	pair := `<span class="pills"><span class="pill next">Next episode</span><span class="pill new">1 new</span></span>`
	if strings.Count(h, pair) != 2 {
		t.Errorf("next episode with one new: want the two pills in the row and the feature:\n%s", h)
	}
}

// The home page: Up Next as a list, the first entry featured beside it;
// every entry labelled with why it's there; "All caught up" when empty.
func TestUIHome(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	page := func() string {
		r := ts.do("GET", "/", "", nil)
		if r.StatusCode != 200 {
			t.Fatalf("home: %d", r.StatusCode)
		}
		return body(t, r)
	}

	h := page()
	if strings.Count(h, `role="option"`) != 3 || strings.Count(h, `class="feature"`) != 3 ||
		strings.Count(h, `class="feature" id=`) != 3 || strings.Count(h, " hidden>") != 2 ||
		!strings.Contains(h, "2 new episodes") || strings.Contains(h, "New movies") {
		t.Errorf("fresh library:\n%s", h)
	}

	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	ts.do("POST", "/api/progress/"+sh.Seasons[0].Episodes[0].ID, `{"watched": true}`, nil).Body.Close()
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	h = page()
	for _, want := range []string{`class="pill continue">Continue watching<`, `class="pill next">Next episode<`,
		`class="pill new">New<`, `>Resume</a>`, `>Remove from Up Next</button>`, `class="playurl" href="` + ts.hs.URL + `/play/m238.m3u"`, `class="line">S01E02 · Half Loop<`, `<div class="qsub">1972 · 1h 53m left</div>`, `class="progress"`} {
		if !strings.Contains(h, want) {
			t.Errorf("after watching: lacks %s", want)
		}
	}
	// The part-watched movie is first, so featured.
	if i := strings.Index(h, `class="feature" id="f-up-m238">`); i < 0 {
		t.Errorf("the movie should be featured")
	}

	ts.sv.now = func() time.Time { return time.Now().Add(40 * 24 * time.Hour) }
	ts.do("POST", "/api/progress/m238", `{"watched": true}`, nil).Body.Close()
	ts.do("POST", "/api/progress/s95396", `{"watched": true}`, nil).Body.Close()
	if h = page(); !strings.Contains(h, "All caught up") {
		t.Errorf("nothing left:\n%s", h)
	}
}

// No "Open in a player" buttons: the .m3u's address, shown as a link, on
// the movie page, under each episode, and on the watch page.
func TestUIPlayLinks(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	link := func(id string) string {
		u := ts.hs.URL + "/play/" + id + ".m3u"
		return `href="` + u + `"` + ` hx-boost="false">` + u + `</a>`
	}
	h := body(t, ts.do("GET", "/movies/m238", "", nil))
	if !strings.Contains(h, link("m238")) || strings.Contains(h, "Open in a player") {
		t.Errorf("movie page:\n%s", h)
	}
	h = body(t, ts.do("GET", "/tv/s95396", "", nil))
	for _, e := range sh.Seasons[0].Episodes {
		if !strings.Contains(h, link(e.ID)) {
			t.Errorf("no link for %s", e.ID)
		}
	}
	if strings.Contains(h, "In a player") || strings.Contains(h, "Open in a player") {
		t.Errorf("show page still has player buttons")
	}
	if r := ts.do("GET", "/play/"+sh.Seasons[0].Episodes[0].ID+".m3u", "", nil); r.StatusCode != 200 ||
		!strings.Contains(body(t, r), "/stream/"+sh.Seasons[0].Episodes[0].ID) {
		t.Errorf("episode m3u: %d", r.StatusCode)
	}
	// With a token, the link is signed (a player can't sign in), and the
	// page has no token in it.
	tok := newTestServer(t, "sekrit")
	defer tok.hs.Close()
	h = body(t, tok.do("GET", "/movies/m238?token=sekrit", "", nil))
	if !strings.Contains(h, "/play/m238.m3u?k="+deriveKeys("sekrit").kid+"&amp;sig=") || strings.Contains(h, "sekrit") {
		t.Errorf("the link isn't signed:\n%s", h)
	}
}

// On the home page a row plays (via its data-play) and the featured title
// opens the movie or show; in progress, the strip along the bottom clears it.
func TestUIHomeCards(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1 := sh.Seasons[0].Episodes[0].ID
	ts.do("POST", "/api/progress/m238", `{"position": 300, "duration": 5520}`, nil).Body.Close()
	h := body(t, ts.do("GET", "/", "", nil))
	for _, want := range []string{
		`data-play="/watch/m238"`, `<a href="/movies/m238">The Godfather</a>`, `src="/art/m238/fanart"`,
		`data-play="/watch/` + e1 + `"`, `<a href="/tv/s95396?season=1">Severance</a>`,
		`src="/art/s95396/fanart"`, // a show's backdrop is its fanart
		`hx-post="/ui/progress/m238/clear"`, `5m of 1h 32m`, `Clear progress`, `/static/home.js`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("home page lacks %s", want)
		}
	}
	if strings.Contains(h, "Clear progress?") {
		t.Errorf("there's no confirmation any more")
	}
	// An episode is described by its own synopsis, never the show's.
	if !strings.Contains(h, `<p class="plot">In which Good News About Hell</p>`) ||
		(sh.Plot != "" && strings.Contains(h, sh.Plot)) || strings.Contains(h, "x-card") {
		t.Errorf("episode synopsis, or a leftover ✕:\n%s", h)
	}
}

// Clearing progress and removing from Up Next answer with the page they
// came from, re-rendered, for htmx to take the changed part.
func TestUIClearAndRemove(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	from := func(page string) map[string]string { return map[string]string{"HX-Current-URL": ts.hs.URL + page} }

	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	if h := body(t, ts.do("GET", "/movies/m238", "", nil)); !strings.Contains(h, ">Resume</a>") ||
		!strings.Contains(h, `hx-post="/ui/progress/m238/clear"`) {
		t.Fatalf("movie page before clearing:\n%s", h)
	}
	r := ts.do("POST", "/ui/progress/m238/clear", "", from("/movies/m238"))
	h := body(t, r)
	if r.StatusCode != 200 || !strings.Contains(h, `id="hero"`) || !strings.Contains(h, ">Play</a>") || strings.Contains(h, ">Resume</a>") {
		t.Errorf("after clearing: %d\n%s", r.StatusCode, h)
	}

	// Up Next: a show's next episode, removed, hides the show.
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1, e2 := sh.Seasons[0].Episodes[0].ID, sh.Seasons[0].Episodes[1].ID
	ts.do("POST", "/api/progress/"+e1, `{"watched": true}`, nil).Body.Close()
	if h := body(t, ts.do("GET", "/", "", nil)); !strings.Contains(h, `hx-post="/ui/up-next/`+e2+`/remove"`) {
		t.Fatalf("no remove ✕ on the next episode:\n%s", h)
	}
	h = body(t, ts.do("POST", "/ui/up-next/"+e2+"/remove", "", from("/")))
	if !strings.Contains(h, `id="home"`) || strings.Contains(h, `data-key="up-`+e2+`"`) {
		t.Errorf("home after removing:\n%s", h)
	}

	// Without a page to re-render, htmx is told to reload.
	r = ts.do("POST", "/ui/progress/m238/clear", "", nil)
	if r.StatusCode != http.StatusNoContent || r.Header.Get("HX-Refresh") != "true" {
		t.Errorf("no HX-Current-URL: %d %v", r.StatusCode, r.Header)
	}
	if r := ts.do("POST", "/ui/progress/nope/clear", "", from("/")); r.StatusCode != 404 {
		t.Errorf("unknown id: %d", r.StatusCode)
	}
}

// inOrder says whether every one of wants is in h, each after the last.
func inOrder(h string, wants ...string) bool {
	for _, w := range wants {
		i := strings.Index(h, w)
		if i < 0 {
			return false
		}
		h = h[i+len(w):]
	}
	return true
}

// The movie page is the Apple TV's hero: the poster, the title, the facts,
// the plot, then how far in, Play and the watched toggle, and the .m3u
// link last. No credits. The player: a centred title with a ✕.
func TestUIMovieAndPlayerLayout(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	h := body(t, ts.do("GET", "/movies/m238", "", nil))
	if !inOrder(h, `<section class="hero full" id="hero">`, `class="poster"`, `<h1>The Godfather&#8288;<span class="cert">15</span></h1>`,
		`<div class="facts">1972 · 2h<span class="genres"> · Drama</span></div>`,
		`class="progress"`, `hx-target="#hero"`, `1h 53m left`, `<a class="btn play" href="/watch/m238" hx-boost="false">Resume</a>`,
		`Mark as Watched`, `class="playurl"`) {
		t.Errorf("movie page:\n%s", h)
	}
	if strings.Contains(h, "Directed by") || strings.Contains(h, "clearband") {
		t.Errorf("credits, or the poster's clear strip, still there")
	}
	h = body(t, ts.do("GET", "/watch/m238", "", nil))
	if !strings.Contains(h, `class="close" href="/movies/m238"`) || strings.Contains(h, ">Back<") {
		t.Errorf("player bar:\n%s", h)
	}
}

// The facts start with the language a title was made in when it isn't the
// viewer's (the first of their Accept-Language, English without one):
// languages, not regions, so en-GB doesn't see "English" on an en film.
func TestUIOriginalLanguage(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var m movieView
	ts.json("GET", "/api/movies/m238", "", &m)
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	if m.OriginalLanguage != "en" || sh.OriginalLanguage != "en" || sh.Seasons[0].Episodes[0].OriginalLanguage != "en" {
		t.Errorf("API: movie %q, show %q", m.OriginalLanguage, sh.OriginalLanguage)
	}
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	for _, c := range []struct{ accept, movie, show, home string }{
		{"", `<div class="facts">1972 · 2h<`, `<div class="facts">2022 · 1 season`, `<div class="facts">1972 · 2h<`},
		{"en-GB,da;q=0.8", `<div class="facts">1972 · 2h<`, `<div class="facts">2022 · 1 season`, `<div class="facts">1972 · 2h<`},
		{"da-DK, en;q=0.5", `<div class="facts">English · 1972 · 2h<`, `<div class="facts">English · 2022 · 1 season`, `<div class="facts">English · 1972 · 2h<`},
	} {
		hdr := map[string]string{}
		if c.accept != "" {
			hdr["Accept-Language"] = c.accept
		}
		for page, want := range map[string]string{"/movies/m238": c.movie, "/tv/s95396": c.show, "/": c.home} {
			if h := body(t, ts.do("GET", page, "", hdr)); !strings.Contains(h, want) {
				t.Errorf("%s with Accept-Language %q: no %s", page, c.accept, want)
			}
		}
	}
}

// A movie in Up Next is subtitled with its year and how long's left; an
// episode not started, with how long it is.
func TestUIHomeMovieSubtitle(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	h := body(t, ts.do("GET", "/", "", nil))
	row := h[strings.Index(h, `id="c-up-m238"`):]
	row = row[:strings.Index(row, `</li>`)]
	if strings.Contains(h, "10:00 in") || !strings.Contains(row, `<div class="qsub">1972 · 1h 53m left</div>`) {
		t.Errorf("Up Next movie row:\n%s", row)
	}
}

// The show page: the hero with Play or Resume for the next episode, its
// certificate after the title; episodes' details with " · ", a Watched
// pill, and a toggle that redraws the row and the hero (the next episode
// moves on).
func TestUIShowPage(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1, e2 := sh.Seasons[0].Episodes[0].ID, sh.Seasons[0].Episodes[1].ID
	if sh.Seasons[0].Number != 1 {
		e1, e2 = sh.Seasons[1].Episodes[0].ID, sh.Seasons[1].Episodes[1].ID
	}

	h := body(t, ts.do("GET", "/tv/s95396", "", nil))
	if !inOrder(h, `<section class="hero" id="hero">`, `<h1>Severance&#8288;<span class="cert">15</span></h1>`,
		`<div class="facts">2022 · 1 season`, `<span class="genres"> · Drama, Mystery</span>`,
		`href="/watch/`+e1+`" hx-boost="false">Play S01E01</a>`, `class="playurl"`, `class="seasons"`) {
		t.Errorf("show page:\n%s", h)
	}
	// A season tab swaps the tabs too, or the one picked isn't marked.
	if !strings.Contains(h, `<nav class="seasons" id="seasons"`) || !strings.Contains(h, `hx-select="#episodes" hx-select-oob="#seasons"`) {
		t.Errorf("season tabs don't swap themselves:\n%s", h)
	}

	ts.do("POST", "/api/progress/"+e1, `{"position": 300, "duration": 3000}`, nil).Body.Close()
	h = body(t, ts.do("GET", "/tv/s95396", "", nil))
	if !inOrder(h, `hx-select-oob="#c-ep-`+e1+`"`, `45m left`, `>Resume S01E01</a>`) ||
		!strings.Contains(h, `S01E01 · `) || !strings.Contains(h, ` · 45m left</div>`) || strings.Contains(h, ` in</div>`) {
		t.Errorf("show page part-way:\n%s", h)
	}

	r := ts.do("POST", "/ui/watched/"+e1, "watched=1", map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "HX-Current-URL": ts.hs.URL + "/tv/s95396?season=1"})
	h = body(t, r)
	row := h[strings.Index(h, `id="c-ep-`+e1+`"`):]
	row = row[:strings.Index(row, `</li>`)]
	if !strings.Contains(row, `<span class="pill corner">Watched</span>`) || !strings.Contains(row, `hx-select-oob="#hero"`) ||
		!strings.Contains(h, `href="/watch/`+e2+`" hx-boost="false">Play S01E02</a>`) {
		t.Errorf("after marking S01E01 watched:\n%s", h)
	}
}

// The grids, as the Apple TV's: a movie's year and length (or what's
// left), "new" or "watched" in the corner; a show's "N new" or "all
// watched". The nav is the tab bar's: Up Next, Movies, TV, then Incoming.
func TestUIGrids(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	h := body(t, ts.do("GET", "/movies", "", nil))
	if !inOrder(h, `>Up Next</a>`, `>Movies</a>`, `>TV</a>`) || strings.Contains(h, `href="/incoming"`) != incomingBuilt ||
		incomingBuilt && !strings.Contains(h, `>Incoming<span class="nav-count" id="nav-needs" hidden`) {
		t.Errorf("nav:\n%s", h)
	}
	card := func(h, id string) string {
		c := h[strings.Index(h, `id="c-l-`+id+`"`):]
		return c[:min(len(c), strings.Index(c, `class="name"`)+200)]
	}
	if c := card(h, "m238"); !strings.Contains(c, `<span class="pill corner new">new</span>`) {
		t.Errorf("new movie:\n%s", c)
	}
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	h = body(t, ts.do("GET", "/movies", "", nil))
	if c := card(h, "m238"); strings.Contains(c, "pill") || !strings.Contains(c, `<div class="sub">1972 · 1h 53m left</div>`) {
		t.Errorf("part-way movie:\n%s", c)
	}
	ts.do("POST", "/api/progress/m238", `{"watched": true}`, nil).Body.Close()
	h = body(t, ts.do("GET", "/movies", "", nil))
	if c := card(h, "m238"); !strings.Contains(c, `<span class="pill corner">watched</span>`) {
		t.Errorf("watched movie:\n%s", c)
	}

	h = body(t, ts.do("GET", "/tv", "", nil))
	if c := card(h, "s95396"); !strings.Contains(c, `<span class="pill corner new">2 new</span>`) {
		t.Errorf("new show:\n%s", c)
	}
	ts.sv.now = func() time.Time { return time.Now().Add(40 * 24 * time.Hour) }
	ts.do("POST", "/api/progress/s95396", `{"watched": true}`, nil).Body.Close()
	h = body(t, ts.do("GET", "/tv", "", nil))
	if c := card(h, "s95396"); !strings.Contains(c, `<span class="pill corner">all watched</span>`) {
		t.Errorf("watched show:\n%s", c)
	}
}

// A movie in parts has one Play, and the player goes on from part to part.
func TestUIMovieParts(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	m := ts.sv.ix.Load().Movie("m238")
	m.Files = append(m.Files, m.Files[0])
	h := body(t, ts.do("GET", "/movies/m238", "", nil))
	if strings.Count(h, `class="btn play"`) != 1 || strings.Contains(h, "part") {
		t.Errorf("movie in parts:\n%s", h)
	}
	h = body(t, ts.do("GET", "/watch/m238", "", nil))
	if !strings.Contains(h, `src="/stream/m238"`) || !strings.Contains(h, `data-parts="/stream/m238 /stream/m238?part=2" data-part="0"`) {
		t.Errorf("player, from the start:\n%s", h)
	}
	h = body(t, ts.do("GET", "/watch/m238?part=2", "", nil))
	if !strings.Contains(h, `src="/stream/m238?part=2"`) || !strings.Contains(h, `data-part="1"`) {
		t.Errorf("player, part 2:\n%s", h)
	}
}

// The show page plays the show's next episode as Up Next has it: part-way
// through S01E02 with S01E01 unwatched, "Resume S01E02"; a special part-way
// doesn't change it.
func TestUIShowPageNextAgrees(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e2, special := sh.Seasons[0].Episodes[1].ID, sh.Seasons[1].Episodes[0].ID
	ts.do("POST", "/api/progress/"+e2, `{"position": 60, "duration": 1800}`, nil).Body.Close()
	time.Sleep(10 * time.Millisecond)
	ts.do("POST", "/api/progress/"+special, `{"position": 60, "duration": 1800}`, nil).Body.Close()
	h := body(t, ts.do("GET", "/tv/s95396", "", nil))
	if !strings.Contains(h, `href="/watch/`+e2+`" hx-boost="false">Resume S01E02</a>`) {
		t.Errorf("show page:\n%s", h)
	}
}

// Each episode has its own Play (or Resume) with its code, before the
// watched button; the watched button is the TV's cornflower one, watched
// or not; the still offers to play on hover.
func TestUIEpisodeButtons(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1, e2 := sh.Seasons[0].Episodes[0].ID, sh.Seasons[0].Episodes[1].ID
	ts.do("POST", "/api/progress/"+e1, `{"position": 300, "duration": 3000}`, nil).Body.Close()
	h := body(t, ts.do("GET", "/tv/s95396", "", nil))
	row := func(id string) string {
		r := h[strings.Index(h, `id="c-ep-`+id+`"`):]
		return r[:strings.Index(r, `</li>`)]
	}
	if !inOrder(row(e1), `class="pic-link play"`, `<div class="acts">`, `<a class="btn play small" href="/watch/`+e1+`" hx-boost="false">Resume S01E01</a>`,
		`class="btn secondary small"`, `Mark as Watched`) {
		t.Errorf("part-way episode:\n%s", row(e1))
	}
	if !strings.Contains(row(e2), `>Play S01E02</a>`) {
		t.Errorf("unstarted episode:\n%s", row(e2))
	}
	ts.do("POST", "/api/progress/"+e2, `{"watched": true}`, nil).Body.Close()
	h = body(t, ts.do("GET", "/tv/s95396", "", nil))
	if r := row(e2); !strings.Contains(r, `class="btn secondary small" type="button" aria-pressed="true"`) || !strings.Contains(r, `Watched</button>`) {
		t.Errorf("watched episode:\n%s", r)
	}
}

// A title with a line break (from a hand-written NFO) can't add lines to
// a playlist: players would read them as more entries or options.
func TestM3UTitleOneLine(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	ts.sv.ix.Load().Movie("m238").Title = "The Godfather\n#EXTVLCOPT:sub-file=http://evil.example/x\r\nhttp://evil.example/v"
	b := body(t, ts.do("GET", "/play/m238.m3u", "", nil))
	if strings.Contains(b, "\n#EXTVLCOPT:sub-file=http://evil") || strings.Contains(b, "\nhttp://evil") {
		t.Errorf("m3u:\n%s", b)
	}
}

func mustDo(t *testing.T, c *http.Client, req *http.Request) *http.Response {
	t.Helper()
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
