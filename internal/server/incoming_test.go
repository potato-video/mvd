//go:build webui && downloads && files

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mvd/internal/testutil"
	"mvd/internal/torrent"
)

// Incoming, top to bottom: uploads, Transmission's queue, those seeding,
// those done, then what's in the import folder. What needs a person is in
// Needs you, not the list.
func TestIncomingOrder(t *testing.T) {
	ts, f, l := newDownloadsServer(t)
	defer ts.hs.Close()
	ts.sv.cfg.Uploads, ts.sv.cfg.SeedRatio = true, 1

	// An import that leaves something for a person.
	ts.uploadFile("Dune.mkv")
	if _, err := ts.sv.runImport(); err != nil {
		t.Fatal(err)
	}
	// An upload under way, a file copied in a while ago, and one arriving.
	uploadID := "0123456789abcdef"
	if err := os.MkdirAll(ts.sv.uploadDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ts.sv.writeRecord(uploadRecord{uploadMeta: uploadMeta{
		ID: uploadID, Name: "Heat.1995.mkv", Size: 5000000000, Started: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ts.sv.uploadDir(), uploadID+".part"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(l.Import, "Copied.In.2001.mkv"), "12345")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(l.Import, "Copied.In.2001.mkv"), old, old)
	testutil.Write(t, filepath.Join(l.Import, "movies", "Fresh.2020.mkv"), "12345")

	gb := int64(4509715660)
	f.Torrents = []testutil.Torrent{
		{ID: 1, Name: "Seeding.2019", DownloadDir: l.Import, Status: torrent.Seeding, PercentDone: 1, UploadRatio: 0.42, Size: gb, QueuePosition: 0},
		{ID: 2, Name: "Queued.2018", DownloadDir: l.Import, Status: torrent.DownloadWait, PercentDone: 0.1, Size: gb, QueuePosition: 2},
		{ID: 3, Name: "Batman.2022", DownloadDir: l.Import, Status: torrent.Downloading, PercentDone: 0.38, Size: gb,
			RateDown: 18 << 10, ETA: 720, QueuePosition: 1},
		{ID: 4, Name: "Done.2017", DownloadDir: l.Import, Status: torrent.Seeding, PercentDone: 1, IsFinished: true, Size: gb, QueuePosition: 3},
		{ID: 5, Name: "Broken.2016", DownloadDir: l.Import, Status: torrent.Stopped, PercentDone: 0.7, Size: gb,
			Error: "Please Verify Local Data! Piece #416 is corrupt.", QueuePosition: 4},
		{ID: 6, Name: "Paused.2015", DownloadDir: l.Import, Status: torrent.Stopped, PercentDone: 0.2, Size: gb, QueuePosition: 5},
	}

	v := ts.sv.incoming()
	var got []string
	for _, r := range v.Rows {
		got = append(got, r.Name+": "+r.Line)
	}
	want := []string{
		"Heat.1995.mkv: 4.7 GB from another computer · starting",
		"Batman.2022: 1.6 GB of 4.2 GB via Transmission · 12 minutes remaining",
		"Queued.2018: 430 MB of 4.2 GB via Transmission · waiting in queue",
		"Paused.2015: 860 MB of 4.2 GB via Transmission · paused",
		"Seeding.2019: 4.2 GB via Transmission · seeding, ratio 0.42 of 1.00",
		"Done.2017: 4.2 GB via Transmission · waiting for import",
		"Copied.In.2001.mkv: 1 KB · waiting for import",
		"Fresh.2020.mkv: 1 KB · still arriving",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if r := v.Rows[1]; !r.Drag || !r.Pause || !r.Cancel || r.Pct != 38 || r.Idle || !r.HasFacts() {
		t.Errorf("downloading: %+v", r)
	}
	if r := v.Rows[3]; !r.Resume || r.Pause {
		t.Errorf("paused: %+v", r)
	}
	if r := v.Rows[4]; r.Drag || r.Cancel || !r.Trash || r.Pct != -1 {
		t.Errorf("seeding: %+v", r)
	}
	if r := v.Rows[6]; r.Item != "Copied.In.2001.mkv" || !r.Trash {
		t.Errorf("waiting: %+v", r)
	}
	if v.Rows[7].Item != "movies/Fresh.2020.mkv" {
		t.Errorf("item in movies/: %q", v.Rows[7].Item)
	}
	if v.Going != 3 || v.Paused != 1 || !v.Torrents || !v.Uploads || v.Free == "" {
		t.Errorf("view: going %d, paused %d, %+v", v.Going, v.Paused, v)
	}

	var needs []string
	for _, n := range v.Needs {
		needs = append(needs, n.Name)
	}
	if strings.Join(needs, ",") != "Broken.2016,Dune.mkv" {
		t.Errorf("needs you: %v", needs)
	}
	if n := v.Needs[0]; n.Torrent != "5" || !strings.Contains(n.Problem, "Piece #416") {
		t.Errorf("problem: %+v", n)
	}
	if n := v.Needs[1]; n.Item != "Dune.mkv" || n.Left == nil || !n.Left.Answerable || n.Left.Kind != "movie" {
		t.Errorf("unsure: %+v", n)
	}

	// An upload with nothing new for a while has stopped.
	ts.sv.now = func() time.Time { return time.Now().Add(time.Hour) }
	if r := ts.sv.incoming().Rows[0]; !r.Stopped || !strings.HasSuffix(r.Line, "· stopped an hour ago") {
		t.Errorf("stopped upload: %+v", r)
	}
}

// Transmission out of reach is said once, above the list.
func TestIncomingTransmissionDown(t *testing.T) {
	ts, _, _ := newDownloadsServer(t)
	defer ts.hs.Close()
	ts.sv.cfg.Transmission = "http://127.0.0.1:1/transmission/rpc"
	ts.sv.dl = downloadsState{}
	v := ts.sv.incoming()
	if len(v.Banners) != 1 || !strings.HasPrefix(v.Banners[0], "Transmission can't be reached") {
		t.Errorf("banners: %q", v.Banners)
	}
}
