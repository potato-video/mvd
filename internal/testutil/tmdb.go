package testutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// TMDBAll serves both the movie and the TV fakes from one address.
func TMDBAll(t *testing.T) *httptest.Server {
	ms, ts := TMDB(t), TMDBTV(t)
	t.Cleanup(func() { ms.Close(); ts.Close() })
	mu, _ := url.Parse(ms.URL)
	tu, _ := url.Parse(ts.URL)
	movies, tv := httputil.NewSingleHostReverseProxy(mu), httputil.NewSingleHostReverseProxy(tu)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/3/search/tv") || strings.HasPrefix(r.URL.Path, "/3/tv/") {
			tv.ServeHTTP(w, r)
		} else {
			movies.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --- end to end against a fake TMDB ---

type fakeMovie struct {
	ID         int
	Title      string
	Date       string
	Collection int
}

var fakeMovies = []fakeMovie{
	{4995, "Boogie Nights", "1997-10-10", 0},
	{9693, "Children of Men", "2006-09-22", 0},
	{438631, "Dune", "2021-09-15", 0},
	{841, "Dune", "1984-12-14", 0},
	{9615, "The Fast and the Furious: Tokyo Drift", "2006-06-03", 9485},
	{51497, "Fast Five", "2011-04-20", 9485},
	{238, "The Godfather", "1972-03-14", 230},
	{240, "The Godfather Part II", "1974-12-20", 230},
	{1102, "Get Carter", "1971-03-03", 0},
	{10477, "Get Carter", "2000-10-06", 0},
	{11702, "Mission: Impossible", "1996-05-22", 0},
}

var fakeCollections = map[int]string{9485: "The Fast and the Furious Collection", 230: "The Godfather Collection"}

func TMDB(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/3/search/movie", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		year := atoi(r.URL.Query().Get("year"))
		var res []map[string]any
		for _, m := range fakeMovies {
			if titleSim(q, m.Title) < 0.6 || (year != 0 && yearOf(m.Date) != year) {
				continue
			}
			res = append(res, map[string]any{"id": m.ID, "title": m.Title, "original_title": m.Title,
				"release_date": m.Date, "overview": "About " + m.Title})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": res})
	})
	mux.HandleFunc("/3/movie/", func(w http.ResponseWriter, r *http.Request) {
		id := atoi(strings.TrimPrefix(r.URL.Path, "/3/movie/"))
		for _, m := range fakeMovies {
			if m.ID != id {
				continue
			}
			out := map[string]any{"id": m.ID, "title": m.Title, "original_title": m.Title, "original_language": "en", "release_date": m.Date,
				"overview": "About " + m.Title, "runtime": 120, "imdb_id": "tt" + strconv.Itoa(1000000+m.ID),
				"poster_path": "/p" + strconv.Itoa(m.ID) + ".jpg", "backdrop_path": "/b" + strconv.Itoa(m.ID) + ".jpg",
				"genres": []map[string]any{{"name": "Drama"}}, "vote_average": 7.9, "vote_count": 100,
				"credits": map[string]any{
					"cast": []map[string]any{{"name": "Some Actor", "character": "Lead", "order": 0}},
					"crew": []map[string]any{{"name": "Some Director", "job": "Director"}},
				},
				"release_dates": map[string]any{"results": []map[string]any{
					{"iso_3166_1": "GB", "release_dates": []map[string]any{{"certification": "15", "type": 3}}},
				}},
			}
			if m.Collection != 0 {
				out["belongs_to_collection"] = map[string]any{"id": m.Collection, "name": fakeCollections[m.Collection]}
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/3/find/", func(w http.ResponseWriter, r *http.Request) { // a movie's IMDb id is tt(1000000+id)
		id := atoi(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/3/find/"), "tt")) - 1000000
		var hits []map[string]any
		for _, m := range fakeMovies {
			if m.ID == id {
				hits = append(hits, map[string]any{"id": m.ID})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"movie_results": hits, "tv_results": []any{}})
	})
	mux.HandleFunc("/3/collection/", func(w http.ResponseWriter, r *http.Request) {
		id := atoi(strings.TrimPrefix(r.URL.Path, "/3/collection/"))
		json.NewEncoder(w).Encode(map[string]any{"id": id, "name": fakeCollections[id],
			"overview": "A collection", "poster_path": "/c.jpg", "backdrop_path": "/cb.jpg"})
	})
	mux.HandleFunc("/t/p/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("\xff\xd8\xff fake jpeg"))
	})
	return httptest.NewServer(mux)
}
