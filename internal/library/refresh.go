package library

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mvd/internal/config"
	"mvd/internal/tmdb"
)

// Refresher tidies the library in place: mvd refresh. It matches what has
// no NFO yet (everything, with -force), renames to the library's layout,
// and writes NFOs and artwork.
type Refresher struct {
	filer
	tv bool // the TV folder, not movies

	nNew, nDone, nSkip int
	nEps, nDup         int
}

// NewRefresher makes a Refresher for root, the movies or the TV folder.
func NewRefresher(o Options, cfg config.Config, tm *tmdb.Client, root string, tv bool) *Refresher {
	return &Refresher{filer: filer{opt: o, cfg: cfg, tmdb: tm, root: root}, tv: tv}
}

// Stats is what the run did.
func (s *Refresher) Stats() Stats {
	return Stats{Episodes: s.nEps, Duplicates: s.nDup, Failed: s.nErr, Changed: s.nNew}
}

// item is one movie on disk: a folder, or a loose file in the library root.
type item struct {
	dir    string   // movie folder; "" for a loose file
	videos []string // main video file(s), several for part1/part2 stacks
	others int      // further videos in the folder we're leaving alone
	alt    string   // the file's own title, when it differs from the folder's
	label  string
	parsed Parsed
}

func (s *Refresher) Run() int {
	s.sets = map[int]bool{}
	items, err := s.scan()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	items = slices.DeleteFunc(items, func(it *item) bool { return !s.wanted(it.label, it.parsed.Title) })
	if len(items) == 0 && s.opt.Name != "" {
		s.printf("no movie matches %q (for a show, use -show)\n", s.opt.Name)
		return 1
	}
	if s.opt.DryRun {
		s.println("dry run: nothing will be changed")
	}
	for _, it := range items {
		if s.process(it) {
			s.println("stopped: this and everything after it left as is")
			break
		}
		if s.fatal {
			s.println("stopping")
			break
		}
	}
	s.printf("\n%d updated, %d already done, %d skipped, %d failed\n", s.nNew, s.nDone, s.nSkip, s.nErr)
	if s.nErr > 0 {
		return 1
	}
	return 0
}

func (s *Refresher) scan() ([]*item, error) {
	ents, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	var items []*item
	for _, e := range ents {
		name := e.Name()
		if skipName(name) {
			continue
		}
		path := filepath.Join(s.root, name)
		if e.IsDir() {
			vids, others := mainVideos(path)
			if len(vids) == 0 {
				continue
			}
			it := &item{dir: path, videos: vids, others: others, label: name}
			it.parsed = parseMovieName(name)
			fp := parseMovieName(filepath.Base(vids[0]))
			if it.parsed.Year == 0 && TitleSim(it.parsed.Title, fp.Title) >= 0.8 {
				it.parsed.Year = fp.Year
			}
			if it.parsed.Edition == "" {
				it.parsed.Edition = fp.Edition
			}
			if fp.Title != "" && NormTitle(fp.Title) != NormTitle(it.parsed.Title) {
				it.alt = fp.Title
			}
			items = append(items, it)
		} else if e.Type().IsRegular() && isVideo(path) {
			items = append(items, &item{videos: []string{path}, label: name, parsed: parseMovieName(name)})
		}
	}
	return items, nil
}

// wanted applies -movie or -show: a loose, case- and punctuation-blind
// substring match on the folder name or the parsed title.
func (s *Refresher) wanted(label, title string) bool {
	if s.opt.Name == "" {
		return true
	}
	f := NormTitle(s.opt.Name)
	return strings.Contains(NormTitle(label), f) || strings.Contains(NormTitle(title), f)
}

// existingID looks for a TMDB (or IMDb) id in an NFO already beside the
// movie, from mvd or any other tool, so re-runs are exact lookups.
func (s *Refresher) existingID(it *item) (int, string) {
	dir := filepath.Dir(it.videos[0])
	cands := []string{videoBase(it.videos[0]) + ".nfo"}
	if it.dir != "" {
		cands = append(cands, "movie.nfo")
	}
	for _, c := range cands {
		b, err := os.ReadFile(filepath.Join(dir, c))
		if err != nil {
			continue
		}
		if m := tmdbIDRe.FindSubmatch(b); m != nil {
			for _, g := range m[1:] {
				if len(g) > 0 {
					return Atoi(string(g)), c
				}
			}
		}
		if m := imdbIDRe.FindSubmatch(b); m != nil {
			if id, err := s.tmdb.Find(string(m[1]), "imdb_id", false); err == nil {
				return id, c
			}
		}
		return 0, c
	}
	return 0, ""
}

// process handles one movie. Returns true if the user asked to quit.
func (s *Refresher) process(it *item) bool {
	id, nfoName := s.existingID(it)
	if id != 0 && !s.opt.Force && !s.opt.Refresh {
		s.nDone++
		return false
	}

	if id == 0 || s.opt.Force {
		var err error
		id, err = s.choose(it.label, it.parsed, it.alt, it.videos[0])
		if err != nil {
			s.fail(it, err)
			return false
		}
		if id <= 0 {
			return id < 0
		}
	}

	m, err := s.tmdb.Movie(id)
	if err != nil {
		s.fail(it, err)
		return false
	}
	s.printf("✓ %s  tmdb:%d\n", TitleYear(m.Title, m.Year()), m.ID)
	if err := s.apply(s.root, it, m, nfoName); err != nil {
		s.fail(it, err)
		return false
	}
	s.nNew++
	return false
}

func (s *Refresher) fail(it *item, err error) { s.failf(it.label, err) }
