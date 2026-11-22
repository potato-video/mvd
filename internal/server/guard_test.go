package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"mvd/internal/config"
)

// Review fix 1: other websites can't make changes through your browser.
func TestGuard(t *testing.T) {
	changes := 0
	hs := httptest.NewServer((&Server{}).guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			changes++
		}
	})))
	defer hs.Close()
	post := func(hdr map[string]string, host string) int {
		form := url.Values{"source": {"magnet:?xt=urn:btih:x&dn=Thing"}}
		req, _ := http.NewRequest("POST", hs.URL+"/api/downloads", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	self := strings.TrimPrefix(hs.URL, "http://")
	cases := []struct {
		name string
		hdr  map[string]string
		host string
		want int
	}{
		{"cross-site form", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, "", 403},
		{"other origin, no fetch metadata", map[string]string{"Origin": "https://evil.example"}, "", 403},
		{"opaque origin", map[string]string{"Origin": "null"}, "", 403},
		{"the web UI itself", map[string]string{"Origin": "http://" + self, "Sec-Fetch-Site": "same-origin"}, "", 200},
		{"curl / mpv / mvd (no headers)", nil, "", 200},
		{"DNS rebinding", map[string]string{"Origin": "http://evil.example:6837"}, "evil.example:6837", 403},
		{"router by bare name", map[string]string{"Origin": "http://gl-ar750s:6837"}, "gl-ar750s:6837", 200},
		{"router by .lan name", nil, "router.lan:6837", 200},
	}
	for _, c := range cases {
		if got := post(c.hdr, c.host); got != c.want {
			t.Errorf("%s: HTTP %d, want %d", c.name, got, c.want)
		}
	}
	if changes != 4 {
		t.Errorf("%d changes went through, want 4 (the allowed ones)", changes)
	}
	// Reading is unaffected, even cross-site (images, video).
	req, _ := http.NewRequest("GET", hs.URL+"/api/downloads", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 200 {
		t.Errorf("cross-site GET: %d", resp.StatusCode)
	}
	if !localHost("media.example.com", map[string]bool{"media.example.com": true}) || localHost("media.example.com", nil) {
		t.Errorf("host_names")
	}
}

// Review M1: another website can't make the server look titles up on TMDB
// (each a request on the owner's key and a file in the cache) through a
// browser, with <img src=".../api/import/poster/movie/N">.
func TestGuardRefusesCrossSiteLookups(t *testing.T) {
	hs := httptest.NewServer((&Server{}).guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	defer hs.Close()
	for _, c := range []struct {
		path, site string
		want       int
	}{
		{"/api/import/poster/movie/1", "cross-site", 403},
		{"/api/import/search?kind=movie&q=x", "cross-site", 403},
		{"/api/import/details?kind=movie&tmdb=1", "cross-site", 403},
		{"/ui/import/search?kind=movie&q=x", "cross-site", 403},
		{"/ui/import/details?kind=movie&tmdb=1", "cross-site", 403},
		{"/api/import/poster/movie/1", "same-origin", 200},
		{"/api/import/poster/movie/1", "", 200},
		{"/art/m1/poster", "cross-site", 200},
	} {
		req, _ := http.NewRequest("GET", hs.URL+c.path, nil)
		if c.site != "" {
			req.Header.Set("Sec-Fetch-Site", c.site)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s from %q: %d, want %d", c.path, c.site, resp.StatusCode, c.want)
		}
	}
}

// Review L3: without a token, a DNS-rebinding page can't read either: a
// GET addressed to an unexpected host name is refused like a change.
func TestGuardRebindingReads(t *testing.T) {
	for _, c := range []struct {
		token, host string
		want        int
	}{
		{"", "evil.example:6837", 403},
		{"", "router.lan:6837", 200},
		{"", "192.168.8.1:6837", 200},
		{"", "gl-ar750s", 200},
		{"", "media.example.com", 200}, // in host_names
		{"secret", "evil.example:6837", 200},
	} {
		sv := &Server{cfg: config.Config{Token: c.token, HostNames: "media.example.com"}}
		hs := httptest.NewServer(sv.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
		req, _ := http.NewRequest("GET", hs.URL+"/api/movies", nil)
		req.Host = c.host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		hs.Close()
		if resp.StatusCode != c.want {
			t.Errorf("token %q, host %s: %d, want %d", c.token, c.host, resp.StatusCode, c.want)
		}
	}
}
