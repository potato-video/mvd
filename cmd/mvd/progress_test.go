package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A fake server: two movies, a show with two seasons and a double episode,
// and a record of what was posted to /api/progress.
func fakeAPI(t *testing.T) (*apiClient, *[]string) {
	var posted []string
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, s string) { io.WriteString(w, s) }
	mux.HandleFunc("GET /api/movies", func(w http.ResponseWriter, r *http.Request) {
		write(w, `[{"kind":"movie","id":"m1","title":"Star Wars","year":1977},{"kind":"movie","id":"m2","title":"Dune","year":2021}]`)
	})
	mux.HandleFunc("GET /api/shows", func(w http.ResponseWriter, r *http.Request) {
		write(w, `[{"kind":"show","id":"s1","title":"Star City"}]`)
	})
	mux.HandleFunc("GET /api/shows/s1", func(w http.ResponseWriter, r *http.Request) {
		write(w, `{"kind":"show","id":"s1","title":"Star City","seasons":[{"number":1,"episodes":[
			{"id":"e1","season":1,"episode":1},{"id":"e2","season":1,"episode":2,"last_episode":3}]},
			{"number":2,"episodes":[{"id":"e4","season":2,"episode":1}]}]}`)
	})
	mux.HandleFunc("GET /api/item/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "m2":
			write(w, `{"kind":"movie","id":"m2","title":"Dune"}`)
		case "e1":
			write(w, `{"kind":"episode","id":"e1","show_title":"Star City","season":1,"episode":1}`)
		case "s1":
			write(w, `{"kind":"show","id":"s1","title":"Star City"}`)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("POST /api/progress/{id}", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]bool
		json.NewDecoder(r.Body).Decode(&b)
		posted = append(posted, r.PathValue("id")+"="+map[bool]string{true: "watched", false: "clear"}[b["watched"]])
		write(w, `{}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &apiClient{base: srv.URL, http: srv.Client()}, &posted
}

func TestResolve(t *testing.T) {
	api, _ := fakeAPI(t)
	cases := map[string]string{
		"m2":               "m2", // an id
		"e1":               "e1",
		"dune":             "m2", // a title, any case
		"Star City":        "s1", // the exact title, though "star" is in Star Wars too
		"star city s01e01": "e1",
		"Star.City.S01E03": "e2", // inside a double episode
		"Star City S1E2":   "e2",
	}
	for in, want := range cases {
		got, _, err := resolve(api, in, "")
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for in, msg := range map[string]string{
		"star":             "could be", // Star Wars or Star City
		"Blade Runner":     "nothing in the library",
		"Star City S03E01": "has no S03E01",
	} {
		if _, _, err := resolve(api, in, ""); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: %v, want %q", in, err, msg)
		}
	}
}

func TestProgressClearAndWatched(t *testing.T) {
	api, posted := fakeAPI(t)
	if err := setWatched(api, "Star City S01E01", "", false); err != nil {
		t.Fatal(err)
	}
	if err := setWatched(api, "star city", "", true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*posted, " "); got != "e1=clear s1=watched" {
		t.Errorf("posted %q", got)
	}
}

func TestProgressNoServer(t *testing.T) {
	api := &apiClient{base: "http://127.0.0.1:1", http: http.DefaultClient}
	if err := setWatched(api, "dune", "", false); err == nil || !strings.Contains(err.Error(), "is it running") {
		t.Errorf("no server: %v", err)
	}
}

// -movie and -show look only among movies or shows.
func TestResolveKind(t *testing.T) {
	api, _ := fakeAPI(t)
	for _, c := range []struct{ in, kind, want string }{
		{"star", "movie", "m1"}, // not "could be": Star City is a show
		{"star", "show", "s1"},
		{"m2", "movie", "m2"},
		{"star city s01e01", "show", "e1"},
	} {
		got, _, err := resolve(api, c.in, c.kind)
		if err != nil || got != c.want {
			t.Errorf("%q as %s: got %q, %v; want %q", c.in, c.kind, got, err, c.want)
		}
	}
	for _, c := range []struct{ in, kind, msg string }{
		{"dune", "show", `no show called "dune" (for a movie, use -movie)`},
		{"star city", "movie", `no movie called "star city" (for a show, use -show)`},
		{"m2", "show", `no show called "m2"`},
		{"e1", "movie", `no movie called "e1"`},
	} {
		if _, _, err := resolve(api, c.in, c.kind); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%q as %s: %v, want %q", c.in, c.kind, err, c.msg)
		}
	}
}

func TestProgressFlags(t *testing.T) {
	for _, c := range []struct {
		verb, args string
		rc         int
		posted     string
	}{
		{"watched", "-show star -season 1", 0, "e1=watched e2=watched"},
		{"clear", "-show star -season 2", 0, "e4=clear"},
		{"watched", "-show star", 0, "s1=watched"},
		{"watched", "-movie star", 0, "m1=watched"},
		{"watched", "dune star.city.s01e01", 0, "m2=watched e1=watched"},
		{"watched", "-show star -season 3", 1, ""}, // no such season
		{"watched", "-show dune", 1, ""},
		{"watched", "-show star.city.s01e01 -season 1", 1, ""}, // an episode, not a show
		{"watched", "-season 1", 2, ""},                        // no -show
		{"watched", "-movie dune -season 1", 2, ""},
		{"watched", "-movie dune -show star", 2, ""},
		{"watched", "-show star dune", 2, ""}, // a flag or names, not both
		{"watched", "-show star -season x", 2, ""},
		{"watched", "", 2, ""},
	} {
		api, posted := fakeAPI(t)
		if rc := progressSet(api, c.verb, strings.Fields(c.args)); rc != c.rc || strings.Join(*posted, " ") != c.posted {
			t.Errorf("%s %s: rc=%d posted %q, want %d and %q", c.verb, c.args, rc, strings.Join(*posted, " "), c.rc, c.posted)
		}
	}
}

// -as sends the name on every request, and the server's refusal is what's
// printed.
func TestProgressAs(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-Mvd-As"))
		if r.Header.Get("X-Mvd-As") == "nobody" {
			http.Error(w, `there's no token named "nobody"; there's "kids"`, http.StatusBadRequest)
			return
		}
		io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	api := &apiClient{base: srv.URL, http: srv.Client(), as: "kids"}
	var out []any
	if err := api.get("/api/up-next", &out); err != nil || len(got) != 1 || got[0] != "kids" {
		t.Errorf("as kids: %v %q", err, got)
	}
	api.as = "nobody"
	if err := api.get("/api/up-next", &out); err == nil || err.Error() != `there's no token named "nobody"; there's "kids"` {
		t.Errorf("as nobody: %v", err)
	}
	api.as = ""
	got = nil
	api.get("/api/up-next", &out)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("as no one: %q", got)
	}
}
