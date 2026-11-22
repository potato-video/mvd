// Package torrent drives Transmission over its RPC API: adding downloads into
// the import folder, and knowing when they're done and seeded.
package torrent

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mvd/internal/config"
)

// A small client for Transmission's RPC. Transmission 4.1 speaks JSON-RPC
// 2.0 with snake_case names; older versions (and 4.x, deprecated) speak the
// original protocol with camelCase and kebab-case names. The first call
// probes which, and everything after uses the right one.

type Client struct {
	url, user, pass string
	http            *http.Client

	mu     sync.Mutex
	sid    string // X-Transmission-Session-Id, for its CSRF check
	probed bool
	legacy bool
	nextID int
}

type Torrent struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Hash        string  `json:"hash"`
	DownloadDir string  `json:"download_dir"`
	Status      int     `json:"status"`
	PercentDone float64 `json:"percent_done"`
	UploadRatio float64 `json:"upload_ratio"`
	IsFinished  bool    `json:"is_finished"`
	Error       string  `json:"error,omitempty"`
	Size        int64   `json:"size"`
	ETA         int64   `json:"eta"` // seconds; negative when unknown
	RateDown    int64   `json:"rate_down"`
	RateUp      int64   `json:"rate_up"`
	Added       int64   `json:"added"`
	// Peers connected now: sending to us (mostly seeds), and getting from
	// us (leechers).
	PeersSending int `json:"peers_sending"`
	PeersGetting int `json:"peers_getting"`
	// The swarm, as its trackers last counted it (the most any of them
	// saw); -1 when none knows.
	Seeders  int `json:"seeders"`
	Leechers int `json:"leechers"`
	// Its place in Transmission's queue, from 0: the order queued ones
	// start in. New ones join the end.
	QueuePosition int     `json:"queue_position"`
	FileCount     int     `json:"file_count,omitempty"` // how many files; 0 when Transmission doesn't say (before 4.0)
	RatioLimit    float64 `json:"-"`                    // the torrent's own seed limit...
	RatioMode     int     `json:"-"`                    // ...and whether it applies (1) or the global one does (0)
}

// Transmission's status numbers.
const (
	Stopped = iota
	CheckWait
	Checking
	DownloadWait
	Downloading
	SeedWait
	Seeding
)

func New(c config.Config) *Client {
	return &Client{url: c.Transmission, user: c.TorrentUser, pass: c.TorrentPass,
		http: &http.Client{Timeout: 20 * time.Second}}
}

// Torrents lists every torrent.
func (t *Client) Torrents() ([]Torrent, error) {
	if err := t.probe(); err != nil {
		return nil, err
	}
	fields := []string{"id", "name", "hash_string", "download_dir", "status", "percent_done",
		"upload_ratio", "is_finished", "error_string", "size_when_done", "eta", "rate_download",
		"rate_upload", "added_date", "seed_ratio_limit", "seed_ratio_mode",
		"peers_sending_to_us", "peers_getting_from_us", "tracker_stats", "queue_position", "file_count"}
	method := "torrent_get"
	if t.legacy {
		method = "torrent-get"
		fields = []string{"id", "name", "hashString", "downloadDir", "status", "percentDone",
			"uploadRatio", "isFinished", "errorString", "sizeWhenDone", "eta", "rateDownload",
			"rateUpload", "addedDate", "seedRatioLimit", "seedRatioMode",
			"peersSendingToUs", "peersGettingFromUs", "trackerStats", "queuePosition", "file-count"}
	}
	var r struct {
		Torrents []wireTorrent `json:"torrents"`
	}
	if err := t.call(method, map[string]any{"fields": fields}, &r); err != nil {
		return nil, err
	}
	out := make([]Torrent, 0, len(r.Torrents))
	for i := range r.Torrents {
		out = append(out, r.Torrents[i].torrent())
	}
	return out, nil
}

// Add adds a torrent: a magnet link or URL, or the contents of a .torrent
// file. dir is where it should end up when complete.
func (t *Client) Add(source string, file []byte, dir string) (Torrent, bool, error) {
	if err := t.probe(); err != nil {
		return Torrent{}, false, err
	}
	// Started, whatever Transmission's start-added-torrents says.
	params := map[string]any{"paused": false}
	if file != nil {
		params["metainfo"] = base64.StdEncoding.EncodeToString(file)
	} else {
		params["filename"] = source
	}
	method, dirKey := "torrent_add", "download_dir"
	if t.legacy {
		method, dirKey = "torrent-add", "download-dir"
	}
	if dir != "" {
		params[dirKey] = dir
	}
	var r map[string]wireTorrent
	if err := t.call(method, params, &r); err != nil {
		return Torrent{}, false, err
	}
	for k, v := range r {
		switch k {
		case "torrent_added", "torrent-added":
			return v.torrent(), false, nil
		case "torrent_duplicate", "torrent-duplicate":
			return v.torrent(), true, nil
		}
	}
	return Torrent{}, false, errors.New("Transmission didn't say what it added")
}

// SetSeedRatio makes Transmission stop seeding a torrent at this ratio.
func (t *Client) SetSeedRatio(id int, ratio float64) error {
	if err := t.probe(); err != nil {
		return err
	}
	if t.legacy {
		return t.call("torrent-set", map[string]any{"ids": []int{id}, "seedRatioLimit": ratio, "seedRatioMode": 1}, nil)
	}
	return t.call("torrent_set", map[string]any{"ids": []int{id}, "seed_ratio_limit": ratio, "seed_ratio_mode": 1}, nil)
}

// Resume starts paused torrents. A torrent with its own seed limit of 0 (as
// mvd once set) counts as already done seeding and stops straight away, so
// that limit is handed back to Transmission's global setting first.
func (t *Client) Resume(ts []Torrent) error {
	if len(ts) == 0 {
		return nil
	}
	if err := t.probe(); err != nil {
		return err
	}
	var ids, zero []int
	for _, x := range ts {
		ids = append(ids, x.ID)
		if x.RatioMode == 1 && x.RatioLimit == 0 {
			zero = append(zero, x.ID)
		}
	}
	if len(zero) > 0 {
		var err error
		if t.legacy {
			err = t.call("torrent-set", map[string]any{"ids": zero, "seedRatioMode": 0}, nil)
		} else {
			err = t.call("torrent_set", map[string]any{"ids": zero, "seed_ratio_mode": 0}, nil)
		}
		if err != nil {
			return err
		}
	}
	if t.legacy {
		return t.call("torrent-start", map[string]any{"ids": ids}, nil)
	}
	return t.call("torrent_start", map[string]any{"ids": ids}, nil)
}

// MoveTo puts a torrent at a place in the queue (from 0); the others
// shuffle along.
func (t *Client) MoveTo(id, pos int) error {
	if err := t.probe(); err != nil {
		return err
	}
	if t.legacy {
		return t.call("torrent-set", map[string]any{"ids": []int{id}, "queuePosition": pos}, nil)
	}
	return t.call("torrent_set", map[string]any{"ids": []int{id}, "queue_position": pos}, nil)
}

// Stop pauses torrents: downloading, seeding or waiting to. Resume starts
// them again.
func (t *Client) Stop(ts []Torrent) error {
	if len(ts) == 0 {
		return nil
	}
	if err := t.probe(); err != nil {
		return err
	}
	var ids []int
	for _, x := range ts {
		ids = append(ids, x.ID)
	}
	if t.legacy {
		return t.call("torrent-stop", map[string]any{"ids": ids}, nil)
	}
	return t.call("torrent_stop", map[string]any{"ids": ids}, nil)
}

// Verify has Transmission check the data torrents have on disk, marking
// what's missing or doesn't match to download again, then starts them. Their
// error, if they have one, clears as they start.
func (t *Client) Verify(ts []Torrent) error {
	if len(ts) == 0 {
		return nil
	}
	if err := t.probe(); err != nil {
		return err
	}
	var ids []int
	for _, x := range ts {
		ids = append(ids, x.ID)
	}
	var err error
	if t.legacy {
		err = t.call("torrent-verify", map[string]any{"ids": ids}, nil)
	} else {
		err = t.call("torrent_verify", map[string]any{"ids": ids}, nil)
	}
	if err != nil {
		return err
	}
	return t.Resume(ts)
}

// Failed says whether Transmission reports an error for a torrent.
func (t Torrent) Failed() bool { return t.Error != "" }

// Active says whether a torrent is going (downloading, seeding, checking,
// or queued for one of those), so can be paused.
func (t Torrent) Active() bool { return t.Status != Stopped }

// Paused says whether a torrent is stopped for no good reason: not one that
// has finished seeding (import takes those), nor one in error.
func (t Torrent) Paused() bool {
	return t.Status == Stopped && !t.IsFinished && t.Error == ""
}

// Remove takes a torrent out of Transmission, leaving its files.
func (t *Client) Remove(id int) error { return t.RemoveTorrents([]int{id}, false) }

// RemoveTorrents takes torrents out of Transmission, deleting what they've
// downloaded if deleteData is set.
func (t *Client) RemoveTorrents(ids []int, deleteData bool) error {
	if len(ids) == 0 {
		return nil
	}
	if err := t.probe(); err != nil {
		return err
	}
	if t.legacy {
		return t.call("torrent-remove", map[string]any{"ids": ids, "delete-local-data": deleteData}, nil)
	}
	return t.call("torrent_remove", map[string]any{"ids": ids, "delete_local_data": deleteData}, nil)
}

// DownloadDir is Transmission's default download folder.
func (t *Client) DownloadDir() (string, error) {
	if err := t.probe(); err != nil {
		return "", err
	}
	var r map[string]any
	method, key := "session_get", "download_dir"
	if t.legacy {
		method, key = "session-get", "download-dir"
	}
	if err := t.call(method, map[string]any{}, &r); err != nil {
		return "", err
	}
	d, _ := r[key].(string)
	return d, nil
}

// wireTorrent is a torrent as Transmission sends it: each field under its
// 4.1 name, and (the L fields) its older one. Typed throughout, not a map of
// raw fields: that copied every torrent's trackerStats, nearly all of the
// answer, before decoding them.
type wireTorrent struct {
	ID            int           `json:"id"`
	Name          string        `json:"name"`
	Status        int           `json:"status"`
	ETA           int64         `json:"eta"`
	Hash          string        `json:"hash_string"`
	HashL         string        `json:"hashString"`
	DownloadDir   string        `json:"download_dir"`
	DownloadDirL  string        `json:"downloadDir"`
	PercentDone   float64       `json:"percent_done"`
	PercentDoneL  float64       `json:"percentDone"`
	UploadRatio   float64       `json:"upload_ratio"`
	UploadRatioL  float64       `json:"uploadRatio"`
	IsFinished    bool          `json:"is_finished"`
	IsFinishedL   bool          `json:"isFinished"`
	Error         string        `json:"error_string"`
	ErrorL        string        `json:"errorString"`
	Size          int64         `json:"size_when_done"`
	SizeL         int64         `json:"sizeWhenDone"`
	RateDown      int64         `json:"rate_download"`
	RateDownL     int64         `json:"rateDownload"`
	RateUp        int64         `json:"rate_upload"`
	RateUpL       int64         `json:"rateUpload"`
	Added         int64         `json:"added_date"`
	AddedL        int64         `json:"addedDate"`
	RatioLimit    float64       `json:"seed_ratio_limit"`
	RatioLimitL   float64       `json:"seedRatioLimit"`
	RatioMode     int           `json:"seed_ratio_mode"`
	RatioModeL    int           `json:"seedRatioMode"`
	PeersSending  int           `json:"peers_sending_to_us"`
	PeersSendingL int           `json:"peersSendingToUs"`
	PeersGetting  int           `json:"peers_getting_from_us"`
	PeersGettingL int           `json:"peersGettingFromUs"`
	QueuePos      int           `json:"queue_position"`
	QueuePosL     int           `json:"queuePosition"`
	Files         int           `json:"file_count"`
	FilesL        int           `json:"file-count"` // (Transmission 4; 3 doesn't know it)
	Trackers      []wireTracker `json:"tracker_stats"`
	TrackersL     []wireTracker `json:"trackerStats"`
}

// wireTracker is the little of a tracker's two dozen fields that mvd uses.
type wireTracker struct {
	Seeders   *int `json:"seeder_count"`
	Leechers  *int `json:"leecher_count"`
	SeedersL  *int `json:"seederCount"`
	LeechersL *int `json:"leecherCount"`
}

// torrent is w as mvd's Torrent. An answer uses one set of names, so the
// other set is zero, and cmp.Or picks whichever was sent.
func (w *wireTorrent) torrent() Torrent {
	x := Torrent{
		ID:            w.ID,
		Name:          w.Name,
		Status:        w.Status,
		ETA:           w.ETA,
		Hash:          cmp.Or(w.Hash, w.HashL),
		DownloadDir:   filepath.Clean(cmp.Or(w.DownloadDir, w.DownloadDirL)),
		PercentDone:   cmp.Or(w.PercentDone, w.PercentDoneL),
		UploadRatio:   cmp.Or(w.UploadRatio, w.UploadRatioL),
		IsFinished:    w.IsFinished || w.IsFinishedL,
		Error:         cmp.Or(w.Error, w.ErrorL),
		Size:          cmp.Or(w.Size, w.SizeL),
		RateDown:      cmp.Or(w.RateDown, w.RateDownL),
		RateUp:        cmp.Or(w.RateUp, w.RateUpL),
		Added:         cmp.Or(w.Added, w.AddedL),
		RatioLimit:    cmp.Or(w.RatioLimit, w.RatioLimitL),
		RatioMode:     cmp.Or(w.RatioMode, w.RatioModeL),
		PeersSending:  cmp.Or(w.PeersSending, w.PeersSendingL),
		PeersGetting:  cmp.Or(w.PeersGetting, w.PeersGettingL),
		QueuePosition: cmp.Or(w.QueuePos, w.QueuePosL),
		FileCount:     cmp.Or(w.Files, w.FilesL),
		Seeders:       -1,
		Leechers:      -1,
	}
	count := func(a, b *int) int {
		switch {
		case a != nil:
			return *a
		case b != nil:
			return *b
		}
		return -1
	}
	for _, trs := range [][]wireTracker{w.Trackers, w.TrackersL} {
		for _, tk := range trs {
			x.Seeders = max(x.Seeders, count(tk.Seeders, tk.SeedersL))
			x.Leechers = max(x.Leechers, count(tk.Leechers, tk.LeechersL))
		}
	}
	return x
}

// probe works out which protocol the server speaks: a JSON-RPC 2.0 request
// gets a JSON-RPC 2.0 reply from 4.1 on, and an old-style "method name not
// recognized" from anything older.
func (t *Client) probe() error {
	t.mu.Lock()
	done := t.probed
	t.mu.Unlock()
	if done {
		return nil
	}
	body, err := t.post(map[string]any{"jsonrpc": "2.0", "method": "session_get",
		"params": map[string]any{"fields": []string{"version"}}, "id": 0})
	if err != nil {
		return err
	}
	var r struct {
		JSONRPC string `json:"jsonrpc"`
	}
	json.Unmarshal(body, &r)
	t.mu.Lock()
	t.legacy, t.probed = r.JSONRPC != "2.0", true
	t.mu.Unlock()
	return nil
}

func (t *Client) call(method string, params map[string]any, out any) error {
	t.mu.Lock()
	t.nextID++
	id, legacy := t.nextID, t.legacy
	t.mu.Unlock()

	// The answer is decoded straight into out (an interface holding a
	// pointer is filled in place), not copied out of the body first.
	if out == nil {
		out = &struct{}{}
	}
	if legacy {
		body, err := t.post(map[string]any{"method": method, "arguments": params, "tag": id})
		if err != nil {
			return err
		}
		r := struct {
			Result    string `json:"result"`
			Arguments any    `json:"arguments"`
		}{Arguments: out}
		if err := json.Unmarshal(body, &r); err != nil {
			return fmt.Errorf("Transmission: %v", err)
		}
		if r.Result != "success" {
			return fmt.Errorf("Transmission: %s", r.Result)
		}
		return nil
	}

	body, err := t.post(map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "id": id})
	if err != nil {
		return err
	}
	r := struct {
		Result any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				ErrorString string `json:"error_string"`
			} `json:"data"`
		} `json:"error"`
	}{Result: out}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("Transmission: %v", err)
	}
	if r.Error != nil {
		msg := r.Error.Message
		if r.Error.Data.ErrorString != "" {
			msg += ": " + r.Error.Data.ErrorString
		}
		return fmt.Errorf("Transmission: %s", msg)
	}
	return nil
}

// post sends one request, doing the session-id dance: the first request
// (and any after Transmission restarts) gets a 409 carrying the id to use.
// noAnswer: Transmission didn't answer in time. It may still have done
// what it was asked: it answers once the work is done, and some work (an
// add, on a slow drive) can take longer than the client waits.
type noAnswer struct{ error }

func (e *noAnswer) Unwrap() error { return e.error }

func isNoAnswer(err error) bool {
	var na *noAnswer
	return errors.As(err, &na)
}

func (t *Client) post(payload any) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest("POST", t.url, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		// Not gzip: Transmission would set up a compressor (a quarter of a
		// megabyte) for every answer, and mvd a decompressor, to save a
		// few KB on what's usually the loopback.
		req.Header.Set("Accept-Encoding", "identity")
		t.mu.Lock()
		if t.sid != "" {
			req.Header.Set("X-Transmission-Session-Id", t.sid)
		}
		t.mu.Unlock()
		if t.user != "" {
			req.SetBasicAuth(t.user, t.pass)
		}
		resp, err := t.http.Do(req)
		if err != nil {
			why := err.Error()
			var ue *url.Error
			if errors.As(err, &ue) {
				why = ue.Err.Error()
			}
			hint := ""
			switch {
			case strings.Contains(why, "connection refused"):
				hint = ": nothing is listening there; is it running, and on that port?"
			case strings.Contains(why, "timeout"), strings.Contains(why, "deadline"):
				hint = ": no answer; a firewall, or the wrong address?"
			}
			var ne net.Error
			timeout := errors.As(err, &ne) && ne.Timeout()
			err = fmt.Errorf("can't reach Transmission at %s (%s)%s", t.url, why, hint)
			if timeout {
				err = &noAnswer{err}
			}
			return nil, err
		}
		body, err := readBody(resp)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusOK, http.StatusNoContent:
			return body, nil
		case http.StatusConflict:
			t.mu.Lock()
			t.sid = resp.Header.Get("X-Transmission-Session-Id")
			t.mu.Unlock()
			continue
		case http.StatusUnauthorized:
			return nil, errors.New("Transmission wants a username and password: set username and password under [downloads] in config.toml")
		case http.StatusForbidden:
			return nil, errors.New("Transmission refused the connection: check rpc_whitelist (and rpc_host_whitelist) in /etc/config/transmission")
		default:
			return nil, fmt.Errorf("Transmission: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(clip(string(body), 120)))
		}
	}
	return nil, errors.New("Transmission kept rejecting its own session id")
}

// maxAnswer is the most read of an answer from Transmission: a poll of six
// season packs is about 55 KB, so this is thousands of torrents.
const maxAnswer = 16 << 20

var errAnswerTooBig = errors.New("Transmission's answer is bigger than mvd will read")

// readBody reads an answer in one allocation when it says how long it is,
// rather than growing a buffer to fit (twice its size, all told).
func readBody(resp *http.Response) ([]byte, error) {
	n := resp.ContentLength
	if n > maxAnswer {
		return nil, errAnswerTooBig
	}
	if n > 0 {
		b := make([]byte, n)
		_, err := io.ReadFull(resp.Body, b)
		return b, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err == nil && len(b) > maxAnswer {
		return nil, errAnswerTooBig
	}
	return b, err
}

// Ready says whether a finished download can be taken out of Transmission
// and imported, and if not, why not.
func (t Torrent) Ready(seedRatio float64) (bool, string) {
	switch {
	case t.Error != "" && t.PercentDone < 1:
		return false, "Transmission reports an error: " + t.Error
	case t.PercentDone < 1:
		return false, fmt.Sprintf("still downloading (%.0f%%)", t.PercentDone*100)
	case seedRatio <= 0, t.IsFinished, t.UploadRatio >= seedRatio:
		return true, ""
	case t.Status == Stopped:
		return false, fmt.Sprintf("paused in Transmission at ratio %.2f of %.2f", max(t.UploadRatio, 0), seedRatio)
	}
	return false, fmt.Sprintf("seeding, ratio %.2f of %.2f", max(t.UploadRatio, 0), seedRatio)
}
