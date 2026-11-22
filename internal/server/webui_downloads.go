//go:build webui && downloads && files

package server

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"mvd/internal/library"
	"mvd/internal/torrent"
)

// Downloads on the web UI's Incoming page. (Built with both the web UI and
// torrents.) Each answers with the page's list, and a line saying what it did.

func (sv *Server) uiDownloadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /ui/downloads", sv.uiAddDownload)
	mux.HandleFunc("POST /ui/downloads/{id}/resume", sv.uiResume)
	mux.HandleFunc("POST /ui/downloads/{id}/pause", sv.uiPause)
	mux.HandleFunc("POST /ui/downloads/{id}/verify", sv.uiVerify)
	mux.HandleFunc("DELETE /ui/downloads/{id}", sv.uiCancelDownload)
	mux.HandleFunc("POST /ui/downloads/{id}/trash", sv.uiTrashDownload)
	mux.HandleFunc("POST /ui/downloads/{id}/move", sv.uiMoveDownload)
}

// uiAddDownload takes a magnet or .torrent link from the add bar, or a
// .torrent file dropped on the page.
func (sv *Server) uiAddDownload(w http.ResponseWriter, r *http.Request) {
	t, dup, warn, err := sv.addDownload(r)
	msg, bad := "", false
	switch {
	case sv.stillAdding(err):
		msg = err.Error() + "."
		err = nil // the field clears: it's in hand
	case err != nil:
		msg, bad = err.Error(), true
	case dup:
		msg = t.Name + " is already downloading."
	default:
		msg = "Added " + t.Name + "."
	}
	if warn != "" {
		msg += " Note: " + warn
	}
	if err == nil {
		w.Header().Set("HX-Trigger", "download-added") // the field clears itself
	}
	sv.renderIncoming(w, msg, bad)
}

func (sv *Server) uiResume(w http.ResponseWriter, r *http.Request) {
	n, err := sv.resumeDownloads(r.PathValue("id"))
	switch {
	case err != nil:
		sv.renderIncoming(w, err.Error(), true)
	case n > 1:
		sv.renderIncoming(w, fmt.Sprintf("Resumed %d downloads.", n), false)
	default:
		sv.renderIncoming(w, "", false)
	}
}

func (sv *Server) uiPause(w http.ResponseWriter, r *http.Request) {
	n, err := sv.pauseDownloads(r.PathValue("id"))
	switch {
	case err != nil:
		sv.renderIncoming(w, err.Error(), true)
	case n > 1:
		sv.renderIncoming(w, fmt.Sprintf("Paused %d downloads.", n), false)
	default:
		sv.renderIncoming(w, "", false)
	}
}

// uiVerify is a problem card's Verify: Transmission checks the data, then
// starts it again.
func (sv *Server) uiVerify(w http.ResponseWriter, r *http.Request) {
	if _, err := sv.verifyDownloads(r.PathValue("id")); err != nil {
		sv.renderIncoming(w, err.Error(), true)
		return
	}
	sv.renderIncoming(w, "Transmission is checking its files; it starts again once they're checked.", false)
}

// uiCancelDownload is ✕: an unfinished download goes, with what it had.
func (sv *Server) uiCancelDownload(w http.ResponseWriter, r *http.Request) {
	t, err := sv.removeDownload(r.PathValue("id"))
	switch {
	case err != nil:
		sv.renderIncoming(w, err.Error(), true)
	case t.PercentDone < 1:
		sv.renderIncoming(w, "Canceled "+t.Name+"; what it had downloaded is deleted.", false)
	default:
		sv.renderIncoming(w, "Removed "+t.Name+" from Transmission; its files stay.", false)
	}
}

// uiTrashDownload is 🗑: a finished download leaves Transmission, and its
// files go to the trash (if they're in the import folder: anything else
// isn't mvd's to move).
func (sv *Server) uiTrashDownload(w http.ResponseWriter, r *http.Request) {
	tr := sv.transmission()
	if tr == nil {
		sv.renderIncoming(w, "Transmission isn't set up", true)
		return
	}
	ts, err := tr.Torrents()
	if err != nil {
		sv.renderIncoming(w, err.Error(), true)
		return
	}
	var t *torrent.Torrent
	for i := range ts {
		if isDownload(ts[i], r.PathValue("id")) {
			t = &ts[i]
		}
	}
	switch {
	case t == nil:
		sv.renderIncoming(w, errNoDownload.Error(), true)
		return
	case t.PercentDone < 1:
		sv.renderIncoming(w, t.Name+" hasn't finished: cancel it instead.", true)
		return
	}
	if err := torrent.Discard(tr, []torrent.Torrent{*t}, true); err != nil {
		sv.renderIncoming(w, "Couldn't take it out of Transmission: "+err.Error(), true)
		return
	}
	path := filepath.Join(t.DownloadDir, t.Name)
	if sv.cfg.ImportDir == "" || !library.Within(sv.cfg.ImportDir, path) {
		sv.renderIncoming(w, "Removed "+t.Name+" from Transmission; its files stay where they are, outside the import folder.", false)
		return
	}
	_, deleted, err := sv.trashItem(sv.importRel(path))
	if err != nil {
		sv.renderIncoming(w, "Removed "+t.Name+" from Transmission, but couldn't move it to the trash: "+err.Error(), true)
		return
	}
	sv.renderIncoming(w, trashedText(t.Name, deleted), false)
}

// uiMoveDownload puts a download in the queue just before another (before),
// or, at the end, just after one (after): where it was dropped among the
// rows, whatever the queue positions between them.
func (sv *Server) uiMoveDownload(w http.ResponseWriter, r *http.Request) {
	err := sv.moveNear(r.PathValue("id"), r.FormValue("before"), r.FormValue("after"))
	if err != nil {
		sv.renderIncoming(w, "Couldn't move it: "+err.Error(), true)
		return
	}
	sv.renderIncoming(w, "", false)
}

func (sv *Server) moveNear(id, before, after string) error {
	tr := sv.transmission()
	if tr == nil {
		return errors.New("Transmission isn't set up")
	}
	ts, err := tr.Torrents()
	if err != nil {
		return err
	}
	find := func(id string) *torrent.Torrent {
		for i := range ts {
			if isDownload(ts[i], id) {
				return &ts[i]
			}
		}
		return nil
	}
	t := find(id)
	if t == nil {
		return errNoDownload
	}
	// Moving to another's position shifts those between along by one.
	var pos int
	switch {
	case find(before) != nil:
		pos = find(before).QueuePosition
		if t.QueuePosition < pos {
			pos--
		}
	case find(after) != nil:
		pos = find(after).QueuePosition
		if t.QueuePosition > pos {
			pos++
		}
	default:
		return errors.New("no such place in the queue")
	}
	if pos == t.QueuePosition {
		return nil
	}
	return tr.MoveTo(t.ID, pos)
}

// --- Incoming ---

// downloadFacts are a download's details (i), in two columns as the
// design has them: the swarm (seeds, leechers, ratio), then this download
// (added, speed, content). Speed is down while it downloads, up once it
// seeds, and a dash with nothing moving; Transmission before 4.0 doesn't
// say how many files.
func downloadFacts(t downloadView) [2][]fact {
	var swarm []fact
	byLabel := map[string]string{}
	for _, f := range t.Facts() {
		byLabel[f.Label] = f.Value
	}
	for _, l := range []string{"Seeds", "Leechers", "Ratio"} {
		if v, ok := byLabel[l]; ok {
			swarm = append(swarm, fact{l, v})
		}
	}
	var mine []fact
	if t.Added > 0 {
		mine = append(mine, fact{"Added", ago(t.AddedAt())})
	}
	speed := "—"
	switch {
	case t.PercentDone < 1 && t.RateDown > 0:
		speed = rateWords(float64(t.RateDown))
	case t.PercentDone >= 1 && t.RateUp > 0:
		speed = rateWords(float64(t.RateUp)) + " up"
	}
	mine = append(mine, fact{"Speed", speed})
	content := sizeStr(t.Size)
	switch {
	case t.FileCount == 1:
		content += " in 1 file"
	case t.FileCount > 1:
		content += fmt.Sprintf(" in %d files", t.FileCount)
	}
	if t.Size > 0 {
		mine = append(mine, fact{"Content", content})
	}
	return [2][]fact{swarm, mine}
}

// incomingTorrents adds Transmission's downloads to Incoming: the queue
// (in its order, draggable), then those seeding, then those done and
// waiting for an import; one Transmission reports a problem with, or that
// an import couldn't place, goes to Needs you. It says which import items
// are downloads, so they aren't listed twice.
func (sv *Server) incomingTorrents(v *incomingView, results map[string]*library.ItemResult, sc importScan) map[string]bool {
	d := sv.downloads()
	v.Torrents = d.Configured
	if d.Error != "" {
		v.Banners = append(v.Banners, "Transmission can't be reached: "+d.Error)
		return nil
	}
	claimed := map[string]bool{}
	var queue, seeding, done []incomingRow
	for _, t := range d.Torrents {
		path := filepath.Join(t.DownloadDir, t.Name)
		if t.InImport {
			claimed[path] = true
		}
		key := "dl-" + strconv.Itoa(t.ID)
		item := ""
		if t.InImport {
			item = sv.importRel(path)
		}
		if t.Error != "" {
			v.Needs = append(v.Needs, needItem{Key: "need-" + key + "-" + shortHash(t.Error), Item: item, Name: t.Name,
				Size: t.Size, Torrent: downloadKey(t.Torrent), Problem: t.Error})
			continue
		}
		if r := results[path]; r != nil && t.PercentDone >= 1 && needsPerson(*r, sv.failCount(path)) {
			if it := sc.item(path); it != nil {
				l := sv.leftItemOf(*it, *r, sv.failCount(path))
				v.Needs = append(v.Needs, needItem{Key: "need-" + shortHash(item+"\x00"+l.Sig), Item: item, Name: t.Name,
					Size: t.Size, Torrent: downloadKey(t.Torrent), Left: &l, Seeding: !t.Ready && t.Active})
				continue
			}
		}
		row := incomingRow{Key: key, Name: t.Name, Torrent: downloadKey(t.Torrent), Item: item, Pct: -1}
		row.Facts = downloadFacts(t)
		var dest *[]incomingRow
		line := []string{}
		if t.Size > 0 {
			line = append(line, sizeOf(int64(t.PercentDone*float64(t.Size)), t.Size)+" via Transmission")
		} else {
			line = append(line, "via Transmission")
		}
		switch {
		case t.PercentDone < 1:
			row.Pct, row.Idle, row.Cancel = int(t.PercentDone*100), t.Status != torrent.Downloading, true
			row.Pause, row.Resume = t.Active, t.Paused
			switch {
			case t.Paused:
				line = append(line, "paused")
				v.Paused++
			case t.Status == torrent.DownloadWait:
				line = append(line, "waiting in queue")
				v.Going++
			case t.Status == torrent.CheckWait, t.Status == torrent.Checking:
				line = append(line, "checking its files")
				v.Going++
			case t.RateDown > 0:
				if t.ETA > 0 {
					line = append(line, remaining(float64(t.ETA)))
				}
				v.Going++
			default:
				line = append(line, "looking for peers")
				v.Going++
			}
			dest = &queue
		case !t.Ready:
			_, why := t.Torrent.Ready(sv.cfg.SeedRatio)
			row.Trash = true
			line = append(line, why)
			if t.Paused {
				v.Paused++
			} else if t.Active {
				v.Going++
			}
			dest = &seeding
		default:
			row.Trash = true
			line = append(line, "waiting for import")
			v.ImportNow = v.ImportNow || !sv.cfg.AutoImport
			dest = &done
		}
		if !t.InImport {
			line = append(line, "outside the import folder")
		}
		row.Line = strings.Join(line, " · ")
		*dest = append(*dest, row)
	}
	if len(queue) > 1 {
		for i := range queue {
			queue[i].Drag = true
		}
	}
	v.Rows = append(append(append(v.Rows, queue...), seeding...), done...)
	return claimed
}
