package archive

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxZipList is the most of a zip read while opening it: the central
// directory, which archive/zip reads whole into memory at a few hundred
// bytes a file, and finding where it starts. A 1 MB directory is about
// 20,000 files; a release has a handful.
const maxZipList = 1 << 20

func walkZip(path string, fn func(*Entry, io.Reader) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	ra := &cappedReaderAt{r: f, left: maxZipList + 70<<10}
	z, err := zip.NewReader(ra, st.Size())
	if ra.over {
		return fmt.Errorf("%s: %w", filepath.Base(path), ErrTooBig)
	}
	if err != nil {
		return err
	}
	ra.left = -1 // the entries' data, as much as they have
	for _, f := range z.File {
		mode := f.Mode()
		e := &Entry{
			Name:      f.Name,
			Size:      int64(f.UncompressedSize64),
			Packed:    int64(f.CompressedSize64),
			Dir:       mode.IsDir() || strings.HasSuffix(f.Name, "/"),
			Link:      mode&fs.ModeSymlink != 0,
			Encrypted: f.Flags&1 != 0,
			Stored:    f.Method == zip.Store,
		}
		if e.Dir || e.Link || e.Encrypted {
			why := ErrEncrypted
			if !e.Encrypted {
				why = errNoData
			}
			if err := fn(e, errReader{why}); err != nil {
				return err
			}
			continue
		}
		// archive/zip checks the CRC at the end, and refuses to read past
		// the declared size.
		rc, err := f.Open()
		if err != nil {
			if err := fn(e, errReader{err}); err != nil {
				return err
			}
			continue
		}
		err = fn(e, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// cappedReaderAt reads at most left bytes in all (-1: no limit), then fails.
type cappedReaderAt struct {
	r    io.ReaderAt
	left int64
	over bool
}

var errZipCap = errors.New("read cap reached")

func (c *cappedReaderAt) ReadAt(b []byte, off int64) (int, error) {
	if c.left >= 0 {
		if int64(len(b)) > c.left {
			c.over = true
			return 0, errZipCap
		}
		c.left -= int64(len(b))
	}
	return c.r.ReadAt(b, off)
}
