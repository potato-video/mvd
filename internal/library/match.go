package library

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"mvd/internal/tmdb"
)

// Candidate is a TMDB search hit with a score against what's on disk.
type Candidate struct {
	tmdb.SearchResult
	Score float64
}

const (
	autoScore  = 0.85 // best match needs at least this...
	autoMargin = 0.10 // ...and this lead over the runner-up
)

// queryVariants turns a parsed title into search queries. Hand-made names
// like "FF3 - The Fast and the Furious - Tokyo Drift" need a few tries.
func queryVariants(title string) []string {
	v := []string{title}
	if parts := strings.Split(title, " - "); len(parts) > 1 {
		v = append(v, strings.Join(parts, " "))
		if len([]rune(parts[0])) <= 4 { // a short prefix tag: "FF3", "MCU"
			v = append(v, strings.Join(parts[1:], " "))
		}
	}
	seen := map[string]bool{}
	out := v[:0]
	for _, q := range v {
		if q != "" && !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	return out
}

// search runs every query variant (with the year, then without if that
// finds nothing) and scores the merged results.
func (s *filer) search(tv bool, p Parsed, alts ...string) ([]Candidate, error) {
	queries := queryVariants(p.Title)
	for _, a := range alts {
		queries = append(queries, queryVariants(a)...)
	}
	byID := map[int]tmdb.SearchResult{}
	run := func(year int) error {
		for _, q := range queries {
			search := s.tmdb.SearchMovie
			if tv {
				search = s.tmdb.SearchTV
			}
			rs, err := search(q, year)
			if err != nil {
				return err
			}
			for i, r := range rs {
				if i >= 10 {
					break
				}
				byID[r.ID] = r
			}
		}
		return nil
	}
	if err := run(p.Year); err != nil {
		return nil, err
	}
	if len(byID) == 0 && p.Year != 0 {
		if err := run(0); err != nil {
			return nil, err
		}
	}

	cs := make([]Candidate, 0, len(byID))
	for _, r := range byID {
		sc := score(queries, p.Year, r)
		if p.Country != "" && hasCountry(r.Countries, p.Country) {
			sc = min(1, sc+0.15) // "The Office (US)"
		}
		cs = append(cs, Candidate{r, sc})
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Score != cs[j].Score {
			return cs[i].Score > cs[j].Score
		}
		return cs[i].Popularity > cs[j].Popularity
	})
	return cs, nil
}

func hasCountry(cs []string, c string) bool {
	if c == "UK" {
		c = "GB"
	}
	for _, x := range cs {
		if strings.EqualFold(x, c) {
			return true
		}
	}
	return false
}

func score(queries []string, year int, r tmdb.SearchResult) float64 {
	ts := 0.0
	for _, q := range queries {
		ts = max(ts, TitleSim(q, r.Title), TitleSim(q, r.OriginalTitle))
	}
	ys := 0.5 // no year to go on: neutral
	if year != 0 {
		switch d := year - r.Year(); {
		case d == 0:
			ys = 1
		case d == 1 || d == -1:
			ys = 0.6
		default:
			ys = 0
		}
	}
	return 0.75*ts + 0.25*ys
}

func confident(cs []Candidate, year int) bool {
	if len(cs) == 0 {
		return false
	}
	// A lone hit in exactly the right year is good evidence even when the
	// title is loose ("Fast 5" for Fast Five).
	if len(cs) == 1 && year != 0 && cs[0].Year() == year && cs[0].Score >= 0.7 {
		return true
	}
	if cs[0].Score < autoScore {
		return false
	}
	return len(cs) == 1 || cs[0].Score-cs[1].Score >= autoMargin-1e-9
}

// NormTitle lowercases, drops a leading article and reduces punctuation to
// spaces, so "Mission: Impossible" and "Mission - Impossible" agree.
func NormTitle(s string) string {
	s = strings.ToLower(cleanRunes(s))
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	f := strings.Fields(s)
	if len(f) > 1 && (f[0] == "the" || f[0] == "a" || f[0] == "an") {
		f = f[1:]
	}
	return strings.Join(f, " ")
}

func TitleSim(a, b string) float64 {
	a, b = NormTitle(a), NormTitle(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	return max(levRatio(a, b), tokenDice(a, b))
}

func levRatio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(max(len(ra), len(rb)))
}

func tokenDice(a, b string) float64 {
	ta, tb := strings.Fields(a), strings.Fields(b)
	set := map[string]int{}
	for _, t := range ta {
		set[t]++
	}
	common := 0
	for _, t := range tb {
		if set[t] > 0 {
			set[t]--
			common++
		}
	}
	return 2 * float64(common) / float64(len(ta)+len(tb))
}

// tieBreakRuntime settles a close call between same-titled movies (remakes,
// reboots) by comparing the file's duration with each candidate's runtime.
// Needs ffprobe; quietly does nothing without it.
func (s *filer) tieBreakRuntime(cs []Candidate, video string) (int, bool) {
	mins := probeMinutes(video)
	if mins == 0 {
		return 0, false
	}
	match := -1
	for i, c := range cs {
		if i >= 3 || c.Score < autoScore {
			break
		}
		m, err := s.tmdb.Movie(c.ID)
		if err != nil || m.Runtime == 0 {
			continue
		}
		if abs(m.Runtime-mins) <= max(5, m.Runtime/12) {
			if match >= 0 {
				return 0, false // still ambiguous
			}
			match = i
		}
	}
	return match, match >= 0
}

// probeMinutes is a video's length in minutes, 0 if unknown.
func probeMinutes(path string) int {
	mf, err := Probe(path)
	if err != nil {
		return 0
	}
	return int(mf.Duration/60 + 0.5)
}

// match matches a parsed name against TMDB without asking: automatically
// when confident, or by runtime when that settles it (movies, given a
// video). Returns a TMDB id (0 if it isn't sure) and the candidates.
func (s *filer) match(tv bool, p Parsed, alt, video string) (int, []Candidate, error) {
	var alts []string
	if alt != "" {
		alts = append(alts, alt)
	}
	cs, err := s.search(tv, p, alts...)
	if err != nil {
		return 0, nil, err
	}
	if confident(cs, p.Year) {
		return cs[0].ID, cs, nil
	}
	if video != "" && !tv {
		if i, ok := s.tieBreakRuntime(cs, video); ok {
			return cs[i].ID, cs, nil
		}
	}
	return 0, cs, nil
}

// choose is match, then asking when it isn't sure. Returns a TMDB id, 0 if
// skipped (and counted), -1 to quit.
func (s *Refresher) choose(label string, p Parsed, alt, video string) (int, error) {
	id, cs, err := s.match(s.tv, p, alt, video)
	if err != nil || id != 0 {
		return id, err
	}
	if s.ask == nil {
		s.printf("- %s: no confident match, skipped\n", label)
		s.nSkip++
		return 0, nil
	}
	id, err = s.askMatch(s.tv, label, p, cs)
	if err == nil && id == 0 {
		s.nSkip++
	}
	return id, err
}

// askMatch asks which of the candidates something is, if there's anyone to
// ask. Returns a TMDB id, 0 to skip, or -1 to quit.
func (s *filer) askMatch(tv bool, label string, p Parsed, cs []Candidate) (int, error) {
	if s.ask == nil {
		return 0, nil
	}
	kind := "movie"
	if tv {
		kind = "tv"
	}
	a, err := s.ask(Question{Label: label, Kind: kind, Parsed: p, Candidates: cs,
		search: func(p Parsed) ([]Candidate, error) { return s.search(tv, p) },
		find:   func(id string) (int, error) { return s.tmdb.Find(id, "imdb_id", tv) },
	})
	switch {
	case err != nil:
		return 0, err
	case a.Quit:
		return -1, nil
	}
	return a.TMDB, nil
}

func TitleYear(title string, year int) string {
	if year == 0 {
		return title
	}
	return fmt.Sprintf("%s (%d)", title, year)
}

func Clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
