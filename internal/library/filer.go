// Package library looks after the files: identifying movies and episodes,
// matching them on TMDB, renaming and moving them into place with NFOs and
// artwork (import and refresh), and indexing what's there for the server.
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

type Options struct {
	DryRun   bool
	Refresh  bool
	Force    bool
	NoRename bool
	Name     string // only the movie or show whose name contains this
	Season   *int   // TV, with Name: only this season, as numbered on disk
	Order    string // TV: episode order to use and remember
	Copy     bool   // import: copy from the source rather than move
}

// filer is what refresh and import share: renaming and moving files into
// place, NFOs and artwork, matching on TMDB, and episode orders.
type filer struct {
	opt  Options
	cfg  config.Config
	tmdb *tmdb.Client
	ask  AskFunc   // nil: ask nothing, and leave what isn't sure
	out  io.Writer // where the running commentary goes (stdout)
	root string    // refresh: the library folder it's working on, which paths are shown from
	imp  *importing

	sets     map[int]bool // collections handled this run
	nErr     int
	fatal    bool  // a failure that stops the run (TMDB refusing the key)
	fatalErr error // what it was
}

// importing is what filing from outside the library adds: set only by
// the importer.
type importing struct {
	src       string                // where files are coming from
	moved     map[string]string     // what went where this run
	extraSubs map[string][]extraSub // subtitles from Subs/ folders, by video
	log       io.Writer             // the import log
}

func skipName(name string) bool {
	return name == "" || name[0] == '.' || name[0] == '_' || name[0] == '@' ||
		strings.HasSuffix(name, ".mvdtmp")
}

var sidecarExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true, ".vtt": true,
	".sup": true, ".nfo": true, ".jpg": true, ".jpeg": true, ".png": true, ".txt": true,
	".json": true, ".xml": true,
}

// isVideo goes by extension, and sniffs anything with no recognisable one.
func isVideo(path string) bool {
	if isVideoExt(path) {
		return true
	}
	if sidecarExts[strings.ToLower(filepath.Ext(path))] {
		return false
	}
	return sniffVideo(path) != ""
}

// mainVideos picks the movie out of a folder: the largest non-sample video,
// or all the parts of a cd1/cd2-style stack.
func mainVideos(dir string) (vids []string, others int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	type vf struct {
		path string
		size int64
		part int
	}
	var all []vf
	for _, e := range ents {
		if !e.Type().IsRegular() || skipName(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if !isVideo(p) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(e.Name()), "sample") && info.Size() < 400<<20 {
			continue
		}
		all = append(all, vf{p, info.Size(), parseMovieName(e.Name()).Part})
	}
	if len(all) == 0 {
		return nil, 0
	}

	var parts []vf
	for _, v := range all {
		if v.part > 0 {
			parts = append(parts, v)
		}
	}
	if len(parts) >= 2 {
		sort.Slice(parts, func(i, j int) bool { return parts[i].part < parts[j].part })
		for _, v := range parts {
			vids = append(vids, v.path)
		}
		return vids, len(all) - len(parts)
	}

	sort.Slice(all, func(i, j int) bool { return all[i].size > all[j].size })
	return []string{all[0].path}, len(all) - 1
}

var (
	tmdbIDRe = regexp.MustCompile(`(?i)<uniqueid[^>]*type="tmdb"[^>]*>\s*(\d+)\s*<|<tmdbid>\s*(\d+)\s*<|themoviedb\.org/movie/(\d+)`)
	imdbIDRe = regexp.MustCompile(`\b(tt\d{7,9})\b`)
)

func (s *filer) failf(label string, err error) {
	s.printf("! %s: %v\n", label, err)
	s.failed(err)
}

// failed counts a failure, and notes one that stops the run.
func (s *filer) failed(err error) {
	s.nErr++
	var fe tmdb.FatalError
	if errors.As(err, &fe) {
		s.fatal, s.fatalErr = true, err
	}
}

// apply renames into lib (the movies folder), then writes the NFO and
// artwork.
func (s *filer) apply(lib string, it *item, m *tmdb.Movie, nfoName string) error {
	folder := SafeName(TitleYear(m.Title, m.Year()))
	if folder == "" {
		folder = fmt.Sprintf("tmdb-%d", m.ID)
	}
	base := folder
	if it.parsed.Edition != "" {
		base += " - " + it.parsed.Edition
	}

	dir := it.dir
	videos := append([]string(nil), it.videos...)

	if !s.opt.NoRename {
		target := filepath.Join(lib, folder)

		// 1. The folder: rename it, or make one for a loose file.
		switch {
		case dir == "":
			// Nothing moves if any file would land on one already there.
			for i, v := range videos {
				if p := filepath.Join(target, partName(base, i, len(videos))+videoExt(v)); Exists(p) {
					return errDuplicate{s.rel(p)}
				}
			}
			if _, err := os.Stat(target); err != nil {
				s.say("mkdir %s", s.rel(target))
				if !s.opt.DryRun {
					if err := os.Mkdir(target, 0o755); err != nil {
						return err
					}
				}
			}
			dir = target
		case dir != target:
			if err := s.rename(dir, target); err != nil {
				s.printf("  ! folder left as is: %v\n", err)
			} else {
				if !s.opt.DryRun { // in a dry run the files are still where they were
					for i, v := range videos {
						videos[i] = filepath.Join(target, filepath.Base(v))
					}
				}
				dir = target
			}
		}

		// 2. The video(s), with their subtitles and other sidecars.
		for i, v := range videos {
			newBase := partName(base, i, len(videos))
			oldDir, oldBase := filepath.Dir(v), videoBase(v)
			newVideo := filepath.Join(dir, newBase+videoExt(v))

			for _, sc := range sidecars(oldDir, oldBase, v) {
				if (s.imp != nil) && !isSubtitle(sc) {
					continue // a release's own .nfo and .jpg stay behind as leftovers
				}
				suffix := strings.TrimPrefix(filepath.Base(sc), oldBase)
				if err := s.sidecarRename(sc, filepath.Join(dir, newBase+suffix)); err != nil {
					s.printf("  ! %v\n", err)
				}
			}
			if err := s.rename(v, newVideo); err != nil {
				return err
			}
			s.moveExtraSubs(v, dir, newBase)
			videos[i] = newVideo
		}
		if nfoName != "" && nfoName != "movie.nfo" {
			nfoName = "" // it travelled with the video
		}
	}
	if it.others > 0 {
		s.printf("  note: %d other video(s) in the folder left alone\n", it.others)
	}

	// 3. NFO and artwork.
	if dir == "" {
		dir = filepath.Dir(videos[0])
	}
	nfo := nfoName
	if nfo == "" {
		if len(videos) > 1 {
			nfo = "movie.nfo"
		} else {
			nfo = videoBase(videos[0]) + ".nfo"
		}
	}
	if s.opt.DryRun {
		s.say("write %s", s.rel(filepath.Join(dir, nfo)))
		return nil
	}

	var coll *tmdb.Collection
	if m.Collection != nil {
		if c, err := s.tmdb.Collection(m.Collection.ID); err == nil {
			coll = c
		} else {
			coll = &tmdb.Collection{ID: m.Collection.ID, Name: m.Collection.Name}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, nfo), buildNFO(m, coll, s.cfg.Region, s.tmdb), 0o644); err != nil {
		return err
	}
	s.art(m.PosterPath, "w780", filepath.Join(dir, "poster.jpg"))
	s.art(m.BackdropPath, "w1280", filepath.Join(dir, "fanart.jpg"))

	// Collection artwork, for Kodi's "movie set information folder".
	if coll != nil && !s.sets[coll.ID] {
		s.sets[coll.ID] = true
		setDir := filepath.Join(lib, "_sets", SafeName(coll.Name))
		if err := os.MkdirAll(setDir, 0o755); err == nil {
			s.art(coll.PosterPath, "w780", filepath.Join(setDir, "poster.jpg"))
			s.art(coll.BackdropPath, "w1280", filepath.Join(setDir, "fanart.jpg"))
		}
	}
	return nil
}

// moveExtraSubs brings an imported video's subtitles from Subs/ folders
// along with it.
func (s *filer) moveExtraSubs(video, dir, base string) {
	if s.imp == nil {
		return
	}
	for _, sub := range s.imp.extraSubs[video] {
		if err := s.sidecarRename(sub.path, filepath.Join(dir, base+sub.suffix)); err != nil {
			s.printf("  ! %v\n", err)
		}
	}
}

func (s *filer) art(path, size, dest string) {
	if path == "" {
		return
	}
	if _, err := os.Stat(dest); err == nil && !s.opt.Force {
		return
	}
	if err := s.tmdb.DownloadImage(path, size, dest); err != nil {
		s.printf("  ! %s: %v\n", filepath.Base(dest), err)
	}
}

// rename moves old to new, coping with case-only renames on case-insensitive
// filesystems (exFAT) and refusing to overwrite anything.
func (s *filer) rename(old, new string) error { return s.mv(old, new, true) }

// sidecarRename is rename without the running commentary: subtitles, NFOs
// and thumbnails follow their video, and listing each one is just noise.
func (s *filer) sidecarRename(old, new string) error { return s.mv(old, new, false) }

func (s *filer) mv(old, new string, loud bool) error {
	if old == new {
		return nil
	}
	copying := s.imp != nil && s.opt.Copy && Within(s.imp.src, old)
	if loud {
		if copying {
			s.say("%s ⇒ %s (copy)", s.rel(old), s.rel(new))
		} else {
			s.say("%s → %s", s.rel(old), s.rel(new))
		}
	}
	if s.imp != nil {
		s.imp.moved[old] = new
	}
	if s.opt.DryRun {
		return nil
	}
	err := s.doMove(old, new, copying)
	if err == nil && (s.imp != nil) && loud {
		if s.imp.log != nil {
			verb := "move"
			if copying {
				verb = "copy"
			}
			fmt.Fprintf(s.imp.log, "%s\t%s\t%s\t%s\n", time.Now().Format(time.RFC3339), verb, old, new)
		}
		// "New movies" goes by date added, not the date on the file.
		if st, err := os.Lstat(new); err == nil && st.Mode().IsRegular() {
			now := time.Now()
			os.Chtimes(new, now, now)
		}
	}
	if err != nil && s.imp != nil {
		delete(s.imp.moved, old)
	}
	return err
}

func (s *filer) doMove(old, new string, copying bool) error {
	if copying {
		if Exists(new) {
			return fmt.Errorf("%s already exists", s.rel(new))
		}
		return copyPath(old, new)
	}
	if nst, err := os.Lstat(new); err == nil {
		ost, err := os.Lstat(old)
		if err != nil || !os.SameFile(ost, nst) {
			return fmt.Errorf("%s already exists", s.rel(new))
		}
		tmp := old + ".mvdtmp"
		if err := os.Rename(old, tmp); err != nil {
			return err
		}
		if err := renameOS(tmp, new); err != nil {
			// Put it back under its old name rather than leave it stranded as
			// a .mvdtmp, which every scan skips.
			os.Rename(tmp, old)
			return err
		}
		return nil
	}
	err := os.Rename(old, new)
	if errors.Is(err, syscall.EXDEV) { // another drive: copy, then remove
		if err = copyPath(old, new); err == nil {
			err = os.RemoveAll(old)
		}
	}
	return err
}

// renameOS is os.Rename, swapped out by tests to force the rare failures
// (an I/O error between the two steps of a case-only rename).
var renameOS = os.Rename

type errDuplicate struct{ path string }

func (e errDuplicate) Error() string { return e.path + " is already in the library" }

// partName is a movie's file name, with " - partN" for stacks.
func partName(base string, i, n int) string {
	if n > 1 {
		return fmt.Sprintf("%s - part%d", base, i+1)
	}
	return base
}

// videoExt is a video's extension, lowercased, or sniffed if it has none.
func videoExt(p string) string {
	if isVideoExt(p) {
		return strings.ToLower(filepath.Ext(p))
	}
	return sniffVideo(p)
}

func Within(dir, p string) bool {
	r, err := filepath.Rel(dir, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// copyPath copies a file or a folder, writing via temp names so a copy cut
// short never looks finished.
func copyPath(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return copyFile(src, dst, st.Mode().Perm())
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		return copyFile(p, out, info.Mode().Perm())
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".mvdtmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm|0o200)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = syncFile(out) // before the source is removed
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}

func (s *filer) writer() io.Writer {
	if s.out != nil {
		return s.out
	}
	return os.Stdout
}

func (s *filer) printf(format string, a ...any) { fmt.Fprintf(s.writer(), format, a...) }
func (s *filer) println(a ...any)               { fmt.Fprintln(s.writer(), a...) }
func (s *filer) print(a ...any)                 { fmt.Fprint(s.writer(), a...) }

func (s *filer) say(format string, a ...any) {
	if s.opt.DryRun {
		s.printf("  [dry run] "+format+"\n", a...)
	} else {
		s.printf("  "+format+"\n", a...)
	}
}

// rel shortens a path for display: relative to the folder being worked on,
// or, when importing, to the library ("movies/Dune (2021)/...").
func (s *filer) rel(p string) string {
	if s.imp == nil {
		if r, err := filepath.Rel(s.root, p); err == nil {
			return r
		}
		return p
	}
	for _, d := range []string{s.cfg.MoviesDir, s.cfg.TVDir, s.imp.src, s.cfg.TrashDir} {
		if d != "" && Within(d, p) {
			r, _ := filepath.Rel(d, p)
			return filepath.Join(filepath.Base(d), r)
		}
	}
	return p
}

// videoBase is the file name without its video extension (a file that has
// lost its extension keeps its whole name).
func videoBase(path string) string {
	name := filepath.Base(path)
	if isVideoExt(name) {
		return strings.TrimSuffix(name, filepath.Ext(name))
	}
	return name
}

var artSuffixes = []string{"-thumb.", "-poster.", "-fanart.", "-landscape.", "-clearlogo."}

// sidecars finds the files that belong with a video: "Base.en.srt",
// "Base.nfo", "Base-thumb.jpg". The suffix must look like a language/flag
// tag plus extension, so "Alien.Resurrection.srt" isn't taken for Alien's.
func sidecars(dir, base, video string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return sidecarsIn(ents, dir, base, video)
}

// sidecarsIn is sidecars from dir's listing, for when it's already read.
func sidecarsIn(ents []os.DirEntry, dir, base, video string) []string {
	var out []string
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, base) || isVideoExt(name) {
			continue
		}
		p := filepath.Join(dir, name)
		if p == video {
			continue
		}
		rest := name[len(base):]
		if !sidecarExts[strings.ToLower(filepath.Ext(rest))] {
			continue
		}
		ok := false
		for _, a := range artSuffixes {
			if strings.HasPrefix(rest, a) {
				ok = true
			}
		}
		if strings.HasPrefix(rest, ".") {
			parts := strings.Split(rest[1:], ".")
			ok = len(parts) <= 3
			for _, pt := range parts[:len(parts)-1] {
				if len(pt) > 7 {
					ok = false
				}
			}
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

// Stats is what a run did.
type Stats struct {
	Movies, Episodes, Left, Duplicates, Waiting, Seeding, Failed int
	Changed                                                      int // movies and shows changed: worth a rescan
}

// SetOutput sends the running commentary somewhere other than stdout.
func (s *filer) SetOutput(w io.Writer) { s.out = w }

// SetAsk is who answers questions (AskTerminal, at a terminal). Without
// one, nothing is asked: what it isn't sure of is skipped.
func (s *filer) SetAsk(ask AskFunc) { s.ask = ask }
