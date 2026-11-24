package tmdb

import (
	"net/url"
	"strconv"
	"strings"
)

type TVShow struct {
	ID                  int      `json:"id"`
	Name                string   `json:"name"`
	OriginalName        string   `json:"original_name"`
	OriginalLanguage    string   `json:"original_language"`
	Overview            string   `json:"overview"`
	Tagline             string   `json:"tagline"`
	FirstAirDate        string   `json:"first_air_date"`
	Status              string   `json:"status"`
	EpisodeRunTime      []int    `json:"episode_run_time"`
	VoteAverage         float64  `json:"vote_average"`
	VoteCount           int      `json:"vote_count"`
	PosterPath          string   `json:"poster_path"`
	BackdropPath        string   `json:"backdrop_path"`
	OriginCountry       []string `json:"origin_country"`
	Genres              []named  `json:"genres"`
	Networks            []named  `json:"networks"`
	ProductionCompanies []named  `json:"production_companies"`
	Seasons             []struct {
		SeasonNumber int    `json:"season_number"`
		Name         string `json:"name"`
		PosterPath   string `json:"poster_path"`
		EpisodeCount int    `json:"episode_count"`
	} `json:"seasons"`
	ExternalIDs struct {
		IMDBID string `json:"imdb_id"`
		TVDBID int    `json:"tvdb_id"`
	} `json:"external_ids"`
	ContentRatings struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Rating  string `json:"rating"`
		} `json:"results"`
	} `json:"content_ratings"`
	Credits struct {
		Cast []CastMember `json:"cast"`
	} `json:"credits"`
}

type CastMember struct {
	Name        string `json:"name"`
	Character   string `json:"character"`
	Order       int    `json:"order"`
	ProfilePath string `json:"profile_path"`
}

func (s *TVShow) Year() int { return YearOf(s.FirstAirDate) }

func (s *TVShow) Rating(country string) string {
	for _, r := range s.ContentRatings.Results {
		if strings.EqualFold(r.Country, country) {
			return r.Rating
		}
	}
	return ""
}

// RegularSeasons counts numbered seasons, not specials.
func (s *TVShow) RegularSeasons() []int {
	var out []int
	for _, se := range s.Seasons {
		if se.SeasonNumber > 0 {
			out = append(out, se.SeasonNumber)
		}
	}
	return out
}

func (s *TVShow) SeasonPoster(n int) string {
	for _, se := range s.Seasons {
		if se.SeasonNumber == n {
			return se.PosterPath
		}
	}
	return ""
}

func (s *TVShow) seasonName(n int) string {
	for _, se := range s.Seasons {
		if se.SeasonNumber == n {
			return se.Name
		}
	}
	return ""
}

type Episode struct {
	ID            int     `json:"id"`
	SeasonNumber  int     `json:"season_number"`
	EpisodeNumber int     `json:"episode_number"`
	Name          string  `json:"name"`
	Overview      string  `json:"overview"`
	AirDate       string  `json:"air_date"`
	Runtime       int     `json:"runtime"`
	StillPath     string  `json:"still_path"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int     `json:"vote_count"`
	Crew          []struct {
		Name string `json:"name"`
		Job  string `json:"job"`
	} `json:"crew"`
	GuestStars []CastMember `json:"guest_stars"`
}

type Season struct {
	SeasonNumber int       `json:"season_number"`
	Name         string    `json:"name"`
	Episodes     []Episode `json:"episodes"`
}

func (se *Season) Episode(n int) *Episode {
	for i := range se.Episodes {
		if se.Episodes[i].EpisodeNumber == n {
			return &se.Episodes[i]
		}
	}
	return nil
}

func (t *Client) SearchTV(query string, year int) ([]SearchResult, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("first_air_date_year", strconv.Itoa(year))
	}
	var r struct {
		Results []struct {
			ID            int      `json:"id"`
			Name          string   `json:"name"`
			OriginalName  string   `json:"original_name"`
			FirstAirDate  string   `json:"first_air_date"`
			Overview      string   `json:"overview"`
			Popularity    float64  `json:"popularity"`
			OriginCountry []string `json:"origin_country"`
		} `json:"results"`
	}
	if err := t.get("/search/tv", q, &r); err != nil {
		return nil, err
	}
	out := make([]SearchResult, len(r.Results))
	for i, x := range r.Results {
		out[i] = SearchResult{ID: x.ID, Title: x.Name, OriginalTitle: x.OriginalName,
			ReleaseDate: x.FirstAirDate, Overview: x.Overview, Popularity: x.Popularity,
			Countries: x.OriginCountry}
	}
	return out, nil
}

func (t *Client) TV(id int) (*TVShow, error) {
	var s TVShow
	q := url.Values{"append_to_response": {"external_ids,content_ratings,credits"}}
	if err := t.get("/tv/"+strconv.Itoa(id), q, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (t *Client) Season(show, n int) (*Season, error) {
	var se Season
	if err := t.get("/tv/"+strconv.Itoa(show)+"/season/"+strconv.Itoa(n), nil, &se); err != nil {
		return nil, err
	}
	return &se, nil
}
