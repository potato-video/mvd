package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"mvd/internal/tmdb"
)

// show is one series on disk: a folder, or episodes loose in the TV root.
type show struct {
	dir     string // show folder; "" for loose episodes
	label   string
	parsed  Parsed
	alt     string // title from the episode file names, if different
	eps     []*epFile
	unknown []string // videos we couldn't put a number to
	fresh   bool     // importing: every episode is new, whatever NFOs are about
}

type epFile struct {
	path   string
	season int // -1: not known yet
	eps    []int
}

func (s *Refresher) RunTV() int {
	shows, err := s.scanTV()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	shows = slices.DeleteFunc(shows, func(sh *show) bool { return !s.wanted(sh.label, sh.parsed.Title) })
	if len(shows) == 0 && s.opt.Name != "" {
		s.printf("no show matches %q (for a movie, use -movie)\n", s.opt.Name)
		return 1
	}
	if s.opt.DryRun {
		s.println("dry run: nothing will be changed")
	}
	for _, sh := range shows {
		if s.processShow(sh) {
			s.println("stopped: this and everything after it left as is")
			break
		}
		if s.fatal {
			s.println("stopping")
			break
		}
	}
	s.printf("\n%d shows updated (%d episodes), %d already done, %d skipped, %d failed\n",
		s.nNew, s.nEps, s.nDone, s.nSkip, s.nErr)
	if s.nErr > 0 {
		return 1
	}
	return 0
}

func (s *Refresher) scanTV() ([]*show, error) {
	ents, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	var shows, loose []*show
	byKey := map[string]*show{}
	for _, e := range ents {
		name := e.Name()
		if skipName(name) {
			continue
		}
		path := filepath.Join(s.root, name)
		switch {
		case e.IsDir():
			sh := &show{dir: path, label: name, parsed: parseShowName(name)}
			sh.eps, sh.unknown = collectEpisodes(path, -1)
			if len(sh.eps) == 0 && len(sh.unknown) == 0 {
				continue
			}
			if len(sh.eps) > 0 {
				fp := parseShowName(filepath.Base(sh.eps[0].path))
				same := NormTitle(fp.Title) == NormTitle(sh.parsed.Title)
				if fp.Title != "" && !same {
					sh.alt = fp.Title
				}
				if same && sh.parsed.Year == 0 {
					sh.parsed.Year = fp.Year
				}
			}
			shows = append(shows, sh)

		case e.Type().IsRegular() && isVideo(path):
			season, eps, ok := parseEpisode(name, -1)
			p := parseShowName(name)
			if !ok || p.Title == "" {
				s.printf("? %s: can't tell which show or episode this is, left alone\n", name)
				continue
			}
			key := fmt.Sprintf("%s|%d|%s", NormTitle(p.Title), p.Year, p.Country)
			sh := byKey[key]
			if sh == nil {
				sh = &show{label: p.Title, parsed: p}
				byKey[key] = sh
				loose = append(loose, sh)
			}
			sh.eps = append(sh.eps, &epFile{path, season, eps})
		}
	}

	// New episodes dropped loose in the root join their show's folder when
	// exactly one folder fits; otherwise they get matched on their own and
	// still land in the right folder.
	for _, l := range loose {
		var home []*show
		for _, sh := range shows {
			if sh.dir != "" && NormTitle(sh.parsed.Title) == NormTitle(l.parsed.Title) &&
				(l.parsed.Year == 0 || sh.parsed.Year == 0 || l.parsed.Year == sh.parsed.Year) {
				home = append(home, sh)
			}
		}
		if len(home) == 1 {
			home[0].eps = append(home[0].eps, l.eps...)
		} else {
			shows = append(shows, l)
		}
	}
	return shows, nil
}

// collectEpisodes walks a show folder, numbering every video it can.
// season is the one implied by the folders above, or -1. Videos it can't
// number come back as unknown.
func collectEpisodes(dir string, season int) (eps []*epFile, unknown []string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	for _, e := range ents {
		name := e.Name()
		if skipName(name) {
			continue
		}
		p := filepath.Join(dir, name)
		if e.IsDir() {
			if isExtrasDir(name) {
				continue
			}
			fs := folderSeason(name)
			if fs < 0 {
				fs = season
			}
			e, u := collectEpisodes(p, fs)
			eps, unknown = append(eps, e...), append(unknown, u...)
			continue
		}
		if !e.Type().IsRegular() || !isVideo(p) {
			continue
		}
		if strings.Contains(strings.ToLower(name), "sample") {
			if info, err := e.Info(); err == nil && info.Size() < 200<<20 {
				continue
			}
		}
		se, nums, ok := parseEpisode(name, season)
		if !ok {
			unknown = append(unknown, p)
			continue
		}
		eps = append(eps, &epFile{p, se, nums})
	}
	return eps, unknown
}

var (
	showTMDBRe = regexp.MustCompile(`(?i)<uniqueid[^>]*type="tmdb"[^>]*>\s*(\d+)\s*<|<tmdbid>\s*(\d+)\s*<|themoviedb\.org/tv/(\d+)`)
	showTVDBRe = regexp.MustCompile(`(?i)<uniqueid[^>]*type="tvdb"[^>]*>\s*(\d+)\s*<|<tvdbid>\s*(\d+)\s*<|<id>\s*(\d+)\s*</id>|thetvdb\.com/\S*?(?:series/|id=)(\d+)`)
)

// existingShowID reads the TMDB id from a tvshow.nfo, from mvd or anything
// else; a TVDB or IMDb id (Sonarr, Kodi) is looked up instead.
func (s *filer) existingShowID(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "tvshow.nfo"))
	if err != nil {
		return 0
	}
	if id := firstGroup(showTMDBRe.FindSubmatch(b)); id != "" {
		return Atoi(id)
	}
	if id := firstGroup(showTVDBRe.FindSubmatch(b)); id != "" {
		if n, err := s.tmdb.Find(id, "tvdb_id", true); err == nil {
			return n
		}
	}
	if m := imdbIDRe.FindSubmatch(b); m != nil {
		if n, err := s.tmdb.Find(string(m[1]), "imdb_id", true); err == nil {
			return n
		}
	}
	return 0
}

func firstGroup(m [][]byte) string {
	if m == nil {
		return ""
	}
	for _, g := range m[1:] {
		if len(g) > 0 {
			return string(g)
		}
	}
	return ""
}

// processShow matches a show (unless its tvshow.nfo already says what it
// is) and brings its episodes into line. Returns true if the user quit.
func (s *Refresher) processShow(sh *show) bool {
	id := 0
	if sh.dir != "" {
		id = s.existingShowID(sh.dir)
	}
	known := id != 0 && !s.opt.Force
	if !known {
		var err error
		if id, err = s.choose(sh.label, sh.parsed, sh.alt, ""); err != nil {
			s.failf(sh.label, err)
			return false
		}
		if id <= 0 {
			return id < 0
		}
	}
	tv, err := s.tmdb.TV(id)
	if err != nil {
		s.failf(sh.label, err)
		return false
	}
	ord, err := s.resolveOrder(sh, tv, s.opt.Order)
	if err != nil {
		s.failf(sh.label, err)
		return false
	}
	var n epCount
	changed, err := s.applyShow(s.root, sh, tv, known, ord, &n)
	s.nEps += n.filed
	s.nDup += n.dups
	switch {
	case err != nil:
		s.failf(sh.label, err)
	case changed:
		s.nNew++
	default:
		s.nDone++
	}
	return false
}

type epJob struct {
	file   *epFile
	season int
	eps    []*tmdb.Episode
}

func showFolder(tv *tmdb.TVShow) string {
	if f := SafeName(TitleYear(tv.Name, tv.Year())); f != "" {
		return f
	}
	return fmt.Sprintf("tmdb-%d", tv.ID)
}

// epCount is what applyShow did with a show's episodes.
type epCount struct {
	filed, dups int
	left        map[string]string // why a file wasn't filed, by its path
}

func (n *epCount) leave(path, why string) {
	if n.left == nil {
		n.left = map[string]string{}
	}
	n.left[path] = why
}

// season is a show's season from TMDB, or nil if it has none by that
// number: from the order the show's filed in, if not the aired one.
func (s *filer) season(tv *tmdb.TVShow, ord *showOrder, seasons map[int]*tmdb.Season, n int) (*tmdb.Season, error) {
	se, seen := seasons[n]
	if seen || ord != nil {
		return se, nil
	}
	se, err := s.tmdb.Season(tv.ID, n)
	var fe tmdb.FatalError
	if errors.As(err, &fe) {
		return nil, err
	}
	seasons[n] = se // nil if TMDB has no such season
	return se, nil
}

// Finding a special by its title: how close a match must be, and how far
// ahead of the next.
const (
	specialScore  = 0.85
	specialMargin = 0.15
)

// specialByTitle finds the special a file is, by the title in its name,
// when its number isn't on TMDB: the one season 0 episode whose name
// matches it closely, with no other close behind. Only for a single
// episode outside season 0.
func (s *filer) specialByTitle(tv *tmdb.TVShow, ord *showOrder, seasons map[int]*tmdb.Season, f *epFile, season int) (*tmdb.Episode, error) {
	title := episodeTitle(filepath.Base(f.path))
	if season == 0 || len(f.eps) != 1 || title == "" {
		return nil, nil
	}
	se, err := s.season(tv, ord, seasons, 0)
	if se == nil || err != nil {
		return nil, err
	}
	return bestByTitle(se.Episodes, title), nil
}

// SuggestEpisodes is a show's episodes whose names come closest to the
// title in a video's file name, best first, at most n: for a person
// choosing which episode it is. None when the name has no title in it.
func SuggestEpisodes(seasons []*tmdb.Season, video string, n int) []tmdb.Episode {
	title := episodeTitle(filepath.Base(video))
	if title == "" {
		return nil
	}
	type scored struct {
		e   tmdb.Episode
		sim float64
	}
	var all []scored
	for _, se := range seasons {
		for _, e := range se.Episodes {
			if sim := TitleSim(title, e.Name); sim >= 0.5 {
				all = append(all, scored{e, sim})
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].sim > all[j].sim })
	var out []tmdb.Episode
	for _, x := range all[:min(len(all), n)] {
		out = append(out, x.e)
	}
	return out
}

// askEpisode asks which episode a video is, when its number won't do and
// its title didn't settle it: the episode, or nil to leave it, and whether
// to ask no more.
func (s *filer) askEpisode(tv *tmdb.TVShow, ord *showOrder, seasons map[int]*tmdb.Season, f *epFile, missing string) (*tmdb.Episode, bool, error) {
	var all []*tmdb.Season
	if se, err := s.season(tv, ord, seasons, 0); err != nil {
		return nil, false, err
	} else if se != nil {
		all = append(all, se)
	}
	if se, err := s.season(tv, ord, seasons, f.season); err == nil && se != nil && f.season != 0 {
		all = append(all, se)
	}
	name := filepath.Base(f.path)
	for {
		a, err := s.ask(Question{Label: name, Kind: "tv", Show: TitleYear(tv.Name, tv.Year()), Video: name,
			Missing: missing, Episodes: SuggestEpisodes(all, f.path, 5)})
		if err != nil || a.Quit || a.Episode == 0 {
			return nil, a.Quit, err
		}
		se, err := s.season(tv, ord, seasons, a.Season)
		if err != nil {
			return nil, false, err
		}
		if se != nil {
			if e := se.Episode(a.Episode); e != nil {
				return e, false, nil
			}
		}
		missing = fmt.Sprintf("TMDB has no S%02dE%02d either", a.Season, a.Episode)
	}
}

// bestByTitle is the episode a title names, if one stands out.
func bestByTitle(eps []tmdb.Episode, title string) *tmdb.Episode {
	var best *tmdb.Episode
	first, second := 0.0, 0.0
	for i := range eps {
		switch sim := TitleSim(title, eps[i].Name); {
		case sim > first:
			best, first, second = &eps[i], sim, first
		case sim > second:
			second = sim
		}
	}
	if first < specialScore || first-second < specialMargin {
		return nil
	}
	return best
}

// applyShow brings a show's episodes into line in lib (the TV folder),
// counting them in n. Says whether anything changed.
func (s *filer) applyShow(lib string, sh *show, tv *tmdb.TVShow, known bool, ord *showOrder, n *epCount) (bool, error) {
	folder := showFolder(tv)
	target := filepath.Join(lib, folder)

	// 1. Work out what needs doing before touching anything: episodes
	// without an NFO yet (all of them with -refresh or -force).
	var notes []string
	var jobs []epJob
	seasons := map[int]*tmdb.Season{}
	regular := tv.RegularSeasons()
	if ord != nil {
		for n, se := range ord.seasons {
			seasons[n] = se
		}
		regular = ord.regular
	}
	inSeason := 0        // files in the -season asked for
	quit := s.ask == nil // nobody to ask which episode a video is, or they've said to stop
	for _, f := range sh.eps {
		season := f.season
		if season < 0 && len(regular) == 1 {
			season = regular[0]
		}
		if s.opt.Season != nil {
			if season != *s.opt.Season {
				continue
			}
			inSeason++
		}
		if !sh.fresh && Exists(filepath.Join(filepath.Dir(f.path), videoBase(f.path)+".nfo")) && !s.opt.Refresh && !s.opt.Force {
			continue
		}
		name := s.rel(f.path)
		if season < 0 {
			notes = append(notes, name+": no season number, left alone")
			n.leave(f.path, "no season number in its name")
			continue
		}
		se, err := s.season(tv, ord, seasons, season)
		if err != nil {
			return false, err
		}
		var eps []*tmdb.Episode
		missing := ""
		if se == nil {
			missing = fmt.Sprintf("TMDB has no season %d", season)
		}
		for _, k := range f.eps {
			if se == nil {
				break
			}
			e := se.Episode(k)
			if e == nil {
				missing = fmt.Sprintf("TMDB has no S%02dE%02d", season, k)
				eps = nil
				break
			}
			eps = append(eps, e)
		}
		if missing == "" {
			jobs = append(jobs, epJob{f, season, eps})
			continue
		}
		// Not where its number says: a special numbered as an extra
		// episode of a season, perhaps, which its title can find.
		if e, err := s.specialByTitle(tv, ord, seasons, f, season); err != nil {
			return false, err
		} else if e != nil {
			notes = append(notes, fmt.Sprintf("%s: %s, but its title is S00E%02d's", name, missing, e.EpisodeNumber))
			jobs = append(jobs, epJob{f, 0, []*tmdb.Episode{e}})
			continue
		}
		if !quit {
			e, q, err := s.askEpisode(tv, ord, seasons, f, missing)
			if err != nil {
				return false, err
			}
			if e != nil {
				jobs = append(jobs, epJob{f, e.SeasonNumber, []*tmdb.Episode{e}})
				continue
			}
			quit = q
		}
		notes = append(notes, name+": "+missing+", left alone")
		n.leave(f.path, missing)
	}
	if s.opt.Season != nil {
		if inSeason == 0 {
			notes = append(notes, fmt.Sprintf("no season %d on disk", *s.opt.Season))
		}
	} else {
		for _, u := range sh.unknown {
			if !Exists(filepath.Join(filepath.Dir(u), videoBase(u)+".nfo")) {
				notes = append(notes, s.rel(u)+": not recognised as an episode, left alone")
			}
		}
	}

	showDir := sh.dir
	renameFolder := !s.opt.NoRename && sh.dir != "" && sh.dir != target
	makeFolder := !s.opt.NoRename && sh.dir == ""
	writeShowNFO := !known || s.opt.Refresh || s.opt.Force ||
		(sh.dir != "" && !Exists(filepath.Join(sh.dir, "tvshow.nfo")))
	if len(jobs) == 0 && !renameFolder && !makeFolder && !writeShowNFO {
		for _, n := range notes {
			s.printf("? %s\n", n)
		}
		return false, nil
	}

	if ord != nil {
		s.printf("✓ %s  tmdb:%d  (%s)\n", TitleYear(tv.Name, tv.Year()), tv.ID, ord.name)
	} else {
		s.printf("✓ %s  tmdb:%d\n", TitleYear(tv.Name, tv.Year()), tv.ID)
	}
	for _, n := range notes {
		s.printf("  note: %s\n", n)
	}

	// 2. The show folder.
	moved := false
	switch {
	case makeFolder:
		if !Exists(target) {
			s.say("mkdir %s", s.rel(target))
			if !s.opt.DryRun {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return true, err
				}
			}
		}
		showDir = target
	case renameFolder:
		if err := s.rename(sh.dir, target); err != nil {
			s.printf("  ! folder left as is: %v\n", err)
		} else {
			moved, showDir = true, target
		}
	}
	// Where a file is now, after the folder rename (which a dry run skips).
	where := func(p string) string {
		if moved && !s.opt.DryRun && strings.HasPrefix(p, sh.dir+string(filepath.Separator)) {
			return target + p[len(sh.dir):]
		}
		return p
	}

	// 3. Episodes: rename into Season NN/, then NFO and thumbnail.
	made := map[string]bool{}
	emptied := map[string]bool{}
	touched := map[int]bool{}
	for _, j := range jobs {
		src := where(j.file.path)
		dir, base := filepath.Dir(src), videoBase(src)

		if !s.opt.NoRename && showDir != "" {
			dir = filepath.Join(showDir, fmt.Sprintf("Season %02d", j.season))
			if !made[dir] && !Exists(dir) {
				s.say("mkdir %s", s.rel(dir))
				if !s.opt.DryRun {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						return true, err
					}
				}
			}
			made[dir] = true

			base = episodeName(folder, j.season, j.eps)
			ext := videoExt(src)
			if p := filepath.Join(dir, base+ext); p != src && existsOther(p, src) {
				s.printf("  = %s is already in the library\n", s.rel(p))
				n.dups++
				n.leave(j.file.path, fmt.Sprintf("S%02dE%02d is already in the library", j.season, j.eps[0].EpisodeNumber))
				continue
			}
			srcDir, srcBase := filepath.Dir(src), videoBase(src)
			for _, sc := range sidecars(srcDir, srcBase, src) {
				if (s.imp != nil) && !isSubtitle(sc) {
					continue
				}
				suffix := strings.TrimPrefix(filepath.Base(sc), srcBase)
				if err := s.sidecarRename(sc, filepath.Join(dir, base+suffix)); err != nil {
					s.printf("  ! %v\n", err)
				}
			}
			if err := s.rename(src, filepath.Join(dir, base+ext)); err != nil {
				s.printf("  ! %v\n", err)
				continue
			}
			s.moveExtraSubs(j.file.path, dir, base)
			emptied[srcDir] = true
		}

		n.filed += len(j.eps)
		touched[j.season] = true
		nfo := filepath.Join(dir, base+".nfo")
		if s.opt.DryRun {
			s.say("write %s", s.rel(nfo))
			continue
		}
		if err := os.WriteFile(nfo, buildEpisodeNFO(tv, j.eps, s.tmdb), 0o644); err != nil {
			return true, err
		}
		thumb := filepath.Join(dir, base+"-thumb.jpg")
		if s.opt.Refresh || s.opt.Force {
			os.Remove(thumb) // it may have travelled from a different episode
		}
		s.art(j.eps[0].StillPath, "w780", thumb)
	}

	// 4. Show NFO and artwork.
	if showDir == "" {
		return true, nil // loose files with -no-rename: no folder to put them in
	}
	if writeShowNFO {
		p := filepath.Join(showDir, "tvshow.nfo")
		if s.opt.DryRun {
			s.say("write %s", s.rel(p))
		} else if err := os.WriteFile(p, buildShowNFO(tv, s.cfg.Region, s.tmdb), 0o644); err != nil {
			return true, err
		}
	}
	if s.opt.DryRun {
		return true, nil
	}
	s.art(tv.PosterPath, "w780", filepath.Join(showDir, "poster.jpg"))
	s.art(tv.BackdropPath, "w1280", filepath.Join(showDir, "fanart.jpg"))
	if ord == nil { // posters are for aired seasons; they'd mislead in another order
		for n := range touched {
			s.art(tv.SeasonPoster(n), "w780", filepath.Join(showDir, seasonPosterName(n)))
		}
	}

	// 5. Tidy away folders the episodes came from, if now empty. os.Remove
	// refuses a non-empty folder, so nothing else can go.
	dirs := make([]string, 0, len(emptied))
	for d := range emptied {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		for d != showDir && d != lib && strings.HasPrefix(d, showDir+string(filepath.Separator)) {
			if os.Remove(d) != nil {
				break
			}
			d = filepath.Dir(d)
		}
	}
	return true, nil
}

// episodeName: "Show (Year) - S01E02 - Title", or "S01E02-E03 - A & B".
func episodeName(show string, season int, eps []*tmdb.Episode) string {
	code := fmt.Sprintf("S%02dE%02d", season, eps[0].EpisodeNumber)
	if len(eps) > 1 {
		code += fmt.Sprintf("-E%02d", eps[len(eps)-1].EpisodeNumber)
	}
	var titles []string
	seen := map[string]bool{}
	for _, e := range eps {
		if t := strings.TrimSpace(e.Name); t != "" && !seen[t] {
			seen[t] = true
			titles = append(titles, t)
		}
	}
	name := show + " - " + code
	if title := SafeName(strings.Join(titles, " & ")); title != "" {
		if r := []rune(title); len(r) > 80 {
			title = strings.TrimRight(string(r[:80]), " .-,&")
		}
		name += " - " + title
	}
	return name
}

func seasonPosterName(n int) string {
	if n == 0 {
		return "season-specials-poster.jpg"
	}
	return fmt.Sprintf("season%02d-poster.jpg", n)
}

func Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// existsOther says whether p exists as a file other than src itself. On a
// case-insensitive filesystem (exFAT, the usual USB drive), a name differing
// only in case finds the same file, and a rename to it is a rename, not a
// collision.
func existsOther(p, src string) bool {
	pst, err := os.Stat(p)
	if err != nil {
		return false
	}
	sst, err := os.Stat(src)
	return err != nil || !os.SameFile(pst, sst)
}
