package testutil

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Torrent is a torrent as the fake keeps it: the same fields as the
// real client's Torrent, so tests read naturally, without depending on it.
type Torrent struct {
	ID          int
	Name        string
	Hash        string
	DownloadDir string
	Status      int
	PercentDone float64
	UploadRatio float64
	IsFinished  bool
	Error       string
	Size        int64
	ETA         int64
	RateDown    int64
	RateUp      int64
	Added       int64
	RatioLimit  float64
	RatioMode   int
	// Connected peers, and the swarm as its trackers last saw it (two
	// trackers: one with these counts, one that doesn't know, -1).
	PeersSending, PeersGetting int
	Seeders, Leechers          int
	// Its place in Transmission's queue, from 0. Tests can leave them all
	// 0: the first move numbers them in the order they're listed.
	QueuePosition int
	FileCount     int
}

const Downloading = 4 // Transmission's "downloading"

// Transmission speaks either RPC protocol, with the CSRF dance and
// optional basic auth.
type Transmission struct {
	Mu       sync.Mutex
	legacy   bool
	User     string
	Torrents []Torrent
	Removed  []int
	Deleted  []int // removed with their data
	Ratios   map[int]float64
	Modes    map[int]int
	Started  []int
	Stopped  []int
	Verified []int
	Moves    [][2]int // {id, queue position}
	Added    []map[string]any
	nextID   int
	Session  string

	// Like a real Transmission on a slow drive: it adds the torrent, then
	// takes AddDelay to answer (setting up its files).
	AddDelay time.Duration
	// Like start-added-torrents = false: torrents added without
	// "paused": false start out stopped.
	StartPaused bool
}

func NewTransmission(legacy bool) *Transmission {
	return &Transmission{legacy: legacy, Ratios: map[int]float64{}, Modes: map[int]int{}, nextID: 100, Session: "sess-1"}
}

func (f *Transmission) Server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return srv
}

func (f *Transmission) torrentJSON(x Torrent) map[string]any {
	if f.legacy {
		return map[string]any{"queuePosition": x.QueuePosition, "peersSendingToUs": x.PeersSending, "peersGettingFromUs": x.PeersGetting,
			"trackerStats": []map[string]any{{"seederCount": x.Seeders, "leecherCount": x.Leechers},
				{"seederCount": -1, "leecherCount": -1}},
			"id": x.ID, "name": x.Name, "hashString": x.Hash, "downloadDir": x.DownloadDir,
			"status": x.Status, "percentDone": x.PercentDone, "uploadRatio": x.UploadRatio, "isFinished": x.IsFinished,
			"errorString": x.Error, "sizeWhenDone": x.Size, "eta": x.ETA, "rateDownload": x.RateDown,
			"rateUpload": x.RateUp, "addedDate": x.Added, "seedRatioLimit": x.RatioLimit, "seedRatioMode": x.RatioMode,
			"file-count": x.FileCount}
	}
	return map[string]any{"queue_position": x.QueuePosition, "peers_sending_to_us": x.PeersSending, "peers_getting_from_us": x.PeersGetting,
		"tracker_stats": []map[string]any{{"seeder_count": x.Seeders, "leecher_count": x.Leechers},
			{"seeder_count": -1, "leecher_count": -1}},
		"id": x.ID, "name": x.Name, "hash_string": x.Hash, "download_dir": x.DownloadDir,
		"status": x.Status, "percent_done": x.PercentDone, "upload_ratio": x.UploadRatio, "is_finished": x.IsFinished,
		"error_string": x.Error, "size_when_done": x.Size, "eta": x.ETA, "rate_download": x.RateDown,
		"rate_upload": x.RateUp, "added_date": x.Added, "seed_ratio_limit": x.RatioLimit, "seed_ratio_mode": x.RatioMode,
		"file_count": x.FileCount}
}

func (f *Transmission) handle(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.User != "" {
		if u, _, ok := r.BasicAuth(); !ok || u != f.User {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	if r.Header.Get("X-Transmission-Session-Id") != f.Session {
		w.Header().Set("X-Transmission-Session-Id", f.Session)
		w.WriteHeader(http.StatusConflict)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		JSONRPC   string         `json:"jsonrpc"`
		Method    string         `json:"method"`
		Params    map[string]any `json:"params"`
		Arguments map[string]any `json:"arguments"`
		ID        any            `json:"id"`
	}
	json.Unmarshal(body, &req)
	args := req.Params
	if f.legacy {
		args = req.Arguments
	}
	reply := func(result any, errMsg string) {
		if f.legacy {
			res := "success"
			if errMsg != "" {
				res = errMsg
			}
			json.NewEncoder(w).Encode(map[string]any{"result": res, "arguments": result})
			return
		}
		if errMsg != "" {
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32000, "message": errMsg}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	// An old server answers a JSON-RPC 2.0 probe the old way.
	if f.legacy && req.JSONRPC != "" {
		json.NewEncoder(w).Encode(map[string]any{"result": "method name not recognized", "arguments": map[string]any{}})
		return
	}
	m := strings.ReplaceAll(req.Method, "-", "_")
	if (f.legacy && strings.Contains(req.Method, "_")) || (!f.legacy && strings.Contains(req.Method, "-")) {
		reply(nil, "method name not recognized")
		return
	}
	ids := func() []int {
		var out []int
		for _, v := range args["ids"].([]any) {
			out = append(out, int(v.(float64)))
		}
		return out
	}
	switch m {
	case "session_get":
		key := "download_dir"
		if f.legacy {
			key = "download-dir"
		}
		reply(map[string]any{key: "/nowhere/import", "version": "4.1.3"}, "")
	case "torrent_get":
		var ts []map[string]any
		for _, x := range f.Torrents {
			ts = append(ts, f.torrentJSON(x))
		}
		reply(map[string]any{"torrents": ts}, "")
	case "torrent_add":
		f.Added = append(f.Added, args)
		dirKey := "download_dir"
		if f.legacy {
			dirKey = "download-dir"
		}
		name := "Some.Download"
		if mi, ok := args["metainfo"].(string); ok {
			b, _ := base64.StdEncoding.DecodeString(mi)
			name = "from-file:" + string(b[:min(len(b), 12)])
		} else if fn, _ := args["filename"].(string); strings.Contains(fn, "dn=") {
			name = fn[strings.Index(fn, "dn=")+3:]
		}
		for _, x := range f.Torrents {
			if x.Name == name {
				k := "torrent_duplicate"
				if f.legacy {
					k = "torrent-duplicate"
				}
				reply(map[string]any{k: f.torrentJSON(x)}, "")
				return
			}
		}
		f.nextID++
		x := Torrent{ID: f.nextID, Name: name, DownloadDir: args[dirKey].(string), Status: Downloading,
			QueuePosition: len(f.Torrents)} // the end of the queue
		if p, ok := args["paused"].(bool); p || (!ok && f.StartPaused) {
			x.Status = 0 // stopped
		}
		f.Torrents = append(f.Torrents, x)
		if f.AddDelay > 0 {
			f.Mu.Unlock()
			time.Sleep(f.AddDelay)
			f.Mu.Lock()
		}
		k := "torrent_added"
		if f.legacy {
			k = "torrent-added"
		}
		reply(map[string]any{k: map[string]any{"id": x.ID, "name": x.Name}}, "")
	case "torrent_set":
		key, mkey, qkey := "seed_ratio_limit", "seed_ratio_mode", "queue_position"
		if f.legacy {
			key, mkey, qkey = "seedRatioLimit", "seedRatioMode", "queuePosition"
		}
		for _, id := range ids() {
			if v, ok := args[qkey].(float64); ok {
				f.Moves = append(f.Moves, [2]int{id, int(v)})
				f.moveTo(id, int(v))
			}
			if v, ok := args[key].(float64); ok {
				f.Ratios[id] = v
			}
			if v, ok := args[mkey].(float64); ok {
				f.Modes[id] = int(v)
				for i := range f.Torrents {
					if f.Torrents[i].ID == id {
						f.Torrents[i].RatioMode = int(v)
					}
				}
			}
		}
		reply(map[string]any{}, "")
	case "torrent_start":
		for _, id := range ids() {
			f.Started = append(f.Started, id)
			for i := range f.Torrents {
				if f.Torrents[i].ID == id {
					f.Torrents[i].Status = Downloading
				}
			}
		}
		reply(map[string]any{}, "")
	case "torrent_verify":
		f.Verified = append(f.Verified, ids()...)
		reply(map[string]any{}, "")
	case "torrent_stop":
		for _, id := range ids() {
			f.Stopped = append(f.Stopped, id)
			for i := range f.Torrents {
				if f.Torrents[i].ID == id {
					f.Torrents[i].Status = 0 // stopped
				}
			}
		}
		reply(map[string]any{}, "")
	case "torrent_remove":
		key := "delete_local_data"
		if f.legacy {
			key = "delete-local-data"
		}
		del := args[key] == true
		keep := f.Torrents[:0]
		for _, x := range f.Torrents {
			gone := false
			for _, id := range ids() {
				if x.ID == id {
					gone = true
					f.Removed = append(f.Removed, id)
					if del {
						f.Deleted = append(f.Deleted, id)
					}
				}
			}
			if !gone {
				keep = append(keep, x)
			}
		}
		f.Torrents = keep
		reply(map[string]any{}, "")
	default:
		reply(nil, "method name not recognized")
	}
}

// moveTo puts a torrent at a place in the queue, the rest shuffling along,
// as Transmission does. Positions stay 0, 1, 2...
func (f *Transmission) moveTo(id, pos int) {
	order := make([]int, len(f.Torrents)) // indexes into f.Torrents, in queue order
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return f.Torrents[order[a]].QueuePosition < f.Torrents[order[b]].QueuePosition
	})
	from := -1
	for i, ti := range order {
		if f.Torrents[ti].ID == id {
			from = i
		}
	}
	if from < 0 {
		return
	}
	moving := order[from]
	order = append(order[:from], order[from+1:]...)
	pos = max(0, min(pos, len(order)))
	order = append(order[:pos], append([]int{moving}, order[pos:]...)...)
	for p, ti := range order {
		f.Torrents[ti].QueuePosition = p
	}
}
