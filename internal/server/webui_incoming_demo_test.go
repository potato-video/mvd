//go:build webui && downloads && files

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mvd/internal/library"
	"mvd/internal/testutil"
	"mvd/internal/torrent"
)

// MVD_DEMO=1 go test -tags 'webui downloads files' -run TestDemoIncoming serves an
// Incoming page with made-up downloads, uploads and things that need you on
// 127.0.0.1:18792 for MVD_DEMO seconds (or a minute), for screenshots.
func TestDemoIncoming(t *testing.T) {
	if os.Getenv("MVD_DEMO") == "" {
		t.Skip("set MVD_DEMO")
	}
	ts, f, l := newDownloadsServer(t)
	defer ts.hs.Close()
	ts.sv.cfg.Uploads, ts.sv.cfg.SeedRatio = true, 1

	// Left for a person: two Dunes to choose from, and no videos at all.
	ts.uploadFile("Dune.mkv")
	testutil.Write(t, filepath.Join(l.Import, "Album Art", "cover.jpg"), "x")
	then := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(l.Import, "Album Art", "cover.jpg"), then, then)
	if _, err := ts.sv.runImport(); err != nil {
		t.Fatal(err)
	}
	ts.startUpload(`{"name": "Season 2/S02E01.mkv", "size": 1500000000, "group": "0123456789abcdef", "group_files": 8}`, 201)
	ts.startUpload(`{"name": "Documentary.mp4", "size": 551550976}`, 201)
	testutil.Write(t, filepath.Join(l.Import, "Copied.In.By.Hand.2001.mkv"), "12345")
	os.Chtimes(filepath.Join(l.Import, "Copied.In.By.Hand.2001.mkv"), then, then)

	now := time.Now().Unix()
	f.Torrents = []testutil.Torrent{
		{ID: 1, Name: "The Batman (2022).mkv", DownloadDir: l.Import, Status: torrent.Downloading, PercentDone: 0.38, RateDown: 18 << 10, ETA: 720, Added: now - 600, Size: 2577 << 20, FileCount: 6, UploadRatio: 0.03, PeersSending: 14, PeersGetting: 2, Seeders: 312, Leechers: 41, QueuePosition: 0},
		{ID: 2, Name: "Severance.S02.2160p.ATVP.WEB-DL.DDP5.1.HDR.H.265", DownloadDir: l.Import, Status: torrent.DownloadWait, PercentDone: 0.0, Added: now - 1200, Size: 18 << 30, Seeders: 58, Leechers: 7, QueuePosition: 1},
		{ID: 3, Name: "Threads (1984)", DownloadDir: l.Import, Status: torrent.Seeding, PercentDone: 1, UploadRatio: 0.42, Added: now - 3*3600, Size: 2362 << 20, PeersGetting: 3, Seeders: 58, Leechers: 7, QueuePosition: 2},
		{ID: 5, Name: "Broken.Thing.2019", DownloadDir: l.Import, Status: torrent.Stopped, PercentDone: 0.2, Error: "No data found! Ensure your drives are connected", Added: now - 5*3600, Size: 700 << 20, Seeders: -1, Leechers: -1, QueuePosition: 3},
	}
	testutil.Write(t, filepath.Join(l.Trash, "2026-11-01 120000 Old.Thing", "a.nfo"), "x")
	ts.sv.imports.trashed(16<<30, false)
	go http.ListenAndServe("127.0.0.1:18792", ts.sv.routes())
	secs := 60
	if n := library.Atoi(os.Getenv("MVD_DEMO")); n > 1 {
		secs = n
	}
	time.Sleep(time.Duration(secs) * time.Second)
}
