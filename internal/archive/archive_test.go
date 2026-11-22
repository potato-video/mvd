package archive

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mvd/internal/testutil"
)

// The reader was checked against archives made by WinRAR (rarfile's test
// files). These tests write their own, so they can damage them, lose a
// volume, and so on.

type got struct {
	name string
	data string
	err  error
	e    Entry
}

func walkAll(t *testing.T, path string) ([]got, error) {
	t.Helper()
	var out []got
	buf := make([]byte, 7) // small, so reads cross parts
	err := Walk(path, func(e *Entry, r io.Reader) error {
		var b bytes.Buffer
		_, err := io.CopyBuffer(&b, r, buf)
		out = append(out, got{e.Name, b.String(), err, *e})
		return nil
	})
	return out, err
}

func bytesOf(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i*7)
	}
	return b
}

func TestRARSets(t *testing.T) {
	big, small := bytesOf(1000, 1), []byte("subtitles")
	files := []testutil.ArchiveFile{{Name: "Movie/movie.mkv", Data: big}, {Name: "movie.srt", Data: small}}
	for _, c := range []struct {
		name          string
		v5, old       bool
		perVol, nVols int
	}{
		{"rar4 single", false, false, 1 << 20, 1},
		{"rar5 single", true, false, 1 << 20, 1},
		{"rar4 part sets", false, false, 300, 4},
		{"rar4 .r00 sets", false, true, 300, 4},
		{"rar5 part sets", true, false, 300, 4},
		{"rar5 volume boundary between files", true, false, 1000, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			first := testutil.WriteRAR(t, dir, "Movie", c.v5, c.old, c.perVol, files)
			if !First(first) {
				t.Errorf("%s isn't taken as a first volume", first)
			}
			if vols := Volumes(first); len(vols) != c.nVols {
				t.Errorf("volumes %v, want %d", vols, c.nVols)
			}
			gs, err := walkAll(t, first)
			if err != nil {
				t.Fatal(err)
			}
			if len(gs) != 2 || gs[0].name != "Movie/movie.mkv" || gs[0].data != string(big) || gs[0].err != nil ||
				gs[1].name != "movie.srt" || gs[1].data != string(small) || gs[1].err != nil {
				t.Errorf("got %+v", gs)
			}
			if gs[0].e.Size != 1000 || !gs[0].e.Stored {
				t.Errorf("entry %+v", gs[0].e)
			}
			// Listing alone (no reading) skips through the volumes too.
			var names []string
			if err := Walk(first, func(e *Entry, r io.Reader) error { names = append(names, e.Name); return nil }); err != nil || len(names) != 2 {
				t.Errorf("listing: %v %v", names, err)
			}
		})
	}
}

func TestRARDamage(t *testing.T) {
	data := bytesOf(1000, 3)
	files := []testutil.ArchiveFile{{Name: "a.mkv", Data: data}}
	for _, v5 := range []bool{false, true} {
		t.Run(fmt.Sprint("v5=", v5), func(t *testing.T) {
			// A byte changed in the middle volume: that part's CRC fails.
			dir := t.TempDir()
			first := testutil.WriteRAR(t, dir, "a", v5, false, 400, files)
			p2 := filepath.Join(dir, "a.part02.rar")
			b, _ := os.ReadFile(p2)
			b[len(b)-40] ^= 0xff
			os.WriteFile(p2, b, 0o644)
			gs, err := walkAll(t, first)
			if err != nil || len(gs) != 1 || !errors.Is(gs[0].err, ErrChecksum) {
				t.Errorf("damaged middle: %v %+v", err, gs)
			}

			// In the last volume: the whole file's CRC fails.
			dir = t.TempDir()
			first = testutil.WriteRAR(t, dir, "a", v5, false, 400, files)
			p3 := filepath.Join(dir, "a.part03.rar")
			b, _ = os.ReadFile(p3)
			b[len(b)-30] ^= 0xff
			os.WriteFile(p3, b, 0o644)
			if gs, _ := walkAll(t, first); len(gs) != 1 || !errors.Is(gs[0].err, ErrChecksum) {
				t.Errorf("damaged last: %+v", gs)
			}

			// A volume missing.
			dir = t.TempDir()
			first = testutil.WriteRAR(t, dir, "a", v5, false, 400, files)
			os.Remove(filepath.Join(dir, "a.part02.rar"))
			if gs, _ := walkAll(t, first); len(gs) != 1 || gs[0].err == nil || !strings.Contains(gs[0].err.Error(), "missing") {
				t.Errorf("missing volume: %+v", gs)
			}

			// Cut short.
			dir = t.TempDir()
			first = testutil.WriteRAR(t, dir, "a", v5, false, 1<<20, files)
			b, _ = os.ReadFile(first)
			os.WriteFile(first, b[:len(b)-500], 0o644)
			if gs, _ := walkAll(t, first); len(gs) != 1 || gs[0].err == nil {
				t.Errorf("cut short: %+v", gs)
			}

			// A damaged file header.
			dir = t.TempDir()
			first = testutil.WriteRAR(t, dir, "a", v5, false, 1<<20, files)
			b, _ = os.ReadFile(first)
			b[len(sigRAR5)+30] ^= 0xff
			os.WriteFile(first, b, 0o644)
			if _, err := walkAll(t, first); err == nil {
				t.Error("damaged header not noticed")
			}
		})
	}
}

func TestRARCompressedAndLinks(t *testing.T) {
	for _, v5 := range []bool{false, true} {
		dir := t.TempDir()
		first := testutil.WriteRAR(t, dir, "c", v5, false, 1<<20, []testutil.ArchiveFile{
			{Name: "packed.mkv", Data: []byte("not really compressed"), Method: 3},
			{Name: "link.mkv", Data: []byte("/etc/passwd"), Link: true},
			{Name: "after.srt", Data: []byte("still readable")},
		})
		gs, err := walkAll(t, first)
		if err != nil || len(gs) != 3 {
			t.Fatalf("v5=%v: %v %+v", v5, err, gs)
		}
		if gs[0].e.Stored || !errors.Is(gs[0].err, ErrCompressed) {
			t.Errorf("v5=%v compressed: %+v", v5, gs[0])
		}
		if !gs[1].e.Link || gs[1].err == nil || gs[1].data != "" {
			t.Errorf("v5=%v link: %+v", v5, gs[1])
		}
		if gs[2].data != "still readable" || gs[2].err != nil {
			t.Errorf("v5=%v after: %+v", v5, gs[2])
		}
	}
}

func TestZip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "subs.zip")
	f, _ := os.Create(p)
	z := zip.NewWriter(f)
	w, _ := z.Create("Subs/English.srt")
	w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"))
	z.Create("Subs/")
	h := &zip.FileHeader{Name: "evil"}
	h.SetMode(os.ModeSymlink | 0o777)
	w, _ = z.CreateHeader(h)
	w.Write([]byte("../../etc"))
	z.Close()
	f.Close()

	gs, err := walkAll(t, p)
	if err != nil || len(gs) != 3 {
		t.Fatalf("%v %+v", err, gs)
	}
	if gs[0].name != "Subs/English.srt" || !strings.Contains(gs[0].data, "Hello") || gs[0].err != nil {
		t.Errorf("srt: %+v", gs[0])
	}
	if !gs[1].e.Dir || !gs[2].e.Link || gs[2].data != "" {
		t.Errorf("dir and link: %+v %+v", gs[1], gs[2])
	}
}

func TestVolumeNames(t *testing.T) {
	for in, want := range map[string]string{
		"a.part01.rar": "a.part02.rar", "a.part9.rar": "a.part10.rar", "a.rar": "a.r00",
		"a.r00": "a.r01", "a.r99": "a.s00", "A.RAR": "A.R00",
	} {
		if got := nextVolume(in); got != want {
			t.Errorf("nextVolume(%s) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"x/a.part03.rar": "x/a.part01.rar", "a.r04": "a.rar", "a.rar": "a.rar", "a.zip": "a.zip", "A.R01": "A.RAR",
	} {
		if got := FirstVolume(in); got != want {
			t.Errorf("FirstVolume(%s) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]bool{
		"a.rar": true, "a.part1.rar": true, "a.part001.rar": true, "a.part2.rar": false,
		"a.r00": false, "a.zip": true, "a.7z": false, "a.001": false,
	} {
		if First(in) != want {
			t.Errorf("First(%s) = %v", in, !want)
		}
	}
}

// block4 is a RAR4 block with its header CRC.
func block4(typ byte, flags uint16, body []byte) []byte {
	le := binary.LittleEndian
	h := []byte{typ}
	h = le.AppendUint16(h, flags)
	h = le.AppendUint16(h, uint16(7+len(body)))
	h = append(h, body...)
	return append(le.AppendUint16(nil, uint16(crc32.ChecksumIEEE(h))), h...)
}

// Review H2: a RAR4 header whose 64-bit packed size is negative moved the
// walk back (or nowhere), so it read the same header forever.
func TestRARNegativeSizes(t *testing.T) {
	le := binary.LittleEndian
	// A large file header (or service header) whose data is -back bytes long.
	header := func(typ byte, back int64) []byte {
		name := "a.mkv"
		size := int64(7 + 25 + 8 + len(name))
		packed := uint64(-(size + back))
		var h []byte
		h = le.AppendUint32(h, uint32(packed))
		h = le.AppendUint32(h, 1000)
		h = append(h, 3)
		h = le.AppendUint32(h, 0)
		h = le.AppendUint32(h, 0)
		h = append(h, 29, 0x30)
		h = le.AppendUint16(h, uint16(len(name)))
		h = le.AppendUint32(h, 0o100644)
		h = le.AppendUint32(h, uint32(packed>>32))
		h = le.AppendUint32(h, 0)
		h = append(h, name...)
		return block4(typ, 0x8100, h)
	}
	for _, c := range []struct {
		name string
		typ  byte
		back int64
	}{
		{"file, same header", 0x74, 0},
		{"file, back to the main header", 0x74, 13},
		{"service, same header", 0x7a, 0},
		{"service, back to the main header", 0x7a, 13},
	} {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			b.WriteString("Rar!\x1a\x07\x00")
			b.Write(block4(0x73, 0, make([]byte, 6)))
			b.Write(header(c.typ, c.back))
			b.Write(block4(0x7b, 0, nil))
			path := filepath.Join(t.TempDir(), "a.rar")
			os.WriteFile(path, b.Bytes(), 0o644)
			calls := 0
			done := make(chan error, 1)
			go func() {
				done <- Walk(path, func(e *Entry, r io.Reader) error {
					if calls++; calls > 100 {
						return errors.New("looping")
					}
					return nil
				})
			}()
			select {
			case err := <-done:
				if err == nil || calls > 1 {
					t.Errorf("walked %d entries, error %v", calls, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("still walking after 5 seconds")
			}
		})
	}
}

// Review M5: archive/zip reads a zip's whole central directory into memory,
// a few hundred bytes a file, so a small zip of very many entries could
// take more than the router has. One whose directory is too big is refused.
func TestZipTooManyEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "many.zip")
	f, _ := os.Create(path)
	z := zip.NewWriter(f)
	for i := 0; i < 50000; i++ {
		z.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("d/%06d.txt", i), Method: zip.Store})
	}
	z.Close()
	f.Close()
	n := 0
	err := Walk(path, func(*Entry, io.Reader) error { n++; return nil })
	if !errors.Is(err, ErrTooBig) || n != 0 {
		t.Errorf("walked %d entries, error %v", n, err)
	}
}

// A RAR5 entry that doesn't know its size is reported as such (Size -1),
// not as damage.
func TestRAR5UnknownSize(t *testing.T) {
	files := []testutil.ArchiveFile{{Name: "a.mkv", Data: []byte("data"), UnknownSize: true}}
	first := testutil.WriteRAR(t, t.TempDir(), "a", true, false, 1<<20, files)
	var sizes []int64
	err := Walk(first, func(e *Entry, r io.Reader) error { sizes = append(sizes, e.Size); return nil })
	if err != nil || len(sizes) != 1 || sizes[0] != -1 {
		t.Errorf("sizes %v, error %v", sizes, err)
	}
}

// A compressed RAR5 entry says how much memory unpacking it takes: its
// dictionary, 128 KB << n.
func TestRAR5Dictionary(t *testing.T) {
	files := []testutil.ArchiveFile{{Name: "a.mkv", Data: []byte("data"), Method: 3, Dict: 13}}
	first := testutil.WriteRAR(t, t.TempDir(), "a", true, false, 1<<20, files)
	var dicts []int64
	Walk(first, func(e *Entry, r io.Reader) error { dicts = append(dicts, e.Dict); return nil })
	if len(dicts) != 1 || dicts[0] != 1<<30 {
		t.Errorf("dictionaries %v", dicts)
	}
}
