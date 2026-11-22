// Package upload sends files and folders to an mvd server's import folder
// with its resumable uploads (/api/uploads), as the web UI's Incoming page
// does: in pieces, each picked up from wherever the server says it got to,
// so a dropped connection costs a piece, not the file. Sending the same
// thing again (after the program was stopped, say) carries on where it got
// to and skips what's in.
//
// The protocol, for a client in another language (a Mac app, say):
//
//  1. POST /api/uploads {name, size, kind, fingerprint[, group, group_files]}.
//     201 or 200 with the upload: {id, offset, done, ...}. If done, it's in
//     already: nothing to send. fingerprint: anything that's the same each
//     time for the same file (here: name|size|modified-milliseconds), so a
//     second POST finds the unfinished upload. For a folder, name is the
//     file's path in it ("Show S01/Subs/E01.srt"), group 16 hex digits the
//     same for every file of the folder and every time it's sent (here and
//     on the Incoming page: FNV-1a 64 of its files' "path|size|mtime-ms" lines,
//     sorted, joined by newlines), group_files how many files it has.
//  2. PATCH /api/uploads/{id}, header Upload-Offset: the upload's offset,
//     body: the next piece (up to 64 MB). 200 with the new offset (and done
//     after the last). 409: wrong offset; the body has the right one.
//  3. Anything else, or no answer: wait (longer each time), GET
//     /api/uploads/{id} for the offset, and carry on from there.
//
// Send a token, if the server has one, as Authorization: Bearer.
package upload

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mvd/internal/library"
)

// Client sends to one server.
type Client struct {
	Base  string // "http://router:6837"
	Token string
	HTTP  *http.Client
	Piece int64 // bytes a piece; 16 MB if 0
	// GiveUp: how long to keep trying with no progress before giving up
	// (sending again later carries on); 10 minutes if 0.
	GiveUp time.Duration
	// Wait: the first pause after a failed piece, doubling each time to 30s;
	// 1 second if 0.
	Wait time.Duration
	// Progress is told how a file's going, now and then.
	Progress func(Progress)
}

// Progress is a file's state.
type Progress struct {
	Name       string // its name, or path in its folder
	Sent, Size int64
	Note       string // "", or what's happening: "connection lost; trying again in 4s"
	Done       bool
	Already    bool // in already, from before
	FolderDone bool // the last of its folder: the folder's in the import folder
}

// Item is a file to send: Name is what it's called on the server (its path
// in a folder, starting with the folder's name, or just its name).
type Item struct {
	Path       string
	Name       string
	Size       int64
	Modified   time.Time
	Group      string
	GroupFiles int
}

// Plan turns paths (files, folders) into the items to send: a file as
// itself; a folder as a group of its uploadable files (videos, subtitles,
// .nfo, .zip and .rar), each named by its path in it. Hidden files and
// folders are left out, as is anything else in a folder. A file named
// directly that can't be uploaded is an error.
func Plan(paths []string) ([]Item, error) {
	var out []Item
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			if !library.Uploadable(st.Name()) {
				return nil, fmt.Errorf("%s: only %s can be uploaded", p, library.UploadKinds())
			}
			out = append(out, Item{Path: p, Name: st.Name(), Size: st.Size(), Modified: st.ModTime()})
			continue
		}
		top := filepath.Base(filepath.Clean(p))
		var group []Item
		err = filepath.WalkDir(p, func(f string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if f != p && strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() || !library.Uploadable(d.Name()) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(p, f)
			group = append(group, Item{Path: f, Name: top + "/" + filepath.ToSlash(rel), Size: info.Size(), Modified: info.ModTime()})
			return nil
		})
		if err != nil {
			return nil, err
		}
		if len(group) == 0 {
			return nil, fmt.Errorf("%s: nothing in it to upload (%s)", p, library.UploadKinds())
		}
		id := GroupID(group)
		for i := range group {
			group[i].Group, group[i].GroupFiles = id, len(group)
		}
		out = append(out, group...)
	}
	return out, nil
}

// GroupID is a folder's group id: FNV-1a 64 of its files' "path|size|mtime"
// lines (mtime in milliseconds), sorted, joined by newlines; as the Files
// page works it out, so the same folder is the same group either way.
func GroupID(items []Item) string {
	lines := make([]string, len(items))
	for i, it := range items {
		lines[i] = fmt.Sprintf("%s|%d|%d", it.Name, it.Size, it.Modified.UnixMilli())
	}
	sort.Strings(lines)
	h := fnv.New64a()
	io.WriteString(h, strings.Join(lines, "\n"))
	return fmt.Sprintf("%016x", h.Sum64())
}

// ErrToken: the server wants a token this client hasn't got (or the wrong one).
var ErrToken = errors.New("the server wants a token ([server] token in config.toml, or -token)")

type upload struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
	Done   bool   `json:"done"`
	Path   string `json:"path"`
	Error  string `json:"error"`
}

// Send sends one item, kind "movie", "tv" or "".
func (c *Client) Send(it Item, kind string) error {
	piece := c.Piece
	if piece <= 0 {
		piece = 16 << 20
	}
	giveUp := c.GiveUp
	if giveUp <= 0 {
		giveUp = 10 * time.Minute
	}
	tell := func(p Progress) {
		if c.Progress != nil {
			p.Name, p.Size = it.Name, it.Size
			c.Progress(p)
		}
	}
	body := map[string]any{"name": it.Name, "size": it.Size, "kind": kind,
		"fingerprint": fmt.Sprintf("%s|%d|%d", it.Name, it.Size, it.Modified.UnixMilli())}
	if it.Group != "" {
		body["group"], body["group_files"] = it.Group, it.GroupFiles
	}
	var u upload
	code, err := c.do("POST", "/api/uploads", body, nil, 0, &u)
	switch {
	case err != nil:
		return err
	case code != 200 && code != 201:
		return fmt.Errorf("%s: %s", it.Name, orStatus(u.Error, code))
	case u.Done:
		tell(Progress{Sent: it.Size, Done: true, Already: true})
		return nil
	}
	f, err := os.Open(it.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	first := c.Wait
	if first <= 0 {
		first = time.Second
	}
	offset, wait, lastGood := u.Offset, first, time.Now()
	for offset < it.Size {
		n := min(piece, it.Size-offset)
		var r upload
		code, err := c.do("PATCH", "/api/uploads/"+u.ID, nil, io.NewSectionReader(f, offset, n), offset, &r)
		switch {
		case err == nil && code == 200:
			offset, wait, lastGood = r.Offset, first, time.Now()
			if r.Done {
				tell(Progress{Sent: it.Size, Done: true, FolderDone: it.Group != "" && r.Path != ""})
				return nil
			}
			tell(Progress{Sent: offset})
			continue
		case errors.Is(err, ErrToken), code == http.StatusForbidden:
			return err
		case err == nil && code == 409:
			offset = r.Offset // where it really got to
			continue
		case err == nil && code == 404:
			return fmt.Errorf("%s: the server no longer has this upload (cancelled?)", it.Name)
		case err == nil && (code == 413 || code == 507):
			return fmt.Errorf("%s: %s", it.Name, orStatus(r.Error, code))
		}
		// No answer, or a bad one: wait, then ask where it got to.
		if time.Since(lastGood) > giveUp {
			return fmt.Errorf("%s: no progress for %s; send it again later to carry on from %s", it.Name,
				giveUp, size(offset))
		}
		why := "connection lost"
		if err == nil {
			why = orStatus(r.Error, code)
		}
		tell(Progress{Sent: offset, Note: fmt.Sprintf("%s; trying again in %s", why, wait)})
		time.Sleep(wait)
		wait = min(wait*2, 30*time.Second)
		var q upload
		if code, err := c.do("GET", "/api/uploads/"+u.ID, nil, nil, 0, &q); err == nil && code == 200 {
			offset = q.Offset
		} else if err == nil && code == 404 {
			return fmt.Errorf("%s: the server no longer has this upload (cancelled?)", it.Name)
		}
	}
	return nil
}

// do makes a request: a JSON body, or a piece (from body, at offset).
func (c *Client) do(method, path string, js any, body io.Reader, offset int64, out any) (int, error) {
	var rd io.Reader
	var n int64 = -1
	if js != nil {
		b, _ := json.Marshal(js)
		rd, n = strings.NewReader(string(b)), int64(len(b))
	} else if body != nil {
		rd = body
		if s, ok := body.(*io.SectionReader); ok {
			n = s.Size()
		}
	}
	req, err := http.NewRequest(method, strings.TrimSuffix(c.Base, "/")+path, rd)
	if err != nil {
		return 0, err
	}
	if n >= 0 {
		req.ContentLength = n
	}
	switch {
	case js != nil:
		req.Header.Set("Content-Type", "application/json")
	case body != nil:
		req.Header.Set("Content-Type", "application/offset+octet-stream")
		req.Header.Set("Upload-Offset", fmt.Sprint(offset))
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil {
		json.Unmarshal(b, out)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return resp.StatusCode, ErrToken
	case http.StatusForbidden:
		// Refused, in words: a viewer's token, which can only watch.
		return resp.StatusCode, errors.New("the server refused: " + strings.TrimPrefix(strings.TrimSpace(string(b)), "refused: "))
	}
	return resp.StatusCode, nil
}

func orStatus(msg string, code int) string {
	if msg != "" {
		return msg
	}
	return fmt.Sprintf("the server answered %d %s", code, http.StatusText(code))
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}
