package server

import (
	"fmt"
	"hash"
	"hash/fnv"
	"io/fs"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Auto-import (auto_import = true): the server runs an import by itself
// when there's something new in the import folder (a finished upload or
// download, or a copy), so nobody has to press Import now.
//
// It looks every minute, and at once when an upload finishes. It only
// imports when the folder has changed since the last import, or when that
// import left something waiting (still arriving, or come in while it ran:
// a minute; still seeding, or failed: ten), so an idle folder, or one holding only things that need a person
// ("Needs you"), doesn't import over and over.

const (
	autoEvery        = time.Minute
	autoRetryWaiting = time.Minute      // something still arriving, or come in mid-import
	autoRetrySeeding = 10 * time.Minute // a finished download still seeding
)

type autoImport struct {
	mu      sync.Mutex
	kick    chan struct{}
	lastSig uint64    // the import folder as the last import left it
	retryAt time.Time // when to try again although it hasn't changed (zero: don't)
	lastErr string
}

// startAutoImport runs auto-import until stop closes, if it's on.
func (sv *Server) startAutoImport(stop <-chan struct{}) {
	if !sv.cfg.AutoImport || sv.cfg.ImportDir == "" {
		return
	}
	sv.auto.kick = make(chan struct{}, 1)
	log.Printf("mvd: auto-import is on: %s", sv.cfg.ImportDir)
	go func() {
		t := time.NewTicker(autoEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			case <-sv.auto.kick:
			}
			sv.autoImportOnce()
		}
	}()
}

// kickAutoImport asks for a look now (an upload has finished).
func (sv *Server) kickAutoImport() {
	if sv.auto.kick == nil {
		return
	}
	select {
	case sv.auto.kick <- struct{}{}:
	default:
	}
}

// autoImportOnce imports if there's reason to, and says whether it did.
func (sv *Server) autoImportOnce() bool {
	if !sv.cfg.AutoImport || sv.cfg.ImportDir == "" {
		return false
	}
	a := &sv.auto
	a.mu.Lock()
	defer a.mu.Unlock()
	sc := scanImport(sv.cfg.ImportDir)
	sig := sc.Sig
	now := sv.clock()
	if sc.Files == 0 {
		a.lastSig, a.retryAt = sig, time.Time{}
		return false
	}
	if sig == a.lastSig && (a.retryAt.IsZero() || now.Before(a.retryAt)) {
		return false
	}
	res, err := sv.runImport()
	if err != nil {
		// Tried again next time round (the folder's signature isn't kept).
		// Said once, not every minute.
		if msg := err.Error(); msg != a.lastErr && !strings.Contains(msg, "already running") {
			log.Printf("mvd: auto-import: %v", err)
			a.lastErr = msg
		}
		return true
	}
	a.lastErr = ""
	sig, retryFailed := sv.imports.settle()
	a.lastSig = sig
	switch {
	case res.Waiting > 0, sv.imports.arrivedDuring(sc):
		a.retryAt = now.Add(autoRetryWaiting)
	case res.Seeding > 0, retryFailed, res.stopped: // (a failure may be TMDB out of reach for a while)
		a.retryAt = now.Add(autoRetrySeeding)
	default:
		a.retryAt = time.Time{}
	}
	return true
}

// importScan is the import folder as one walk found it: a signature of the
// whole (what's in it, how big, when changed), skipping what the import
// skips, and each item in it as the import sees them (each entry, and each
// in movies/ and tv/). Auto-import compares the signature; the website
// lists the items.
type importScan struct {
	Sig   uint64
	Files int
	Items []scannedItem
}

// scannedItem is one item in the import folder.
type scannedItem struct {
	Path   string
	Name   string
	Kind   string // "movie" or "tv" in import/movies or import/tv, else ""
	Dir    bool
	Size   int64
	Files  int
	Newest time.Time // its latest change
	Sig    uint64    // its own signature: a change to it starts its failure count again
}

// item finds an item by its path.
func (sc importScan) item(path string) *scannedItem {
	for i := range sc.Items {
		if sc.Items[i].Path == path {
			return &sc.Items[i]
		}
	}
	return nil
}

func scanImport(dir string) importScan {
	dir = filepath.Clean(dir) // as WalkDir gives the paths under it
	var sc importScan
	type itemHint struct {
		path string
		line []byte
	}
	var hints []itemHint
	all := fnv.New64a()
	var cur *scannedItem
	var one hash.Hash64
	done := func() {
		if cur != nil {
			cur.Sig = one.Sum64()
		}
	}
	var line []byte
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		name := d.Name()
		// Hidden, as the import skips, but a folder's .mvd hint counts:
		// writing one is a change worth an import.
		if (name[0] == '.' && name != ".mvd") || name[0] == '_' || name[0] == '@' || strings.HasSuffix(name, ".mvdtmp") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel := p[len(dir)+1:]
		top, rest, nested := strings.Cut(rel, string(filepath.Separator))
		kind := ""
		switch strings.ToLower(top) {
		case "movies":
			kind = "movie"
		case "tv":
			kind = "tv"
		}
		key := top
		if kind != "" {
			if !nested { // the movies or tv folder itself
				if d.IsDir() {
					return nil
				}
				kind = ""
			} else {
				second, _, _ := strings.Cut(rest, string(filepath.Separator))
				key = top + string(filepath.Separator) + second
			}
		}
		if !d.IsDir() && key == rel || kind != "" && !d.IsDir() && rest == name {
			if strings.HasSuffix(strings.ToLower(name), ".mvd") { // a file's hint: part of the whole, not an item
				if info, err := d.Info(); err == nil {
					line = fmt.Appendf(line[:0], "%s\x00%d\x00%d\n", p, info.Size(), info.ModTime().UnixNano())
					all.Write(line)
					hints = append(hints, itemHint{path: p, line: append([]byte(nil), line...)})
				}
				return nil
			}
		}
		if cur == nil || cur.Path != filepath.Join(dir, key) {
			done()
			sc.Items = append(sc.Items, scannedItem{Path: filepath.Join(dir, key), Name: filepath.Base(key),
				Kind: kind, Dir: d.IsDir()})
			cur = &sc.Items[len(sc.Items)-1]
			one = fnv.New64a()
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		sc.Files++
		cur.Files++
		cur.Size += info.Size()
		if t := info.ModTime(); t.After(cur.Newest) {
			cur.Newest = t
		}
		line = fmt.Appendf(line[:0], "%s\x00%d\x00%d\n", p, info.Size(), info.ModTime().UnixNano())
		all.Write(line)
		one.Write(line)
		return nil
	})
	done()
	for _, hint := range hints {
		name := filepath.Base(hint.path)
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		for i := range sc.Items {
			it := &sc.Items[i]
			if it.Dir || filepath.Dir(it.Path) != filepath.Dir(hint.path) ||
				strings.TrimSuffix(filepath.Base(it.Path), filepath.Ext(it.Path)) != stem {
				continue
			}
			h := fnv.New64a()
			line = fmt.Appendf(line[:0], "%d\n", it.Sig)
			h.Write(line)
			h.Write(hint.line)
			it.Sig = h.Sum64()
		}
	}
	sc.Sig = all.Sum64()
	return sc
}
