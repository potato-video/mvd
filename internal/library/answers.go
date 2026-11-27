package library

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"mvd/internal/tmdb"
)

// What the website's Needs you (on the Incoming page) needs to answer what an import couldn't
// settle: what's in an item, a search, a lookup, and the hint that settles
// it for the next import.

// ItemSummary is what's in an import item, read without asking TMDB: what
// importing it would file.
type ItemSummary struct {
	Movies      int
	Episodes    int
	First, Last string // TV: the first and last episodes, "S01E01" and "S01E08"
	Unknown     int    // videos it can't tell are movies or episodes
}

// PlanItem reads an import item. kind is "movie" or "tv" for an item in
// import/movies or import/tv, otherwise ""; the item's hint can settle it
// too.
func PlanItem(path, kind string) (ItemSummary, error) {
	st, err := os.Stat(path)
	if err != nil {
		return ItemSummary{}, err
	}
	if h := readHint(path, st.IsDir()); h.kind != "" {
		kind = h.kind
	}
	p := planItem(path, st.IsDir(), kind)
	sum := ItemSummary{Movies: len(p.movies), Unknown: len(p.unknown)}
	first, last := [2]int{-1, -1}, [2]int{-1, -1}
	for _, sh := range p.shows {
		for _, f := range sh.eps {
			sum.Episodes += len(f.eps)
			if f.season < 0 || len(f.eps) == 0 {
				continue
			}
			a, b := [2]int{f.season, f.eps[0]}, [2]int{f.season, f.eps[len(f.eps)-1]}
			if first[0] < 0 || a[0] < first[0] || a[0] == first[0] && a[1] < first[1] {
				first = a
			}
			if b[0] > last[0] || b[0] == last[0] && b[1] > last[1] {
				last = b
			}
		}
	}
	if first[0] >= 0 {
		sum.First = fmt.Sprintf("S%02dE%02d", first[0], first[1])
		sum.Last = fmt.Sprintf("S%02dE%02d", last[0], last[1])
	}
	return sum, nil
}

// Search searches TMDB for movies or shows (kind "tv"), as mvd import's
// [e]: "Title" or "Title (Year)". The best match comes first.
func Search(tm *tmdb.Client, kind, query string) ([]Candidate, error) {
	f := &filer{tmdb: tm}
	return f.search(kind == "tv", parseMovieName(query))
}

var (
	tmdbURLRe = regexp.MustCompile(`themoviedb\.org/(movie|tv)/(\d+)`)
	imdbRefRe = regexp.MustCompile(`^(?:https?://(?:www\.|m\.)?imdb\.com/title/)?(tt\d{7,9})\b`)
	tmdbRefRe = regexp.MustCompile(`^(?:tmdb:)?(\d+)$`)
)

// IsReference says whether s names a title rather than searching for one:
// a TMDB id, an IMDb id, or a TMDB or IMDb page's address.
func IsReference(s string) bool {
	s = strings.TrimSpace(s)
	return tmdbURLRe.MatchString(s) || imdbRefRe.MatchString(s) || tmdbRefRe.MatchString(s)
}

// Lookup finds the TMDB id a reference means: a TMDB id ("87108" or
// "tmdb:87108"), an IMDb id ("tt7366338"), or a TMDB or IMDb page's
// address. kind is "movie" or "tv", or "" if it isn't known; the kind it
// found is returned with the id.
func Lookup(tm *tmdb.Client, kind, ref string) (int, string, error) {
	ref = strings.TrimSpace(ref)
	if m := tmdbURLRe.FindStringSubmatch(ref); m != nil {
		return Atoi(m[2]), m[1], nil
	}
	if m := imdbRefRe.FindStringSubmatch(ref); m != nil {
		kinds := []string{kind}
		if kind == "" {
			kinds = []string{"movie", "tv"}
		}
		var err error
		for _, k := range kinds {
			var id int
			if id, err = tm.Find(m[1], "imdb_id", k == "tv"); err == nil {
				return id, k, nil
			}
		}
		return 0, "", err
	}
	if m := tmdbRefRe.FindStringSubmatch(ref); m != nil && kind != "" {
		return Atoi(m[1]), kind, nil
	}
	if kind == "" {
		return 0, "", errors.New("say whether it's a movie or TV, or give a TMDB or IMDb address")
	}
	return 0, "", fmt.Errorf("%q isn't a TMDB id, an IMDb id, or a TMDB or IMDb address", ref)
}

// WriteHint settles what an import item is, for the next import: its .mvd
// (inside a folder, or "Name.mvd" beside a file) gets the kind and TMDB id,
// keeping any episode order and episode answers it already has for that
// title.
func WriteHint(item, kind string, id int) error {
	st, err := os.Stat(item)
	if err != nil {
		return err
	}
	if kind != "movie" && kind != "tv" {
		return fmt.Errorf("kind %q: want movie or tv", kind)
	}
	h := readHint(item, st.IsDir())
	if kind != "tv" {
		h.order = ""
	}
	if kind != "tv" || h.tmdb != id {
		h.episodes = nil
	}
	h.kind, h.tmdb = kind, id
	return h.write(item, st.IsDir())
}

// WriteEpisodeHint settles which episode one of an item's videos is, for
// the next import: the item's hint gets the show and a line for the video.
// An answer for another show than the hint's starts it over.
func WriteEpisodeHint(item, video string, show, season, episode int) error {
	st, err := os.Stat(item)
	if err != nil {
		return err
	}
	if show <= 0 || season < 0 || episode <= 0 {
		return fmt.Errorf("which episode? (a show's TMDB id, a season and an episode)")
	}
	rel, ok := hintRel(item, st.IsDir(), video)
	if !ok {
		return fmt.Errorf("%s isn't in %s", video, filepath.Base(item))
	}
	h := readHint(item, st.IsDir())
	if h.kind != "tv" || h.tmdb != show {
		h.order, h.episodes = "", nil
	}
	h.kind, h.tmdb = "tv", show
	if h.episodes == nil {
		h.episodes = map[string][2]int{}
	}
	h.episodes[rel] = [2]int{season, episode}
	return h.write(item, st.IsDir())
}

// hintRel is how a hint names a video in its item: its path inside the
// item's folder, or a lone file's name.
func hintRel(item string, isDir bool, video string) (string, bool) {
	if !isDir {
		return filepath.Base(item), filepath.Clean(video) == filepath.Clean(item)
	}
	rel, err := filepath.Rel(item, video)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// write puts the hint in the item's .mvd.
func (h hint) write(item string, isDir bool) error {
	p := h.path
	if p == "" {
		p = filepath.Join(item, ".mvd")
		if !isDir {
			p = strings.TrimSuffix(item, filepath.Ext(item)) + ".mvd"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "kind = %q\ntmdb = %d\n", h.kind, h.tmdb)
	if h.order != "" {
		fmt.Fprintf(&b, "order = %q\n", h.order)
	}
	if len(h.episodes) > 0 {
		b.WriteString("\n# which episode each of these is\n[episodes]\n")
		for _, rel := range slices.Sorted(maps.Keys(h.episodes)) {
			e := h.episodes[rel]
			fmt.Fprintf(&b, "%s = \"S%02dE%02d\"\n", strconv.Quote(rel), e[0], e[1])
		}
	}
	return os.WriteFile(p, []byte(b.String()), 0o644)
}
