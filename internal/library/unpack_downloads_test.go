//go:build downloads && unpack

package library

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"mvd/internal/testutil"
	"mvd/internal/torrent"
)

// A download still seeding whose videos are all in archives: they're
// unpacked and filed now, and the archives stay, seeding, until it's done.
func TestUnpackWhileSeeding(t *testing.T) {
	tmdbSrv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	rel := filepath.Join(l.Import, "Dune.2021.1080p-GRP")
	testutil.Write(t, filepath.Join(rel, "dune.nfo"), "release notes")
	testutil.WriteRAR(t, rel, "dune", true, false, 100, []testutil.ArchiveFile{
		{Name: "Dune.2021.1080p-GRP.mkv", Data: []byte(strings.Repeat("\x1a\x45\xdf\xa3 matroska", 20))},
	})
	// And one with a loose video too: that waits, as before.
	testutil.Write(t, filepath.Join(l.Import, "Fast.Five.2011/Fast.Five.2011.mkv"), "\x1a\x45\xdf\xa3")
	testutil.WriteRAR(t, filepath.Join(l.Import, "Fast.Five.2011"), "extras", true, false, 1<<20, []testutil.ArchiveFile{
		{Name: "Fast.Five.2011-featurette.mkv", Data: []byte("\x1a\x45\xdf\xa3")},
	})
	f := testutil.NewTransmission(false)
	f.Torrents = []testutil.Torrent{
		{ID: 1, Name: "Dune.2021.1080p-GRP", DownloadDir: l.Import, Status: torrent.Seeding, PercentDone: 1, UploadRatio: 0.2},
		{ID: 2, Name: "Fast.Five.2011", DownloadDir: l.Import, Status: torrent.Seeding, PercentDone: 1, UploadRatio: 0.2},
	}
	trSrv := f.Server(t)
	run := func() (*Importer, string) {
		s := newTestImporter(t, l, tmdbSrv, Options{}, "")
		s.cfg.Transmission, s.cfg.SeedRatio = trSrv.URL, 1.0
		var out bytes.Buffer
		s.out = &out
		s.Run()
		return s, out.String()
	}

	s, out := run()
	testutil.MustExist(t, l.Movies, "Dune (2021)/Dune (2021).mkv")
	testutil.MustExist(t, l.Import, "Dune.2021.1080p-GRP/dune.part01.rar", "Dune.2021.1080p-GRP/dune.nfo",
		"Fast.Five.2011/Fast.Five.2011.mkv", "Fast.Five.2011/extras.rar")
	testutil.MustNotExist(t, l.Import, "Fast.Five.2011/Fast.Five.2011-featurette.mkv")
	if len(f.Removed) != 0 || s.Stats().Seeding != 2 || s.Stats().Movies != 1 {
		t.Errorf("removed=%v seeding=%d movies=%d\n%s", f.Removed, s.Stats().Seeding, s.Stats().Movies, out)
	}

	// Again while still seeding: nothing to do, nothing unpacked twice.
	s, out = run()
	if s.Stats().Movies != 0 || s.Stats().Seeding != 2 || strings.Contains(out, "unpack") {
		t.Errorf("second run: movies=%d seeding=%d\n%s", s.Stats().Movies, s.Stats().Seeding, out)
	}

	// Done seeding: out of Transmission, and the archives to the trash.
	f.Torrents[0].UploadRatio = 1.5
	_, out = run()
	if len(f.Removed) != 1 || f.Removed[0] != 1 {
		t.Errorf("removed %v, want 1\n%s", f.Removed, out)
	}
	testutil.MustNotExist(t, l.Import, "Dune.2021.1080p-GRP")
}
