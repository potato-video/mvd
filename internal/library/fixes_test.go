package library

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mvd/internal/config"
	"mvd/internal/testutil"
)

// Review fixes 2 and 3: the trash only ever deletes its own entries, and
// won't sit over the library.
func TestTrashSafety(t *testing.T) {
	dir := t.TempDir()
	testutil.Write(t, filepath.Join(dir, "movies/Dune (2021)/Dune (2021).mkv"), "x")
	old := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(filepath.Join(dir, "movies"), old, old)
	stamp := old.Format(trashStamp)
	testutil.Write(t, filepath.Join(dir, stamp+" Old leftovers", "junk.txt"), "x")
	if n := purgeTrash(dir, 7*24*time.Hour, 2<<30, time.Now()); n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	if n := purgeTrash(dir, 0, 0, time.Now()); n != 0 || !Exists(filepath.Join(dir, "movies/Dune (2021)/Dune (2021).mkv")) {
		t.Errorf("purged something that wasn't mvd's")
	}

	video := t.TempDir()
	cfg := config.Config{VideoDir: video, MoviesDir: filepath.Join(video, "movies"), TVDir: filepath.Join(video, "tv")}
	imp := filepath.Join(video, "import")
	for trash, ok := range map[string]bool{
		filepath.Join(video, ".trash"):           true,
		"/somewhere/else/trash":                  true,
		filepath.Dir(video):                      false, // contains the library
		video:                                    false,
		filepath.Join(video, "movies", ".trash"): false, // inside the library
		filepath.Join(imp, ".trash"):             false,
	} {
		cfg.TrashDir = trash
		if err := CheckTrashDir(cfg, imp); (err == nil) != ok {
			t.Errorf("trash_dir %s: %v", trash, err)
		}
	}

	// A trash that can't be moved into leaves the leftovers alone.
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	testutil.Write(t, filepath.Join(l.Import, "Fast.Five.2011.1080p/Fast.Five.2011.1080p.mkv"), "\x1a\x45\xdf\xa3")
	testutil.Write(t, filepath.Join(l.Import, "Fast.Five.2011.1080p/junk.txt"), "x")
	testutil.Write(t, l.Trash, "a file where the trash folder should be")
	s := newTestImporter(t, l, srv, Options{}, "")
	s.Run()
	testutil.MustExist(t, l.Movies, "Fast Five (2011)/Fast Five (2011).mkv")
	testutil.MustExist(t, l.Import, "Fast.Five.2011.1080p/junk.txt")
}

// Review fixes 4 and 7: what counts as an extra, and as an archive.
func TestImportClassification(t *testing.T) {
	root := t.TempDir()
	mkv := "\x1a\x45\xdf\xa3"
	testutil.Write(t, filepath.Join(root, "The Big Short/The.Big.Short.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "The Big Short/The.Big.Short-trailer.mkv"), mkv)
	p := planItem(filepath.Join(root, "The Big Short"), true, "")
	if len(p.movies) != 1 || len(p.movies[0].extras) != 1 || len(p.unknown) != 0 {
		t.Errorf("Big Short: movies=%d unknown=%v", len(p.movies), p.unknown)
	}
	testutil.Write(t, filepath.Join(root, "Show.S01E01.101"), mkv)
	testutil.Write(t, filepath.Join(root, "Pack/release.r00"), "Rar!")
	if p := planItem(filepath.Join(root, "Show.S01E01.101"), false, ""); len(p.videos) != 1 || p.archives {
		t.Errorf("extensionless video taken for an archive: %+v", p)
	}
	if p := planItem(filepath.Join(root, "Pack"), true, ""); len(p.videos) != 0 || !p.archives {
		t.Errorf("archive not recognised: %+v", p)
	}
}

// Review fix 6: one import at a time, across processes.
func TestImportLock(t *testing.T) {
	cfg := config.Config{DataDir: t.TempDir()}
	release, err := LockImport(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockImport(cfg); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second lock: %v", err)
	}
	release()
	if r, err := LockImport(cfg); err != nil {
		t.Errorf("after release: %v", err)
	} else {
		r()
	}
	// A lock left by a process that has gone is taken over.
	if Exists("/proc/self") {
		testutil.Write(t, filepath.Join(cfg.DataDir, "import.lock"), strconv.Itoa(1<<22-3)+"\n")
		if r, err := LockImport(cfg); err != nil {
			t.Errorf("stale lock: %v", err)
		} else {
			r()
		}
	}
}

// A failed two-step rename (case-only moves on exFAT go old → tmp → new)
// must put the file back under its old name, not leave it as a .mvdtmp
// that every scan skips.
func TestRenameTwoStepRestore(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "movies")
	testutil.Write(t, old, "x")
	linked := filepath.Join(root, "Movies")
	if err := os.Link(old, linked); err != nil {
		t.Skip(err)
	}
	renameOS = func(string, string) error { return errors.New("I/O error") }
	defer func() { renameOS = os.Rename }()
	s := &filer{root: root}
	if err := s.rename(old, linked); err == nil {
		t.Fatal("the forced failure didn't surface")
	}
	if !Exists(old) || Exists(old+".mvdtmp") {
		t.Errorf("file not restored to its old name: %v", testutil.Tree(root))
	}
}

// Two processes taking over the same stale lock: only one may win. The loser's
// rename-claim fails, and its retry sees the winner's fresh lock.
func TestLockTakeoverClaim(t *testing.T) {
	cfg := config.Config{DataDir: t.TempDir()}
	p := filepath.Join(cfg.DataDir, "import.lock")
	testutil.Write(t, p, "4194301\n") // a pid that's gone
	old := time.Now().Add(-24 * time.Hour)
	os.Chtimes(p, old, old)
	release, err := LockImport(cfg)
	if err != nil {
		t.Fatalf("takeover failed: %v", err)
	}
	defer release()
	// The lock is now fresh and ours: another taker must be refused, and
	// must not have removed our lock on the way out.
	if _, err := LockImport(cfg); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second taker: %v", err)
	}
	if !Exists(p) {
		t.Errorf("the refused taker removed the fresh lock")
	}
}

// Review M4: after a power cut, the lock's process id may well be alive
// again as something else, or as this very server. Neither holds the lock.
func TestStaleLockWhosePIDIsInUse(t *testing.T) {
	if !Exists("/proc/self") {
		t.Skip("needs /proc")
	}
	cfg := config.Config{DataDir: t.TempDir()}
	p := filepath.Join(cfg.DataDir, "import.lock")
	take := func(what string) {
		t.Helper()
		r, err := LockImport(cfg)
		if err != nil {
			t.Errorf("%s: %v", what, err)
			return
		}
		r()
	}
	// This process's id, but this process never took it.
	testutil.Write(t, p, strconv.Itoa(os.Getpid())+"\n")
	take("our own pid")
	// Another program's (pid 1 is init, never mvd).
	testutil.Write(t, p, "1\n")
	take("another program's pid")
	// Taken in an earlier boot.
	if bootID() != "" {
		testutil.Write(t, p, strconv.Itoa(os.Getpid())+" not-this-boot\n")
		if !lockStale(p) {
			t.Error("a lock from an earlier boot isn't stale")
		}
	}
}
