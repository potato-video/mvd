package library

import (
	"os"
	"path/filepath"
)

// syncFile is (*os.File).Sync, swapped out by tests.
var syncFile = (*os.File).Sync

// WriteFileAtomic writes b to path whole or not at all: to a temporary file
// beside it, synced, then renamed over it, and the folder synced so the
// rename sticks. Without the syncs, a power cut (the router's kind of
// failure) can leave path empty or partial.
func WriteFileAtomic(path string, b []byte, perm os.FileMode) error {
	tmp := path + ".mvdtmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = syncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir syncs a folder, so a rename into it survives a power cut. Best
// effort: not every system or filesystem can.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		syncFile(d)
		d.Close()
	}
}
