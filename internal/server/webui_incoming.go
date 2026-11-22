//go:build webui && files

package server

import (
	"net/http"
	"sort"
	"strings"

	"mvd/internal/library"
)

// The web UI's Incoming page (incoming.go builds its list): the add bar,
// the list and Needs you, polled as one. The uploading itself is
// static/incoming.js, against /api/uploads.

// incomingBuilt: the nav has Incoming.
const incomingBuilt = true

func init() { pageTemplates = append(pageTemplates, "incoming") }

func (sv *Server) uiIncomingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /incoming", sv.uiIncoming)
	mux.HandleFunc("GET /ui/incoming", sv.uiIncomingList)
	mux.HandleFunc("POST /ui/incoming/trash", sv.uiTrashItem)
	mux.HandleFunc("POST /ui/incoming/empty-trash", sv.uiEmptyTrash)
	mux.HandleFunc("POST /ui/import", sv.uiImport)
	mux.HandleFunc("DELETE /ui/uploads/{id}", sv.uiCancelUpload)
	sv.uiNeedsRoutes(mux)
}

func uploadAccept() string {
	exts := []string{".mkv", ".mp4", ".m4v", ".avi", ".mov", ".wmv", ".mpg", ".mpeg", ".ts", ".m2ts",
		".webm", ".ogv", ".divx", ".vob", ".iso"}
	for e := range library.UploadExts {
		exts = append(exts, e)
	}
	exts = append(exts, library.ArchiveExts()...)
	sort.Strings(exts)
	return strings.Join(exts, ",")
}

// incomingPage is the whole page: the polled part, and what's around it.
type incomingPage struct {
	incomingView
	Accept      string // what the file picker offers
	TorrentsOff bool   // built with downloads, but Transmission isn't set up
}

func (sv *Server) uiIncoming(w http.ResponseWriter, r *http.Request) {
	v := sv.incoming()
	d := incomingPage{incomingView: v, Accept: uploadAccept(), TorrentsOff: sv.hasDownloads() && !v.Torrents}
	sv.render(w, r, "incoming", page{Title: "Incoming", Nav: "incoming", Data: d, Needs: len(v.Needs)})
}

// renderIncoming answers with the polled part, and a line above it.
// renderIncoming answers a button: the page's polled part, looked at
// afresh, with what happened.
func (sv *Server) renderIncoming(w http.ResponseWriter, msg string, bad bool) {
	v := sv.incoming()
	sv.polled.keep(v, sv.clock())
	v.Message, v.MessageBad = msg, bad
	sv.partial(w, "incoming", "poll", v)
}

func (sv *Server) uiIncomingList(w http.ResponseWriter, r *http.Request) {
	p := &sv.polled
	p.mu.Lock()
	now := sv.clock()
	v, ok := p.v.(incomingView)
	if !ok || now.Sub(p.at) >= sv.pollShare || now.Before(p.at) {
		v = sv.incoming()
		p.v, p.at = v, now
	}
	p.mu.Unlock()
	sv.partial(w, "incoming", "poll", v)
}

func (sv *Server) uiCancelUpload(w http.ResponseWriter, r *http.Request) {
	if id := r.PathValue("id"); validUploadID(id) && sv.cfg.Uploads {
		if !sv.cancelGroup(id) {
			defer sv.ups.lock(id)()
			sv.removeUpload(id)
		}
	}
	sv.renderIncoming(w, "", false)
}

func (sv *Server) uiTrashItem(w http.ResponseWriter, r *http.Request) {
	name, deleted, err := sv.trashItem(r.FormValue("item"))
	if err != nil {
		sv.renderIncoming(w, "Couldn't move it to the trash: "+err.Error(), true)
		return
	}
	sv.renderIncoming(w, trashedText(name, deleted), false)
}

// uiEmptyTrash is the trash dialog's button: everything mvd put in the
// trash goes now. Not while an import is running, which may be adding to it.
func (sv *Server) uiEmptyTrash(w http.ResponseWriter, r *http.Request) {
	release, err := sv.lockImports()
	if err != nil {
		sv.renderIncoming(w, "Couldn't empty the trash: "+err.Error(), true)
		return
	}
	size, known := sv.trashSize()
	library.EmptyTrash(sv.cfg)
	sv.imports.trashed(0, false)
	release()
	w.Header().Set("HX-Trigger", "trash-emptied")
	msg := "Emptied the trash."
	if known && size > 0 {
		msg = "Emptied the trash: " + sizeStr(size) + " deleted."
	}
	sv.renderIncoming(w, msg, false)
}

func (sv *Server) uiImport(w http.ResponseWriter, r *http.Request) {
	res, err := sv.runImport()
	if err != nil {
		sv.renderIncoming(w, err.Error(), true)
		return
	}
	sv.renderIncoming(w, importSummary(res), false)
}

// needsCount is how many items the last import left for a person, for
// the nav: from memory, so cheap enough for every page. (Incoming's own
// count adds Transmission's problems.)
func (sv *Server) needsCount() int {
	results, _ := sv.lastImport()
	n := 0
	for _, r := range results {
		if needsPerson(r, sv.failCount(r.Path)) {
			if library.Exists(r.Path) {
				n++
			}
		}
	}
	return n
}
