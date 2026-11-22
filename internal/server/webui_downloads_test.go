//go:build webui && downloads && files

package server

import (
	"path/filepath"
	"strings"
	"testing"

	"mvd/internal/testutil"
	"mvd/internal/torrent"
)

// Downloads on the Incoming page: added from the bar, each row's buttons,
// the queue dragged into order, Pause all and Resume all.
func TestIncomingDownloads(t *testing.T) {
	ts, f, l := newDownloadsServer(t)
	defer ts.hs.Close()
	f.Torrents = []testutil.Torrent{{ID: 9, Name: "Dune.2021.mkv", DownloadDir: ts.sv.cfg.ImportDir, Status: torrent.Downloading,
		PercentDone: 0.43, RateDown: 2 << 20, ETA: 720, QueuePosition: 0, FileCount: 3,
		Size: 4509715660, UploadRatio: 0.12, PeersSending: 3, PeersGetting: 1, Seeders: 40, Leechers: 12}}

	b := body(t, ts.do("GET", "/incoming", "", nil))
	for _, want := range []string{`placeholder="Paste a magnet link or a .torrent link"`, ">Add</button>",
		"Dune.2021.mkv", "1.8 GB of 4.2 GB via Transmission · 12 minutes remaining", `style="width:43%"`,
		// The details are folded away: the fold kept across refreshes, what's
		// beneath it replaced.
		`<details class="inc-info" id="info-dl-9" hx-preserve>`, "<dt>Ratio</dt><dd>0.12</dd>",
		"<dt>Seeds</dt><dd>3 of 40</dd>", "<dt>Leechers</dt><dd>1 of 12</dd>",
		// Two columns: the swarm, then this download.
		"<dl><dt>Seeds</dt><dd>3 of 40</dd><dt>Leechers</dt><dd>1 of 12</dd><dt>Ratio</dt><dd>0.12</dd></dl>",
		"<dl><dt>Speed</dt><dd>2.0 MB/sec</dd><dt>Content</dt><dd>4.2 GB in 3 files</dd></dl>",
		`hx-post="/ui/downloads/9/pause"`, `hx-delete="/ui/downloads/9"`, "Pause all"} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(b, "Import now") {
		t.Error("Import now with auto-import on")
	}

	r := ts.do("POST", "/ui/downloads", "source=magnet%3A%3Fxt%3Durn%3Abtih%3Ax%26dn%3DFirefly.S01E01.mkv",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if b := body(t, r); !strings.Contains(b, "Added Firefly.S01E01.mkv.") || r.Header.Get("HX-Trigger") != "download-added" {
		t.Errorf("add via page: %s", b)
	}
	if b := body(t, ts.do("POST", "/ui/downloads", "source=nope", map[string]string{"Content-Type": "application/x-www-form-urlencoded"})); !strings.Contains(b, `flash bad`) {
		t.Errorf("bad add should show an error: %s", b)
	}

	// The queue: dragged rows, each moved to just before another, or after the last.
	f.Torrents = []testutil.Torrent{
		{ID: 9, Name: "A", DownloadDir: l.Import, Status: torrent.Downloading, PercentDone: 0.1, QueuePosition: 0},
		{ID: 10, Name: "B", DownloadDir: l.Import, Status: torrent.Stopped, PercentDone: 0.1, QueuePosition: 1},
		{ID: 11, Name: "C", DownloadDir: l.Import, Status: torrent.Stopped, PercentDone: 0.2, QueuePosition: 2},
	}
	b = body(t, ts.do("GET", "/ui/incoming", "", nil))
	if !inOrder(b, `id="dl-9"`, `data-drag data-id="9"`, `id="dl-10"`, `hx-post="/ui/downloads/10/resume"`, `id="dl-11"`) ||
		!strings.Contains(b, "every 5s [!document.querySelector('#incoming.dragging')]") ||
		!strings.Contains(b, `id="inc-space" hx-swap-oob="true"`) {
		t.Errorf("queue:\n%s", b)
	}
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	body(t, ts.do("POST", "/ui/downloads/9/move", "after=11", form))
	body(t, ts.do("POST", "/ui/downloads/11/move", "before=10", form)) // 10, 11, 9 by now
	if len(f.Moves) != 2 || f.Moves[0] != [2]int{9, 2} || f.Moves[1] != [2]int{11, 0} {
		t.Errorf("moves: %v", f.Moves)
	}
	if b := body(t, ts.do("POST", "/ui/downloads/9/move", "before=99", form)); !strings.Contains(b, "flash bad") {
		t.Errorf("move nowhere: %s", b)
	}

	if b := body(t, ts.do("POST", "/ui/downloads/all/pause", "", nil)); !strings.Contains(b, "Resume all") {
		t.Errorf("pause all: %s", b)
	}
	if b := body(t, ts.do("DELETE", "/ui/downloads/10", "", nil)); !strings.Contains(b, "Canceled B") || len(f.Deleted) != 1 {
		t.Errorf("cancel via page: %s", b)
	}
	if b := body(t, ts.do("DELETE", "/ui/downloads/99", "", nil)); !strings.Contains(b, "flash bad") {
		t.Errorf("cancel nothing: %s", b)
	}

	// A finished download's 🗑: out of Transmission, its files to the trash.
	testutil.Write(t, filepath.Join(l.Import, "Seeded.2020", "Seeded.2020.mkv"), "x")
	f.Torrents = []testutil.Torrent{{ID: 12, Name: "Seeded.2020", DownloadDir: l.Import, Status: torrent.Seeding, PercentDone: 1, UploadRatio: 0.1}}
	ts.sv.cfg.SeedRatio = 1
	b = body(t, ts.do("GET", "/ui/incoming", "", nil))
	if !strings.Contains(b, "seeding, ratio 0.10 of 1.00") || !strings.Contains(b, `hx-post="/ui/downloads/12/trash"`) {
		t.Errorf("seeding row: %s", b)
	}
	if b := body(t, ts.do("POST", "/ui/downloads/12/trash", "", nil)); !strings.Contains(b, "Moved Seeded.2020 to the trash.") {
		t.Errorf("trash: %s", b)
	}
	testutil.MustNotExist(t, l.Import, "Seeded.2020")

	// Not set up: says how.
	ts.sv.cfg.Transmission = ""
	ts.sv.dl = downloadsState{}
	ts.sv.cfg.Uploads = false
	if b := body(t, ts.do("GET", "/incoming", "", nil)); !strings.Contains(b, "mvd setup transmission") {
		t.Errorf("unconfigured page: %s", b)
	}
}
