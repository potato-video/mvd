package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"slices"
	"sync"

	"mvd/internal/library"
	"mvd/internal/tmdb"
)

// The server's import: POST /api/import, and the web UI's Import now.

type importResult struct {
	Movies     int    `json:"movies"`
	Episodes   int    `json:"episodes"`
	Left       int    `json:"left"`
	Duplicates int    `json:"duplicates"`
	Waiting    int    `json:"waiting"`
	Seeding    int    `json:"seeding"`
	Failed     int    `json:"failed"`
	Output     string `json:"output"`

	stopped bool // it stopped short: TMDB out of reach, or the key refused
}

var importMu sync.Mutex

// imports is what the last import left, for the website: each item's
// result (Incoming's rows waiting for an import, and Needs you), why it
// stopped if it stopped short, and how many imports in a row each item has
// failed. In memory only: the next import rebuilds it.
type imports struct {
	mu      sync.Mutex
	results []library.ItemResult
	stopped string              // the last import stopped short: TMDB out of reach, or the key refused
	fails   map[string]failures // by item path
	after   importScan          // the import folder as the last import left it
	trash   int64               // the trash's size, as the last import left it...
	known   bool                // ...once one has looked
}

// failures counts the imports in a row an item has failed in (a file it
// couldn't move, a write error), while the item stays the same.
type failures struct {
	sig uint64
	n   int
}

// failLimit is how many failures in a row before an item waits for a
// person, rather than being tried again.
const failLimit = 3

// lastImport is the last import's results, and why it stopped short, if it did.
func (sv *Server) lastImport() ([]library.ItemResult, string) {
	sv.imports.mu.Lock()
	defer sv.imports.mu.Unlock()
	return sv.imports.results, sv.imports.stopped
}

// failCount is how many imports in a row have failed on an item.
func (sv *Server) failCount(path string) int {
	sv.imports.mu.Lock()
	defer sv.imports.mu.Unlock()
	return sv.imports.fails[path].n
}

// keep records an import's results (under the import's lock). An import
// that stopped short keeps the earlier results for the items it didn't
// get to, and for the one it stopped on, and counts no failures: the
// problem was everything's, not theirs.
func (im *imports) keep(rs []library.ItemResult, stopped error, after importScan) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.after = after
	if stopped != nil {
		im.stopped = stopped.Error()
		if len(rs) > 0 {
			rs = rs[:len(rs)-1]
		}
		seen := map[string]bool{}
		for _, r := range rs {
			seen[r.Path] = true
		}
		merged := slices.Clone(rs)
		for _, r := range im.results {
			if !seen[r.Path] && after.item(r.Path) != nil {
				merged = append(merged, r)
			}
		}
		im.results = merged
		return
	}
	im.stopped = ""
	im.results = rs
	fails := map[string]failures{}
	for _, r := range rs {
		if !hasFailed(r) {
			continue
		}
		var sig uint64
		if it := after.item(r.Path); it != nil {
			sig = it.Sig
		}
		f := im.fails[r.Path]
		if f.sig != sig {
			f = failures{sig: sig}
		}
		f.n++
		fails[r.Path] = f
	}
	im.fails = fails
}

// settle is what auto-import needs from the last import: the folder as it
// left it, and whether anything failed that's worth trying again (not yet
// failLimit times).
func (im *imports) settle() (uint64, bool) {
	im.mu.Lock()
	defer im.mu.Unlock()
	retry := false
	for _, r := range im.results {
		if hasFailed(r) && im.fails[r.Path].n < failLimit {
			retry = true
		}
	}
	return im.after.Sig, retry
}

// trashed records the trash's size: as an import found it (grew false),
// or grown by something put there since.
func (im *imports) trashed(n int64, grew bool) {
	im.mu.Lock()
	defer im.mu.Unlock()
	switch {
	case !grew:
		im.trash, im.known = n, n >= 0
	case im.known:
		im.trash += n
	}
}

// trashSize is how much is in the trash, if an import has looked.
func (sv *Server) trashSize() (int64, bool) {
	sv.imports.mu.Lock()
	defer sv.imports.mu.Unlock()
	return sv.imports.trash, sv.imports.known
}

// hasFailed: something in the item failed to file.
func hasFailed(r library.ItemResult) bool {
	for _, u := range r.Units {
		if u.State == library.UnitFailed {
			return true
		}
	}
	return false
}

// runImport runs an import with no questions asked: what mvd is sure of is
// filed, the rest stays put. One at a time.
// errBusy is an answer while an import is running.
var errBusy = errors.New("an import is running: try again in a moment")

// lockImports keeps imports out while the website changes the import
// folder or the trash: the server's own (importMu) and one at a terminal
// (import.lock). errBusy, or the lock's error, if one's running.
func (sv *Server) lockImports() (release func(), err error) {
	if !importMu.TryLock() {
		return nil, errBusy
	}
	unlock, err := library.LockImport(sv.cfg)
	if err != nil {
		importMu.Unlock()
		return nil, err
	}
	return func() { unlock(); importMu.Unlock() }, nil
}

func (sv *Server) runImport() (importResult, error) {
	cfg := sv.cfg
	switch {
	case cfg.ImportDir == "":
		return importResult{}, errors.New("no import folder: set video_dir or import_dir under [paths] in config.toml")
	}
	if err := library.CheckImport(cfg, cfg.ImportDir); err != nil {
		return importResult{}, err
	}
	if !importMu.TryLock() {
		return importResult{}, errors.New("an import is already running")
	}
	defer importMu.Unlock()
	release, err := library.LockImport(cfg) // and none from the command line either
	if err != nil {
		return importResult{}, err
	}
	defer release()

	os.MkdirAll(cfg.ImportDir, 0o777)
	var out bytes.Buffer
	mk := sv.newTMDB
	if mk == nil {
		mk = tmdb.New
	}
	s := library.NewImporter(library.Options{}, cfg, mk(cfg), cfg.ImportDir)
	s.SetOutput(&out)
	s.SetComplete(sv.ups.complete())
	s.SetDownloads(sv.importDownloads())
	if f := library.OpenImportLog(cfg); f != nil {
		defer f.Close()
		s.SetLog(f)
	}
	s.Run()
	sv.imports.keep(s.Results(), s.Stopped(), scanImport(cfg.ImportDir))
	sv.imports.trashed(s.TrashSize(), false)
	st := s.Stats()
	if st.Changed > 0 {
		sv.rescan()
	}
	if st.Movies+st.Episodes+st.Failed > 0 { // (auto-import runs often; most runs file nothing)
		log.Printf("mvd: import: %d movie(s), %d episode(s) filed", st.Movies, st.Episodes)
	}
	return importResult{Movies: st.Movies, Episodes: st.Episodes, Left: st.Left, Duplicates: st.Duplicates,
		Waiting: st.Waiting, Seeding: st.Seeding, Failed: st.Failed, Output: out.String(),
		stopped: s.Stopped() != nil}, nil
}

func (sv *Server) apiImport(w http.ResponseWriter, r *http.Request) {
	res, err := sv.runImport()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, res)
}
