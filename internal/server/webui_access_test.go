//go:build webui

package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A viewer's website: no Incoming, its name at the foot, and play links
// signed for its own token, never the admin's, with no token in them.
func TestUIViewer(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	_, home := ts.body("GET", "/", "", bearer(denTok))
	if strings.Contains(home, `href="/incoming"`) || !strings.Contains(home, "Signed in as Den") {
		t.Errorf("den's home page:\n%s", home)
	}
	den := "k=" + deriveKeys(denTok).kid + "&amp;sig="
	if strings.Contains(home, adminTok) || strings.Contains(home, denTok) || !strings.Contains(home, den) {
		t.Errorf("den's play links:\n%s", home)
	}
	_, m3u := ts.body("GET", "/play/m238.m3u", "", bearer(denTok))
	if strings.Contains(m3u, denTok) || !strings.Contains(m3u, "k="+deriveKeys(denTok).kid+"&sig=") {
		t.Errorf("den's .m3u:\n%s", m3u)
	}
	_, home = ts.body("GET", "/", "", bearer(adminTok))
	if incomingBuilt && !strings.Contains(home, `href="/incoming"`) || !strings.Contains(home, "Signed in as admin") {
		t.Errorf("the admin's home page:\n%s", home)
	}

	// Signing in with a viewer's token.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := noFollow.PostForm(ts.hs.URL+"/login", url.Values{"token": {kidsTok}})
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusSeeOther || r.Cookies()[0].Name != sessionCookie {
		t.Fatalf("sign in as kids: %d %v", r.StatusCode, r.Cookies())
	}
	code, page := ts.body("GET", "/movies", "", map[string]string{"Cookie": r.Cookies()[0].String()})
	if code != 200 || !strings.Contains(page, "Signed in as kids") || strings.Contains(page, "Godfather") {
		t.Errorf("kids' movies: %d\n%s", code, page)
	}
}

// Signing in goes on to where the browser was going, on this server only;
// signing out forgets the cookie.
func TestUISignInNext(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := noFollow.Get(ts.hs.URL + "/pair?code=CC6W-TAB6")
	if loc := r.Header.Get("Location"); loc != "/login?next=%2Fpair%3Fcode%3DCC6W-TAB6" {
		t.Errorf("to sign in: %s", loc)
	}
	_, page := ts.body("GET", "/login?next=%2Fpair%3Fcode%3DCC6W-TAB6", "", nil)
	if !strings.Contains(page, `name="next" value="/pair?code=CC6W-TAB6"`) {
		t.Errorf("the form doesn't carry next:\n%s", page)
	}
	for next, want := range map[string]string{
		"/pair?code=CC6W-TAB6":  "/pair?code=CC6W-TAB6",
		"":                      "/",
		"//evil.example/":       "/",
		"/\\evil.example/":      "/",
		"https://evil.example/": "/",
		"evil":                  "/",
	} {
		r, _ := noFollow.PostForm(ts.hs.URL+"/login", url.Values{"token": {adminTok}, "next": {next}})
		if loc := r.Header.Get("Location"); loc != want {
			t.Errorf("next %q: %s", next, loc)
		}
	}

	// Everyone's footer: Pair and Sign out.
	for _, tok := range []string{adminTok, denTok} {
		_, page = ts.body("GET", "/", "", bearer(tok))
		if !strings.Contains(page, `href="/pair"`) || !strings.Contains(page, `action="/logout"`) {
			t.Errorf("%s's footer:\n%s", tok, page)
		}
	}

	// The sign-in page asks for a password, and on the way to letting a
	// device in, says which.
	d := newDevice(t)
	d.ask(ts, "Living Room")
	_, page = ts.body("GET", "/login?next=%2Fpair%3Fcode%3D"+d.code, "", nil)
	if !strings.Contains(page, "Password") || strings.Contains(page, "Token") || !strings.Contains(page, "Living Room") {
		t.Errorf("signing in to let a device in:\n%s", page)
	}
	_, page = ts.body("GET", "/login", "", nil)
	if strings.Contains(page, "Living Room") {
		t.Errorf("signing in:\n%s", page)
	}

	r, _ = noFollow.PostForm(ts.hs.URL+"/logout", nil)
	if r.StatusCode != http.StatusSeeOther || len(r.Cookies()) == 0 || r.Cookies()[0].Name != sessionCookie || r.Cookies()[0].MaxAge >= 0 {
		t.Errorf("sign out: %d %v", r.StatusCode, r.Cookies())
	}
}

// Too many wrong tries at the sign-in page, and it says to wait.
func TestUISignInGuesses(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	for range 5 {
		http.PostForm(ts.hs.URL+"/login", url.Values{"token": {"wrong"}})
	}
	r, _ := http.PostForm(ts.hs.URL+"/login", url.Values{"token": {adminTok}})
	if r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("%d", r.StatusCode)
	}
	b := make([]byte, 64<<10)
	n, _ := r.Body.Read(b)
	if !strings.Contains(string(b[:n]), "Too many wrong tries") {
		t.Errorf("%s", b[:n])
	}
}

// The Pair page: a device's code, approving it from the form.
func TestUIPair(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	d := newDevice(t)
	d.ask(ts, "Living Room")
	_, page := ts.body("GET", "/pair", "", bearer(adminTok))
	if !strings.Contains(page, "Living Room") || !strings.Contains(page, "/pair?code="+d.code) {
		t.Errorf("waiting:\n%s", page)
	}
	_, page = ts.body("GET", "/pair?code="+strings.ToLower(showCode(d.code)), "", bearer(adminTok))
	if !strings.Contains(page, showCode(d.code)) || !strings.Contains(page, `value="kids"`) || strings.Contains(page, "checked") {
		t.Errorf("the device:\n%s", page)
	}

	// A viewer: no list of what's waiting and no tokens to choose from,
	// one button, and the device gets the viewer's own access.
	v := newDevice(t)
	v.ask(ts, "Bedroom")
	den := map[string]string{"Authorization": "Bearer " + denTok, "Content-Type": "application/x-www-form-urlencoded"}
	code, page := ts.body("GET", "/pair", "", bearer(denTok))
	if code != 200 || strings.Contains(page, "Bedroom") || strings.Contains(page, "Living Room") {
		t.Errorf("a viewer's Pair page: %d\n%s", code, page)
	}
	_, page = ts.body("GET", "/pair?code="+v.code, "", bearer(denTok))
	if !strings.Contains(page, "Bedroom") || !strings.Contains(page, showCode(v.code)) || strings.Contains(page, `type="radio"`) {
		t.Errorf("a viewer's device:\n%s", page)
	}
	if _, page = ts.body("POST", "/ui/pair", "code="+v.code+"&token=admin&action=approve", den); !strings.Contains(page, "only the admin") {
		t.Errorf("a viewer giving the admin's:\n%s", page)
	}
	if _, page = ts.body("POST", "/ui/pair", "code="+v.code+"&action=approve", den); !strings.Contains(page, "Bedroom is let in") {
		t.Errorf("a viewer letting it in:\n%s", page)
	}
	var vbox pairBox
	decode(t, ts.do("GET", "/api/pair/"+v.code, "", nil), &vbox)
	if s, err := v.open(vbox, v.code); err != nil || s.Name != "Den" || s.Admin {
		t.Errorf("%+v %v", s, err)
	}
	form := map[string]string{"Authorization": "Bearer " + adminTok, "Content-Type": "application/x-www-form-urlencoded"}
	_, page = ts.body("POST", "/ui/pair", "code="+d.code+"&action=approve", form)
	if !strings.Contains(page, "Choose who") {
		t.Errorf("no token chosen:\n%s", page)
	}
	_, page = ts.body("POST", "/ui/pair", "code="+d.code+"&token=Den&action=approve", form)
	if !strings.Contains(page, "Living Room is let in as Den") {
		t.Errorf("approved:\n%s", page)
	}
	var box pairBox
	decode(t, ts.do("GET", "/api/pair/"+d.code, "", nil), &box)
	if s, err := d.open(box, d.code); err != nil || s.Name != "Den" {
		t.Errorf("%+v %v", s, err)
	}
}
