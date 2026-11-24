package library

import (
	"encoding/xml"
	"fmt"
	"strconv"

	"mvd/internal/lang"
	"mvd/internal/tmdb"
)

// Kodi-format movie NFO. Jellyfin, Emby and Infuse read the same thing.
type nfoMovie struct {
	XMLName       xml.Name      `xml:"movie"`
	Title         string        `xml:"title"`
	OriginalTitle string        `xml:"originaltitle,omitempty"`
	OriginalLang  string        `xml:"originallanguage,omitempty"` // Kodi v22's
	Year          int           `xml:"year,omitempty"`
	Premiered     string        `xml:"premiered,omitempty"`
	Plot          string        `xml:"plot,omitempty"`
	Tagline       string        `xml:"tagline,omitempty"`
	Runtime       int           `xml:"runtime,omitempty"`
	MPAA          string        `xml:"mpaa,omitempty"`
	Ratings       *nfoRatings   `xml:"ratings,omitempty"`
	UniqueIDs     []nfoUniqueID `xml:"uniqueid"`
	Genres        []string      `xml:"genre"`
	Countries     []string      `xml:"country"`
	Studios       []string      `xml:"studio"`
	Credits       []string      `xml:"credits"`
	Directors     []string      `xml:"director"`
	Set           *nfoSet       `xml:"set,omitempty"`
	Actors        []nfoActor    `xml:"actor"`
}

type nfoUniqueID struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr,omitempty"`
	Value   string `xml:",chardata"`
}

type nfoRatings struct {
	Rating struct {
		Name    string `xml:"name,attr"`
		Max     int    `xml:"max,attr"`
		Default bool   `xml:"default,attr"`
		Value   string `xml:"value"`
		Votes   int    `xml:"votes"`
	} `xml:"rating"`
}

type nfoSet struct {
	TMDBID   int    `xml:"tmdbcolid,attr,omitempty"` // Jellyfin reads this
	Name     string `xml:"name"`
	Overview string `xml:"overview,omitempty"`
}

type nfoActor struct {
	Name  string `xml:"name"`
	Role  string `xml:"role,omitempty"`
	Order int    `xml:"order"`
	Thumb string `xml:"thumb,omitempty"`
}

func buildNFO(m *tmdb.Movie, coll *tmdb.Collection, region string, t *tmdb.Client) []byte {
	n := nfoMovie{
		Title:     m.Title,
		Year:      m.Year(),
		Premiered: m.ReleaseDate,
		Plot:      m.Overview,
		Tagline:   m.Tagline,
		Runtime:   m.Runtime,
		MPAA:      m.Certification(region),
		UniqueIDs: []nfoUniqueID{{Type: "tmdb", Default: true, Value: strconv.Itoa(m.ID)}},
	}
	n.OriginalLang = lang.Code(m.OriginalLanguage)
	if m.OriginalTitle != m.Title {
		n.OriginalTitle = m.OriginalTitle
	}
	if m.IMDBID != "" {
		n.UniqueIDs = append(n.UniqueIDs, nfoUniqueID{Type: "imdb", Value: m.IMDBID})
	}
	if m.VoteCount > 0 {
		n.Ratings = &nfoRatings{}
		r := &n.Ratings.Rating
		r.Name, r.Max, r.Default = "themoviedb", 10, true
		r.Value = fmt.Sprintf("%.1f", m.VoteAverage)
		r.Votes = m.VoteCount
	}
	for _, g := range m.Genres {
		n.Genres = append(n.Genres, g.Name)
	}
	for _, c := range m.ProductionCountries {
		n.Countries = append(n.Countries, c.Name)
	}
	for i, c := range m.ProductionCompanies {
		if i >= 3 {
			break
		}
		n.Studios = append(n.Studios, c.Name)
	}

	seen := map[string]bool{}
	for _, c := range m.Credits.Crew {
		switch c.Job {
		case "Director":
			n.Directors = append(n.Directors, c.Name)
		case "Screenplay", "Writer", "Story", "Novel", "Author":
			if !seen[c.Name] {
				seen[c.Name] = true
				n.Credits = append(n.Credits, c.Name)
			}
		}
	}
	for i, a := range m.Credits.Cast {
		if i >= 20 {
			break
		}
		n.Actors = append(n.Actors, nfoActor{
			Name:  a.Name,
			Role:  a.Character,
			Order: a.Order,
			Thumb: t.ImageURL(a.ProfilePath, "w185"),
		})
	}
	if coll != nil {
		n.Set = &nfoSet{TMDBID: coll.ID, Name: coll.Name, Overview: coll.Overview}
	}

	out, _ := xml.MarshalIndent(n, "", "  ")
	return append([]byte(xml.Header), append(out, '\n')...)
}
