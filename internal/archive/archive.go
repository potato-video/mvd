// Package archive reads the archives that releases and subtitles come in:
// .zip (through archive/zip) and .rar, with a reader of our own for RAR4 and
// RAR5 that handles only stored (-m0) data, which is how most video releases
// are packed. It lists entries and streams their data; deciding what to
// keep, and where to put it, is the caller's job.
package archive

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Entry is one file (or folder, or link) in an archive.
type Entry struct {
	Name      string // as stored, with "/" between folders
	Size      int64  // the size it declares, which may lie
	Packed    int64  // its data's size in the archive, or -1 if not known (split across volumes)
	Dict      int64  // compressed RAR5 data: the dictionary unpacking it needs in memory (else 0)
	Dir       bool
	Link      bool // a symlink or hard link: never extracted
	Encrypted bool
	Stored    bool // false for compressed RAR data, which this package can't read
}

var (
	// ErrCompressed is what reading a compressed RAR entry gives.
	ErrCompressed = errors.New("compressed, and mvd only reads stored (-m0) RAR archives")
	// ErrChecksum means the data doesn't match its CRC.
	ErrChecksum = errors.New("checksum mismatch: the archive is damaged")
	// ErrEncrypted means the archive, or its headers, need a password.
	ErrEncrypted = errors.New("encrypted")
	// ErrTooBig means the archive lists more than it's safe to read.
	ErrTooBig = errors.New("too many files listed in it to read")
)

// Walk calls fn for each entry of the archive at path (for a RAR set, its
// first volume). r reads the entry's data: across volumes, and checked
// against its CRC at the end. For a folder, a link, or compressed RAR data,
// reading gives an error. fn needn't read all of r; Walk skips the rest.
func Walk(path string, fn func(e *Entry, r io.Reader) error) error {
	switch Kind(path) {
	case "zip":
		return walkZip(path, fn)
	case "rar":
		return walkRAR(path, fn)
	}
	return fmt.Errorf("%s: not a .zip or .rar", filepath.Base(path))
}

var (
	partRe = regexp.MustCompile(`(?i)^(.*\.part)(\d+)(\.rar)$`)
	rNNRe  = regexp.MustCompile(`(?i)^(.*\.)([r-z])(\d\d)$`)
)

// Kind says what an archive is by its name: "zip", "rar" (any volume of a
// set), or "".
func Kind(name string) string {
	low := strings.ToLower(filepath.Base(name))
	switch {
	case strings.HasSuffix(low, ".zip"):
		return "zip"
	case strings.HasSuffix(low, ".rar"), rNNRe.MatchString(low):
		return "rar"
	}
	return ""
}

// First says whether name is an archive to start from: a .zip, a .rar, or
// the first of a .partN.rar set. (Further volumes, .r00 and .part2.rar, are
// read along with their first.)
func First(name string) bool {
	base := filepath.Base(name)
	switch Kind(base) {
	case "zip":
		return true
	case "rar":
		if m := partRe.FindStringSubmatch(base); m != nil {
			n, _ := strconv.Atoi(m[2])
			return n == 1
		}
		return strings.HasSuffix(strings.ToLower(base), ".rar")
	}
	return false
}

// FirstVolume is the first volume of the set a volume belongs to, by name:
// "a.part1.rar" for "a.part3.rar", "a.rar" for "a.r04". Zips and first
// volumes are their own.
func FirstVolume(name string) string {
	dir, base := filepath.Split(name)
	if m := partRe.FindStringSubmatch(base); m != nil {
		return dir + m[1] + fmt.Sprintf("%0*d", len(m[2]), 1) + m[3]
	}
	if m := rNNRe.FindStringSubmatch(base); m != nil {
		r := "rar"
		if m[2] == strings.ToUpper(m[2]) {
			r = "RAR"
		}
		return dir + m[1] + r
	}
	return name
}

// nextVolume names the volume after this one: a.part01.rar → a.part02.rar;
// a.rar → a.r00 → a.r01 … a.r99 → a.s00.
func nextVolume(name string) string {
	dir, base := filepath.Split(name)
	if m := partRe.FindStringSubmatch(base); m != nil {
		n, _ := strconv.Atoi(m[2])
		return dir + m[1] + fmt.Sprintf("%0*d", len(m[2]), n+1) + m[3]
	}
	if m := rNNRe.FindStringSubmatch(base); m != nil {
		n, _ := strconv.Atoi(m[3])
		letter := m[2][0]
		if n == 99 {
			n, letter = -1, letter+1
		}
		return dir + m[1] + string(letter) + fmt.Sprintf("%02d", n+1)
	}
	if ext := filepath.Ext(base); strings.EqualFold(ext, ".rar") {
		r := "r"
		if ext == ".RAR" {
			r = "R"
		}
		return dir + strings.TrimSuffix(base, ext) + "." + r + "00"
	}
	return ""
}

// Volumes lists the files of the set that start at first, as far as they
// exist on disk: just the file itself, for a zip or a lone .rar.
func Volumes(first string) []string {
	out := []string{first}
	if Kind(first) != "rar" {
		return out
	}
	for p := nextVolume(first); p != ""; p = nextVolume(p) {
		if _, err := os.Stat(p); err != nil {
			break
		}
		out = append(out, p)
	}
	return out
}

// errReader is the reader for an entry that can't be read.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

var errNoData = errors.New("a folder or a link: no data to read")
