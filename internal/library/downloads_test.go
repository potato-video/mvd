//go:build downloads

package library

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"mvd/internal/testutil"
	"mvd/internal/torrent"
)

func TestImportWithTorrents(t *testing.T) {
	tmdbSrv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	mkv := "\x1a\x45\xdf\xa3"
	// Three finished downloads in import/: one still seeding, one Transmission
	// has stopped at its ratio, one past mvd's ratio. And a file copied in
	// by hand, which has nothing to do with Transmission.
	testutil.Write(t, filepath.Join(l.Import, "Dune.2021.1080p/Dune.2021.1080p.mkv"), mkv)
	testutil.Write(t, filepath.Join(l.Import, "Fast.Five.2011.mkv"), mkv)
	testutil.Write(t, filepath.Join(l.Import, "tv/Severance.S01E01.mkv"), mkv)
	testutil.Write(t, filepath.Join(l.Import, "Children.of.Men.2006.mkv"), mkv)

	f := testutil.NewTransmission(false)
	f.Torrents = []testutil.Torrent{
		{ID: 1, Name: "Dune.2021.1080p", DownloadDir: l.Import, Status: torrent.Seeding, PercentDone: 1, UploadRatio: 0.3},
		{ID: 2, Name: "Fast.Five.2011.mkv", DownloadDir: l.Import, Status: torrent.Stopped, PercentDone: 1, UploadRatio: 0.1, IsFinished: true},
		{ID: 3, Name: "Severance.S01E01.mkv", DownloadDir: filepath.Join(l.Import, "tv"), Status: torrent.Seeding, PercentDone: 1, UploadRatio: 1.4},
		{ID: 4, Name: "Unrelated", DownloadDir: "/elsewhere", Status: torrent.Seeding, PercentDone: 1, UploadRatio: 9},
	}
	trSrv := f.Server(t)

	s := newTestImporter(t, l, tmdbSrv, Options{}, "")
	s.cfg.Transmission, s.cfg.SeedRatio = trSrv.URL, 1.0
	s.settle = 3600e9 // only Transmission's word should let its downloads through
	s.Run()

	if s.Stats().Seeding != 1 || s.Stats().Movies != 1 || s.Stats().Episodes != 1 {
		t.Errorf("seeding=%d movies=%d eps=%d, want 1, 1, 1", s.Stats().Seeding, s.Stats().Movies, s.Stats().Episodes)
	}
	testutil.MustExist(t, l.Movies, "Fast Five (2011)/Fast Five (2011).mkv")
	testutil.MustExist(t, l.TV, "Severance (2022)/Season 01/Severance (2022) - S01E01 - Good News About Hell.mkv")
	testutil.MustExist(t, l.Import, "Dune.2021.1080p", "Children.of.Men.2006.mkv") // seeding; just copied in
	if len(f.Removed) != 2 || f.Removed[0] == 1 || f.Removed[1] == 1 {
		t.Errorf("removed from Transmission: %v, want 2 and 3", f.Removed)
	}

	// Later, Dune reaches its ratio.
	f.Torrents[0].UploadRatio = 1.0
	s = newTestImporter(t, l, tmdbSrv, Options{}, "")
	s.cfg.Transmission, s.cfg.SeedRatio = trSrv.URL, 1.0
	s.Run()
	testutil.MustExist(t, l.Movies, "Dune (2021)/Dune (2021).mkv",
		"Children of Men (2006)/Children of Men (2006).mkv") // settled by now

	// Transmission down: import carries on, saying so.
	testutil.Write(t, filepath.Join(l.Import, "Boogie.Nights.1997.mkv"), mkv)
	s = newTestImporter(t, l, tmdbSrv, Options{}, "")
	var out bytes.Buffer
	s.out = &out
	s.cfg.Transmission = "http://127.0.0.1:1/transmission/rpc"
	s.Run()
	if !strings.Contains(out.String(), "carrying on without it") || s.Stats().Movies != 1 {
		t.Errorf("without Transmission: movies=%d\n%s", s.Stats().Movies, out.String())
	}
}

// Review fix 5: a torrent leaves Transmission only once something from it
// has been filed.
func TestTorrentRemovedOnlyWhenFiled(t *testing.T) {
	tmdbSrv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	testutil.Write(t, filepath.Join(l.Import, "Unmatchable Thing/thing.mkv"), "\x1a\x45\xdf\xa3")
	testutil.Write(t, filepath.Join(l.Import, "Archives/a.rar"), "Rar!")
	testutil.Write(t, filepath.Join(l.Import, "Fast.Five.2011.mkv"), "\x1a\x45\xdf\xa3")
	f := testutil.NewTransmission(false)
	f.Torrents = []testutil.Torrent{
		{ID: 1, Name: "Unmatchable Thing", DownloadDir: l.Import, Status: torrent.Seeding, PercentDone: 1},
		{ID: 2, Name: "Archives", DownloadDir: l.Import, Status: torrent.Seeding, PercentDone: 1},
		{ID: 3, Name: "Fast.Five.2011.mkv", DownloadDir: l.Import, Status: torrent.Seeding, PercentDone: 1},
	}
	s := newTestImporter(t, l, tmdbSrv, Options{}, "")
	s.cfg.Transmission, s.cfg.SeedRatio = f.Server(t).URL, 0
	s.Run()
	if len(f.Removed) != 1 || f.Removed[0] != 3 {
		t.Errorf("removed %v, want only 3 (the one filed)", f.Removed)
	}
}
