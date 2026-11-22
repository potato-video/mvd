package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// guard stops other websites using the server through your browser. A page
// on any site can submit a form to http://router:6837/..., and the browser
// sends it, so without this, visiting the wrong page could add a torrent or
// start an import.
//
//   - Changes (anything but GET and HEAD) from another site are refused: the
//     browser says where a request came from in Origin and Sec-Fetch-Site.
//     Tools like curl, mpv and mvd itself send neither, and pass.
//   - A GET that looks a title up on TMDB (the import's search, details and
//     posters) from another site is refused too: each is a request on the
//     owner's key and a file in the cache, and an <img> on any page could
//     make thousands. Nothing else needs them cross-site.
//   - Without a token, anything addressed to an unexpected host name is
//     refused, reading included. That's DNS rebinding: a site whose name
//     points at your router counts as the same site to the browser, so
//     could read the library and the import folder's names, or make
//     changes. With a token, the attacker's page doesn't have it, so this
//     isn't needed.
func (sv *Server) guard(next http.Handler) http.Handler {
	extra := map[string]bool{}
	for _, h := range strings.Split(sv.cfg.HostNames, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			extra[h] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sv.cfg.Token == "" && !localHost(r.Host, extra) {
			http.Error(w, "refused: "+hostOnly(r.Host)+" isn't a name mvd expects. Set a token in config.toml, "+
				"or add the name to [server] host_names", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" && lookup(r.URL.Path) {
				http.Error(w, "refused: this request came from another website", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "refused: this request came from another website", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if o == "null" || err != nil || !sameHost(u.Host, r) {
				http.Error(w, "refused: this request came from another website", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// lookup says whether a GET path asks TMDB for something.
func lookup(p string) bool {
	for _, pre := range []string{"/api/import/search", "/api/import/details", "/api/import/poster/",
		"/ui/import/search", "/ui/import/details"} {
		if p == pre || strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

func sameHost(originHost string, r *http.Request) bool {
	o := strings.ToLower(originHost)
	if o == strings.ToLower(r.Host) {
		return true
	}
	// Behind a reverse proxy, the name the browser used is passed on here.
	if f := r.Header.Get("X-Forwarded-Host"); f != "" && o == strings.ToLower(strings.TrimSpace(strings.Split(f, ",")[0])) {
		return true
	}
	return false
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.Trim(h, "[]")
}

// localHost says whether a Host header names this machine the way a home
// network does: an IP address, localhost, a bare name ("router",
// "GL-AR750S"), a local suffix (.lan, .local, .home.arpa...), or one listed
// in host_names. Anything else (a real domain) could be a rebinding attack.
func localHost(h string, extra map[string]bool) bool {
	host := strings.ToLower(hostOnly(h))
	switch {
	case host == "":
		return true // HTTP/1.0, no Host: not a browser
	case net.ParseIP(host) != nil, host == "localhost", !strings.Contains(host, "."), extra[host]:
		return true
	}
	for _, suffix := range []string{".lan", ".local", ".home", ".home.arpa", ".internal", ".localdomain", ".localhost"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
