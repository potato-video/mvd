package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mvd/internal/config"
)

// Only one import at a time, whether from the command line or the server:
// two would try to move the same files. The lock is a file in data_dir
// holding the importer's process id and, on Linux, the boot's id, so a lock
// left by a crash or a power cut can be taken over: when it's from an
// earlier boot, or its process has gone or is now some other program (on
// Linux, where /proc says), or after a few hours anywhere. A router's
// process ids come round much the same after every boot, so the process
// being alive says little on its own.

const staleLock = 6 * time.Hour

// held is the locks this process holds, by path: a lock with our own id
// that isn't in it was left by an earlier process that had the same id.
var held sync.Map

func LockImport(cfg config.Config) (release func(), err error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(cfg.DataDir, "import.lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), bootID())
			f.Close()
			held.Store(p, true)
			return func() { held.Delete(p); os.Remove(p) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if !lockStale(p) {
			pid, _ := readLock(p)
			return nil, fmt.Errorf("an import is already running (process %d); if it isn't, delete %s", pid, p)
		}
		// Its owner has gone: take it over. Claim it by renaming it to a name
		// of our own — if two processes try at once, one rename fails, and the
		// loser's next O_EXCL create sees the winner's fresh lock. A plain
		// Remove here could delete the winner's new lock instead of the stale
		// one, and both would then "hold" the lock.
		claim := fmt.Sprintf("%s.stale.%d", p, os.Getpid())
		if os.Rename(p, claim) == nil {
			os.Remove(claim)
		}
	}
	return nil, errors.New("couldn't take the import lock")
}

func lockStale(p string) bool {
	st, err := os.Stat(p)
	if err != nil {
		return true
	}
	if time.Since(st.ModTime()) > staleLock {
		return true
	}
	pid, boot := readLock(p)
	if pid <= 0 {
		return true
	}
	if now := bootID(); boot != "" && now != "" && boot != now {
		return true // from before a reboot
	}
	if pid == os.Getpid() {
		_, ours := held.Load(p)
		return !ours
	}
	if Exists("/proc/self") { // Linux: we can see whether it's still running, and what it is
		comm, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
		if err != nil {
			return true
		}
		self, err := os.ReadFile("/proc/self/comm")
		return err == nil && string(comm) != string(self)
	}
	return false
}

// readLock is a lock file's process id and boot id (if it has one).
func readLock(p string) (pid int, boot string) {
	b, _ := os.ReadFile(p)
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, ""
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil {
		return 0, ""
	}
	if len(f) > 1 {
		boot = f[1]
	}
	return pid, boot
}

// bootID tells this boot from the last (Linux), or is "".
func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// OpenImportLog opens the import log for appending: every move goes in it.
func OpenImportLog(cfg config.Config) *os.File {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(cfg.DataDir, "import.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	return f
}
