//go:build webui && files

package server

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"
	"time"

	"mvd/internal/library"
)

// The website's Incoming page: everything on its way into the library, in
// one list, whatever brought it (an upload, Transmission, a copy into the
// import folder), and what can't go further without a person (Needs you).
// The server renders the lot and the page polls it; incoming.js overlays its
// own uploads' finer progress.

// incomingView is the page's polled part.
type incomingView struct {
	Rows    []incomingRow
	Needs   []needItem
	Banners []string // about everything, not one item: Transmission out of reach, TMDB refusing the key

	Uploads    bool // uploads are on: the page takes drops
	Torrents   bool // Transmission is set up: the page takes links
	AutoImport bool
	ImportNow  bool // auto-import is off and something's waiting for an import

	Going, Paused int    // Transmission's downloads that Pause all or Resume all would change
	Free, Trash   string // the head: "4.1 TB free", and the trash's size

	Message    string // what a button just did, above the list
	MessageBad bool
}

// incomingRow is one thing on its way in.
type incomingRow struct {
	Key  string // the element's id, unique on the page
	Name string
	Line string // the facts: "1.2 GB of 4.1 GB from another computer · 30 minutes remaining"
	Pct  int    // the bar, 0 to 100; -1 for none
	Idle bool   // the bar is waiting (paused, queued), not moving

	Upload  string // an upload's id, or a folder's group: incoming.js finds its own by it, and ✕ cancels it
	Stopped bool   // an upload that isn't going: nothing's arrived for a while
	Torrent string // a download's hash (downloadKey)
	Item    string // an item in the import folder, as a path in it ("movies/Heat (1995)")

	Drag          bool // a download in the queue: dragging reorders it
	Pause, Resume bool
	Cancel        bool      // ✕: stop it and throw away what's arrived
	Trash         bool      // 🗑: it's finished: to the trash (and out of Transmission)
	Facts         [2][]fact // a download's details (i), in two columns
}

// HasFacts: it has details to show (a download).
func (r incomingRow) HasFacts() bool { return len(r.Facts[0])+len(r.Facts[1]) > 0 }

// fact is a labeled value in a download's details (ⓘ).
type fact struct{ Label, Value string }

// needItem is something in the import folder that won't go any further
// without a person: an import left it, or it's failed too often, or
// Transmission reports a problem with it.
type needItem struct {
	Key     string
	Item    string // the item, as a path in the import folder ("" for a download outside it)
	Name    string
	Size    int64
	Left    *leftItem // what an import left (needs.go)
	Torrent string    // a download: its hash (downloadKey)
	Problem string    // Transmission's message about it
	Seeding bool      // a download still seeding
}

// Line is what's wrong, under the card's name.
func (n needItem) Line() string {
	if n.Problem != "" {
		return "Transmission says: " + n.Problem
	}
	if n.Left != nil {
		return n.Left.Why
	}
	return ""
}

// nearlyFull is free space low enough to say so above the list.
const nearlyFull = 1 << 30

// incoming builds the page's polled part.
func (sv *Server) incoming() incomingView {
	cfg := sv.cfg
	v := incomingView{Uploads: cfg.Uploads && cfg.ImportDir != "", AutoImport: cfg.AutoImport}
	if cfg.ImportDir != "" {
		if !library.Exists(cfg.ImportDir) && !library.Exists(filepath.Dir(cfg.ImportDir)) { // (not just not made yet)
			v.Banners = append(v.Banners, "The import folder isn't there ("+cfg.ImportDir+"): is the drive plugged in?")
		} else if n, ok := sv.freeSpace(); ok { // the library's drive, as /api/status says
			v.Free = sizeStr(n) + " free"
			if n < nearlyFull {
				v.Banners = append(v.Banners, "The drive is nearly full: "+sizeStr(n)+" free.")
			}
		}
	}
	if n, ok := sv.trashSize(); ok && n > 0 {
		v.Trash = sizeStr(n)
	}
	if v.Uploads {
		v.Rows = sv.incomingUploads()
	}
	results, stopped := sv.lastImport()
	if stopped != "" {
		v.Banners = append(v.Banners, "The last import stopped: "+stopped+".")
	}
	byPath := make(map[string]*library.ItemResult, len(results))
	for i := range results {
		byPath[results[i].Path] = &results[i]
	}
	var sc importScan
	if cfg.ImportDir != "" {
		sc = scanImport(cfg.ImportDir)
	}
	claimed := sv.incomingTorrents(&v, byPath, sc)
	sv.incomingItems(&v, sc, byPath, claimed)
	return v
}

// --- uploads ---

const uploadStopped = 30 * time.Second // nothing's arrived for this long: it isn't going

// incomingUploads is the unfinished uploads: a folder as one row. The
// newest first; incoming.js moves its own after the rest.
func (sv *Server) incomingUploads() []incomingRow {
	now := sv.clock()
	var rows []incomingRow
	groups := map[string]int{} // a folder's row, by group
	ups := sv.listUploads()
	sv.ups.keepRates(ups)
	for _, u := range ups {
		going := now.Sub(u.Updated) < uploadStopped
		if u.Group == "" {
			r := incomingRow{Key: "up-" + u.ID, Name: u.Name, Upload: u.ID, Cancel: true, Stopped: !going,
				Pct: pctOf(u.Offset, u.Size)}
			r.Line = sizeOf(u.Offset, u.Size) + " from another computer"
			r.Line += sv.ups.rateLine(u.ID, u.Offset, u.Size, going, u.Updated, now)
			rows = append(rows, r)
			continue
		}
		i, ok := groups[u.Group]
		if !ok {
			name, _, _ := strings.Cut(u.Name, "/")
			rows = append(rows, incomingRow{Key: "up-" + u.Group, Name: name, Upload: u.Group, Cancel: true,
				Stopped: !going, Pct: -1})
			i = len(rows) - 1
			groups[u.Group] = i
		}
		r := &rows[i]
		files := fmt.Sprintf("%d of %d files from another computer", u.GroupDone, u.GroupFiles)
		if u.GroupFiles == 1 {
			files = "1 file from another computer"
		}
		if u.ID == "" { // the folder, with no file going
			r.Line = files + " · stopped " + agoAt(u.Updated, now)
			r.Pct = pctOf(int64(u.GroupDone), int64(u.GroupFiles))
			continue
		}
		r.Stopped = r.Stopped && !going
		r.Line = files + sv.ups.rateLine(u.ID, u.Offset, u.Size, going, u.Updated, now)
		if u.GroupFiles > 0 {
			r.Pct = min(100, int((float64(u.GroupDone)+float64(u.Offset)/max(float64(u.Size), 1))*100/float64(u.GroupFiles)))
		}
	}
	return rows
}

// keepRates forgets the speed samples of uploads no longer listed: finished
// (perhaps while going, so never seen stopped), canceled or swept.
func (u *uploads) keepRates(listed []uploadView) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.rates) == 0 {
		return
	}
	live := make(map[string]bool, len(listed))
	for _, v := range listed {
		live[v.ID] = true
	}
	for id := range u.rates {
		if !live[id] {
			delete(u.rates, id)
		}
	}
}

// rateLine is the rest of an upload's line: how long to go (at the speed
// it's gone since the server first saw it going), or when it stopped.
func (u *uploads) rateLine(id string, offset, size int64, going bool, updated, now time.Time) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.rates == nil {
		u.rates = map[string]rateSample{}
	}
	s, seen := u.rates[id]
	if !going {
		delete(u.rates, id)
		return " · stopped " + agoAt(updated, now)
	}
	if !seen || offset < s.offset {
		u.rates[id] = rateSample{offset, now}
		return " · starting"
	}
	secs := now.Sub(s.at).Seconds()
	if secs < 2 || offset == s.offset {
		return " · starting"
	}
	rate := float64(offset-s.offset) / secs
	return " · " + remaining(float64(size-offset)/rate)
}

// --- the import folder ---

// incomingItems adds what's in the import folder: waiting for an import
// (Incoming), or for a person (Needs you). Downloads (claimed) are
// Transmission's rows.
func (sv *Server) incomingItems(v *incomingView, sc importScan, results map[string]*library.ItemResult, claimed map[string]bool) {
	now := sv.clock()
	whole := map[string]bool{} // finished uploads: an import takes them without waiting
	for _, p := range sv.ups.complete() {
		whole[p] = true
	}
	for _, it := range sc.Items {
		if claimed[it.Path] {
			continue
		}
		rel := sv.importRel(it.Path)
		key := "in-" + shortHash(rel)
		r := results[it.Path]
		fails := 0
		if r != nil && hasFailed(*r) {
			fails = sv.failCount(it.Path)
		}
		if r != nil && needsPerson(*r, fails) {
			l := sv.leftItemOf(it, *r, fails)
			v.Needs = append(v.Needs, needItem{Key: "need-" + shortHash(rel+"\x00"+l.Sig), Item: rel, Name: it.Name,
				Size: it.Size, Left: &l})
			continue
		}
		row := incomingRow{Key: key, Name: it.Name, Item: rel, Pct: -1, Trash: true}
		line := []string{}
		if it.Size > 0 {
			line = append(line, sizeStr(it.Size))
		}
		switch {
		case !whole[it.Path] && (now.Sub(it.Newest) < time.Minute || r != nil && r.State == library.ItemWaiting):
			line = append(line, "still arriving")
		case fails > 0:
			line = append(line, "waiting for import", "last try failed ("+failedErr(*r)+")")
			if sv.cfg.AutoImport {
				line = append(line, "trying again in 10 minutes")
			}
			v.ImportNow = !sv.cfg.AutoImport
		default:
			line = append(line, "waiting for import")
			v.ImportNow = !sv.cfg.AutoImport
		}
		row.Line = strings.Join(line, " · ")
		v.Rows = append(v.Rows, row)
	}
}

func shortHash(s string) string {
	h := fnv.New64a()
	h.Write([]byte(s))
	return fmt.Sprintf("%016x", h.Sum64())
}

// --- words ---

func pctOf(n, of int64) int {
	if of <= 0 {
		return 0
	}
	return max(0, min(100, int(n*100/of)))
}

// sizeOf is how much of something has arrived: "1.2 GB of 4.1 GB", or just
// its size before anything has.
func sizeOf(done, size int64) string {
	if done <= 0 || done >= size {
		return sizeStr(size)
	}
	return sizeStr(done) + " of " + sizeStr(size)
}

// rateWords is a speed: "2.1 MB/sec", "18 kB/sec".
func rateWords(bps float64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/sec", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0f kB/sec", bps/(1<<10))
	}
	return fmt.Sprintf("%.0f bytes/sec", bps)
}

// remaining is how long to go: "23 seconds remaining", "12 minutes
// remaining", "1 day remaining".
func remaining(sec float64) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " remaining"
		}
		return fmt.Sprintf("%d %ss remaining", n, unit)
	}
	switch {
	case sec < 90:
		return plural(max(1, int(sec+0.5)), "second")
	case sec < 90*60:
		return plural(int(sec/60+0.5), "minute")
	case sec < 36*3600:
		return plural(int(sec/3600+0.5), "hour")
	}
	return plural(int(sec/86400+0.5), "day")
}
