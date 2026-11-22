package server

import (
	"os"
	"sync"
	"time"

	"mvd/internal/library"
)

// What every build keeps of uploads (the files feature has the rest,
// uploads.go): the finished ones an import needn't wait for.

const uploadMaxAge = 7 * 24 * time.Hour // an unfinished upload untouched this long goes

// uploads serialises the work on each upload (and its creation).
type uploads struct {
	mu       sync.Mutex
	busy     map[string]*busyLock  // held or waited for, by upload
	finished map[string]time.Time  // finished uploads, by path: whole, so an import needn't wait
	rates    map[string]rateSample // where each upload going was when first seen, for its speed (the website)
}

// busyLock is an upload's lock, and how many hold it or wait for it.
type busyLock struct {
	sync.Mutex
	n int
}

// rateSample is where an upload was when the server first saw it going.
type rateSample struct {
	offset int64
	at     time.Time
}

// complete is the finished uploads still in the import folder.
func (u *uploads) complete() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for p, when := range u.finished {
		if _, err := os.Stat(p); err != nil || time.Since(when) > uploadMaxAge {
			delete(u.finished, p) // imported, or moved by hand
			continue
		}
		out = append(out, p)
	}
	return out
}

func sizeStr(n int64) string { return library.SizeStr(n) }
