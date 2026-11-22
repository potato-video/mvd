package torrent

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"mvd/internal/config"
	"mvd/internal/testutil"
)

func TestTransmissionClient(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		f := testutil.NewTransmission(legacy)
		f.User = "me"
		f.Torrents = []testutil.Torrent{{ID: 1, Name: "Dune.2021", DownloadDir: "/v/import/", Status: Seeding,
			PercentDone: 1, UploadRatio: 0.5, Hash: "abc", PeersSending: 3, PeersGetting: 1, Seeders: 40, Leechers: 12}}
		srv := f.Server(t)
		tr := New(config.Config{Transmission: srv.URL, TorrentUser: "me"})

		ts, err := tr.Torrents()
		if err != nil || len(ts) != 1 || ts[0].Name != "Dune.2021" || ts[0].DownloadDir != "/v/import" ||
			ts[0].UploadRatio != 0.5 || ts[0].Hash != "abc" || tr.legacy != legacy ||
			ts[0].PeersSending != 3 || ts[0].PeersGetting != 1 || ts[0].Seeders != 40 || ts[0].Leechers != 12 {
			t.Fatalf("legacy=%v torrents: %+v %v (detected legacy=%v)", legacy, ts, err, tr.legacy)
		}
		added, dup, err := tr.Add("magnet:?xt=urn:btih:x&dn=Severance.S01", nil, "/v/import/tv")
		if err != nil || dup || added.Name != "Severance.S01" || added.ID == 0 {
			t.Errorf("legacy=%v add: %+v %v %v", legacy, added, dup, err)
		}
		if _, dup, _ := tr.Add("magnet:?xt=urn:btih:x&dn=Severance.S01", nil, "/v/import"); !dup {
			t.Errorf("legacy=%v: second add not a duplicate", legacy)
		}
		if _, _, err := tr.Add("", []byte("d8:announce..."), "/v/import"); err != nil {
			t.Errorf("legacy=%v add file: %v", legacy, err)
		}
		if err := tr.SetSeedRatio(added.ID, 1.5); err != nil || f.Ratios[added.ID] != 1.5 {
			t.Errorf("legacy=%v seed ratio: %v %v", legacy, err, f.Ratios)
		}
		if err := tr.MoveTo(added.ID, 0); err != nil || len(f.Moves) != 1 || f.Moves[0] != [2]int{added.ID, 0} {
			t.Errorf("legacy=%v move: %v %v", legacy, err, f.Moves)
		}
		if ts, _ := tr.Torrents(); ts[0].QueuePosition != 1 || ts[1].QueuePosition != 0 {
			t.Errorf("legacy=%v queue positions after the move: %+v", legacy, ts)
		}
		if err := tr.Stop([]Torrent{{ID: 1}}); err != nil || len(f.Stopped) != 1 || f.Torrents[0].Status != Stopped {
			t.Errorf("legacy=%v stop: %v %v", legacy, err, f.Stopped)
		}
		if err := tr.Remove(1); err != nil || len(f.Removed) != 1 {
			t.Errorf("legacy=%v remove: %v %v", legacy, err, f.Removed)
		}
		if d, err := tr.DownloadDir(); err != nil || d != "/nowhere/import" {
			t.Errorf("legacy=%v download dir: %q %v", legacy, d, err)
		}
		// Transmission restarts: a new session id; the client catches up.
		f.Session = "sess-2"
		if _, err := tr.Torrents(); err != nil {
			t.Errorf("legacy=%v after restart: %v", legacy, err)
		}
	}

	f := testutil.NewTransmission(false)
	f.User = "me"
	tr := New(config.Config{Transmission: f.Server(t).URL})
	if _, err := tr.Torrents(); err == nil || !strings.Contains(err.Error(), "username and password") {
		t.Errorf("no credentials: %v", err)
	}
	tr = New(config.Config{Transmission: "http://127.0.0.1:1/transmission/rpc"})
	if _, err := tr.Torrents(); err == nil || !strings.Contains(err.Error(), "nothing is listening") {
		t.Errorf("not running: %v", err)
	}
}

func TestTorrentReady(t *testing.T) {
	cases := []struct {
		t     Torrent
		ratio float64
		ready bool
	}{
		{Torrent{PercentDone: 0.5, Status: Downloading}, 0, false},
		{Torrent{PercentDone: 1, Status: Seeding}, 0, true},
		{Torrent{PercentDone: 1, Status: Seeding, UploadRatio: 0.4}, 1, false},
		{Torrent{PercentDone: 1, Status: Seeding, UploadRatio: 1.2}, 1, true},
		{Torrent{PercentDone: 1, Status: Stopped, IsFinished: true, UploadRatio: 0.1}, 1, true}, // Transmission's own limit
		{Torrent{PercentDone: 1, Status: Stopped, UploadRatio: 0.1}, 1, false},                  // paused by hand
	}
	for i, c := range cases {
		if ok, why := c.t.Ready(c.ratio); ok != c.ready {
			t.Errorf("case %d: ready=%v (%s)", i, ok, why)
		}
	}
}

func TestMountFS(t *testing.T) {
	dir := t.TempDir()
	table := dir + "/mounts"
	testutil.Write(t, table, `/dev/root / squashfs ro 0 0
overlayfs:/overlay / overlay rw 0 0
/dev/sda1 /mnt/Maxtor exfat rw,relatime,uid=0,gid=0,fmask=0022,dmask=0022 0 0
/dev/sdb1 /mnt/My\040Disk ext4 rw 0 0
`)
	for d, want := range map[string]string{
		"/mnt/Maxtor/video/import": "exfat",
		"/mnt/My Disk/video":       "ext4",
		"/root":                    "overlay",
		"/mnt/Maxtorious":          "overlay",
	} {
		if got := mountFS(table, d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
	if !strings.Contains(runAs("/nonexistent-so-unknown"), "#uci set $T.user='root'") {
		t.Errorf("unknown filesystem should leave root commented out")
	}
}

// Partial downloads go inside the import folder: OpenWRT's jail mounts only
// download_dir, so an incomplete_dir beside it is missing inside the jail
// ("No data found!").
func TestSetupIncompleteInsideImport(t *testing.T) {
	s := Setup(config.Config{ImportDir: "/mnt/Maxtor/video/import", VideoDir: "/mnt/Maxtor/video"})
	if !strings.Contains(s, "incomplete_dir='/mnt/Maxtor/video/import/.downloading'") {
		t.Errorf("incomplete_dir not inside import:\n%s", s)
	}
}

// The package's seccomp filter leaves out syscalls Transmission makes on
// 32-bit MIPS: ftruncate64 (preallocating a new file), and geteuid, getgid
// and getegid (it lists only the …32 names, which MIPS doesn't have). Any of
// them kills the daemon.
func TestSetupAllowsMissingSyscalls(t *testing.T) {
	s := Setup(config.Config{ImportDir: "/mnt/Maxtor/video/import"})
	if !strings.Contains(s, "/etc/seccomp/transmission-daemon.json") {
		t.Fatalf("setup doesn't touch the seccomp filter:\n%s", s)
	}
	for _, name := range []string{"ftruncate", "ftruncate64", "geteuid", "getgid", "getegid"} {
		if !regexp.MustCompile(`for s in [^;]*\b` + name + `\b[^;]*;`).MatchString(s) {
			t.Errorf("setup doesn't add %s to the seccomp filter:\n%s", name, s)
		}
	}
}

// Added torrents start, whatever Transmission's start-added-torrents says.
func TestAddStarts(t *testing.T) {
	f := testutil.NewTransmission(false)
	f.StartPaused = true
	tr := New(config.Config{Transmission: f.Server(t).URL})
	cfg := config.Config{ImportDir: t.TempDir()}
	added, _, _, err := Add(cfg, tr, "magnet:?xt=urn:btih:x&dn=Dune.2021", nil, "movie")
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := tr.Torrents()
	if len(ts) != 1 || ts[0].ID != added.ID || ts[0].Status != Downloading {
		t.Errorf("added paused: %+v", ts)
	}
}

// Transmission can take longer to answer an add than the client waits
// (setting up the files on a slow drive), though it has added it. That's
// not a failure: Add says it's still adding, and Finish finds it once it
// appears, starts it if it's stopped and sets its seed ratio.
func TestAddSlow(t *testing.T) {
	defer func(every time.Duration) { settleEvery = every }(settleEvery)
	settleEvery = 20 * time.Millisecond
	f := testutil.NewTransmission(false)
	f.Torrents = []testutil.Torrent{{ID: 1, Name: "Old.One", Status: Seeding, PercentDone: 1}}
	f.AddDelay = 400 * time.Millisecond
	f.StartPaused = true // an older Transmission ignoring "paused": false, say
	tr := New(config.Config{Transmission: f.Server(t).URL})
	tr.http.Timeout = 100 * time.Millisecond
	tr.Torrents() // (probed before the slow part)
	cfg := config.Config{ImportDir: t.TempDir(), SeedRatio: 1.5}

	_, _, _, err := Add(cfg, tr, "magnet:?xt=urn:btih:x&dn=Severance.S01", nil, "tv")
	var still *StillAdding
	if !errors.As(err, &still) {
		t.Fatalf("want StillAdding, got %v", err)
	}
	if strings.Contains(err.Error(), "can't reach") {
		t.Errorf("reads as a failure: %v", err)
	}
	f.Mu.Lock()
	for i := range f.Torrents { // as if it ignored "paused": false
		if f.Torrents[i].Name == "Severance.S01" {
			f.Torrents[i].Status = Stopped
		}
	}
	f.Mu.Unlock()
	got, err := still.Finish()
	if err != nil || got.Name != "Severance.S01" {
		t.Fatalf("finish: %+v %v", got, err)
	}
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if len(f.Started) != 1 || f.Started[0] != got.ID || f.Ratios[got.ID] != 1.5 {
		t.Errorf("not started or no ratio: started %v ratios %v", f.Started, f.Ratios)
	}
}

func TestDetails(t *testing.T) {
	for _, c := range []struct {
		t    Torrent
		want string
	}{
		{Torrent{Status: Downloading, Size: 4509715660, UploadRatio: 0.123, PeersSending: 3, PeersGetting: 1, Seeders: 40, Leechers: 12},
			"4.2 GB · ratio 0.12 · seeds 3 of 40 · leechers 1 of 12"},
		// The trackers don't know: just who's connected.
		{Torrent{Status: Seeding, PercentDone: 1, Size: 700 << 20, UploadRatio: 1.5, PeersGetting: 2, Seeders: -1, Leechers: -1},
			"700 MB · ratio 1.50 · leechers 2"},
		// Finished, it's getting from nobody: the swarm's seeds alone.
		{Torrent{Status: Seeding, PercentDone: 1, Size: 2 << 30, UploadRatio: 0.42, PeersGetting: 3, Seeders: 58, Leechers: 7},
			"2.0 GB · ratio 0.42 · seeds 58 · leechers 3 of 7"},
		// Stopped: nobody's connected, so the swarm alone.
		{Torrent{Status: Stopped, Size: 1 << 30, UploadRatio: -1, Seeders: 5, Leechers: 0},
			"1.0 GB · ratio 0.00 · seeds 5 · leechers 0"},
		// A magnet still fetching its metadata: no size yet; nothing to share.
		{Torrent{Status: Downloading, UploadRatio: -2, Seeders: -1, Leechers: -1}, "ratio ∞ · seeds 0 · leechers 0"},
	} {
		if got := Details(c.t); got != c.want {
			t.Errorf("Details(%+v)\n got %q\nwant %q", c.t, got, c.want)
		}
	}
}

func TestFacts(t *testing.T) {
	got := Facts(Torrent{Status: Seeding, PercentDone: 1, Size: 2 << 30, UploadRatio: 0.42, PeersGetting: 3, Seeders: 58, Leechers: -1})
	want := []Fact{{"Size", "2.0 GB"}, {"Ratio", "0.42"}, {"Seeds", "58"}, {"Leechers", "3"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}

// A listing as big as the router's in October 2026 (six season packs with
// thirteen trackers each: 54 KB, nearly all trackerStats) is decoded
// without building a map per tracker or copying the body about: it was ten
// times its size in garbage every time the Files page (now Incoming) polled. The body
// itself is read once; HTTP (both ends, here) and the decoded slices are
// under half as much again. Twice the body leaves room for that to drift
// between Go versions, but not for another copy of the trackers.
func TestTorrentsGarbage(t *testing.T) {
	tracker := `{"announce":"udp://tracker.example.org:1337","announceState":1,"downloadCount":35285,` +
		`"hasAnnounced":true,"hasScraped":true,"host":"udp://tracker.example.org:1337","id":%d,"isBackup":false,` +
		`"lastAnnouncePeerCount":80,"lastAnnounceResult":"Success","lastAnnounceStartTime":1793483096,` +
		`"lastAnnounceSucceeded":true,"lastAnnounceTime":1793483097,"lastAnnounceTimedOut":false,` +
		`"lastScrapeResult":"","lastScrapeStartTime":1793483100,"lastScrapeSucceeded":true,` +
		`"lastScrapeTime":1793483100,"lastScrapeTimedOut":false,"leecherCount":%d,"nextAnnounceTime":1793486720,` +
		`"nextScrapeTime":1793484900,"scrape":"udp://tracker.example.org:1337","scrapeState":1,"seederCount":%d,"tier":%[1]d}`
	var ts []string
	for id := 1; id <= 6; id++ {
		var trs []string
		for i := range 13 {
			trs = append(trs, fmt.Sprintf(tracker, i, 100+i, 200+i))
		}
		ts = append(ts, fmt.Sprintf(`{"addedDate":1793480000,"downloadDir":"/mnt/drive/video/import/tv","errorString":"",`+
			`"eta":-1,"hashString":"0123456789abcdef0123456789abcdef01234567","id":%d,"isFinished":false,`+
			`"name":"Show.S0%[1]d.1080p.BluRay.x265-GRP","peersGettingFromUs":0,"peersSendingToUs":3,"percentDone":0.0789,`+
			`"queuePosition":%[1]d,"rateDownload":120000,"rateUpload":0,"seedRatioLimit":2,"seedRatioMode":0,`+
			`"sizeWhenDone":40000000000,"status":4,"trackerStats":[%s],"uploadRatio":0}`, id, strings.Join(trs, ",")))
	}
	body := []byte(`{"arguments":{"torrents":[` + strings.Join(ts, ",") + `]},"result":"success"}`)
	var enc string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enc = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Length", strconv.Itoa(len(body))) // as Transmission does
		w.Write(body)
	}))
	defer srv.Close()
	c := &Client{url: srv.URL, http: srv.Client(), probed: true, legacy: true}

	got, err := c.Torrents()
	if err != nil || len(got) != 6 || got[5].Name != "Show.S06.1080p.BluRay.x265-GRP" || got[5].Seeders != 212 ||
		got[5].Leechers != 112 || got[5].DownloadDir != "/mnt/drive/video/import/tv" || got[5].PeersSending != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	// Nor gzipped: that's a compressor in Transmission and a decompressor
	// here, every time, to save a few KB on the loopback.
	if enc != "identity" {
		t.Errorf("asked for Accept-Encoding %q", enc)
	}
	if testutil.Race {
		return
	}
	const n = 20
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	for range n {
		c.Torrents()
	}
	runtime.ReadMemStats(&b)
	per := (b.TotalAlloc - a.TotalAlloc) / n
	if per > 2*uint64(len(body)) {
		t.Errorf("%d KB of garbage for a %d KB listing", per>>10, len(body)>>10)
	}
	t.Logf("%d KB of garbage for a %d KB listing", per>>10, len(body)>>10)
}

// Review L10: an answer from Transmission is read only up to a limit, and
// its Content-Length isn't trusted for an allocation past it.
func TestTransmissionAnswerLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(maxAnswer+10))
		w.Write([]byte(strings.Repeat(" ", maxAnswer+10)))
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, err := readBody(resp); err == nil {
		t.Errorf("read %d bytes", len(b))
	}
}
