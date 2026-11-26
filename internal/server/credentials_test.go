package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// The test vectors in docs/SERVER_DEVELOPMENT.md ("The formats"). The
// Apple apps' tests reproduce them too.
const vectorToken = "correct-horse-battery-staple-0123"

func TestKeyVectors(t *testing.T) {
	k := deriveKeys(vectorToken)
	for name, c := range map[string][2]string{
		"kid":        {k.kid, "e9ec59536a0993e9"},
		"linkKey":    {hex.EncodeToString(k.link), "a47ba02af92c7633c9ba7afa1ae0a8ebddc0adcd3bcf4d5b8ccf8fd5acc93c1c"},
		"requestKey": {hex.EncodeToString(k.request), "a00c2101f85eb91c2a50ee679b514a1d3151d6938c0d1d66225773bfa9a2bcb0"},
		"cookieKey":  {hex.EncodeToString(hmacSum([]byte(vectorToken), "mvd cookie v1")), "8b150cff8d8675e6e8afbe12236d34564a2f627b23d717609cd046aad726860a"},
		"cookie":     {k.cookie, "e9ec59536a0993e9.wDvNAP6ukgSHCPs4m32YMUzenzFJ6Ve6QPJd_5OjFug"},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %s, want %s", name, c[0], c[1])
		}
	}
	if got := linkSig(k.link, "/stream/abc123?part=2"); got != "8qQTFwxKREAY-S0A_w7W5A" {
		t.Errorf("link sig = %s", got)
	}
	if got := requestSig(k.request, "GET", "/api/movies?since=30d", "1794400000", sha256.Sum256(nil)); got != "DkIaVFZG9Ig2rqYgkk92ncldcCemKf43uuI0NPUg-uQ" {
		t.Errorf("GET request sig = %s", got)
	}
	body := []byte(`{"position":120,"duration":5400}`)
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != "a13660cae5de859f0564cacbc5ffe1cba6ac383015a0e438f4a6a4e2e4c8de23" {
		t.Errorf("body hash = %x", sum)
	}
	if got := requestSig(k.request, "POST", "/api/progress/abc123", "1794400000", sum); got != "dyHCfX1MegRvwWQnoRVuEaz4d7gRxwfx7hkFRnj9Nw0" {
		t.Errorf("POST request sig = %s", got)
	}
}

// linkTarget takes k= and sig= out of the query and keeps the rest, in order.
func TestLinkTarget(t *testing.T) {
	for raw, want := range map[string]string{
		"/stream/a":                       "/stream/a",
		"/stream/a?part=2":                "/stream/a?part=2",
		"/stream/a?part=2&k=x&sig=y":      "/stream/a?part=2",
		"/stream/a?k=x&sig=y":             "/stream/a",
		"/stream/a?k=x&part=2&sig=y&b=%2": "/stream/a?part=2&b=%2",
	} {
		r := httptest.NewRequest("GET", raw, nil)
		if got, _, _ := linkTarget(r); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
}

// signedLink is the shelf's way of signing: path + k + sig.
func signedPath(tok, path string) string {
	k := deriveKeys(tok)
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "k=" + k.kid + "&sig=" + linkSig(k.link, path)
}

func TestSignedLinks(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	code := func(method, path string) int {
		c, _ := ts.body(method, path, "", nil)
		return c
	}
	good := signedPath(denTok, "/stream/m238")
	for _, c := range []struct {
		what, method, path string
		want               int
	}{
		{"good", "GET", good, 200},
		{"HEAD, signed as GET", "HEAD", good, 200},
		{"art", "GET", signedPath(denTok, "/art/m238/poster"), 200},
		{"subtitles", "GET", signedPath(denTok, "/subs/m238/0"), 200},
		{"wrong", "GET", "/stream/m238?k=" + deriveKeys(denTok).kid + "&sig=8qQTFwxKREAY-S0A_w7W5A", 401},
		{"no sig", "GET", "/stream/m238?k=" + deriveKeys(denTok).kid, 401},
		{"another path", "GET", strings.Replace(good, "m238", "m603", 1), 401},
		{"part changed", "GET", strings.Replace(signedPath(denTok, "/stream/m238?part=2"), "part=2", "part=3", 1), 401},
		{"another token's kid", "GET", strings.Replace(good, deriveKeys(denTok).kid, deriveKeys(kidsTok).kid, 1), 401},
		{"a token that changed", "GET", signedPath("den-token-0123456789-old", "/stream/m238"), 401},
		{"on the API", "GET", signedPath(denTok, "/api/movies"), 401},
		{"hidden from its token", "GET", signedPath(kidsTok, "/stream/m238"), 404},
		{"the admin's", "GET", signedPath(adminTok, "/stream/m238"), 200},
	} {
		ts.sv.guesses.reset() // each wrong one counts
		if got := code(c.method, c.path); got != c.want {
			t.Errorf("%s: %s %s: %d, want %d", c.what, c.method, c.path, got, c.want)
		}
	}
	// A ?token= comes first, so an old app adding one to a signed link works.
	if got := code("GET", good+"&token="+kidsTok); got != 404 {
		t.Errorf("?token= on a signed link: %d, want kids' 404", got)
	}
}

// The API hands out signed media URLs, for the token that asked; with no
// token configured, nothing's signed.
func TestAPISignsURLs(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	var m movieView
	decode(t, ts.do("GET", "/api/item/m238", "", bearer(denTok)), &m)
	kid := deriveKeys(denTok).kid
	urls := append([]string{m.Poster, m.Fanart}, m.Streams...)
	for _, s := range m.Subs {
		urls = append(urls, s.URL, s.VTT)
	}
	for _, u := range urls {
		if u == "" {
			continue
		}
		if !strings.Contains(u, "k="+kid+"&sig=") {
			t.Errorf("not signed for den: %s", u)
		}
		if c, _ := ts.body("GET", u, "", nil); c != 200 {
			t.Errorf("%s: %d", u, c)
		}
	}
	if len(urls) < 4 {
		t.Errorf("only %v", urls)
	}

	var up []map[string]any
	decode(t, ts.do("GET", "/api/up-next", "", bearer(denTok)), &up)
	for _, it := range up {
		for _, k := range []string{"poster", "fanart", "thumb", "stream", "backdrop", "row_thumb", "shelf_image"} {
			if u, ok := it[k].(string); ok && !strings.Contains(u, "k="+kid) {
				t.Errorf("up next %s: %s", k, u)
			}
		}
	}

	open := newTestServer(t, "")
	defer open.hs.Close()
	var mo movieView
	open.json("GET", "/api/item/m238", "", &mo)
	if mo.Poster != "/art/m238/poster" || mo.Streams[0] != "/stream/m238" {
		t.Errorf("signed with no token: %+v", mo)
	}
}

// mvd1 is a signed request's Authorization header.
func mvd1(tok, method, target string, t int64, body string) string {
	k := deriveKeys(tok)
	ts := fmt.Sprint(t)
	return "MVD1 k=" + k.kid + ",t=" + ts + ",sig=" + requestSig(k.request, method, target, ts, sha256.Sum256([]byte(body)))
}

func TestSignedRequests(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	now := time.Unix(1794400000, 0)
	ts.sv.now = func() time.Time { return now }
	send := func(method, target, body, auth string) *http.Response {
		return ts.do(method, target, body, map[string]string{"Authorization": auth})
	}
	check := func(what string, resp *http.Response, want int) {
		t.Helper()
		ts.sv.guesses.reset() // each wrong one counts
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", what, resp.StatusCode, want)
		}
		if resp.StatusCode == 401 && !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "MVD1") {
			t.Errorf("%s: WWW-Authenticate %q", what, resp.Header.Get("WWW-Authenticate"))
		}
	}
	t0 := now.Unix()
	check("GET", send("GET", "/api/movies?since=0", "", mvd1(denTok, "GET", "/api/movies?since=0", t0, "")), 200)
	body := `{"position": 60, "duration": 600}`
	check("POST", send("POST", "/api/progress/m238", body, mvd1(denTok, "POST", "/api/progress/m238", t0, body)), 200)
	if ts.sv.accessNamed("den").state.Get("m238").Position != 60 {
		t.Error("the POST wasn't den's")
	}
	check("body changed", send("POST", "/api/progress/m238", `{"position": 61, "duration": 600}`,
		mvd1(denTok, "POST", "/api/progress/m238", t0, body)), 401)
	check("method changed", send("POST", "/api/movies", "", mvd1(denTok, "GET", "/api/movies", t0, "")), 401)
	check("query changed", send("GET", "/api/movies?since=1d", "", mvd1(denTok, "GET", "/api/movies?since=0", t0, "")), 401)
	check("unknown key id", send("GET", "/api/movies", "", "MVD1 k=0000000000000000,t=1,sig=AAAA"), 401)
	check("not even that", send("GET", "/api/movies", "", "MVD1 nonsense"), 401)
	check("no credential", ts.do("GET", "/api/movies", "", nil), 401)
	check("wrong Bearer", ts.do("GET", "/api/movies", "", bearer("nope")), 401)

	// Each side of the window.
	for _, d := range []int64{-300, 300} {
		check(fmt.Sprint("t ", d), send("GET", "/api/movies", "", mvd1(denTok, "GET", "/api/movies", t0+d, "")), 200)
	}
	for _, d := range []int64{-301, 301} {
		resp := send("GET", "/api/movies", "", mvd1(denTok, "GET", "/api/movies", t0+d, ""))
		check(fmt.Sprint("t ", d), resp, 401)
		if h := resp.Header.Get("WWW-Authenticate"); h != `MVD1 error="stale"` {
			t.Errorf("stale: WWW-Authenticate %q", h)
		}
		if d, err := http.ParseTime(resp.Header.Get("Date")); err != nil || !d.Equal(now) {
			t.Errorf("stale: Date %q, want the server's clock", resp.Header.Get("Date"))
		}
	}

	// A body too big to sign.
	big := strings.Repeat("x", formBodyMax+1)
	check("too big", send("POST", "/api/progress/m238", big, mvd1(denTok, "POST", "/api/progress/m238", t0, big)), 413)

	// Stale ones aren't guesses: well past five, still let in.
	ts.sv.guesses.reset()
	for range 8 {
		send("GET", "/api/movies", "", mvd1(denTok, "GET", "/api/movies", t0-1000, "")).Body.Close()
	}
	check("after stale ones", send("GET", "/api/movies", "", mvd1(denTok, "GET", "/api/movies", t0, "")), 200)
}

// The website's cookie signs a browser in, and nothing else.
func TestSessionCookie(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	cookie := deriveKeys(denTok).cookie
	if c, _ := ts.body("GET", "/api/movies", "", map[string]string{"Cookie": sessionCookie + "=" + cookie}); c != 200 {
		t.Errorf("good cookie: %d", c)
	}
	for what, hdr := range map[string]map[string]string{
		"the old cookie":        {"Cookie": tokenCookie + "=" + denTok},
		"cookie as a token":     bearer(cookie),
		"another kid's":         {"Cookie": sessionCookie + "=" + deriveKeys(kidsTok).kid + cookie[16:]},
		"a token's that's gone": {"Cookie": sessionCookie + "=" + deriveKeys("old-den-token-0123456789").cookie},
	} {
		if c, _ := ts.body("GET", "/api/movies", "", hdr); c != 401 {
			t.Errorf("%s: %d", what, c)
		}
	}
	if c, _ := ts.body("GET", "/api/movies?token="+cookie, "", nil); c != 401 {
		t.Errorf("cookie as ?token=: %d", c)
	}
}

// Wrong guesses from an address lock it out, doubling, up to a minute; a
// success clears it.
func TestGuessLimit(t *testing.T) {
	var g guesses
	a := addrOf("192.0.2.1:5000", "", "")
	b := addrOf("192.0.2.2:5000", "", "")
	now := time.Unix(1000, 0)
	for i := range 4 {
		g.fail(a, now)
		if w := g.wait(a, now); w != 0 {
			t.Fatalf("locked after %d", i+1)
		}
	}
	g.fail(a, now) // the fifth
	if w := g.wait(a, now); w != time.Second {
		t.Errorf("after 5: %v", w)
	}
	if w := g.wait(b, now); w != 0 {
		t.Errorf("another address: %v", w)
	}
	g.fail(a, now)
	if w := g.wait(a, now); w != 2*time.Second {
		t.Errorf("after 6: %v", w)
	}
	for range 20 {
		g.fail(a, now)
	}
	if w := g.wait(a, now); w != time.Minute {
		t.Errorf("at most a minute: %v", w)
	}
	if w := g.wait(a, now.Add(time.Minute)); w != 0 {
		t.Errorf("after the minute: %v", w)
	}
	g.ok(a)
	g.fail(a, now)
	if w := g.wait(a, now); w != 0 {
		t.Errorf("after a success, one failure: %v", w)
	}

	// The table doesn't grow: the oldest failure makes room.
	var h guesses
	for i := range 2 * len(h.t) {
		x := addrOf(fmt.Sprintf("198.51.100.%d:1", i), "", "")
		for range 5 {
			h.fail(x, now.Add(time.Duration(i)*time.Second))
		}
	}
	last := addrOf(fmt.Sprintf("198.51.100.%d:1", 2*len(h.t)-1), "", "")
	if h.wait(last, now.Add(time.Duration(2*len(h.t)-1)*time.Second)) == 0 {
		t.Error("the newest isn't kept")
	}

	// Behind a proxy on the same machine, the forwarded address counts.
	if addrOf("127.0.0.1:1", "10.0.0.1, 192.0.2.9", "") != addrOf("192.0.2.9:7", "", "") {
		t.Error("X-Forwarded-For from loopback isn't used")
	}
	if addrOf("192.0.2.1:1", "192.0.2.9", "") != a {
		t.Error("X-Forwarded-For from elsewhere is believed")
	}
	if n := testing.AllocsPerRun(100, func() { g.fail(a, now); g.wait(a, now); g.ok(a) }); n != 0 {
		t.Errorf("%v allocations", n)
	}
}

// Through the server: five wrong tokens, then 429 for anything with a
// credential, the right one included; nothing presented isn't counted.
func TestGuessLimitServer(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	for range 10 {
		ts.body("GET", "/api/movies", "", nil)
	}
	if c, _ := ts.body("GET", "/api/movies", "", bearer(denTok)); c != 200 {
		t.Fatalf("no credential counted: %d", c)
	}
	for range 5 {
		ts.body("GET", "/api/movies", "", bearer("wrong"))
	}
	resp := ts.do("GET", "/api/movies", "", bearer(denTok))
	resp.Body.Close()
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "1" {
		t.Errorf("locked out: %d Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if c, _ := ts.body("GET", "/api/movies", "", nil); c != 401 {
		t.Errorf("nothing presented, while locked: %d", c)
	}
}

// Short tokens still work: a warning, not an error.
func TestShortTokenWorks(t *testing.T) {
	ts := newTestServer(t, "short")
	defer ts.hs.Close()
	if c, _ := ts.body("GET", signedPath("short", "/art/m238/poster"), "", nil); c != 200 {
		t.Errorf("%d", c)
	}
}

func (g *guesses) reset() {
	g.mu.Lock()
	g.t = [len(g.t)]guessEntry{}
	g.mu.Unlock()
}

// The cost of signing a list: each movie's poster and fanart. Compare
// with BenchmarkMovieListPlain; on the router build, GOARCH=mips.
func BenchmarkMovieListSigned(b *testing.B) { benchList(b, true) }
func BenchmarkMovieListPlain(b *testing.B)  { benchList(b, false) }

func benchList(b *testing.B, signed bool) {
	acc := &access{keys: deriveKeys(vectorToken), state: &State{m: map[string]Progress{}}}
	sv := &Server{}
	s := shelf{sv: sv, acc: acc}
	if signed {
		s.sign = &linkSigner{kid: acc.kid, key: acc.link}
	}
	paths := make([]string, 500)
	for i := range paths {
		paths[i] = fmt.Sprintf("m%d", 1000+i)
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, id := range paths {
			_ = s.art(id, "poster", "x")
			_ = s.art(id, "fanart", "x")
		}
	}
}

// Behind a proxy that isn't on loopback (a hosting service's, on a private
// address), client_ip_header names the header with the client's address:
// two clients are two addresses, not the proxy's one, so five wrong tokens
// from one don't lock the other out.
func TestGuessLimitBehindRemoteProxy(t *testing.T) {
	// As Render passes them on: the connection from its proxy, an
	// X-Forwarded-For with the Cloudflare edge last, and Cloudflare's header.
	a := addrOf("10.21.157.68:4000", "198.51.100.7, 104.22.17.40", "198.51.100.7")
	b := addrOf("10.21.157.68:4000", "203.0.113.9, 104.22.17.40", "203.0.113.9")
	var g guesses
	now := time.Unix(1000, 0)
	for range 5 {
		g.fail(a, now)
	}
	if w := g.wait(b, now); w != 0 {
		t.Errorf("another client behind the proxy waits %v (both are %v and %v)", w, a, b)
	}
	if a != netip.MustParseAddr("198.51.100.7") {
		t.Errorf("the client is %v", a)
	}

	// The header is believed only from a proxy on loopback or a private
	// network: a client reaching the server directly can't pick its address.
	if addrOf("192.0.2.1:1", "", "198.51.100.7") != netip.MustParseAddr("192.0.2.1") {
		t.Error("the header from a public address is believed")
	}
	if addrOf("10.0.0.1:1", "", "nonsense") != netip.MustParseAddr("10.0.0.1") {
		t.Error("a header that isn't an address isn't the connection's")
	}
}
