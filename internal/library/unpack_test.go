//go:build unpack

package library

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mvd/internal/archive"
	"mvd/internal/testutil"
)

const testMKV = "\x1a\x45\xdf\xa3 matroska, padded out to cross RAR volumes nicely"

func writeZip(t *testing.T, p string, files map[string]string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, data := range files {
		if strings.HasPrefix(name, "link:") {
			h := &zip.FileHeader{Name: strings.TrimPrefix(name, "link:")}
			h.SetMode(os.ModeSymlink | 0o777)
			w, _ := z.CreateHeader(h)
			w.Write([]byte(data))
			continue
		}
		w, _ := z.Create(name)
		w.Write([]byte(data))
	}
	z.Close()
	f.Close()
}

func runImport(t *testing.T, s *Importer) string {
	t.Helper()
	var out bytes.Buffer
	s.out = &out
	s.Run()
	return out.String()
}

func TestUnpackOnImport(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	mkv := []byte(strings.Repeat(testMKV, 20))

	// A scene release: a RAR5 set in its folder, with the usual clutter.
	rel := filepath.Join(l.Import, "Dune.2021.1080p.BluRay-GRP")
	os.MkdirAll(rel, 0o755)
	testutil.WriteRAR(t, rel, "dune.2021.1080p-grp", true, false, 400, []testutil.ArchiveFile{
		{Name: "Dune.2021.1080p.BluRay-GRP.mkv", Data: mkv},
	})
	testutil.Write(t, filepath.Join(rel, "dune.2021.1080p-grp.sfv"), "checksums")
	testutil.Write(t, filepath.Join(rel, "dune.2021.1080p-grp.nfo"), "release notes")
	// Its subtitles in a RAR4 set of their own, in Subs/.
	os.MkdirAll(filepath.Join(rel, "Subs"), 0o755)
	testutil.WriteRAR(t, filepath.Join(rel, "Subs"), "dune.subs", false, true, 1<<20, []testutil.ArchiveFile{
		{Name: `Subs\English.srt`, Data: []byte("1\nHello")},
	})

	// A loose RAR4 set with old names (.rar, .r00, .r01), and its hint.
	testutil.WriteRAR(t, l.Import, "Fast.Five.2011.720p", false, true, 300, []testutil.ArchiveFile{
		{Name: "fast.five.2011.720p.mkv", Data: mkv},
	})

	// A movie with its subtitles zipped beside it.
	testutil.Write(t, filepath.Join(l.Import, "Children.of.Men.2006/Children.of.Men.2006.mkv"), testMKV)
	writeZip(t, filepath.Join(l.Import, "Children.of.Men.2006/subs.zip"), map[string]string{
		"English.srt": "1\nHi", "French.srt": "1\nSalut", "readme.txt": "ignored",
	})

	s := newTestImporter(t, l, srv, Options{}, "")
	out := runImport(t, s)
	if s.Stats().Movies != 3 || s.Stats().Left != 0 {
		t.Errorf("movies=%d left=%d, want 3, 0\n%s", s.Stats().Movies, s.Stats().Left, out)
	}
	testutil.MustExist(t, l.Movies,
		"Dune (2021)/Dune (2021).mkv",
		"Dune (2021)/Dune (2021).en.srt",
		"Fast Five (2011)/Fast Five (2011).mkv",
		"Children of Men (2006)/Children of Men (2006).mkv",
		"Children of Men (2006)/Children of Men (2006).en.srt",
		"Children of Men (2006)/Children of Men (2006).fr.srt",
	)
	if b, _ := os.ReadFile(filepath.Join(l.Movies, "Dune (2021)/Dune (2021).mkv")); !bytes.Equal(b, mkv) {
		t.Errorf("Dune came out wrong: %d bytes", len(b))
	}
	// The archives went to the trash with the rest; import/ is empty.
	if ents, _ := os.ReadDir(l.Import); len(ents) != 0 {
		t.Errorf("left in import: %v\n%s", testutil.Tree(l.Import), out)
	}
	trash := strings.Join(testutil.Tree(l.Trash), "\n")
	for _, want := range []string{"dune.2021.1080p-grp.part03.rar", "Fast.Five.2011.720p.r01", "subs.zip"} {
		if !strings.Contains(trash, want) {
			t.Errorf("%s not in the trash:\n%s", want, trash)
		}
	}
}

func TestUnpackDryRunAndCopy(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	rel := filepath.Join(l.Import, "Fast.Five.2011")
	os.MkdirAll(rel, 0o755)
	testutil.WriteRAR(t, rel, "ff", true, false, 1<<20, []testutil.ArchiveFile{{Name: "ff.mkv", Data: []byte(testMKV)}})
	testutil.WriteRAR(t, l.Import, "Boogie.Nights.1997", true, false, 1<<20, []testutil.ArchiveFile{{Name: "bn.mkv", Data: []byte(testMKV)}})
	before := strings.Join(testutil.Tree(l.Video), "\n")

	out := runImport(t, newTestImporter(t, l, srv, Options{DryRun: true}, ""))
	if after := strings.Join(testutil.Tree(l.Video), "\n"); after != before {
		t.Errorf("dry run changed things:\n%s", after)
	}
	if !strings.Contains(out, "unpack import/Fast.Five.2011/ff.rar: 1 file(s)") || !strings.Contains(out, "to unpack it there") {
		t.Errorf("dry run didn't say what it would unpack:\n%s", out)
	}

	// -copy never writes into the source, so it doesn't unpack.
	out = runImport(t, newTestImporter(t, l, srv, Options{Copy: true}, ""))
	if !strings.Contains(out, "-copy doesn't unpack") {
		t.Errorf("copy:\n%s", out)
	}
	if after := strings.Join(testutil.Tree(l.Video), "\n"); after != before {
		t.Errorf("copy changed the source:\n%s", after)
	}
}

// Entries are flattened to safe base names, and only videos, subtitles and
// .nfo files come out: nothing lands outside the item.
func TestUnpackHostileZip(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	writeZip(t, filepath.Join(l.Import, "Fast.Five.2011/Fast.Five.2011.zip"), map[string]string{
		"../../../escape.mkv":          testMKV,
		"/abs/path/Fast.Five.2011.mkv": testMKV,
		"..":                           "x",
		".hidden.srt":                  "x",
		"run.sh":                       "#!/bin/sh",
		"link:evil.srt":                "/etc/passwd",
		"sub\\dir\\English.srt":        "1\nHi",
	})
	s := newTestImporter(t, l, srv, Options{}, "")
	out := runImport(t, s)
	if Exists(filepath.Join(l.Video, "escape.mkv")) || Exists(filepath.Join(filepath.Dir(l.Video), "escape.mkv")) {
		t.Fatal("an entry escaped the item")
	}
	// Two videos came out (escape.mkv flattened into the item, and the
	// movie), so the item needs a person; nothing else came out.
	got := strings.Join(testutil.Tree(l.Import), "\n")
	for _, bad := range []string{"run.sh", ".hidden.srt", "evil.srt", "passwd"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s came out:\n%s", bad, got)
		}
	}
	testutil.MustExist(t, l.Movies, "Fast Five (2011)/Fast Five (2011).en.srt")
	if !strings.Contains(got, "escape.mkv") {
		t.Errorf("expected flattened entries:\n%s\n%s\n%s", got, out, strings.Join(testutil.Tree(l.Movies), "\n"))
	}
}

func TestUnpackCompressed(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	rel := filepath.Join(l.Import, "Fast.Five.2011")
	os.MkdirAll(rel, 0o755)
	testutil.WriteRAR(t, rel, "ff", true, false, 1<<20, []testutil.ArchiveFile{
		{Name: "ff.mkv", Data: []byte("tiny"), Method: 3},
	})

	saved := unrarPath
	defer func() { unrarPath = saved }()

	// Without unrar: said clearly, and left.
	unrarPath = ""
	s := newTestImporter(t, l, srv, Options{}, "")
	out := runImport(t, s)
	if !strings.Contains(out, "it's compressed") || !strings.Contains(out, "unrar") || s.Stats().Left != 1 {
		t.Errorf("compressed without unrar: left=%d\n%s", s.Stats().Left, out)
	}
	testutil.MustExist(t, l.Import, "Fast.Five.2011/ff.rar")

	// With unrar: it's asked for each entry, and its output goes through
	// the same checks. (A stand-in that prints what it's asked for.)
	bin := t.TempDir()
	unrarPath = filepath.Join(bin, "unrar")
	script := "#!/bin/sh\n[ \"$1\" = p ] || exit 9\nfor a; do last=$a; done\nprintf '\\032\\105\\337\\243 from unrar: %s' \"$last\"\n"
	os.WriteFile(unrarPath, []byte(script), 0o755)
	// It declares 4 bytes; the stand-in prints more, so it's refused.
	out = runImport(t, newTestImporter(t, l, srv, Options{}, ""))
	if !strings.Contains(out, "bigger than it says") {
		t.Errorf("unrar's overlong output wasn't refused:\n%s", out)
	}
	if Exists(filepath.Join(rel, "ff.mkv")) || Exists(filepath.Join(rel, "ff.mkv.mvdtmp")) {
		t.Error("a refused entry was left behind")
	}
	want := "\x1a\x45\xdf\xa3 from unrar: ff.mkv"
	os.RemoveAll(rel)
	os.MkdirAll(rel, 0o755)
	testutil.WriteRAR(t, rel, "ff", true, false, 1<<20, []testutil.ArchiveFile{
		{Name: "ff.mkv", Data: []byte(want), Method: 3},
	})
	s = newTestImporter(t, l, srv, Options{}, "")
	out = runImport(t, s)
	if s.Stats().Movies != 1 {
		t.Fatalf("through unrar: movies=%d\n%s", s.Stats().Movies, out)
	}
	if b, _ := os.ReadFile(filepath.Join(l.Movies, "Fast Five (2011)/Fast Five (2011).mkv")); string(b) != want {
		t.Errorf("got %q", b)
	}
}

// A damaged archive is left as it is, with nothing half-written beside it.
func TestUnpackDamaged(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	rel := filepath.Join(l.Import, "Fast.Five.2011")
	os.MkdirAll(rel, 0o755)
	testutil.WriteRAR(t, rel, "ff", true, false, 200, []testutil.ArchiveFile{
		{Name: "ff.mkv", Data: []byte(strings.Repeat(testMKV, 10))},
	})
	p := filepath.Join(rel, "ff.part02.rar")
	b, _ := os.ReadFile(p)
	b[len(b)-20] ^= 0xff
	os.WriteFile(p, b, 0o644)
	s := newTestImporter(t, l, srv, Options{}, "")
	out := runImport(t, s)
	if !strings.Contains(out, "checksum") || s.Stats().Left != 1 {
		t.Errorf("left=%d\n%s", s.Stats().Left, out)
	}
	for _, f := range testutil.Tree(rel) {
		if strings.Contains(f, ".mkv") {
			t.Errorf("left behind: %s", f)
		}
	}
}

func TestUnpackRoom(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	rel := filepath.Join(l.Import, "Fast.Five.2011")
	os.MkdirAll(rel, 0o755)
	testutil.WriteRAR(t, rel, "ff", true, false, 1<<20, []testutil.ArchiveFile{{Name: "ff.mkv", Data: []byte(testMKV)}})
	// The trash holds an old leftover of 1 MB.
	old := time.Now().Add(-time.Hour).Format(trashStamp) + " Old Thing"
	testutil.Write(t, filepath.Join(l.Trash, old, "big.bin"), strings.Repeat("x", 1<<20))

	saved := freeSpace
	defer func() { freeSpace = saved }()

	// Not enough room even with the trash cleared: left, and the trash kept.
	freeSpace = func(string) (int64, bool) { return 1 << 20, true }
	s := newTestImporter(t, l, srv, Options{}, "")
	out := runImport(t, s)
	if !strings.Contains(out, "unpacking needs") || !strings.Contains(out, "even with the trash") || s.Stats().Movies != 0 {
		t.Errorf("no room:\n%s", out)
	}
	testutil.MustExist(t, l.Trash, old)

	// Room once the trash is cleared: it's cleared, and the import goes on.
	room := false
	freeSpace = func(string) (int64, bool) {
		if !Exists(filepath.Join(l.Trash, old)) {
			room = true
		}
		if room {
			return 1 << 30, true
		}
		return unpackMargin, true
	}
	s = newTestImporter(t, l, srv, Options{}, "")
	out = runImport(t, s)
	if !strings.Contains(out, "to make room") || s.Stats().Movies != 1 {
		t.Errorf("room after clearing the trash: movies=%d\n%s", s.Stats().Movies, out)
	}
	testutil.MustNotExist(t, l.Trash, old)
}

func TestWriteCapped(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x")
	buf := make([]byte, 4)
	if n, err := writeCapped(p, strings.NewReader("0123456789"), 5, buf); err == nil || n != 5 {
		t.Errorf("overlong: n=%d err=%v", n, err)
	}
	if n, err := writeCapped(p, strings.NewReader("01234"), 5, buf); err != nil || n != 5 {
		t.Errorf("exact: n=%d err=%v", n, err)
	}
}

func TestFlatName(t *testing.T) {
	for in, want := range map[string]string{
		"a/b/c.mkv": "c.mkv", `a\b\c.srt`: "c.srt", "../../x.mkv": "x.mkv", "/etc/passwd": "passwd",
		"..": "", ".": "", "dir/": "dir", ".hidden": "", "a/\x01b.srt": "b.srt", "": "",
	} {
		if got := flatName(in); got != want {
			t.Errorf("flatName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUploadableArchives(t *testing.T) {
	for name, want := range map[string]bool{
		"subs.zip": true, "Movie.part01.rar": true, "movie.r07": true, "movie.RAR": true,
		"movie.7z": false, "movie.001": false, "movie.exe": false,
	} {
		if Uploadable(name) != want {
			t.Errorf("Uploadable(%s) = %v", name, !want)
		}
	}
}

// An archive that can't be unpacked holds up its item, loose videos and all.
func TestUnpackFailureHoldsItem(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	rel := filepath.Join(l.Import, "Fast.Five.2011")
	testutil.Write(t, filepath.Join(rel, "Fast.Five.2011-featurette.mkv"), testMKV)
	testutil.Write(t, filepath.Join(rel, "Fast.Five.2011.mkv"), testMKV)
	testutil.Write(t, filepath.Join(rel, "subs.rar"), "not really a rar")
	s := newTestImporter(t, l, srv, Options{}, "")
	out := runImport(t, s)
	if s.Stats().Movies != 0 || s.Stats().Left != 1 || !strings.Contains(out, "can't unpack subs.rar") {
		t.Errorf("movies=%d left=%d\n%s", s.Stats().Movies, s.Stats().Left, out)
	}
	testutil.MustExist(t, rel, "subs.rar", "Fast.Five.2011.mkv")
}

// Review M5: an archive can't make the unpack take memory or room without
// limit: too many entries to pick, or declared sizes whose sum overflows.
func TestListArchiveLimits(t *testing.T) {
	dir := t.TempDir()
	many := map[string]string{}
	for i := 0; i <= maxUnpackEntries; i++ {
		many[fmt.Sprintf("%04d.srt", i)] = "1"
	}
	writeZip(t, filepath.Join(dir, "many.zip"), many)
	if es, err := listArchive(filepath.Join(dir, "many.zip")); err == nil {
		t.Errorf("%d entries picked", len(es))
	}

	p := filepath.Join(dir, "huge.zip")
	f, _ := os.Create(p)
	z := zip.NewWriter(f)
	for _, name := range []string{"a.mkv", "b.mkv"} {
		w, _ := z.CreateRaw(&zip.FileHeader{Name: name, Method: zip.Store, UncompressedSize64: 1 << 62, CompressedSize64: 4})
		w.Write([]byte("abcd"))
	}
	z.Close()
	f.Close()
	if es, err := listArchive(p); err == nil {
		t.Errorf("declared sizes summing past int64 listed: %d entries, %d bytes", len(es), sumSizes(es))
	}
}

// Review M5: video doesn't compress, so a compressed video that comes out
// many times its size is a bomb, made to fill the drive.
func TestUnpackRefusesBombs(t *testing.T) {
	dir := t.TempDir()
	bomb := filepath.Join(dir, "bomb.zip")
	writeZip(t, bomb, map[string]string{"x.mkv": strings.Repeat("\x00", 10<<20)})
	if es, err := listArchive(bomb); err == nil || !strings.Contains(err.Error(), "compress") {
		t.Errorf("a bomb listed: %v, %v", es, err)
	}
	// Subtitles do compress, and are capped at maxSideFile anyway.
	subs := filepath.Join(dir, "subs.zip")
	writeZip(t, subs, map[string]string{"x.srt": strings.Repeat("1\n00:00:01,000 --> 00:00:02,000\nHi\n\n", 10000)})
	if _, err := listArchive(subs); err != nil {
		t.Errorf("subtitles: %v", err)
	}
}

// Review M5: unrar allocates a RAR5 entry's whole dictionary (up to 4 GB,
// more in RAR7), so one bigger than the machine can spare isn't unpacked.
func TestUnrarDictionaryLimit(t *testing.T) {
	saved := unrarPath
	defer func() { unrarPath = saved }()
	unrarPath = "/usr/bin/unrar"
	e := &archive.Entry{Name: "x.mkv", Size: 1 << 30, Packed: 1 << 30, Dict: 4 << 30}
	if _, ok, err := pick("rar", e); ok || err == nil || !strings.Contains(err.Error(), "memory") {
		t.Errorf("a 4 GB dictionary: %v %v", ok, err)
	}
	e.Dict = 4 << 20
	if _, ok, err := pick("rar", e); !ok || err != nil {
		t.Errorf("a 4 MB dictionary: %v %v", ok, err)
	}
}

// An unrar that stops sending is stopped, rather than holding the import
// lock for good.
func TestUnrarStalls(t *testing.T) {
	savedPath, savedStall := unrarPath, unrarStall
	defer func() { unrarPath, unrarStall = savedPath, savedStall }()
	unrarPath = filepath.Join(t.TempDir(), "unrar")
	os.WriteFile(unrarPath, []byte("#!/bin/sh\nprintf abc\nexec sleep 30\n"), 0o755)
	unrarStall = 200 * time.Millisecond
	dir := t.TempDir()
	first := testutil.WriteRAR(t, dir, "ff", true, false, 1<<20, []testutil.ArchiveFile{
		{Name: "ff.mkv", Data: []byte("abcdef"), Method: 3},
	})
	list, err := listArchive(first)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	s := &Importer{}
	_, err = s.extract(first, list, make([]byte, 1024))
	if err == nil || time.Since(start) > 10*time.Second {
		t.Errorf("after %v: %v", time.Since(start), err)
	}
	testutil.MustNotExist(t, dir, "ff.mkv.mvdtmp")
}

// Review M6: a file already beside the archive, taken as one unpacked by a
// run cut short, isn't removed when the unpack fails: it may be the user's.
func TestFailedUnpackKeepsWhatItFound(t *testing.T) {
	dir := t.TempDir()
	movie := bytes.Repeat([]byte("m"), 1000)
	first := testutil.WriteRAR(t, dir, "set", false, false, 1<<20, []testutil.ArchiveFile{
		{Name: "movie.mkv", Data: movie},
		{Name: "other.mkv", Data: bytes.Repeat([]byte("o"), 1000)},
	})
	list, err := listArchive(first)
	if err != nil {
		t.Fatal(err)
	}
	// Damage the second entry's data, so its CRC fails.
	b, _ := os.ReadFile(first)
	b[bytes.LastIndex(b, []byte("ooo"))] ^= 0xff
	os.WriteFile(first, b, 0o644)
	// movie.mkv unpacked by hand, at the size the archive says.
	testutil.Write(t, filepath.Join(dir, "movie.mkv"), string(movie))
	s := &Importer{}
	if _, err := s.extract(first, list, make([]byte, 1024)); err == nil {
		t.Fatal("a damaged archive unpacked")
	}
	testutil.MustExist(t, dir, "movie.mkv")
	testutil.MustNotExist(t, dir, "other.mkv")
}

// unrar reads an argument starting with @ as a list file of names, perhaps
// even after --, so such an entry isn't handed to it.
func TestUnrarRefusesListFileNames(t *testing.T) {
	saved := unrarPath
	defer func() { unrarPath = saved }()
	unrarPath = "/usr/bin/unrar"
	e := &archive.Entry{Name: "@names.mkv", Size: 10, Packed: 10}
	if _, ok, err := pick("rar", e); ok || err == nil {
		t.Errorf("@names.mkv: %v %v", ok, err)
	}
}
