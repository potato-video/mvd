//go:build linux

package library

import (
	"os"
	"syscall"
	"time"
)

// changedAt is when a file last changed in any way: the later of its mtime
// and ctime. A Finder copy sets the mtime back to the original's when it
// finishes, but the ctime says it just happened.
func changedAt(fi os.FileInfo) time.Time {
	t := fi.ModTime()
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if c := time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)); c.After(t) {
			t = c
		}
	}
	return t
}
