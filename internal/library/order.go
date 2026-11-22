package library

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"mvd/internal/config"
	"mvd/internal/tmdb"
)

// Alternative episode orders. TMDB calls them episode groups; a show can
// have several of each type (two DVD orders, say).
var groupTypes = map[string]int{
	"absolute": 2, "dvd": 3, "digital": 4, "arc": 5, "production": 6, "tv": 7,
}

var groupTypeNames = map[int]string{
	1: "aired", 2: "absolute", 3: "dvd", 4: "digital", 5: "arc", 6: "production", 7: "tv",
}

var groupIDRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

func ValidOrder(o string) bool {
	return o == "aired" || o == "pick" || groupTypes[o] != 0 || groupIDRe.MatchString(o)
}

// A show folder's own settings live in a small ".mvd" file:
//
//	order = dvd
//	group = 5b11ad7b0e0a265cda01b6d5
func readShowSettings(dir string) map[string]string {
	kv, _ := readKV(filepath.Join(dir, ".mvd"))
	return kv
}

// readKV reads a small key = value file (a .mvd).
func readKV(path string) (map[string]string, error) {
	kv := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return kv, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			kv[strings.TrimSpace(k)] = config.Value(v)
		}
	}
	return kv, sc.Err()
}

func (s *filer) writeShowSettings(dir string, kv map[string]string) error {
	p := filepath.Join(dir, ".mvd")
	if s.opt.DryRun {
		s.say("write %s", s.rel(p))
		return nil
	}
	var b strings.Builder
	b.WriteString("# mvd settings for this show\n")
	for _, k := range []string{"order", "group"} {
		if v := kv[k]; v != "" {
			fmt.Fprintf(&b, "%s = %s\n", k, v)
		}
	}
	return os.WriteFile(p, []byte(b.String()), 0o644)
}

// showOrder is a show's episodes renumbered into an alternative order.
type showOrder struct {
	name    string
	group   string // TMDB episode group id
	seasons map[int]*tmdb.Season
	regular []int
}

// resolveOrder works out which order a show uses: want (-order on the
// command line, or an import's hint), which is then saved, else the show's
// .mvd, else aired.
func (s *filer) resolveOrder(sh *show, tv *tmdb.TVShow, want string) (*showOrder, error) {
	kv := map[string]string{}
	if sh.dir != "" {
		kv = readShowSettings(sh.dir)
	}
	order, group := kv["order"], kv["group"]
	if o := want; o != "" {
		order, group = o, ""
		if groupIDRe.MatchString(o) {
			order, group = "custom", o
		}
	}
	if order == "" || order == "aired" {
		if want == "aired" && kv["order"] != "" && kv["order"] != "aired" && sh.dir != "" {
			return nil, s.writeShowSettings(sh.dir, map[string]string{"order": "aired"})
		}
		return nil, nil
	}

	if group == "" {
		var err error
		if group, err = s.pickGroup(tv, order); err != nil || group == "" {
			return nil, err
		}
	}
	g, err := s.tmdb.EpisodeGroup(group)
	if err != nil {
		return nil, fmt.Errorf("episode group %s: %v", group, err)
	}
	if want != "" && sh.dir != "" {
		if err := s.writeShowSettings(sh.dir, map[string]string{"order": order, "group": group}); err != nil {
			return nil, err
		}
	}
	return s.buildOrder(tv, g)
}

func (s *filer) pickGroup(tv *tmdb.TVShow, order string) (string, error) {
	all, err := s.tmdb.EpisodeGroups(tv.ID)
	if err != nil {
		return "", err
	}
	var cands []tmdb.EpisodeGroupInfo
	for _, g := range all {
		if order == "pick" || g.Type == groupTypes[order] {
			cands = append(cands, g)
		}
	}
	if len(cands) == 0 {
		var have []string
		for _, g := range all {
			have = append(have, fmt.Sprintf("%q (%s)", g.Name, groupTypeNames[g.Type]))
		}
		if len(have) == 0 {
			return "", fmt.Errorf("TMDB has no alternative orders for %s", tv.Name)
		}
		return "", fmt.Errorf("TMDB has no %s order for %s; it has %s", order, tv.Name, strings.Join(have, ", "))
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].EpisodeCount > cands[j].EpisodeCount })
	if len(cands) == 1 || s.ask == nil {
		return cands[0].ID, nil
	}
	a, err := s.ask(Question{Show: TitleYear(tv.Name, tv.Year()), Groups: cands})
	if err != nil {
		return "", nil
	}
	return a.Group, nil
}

var specialsRe = regexp.MustCompile(`(?i)special`)

// buildOrder renumbers a show's episodes through an episode group. Each
// group becomes a season ("Specials" is season 0) and episodes are numbered
// by their place in it. Full episode details (crew, guests) come from the
// aired seasons, which are cached anyway.
func (s *filer) buildOrder(tv *tmdb.TVShow, g *tmdb.EpisodeGroup) (*showOrder, error) {
	// Group orders usually run 1, 2, 3... (often with Specials as 0), but
	// some start their first real season at 0.
	offset := 0
	for _, grp := range g.Groups {
		if !specialsRe.MatchString(grp.Name) && grp.Order == 0 {
			offset = 1
		}
	}

	o := &showOrder{name: g.Name, group: g.ID, seasons: map[int]*tmdb.Season{}}
	aired := map[int]*tmdb.Season{}
	for _, grp := range g.Groups {
		n := grp.Order + offset
		if specialsRe.MatchString(grp.Name) {
			n = 0
		}
		se := o.seasons[n]
		if se == nil {
			se = &tmdb.Season{SeasonNumber: n, Name: grp.Name}
			o.seasons[n] = se
			if n > 0 {
				o.regular = append(o.regular, n)
			}
		}
		eps := append([]tmdb.GroupEpisode(nil), grp.Episodes...)
		sort.SliceStable(eps, func(i, j int) bool { return eps[i].Order < eps[j].Order })
		for _, ge := range eps {
			e := ge.Episode
			src, ok := aired[ge.SeasonNumber]
			if !ok {
				src, _ = s.tmdb.Season(tv.ID, ge.SeasonNumber)
				aired[ge.SeasonNumber] = src
			}
			if src != nil {
				if full := src.Episode(ge.EpisodeNumber); full != nil {
					e = *full
				}
			}
			e.SeasonNumber, e.EpisodeNumber = n, len(se.Episodes)+1
			se.Episodes = append(se.Episodes, e)
		}
	}
	sort.Ints(o.regular)
	return o, nil
}
