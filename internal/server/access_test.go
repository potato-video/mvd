package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"mvd/internal/config"
)

// Every pattern the server registers, in any build, has an access level,
// and the table has none it doesn't register.
func TestRouteLevelsCoverRoutes(t *testing.T) {
	route := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`)
	files, _ := filepath.Glob("*.go")
	var registered []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		for _, m := range route.FindAllStringSubmatch(string(src), -1) {
			if !slices.Contains(registered, m[1]) {
				registered = append(registered, m[1])
			}
		}
	}
	for _, r := range registered {
		if _, ok := routeLevels[r]; !ok {
			t.Errorf("%s has no access level in routeLevels", r)
		}
	}
	for p := range routeLevels {
		if !slices.Contains(registered, p) {
			t.Errorf("routeLevels has %s, which the server doesn't register", p)
		}
	}
	if len(registered) < 40 {
		t.Errorf("found only %d routes: has the registration code changed shape?", len(registered))
	}
}

// A route missing from the table is refused, whatever the token.
func TestUnlistedRouteRefused(t *testing.T) {
	sv := &Server{cfg: config.Config{}}
	sv.setupAccess()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /secret", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "secret") })
	hs := httptest.NewServer(sv.auth(mux))
	defer hs.Close()
	for path, want := range map[string]int{"/secret": 403, "/nothing": 404} {
		resp, err := http.Get(hs.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestAllowed(t *testing.T) {
	all := &access{}
	kids := &access{ratings: map[string]bool{"u": true, "pg": true}}
	kidsUnrated := &access{ratings: map[string]bool{"u": true}, unrated: true}
	none := &access{ratings: map[string]bool{}}
	for _, c := range []struct {
		a      *access
		rating string
		want   bool
	}{
		{all, "18", true}, {all, "", true},
		{kids, "U", true}, {kids, " pg ", true}, {kids, "12A", false}, {kids, "", false},
		{kidsUnrated, "", true}, {kidsUnrated, "PG", false},
		{none, "U", false}, {none, "", false},
	} {
		if got := c.a.allowed(c.rating); got != c.want {
			t.Errorf("%+v allowed(%q) = %v", c.a.ratings, c.rating, got)
		}
	}
}

const (
	adminTok = "admin-token-0123456789"
	kidsTok  = "kids-token-0123456789"
	denTok   = "den-token-0123456789"
)

// newTokenServer is the test library with the admin's token and two
// viewers: "kids", who sees only U and PG and things with no certification
// (here, only the unscraped movie), and "den", who sees everything.
func newTokenServer(t *testing.T) *testServer {
	movies, tv := testLibrary(t)
	dir := t.TempDir()
	st, _ := loadState(filepath.Join(dir, "state.json"))
	cfg := config.Config{MoviesDir: movies, TVDir: tv, Token: adminTok, DataDir: t.TempDir(),
		StateFile: filepath.Join(dir, "state.json"),
		Tokens: []config.Token{
			{Name: "kids", Token: kidsTok, Ratings: []string{"U", "PG"}, Limited: true, Unrated: true},
			{Name: "Den", Token: denTok},
		}}
	sv := &Server{cfg: cfg, state: st}
	sv.rescan()
	sv.waitProbing()
	return &testServer{t, httptest.NewServer(sv.routes()), sv}
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func (ts *testServer) body(method, path, body string, hdr map[string]string) (int, string) {
	resp := ts.do(method, path, body, hdr)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Everything hidden from a token is a 404, by every route that takes an
// id, and missing from every list.
func TestHiddenByCertification(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	hidden := []string{"m238", "s95396", "e95396101", "e95396001"}

	// Every viewer route with an id in it, driven from the table, so a new
	// route is covered or this fails.
	tried := 0
	for pattern, lv := range routeLevels {
		if lv != viewer || !strings.Contains(pattern, "{") {
			continue
		}
		if strings.Contains(pattern, "{code}") {
			continue // a pairing code, not something in the library
		}
		method, path, _ := strings.Cut(pattern, " ")
		seen := false
		for _, id := range hidden {
			kind := "poster"
			if id[0] == 'e' {
				kind = "thumb"
			}
			p := strings.NewReplacer("{id}", id, "{file}", id+".m3u", "{n}", "0", "{kind}", kind).Replace(path)
			body := ""
			if method == "POST" {
				body = `{"watched": true}`
			}
			if code, b := ts.body(method, p, body, bearer(kidsTok)); code != 404 {
				t.Errorf("kids: %s %s: %d %s", method, p, code, b)
			}
			// The same for the admin, where it's built: not hidden.
			req := httptest.NewRequest(method, p, nil)
			if _, got := ts.sv.mux.Handler(req); got != pattern {
				continue
			}
			if code, _ := ts.body(method, p, body, bearer(adminTok)); code != 404 {
				seen = true
			}
		}
		req := httptest.NewRequest(method, strings.NewReplacer("{id}", "x", "{file}", "x", "{n}", "0", "{kind}", "x").Replace(path), nil)
		if _, got := ts.sv.mux.Handler(req); got == pattern {
			tried++
			if !seen {
				t.Errorf("%s: the admin can't reach any of %v either, so this proves nothing", pattern, hidden)
			}
		}
	}
	if tried < 8 {
		t.Errorf("only %d routes with ids tried", tried)
	}

	// The lists.
	for _, p := range []string{"/api/movies", "/api/shows", "/api/up-next", "/api/episodes/new", "/api/episodes/new?since=0",
		"/api/movies?since=0", "/", "/movies", "/tv"} {
		code, b := ts.body("GET", p, "", bearer(kidsTok))
		if code == 404 && !strings.HasPrefix(p, "/api/") {
			continue // no website in this build
		}
		if code != 200 {
			t.Errorf("kids: %s: %d", p, code)
		}
		for _, id := range append(hidden, "Godfather", "Severance") {
			if strings.Contains(b, id) {
				t.Errorf("kids: %s has %s:\n%s", p, id, b)
			}
		}
		if !strings.Contains(b, "Unscraped Thing") && p != "/api/shows" && p != "/tv" && !strings.Contains(p, "episodes") {
			t.Errorf("kids: %s doesn't have what it can see:\n%s", p, b)
		}
	}

	// The counts.
	var st statusView
	resp := ts.do("GET", "/api/status", "", bearer(kidsTok))
	decode(t, resp, &st)
	// Its size too: only the one movie's.
	var kidsBytes int64
	for _, m := range ts.sv.ix.Load().Movies {
		if m.Title == "Unscraped Thing" {
			kidsBytes = m.Bytes
		}
	}
	if kidsBytes == 0 || st.Library != (libraryCounts{Movies: 1, Bytes: kidsBytes}) || st.Access != (accessView{Name: "kids"}) {
		t.Errorf("kids' status: %+v %+v", st.Library, st.Access)
	}
	st = statusView{}
	decode(t, ts.do("GET", "/api/status", "", bearer(adminTok)), &st)
	if st.Library != (libraryCounts{2, 1, 3, ts.sv.ix.Load().Bytes()}) || st.Access != (accessView{Name: "admin", Admin: true}) {
		t.Errorf("admin's status: %+v %+v", st.Library, st.Access)
	}
}

// Without a token, everyone's the admin.
func TestStatusAccessOpen(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var st statusView
	ts.json("GET", "/api/status", "", &st)
	if st.Access != (accessView{Name: "", Admin: true}) {
		t.Errorf("%+v", st.Access)
	}
}

// A viewer can watch, but not upload, import or download.
func TestViewerRefused(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/rescan"}, {"POST", "/api/import"}, {"GET", "/api/import"}, {"GET", "/api/downloads"},
		{"GET", "/api/uploads"}, {"POST", "/api/uploads"}, {"GET", "/incoming"},
	} {
		if _, p := ts.sv.mux.Handler(httptest.NewRequest(c.method, c.path, nil)); p == "" {
			continue // not in this build
		}
		code, b := ts.body(c.method, c.path, "", bearer(denTok))
		if code != 403 || !strings.Contains(b, "can only watch") {
			t.Errorf("den: %s %s: %d %s", c.method, c.path, code, b)
		}
		if code, _ := ts.body(c.method, c.path, "", bearer(adminTok)); code == 403 || code == 401 {
			t.Errorf("admin: %s %s: %d", c.method, c.path, code)
		}
	}
	// What it can do, by each way of sending a token.
	if code, _ := ts.body("GET", "/api/movies?token="+denTok, "", nil); code != 200 {
		t.Errorf("?token=: %d", code)
	}
	if code, _ := ts.body("GET", "/api/movies", "", map[string]string{"Cookie": sessionCookie + "=" + deriveKeys(denTok).cookie}); code != 200 {
		t.Errorf("cookie: %d", code)
	}
	// A wrong Bearer isn't rescued by a right ?token=.
	if code, _ := ts.body("GET", "/api/movies?token="+denTok, "", bearer("wrong")); code != 401 {
		t.Errorf("wrong Bearer: %d", code)
	}
	if code, _ := ts.body("GET", "/api/movies", "", nil); code != 401 {
		t.Errorf("no token: %d", code)
	}
}

// Each token keeps its own progress, in its own file.
func TestProgressPerToken(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	if code, b := ts.body("POST", "/api/progress/m238", `{"position": 600, "duration": 7200}`, bearer(denTok)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	ts.body("POST", "/api/progress/e95396101", `{"watched": true}`, bearer(adminTok))

	position := func(tok, id string) (float64, bool) {
		var v map[string]any
		decode(t, ts.do("GET", "/api/item/"+id, "", bearer(tok)), &v)
		p, _ := v["position"].(float64)
		w, _ := v["watched"].(bool)
		return p, w
	}
	if p, _ := position(denTok, "m238"); p != 600 {
		t.Errorf("den's position: %v", p)
	}
	if p, _ := position(adminTok, "m238"); p != 0 {
		t.Errorf("admin has den's position: %v", p)
	}
	if _, w := position(denTok, "e95396101"); w {
		t.Errorf("den has the admin's watched")
	}
	if it := upNextEntryAs(ts, denTok, "m238"); it == nil || it["reason"] != "continue" {
		t.Errorf("den's Up Next: %+v", it)
	}
	if it := upNextEntryAs(ts, adminTok, "m238"); it != nil && it["reason"] == "continue" {
		t.Errorf("the admin's Up Next has den's: %+v", it)
	}

	ts.sv.flushAll()
	dir := filepath.Dir(ts.sv.cfg.StateFile)
	for _, f := range []string{"state.json", "state-den.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "state-den.json")); !strings.Contains(string(b), "m238") {
		t.Errorf("state-den.json: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "state.json")); strings.Contains(string(b), "m238") {
		t.Errorf("state.json has den's: %s", b)
	}
}

// The admin can act as a viewer: its progress, and its limits.
func TestActingAsViewer(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	as := func(tok, name string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + tok, asHeader: name}
	}
	// Changes go to the viewer's progress, not the admin's.
	if code, b := ts.body("POST", "/api/progress/m238", `{"position": 900, "duration": 7200}`, as(adminTok, "DEN")); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if ts.sv.state.Get("m238").Position != 0 || ts.sv.accessNamed("den").state.Get("m238").Position != 900 {
		t.Errorf("as den: admin %+v, den %+v", ts.sv.state.Get("m238"), ts.sv.accessNamed("den").state.Get("m238"))
	}
	// The viewer's limits apply.
	if code, _ := ts.body("GET", "/api/item/m238", "", as(adminTok, "kids")); code != 404 {
		t.Errorf("hidden, as kids: %d", code)
	}
	var st statusView
	decode(t, ts.do("GET", "/api/status", "", as(adminTok, "kids")), &st)
	if st.Access.Name != "kids" || st.Library.Movies != 1 {
		t.Errorf("status as kids: %+v", st)
	}
	// And so do its levels.
	if code, _ := ts.body("POST", "/api/rescan", "", as(adminTok, "kids")); code != 403 {
		t.Errorf("rescan as kids: %d", code)
	}
	// Not for a viewer; not for a name there isn't.
	if code, _ := ts.body("GET", "/api/movies", "", as(denTok, "kids")); code != 403 {
		t.Errorf("den as kids: %d", code)
	}
	if code, b := ts.body("GET", "/api/movies", "", as(adminTok, "nobody")); code != 400 || !strings.Contains(b, `"Den", "kids"`) {
		t.Errorf("as nobody: %d %s", code, b)
	}
}

func upNextEntryAs(ts *testServer, tok, id string) map[string]any {
	var up []map[string]any
	decode(ts.t, ts.do("GET", "/api/up-next", "", bearer(tok)), &up)
	for _, it := range up {
		if it["id"] == id || it["show_id"] == id {
			return it
		}
	}
	return nil
}

func decode(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: %d %s", resp.Request.URL, resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}
