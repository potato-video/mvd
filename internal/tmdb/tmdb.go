// Package tmdb is a small client for The Movie Database API, with a disk cache.
package tmdb

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mvd/internal/config"
)

// TMDB is a minimal client for the bits of the TMDB v3 API mvd needs.
// Responses are cached on disk, so re-runs cost no requests.
type Client struct {
	key      string
	bearer   bool // v4 read access token rather than v3 key
	lang     string
	region   string
	base     string
	imgBase  string
	cacheDir string
	ttl      time.Duration
	http     *http.Client
}

func New(c config.Config) *Client {
	return &Client{
		key:      c.APIKey,
		bearer:   strings.HasPrefix(c.APIKey, "eyJ"), // JWT
		lang:     c.Language,
		region:   c.Region,
		base:     "https://api.themoviedb.org/3",
		imgBase:  "https://image.tmdb.org/t/p/",
		cacheDir: c.CacheDir,
		ttl:      30 * 24 * time.Hour,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Notice is what TMDB's API terms ask anything that shows its data to say:
// the website's footer and mvd version say it.
const Notice = "This product uses the TMDB API but is not endorsed or certified by TMDB."

type SearchResult struct {
	ID            int      `json:"id"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title"`
	ReleaseDate   string   `json:"release_date"`
	Overview      string   `json:"overview"`
	Popularity    float64  `json:"popularity"`
	Countries     []string // TV only: origin_country
}

// SetBaseURLs points the client somewhere other than TMDB (a test server).
func (t *Client) SetBaseURLs(api, images string) { t.base, t.imgBase = api, images }

func (r SearchResult) Year() int { return YearOf(r.ReleaseDate) }

type named struct {
	Name string `json:"name"`
}

type Movie struct {
	ID                  int     `json:"id"`
	IMDBID              string  `json:"imdb_id"`
	Title               string  `json:"title"`
	OriginalTitle       string  `json:"original_title"`
	Overview            string  `json:"overview"`
	Tagline             string  `json:"tagline"`
	ReleaseDate         string  `json:"release_date"`
	Runtime             int     `json:"runtime"`
	VoteAverage         float64 `json:"vote_average"`
	VoteCount           int     `json:"vote_count"`
	PosterPath          string  `json:"poster_path"`
	BackdropPath        string  `json:"backdrop_path"`
	Genres              []named `json:"genres"`
	ProductionCompanies []named `json:"production_companies"`
	ProductionCountries []named `json:"production_countries"`
	Collection          *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"belongs_to_collection"`
	Credits struct {
		Cast []struct {
			Name        string `json:"name"`
			Character   string `json:"character"`
			Order       int    `json:"order"`
			ProfilePath string `json:"profile_path"`
		} `json:"cast"`
		Crew []struct {
			Name string `json:"name"`
			Job  string `json:"job"`
		} `json:"crew"`
	} `json:"credits"`
	ReleaseDates struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Dates   []struct {
				Certification string `json:"certification"`
				Type          int    `json:"type"`
			} `json:"release_dates"`
		} `json:"results"`
	} `json:"release_dates"`
}

func (m *Movie) Year() int { return YearOf(m.ReleaseDate) }

// Certification for the configured country, preferring the theatrical release.
func (m *Movie) Certification(country string) string {
	for _, r := range m.ReleaseDates.Results {
		if !strings.EqualFold(r.Country, country) {
			continue
		}
		best := ""
		for _, d := range r.Dates {
			if d.Certification == "" {
				continue
			}
			if d.Type == 3 {
				return d.Certification
			}
			if best == "" {
				best = d.Certification
			}
		}
		return best
	}
	return ""
}

type Collection struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
}

func (t *Client) SearchMovie(query string, year int) ([]SearchResult, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var r struct {
		Results []SearchResult `json:"results"`
	}
	err := t.get("/search/movie", q, &r)
	return r.Results, err
}

func (t *Client) Movie(id int) (*Movie, error) {
	var m Movie
	q := url.Values{"append_to_response": {"credits,release_dates"}}
	if err := t.get("/movie/"+strconv.Itoa(id), q, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (t *Client) Collection(id int) (*Collection, error) {
	var c Collection
	if err := t.get("/collection/"+strconv.Itoa(id), nil, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Find maps an outside id to a TMDB id: source is "imdb_id" (tt0119116) or
// "tvdb_id"; tv picks the show results rather than the movie ones.
func (t *Client) Find(id, source string, tv bool) (int, error) {
	type hit struct {
		ID int `json:"id"`
	}
	var r struct {
		Movies []hit `json:"movie_results"`
		Shows  []hit `json:"tv_results"`
	}
	if err := t.get("/find/"+id, url.Values{"external_source": {source}}, &r); err != nil {
		return 0, err
	}
	hits := r.Movies
	if tv {
		hits = r.Shows
	}
	if len(hits) == 0 {
		return 0, fmt.Errorf("nothing on TMDB for %s", id)
	}
	return hits[0].ID, nil
}

func (t *Client) ImageURL(path, size string) string {
	if path == "" {
		return ""
	}
	return t.imgBase + size + path
}

// OpenImage fetches a TMDB image to pass on as it comes (the website's
// import dialog), saying its type. The caller closes it.
func (t *Client) OpenImage(path, size string) (io.ReadCloser, string, error) {
	if path == "" {
		return nil, "", fmt.Errorf("no image")
	}
	resp, err := t.http.Get(t.ImageURL(path, size))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, "", fmt.Errorf("image %s: HTTP %d", path, resp.StatusCode)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// Limits on what's read from TMDB: its answers are at most a few hundred
// KB, and posters and fanart a few MB. Past these, something's wrong, and
// the router shouldn't pay for it.
const (
	maxAnswer = 8 << 20
	maxImage  = 32 << 20
)

// maxRetryWait caps a wait TMDB asks for (Retry-After) when it's
// rate-limiting: an import waits holding the import lock. Tests shorten it.
var maxRetryWait = 10 * time.Second

// errTooBig is an answer or image past its limit.
var errTooBig = errors.New("more than TMDB ever sends")

// DownloadImage fetches a TMDB image to dest, writing via a temp file so a
// failed download never leaves a truncated image behind.
func (t *Client) DownloadImage(path, size, dest string) error {
	resp, err := t.http.Get(t.ImageURL(path, size))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("image %s: HTTP %d", path, resp.StatusCode)
	}
	tmp := dest + ".mvdtmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxImage+1))
	if err == nil && n > maxImage {
		err = fmt.Errorf("image %s: %w", path, errTooBig)
	}
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

func (t *Client) get(path string, q url.Values, out any) error {
	if q == nil {
		q = url.Values{}
	}
	if q.Get("language") == "" {
		q.Set("language", t.lang)
	}
	cacheKey := path + "?" + q.Encode() // before the key is added

	if body, ok := t.cacheGet(cacheKey); ok {
		if json.Unmarshal(body, out) == nil {
			return nil
		}
	}

	if !t.bearer {
		q.Set("api_key", t.key)
	}
	u := t.base + path + "?" + q.Encode()

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if t.bearer {
			req.Header.Set("Authorization", "Bearer "+t.key)
		}
		resp, err := t.http.Do(req)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err // the URL carries the API key; keep it out of messages
			}
			return FatalError{fmt.Errorf("can't reach TMDB: %v", err)}
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
		resp.Body.Close()
		if err == nil && len(body) > maxAnswer {
			err = fmt.Errorf("TMDB %s: %w", path, errTooBig)
		}
		if err != nil {
			return err
		}
		switch {
		case resp.StatusCode == 429 && attempt < 3:
			wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			time.Sleep(min(time.Duration(max(wait, 1))*time.Second, maxRetryWait))
			continue
		case resp.StatusCode == 401:
			return FatalError{fmt.Errorf("TMDB rejected the API key")}
		case resp.StatusCode != 200:
			return fmt.Errorf("TMDB %s: HTTP %d", path, resp.StatusCode)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("TMDB %s: %v", path, err)
		}
		t.cachePut(cacheKey, body)
		return nil
	}
}

func (t *Client) cachePath(key string) string {
	h := sha1.Sum([]byte(key))
	return filepath.Join(t.cacheDir, "tmdb", hex.EncodeToString(h[:])+".json")
}

func (t *Client) cacheGet(key string) ([]byte, bool) {
	if t.cacheDir == "" {
		return nil, false
	}
	p := t.cachePath(key)
	st, err := os.Stat(p)
	if err != nil || time.Since(st.ModTime()) > t.ttl {
		return nil, false
	}
	b, err := os.ReadFile(p)
	return b, err == nil
}

// PruneCache deletes cached responses past their expiry. Entries are only
// ever reused within the TTL, so anything older is dead weight; pruning on
// every run keeps the cache to roughly one TTL's worth of lookups.
func (t *Client) PruneCache() {
	if t.cacheDir == "" {
		return
	}
	dir := filepath.Join(t.cacheDir, "tmdb")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		info, err := e.Info()
		if err == nil && time.Since(info.ModTime()) > t.ttl {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func (t *Client) cachePut(key string, body []byte) {
	if t.cacheDir == "" {
		return
	}
	p := t.cachePath(key)
	if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
		os.WriteFile(p, body, 0o644)
	}
}

// FatalError is a problem that will hit every movie (no network, bad key),
// so the run stops rather than failing them one by one.
type FatalError struct{ error }

func (e FatalError) Unwrap() error { return e.error }

func YearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(date[:4])
	return y
}
