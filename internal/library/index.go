package library

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The library as the server sees it, read from the folders and NFOs that
// the scraper writes. Rebuilt on startup and on /api/rescan.

type Sub struct {
	Lang   string `json:"lang,omitempty"`
	Forced bool   `json:"forced,omitempty"`
	SDH    bool   `json:"sdh,omitempty"`
	Format string `json:"format"`
	Path   string `json:"path"`
}

type Movie struct {
	ID        string
	Title     string
	SortTitle string
	Year      int
	Plot      string
	Tagline   string
	Runtime   int
	Rating    string
	Genres    []string
	Directors []string
	Set       string
	Dir       string
	Files     []string     // several for part1/part2 stacks
	Media     []*MediaFile `json:",omitempty"` // Files' probes, nil where not probed
	Bytes     int64        `json:",omitempty"` // Files' size, together
	Subs      []Sub
	Added     time.Time
	Poster    string
	Fanart    string
}

type Show struct {
	ID        string
	Title     string
	SortTitle string
	Year      int
	Plot      string
	Rating    string
	Status    string
	Genres    []string
	Dir       string
	Seasons   []*Season
	Poster    string
	Fanart    string
}

type Season struct {
	Number   int
	Name     string
	Episodes []*Episode
	Poster   string
}

type Episode struct {
	ID       string
	Show     *Show `json:"-"` // restored on load
	Season   int
	Episodes []int
	Title    string
	Plot     string
	Aired    string
	Runtime  int
	File     string
	Media    *MediaFile `json:",omitempty"` // File's probe, nil if not probed
	Bytes    int64      `json:",omitempty"` // File's size
	Subs     []Sub
	Added    time.Time
	Thumb    string
}

type Index struct {
	Movies []*Movie
	Shows  []*Show
	movie  map[string]*Movie
	show   map[string]*Show
	ep     map[string]*Episode
	bytes  int64     // every movie's and episode's Bytes
	Built  time.Time `json:"-"`
}

// Movie, Show and Episode look an item up by id; nil if there isn't one.
func (ix *Index) Movie(id string) *Movie     { return ix.movie[id] }
func (ix *Index) Show(id string) *Show       { return ix.show[id] }
func (ix *Index) Episode(id string) *Episode { return ix.ep[id] }
func (ix *Index) EpisodeCount() int          { return len(ix.ep) }

// Bytes is the size of the library's videos.
func (ix *Index) Bytes() int64 { return ix.bytes }

func (ix *Index) taken(id string) bool {
	return ix.movie[id] != nil || ix.show[id] != nil || ix.ep[id] != nil
}

// uniqueID keeps ids stable (TMDB-based) and unique: a second copy of the
// same movie falls back to a hash of its path.
func (ix *Index) uniqueID(prefix string, tmdb int, rel string) string {
	if tmdb > 0 {
		if id := prefix + strconv.Itoa(tmdb); !ix.taken(id) {
			return id
		}
	}
	h := sha1.Sum([]byte(rel))
	return "f" + hex.EncodeToString(h[:6])
}

// Save writes the index for a quick start next time, skipping the write when
// nothing changed (it may well live on flash).
func (ix *Index) Save(path string) error {
	b, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return WriteFileAtomic(path, b, 0o644)
}

func LoadIndex(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ix := &Index{}
	if err := json.Unmarshal(b, ix); err != nil {
		return nil, err
	}
	ix.movie, ix.show, ix.ep = map[string]*Movie{}, map[string]*Show{}, map[string]*Episode{}
	for _, m := range ix.Movies {
		ix.movie[m.ID] = m
		ix.bytes += m.Bytes
	}
	for _, sh := range ix.Shows {
		ix.show[sh.ID] = sh
		for _, se := range sh.Seasons {
			for _, e := range se.Episodes {
				e.Show = sh
				ix.ep[e.ID] = e
				ix.bytes += e.Bytes
			}
		}
	}
	if st, err := os.Stat(path); err == nil {
		ix.Built = st.ModTime()
	}
	return ix, nil
}

// BuildIndex reads the library. known is what's been probed already
// (usually the last index's Probed): a file's probe is kept while its size
// and modification time are the same.
func BuildIndex(moviesDir, tvDir string, known Probed) *Index {
	ix := &Index{movie: map[string]*Movie{}, show: map[string]*Show{},
		ep: map[string]*Episode{}, Built: time.Now()}
	if moviesDir != "" {
		ix.indexMovies(moviesDir, known)
	}
	if tvDir != "" {
		ix.indexShows(tvDir, known)
	}
	sort.Slice(ix.Movies, func(i, j int) bool { return ix.Movies[i].SortTitle < ix.Movies[j].SortTitle })
	sort.Slice(ix.Shows, func(i, j int) bool { return ix.Shows[i].SortTitle < ix.Shows[j].SortTitle })
	return ix
}

func (ix *Index) indexMovies(root string, known Probed) {
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if !e.IsDir() || skipName(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		files, _ := mainVideos(dir)
		if len(files) == 0 {
			continue
		}
		m := &Movie{Dir: dir, Files: files}
		var n nfoMovie
		nfo := filepath.Join(dir, videoBase(files[0])+".nfo")
		if !Exists(nfo) {
			nfo = filepath.Join(dir, "movie.nfo")
		}
		tmdb := 0
		if b, err := os.ReadFile(nfo); err == nil && xml.Unmarshal(b, &n) == nil && n.Title != "" {
			m.Title, m.Year, m.Plot, m.Tagline = n.Title, n.Year, n.Plot, n.Tagline
			m.Runtime, m.Rating, m.Genres, m.Directors = n.Runtime, n.MPAA, n.Genres, n.Directors
			if n.Set != nil {
				m.Set = n.Set.Name
			}
			tmdb = nfoTMDB(n.UniqueIDs, b)
		} else {
			p := parseMovieName(e.Name())
			m.Title, m.Year = p.Title, p.Year
		}
		m.SortTitle = NormTitle(m.Title)
		m.ID = ix.uniqueID("m", tmdb, filepath.Join("movies", e.Name()))
		m.Subs = findSubs(files[0], nil)
		m.Poster, m.Fanart = ifExists(dir, "poster.jpg"), ifExists(dir, "fanart.jpg")
		for i, f := range files {
			st, err := os.Stat(f)
			if err != nil {
				continue
			}
			if i == 0 {
				m.Added = st.ModTime()
			}
			m.Bytes += st.Size()
			if mf := known.still(f, st); mf != nil {
				if m.Media == nil {
					m.Media = make([]*MediaFile, len(files))
				}
				m.Media[i] = mf
			}
		}
		ix.Movies = append(ix.Movies, m)
		ix.movie[m.ID] = m
		ix.bytes += m.Bytes
	}
}

func (ix *Index) indexShows(root string, known Probed) {
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if !e.IsDir() || skipName(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		files, _ := collectEpisodes(dir, -1)
		if len(files) == 0 {
			continue
		}

		sh := &Show{Dir: dir}
		tmdb := 0
		var n nfoShow
		if b, err := os.ReadFile(filepath.Join(dir, "tvshow.nfo")); err == nil && xml.Unmarshal(b, &n) == nil && n.Title != "" {
			sh.Title, sh.Year, sh.Plot, sh.Rating = n.Title, n.Year, n.Plot, n.MPAA
			sh.Status, sh.Genres = n.Status, n.Genres
			tmdb = nfoTMDB(n.UniqueIDs, b)
		} else {
			p := parseShowName(e.Name())
			sh.Title, sh.Year = p.Title, p.Year
		}
		sh.SortTitle = NormTitle(sh.Title)
		sh.ID = ix.uniqueID("s", tmdb, filepath.Join("tv", e.Name()))
		sh.Poster, sh.Fanart = ifExists(dir, "poster.jpg"), ifExists(dir, "fanart.jpg")
		names := map[int]string{}
		for _, ns := range n.NamedSeasons {
			names[ns.Number] = ns.Name
		}

		seasons := map[int]*Season{}
		listed := map[string][]os.DirEntry{} // each folder once, not once an episode
		for _, f := range files {
			ep := &Episode{Show: sh, Season: f.season, Episodes: f.eps, File: f.path}
			epTMDB := 0
			if eps := readEpisodeNFO(filepath.Join(filepath.Dir(f.path), videoBase(f.path)+".nfo")); len(eps) > 0 {
				ep.Season = eps[0].Season
				ep.Episodes = nil
				var titles []string
				for _, x := range eps {
					ep.Episodes = append(ep.Episodes, x.Episode)
					titles = append(titles, x.Title)
				}
				ep.Title = strings.Join(titles, " & ")
				ep.Plot, ep.Aired, ep.Runtime = eps[0].Plot, eps[0].Aired, eps[0].Runtime
				epTMDB = nfoTMDB(eps[0].UniqueIDs, nil)
			}
			if ep.Season < 0 {
				ep.Season = 1
			}
			rel, _ := filepath.Rel(root, f.path)
			ep.ID = ix.uniqueID("e", epTMDB, filepath.Join("tv", rel))
			d := filepath.Dir(f.path)
			ents, ok := listed[d]
			if !ok {
				ents, _ = os.ReadDir(d)
				listed[d] = ents
			}
			ep.Subs = findSubs(f.path, ents)
			ep.Thumb = ifExists(filepath.Dir(f.path), videoBase(f.path)+"-thumb.jpg")
			if st, err := os.Stat(f.path); err == nil {
				ep.Added = st.ModTime()
				ep.Bytes = st.Size()
				ix.bytes += ep.Bytes
				ep.Media = known.still(f.path, st)
			}
			se := seasons[ep.Season]
			if se == nil {
				se = &Season{Number: ep.Season, Name: names[ep.Season],
					Poster: ifExists(dir, seasonPosterName(ep.Season))}
				seasons[ep.Season] = se
			}
			se.Episodes = append(se.Episodes, ep)
			ix.ep[ep.ID] = ep
		}
		for _, se := range seasons {
			sort.Slice(se.Episodes, func(i, j int) bool { return se.Episodes[i].Episodes[0] < se.Episodes[j].Episodes[0] })
			sh.Seasons = append(sh.Seasons, se)
		}
		// Regular seasons first, specials last.
		sort.Slice(sh.Seasons, func(i, j int) bool {
			a, b := sh.Seasons[i].Number, sh.Seasons[j].Number
			if (a == 0) != (b == 0) {
				return b == 0
			}
			return a < b
		})
		ix.Shows = append(ix.Shows, sh)
		ix.show[sh.ID] = sh
	}
}

// Episodes in watching order: every regular season, specials excluded.
func (sh *Show) InOrder() []*Episode {
	var out []*Episode
	for _, se := range sh.Seasons {
		if se.Number > 0 {
			out = append(out, se.Episodes...)
		}
	}
	return out
}

func ifExists(dir, name string) string {
	if p := filepath.Join(dir, name); Exists(p) {
		return p
	}
	return ""
}

func readEpisodeNFO(path string) []nfoEpisode {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []nfoEpisode
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		var n nfoEpisode
		if err := d.Decode(&n); err != nil {
			if err != io.EOF && len(out) == 0 {
				return nil
			}
			return out
		}
		out = append(out, n)
	}
}

func nfoTMDB(ids []nfoUniqueID, raw []byte) int {
	for _, u := range ids {
		if u.Type == "tmdb" {
			return Atoi(strings.TrimSpace(u.Value))
		}
	}
	if raw != nil {
		if id := firstGroup(tmdbIDRe.FindSubmatch(raw)); id != "" {
			return Atoi(id)
		}
	}
	return 0
}

func isSubtitle(p string) bool { return subExts[strings.ToLower(filepath.Ext(p))] != "" }

var subExts = map[string]string{".srt": "srt", ".vtt": "vtt", ".ass": "ass", ".ssa": "ass", ".sub": "sub", ".sup": "sup"}

// findSubs lists a video's sidecar subtitles: "Base.en.srt",
// "Base.en.forced.srt", "Base.srt".
// findSubs: ents is the video's folder, listed (nil to list it here).
func findSubs(video string, ents []os.DirEntry) []Sub {
	base, dir := videoBase(video), filepath.Dir(video)
	if ents == nil {
		ents, _ = os.ReadDir(dir)
	}
	var out []Sub
	for _, p := range sidecarsIn(ents, dir, base, video) {
		name := filepath.Base(p)
		ext := strings.ToLower(filepath.Ext(name))
		format, ok := subExts[ext]
		if !ok {
			continue
		}
		sub := Sub{Format: format, Path: p}
		tags := strings.Split(strings.Trim(strings.TrimSuffix(name[len(base):], filepath.Ext(name)), "."), ".")
		for _, t := range tags {
			switch t = strings.ToLower(t); t {
			case "":
			case "forced":
				sub.Forced = true
			case "sdh", "cc", "hi":
				sub.SDH = true
			default:
				if sub.Lang == "" {
					sub.Lang = t
				}
			}
		}
		out = append(out, sub)
	}
	return out
}
