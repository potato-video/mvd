package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchReport(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			http.NotFound(w, r)
			return
		}
		asked = r.URL.Query().Get("q")
		io.WriteString(w, `{"movies":[{"kind":"movie","id":"m238","title":"The Godfather","year":1972},
			{"kind":"movie","id":"f0a1b2c3d4e5","title":"Amélie"}],
			"shows":[{"kind":"show","id":"s95396","title":"Severance","year":2022,"episode_count":2,"unwatched":2}],
			"episodes":[{"kind":"episode","id":"e95396102","show_id":"s95396","show_title":"Severance","season":1,"episode":2,"title":"Half Loop","stream":"/stream/e95396102"},
			{"kind":"episode","id":"e7","show_id":"s1","show_title":"Batman","season":1,"episode":3,"last_episode":4,"stream":"/stream/e7"}]}`)
	}))
	defer srv.Close()
	api := &apiClient{base: srv.URL, http: srv.Client()}

	var res searchResults
	if err := api.get("/api/search?q=god+fa%26", &res); err != nil {
		t.Fatal(err)
	}
	if asked != "god fa&" {
		t.Errorf("asked for %q", asked)
	}
	want := "movie    The Godfather (1972)        m238\n" +
		"movie    Amélie                      f0a1b2c3d4e5\n" +
		"show     Severance (2022)            s95396\n" +
		"episode  Severance S01E02 Half Loop  e95396102\n" +
		"episode  Batman S01E03-E04           e7\n"
	if got := searchReport(res); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := searchReport(searchResults{}); got != "" {
		t.Errorf("nothing found: %q", got)
	}
}
