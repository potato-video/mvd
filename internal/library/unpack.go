//go:build unpack

package library

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"mvd/internal/archive"
)

// Unpacking archives on import: .zip, and .rar sets (stored ones by
// ourselves, compressed ones through unrar when it's installed). Entries are
// extracted beside their archive, flattened to their base names, keeping
// only videos and the files that can be uploaded (subtitles, .nfo).

const (
	// unpackBuffer is the one buffer data goes through.
	unpackBuffer = 256 << 10
	// unpackMargin is kept free on the drive beyond what's extracted.
	unpackMargin = 64 << 20
	// maxSideFile caps a subtitle or .nfo from an archive: anything bigger is
	// not what it says it is.
	maxSideFile = 64 << 20
	// maxUnpackEntries caps the files taken from one archive: a release
	// has a few, a season a few dozen.
	maxUnpackEntries = 1000
	// maxUnpackTotal caps what one archive may say it holds (a petabyte):
	// declared sizes are summed, and must not overflow.
	maxUnpackTotal = 1 << 50
	// maxVideoRatio is the most a compressed video may come out at for
	// each byte in the archive. Video is compressed already; one that says
	// it grows by more is a bomb, made to fill the drive.
	maxVideoRatio = 20
)

// unrarPath is unrar's path, found once, or "" when it isn't installed.
// Tests set it.
var unrarPath = func() string { p, _ := exec.LookPath("unrar"); return p }()

// unrarMemory is the most memory unrar may need for an entry's dictionary:
// a quarter of the RAM, or 32 MB at least (on the router, 32 MB).
var unrarMemory = func() int64 {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				return max(32<<20, kb<<10/4)
			}
		}
	}
	return 32 << 20
}()

// unrarStall is how long unrar may go without sending anything before it's
// stopped: a big entry takes as long as it takes, but one that's stuck
// would hold the import lock for good.
var unrarStall = 10 * time.Minute

// freeSpace is FreeSpace, swapped out in tests.
var freeSpace = FreeSpace

// unpackBuilt says this mvd unpacks archives (the unpack feature).
const unpackBuilt = true

// isArchive says whether a name is an archive mvd can unpack (any volume).
func isArchive(name string) bool { return archive.Kind(name) != "" }

// archiveFirst says whether an archive is one to start from: a zip, or a
// RAR set's first volume.
func archiveFirst(name string) bool { return archive.First(name) }

// archiveFirstVolume is the first volume of the set a volume belongs to.
func archiveFirstVolume(name string) string { return archive.FirstVolume(name) }

// ArchiveExts are the archive extensions that can be uploaded, for a file
// picker: .zip, .rar, and the .r00 to .r99 of old-style RAR sets.
func ArchiveExts() []string {
	exts := []string{".zip", ".rar"}
	for i := 0; i < 100; i++ {
		exts = append(exts, fmt.Sprintf(".r%02d", i))
	}
	return exts
}

// unpackEntry is one entry to extract.
type unpackEntry struct {
	name  string // its name in the archive
	out   string // the flattened name it's written as
	size  int64
	unrar bool // compressed RAR data: through unrar
}

// flatName is an entry's name reduced to a safe file name (no folders, no
// "..", not hidden), or "" if nothing safe is left.
func flatName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name))
	if name == "" || name == "." || name == ".." || name == "/" || strings.HasPrefix(name, ".") {
		return ""
	}
	return name
}

var errUnknownSize = errors.New("an entry doesn't say how big it is")

// pick decides what happens to an entry: extracted (ok), passed over (not
// ok, no error), or an error that stops the archive.
func pick(kind string, e *archive.Entry) (unpackEntry, bool, error) {
	out := flatName(e.Name)
	switch {
	case e.Dir || out == "" || !(isVideoExt(out) || UploadExts[strings.ToLower(filepath.Ext(out))]):
		return unpackEntry{}, false, nil
	case e.Link:
		return unpackEntry{}, false, nil // never followed or made
	case e.Encrypted:
		return unpackEntry{}, false, fmt.Errorf("%s is %w (it needs a password)", out, archive.ErrEncrypted)
	case e.Size < 0:
		return unpackEntry{}, false, errUnknownSize
	case !isVideoExt(out) && e.Size > maxSideFile:
		return unpackEntry{}, false, nil
	case isVideoExt(out) && e.Packed >= 0 && e.Size/maxVideoRatio > max(e.Packed, 1<<10):
		return unpackEntry{}, false, fmt.Errorf("%s says it's %s from %s compressed, and video doesn't compress like that",
			out, SizeStr(e.Size), SizeStr(e.Packed))
	}
	u := unpackEntry{name: e.Name, out: out, size: e.Size}
	if kind == "rar" && !e.Stored {
		if unrarPath == "" {
			return u, false, errors.New("it's compressed: mvd unpacks stored (-m0) RARs, which is how most releases come, " +
				"and needs unrar installed for the rest (or unpack it yourself)")
		}
		if e.Dict > unrarMemory {
			return u, false, fmt.Errorf("%s needs %s of memory to unpack, more than can be spared here (unpack it yourself)",
				out, SizeStr(e.Dict))
		}
		if strings.ContainsAny(e.Name, "*?") || strings.HasPrefix(e.Name, "@") {
			return u, false, fmt.Errorf("%s: a name unrar would take as a pattern or a list of names", out)
		}
		u.unrar = true
	}
	return u, true, nil
}

// listArchive is what would come out of an archive.
func listArchive(a string) ([]unpackEntry, error) {
	kind := archive.Kind(a)
	var out []unpackEntry
	var total int64
	err := archive.Walk(a, func(e *archive.Entry, _ io.Reader) error {
		u, ok, err := pick(kind, e)
		if !ok {
			return err
		}
		if len(out) == maxUnpackEntries {
			return fmt.Errorf("it holds more than %d files to unpack", maxUnpackEntries)
		}
		if u.size > maxUnpackTotal-total {
			return errors.New("it says it holds more than any drive")
		}
		total += u.size
		out = append(out, u)
		return err
	})
	return out, err
}

// unpackItem unpacks an item's archives (first volumes) beside themselves,
// or says why it couldn't.
func (s *Importer) unpackItem(item, label string, archives []string, u unpacked) error {
	lists := make([][]unpackEntry, len(archives))
	var need int64
	for i, a := range archives {
		es, err := listArchive(a)
		if err != nil {
			return fmt.Errorf("can't unpack %s: %v", filepath.Base(a), err)
		}
		lists[i] = es
		for _, e := range es {
			need += e.size
		}
	}
	if s.opt.DryRun {
		for i, a := range archives {
			s.say("unpack %s: %d file(s), %s", s.rel(a), len(lists[i]), SizeStr(sumSizes(lists[i])))
		}
		return nil
	}
	if err := s.roomToUnpack(item, need); err != nil {
		return err
	}
	buf := make([]byte, unpackBuffer)
	for i, a := range archives {
		start := time.Now()
		files, err := s.extract(a, lists[i], buf)
		if err != nil {
			return fmt.Errorf("couldn't unpack %s: %v", filepath.Base(a), err)
		}
		if err := u.note(item, a, files); err != nil {
			s.printf("  ! %s: %v\n", label, err)
		}
		if len(files) == 0 {
			s.say("unpack %s: nothing in it worth keeping", s.rel(a))
		} else {
			s.say("unpack %s: %d file(s), %s, in %s", s.rel(a), len(files), SizeStr(sumSizes(lists[i])),
				time.Since(start).Round(time.Second))
		}
		if s.imp.log != nil {
			for _, f := range files {
				fmt.Fprintf(s.imp.log, "%s\tunpack\t%s\t%s\n", time.Now().Format(time.RFC3339), a, f)
			}
		}
		// The new files have only just changed; they're whole.
		for _, f := range files {
			s.markComplete(f)
		}
	}
	s.markComplete(item)
	return nil
}

func (s *Importer) markComplete(p string) {
	if s.complete == nil {
		s.complete = map[string]bool{}
	}
	s.complete[filepath.Clean(p)] = true
}

func sumSizes(es []unpackEntry) int64 {
	var n int64
	for _, e := range es {
		n += e.size
	}
	return n
}

// roomToUnpack checks the drive has room for need more bytes. The archives
// stay until the item is filed, and the trash keeps them on the drive after
// that if it's on the same one; so when the trash is what's in the way, its
// oldest leftovers are cleared to make room.
func (s *Importer) roomToUnpack(item string, need int64) error {
	free, ok := freeSpace(item)
	need += unpackMargin
	if !ok || free >= need {
		return nil
	}
	short := need - free
	var held int64
	if t := s.cfg.TrashDir; t != "" && sameDrive(item, t) {
		held = trashUsage(t)
		if held >= short {
			n := purgeTrash(t, s.cfg.TrashMaxAge, held-short, time.Now())
			if free, ok = freeSpace(item); ok && free >= need {
				s.printf("trash: cleared %d old leftover(s) to make room for unpacking\n", n)
				return nil
			}
		}
	}
	msg := fmt.Sprintf("unpacking needs %s free, and the drive has %s", SizeStr(need), SizeStr(free))
	if held > 0 {
		msg += fmt.Sprintf(" (even with the trash's %s cleared)", SizeStr(held))
	}
	return errors.New(msg)
}

// extract writes an archive's entries beside it, under their flattened
// names. Each is capped at the size it declared, and so is the total, since
// declared sizes can lie. On an error, whatever it wrote is removed (but
// not a file it found already there, which may not be its own).
func (s *Importer) extract(a string, list []unpackEntry, buf []byte) (files []string, err error) {
	dir := filepath.Dir(a)
	kind := archive.Kind(a)
	budget := sumSizes(list)
	var total int64
	var tmp string
	var wrote []string
	defer func() {
		if err != nil {
			if tmp != "" {
				os.Remove(tmp)
			}
			for _, f := range wrote {
				os.Remove(f)
			}
			files = nil
		}
	}()
	taken := map[string]bool{}
	i := 0
	err = archive.Walk(a, func(e *archive.Entry, r io.Reader) error {
		u, ok, err := pick(kind, e)
		if err != nil || !ok {
			return err
		}
		if i >= len(list) || list[i].name != u.name {
			return errors.New("it changed while being unpacked")
		}
		i++
		out, done := unpackTarget(dir, u, taken)
		if done {
			files = append(files, out) // already here from a run cut short
			total += u.size
			return nil
		}
		var cmd *exec.Cmd
		var stalled atomic.Bool
		if u.unrar {
			cmd = exec.Command(unrarPath, "p", "-inul", "-p-", "-y", "--", a, u.name)
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				return err
			}
			if err := cmd.Start(); err != nil {
				return fmt.Errorf("unrar: %w", err)
			}
			watch := time.AfterFunc(unrarStall, func() { stalled.Store(true); cmd.Process.Kill() })
			defer watch.Stop()
			r = readWatched{pipe, func() { watch.Reset(unrarStall) }}
		}
		tmp = out + ".mvdtmp"
		n, err := writeCapped(tmp, r, u.size, buf)
		if cmd != nil {
			if stalled.Load() {
				err = fmt.Errorf("unrar sent nothing for %s, so was stopped", unrarStall)
			}
			if err != nil {
				cmd.Process.Kill()
			}
			if werr := cmd.Wait(); err == nil && werr != nil {
				err = fmt.Errorf("unrar: %v", werr)
			}
		}
		total += n
		switch {
		case err != nil:
		case total > budget:
			err = errors.New("it holds more than it says")
		case n < u.size:
			err = fmt.Errorf("%s is cut short", u.out)
		}
		if err == nil {
			err = os.Rename(tmp, out)
		}
		if err != nil {
			return err
		}
		tmp = ""
		files = append(files, out)
		wrote = append(wrote, out)
		return nil
	})
	return files, err
}

// unpackTarget picks the name an entry is written as: its own, or with
// " (2)" and so on when that's taken. done means the file is already there,
// the right size, from an earlier run that was cut short before it finished.
func unpackTarget(dir string, u unpackEntry, taken map[string]bool) (string, bool) {
	ext := filepath.Ext(u.out)
	stem := strings.TrimSuffix(u.out, ext)
	for n := 1; ; n++ {
		name := u.out
		if n > 1 {
			name = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		}
		if taken[name] {
			continue
		}
		p := filepath.Join(dir, name)
		st, err := os.Lstat(p)
		if err != nil {
			taken[name] = true
			return p, false
		}
		if st.Mode().IsRegular() && st.Size() == u.size && n == 1 {
			taken[name] = true
			return p, true
		}
	}
}

// readWatched calls read after each read that gets something.
type readWatched struct {
	r    io.Reader
	read func()
}

func (w readWatched) Read(b []byte) (int, error) {
	n, err := w.r.Read(b)
	if n > 0 {
		w.read()
	}
	return n, err
}

// writeCapped copies at most size bytes from r to a new file at p, through
// buf, and fails if r has more.
func writeCapped(p string, r io.Reader, size int64, buf []byte) (int64, error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	// Hide the file's ReadFrom, so the copy goes through buf.
	n, err := io.CopyBuffer(struct{ io.Writer }{f}, io.LimitReader(r, size+1), buf)
	if err == nil && n > size {
		err = errors.New("an entry is bigger than it says")
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return min(n, size), err
}

// unpackLoose puts a loose archive (and the rest of its set, and its hint)
// into a folder of its own, named after it, so it can be unpacked inside it.
// Returns the folder ("" in a dry run), or why it couldn't.
func (s *Importer) unpackLoose(p, label string) (string, error) {
	vols := archive.Volumes(p)
	base := filepath.Base(p)
	name := base
	if m := archiveSuffix.FindStringIndex(base); m != nil {
		name = base[:m[0]]
	}
	if name == "" {
		name = base + " unpacked"
	}
	dir := filepath.Join(filepath.Dir(p), name)
	for n := 2; Exists(dir); n++ {
		dir = filepath.Join(filepath.Dir(p), fmt.Sprintf("%s (%d)", name, n))
	}
	s.say("%s → %s/, to unpack it there", label, s.rel(dir))
	if s.opt.DryRun {
		return "", nil
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	hint := strings.TrimSuffix(p, filepath.Ext(p)) + ".mvd"
	if h := filepath.Join(filepath.Dir(p), name+".mvd"); Exists(h) {
		hint = h
	}
	moves := map[string]string{}
	for _, v := range vols {
		moves[v] = filepath.Join(dir, filepath.Base(v))
	}
	if Exists(hint) {
		moves[hint] = filepath.Join(dir, ".mvd")
	}
	for from, to := range moves {
		if err := os.Rename(from, to); err != nil {
			return "", err
		}
	}
	s.markComplete(dir)
	return dir, nil
}

var archiveSuffix = regexp.MustCompile(`(?i)(\.part\d+)?\.rar$|\.zip$`)

// trashUsage is how much the trash holds, counting only what mvd put there.
func trashUsage(dir string) int64 {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range ents {
		if trashName.MatchString(e.Name()) {
			n += treeSize(filepath.Join(dir, e.Name()))
		}
	}
	return n
}
