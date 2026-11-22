//go:build downloads

package main

import (
	"testing"

	"mvd/internal/config"
	"mvd/internal/testutil"
	"mvd/internal/torrent"
)

func TestTorrentClear(t *testing.T) {
	for _, keep := range []bool{false, true} {
		f := testutil.NewTransmission(true)
		f.Torrents = []testutil.Torrent{
			{ID: 1, Name: "done", PercentDone: 1, Status: torrent.Seeding},
			{ID: 2, Name: "half", PercentDone: 0.5, Status: torrent.Downloading},
			{ID: 3, Name: "none", PercentDone: 0, Status: torrent.Stopped},
		}
		tr := torrent.New(config.Config{Transmission: f.Server(t).URL})
		args := []string{"-y"}
		if keep {
			args = append(args, "-keep")
		}
		if rc := torrentClear(tr, args); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
		if len(f.Torrents) != 0 || len(f.Removed) != 3 {
			t.Errorf("keep=%v: left %v, removed %v", keep, f.Torrents, f.Removed)
		}
		wantDeleted := 2
		if keep {
			wantDeleted = 0
		}
		if len(f.Deleted) != wantDeleted {
			t.Errorf("keep=%v: deleted data of %v", keep, f.Deleted)
		}
		for _, id := range f.Deleted {
			if id == 1 {
				t.Errorf("deleted a finished download's files")
			}
		}
	}
	// Without -y and with no answer (stdin is empty under go test, as under
	// cron), it won't act.
	f := testutil.NewTransmission(false)
	f.Torrents = []testutil.Torrent{{ID: 1, Name: "x", PercentDone: 0.1}}
	tr := torrent.New(config.Config{Transmission: f.Server(t).URL})
	if rc := torrentClear(tr, nil); rc != 2 || len(f.Removed) != 0 {
		t.Errorf("cleared without asking: rc=%d removed=%v", rc, f.Removed)
	}
}

func TestTorrentResume(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		f := testutil.NewTransmission(legacy)
		f.Torrents = []testutil.Torrent{
			{ID: 1, Name: "Stuck.At.Zero", Status: torrent.Stopped, PercentDone: 0, RatioMode: 1, RatioLimit: 0},
			{ID: 2, Name: "Paused.By.Hand", Status: torrent.Stopped, PercentDone: 0.4},
			{ID: 3, Name: "Done.Seeding", Status: torrent.Stopped, PercentDone: 1, IsFinished: true},
			{ID: 4, Name: "Busy", Status: torrent.Downloading, PercentDone: 0.2},
			{ID: 5, Name: "Broken", Status: torrent.Stopped, PercentDone: 0.1, Error: "No data found"},
		}
		tr := torrent.New(config.Config{Transmission: f.Server(t).URL})

		// No names: every paused one, and only those.
		if rc := torrentResume(tr, nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
		if len(f.Started) != 2 || f.Started[0] != 1 || f.Started[1] != 2 {
			t.Errorf("legacy=%v: started %v, want [1 2]", legacy, f.Started)
		}
		if f.Modes[1] != 0 || len(f.Modes) != 1 {
			t.Errorf("legacy=%v: the zero limit wasn't handed back: %v", legacy, f.Modes)
		}

		// By name or ID, even one that isn't "paused" (asked for explicitly).
		f.Started = nil
		if rc := torrentResume(tr, []string{"done.seed", "5"}); rc != 0 || len(f.Started) != 2 {
			t.Errorf("legacy=%v: by name/ID started %v", legacy, f.Started)
		}
		if rc := torrentResume(tr, []string{"nothing-like-this"}); rc != 1 {
			t.Errorf("unknown name: rc=%d", rc)
		}
	}
}

func TestTorrentVerify(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		f := testutil.NewTransmission(legacy)
		f.Torrents = []testutil.Torrent{
			{ID: 1, Name: "Unreadable", Status: torrent.Stopped, PercentDone: 0.7, Error: "Please Verify Local Data! Piece #416 is corrupt."},
			{ID: 2, Name: "Paused.By.Hand", Status: torrent.Stopped, PercentDone: 0.4},
			{ID: 3, Name: "Busy", Status: torrent.Downloading, PercentDone: 0.2},
		}
		tr := torrent.New(config.Config{Transmission: f.Server(t).URL})

		// No names: those in error, checked and then started.
		if rc := torrentVerify(tr, nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
		if len(f.Verified) != 1 || f.Verified[0] != 1 || len(f.Started) != 1 || f.Started[0] != 1 {
			t.Errorf("legacy=%v: verified %v, started %v, want [1] and [1]", legacy, f.Verified, f.Started)
		}

		// By name or ID, any of them.
		f.Verified, f.Started = nil, nil
		if rc := torrentVerify(tr, []string{"paused", "3"}); rc != 0 || len(f.Verified) != 2 || len(f.Started) != 2 {
			t.Errorf("legacy=%v: by name/ID verified %v, started %v", legacy, f.Verified, f.Started)
		}
		if rc := torrentVerify(tr, []string{"nothing-like-this"}); rc != 1 {
			t.Errorf("unknown name: rc=%d", rc)
		}
	}
}

func TestTorrentPause(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		f := testutil.NewTransmission(legacy)
		f.Torrents = []testutil.Torrent{
			{ID: 1, Name: "Getting.It", Status: torrent.Downloading, PercentDone: 0.3},
			{ID: 2, Name: "Sharing.It", Status: torrent.Seeding, PercentDone: 1},
			{ID: 3, Name: "Already.Paused", Status: torrent.Stopped, PercentDone: 0.4},
			{ID: 4, Name: "Waiting", Status: torrent.DownloadWait, PercentDone: 0},
		}
		tr := torrent.New(config.Config{Transmission: f.Server(t).URL})

		// By name or ID.
		if rc := torrentPause(tr, []string{"sharing"}); rc != 0 || len(f.Stopped) != 1 || f.Stopped[0] != 2 {
			t.Errorf("legacy=%v: by name stopped %v (rc=%d)", legacy, f.Stopped, rc)
		}
		// No names: everything still going, and only that.
		f.Stopped = nil
		if rc := torrentPause(tr, nil); rc != 0 || len(f.Stopped) != 2 || f.Stopped[0] != 1 || f.Stopped[1] != 4 {
			t.Errorf("legacy=%v: pause all stopped %v, want [1 4] (rc=%d)", legacy, f.Stopped, rc)
		}
		if rc := torrentPause(tr, []string{"nothing-like-this"}); rc != 1 {
			t.Errorf("unknown name: rc=%d", rc)
		}
	}
}

func TestTorrentRemove(t *testing.T) {
	for _, keep := range []bool{false, true} {
		f := testutil.NewTransmission(false)
		f.Torrents = []testutil.Torrent{
			{ID: 1, Name: "Done.Seeding", PercentDone: 1, Status: torrent.Seeding},
			{ID: 2, Name: "Half.Done", PercentDone: 0.5, Status: torrent.Downloading},
			{ID: 3, Name: "Left.Alone", PercentDone: 0.2, Status: torrent.Downloading},
		}
		tr := torrent.New(config.Config{Transmission: f.Server(t).URL})
		args := []string{"-y", "done.seeding", "2"}
		if keep {
			args = append([]string{"-keep"}, args...)
		}
		if rc := torrentRemove(tr, args); rc != 0 {
			t.Fatalf("keep=%v: rc=%d", keep, rc)
		}
		if len(f.Torrents) != 1 || f.Torrents[0].ID != 3 {
			t.Errorf("keep=%v: left %v", keep, f.Torrents)
		}
		// The finished one keeps its files; the unfinished one loses its
		// partial data, unless -keep.
		if (!keep && (len(f.Deleted) != 1 || f.Deleted[0] != 2)) || (keep && len(f.Deleted) != 0) {
			t.Errorf("keep=%v: deleted the data of %v", keep, f.Deleted)
		}
	}

	f := testutil.NewTransmission(false)
	f.Torrents = []testutil.Torrent{{ID: 1, Name: "x", PercentDone: 0.1}}
	tr := torrent.New(config.Config{Transmission: f.Server(t).URL})
	// Without -y and with no answer, it won't act; with no names, it says how.
	if rc := torrentRemove(tr, []string{"x"}); rc != 2 || len(f.Removed) != 0 {
		t.Errorf("removed without asking: rc=%d removed=%v", rc, f.Removed)
	}
	if rc := torrentRemove(tr, []string{"-y"}); rc != 2 || len(f.Removed) != 0 {
		t.Errorf("no names: rc=%d removed=%v", rc, f.Removed)
	}
	if rc := torrentRemove(tr, []string{"-y", "nope"}); rc != 1 {
		t.Errorf("unknown name: rc=%d", rc)
	}
}
