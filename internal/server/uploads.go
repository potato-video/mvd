//go:build files

package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mvd/internal/library"
)

// Uploads: a file sent from the browser into the import folder, in pieces,
// so that a dropped connection or a sleeping laptop loses a piece rather
// than the lot. (Copying multi-GB files with the Finder over SMB to the
// router's drive often fails half way, and starts again from nothing.)
//
//	POST   /api/uploads       {name, size, kind, fingerprint}: start, or
//	                          pick up an unfinished one of the same file
//	GET    /api/uploads       the unfinished ones
//	GET    /api/uploads/{id}  one, with how much has arrived
//	PATCH  /api/uploads/{id}  a piece: the body, written at Upload-Offset
//	DELETE /api/uploads/{id}  give up on it
//
// An upload lives in the import folder's .uploads (which the import never
// looks in) as <id>.part, the data, and <id>.json, what it is. The data's
// size on disk is the offset: nothing else to keep in step. By default
// pieces only ever add to the end, never set the size up front: exFAT can't
// make sparse files, so that would make the router write the whole size in
// zeros first. ([server] upload_preallocate = true sets it up front, which on a
// drive that can do it quickly reserves the space and keeps the file in one
// piece; then the offset is kept in the record instead.) When the last byte
// is in, the file moves to the import folder (movies/ or tv/ when the kind
// is given), where an import takes it.
//
// The body is read as it comes, straight to the file through a small
// buffer ([server] upload_buffer, 256KB): never a multipart form, which Go would
// spill to /tmp, which on OpenWRT is RAM.
//
// [server] uploads = false turns it all off: the API answers 404.

const maxPiece = 64 << 20 // largest piece accepted

const uploadsBuilt = true

type uploadMeta struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	Kind        string    `json:"kind,omitempty"` // "movie", "tv" or "" (work it out)
	Fingerprint string    `json:"fingerprint,omitempty"`
	Started     time.Time `json:"started"`
	// A folder goes as a group: each file's name is its path in the folder
	// ("Severance S01/Subs/E01.en.srt"), and the folder moves into the
	// import folder once all group_files of it are in (uploads_folder.go).
	Group      string `json:"group,omitempty"`
	GroupFiles int    `json:"group_files,omitempty"`
}

// uploadRecord is what <id>.json holds: what the upload is, and, when its
// file was given its full size up front, how much has been written.
type uploadRecord struct {
	uploadMeta
	Preallocated bool  `json:"preallocated,omitempty"`
	Written      int64 `json:"written,omitempty"`
}

type uploadView struct {
	uploadMeta
	Offset  int64     `json:"offset"`  // bytes safely on disk
	Updated time.Time `json:"updated"` // when a piece last arrived
	Done    bool      `json:"done"`    // all there, and moved to the import folder (or, in a group, put by)
	Path    string    `json:"path,omitempty"`
	// In a group: how many of its files are in so far.
	GroupDone int `json:"group_done,omitempty"`
	// Started again after it had finished: nothing to send.
	Already bool `json:"already,omitempty"`
}

// lock takes an upload's lock (by id; "" is the start lock), and returns
// what releases it. The lock is forgotten once nobody holds or wants it,
// so requests for made-up ids don't build up.
func (u *uploads) lock(id string) func() {
	u.mu.Lock()
	if u.busy == nil {
		u.busy = map[string]*busyLock{}
	}
	m := u.busy[id]
	if m == nil {
		m = &busyLock{}
		u.busy[id] = m
	}
	m.n++
	u.mu.Unlock()
	m.Lock()
	return func() {
		m.Unlock()
		u.mu.Lock()
		if m.n--; m.n == 0 {
			delete(u.busy, id)
		}
		u.mu.Unlock()
	}
}

func (sv *Server) uploadRoutes(mux *http.ServeMux) {
	on := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !sv.cfg.Uploads {
				uploadError(w, http.StatusNotFound, "uploads are turned off ([server] uploads = false)")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/uploads", on(sv.apiUploads))
	mux.HandleFunc("POST /api/uploads", on(sv.apiStartUpload))
	mux.HandleFunc("GET /api/uploads/{id}", on(sv.apiUpload))
	mux.HandleFunc("PATCH /api/uploads/{id}", on(sv.apiUploadPiece))
	mux.HandleFunc("DELETE /api/uploads/{id}", on(sv.apiCancelUpload))
}

func (sv *Server) uploadDir() string { return filepath.Join(sv.cfg.ImportDir, ".uploads") }

func validUploadID(id string) bool {
	if len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// readUpload is an unfinished upload as it stands, or an error if there's no
// such upload.
func (sv *Server) readUpload(id string) (uploadView, error) {
	v, _, err := sv.readRecord(id)
	return v, err
}

func (sv *Server) readRecord(id string) (uploadView, uploadRecord, error) {
	var rec uploadRecord
	if !validUploadID(id) || sv.cfg.ImportDir == "" {
		return uploadView{}, rec, os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(sv.uploadDir(), id+".json"))
	if err != nil {
		return uploadView{}, rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return uploadView{}, rec, err
	}
	st, err := os.Stat(filepath.Join(sv.uploadDir(), id+".part"))
	if err != nil {
		return uploadView{}, rec, err
	}
	v := uploadView{uploadMeta: rec.uploadMeta, Offset: st.Size(), Updated: st.ModTime()}
	if rec.Preallocated {
		v.Offset = rec.Written
	}
	return v, rec, nil
}

// writeRecord saves an upload's record, whole or not at all.
func (sv *Server) writeRecord(rec uploadRecord) error {
	j, _ := json.Marshal(rec)
	return library.WriteFileAtomic(filepath.Join(sv.uploadDir(), rec.ID+".json"), j, 0o666)
}

// listUploads is every unfinished upload, newest first, leaving out those
// untouched for uploadMaxAge. It only reads: it runs on every poll, and
// one that deleted could take the .part of an upload being started before
// its record is written. sweepUploads clears up.
func (sv *Server) listUploads() []uploadView {
	out := []uploadView{}
	if sv.cfg.ImportDir == "" {
		return out
	}
	ents, _ := os.ReadDir(sv.uploadDir())
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		v, err := sv.readUpload(id)
		if err != nil || time.Since(v.Updated) > uploadMaxAge {
			continue
		}
		out = append(out, v)
	}
	out = sv.addGroups(out)
	sortUploads(out)
	return out
}

// sweepUploads removes what listUploads leaves out: uploads and folders
// untouched for uploadMaxAge, finished ones forgotten after it, and data
// whose record was never written. It holds the start lock, so no upload is
// part way through starting.
func (sv *Server) sweepUploads() {
	if sv.cfg.ImportDir == "" {
		return
	}
	defer sv.ups.lock("")()
	ents, _ := os.ReadDir(sv.uploadDir())
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".part"); ok {
			if _, err := os.Stat(filepath.Join(sv.uploadDir(), id+".json")); errors.Is(err, os.ErrNotExist) {
				os.Remove(filepath.Join(sv.uploadDir(), e.Name())) // its record never written
			}
			continue
		}
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		if v, err := sv.readUpload(id); err == nil && time.Since(v.Updated) > uploadMaxAge {
			unlock := sv.ups.lock(id)
			sv.removeUpload(id)
			unlock()
		}
	}
	sv.sweepGroups()
	sv.sweepDone()
}

// sweepUploadsEvery runs sweepUploads now and every d until stop closes.
func (sv *Server) sweepUploadsEvery(d time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		sv.sweepUploads()
		select {
		case <-t.C:
		case <-stop:
			return
		}
	}
}

func sortUploads(vs []uploadView) {
	for i := 1; i < len(vs); i++ {
		for j := i; j > 0 && vs[j].Started.After(vs[j-1].Started); j-- {
			vs[j], vs[j-1] = vs[j-1], vs[j]
		}
	}
}

// uploadsGoing is how many uploads are under way, for /api/status.
func (sv *Server) uploadsGoing() int {
	n := 0
	if sv.cfg.Uploads {
		for _, u := range sv.listUploads() {
			if !u.Done {
				n++
			}
		}
	}
	return n
}

func (sv *Server) removeUpload(id string) {
	os.Remove(filepath.Join(sv.uploadDir(), id+".part"))
	os.Remove(filepath.Join(sv.uploadDir(), id+".json"))
}

// maxNameBytes is the longest file or folder name an upload may have: a
// file system's limit is 255 bytes (ext4) or characters (exFAT), less room
// for the " (2)" a name already taken gets.
const maxNameBytes = 240

// cleanUploadName is a name safe to file: no folders, not hidden, and a
// video, subtitles, an .nfo, or (with the unpack feature) a .zip or .rar.
func cleanUploadName(name string) (string, error) {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	switch {
	case name == "" || name == "." || name == "..":
		return "", errors.New("no file name")
	case len(name) > maxNameBytes:
		return "", errors.New(name + ": the name is too long")
	case strings.HasPrefix(name, "."):
		return "", errors.New(name + ": a hidden file")
	case !library.Uploadable(name):
		return "", errors.New(name + ": only " + library.UploadKinds() + " can be uploaded")
	}
	return name, nil
}

func uploadError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (sv *Server) apiUploads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, sv.listUploads())
}

func (sv *Server) apiUpload(w http.ResponseWriter, r *http.Request) {
	v, err := sv.readUpload(r.PathValue("id"))
	if err != nil {
		uploadError(w, http.StatusNotFound, "no such upload (finished, cancelled or too old)")
		return
	}
	writeJSON(w, v)
}

// apiStartUpload starts an upload, or, given the fingerprint of one not
// finished (the same file, dropped again), answers with that one to carry
// on from where it got to.
func (sv *Server) apiStartUpload(w http.ResponseWriter, r *http.Request) {
	if sv.cfg.ImportDir == "" {
		uploadError(w, http.StatusBadRequest, "no import folder: set video_dir or import_dir under [paths] in config.toml")
		return
	}
	var m uploadMeta
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&m); err != nil {
		uploadError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	name, err := cleanUploadName(m.Name)
	if m.Group != "" {
		name, err = cleanFolderPath(m.Name)
	}
	switch {
	case m.Group != "" && !validUploadID(m.Group):
		uploadError(w, http.StatusBadRequest, "group: 16 hex digits, the same for every file of the folder")
		return
	case m.Group != "" && m.GroupFiles < 1:
		uploadError(w, http.StatusBadRequest, "group_files: how many files the folder has")
		return
	case err != nil:
		uploadError(w, http.StatusBadRequest, err.Error())
		return
	case m.Size <= 0:
		uploadError(w, http.StatusBadRequest, "size: how big the file is, in bytes")
		return
	case m.Kind != "" && m.Kind != "movie" && m.Kind != "tv":
		uploadError(w, http.StatusBadRequest, `kind: "movie", "tv" or nothing`)
		return
	}
	m.Name = name

	defer sv.ups.lock("")() // one start at a time, so the same file can't start twice
	if d, ok := sv.wasDone(m); ok {
		writeJSON(w, uploadView{uploadMeta: m, Offset: m.Size, Updated: d.At, Done: true, Already: true, Path: d.Path})
		return
	}
	if m.Group != "" {
		if v, ok := sv.groupHas(m); ok { // in already: sent before the tab closed
			writeJSON(w, v)
			return
		}
	}
	if m.Fingerprint != "" {
		for _, v := range sv.listUploads() {
			if v.Fingerprint == m.Fingerprint && v.Size == m.Size {
				writeJSON(w, v)
				return
			}
		}
	}
	if free, ok := library.FreeSpace(sv.cfg.ImportDir); ok && free < m.Size+(64<<20) {
		uploadError(w, http.StatusInsufficientStorage,
			fmt.Sprintf("not enough room: %s needs %s, and the drive has %s free", m.Name, sizeStr(m.Size), sizeStr(free)))
		return
	}
	if err := os.MkdirAll(sv.uploadDir(), 0o777); err != nil {
		uploadError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var b [8]byte
	rand.Read(b[:])
	m.ID, m.Started = hex.EncodeToString(b[:]), time.Now()
	f, err := os.OpenFile(filepath.Join(sv.uploadDir(), m.ID+".part"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		uploadError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rec := uploadRecord{uploadMeta: m}
	if sv.cfg.UploadPreallocate {
		err = allocate(f, m.Size)
		rec.Preallocated = err == nil
	}
	f.Close()
	if err != nil {
		sv.removeUpload(m.ID)
		uploadError(w, http.StatusInsufficientStorage, "couldn't make room for it: "+err.Error())
		return
	}
	if err := sv.writeRecord(rec); err != nil {
		sv.removeUpload(m.ID)
		uploadError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(uploadView{uploadMeta: m, Updated: m.Started})
}

// apiUploadPiece writes a piece at Upload-Offset, which must be where the
// file has got to (else 409, with where that is). Whatever of the piece
// arrives is kept, even if the connection goes part way: the next piece
// starts from there. The last byte finishes it.
func (sv *Server) apiUploadPiece(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUploadID(id) {
		uploadError(w, http.StatusNotFound, "no such upload")
		return
	}
	defer sv.ups.lock(id)()
	v, rec, err := sv.readRecord(id)
	if err != nil {
		uploadError(w, http.StatusNotFound, "no such upload (finished, cancelled or too old)")
		return
	}
	off, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || off != v.Offset {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(v)
		return
	}
	left := v.Size - v.Offset
	if r.ContentLength > left || r.ContentLength > maxPiece {
		uploadError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("that piece is %d bytes; at most %d more are expected", r.ContentLength, min(left, maxPiece)))
		return
	}
	f, err := os.OpenFile(filepath.Join(sv.uploadDir(), id+".part"), os.O_WRONLY, 0)
	if err == nil {
		_, err = f.Seek(v.Offset, io.SeekStart)
	}
	if err != nil {
		if f != nil {
			f.Close()
		}
		uploadError(w, http.StatusInternalServerError, err.Error())
		return
	}
	buf := sv.cfg.UploadBuffer
	if buf <= 0 {
		buf = 256 << 10
	}
	limit := min(left, maxPiece)
	n, copyErr := io.CopyBuffer(f, io.LimitReader(r.Body, limit), make([]byte, buf))
	if copyErr == nil && n == limit {
		var one [1]byte // more than expected, with no Content-Length to have said so?
		if k, _ := r.Body.Read(one[:]); k > 0 {
			copyErr = errors.New("more than expected")
		}
	}
	syncErr := f.Sync()
	f.Close()
	v.Offset += n
	v.Updated = time.Now()
	if rec.Preallocated && syncErr == nil {
		rec.Written = v.Offset
		syncErr = sv.writeRecord(rec)
	}
	if copyErr != nil || syncErr != nil {
		// What arrived is kept; the client asks where it got to and carries on.
		why := copyErr
		if why == nil {
			why = syncErr
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": "piece cut short: " + why.Error(), "offset": v.Offset})
		return
	}
	if v.Offset == v.Size {
		path, err := sv.finishUpload(v)
		if err != nil {
			uploadError(w, http.StatusInternalServerError, "all arrived, but couldn't move it into the import folder: "+err.Error())
			return
		}
		v.Done, v.Path = true, path
	}
	writeJSON(w, v)
}

// finishUpload moves a complete upload into the import folder, under a name
// of its own (a number added if the name's taken), and lets the
// auto-import know.
func (sv *Server) finishUpload(v uploadView) (string, error) {
	if v.Group != "" {
		return sv.finishInGroup(v)
	}
	dir := sv.targetDir(v.Kind)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	unlock := sv.ups.lock("finish") // so two finishing can't both find a name free
	dst := freeName(dir, v.Name, false)
	err := os.Rename(filepath.Join(sv.uploadDir(), v.ID+".part"), dst)
	unlock()
	if err != nil {
		return "", err
	}
	os.Remove(filepath.Join(sv.uploadDir(), v.ID+".json"))
	sv.markDone(v.uploadMeta, dst)
	sv.uploaded(dst)
	return dst, nil
}

// Finished uploads are remembered for uploadMaxAge (in .uploads/done, by
// fingerprint, or a folder's group), so sending the same thing again (the
// tab closed just as it finished, or mvd upload run twice) says it's in
// already rather than sending it twice.
type doneRecord struct {
	Path string    `json:"path"`
	At   time.Time `json:"at"`
}

func doneKey(m uploadMeta) string {
	switch {
	case m.Group != "":
		return "g" + m.Group
	case m.Fingerprint != "":
		h := fnv.New64a()
		fmt.Fprintf(h, "%s\x00%d", m.Fingerprint, m.Size)
		return fmt.Sprintf("f%016x", h.Sum64())
	}
	return ""
}

func (sv *Server) markDone(m uploadMeta, path string) {
	k := doneKey(m)
	if k == "" {
		return
	}
	os.MkdirAll(filepath.Join(sv.uploadDir(), "done"), 0o777)
	b, _ := json.Marshal(doneRecord{Path: path, At: time.Now()})
	os.WriteFile(filepath.Join(sv.uploadDir(), "done", k), b, 0o666)
}

// wasDone is where an upload like m went, if it finished lately.
func (sv *Server) wasDone(m uploadMeta) (doneRecord, bool) {
	var d doneRecord
	k := doneKey(m)
	if k == "" {
		return d, false
	}
	b, err := os.ReadFile(filepath.Join(sv.uploadDir(), "done", k))
	if err != nil || json.Unmarshal(b, &d) != nil || time.Since(d.At) > uploadMaxAge {
		return d, false
	}
	return d, true
}

// sweepDone forgets finished uploads older than uploadMaxAge.
func (sv *Server) sweepDone() {
	dir := filepath.Join(sv.uploadDir(), "done")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > uploadMaxAge {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// uploaded notes a finished file or folder in the import folder: whole, so
// an import needn't wait for it to settle; and tells the auto-import.
func (sv *Server) uploaded(path string) {
	sv.ups.mu.Lock()
	if sv.ups.finished == nil {
		sv.ups.finished = map[string]time.Time{}
	}
	sv.ups.finished[path] = time.Now()
	sv.ups.mu.Unlock()
	sv.kickAutoImport()
}

// targetDir is where finished uploads of a kind go.
func (sv *Server) targetDir(kind string) string {
	switch kind {
	case "movie":
		return filepath.Join(sv.cfg.ImportDir, "movies")
	case "tv":
		return filepath.Join(sv.cfg.ImportDir, "tv")
	}
	return sv.cfg.ImportDir
}

// freeName is dir/name, or with " (2)" and so on before the extension
// (none, for a folder) if that's taken.
func freeName(dir, name string, folder bool) string {
	ext := filepath.Ext(name)
	if folder {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	dst := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			return dst
		}
		dst = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
}

func (sv *Server) apiCancelUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := sv.readUpload(id); err != nil {
		if sv.cancelGroup(id) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		uploadError(w, http.StatusNotFound, "no such upload")
		return
	}
	defer sv.ups.lock(id)()
	sv.removeUpload(id)
	w.WriteHeader(http.StatusNoContent)
}
