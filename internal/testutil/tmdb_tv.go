package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeShow struct {
	ID      int
	Name    string
	Date    string
	Country string
	Seasons map[int][]string // season -> episode names (episode n = index+1)
}

var fakeShows = []fakeShow{
	{95396, "Severance", "2022-02-17", "US", map[int][]string{
		0: {"Inside Severance"},
		1: {"Good News About Hell", "Half Loop", "In Perpetuity", "The You You Are", "The Grim Barbarity of Optics and Design"},
	}},
	{2316, "The Office", "2005-03-24", "US", map[int][]string{2: {"The Dundies", "Sexual Harassment"}}},
	{2996, "The Office", "2001-07-09", "GB", map[int][]string{1: {"Downsize"}}},
	{57243, "Doctor Who", "2005-03-26", "GB", map[int][]string{1: {"Rose", "The End of the World", "The Unquiet Dead"}}},
	{121, "Doctor Who", "1963-11-23", "GB", map[int][]string{1: {"An Unearthly Child"}}},
	{1437, "Firefly", "2002-09-20", "US", map[int][]string{0: {"Serenity"}, 1: {"The Train Job", "Bushwhacked", "Our Mrs. Reynolds", "Jaynestown"}}},
	{2098, "Batman: The Animated Series", "1992-09-05", "US", map[int][]string{
		1: {"The Cat and the Claw (1)", "On Leather Wings", "Heart of Ice", "Christmas with the Joker"}}},
}

// Batman's DVD order, as aired season/episode pairs.
var fakeGroups = map[string][][2]int{
	"aaaaaaaaaaaaaaaaaaaaaaaa": {{1, 2}, {1, 4}, {1, 1}}, // DVD: Leather Wings, Joker, Cat and Claw
	"bbbbbbbbbbbbbbbbbbbbbbbb": {{1, 3}, {1, 2}},         // a second, shorter DVD order
}

func TMDBTV(t *testing.T) *httptest.Server {
	find := func(id int) *fakeShow {
		for i := range fakeShows {
			if fakeShows[i].ID == id {
				return &fakeShows[i]
			}
		}
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/3/search/tv", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		year := atoi(r.URL.Query().Get("first_air_date_year"))
		res := []map[string]any{}
		for _, s := range fakeShows {
			if titleSim(q, s.Name) < 0.6 || (year != 0 && yearOf(s.Date) != year) {
				continue
			}
			res = append(res, map[string]any{"id": s.ID, "name": s.Name, "original_name": s.Name,
				"first_air_date": s.Date, "overview": "About " + s.Name, "origin_country": []string{s.Country}})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": res})
	})
	mux.HandleFunc("/3/tv/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/3/tv/"), "/")
		if parts[0] == "episode_group" {
			pairs, ok := fakeGroups[parts[1]]
			if !ok {
				http.NotFound(w, r)
				return
			}
			var eps []map[string]any
			for i, p := range pairs {
				eps = append(eps, map[string]any{"season_number": p[0], "episode_number": p[1], "order": i,
					"name": "group copy", "id": 2098000 + p[0]*100 + p[1]})
			}
			json.NewEncoder(w).Encode(map[string]any{"id": parts[1], "name": "DVD Order", "type": 3,
				"groups": []map[string]any{{"name": "Volume 1", "order": 1, "episodes": eps}}})
			return
		}
		if len(parts) == 2 && parts[1] == "episode_groups" {
			res := []map[string]any{}
			if parts[0] == "2098" {
				res = append(res,
					map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaa", "name": "DVD Order", "type": 3, "group_count": 1, "episode_count": 3},
					map[string]any{"id": "bbbbbbbbbbbbbbbbbbbbbbbb", "name": "DVD (UK)", "type": 3, "group_count": 1, "episode_count": 2},
					map[string]any{"id": "cccccccccccccccccccccccc", "name": "Production", "type": 6, "group_count": 1, "episode_count": 3})
			}
			json.NewEncoder(w).Encode(map[string]any{"results": res})
			return
		}
		s := find(atoi(parts[0]))
		if s == nil {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 3 && parts[1] == "season" {
			n := atoi(parts[2])
			names, ok := s.Seasons[n]
			if !ok {
				http.NotFound(w, r)
				return
			}
			var eps []map[string]any
			for i, name := range names {
				eps = append(eps, map[string]any{"id": s.ID*1000 + n*100 + i + 1, "season_number": n,
					"episode_number": i + 1, "name": name, "overview": "In which " + name,
					"air_date": s.Date, "still_path": fmt.Sprintf("/still%d.jpg", i+1),
					"crew": []map[string]any{{"name": "A Director", "job": "Director"}}})
			}
			json.NewEncoder(w).Encode(map[string]any{"season_number": n, "episodes": eps})
			return
		}
		var seasons []map[string]any
		for n, names := range s.Seasons {
			name := fmt.Sprintf("Season %d", n)
			if n == 0 {
				name = "Specials"
			}
			seasons = append(seasons, map[string]any{"season_number": n, "name": name,
				"poster_path": fmt.Sprintf("/s%d.jpg", n), "episode_count": len(names)})
		}
		json.NewEncoder(w).Encode(map[string]any{"id": s.ID, "name": s.Name, "original_name": s.Name,
			"first_air_date": s.Date, "overview": "About " + s.Name, "poster_path": "/p.jpg",
			"backdrop_path": "/b.jpg", "seasons": seasons, "origin_country": []string{s.Country},
			"external_ids":    map[string]any{"imdb_id": "tt0000001", "tvdb_id": 7000 + s.ID},
			"content_ratings": map[string]any{"results": []map[string]any{{"iso_3166_1": "GB", "rating": "15"}}},
			"networks":        []map[string]any{{"name": "Some Network"}},
			"genres":          []map[string]any{{"name": "Drama"}, {"name": "Mystery"}},
			"status":          "Returning Series"})
	})
	mux.HandleFunc("/t/p/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("\xff\xd8\xff fake jpeg"))
	})
	return httptest.NewServer(mux)
}
