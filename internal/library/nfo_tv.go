package library

import (
	"encoding/xml"
	"fmt"
	"strconv"

	"mvd/internal/lang"
	"mvd/internal/tmdb"
)

type nfoShow struct {
	XMLName       xml.Name         `xml:"tvshow"`
	Title         string           `xml:"title"`
	OriginalTitle string           `xml:"originaltitle,omitempty"`
	OriginalLang  string           `xml:"originallanguage,omitempty"` // Kodi v22's
	Year          int              `xml:"year,omitempty"`
	Premiered     string           `xml:"premiered,omitempty"`
	Plot          string           `xml:"plot,omitempty"`
	Tagline       string           `xml:"tagline,omitempty"`
	Runtime       int              `xml:"runtime,omitempty"`
	MPAA          string           `xml:"mpaa,omitempty"`
	Status        string           `xml:"status,omitempty"`
	Ratings       *nfoRatings      `xml:"ratings,omitempty"`
	UniqueIDs     []nfoUniqueID    `xml:"uniqueid"`
	Genres        []string         `xml:"genre"`
	Studios       []string         `xml:"studio"`
	NamedSeasons  []nfoNamedSeason `xml:"namedseason"`
	Actors        []nfoActor       `xml:"actor"`
}

type nfoNamedSeason struct {
	Number int    `xml:"number,attr"`
	Name   string `xml:",chardata"`
}

type nfoEpisode struct {
	XMLName   xml.Name      `xml:"episodedetails"`
	Title     string        `xml:"title"`
	ShowTitle string        `xml:"showtitle"`
	Season    int           `xml:"season"`
	Episode   int           `xml:"episode"`
	Plot      string        `xml:"plot,omitempty"`
	Aired     string        `xml:"aired,omitempty"`
	Runtime   int           `xml:"runtime,omitempty"`
	Ratings   *nfoRatings   `xml:"ratings,omitempty"`
	UniqueIDs []nfoUniqueID `xml:"uniqueid"`
	Directors []string      `xml:"director"`
	Credits   []string      `xml:"credits"`
	Actors    []nfoActor    `xml:"actor"`
}

func tmdbRating(avg float64, votes int) *nfoRatings {
	if votes == 0 {
		return nil
	}
	r := &nfoRatings{}
	r.Rating.Name, r.Rating.Max, r.Rating.Default = "themoviedb", 10, true
	r.Rating.Value = fmt.Sprintf("%.1f", avg)
	r.Rating.Votes = votes
	return r
}

func actors(cast []tmdb.CastMember, limit int, t *tmdb.Client) []nfoActor {
	var out []nfoActor
	for i, a := range cast {
		if i >= limit {
			break
		}
		out = append(out, nfoActor{Name: a.Name, Role: a.Character, Order: a.Order,
			Thumb: t.ImageURL(a.ProfilePath, "w185")})
	}
	return out
}

func buildShowNFO(s *tmdb.TVShow, region string, t *tmdb.Client) []byte {
	n := nfoShow{
		Title:     s.Name,
		Year:      s.Year(),
		Premiered: s.FirstAirDate,
		Plot:      s.Overview,
		Tagline:   s.Tagline,
		MPAA:      s.Rating(region),
		Status:    s.Status,
		Ratings:   tmdbRating(s.VoteAverage, s.VoteCount),
		UniqueIDs: []nfoUniqueID{{Type: "tmdb", Default: true, Value: strconv.Itoa(s.ID)}},
		Actors:    actors(s.Credits.Cast, 20, t),
	}
	n.OriginalLang = lang.Code(s.OriginalLanguage)
	if s.OriginalName != s.Name {
		n.OriginalTitle = s.OriginalName
	}
	if len(s.EpisodeRunTime) > 0 {
		n.Runtime = s.EpisodeRunTime[0]
	}
	if s.ExternalIDs.TVDBID != 0 {
		n.UniqueIDs = append(n.UniqueIDs, nfoUniqueID{Type: "tvdb", Value: strconv.Itoa(s.ExternalIDs.TVDBID)})
	}
	if s.ExternalIDs.IMDBID != "" {
		n.UniqueIDs = append(n.UniqueIDs, nfoUniqueID{Type: "imdb", Value: s.ExternalIDs.IMDBID})
	}
	for _, g := range s.Genres {
		n.Genres = append(n.Genres, g.Name)
	}
	for _, nw := range s.Networks {
		n.Studios = append(n.Studios, nw.Name)
	}
	for _, se := range s.Seasons {
		if se.Name != "" {
			n.NamedSeasons = append(n.NamedSeasons, nfoNamedSeason{se.SeasonNumber, se.Name})
		}
	}
	out, _ := xml.MarshalIndent(n, "", "  ")
	return append([]byte(xml.Header), append(out, '\n')...)
}

// buildEpisodeNFO writes one <episodedetails> per episode in the file;
// Kodi and Jellyfin both read several back to back for multi-episode files.
func buildEpisodeNFO(show *tmdb.TVShow, eps []*tmdb.Episode, t *tmdb.Client) []byte {
	out := []byte(xml.Header)
	for _, e := range eps {
		n := nfoEpisode{
			Title:     e.Name,
			ShowTitle: show.Name,
			Season:    e.SeasonNumber,
			Episode:   e.EpisodeNumber,
			Plot:      e.Overview,
			Aired:     e.AirDate,
			Runtime:   e.Runtime,
			Ratings:   tmdbRating(e.VoteAverage, e.VoteCount),
			UniqueIDs: []nfoUniqueID{{Type: "tmdb", Default: true, Value: strconv.Itoa(e.ID)}},
			Actors:    actors(e.GuestStars, 10, t),
		}
		seen := map[string]bool{}
		for _, c := range e.Crew {
			switch c.Job {
			case "Director":
				n.Directors = append(n.Directors, c.Name)
			case "Writer", "Screenplay", "Teleplay", "Story":
				if !seen[c.Name] {
					seen[c.Name] = true
					n.Credits = append(n.Credits, c.Name)
				}
			}
		}
		b, _ := xml.MarshalIndent(n, "", "  ")
		out = append(append(out, b...), '\n')
	}
	return out
}
