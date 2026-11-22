package library

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// unpackedFile, in an item, lists the archives already unpacked there and
// what came out of them, so a re-run (or a torrent still seeding) doesn't
// unpack them again.
const unpackedFile = ".mvd-unpacked"

// unpacked is what an item's unpackedFile says.
type unpacked struct {
	archives map[string]bool // first volumes, relative to the item
	files    map[string]bool // what came out, relative to the item
}

func readUnpacked(dir string) unpacked {
	u := unpacked{archives: map[string]bool{}, files: map[string]bool{}}
	f, err := os.Open(filepath.Join(dir, unpackedFile))
	if err != nil {
		return u
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		kind, p, ok := strings.Cut(sc.Text(), "\t")
		switch {
		case !ok:
		case kind == "archive":
			u.archives[p] = true
		case kind == "file":
			u.files[p] = true
		}
	}
	return u
}

// note adds an archive and what came out of it to the item's list.
func (u unpacked) note(item, arch string, files []string) error {
	f, err := os.OpenFile(filepath.Join(item, unpackedFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	rel := func(p string) string { r, _ := filepath.Rel(item, p); return filepath.ToSlash(r) }
	var b strings.Builder
	fmt.Fprintf(&b, "archive\t%s\n", rel(arch))
	u.archives[rel(arch)] = true
	for _, x := range files {
		fmt.Fprintf(&b, "file\t%s\n", rel(x))
		u.files[rel(x)] = true
	}
	_, err = f.WriteString(b.String())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// pending is the item's archives not yet unpacked.
func (u unpacked) pending(item string, firsts []string) []string {
	var out []string
	for _, a := range firsts {
		if r, _ := filepath.Rel(item, a); !u.archives[filepath.ToSlash(r)] {
			out = append(out, a)
		}
	}
	return out
}

// fromArchives says whether every video is one that came out of an archive.
func (u unpacked) fromArchives(item string, videos []string) bool {
	for _, v := range videos {
		if r, _ := filepath.Rel(item, v); !u.files[filepath.ToSlash(r)] {
			return false
		}
	}
	return true
}

// SizeStr is a size for people: "1.2 TB", "4.3 GB", "700 MB", "12 KB".
func SizeStr(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(n)/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}
