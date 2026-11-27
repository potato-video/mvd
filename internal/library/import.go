package library

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"mvd/internal/config"
	"mvd/internal/tmdb"
)

// mvd import: everything in the import folder (or another source) is
// identified as movies or episodes, matched on TMDB, and filed into the
// library; what's left of an item once all its videos are filed goes to
// the trash.

const defaultSettle = time.Minute

// Importer files what's in the import folder (or another source) into the
// library: mvd import, and the server's auto-import. Each item goes through
// Identify and File; Run does that for every item, asking in between.
type Importer struct {
	filer
	settle   time.Duration   // how long an item must sit unchanged
	complete map[string]bool // items known to be whole: no need to settle
	movieLib map[int]string  // TMDB id → movie folder already in the library
	tvLib    map[int]string  // TMDB id → show folder already in the library
	dl       *Downloads      // what Transmission says about the items
	results  []ItemResult    // what became of each item
	trashSz  int64           // the trash's size, once emptied of what's old (-1: not known)
}

func NewImporter(o Options, cfg config.Config, tm *tmdb.Client, src string) *Importer {
	return &Importer{
		filer: filer{opt: o, cfg: cfg, tmdb: tm, sets: map[int]bool{},
			imp: &importing{src: src, moved: map[string]string{}, extraSubs: map[string][]extraSub{}}},
		settle: defaultSettle, trashSz: -1,
	}
}

// SetDownloads gives the import what Transmission says about its items
// (LoadDownloads), rather than asking itself.
func (s *Importer) SetDownloads(d Downloads) { s.dl = &d }

// SetLog records every move (the import log).
func (s *Importer) SetLog(w io.Writer) { s.imp.log = w }

// SetComplete names items known to be whole (uploads the server has
// finished), which an import takes without waiting for them to settle.
func (s *Importer) SetComplete(paths []string) {
	s.complete = map[string]bool{}
	for _, p := range paths {
		s.complete[filepath.Clean(p)] = true
	}
}

// Stats counts up what the run did.
func (s *Importer) Stats() Stats { return importStats(s.results) }

// Results is what became of each item.
func (s *Importer) Results() []ItemResult { return s.results }

// TrashSize is how much the trash holds after the run (what was old
// cleared, the run's leftovers added), or -1 if the run didn't look.
func (s *Importer) TrashSize() int64 { return s.trashSz }

// Stopped is why the run stopped short (TMDB out of reach, or refusing the
// key), or nil if it didn't: then Results has only the items before it.
func (s *Importer) Stopped() error { return s.fatalErr }

// Run imports everything in the source, item by item.
func (s *Importer) Run() int {
	if s.opt.DryRun {
		s.println("dry run: nothing will be changed")
	} else {
		n, size := purgeTrashSize(s.cfg.TrashDir, s.cfg.TrashMaxAge, s.cfg.TrashMaxSize, time.Now())
		if n > 0 {
			s.printf("trash: cleared %d old leftover(s)\n", n)
		}
		s.trashSz = size
	}
	s.scanLibrary()
	s.loadDownloads()

	items, err := importItems(s.imp.src)
	if err != nil {
		s.println("mvd:", err)
		return 1
	}
	if len(items) == 0 {
		s.printf("nothing to import in %s\n", s.imp.src)
		return 0
	}
	for _, it := range items {
		pl := s.Identify(it.path, it.kind)
		if pl == nil {
			continue
		}
		if s.askAll(pl) {
			s.println("stopped: this and everything after it left as is")
			break
		}
		r := s.File(pl)
		s.printItem(r)
		s.results = append(s.results, r)
		if s.fatal {
			s.println("stopping")
			break
		}
	}

	st := s.Stats()
	s.printf("\n%d movie(s) and %d episode(s) filed", st.Movies, st.Episodes)
	trashed := 0
	for _, r := range s.results {
		if r.Trashed {
			trashed++
		}
	}
	if trashed > 0 {
		s.printf("; %d item(s) cleared away", trashed)
	}
	s.println()
	if st.Left > 0 {
		s.printf("%d item(s) left in %s for you to sort out (see above)\n", st.Left, filepath.Base(s.imp.src))
	}
	if st.Duplicates > 0 {
		s.printf("%d already in the library, left where they were\n", st.Duplicates)
	}
	if st.Waiting > 0 {
		s.printf("%d still arriving; run again shortly\n", st.Waiting)
	}
	if st.Seeding > 0 {
		s.printf("%d torrent(s) still seeding; they'll be imported once they reach their ratio\n", st.Seeding)
	}
	if s.nErr > 0 {
		return 1
	}
	return 0
}

type importEntry struct {
	path string
	kind string
}

// importItems lists the things to import. "movies" and "tv"
// folders force the type of everything in them.
func importItems(src string) ([]importEntry, error) {
	st, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []importEntry{{src, ""}}, nil
	}
	var out []importEntry
	var walk func(dir, kind string) error
	walk = func(dir, kind string) error {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range ents {
			name := e.Name()
			if skipName(name) || strings.HasSuffix(strings.ToLower(name), ".mvd") {
				continue
			}
			p := filepath.Join(dir, name)
			if e.IsDir() && kind == "" && dir == src {
				switch strings.ToLower(name) {
				case "movies":
					if err := walk(p, "movie"); err != nil {
						return err
					}
					continue
				case "tv":
					if err := walk(p, "tv"); err != nil {
						return err
					}
					continue
				}
			}
			out = append(out, importEntry{p, kind})
		}
		return nil
	}
	return out, walk(src, "")
}

// scanLibrary notes the TMDB ids already in the library, so a movie filed
// under another folder name counts as a duplicate, and new episodes join
// their show wherever it lives.
func (s *Importer) scanLibrary() {
	s.movieLib, s.tvLib = map[int]string{}, map[int]string{}
	if ents, err := os.ReadDir(s.cfg.MoviesDir); err == nil {
		for _, e := range ents {
			if !e.IsDir() || skipName(e.Name()) {
				continue
			}
			dir := filepath.Join(s.cfg.MoviesDir, e.Name())
			nfos, _ := filepath.Glob(filepath.Join(dir, "*.nfo"))
			for _, n := range nfos {
				if b, err := os.ReadFile(n); err == nil {
					if id := firstGroup(tmdbIDRe.FindSubmatch(b)); id != "" {
						s.movieLib[Atoi(id)] = dir
						break
					}
				}
			}
		}
	}
	if ents, err := os.ReadDir(s.cfg.TVDir); err == nil {
		for _, e := range ents {
			if !e.IsDir() || skipName(e.Name()) {
				continue
			}
			dir := filepath.Join(s.cfg.TVDir, e.Name())
			if id := s.existingShowID(dir); id != 0 {
				s.tvLib[id] = dir
			}
		}
	}
}

// A Plan is one import item, identified: what's in it, and what each movie
// or show in it matched. File files it.
type Plan struct {
	Result ItemResult // its State is set when it went no further than Identify

	units   []*planUnit
	kind    string
	isDir   bool
	h       hint
	videos  []string // every video in it that matters
	unknown []string
	extras  []string // a TV item's extras folders
	torrent *download
	seeding bool
	settled bool // unpacked earlier, and everything from it filed: done with
	empty   bool // a folder with nothing in it
}

// planUnit is a movie or a show in an item, and its match.
type planUnit struct {
	label  string
	kind   string
	parsed Parsed
	tmdb   int // 0: no match yet
	cands  []Candidate
	err    error // matching failed
	asked  bool  // a person was asked, and skipped it
	movie  *movieJob
	show   *showJob
}

// Identify reads an import item and matches what's in it, asking nothing:
// what it isn't sure of is left with its candidates. It waits for items
// still arriving or seeding, and unpacks archives. Returns nil for an item
// that isn't one (a later volume of an archive, which goes with its first).
func (s *Importer) Identify(path, kind string) *Plan {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	isDir := st.IsDir()
	pl := &Plan{Result: ItemResult{Path: path, Label: s.rel(path)}, kind: kind, isDir: isDir}
	r := &pl.Result
	left := func(p Problem, detail string) *Plan {
		r.State, r.Problem, r.Detail = ItemLeft, p, detail
		return pl
	}

	torrent := s.dl.item(path)
	if torrent != nil {
		// A download: its client knows when it's complete, so no need to
		// wait for it to settle; but it may still be seeding. Then only
		// what's in its archives can be taken, since unpacking leaves the
		// archives as they are.
		if !torrent.ready && torrent.complete && isDir && s.unpackWhileSeeding(path, kind) {
			pl.seeding = true
		} else if !torrent.ready {
			r.State, r.Detail = ItemSeeding, torrent.why
			if !torrent.complete {
				r.State = ItemWaiting
			}
			return pl
		}
		pl.torrent = torrent
	} else if why := s.busy(path); why != "" {
		r.State, r.Detail = ItemWaiting, why
		return pl
	}

	// A loose archive goes into a folder of its own first, its other
	// volumes and its hint with it, to be unpacked there.
	if !isDir && isArchive(path) {
		switch {
		case !archiveFirst(path):
			if first := archiveFirstVolume(path); first != path && Exists(first) {
				return nil // it goes with its first volume
			}
			return left(MissingVolume, "")
		case s.opt.Copy:
			return left(CopyArchives, "")
		}
		dir, err := s.unpackLoose(path, r.Label)
		switch {
		case err != nil:
			return left(CantUnpack, err.Error())
		case dir == "":
			r.State = ItemPending // a dry run
			return pl
		}
		if torrent != nil {
			s.dl.byItem[filepath.Clean(dir)] = torrent
		}
		return s.Identify(dir, kind)
	}

	h := readHint(path, isDir)
	if h.kind != "" {
		kind = h.kind
	}
	pl.h, pl.kind = h, kind
	p := planItem(path, isDir, kind)

	// Archives: unpacked beside themselves, then the item is read again.
	var u unpacked
	if isDir && len(p.firsts) > 0 {
		u = readUnpacked(path)
		if todo := u.pending(path, p.firsts); len(todo) > 0 {
			// One that can't be unpacked holds up the whole item: what's
			// loose beside it might be an extra taken for the movie, and the
			// archive would go to the trash once that was filed.
			switch {
			case s.opt.Copy:
				if len(p.videos) == 0 {
					return left(CopyArchives, "")
				}
				s.printf("  note: %s: its archives are left, since -copy doesn't unpack (it never writes to the source)\n", r.Label)
			default:
				if err := s.unpackItem(path, r.Label, todo, u); err != nil {
					return left(CantUnpack, err.Error())
				}
				if s.opt.DryRun {
					r.State = ItemPending // what's in them isn't there to read yet
					return pl
				}
				p = planItem(path, isDir, kind)
			}
		}
	}
	for v, subs := range p.subs {
		s.imp.extraSubs[v] = append(s.imp.extraSubs[v], subs...)
	}

	if len(p.videos) == 0 {
		switch {
		case len(p.firsts) > 0 && len(u.pending(path, p.firsts)) == 0:
			pl.settled = true
		case p.archives && !unpackBuilt:
			return left(NotUnpackable, "")
		case p.archives:
			return left(NotUnpackable, "")
		case isDir && emptyTree(path):
			pl.empty = true
		default:
			return left(NoVideos, "")
		}
		return pl
	}

	// A hint's TMDB id settles the match: for a movie item with one movie, or
	// for every episode in a TV item, which become one show.
	if h.tmdb != 0 {
		switch {
		case kind == "tv" && len(p.shows) > 0:
			all := p.shows[0]
			for _, o := range p.shows[1:] {
				all.eps = append(all.eps, o.eps...)
			}
			all.tmdb, all.order = h.tmdb, h.order
			p.shows = []*showJob{all}
		case kind != "tv" && len(p.movies) == 1:
			p.movies[0].tmdb = h.tmdb
		default:
			s.printf("  note: %s: the hint's tmdb id doesn't say which of several things it means; ignored\n", filepath.Base(h.path))
		}
	} else if h.order != "" {
		for _, sh := range p.shows {
			sh.order = h.order
		}
	}
	if len(h.episodes) > 0 && h.tmdb != 0 && kind == "tv" {
		answerEpisodes(p, h, path, isDir)
	}
	pl.videos, pl.unknown, pl.extras = p.videos, p.unknown, p.tvExtras

	for _, f := range p.movies {
		pl.units = append(pl.units, &planUnit{label: f.label, kind: "movie", parsed: f.parsed, tmdb: f.tmdb, movie: f})
	}
	for _, j := range p.shows {
		pl.units = append(pl.units, &planUnit{label: j.label, kind: "tv", parsed: j.parsed, tmdb: j.tmdb, show: j})
	}
	for _, u := range pl.units {
		if u.tmdb != 0 || s.fatal {
			continue
		}
		video := ""
		if u.movie != nil {
			video = u.movie.videos[0]
		}
		u.tmdb, u.cands, u.err = s.match(u.kind == "tv", u.parsed, altOf(u), video)
		var fe tmdb.FatalError
		if errors.As(u.err, &fe) {
			s.fatal, s.fatalErr = true, u.err
		}
	}
	return pl
}

func altOf(u *planUnit) string {
	if u.movie != nil {
		return u.movie.alt
	}
	return u.show.alt
}

// askAll asks about each unit Identify wasn't sure of, if there's anyone to
// ask (and says so if there isn't). Returns true if the user quit.
func (s *Importer) askAll(pl *Plan) bool {
	for _, u := range pl.units {
		if u.tmdb != 0 || u.err != nil || s.fatal {
			continue
		}
		id, err := s.askMatch(u.kind == "tv", u.label, u.parsed, u.cands)
		switch {
		case err != nil:
			u.err = err
		case id < 0:
			return true
		case id == 0:
			u.asked = s.ask != nil
		default:
			u.tmdb = id
		}
	}
	return false
}

// File files what Identify matched (or what was chosen since), sees to
// what's left of the item, and says what became of it.
func (s *Importer) File(pl *Plan) ItemResult {
	r := &pl.Result
	switch {
	case r.State != "":
		// it went no further than Identify
	case pl.empty:
		r.State = ItemFiled // nothing in it to file
		if !s.opt.DryRun && !s.opt.Copy {
			os.RemoveAll(r.Path)
			r.State = ItemRemoved
		}
	case pl.settled:
		// Unpacked earlier, and everything from it filed: done with.
		r.State = ItemFiled
		s.finishItem(pl, true, true)
	default:
		s.fileUnits(pl)
	}
	return *r
}

func (s *Importer) fileUnits(pl *Plan) {
	r := &pl.Result
	r.State = ItemLeft // until it's all filed
	var shows int
	var showDirs []string
	for _, u := range pl.units {
		if u.movie != nil {
			s.importMovie(r, u)
		} else {
			shows++
			if dir := s.importShow(r, u); dir != "" {
				showDirs = append(showDirs, dir)
			}
		}
		if s.fatal {
			return
		}
	}
	if len(pl.extras) > 0 && shows == 1 && len(showDirs) == 1 {
		for _, x := range pl.extras {
			s.moveExtra(x, showDirs[0])
		}
	}
	r.Unknown = pl.unknown

	// Leftovers: only once every video in the item has gone somewhere.
	var unfiled []string
	for _, v := range pl.videos {
		if !s.handled(v) {
			unfiled = append(unfiled, v)
		}
	}

	if len(unfiled) == 0 {
		r.State = ItemFiled
	} else if len(unfiled) < len(pl.videos) {
		r.Unfiled = unfiled
	}
	s.finishItem(pl, len(unfiled) < len(pl.videos), len(unfiled) == 0)
}

// finishItem is the end of an item: a torrent goes from Transmission once
// any of it has been filed (it can't seed what's moved), and not before, so
// one that couldn't be matched carries on seeding while it waits for you.
// Once everything is filed, the leftovers go to the trash. A torrent still
// seeding (whose archives were unpacked) keeps its files and stays.
func (s *Importer) finishItem(pl *Plan, filedAny, allDone bool) {
	r, torrent := &pl.Result, pl.torrent
	if pl.seeding {
		r.State, r.Detail = ItemSeeding, torrent.why+", so its archives stay until it's done"
		return
	}
	if torrent != nil && filedAny {
		s.say("remove %s from Transmission (keeping its files)", torrent.name)
		if !s.opt.DryRun {
			if err := s.dl.client.forget(torrent.id); err != nil {
				s.printf("  ! couldn't remove it from Transmission: %v\n", err)
			}
		}
	}
	switch {
	case !allDone:
	case s.opt.Copy:
		// the source is someone else's: leave it be
	case pl.isDir:
		r.Trashed = s.trash(r.Path)
	case pl.h.path != "":
		r.Trashed = s.trash(pl.h.path)
	}
}

// unpackWhileSeeding says whether a download that's still seeding can be
// imported now: when everything it has to give is in archives, which stay.
func (s *Importer) unpackWhileSeeding(path, kind string) bool {
	p := planItem(path, true, kind)
	if len(p.firsts) == 0 || s.opt.Copy {
		return false
	}
	u := readUnpacked(path)
	if !u.fromArchives(path, p.videos) {
		return false // loose videos: moving them would stop the seeding
	}
	return len(u.pending(path, p.firsts)) > 0 || len(p.videos) > 0
}

func (s *Importer) handled(p string) bool {
	for {
		if _, ok := s.imp.moved[p]; ok {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p || parent == s.imp.src {
			return false
		}
		p = parent
	}
}

// unmatched records a unit left without a match: skipped when asked, or
// waiting for a choice from its candidates (a few: what a person needs to
// pick from).
func (s *Importer) unmatched(r *ItemResult, u UnitResult, asked bool, cs []Candidate) {
	if asked {
		u.State = UnitSkipped
	} else {
		u.State = UnitChoose
	}
	u.Candidates = cs[:min(len(cs), 5)]
	r.Units = append(r.Units, u)
}

func (s *Importer) unitFailed(r *ItemResult, u UnitResult, err error) {
	s.failed(err)
	u.State, u.Err = UnitFailed, err.Error()
	r.Units = append(r.Units, u)
}

func (s *Importer) importMovie(r *ItemResult, pu *planUnit) {
	f := pu.movie
	u := UnitResult{Label: pu.label, Kind: "movie", TMDB: pu.tmdb}
	switch {
	case pu.err != nil:
		s.unitFailed(r, u, pu.err)
		return
	case pu.tmdb == 0:
		s.unmatched(r, u, pu.asked, pu.cands)
		return
	}
	m, err := s.tmdb.Movie(u.TMDB)
	if err != nil {
		s.unitFailed(r, u, err)
		return
	}
	u.Title = TitleYear(m.Title, m.Year())
	folder := SafeName(u.Title)
	if folder == "" {
		folder = fmt.Sprintf("tmdb-%d", m.ID)
	}
	target := filepath.Join(s.cfg.MoviesDir, folder)
	if dir, ok := s.movieLib[m.ID]; ok {
		u.State, u.Dest = UnitDuplicate, dir
		r.Units = append(r.Units, u)
		return
	}

	s.printf("✓ %s  tmdb:%d\n", u.Title, m.ID)
	if !s.opt.DryRun {
		os.MkdirAll(s.cfg.MoviesDir, 0o755)
	}
	it := &item{videos: f.videos, label: f.label, parsed: f.parsed}
	if err := s.apply(s.cfg.MoviesDir, it, m, ""); err != nil {
		var dup errDuplicate
		if !errors.As(err, &dup) {
			s.unitFailed(r, u, err)
			return
		}
		u.State, u.Dest = UnitDuplicate, target
		r.Units = append(r.Units, u)
		return
	}
	u.State, u.Dest, u.changed = UnitFiled, target, true
	r.Units = append(r.Units, u)
	s.movieLib[m.ID] = target
	for _, x := range f.extras {
		s.moveExtra(x, target)
	}
}

// importShow files one show's episodes, into its existing folder if the
// library has one. Returns the show's folder.
func (s *Importer) importShow(r *ItemResult, pu *planUnit) string {
	j := pu.show
	u := UnitResult{Label: pu.label, Kind: "tv", TMDB: pu.tmdb}
	switch {
	case pu.err != nil:
		s.unitFailed(r, u, pu.err)
		return ""
	case pu.tmdb == 0:
		s.unmatched(r, u, pu.asked, pu.cands)
		return ""
	}
	tv, err := s.tmdb.TV(u.TMDB)
	if err != nil {
		s.unitFailed(r, u, err)
		return ""
	}
	u.Title = TitleYear(tv.Name, tv.Year())
	libDir := s.tvLib[u.TMDB]
	sh := &show{dir: libDir, label: j.label, parsed: j.parsed, eps: j.eps, fresh: true}

	ord, err := s.resolveOrder(sh, tv, j.order)
	if err != nil {
		s.unitFailed(r, u, err)
		return ""
	}

	if !s.opt.DryRun {
		os.MkdirAll(s.cfg.TVDir, 0o755)
	}
	var n epCount
	changed, err := s.applyShow(s.cfg.TVDir, sh, tv, libDir != "", ord, &n)
	u.Episodes, u.Duplicates, u.changed = n.filed, n.dups, changed
	for p, why := range n.left {
		if r.Why == nil {
			r.Why = map[string]string{}
		}
		r.Why[p] = why
	}
	if err != nil {
		s.unitFailed(r, u, err)
		return ""
	}
	showDir := filepath.Join(s.cfg.TVDir, showFolder(tv))
	if !Exists(showDir) && libDir != "" {
		showDir = libDir // its rename failed: it's still where it was
	}
	u.Dest = showDir
	switch {
	case u.Episodes > 0:
		u.State = UnitFiled
	case u.Duplicates > 0:
		u.State = UnitDuplicate
	default:
		u.State = UnitNotFiled
	}
	r.Units = append(r.Units, u)
	if u.Episodes > 0 {
		s.tvLib[u.TMDB] = showDir
		if libDir == "" && ord != nil && !s.opt.DryRun {
			s.writeShowSettings(showDir, map[string]string{"order": j.order, "group": ord.group})
		}
	}
	return showDir
}

var extrasDest = map[string]string{
	"featurettes": "featurettes", "behind the scenes": "behind the scenes",
	"deleted scenes": "deleted scenes", "interviews": "interviews", "scenes": "scenes",
	"shorts": "shorts", "trailers": "trailers", "clips": "clips", "other": "other",
	"extras": "extras", "extra": "extras",
}

// moveExtra puts an extras folder (or an extras file) beside the movie or
// show, under the folder name Jellyfin and Kodi look for.
func (s *Importer) moveExtra(x, dest string) {
	name := filepath.Base(x)
	var to string
	if st, err := os.Stat(x); err == nil && st.IsDir() {
		to = filepath.Join(dest, extrasDest[strings.ToLower(cleanRunes(name))])
	} else {
		to = filepath.Join(dest, "extras", name)
		if !s.opt.DryRun {
			os.MkdirAll(filepath.Dir(to), 0o755)
		}
	}
	if err := s.rename(x, to); err != nil {
		s.printf("  ! extras: %v\n", err)
	}
}

// --- still arriving? ---

var partialRe = regexp.MustCompile(`(?i)\.(part|partial|crdownload|download|tmp|aria2|opdownload|!qb|!ut|!bt)$`)

// busy says why an item isn't ready, or "" if it is: a half-downloaded file,
// or anything changed too recently (a copy over SMB writes to the final name,
// so a file still arriving looks complete).
func (s *Importer) busy(path string) string {
	if s.complete[filepath.Clean(path)] {
		return ""
	}
	why := ""
	limit := time.Now().Add(-s.settle)
	files := false
	var made time.Time // the item's own folder: when it changed
	filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if p == path && info.IsDir() {
			made = info.ModTime()
		}
		if partialRe.MatchString(info.Name()) {
			why = "still downloading (" + info.Name() + ")"
			return filepath.SkipAll
		}
		if !info.IsDir() && !macJunk(info.Name()) {
			files = true
		}
		if s.settle > 0 && !info.IsDir() && changedAt(info).After(limit) {
			why = "changed in the last minute, so probably still arriving"
			return filepath.SkipAll
		}
		return nil
	})
	// A copy (over SMB, say) makes the folder before its first file: a new
	// folder with nothing in it yet (or only a Mac's .DS_Store) is arriving,
	// not empty. (Its mtime, not
	// ctime: a folder's ctime changes with what's put in or taken out.)
	if why == "" && s.settle > 0 && !files && !made.IsZero() && made.After(limit) {
		why = "an empty folder made in the last minute, so probably still arriving"
	}
	return why
}

// --- the trash ---

const trashStamp = "2006-01-02 150405"

// trash moves an item's leftovers aside, or deletes them when the trash is
// switched off (either limit 0). It says whether they've gone.
func (s *Importer) trash(path string) bool {
	label := s.rel(path)
	if s.cfg.TrashDir == "" || s.cfg.TrashMaxAge == 0 || s.cfg.TrashMaxSize == 0 {
		s.say("delete leftovers in %s", label)
		if !s.opt.DryRun {
			os.RemoveAll(path)
		}
		return true
	}
	dest := filepath.Join(s.cfg.TrashDir, time.Now().Format(trashStamp)+" "+filepath.Base(path))
	s.say("leftovers %s → %s", label, s.rel(dest))
	if s.opt.DryRun {
		return true
	}
	size, _, err := moveToTrash(s.cfg.TrashDir, path)
	if err != nil {
		// Permissions, a full drive, a name too long: leave the leftovers
		// where they are rather than delete them.
		s.printf("  ! couldn't move the leftovers to the trash, so they're left in place: %v\n", err)
		return false
	}
	if s.trashSz >= 0 {
		s.trashSz += size
	}
	return true
}

// moveToTrash moves path into the trash folder, named so the purge knows
// it, and says how big it was. A trash on another drive isn't worth a
// copy: then it's deleted, and deleted says so.
func moveToTrash(trashDir, path string) (size int64, deleted bool, err error) {
	size = treeSize(path)
	base := filepath.Join(trashDir, time.Now().Format(trashStamp)+" "+filepath.Base(path))
	os.MkdirAll(trashDir, 0o755)
	// A free name: a rename onto a file replaces it, and two of the same
	// name (two x.mvd hints) can be trashed in the same second.
	dest := base
	for n := 2; Exists(dest); n++ {
		dest = fmt.Sprintf("%s %d", base, n)
	}
	err = os.Rename(path, dest)
	if errors.Is(err, syscall.EXDEV) {
		return 0, true, os.RemoveAll(path)
	}
	if err != nil {
		return 0, false, err
	}
	return size, false, nil
}

// TrashItem moves an item in the import folder to the trash (deleting it
// when the trash is off, as an import does with leftovers), with its
// file's .mvd hint, if it has one. It says how much the trash grew by, and
// whether the item was deleted instead (the trash off, or on another
// drive).
func TrashItem(cfg config.Config, path string) (grew int64, deleted bool, err error) {
	if err := CheckTrashDir(cfg, cfg.ImportDir); err != nil {
		return 0, false, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, false, err
	}
	paths := []string{path}
	if h := readHint(path, st.IsDir()); h.path != "" && !st.IsDir() {
		paths = append(paths, h.path)
	}
	for i, p := range paths {
		if cfg.TrashDir == "" || cfg.TrashMaxAge == 0 || cfg.TrashMaxSize == 0 {
			if err := os.RemoveAll(p); err != nil {
				return grew, deleted, err
			}
			deleted = deleted || i == 0
			continue
		}
		n, gone, err := moveToTrash(cfg.TrashDir, p)
		if err != nil {
			return grew, deleted, err
		}
		grew += n
		deleted = deleted || (i == 0 && gone)
	}
	return grew, deleted, nil
}

// EmptyTrash deletes everything mvd put in the trash, now rather than when
// it's old enough (the website's "in trash"). Only what mvd named, as the
// purge. It says how many entries went.
func EmptyTrash(cfg config.Config) int {
	if cfg.TrashDir == "" {
		return 0
	}
	n, _ := purgeTrashSize(cfg.TrashDir, 0, 0, time.Now())
	return n
}

// CheckImport says whether an import from src can go ahead: there's a
// library to file into, a TMDB key to match with, src and the library don't
// overlap, and the trash is somewhere safe.
func CheckImport(cfg config.Config, src string) error {
	switch {
	case cfg.MoviesDir == "" || cfg.TVDir == "":
		return errors.New("set video_dir (or movies_dir and tv_dir) under [paths] in config.toml, so mvd knows where to file things")
	case cfg.APIKey == "":
		return errors.New("no TMDB API key: set TMDB_API_KEY, or api_key under [tmdb] in " + config.Path())
	}
	src, _ = filepath.Abs(src)
	for _, lib := range []string{cfg.MoviesDir, cfg.TVDir} {
		lib, _ = filepath.Abs(lib)
		if Within(lib, src) || Within(src, lib) {
			return fmt.Errorf("%s overlaps the library (%s); import from somewhere else", src, lib)
		}
	}
	return CheckTrashDir(cfg, src)
}

// trashName matches what trash() names things: purgeTrash only ever
// deletes these, so a trash_dir pointed somewhere wrong can't take
// anything else with it.
var trashName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{6} .`)

// CheckTrashDir refuses a trash_dir that overlaps the library or the import
// folder: the trash deletes what's in it.
func CheckTrashDir(cfg config.Config, src string) error {
	if cfg.TrashDir == "" {
		return nil
	}
	t, _ := filepath.Abs(cfg.TrashDir)
	video := ""
	if cfg.VideoDir != "" {
		video, _ = filepath.Abs(cfg.VideoDir)
	}
	for _, d := range []string{cfg.MoviesDir, cfg.TVDir, src, cfg.VideoDir} {
		if d == "" {
			continue
		}
		d, _ = filepath.Abs(d)
		// The trash may live inside video_dir (that's the default), but not
		// contain any of these, nor live inside the library or the inbox.
		if Within(t, d) || (Within(d, t) && d != video) {
			return fmt.Errorf("trash_dir (%s) overlaps %s; the trash deletes what's in it, so it needs a folder of its own", t, d)
		}
	}
	return nil
}

// purgeTrash enforces the limits: entries older than maxAge go, then the
// oldest until the rest fit in maxSize. Either limit 0 empties it. Only
// entries named the way trash() names them are considered.
func purgeTrash(dir string, maxAge time.Duration, maxSize int64, now time.Time) int {
	n, _ := purgeTrashSize(dir, maxAge, maxSize, now)
	return n
}

// purgeTrashSize is purgeTrash, also saying how much the trash still holds
// (what mvd put there).
func purgeTrashSize(dir string, maxAge time.Duration, maxSize int64, now time.Time) (int, int64) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	type entry struct {
		path string
		when time.Time
		size int64
	}
	var all []entry
	for _, e := range ents {
		if !trashName.MatchString(e.Name()) {
			continue // not something mvd put here: never touch it
		}
		when, err := time.ParseInLocation(trashStamp, e.Name()[:len(trashStamp)], time.Local)
		if err != nil {
			continue
		}
		p := filepath.Join(dir, e.Name())
		all = append(all, entry{p, when, treeSize(p)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].when.Before(all[j].when) })
	var total int64
	for _, e := range all {
		total += e.size
	}
	removed := 0
	for _, e := range all {
		if maxAge > 0 && maxSize > 0 && now.Sub(e.when) <= maxAge && total <= maxSize {
			break
		}
		if os.RemoveAll(e.path) == nil {
			removed++
			total -= e.size
		}
	}
	return removed, total
}

func treeSize(p string) int64 {
	var n int64
	filepath.Walk(p, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

// answerEpisodes puts the hint's episode answers on the videos they name,
// numbered or not: they're all the hint's show by now.
func answerEpisodes(p *itemPlan, h hint, item string, isDir bool) {
	answer := func(v string) ([2]int, bool) {
		rel, ok := hintRel(item, isDir, v)
		e, found := h.episodes[rel]
		return e, ok && found
	}
	if len(p.shows) == 0 {
		p.shows = []*showJob{{label: filepath.Base(item), tmdb: h.tmdb, order: h.order}}
	}
	sh := p.shows[0]
	for _, f := range sh.eps {
		if e, ok := answer(f.path); ok {
			f.season, f.eps = e[0], []int{e[1]}
		}
	}
	var unknown []string
	for _, v := range p.unknown {
		if e, ok := answer(v); ok {
			sh.eps = append(sh.eps, &epFile{v, e[0], []int{e[1]}})
		} else {
			unknown = append(unknown, v)
		}
	}
	p.unknown = unknown
}
