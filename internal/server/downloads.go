//go:build downloads

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mvd/internal/library"
	"mvd/internal/torrent"
)

// The server side of downloads and importing: the API, used by the web UI's
// Incoming page and by anything else.

type downloadView struct {
	torrent.Torrent
	State    string `json:"state"`     // in words: "Downloading, 43%, 12 min left"
	Details  string `json:"details"`   // in words: "4.2 GB · ratio 0.12 · seeds 3 of 40 · leechers 1 of 12"
	Ready    bool   `json:"ready"`     // finished and seeded: the next import takes it
	Paused   bool   `json:"paused"`    // stopped for no good reason: can be resumed
	Active   bool   `json:"active"`    // going (downloading, seeding, queued): can be paused
	InImport bool   `json:"in_import"` // downloading into the import folder
}

type downloadsView struct {
	Configured bool           `json:"configured"`
	Error      string         `json:"error,omitempty"`
	SeedRatio  float64        `json:"seed_ratio"`
	Torrents   []downloadView `json:"torrents"`
}

// downloadsState is the server's Transmission client, made on first use.
type downloadsState struct {
	once sync.Once
	c    *torrent.Client
}

func (sv *Server) transmission() *torrent.Client {
	sv.dl.once.Do(func() {
		if sv.cfg.Transmission != "" {
			sv.dl.c = torrent.New(sv.cfg)
		}
	})
	return sv.dl.c
}

// importDownloads is what Transmission says about the import folder, for
// an import, through the server's own client.
func (sv *Server) importDownloads() library.Downloads {
	return library.TransmissionDownloads(sv.transmission(), sv.cfg, sv.cfg.ImportDir)
}

// hasDownloads: this build can download (the web UI shows Downloads).
func (sv *Server) hasDownloads() bool { return true }

func (sv *Server) downloadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/downloads", sv.apiDownloads)
	mux.HandleFunc("POST /api/downloads", sv.apiAddDownload)
	mux.HandleFunc("POST /api/downloads/{id}/resume", sv.apiResume)
	mux.HandleFunc("POST /api/downloads/{id}/pause", sv.apiPause)
	mux.HandleFunc("POST /api/downloads/{id}/verify", sv.apiVerify)
	mux.HandleFunc("DELETE /api/downloads/{id}", sv.apiRemoveDownload)
	mux.HandleFunc("POST /api/downloads/{id}/move", sv.apiMoveDownload)
	sv.uiDownloadRoutes(mux)
}

// Facts are what the page folds away: size, ratio, peers.
func (d downloadView) Facts() []torrent.Fact { return torrent.Facts(d.Torrent) }

// AddedAt is when it was added to Transmission.
func (d downloadView) AddedAt() time.Time { return time.Unix(d.Added, 0) }

func (sv *Server) downloads() downloadsView {
	v := downloadsView{Configured: sv.cfg.Transmission != "", SeedRatio: sv.cfg.SeedRatio, Torrents: []downloadView{}}
	tr := sv.transmission()
	if tr == nil {
		return v
	}
	ts, err := tr.Torrents()
	if err != nil {
		v.Error = err.Error()
		return v
	}
	torrent.InQueueOrder(ts)
	for _, t := range ts {
		state, ready := torrent.State(t, sv.cfg.SeedRatio)
		in := sv.cfg.ImportDir != "" && library.Within(sv.cfg.ImportDir, t.DownloadDir)
		v.Torrents = append(v.Torrents, downloadView{t, state, torrent.Details(t), ready && in, t.Paused(), t.Active(), in})
	}
	return v
}

func (sv *Server) apiDownloads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, sv.downloads())
}

// apiAddDownload takes {"source": "magnet:...", "kind": "movie"|"tv"|""} as
// JSON, or a form with source, kind and a .torrent file as "torrent".
func (sv *Server) apiAddDownload(w http.ResponseWriter, r *http.Request) {
	t, dup, warn, err := sv.addDownload(r)
	if sv.stillAdding(err) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"pending": true, "note": err.Error()})
		return
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"torrent": t, "duplicate": dup, "note": warn})
}

func (sv *Server) addDownload(r *http.Request) (torrent.Torrent, bool, string, error) {
	tr := sv.transmission()
	if tr == nil {
		return torrent.Torrent{}, false, "", errors.New("Transmission isn't set up: add [downloads] transmission = ... to config.toml")
	}
	var source, kind string
	var file []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body struct{ Source, Kind string }
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(&body); err != nil {
			return torrent.Torrent{}, false, "", errors.New("bad JSON")
		}
		source, kind = body.Source, body.Kind
	} else {
		r.Body = http.MaxBytesReader(nil, r.Body, 8<<20)
		if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
			return torrent.Torrent{}, false, "", errors.New("that upload didn't come through")
		}
		source, kind = r.FormValue("source"), r.FormValue("kind")
		if f, hdr, err := r.FormFile("torrent"); err == nil {
			defer f.Close()
			b, err := io.ReadAll(f)
			if err != nil || len(b) == 0 {
				return torrent.Torrent{}, false, "", errors.New("couldn't read " + hdr.Filename)
			}
			if !bytes.HasPrefix(b, []byte("d")) {
				return torrent.Torrent{}, false, "", errors.New(hdr.Filename + " isn't a .torrent file")
			}
			file = b
		}
	}
	if kind != "movie" && kind != "tv" {
		kind = ""
	}
	if file == nil && strings.TrimSpace(source) == "" {
		return torrent.Torrent{}, false, "", errors.New("paste a magnet link or a link to a .torrent, or choose a .torrent file")
	}
	return torrent.Add(sv.cfg, tr, source, file, kind)
}

// stillAdding: when Transmission was too slow to answer an add (it has
// usually done it), finish the job (start it, set its ratio) once it shows
// up, in the background.
func (sv *Server) stillAdding(err error) bool {
	var still *torrent.StillAdding
	if !errors.As(err, &still) {
		return false
	}
	go func() {
		if t, err := still.Finish(); err != nil {
			log.Printf("mvd: add: %v", err)
		} else {
			log.Printf("mvd: add: %s added (Transmission was slow to answer)", t.Name)
		}
	}()
	return true
}

// resumeDownloads starts a paused download again, or every paused one for
// "all".
func (sv *Server) resumeDownloads(which string) (int, error) {
	tr := sv.transmission()
	if tr == nil {
		return 0, errors.New("Transmission isn't set up")
	}
	ts, err := tr.Torrents()
	if err != nil {
		return 0, err
	}
	var args []string
	if which != "all" {
		args = []string{which}
	}
	picked, err := torrent.Pick(ts, args, torrent.Torrent.Paused)
	if err != nil {
		return 0, err
	}
	return len(picked), tr.Resume(picked)
}

func (sv *Server) apiResume(w http.ResponseWriter, r *http.Request) {
	n, err := sv.resumeDownloads(r.PathValue("id"))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]int{"resumed": n})
}

// pauseDownloads pauses a download (or its seeding), or every one going for
// "all".
func (sv *Server) pauseDownloads(which string) (int, error) {
	tr := sv.transmission()
	if tr == nil {
		return 0, errors.New("Transmission isn't set up")
	}
	ts, err := tr.Torrents()
	if err != nil {
		return 0, err
	}
	var args []string
	if which != "all" {
		args = []string{which}
	}
	picked, err := torrent.Pick(ts, args, torrent.Torrent.Active)
	if err != nil {
		return 0, err
	}
	return len(picked), tr.Stop(picked)
}

func (sv *Server) apiPause(w http.ResponseWriter, r *http.Request) {
	n, err := sv.pauseDownloads(r.PathValue("id"))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]int{"paused": n})
}

// verifyDownloads has Transmission check a download's data on disk (one it
// reports a problem with, or every such for "all"), then start it again:
// mvd torrent verify.
func (sv *Server) verifyDownloads(which string) (int, error) {
	tr := sv.transmission()
	if tr == nil {
		return 0, errors.New("Transmission isn't set up")
	}
	ts, err := tr.Torrents()
	if err != nil {
		return 0, err
	}
	var args []string
	if which != "all" {
		args = []string{which}
	}
	picked, err := torrent.Pick(ts, args, torrent.Torrent.Failed)
	if err != nil {
		return 0, err
	}
	return len(picked), tr.Verify(picked)
}

func (sv *Server) apiVerify(w http.ResponseWriter, r *http.Request) {
	n, err := sv.verifyDownloads(r.PathValue("id"))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]int{"verifying": n})
}

// isDownload says whether id names t: its hash, which stays the same, or
// its number, which Transmission gives afresh each time it starts (so only
// for lists fetched since).
func isDownload(t torrent.Torrent, id string) bool {
	return (t.Hash != "" && strings.EqualFold(t.Hash, id)) || strconv.Itoa(t.ID) == id
}

// downloadKey is what the website names a download by: its hash.
func downloadKey(t torrent.Torrent) string {
	if t.Hash != "" {
		return t.Hash
	}
	return strconv.Itoa(t.ID)
}

// errNoDownload: no torrent has that ID.
var errNoDownload = errors.New("no such download (gone already?)")

// removeDownload takes one download out of Transmission, by its ID alone.
// Unfinished, its partial data goes too; finished, its files stay for
// import.
func (sv *Server) removeDownload(id string) (torrent.Torrent, error) {
	tr := sv.transmission()
	if tr == nil {
		return torrent.Torrent{}, errors.New("Transmission isn't set up")
	}
	ts, err := tr.Torrents()
	if err != nil {
		return torrent.Torrent{}, err
	}
	for _, t := range ts {
		if isDownload(t, id) {
			return t, torrent.Discard(tr, []torrent.Torrent{t}, false)
		}
	}
	return torrent.Torrent{}, errNoDownload
}

func (sv *Server) apiRemoveDownload(w http.ResponseWriter, r *http.Request) {
	t, err := sv.removeDownload(r.PathValue("id"))
	if err != nil {
		code := http.StatusBadRequest
		if err == errNoDownload {
			code = http.StatusNotFound
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"name": t.Name, "deleted_data": t.PercentDone < 1})
}

// moveDownload puts a download at a place in the list, as the list orders
// them (queue order), from 0: so at that place in Transmission's queue.
// Past the end is the end.
func (sv *Server) moveDownload(id string, place int) error {
	tr := sv.transmission()
	if tr == nil {
		return errors.New("Transmission isn't set up")
	}
	if place < 0 {
		return errors.New("no such place in the list")
	}
	ts, err := tr.Torrents()
	if err != nil {
		return err
	}
	torrent.InQueueOrder(ts)
	for _, t := range ts {
		if isDownload(t, id) {
			// The queue position of whatever is at that place now (the
			// positions needn't run 0, 1, 2... with no gaps).
			return tr.MoveTo(t.ID, ts[min(place, len(ts)-1)].QueuePosition)
		}
	}
	return errNoDownload
}

func (sv *Server) apiMoveDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Position *int `json:"position"`
	}
	err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<10)).Decode(&body)
	if err == nil && body.Position == nil {
		err = errors.New("no position")
	}
	if err == nil {
		err = sv.moveDownload(r.PathValue("id"), *body.Position)
	}
	if err != nil {
		code := http.StatusBadRequest
		if err == errNoDownload {
			code = http.StatusNotFound
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, sv.downloads())
}
