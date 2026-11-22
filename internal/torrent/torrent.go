package torrent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"mvd/internal/config"
)

func IsLink(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(l, "magnet:") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// ImportDir is where a download should finish up: the import folder, or
// its movies/ or tv/ folder when the type is known.
func ImportDir(cfg config.Config, kind string) string {
	switch kind {
	case "movie":
		return filepath.Join(cfg.ImportDir, "movies")
	case "tv":
		return filepath.Join(cfg.ImportDir, "tv")
	}
	return cfg.ImportDir
}

// Add hands a download to Transmission, set to finish in the import
// folder and to stop seeding at seed_ratio. warn is for things the user
// should know but that didn't stop it.
func Add(cfg config.Config, tr *Client, source string, file []byte, kind string) (Torrent, bool, string, error) {
	if cfg.ImportDir == "" {
		return Torrent{}, false, "", errors.New("no import folder: set video_dir or import_dir under [paths] in config.toml")
	}
	if file == nil && !IsLink(source) {
		return Torrent{}, false, "", errors.New("that isn't a magnet link or a link to a .torrent file")
	}
	dir := ImportDir(cfg, kind)
	os.MkdirAll(dir, 0o777)

	warn := ""
	if def, err := tr.DownloadDir(); err == nil && def != "" && !within(filepath.Clean(def), dir) {
		warn = fmt.Sprintf("Transmission's download_dir is %s; on OpenWRT it may only be able to write there, "+
			"so set it to %s ('mvd setup transmission' shows how)", def, cfg.ImportDir)
	}
	// What's there already, to tell the new one apart if Transmission is
	// too slow to say which it is.
	before := map[int]bool{}
	ts, listErr := tr.Torrents()
	for _, x := range ts {
		before[x.ID] = true
	}
	start := time.Now()
	t, dup, err := tr.Add(strings.TrimSpace(source), file, dir)
	if isNoAnswer(err) && listErr == nil {
		return t, false, warn, &StillAdding{finish: func() (Torrent, error) {
			return settle(cfg, tr, before, start)
		}}
	}
	if err != nil {
		return t, dup, warn, err
	}
	if !dup {
		if err := setRatio(cfg, tr, t); err != nil && warn == "" {
			warn = "couldn't set its seed ratio: " + err.Error()
		}
	}
	return t, dup, warn, nil
}

// setRatio gives a new torrent the configured seed ratio. Only a real
// ratio: a limit of 0 can make Transmission think a fresh torrent has
// already done its seeding and stop it at once. At 0 there's nothing to set
// anyway, since import takes it as soon as it's complete.
func setRatio(cfg config.Config, tr *Client, t Torrent) error {
	if cfg.SeedRatio > 0 {
		return tr.SetSeedRatio(t.ID, cfg.SeedRatio)
	}
	return nil
}

// StillAdding is Add's answer when Transmission took too long to reply. It
// has usually done it all the same: setting up a big download's files on a
// slow drive (exFAT can't make sparse files, so it writes the full size
// first) holds up its answer, not the add. Finish waits for the torrent to
// appear, then does what Add would have.
type StillAdding struct{ finish func() (Torrent, error) }

func (e *StillAdding) Error() string {
	return "Transmission is taking a while to add it (setting up its files, probably); it'll appear here shortly"
}

func (e *StillAdding) Finish() (Torrent, error) { return e.finish() }

// How often, and how long, settle looks for a slow add.
var (
	settleEvery = 5 * time.Second
	settleFor   = 15 * time.Minute
)

// settle waits for a torrent that wasn't there before the add, starts it if
// it's stopped (an older Transmission can ignore "paused": false, and a
// timed-out add has been seen to leave one stopped) and sets its ratio.
func settle(cfg config.Config, tr *Client, before map[int]bool, start time.Time) (Torrent, error) {
	for deadline := time.Now().Add(settleFor); time.Now().Before(deadline); time.Sleep(settleEvery) {
		ts, err := tr.Torrents()
		if err != nil {
			continue // still busy
		}
		for _, t := range ts {
			if before[t.ID] {
				continue
			}
			if t.Paused() {
				if err := tr.Resume([]Torrent{t}); err != nil {
					return t, fmt.Errorf("added %s, but couldn't start it: %v", t.Name, err)
				}
			}
			if err := setRatio(cfg, tr, t); err != nil {
				return t, fmt.Errorf("added %s, but couldn't set its seed ratio: %v", t.Name, err)
			}
			return t, nil
		}
	}
	return Torrent{}, fmt.Errorf("Transmission never showed the torrent added at %s", start.Format("15:04"))
}

// State puts a torrent's state into words.
func State(t Torrent, seedRatio float64) (string, bool) {
	ok, why := t.Ready(seedRatio)
	switch {
	case t.Error != "":
		s := "Problem: " + t.Error
		if !strings.ContainsAny(s[len(s)-1:], ".!?") {
			s += "."
		}
		return s, false
	case t.PercentDone >= 1 && ok:
		return "Ready to import", true
	case t.Status == CheckWait || t.Status == Checking:
		return "Checking files", false
	case t.Status == DownloadWait:
		return fmt.Sprintf("Queued, %.0f%%", t.PercentDone*100), false
	case t.Status == Stopped && t.PercentDone < 1:
		return fmt.Sprintf("Paused, %.0f%%", t.PercentDone*100), false
	case t.Status == Downloading:
		s := fmt.Sprintf("Downloading, %.0f%%", t.PercentDone*100)
		if t.RateDown > 0 {
			s += ", " + rate(t.RateDown)
		}
		if t.ETA > 0 {
			s += ", " + eta(t.ETA) + " left"
		}
		return s, false
	}
	return strings.ToUpper(why[:1]) + why[1:], false
}

// A Fact is one of a torrent's numbers, labelled: Size, 4.2 GB.
type Fact struct{ Label, Value string }

// Facts are a torrent's size, ratio, and peers. Going, the seeds and
// leechers are those connected "of" the swarm's (when the trackers know
// it). Stopped, nobody's connected, so the swarm alone; finished, it gets
// from no one, so the same for its seeds.
func Facts(t Torrent) []Fact {
	var fs []Fact
	if t.Size > 0 {
		fs = append(fs, Fact{"Size", size(t.Size)})
	}
	if t.UploadRatio == -2 { // Transmission's "infinite": shared without downloading
		fs = append(fs, Fact{"Ratio", "∞"})
	} else { // -1 is "none yet"
		fs = append(fs, Fact{"Ratio", fmt.Sprintf("%.2f", max(t.UploadRatio, 0))})
	}
	peers := func(label string, now, swarm int, connected bool) {
		switch {
		case connected && swarm >= 0:
			fs = append(fs, Fact{label, fmt.Sprintf("%d of %d", now, swarm)})
		case connected:
			fs = append(fs, Fact{label, fmt.Sprint(now)})
		case swarm >= 0:
			fs = append(fs, Fact{label, fmt.Sprint(swarm)})
		}
	}
	going := t.Status != Stopped
	peers("Seeds", t.PeersSending, t.Seeders, going && t.PercentDone < 1)
	peers("Leechers", t.PeersGetting, t.Leechers, going)
	return fs
}

// Details are the Facts on one line: "4.2 GB · ratio 0.12 · seeds 3 of 40
// · leechers 1 of 12".
func Details(t Torrent) string {
	var parts []string
	for _, f := range Facts(t) {
		if f.Label == "Size" {
			parts = append(parts, f.Value)
		} else {
			parts = append(parts, strings.ToLower(f.Label)+" "+f.Value)
		}
	}
	return strings.Join(parts, " · ")
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}

func rate(bps int64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", float64(bps)/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0f kB/s", float64(bps)/(1<<10))
	}
	return fmt.Sprintf("%d B/s", bps)
}

func eta(sec int64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%.0f days", d.Hours()/24)
	case d >= time.Hour && int(d.Minutes())%60 == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return "under a minute"
}

// Setup prints the router-side setup, filled in from mvd's
// config. mvd doesn't run these itself: they change the router's config.
func Setup(cfg config.Config) string {
	imp := cfg.ImportDir
	if imp == "" {
		imp = "/mnt/drive/video/import"
	}
	base := filepath.Dir(imp)
	if cfg.VideoDir != "" {
		base = cfg.VideoDir
	}
	// Inside the import folder, not beside it: OpenWRT runs Transmission in
	// a jail that mounts download_dir but not incomplete_dir. Import skips
	// hidden folders.
	incomplete := filepath.Join(imp, ".downloading")
	state := filepath.Join(filepath.Dir(base), ".transmission")
	ratio := fmt.Sprintf("%g", cfg.SeedRatio)
	return `#!/bin/sh
# Transmission for mvd, on OpenWRT. Read it through, then run it on the router:
#   mvd setup transmission > setup.sh && sh setup.sh
# (On OpenWRT 25 and later, 'apk add' replaces 'opkg install'.)
set -e

opkg update && opkg install transmission-daemon

# Some feeds (GL.iNet's 22.03) build it against mbedTLS without depending on
# it, so it can't start ("Error relocating ... symbol not found" in the log).
# Check every library it needs is there, and add mbedTLS if that's the gap.
missing() { /lib/ld-musl-*.so.1 --list /usr/bin/transmission-daemon 2>&1 | grep 'Error loading'; }
if missing | grep -q libmbedcrypto; then opkg install libmbedtls12 || opkg install libmbedtls; fi
if missing; then echo "transmission-daemon is missing the libraries above; install them and run this again" >&2; exit 1; fi

# Its seccomp filter (22.03's, at least) leaves out syscalls it makes on
# 32-bit MIPS, so it's killed, silently: ftruncate64, to preallocate each new
# file (every few minutes), and geteuid, getgid and getegid, now and then (the
# filter has only the …32 names, which MIPS doesn't have). Allow them. (An
# upgrade puts the file back: run this again after one.)
F=/etc/seccomp/transmission-daemon.json
if [ -f $F ]; then
	for s in ftruncate ftruncate64 geteuid getgid getegid; do
		grep -q "\"$s\"" $F || sed -i "s/\"fsync\",/\"fsync\", \"$s\",/" $F
		grep -q "\"$s\"" $F || echo "couldn't add $s to $F: add it by hand" >&2
	done
fi

T=transmission.@transmission[0]
uci set $T.enabled='1'
# Its own state, on the drive: the default (/tmp) is RAM, lost at every reboot.
uci set $T.config_dir='` + state + `'
# Finished downloads land in mvd's import folder; partial ones wait in a
# hidden folder inside it, which import skips. (Inside, because the jail
# Transmission runs in only mounts download_dir.)
uci set $T.download_dir='` + imp + `'
uci set $T.incomplete_dir='` + incomplete + `'
uci set $T.incomplete_dir_enabled='1'
` + queue(cfg.MaxDownloads) + memory("/proc/meminfo") + `# Only mvd (on this router) needs to talk to it.
uci set $T.rpc_bind_address='127.0.0.1'
# Optional: Transmission stops seeding by itself too (mvd sets ` + ratio + ` per torrent it adds).
#uci set $T.ratio_limit_enabled='1'
#uci set $T.ratio_limit='` + ratio + `'
# Optional: stop seeding anything nobody has downloaded from for an hour.
#uci set $T.idle_seeding_limit_enabled='1'
#uci set $T.idle_seeding_limit='60'
` + runAs(imp) + `uci commit transmission
/etc/init.d/transmission enable
/etc/init.d/transmission restart

# Optional: let peers connect in (more peers, faster downloads). UPnP doesn't
# apply here: Transmission is on the firewall itself.
#uci add firewall rule
#uci set firewall.@rule[-1].name='Transmission'
#uci set firewall.@rule[-1].src='wan'
#uci set firewall.@rule[-1].dest_port='51413'
#uci set firewall.@rule[-1].proto='tcp udp'
#uci set firewall.@rule[-1].target='ACCEPT'
#uci commit firewall && /etc/init.d/firewall reload

# Then, in ~/.config/mvd/config.toml:
#
#   [downloads]
#   transmission = "http://127.0.0.1:9091/transmission/rpc"
#   seed_ratio   = ` + ratio + `
`
}

// queue is the setup's download queue lines: at most n downloading at once
// ([downloads] max_downloads, 2 by default), or with 0, Transmission's own.
func queue(n int) string {
	if n > 0 {
		return `# ` + strconv.Itoa(n) + ` downloads at a time (max_downloads), the rest queued in turn (mvd's
# Incoming page can reorder them): on a router, fewer at once finish sooner, one
# after another, than many crawling along together, and need less memory.
uci set $T.download_queue_enabled='1'
uci set $T.download_queue_size='` + strconv.Itoa(n) + `'
`
	}
	return `# Transmission's own download queue (max_downloads = 0 in config.toml).
`
}

// memory is the setup's lines sized from the RAM, read from a /proc/meminfo-
// style table, so Transmission fits beside mvd: a 64th of it for the piece
// cache (2MB on a 128MB router), and a peer for every 3MB, 20 to 200 (39
// there), since each connection has its own buffers; as many for each torrent,
// at most 60, commented out. Nothing if it can't tell.
func memory(table string) string {
	cache, peers, ok := memSizes(table)
	if !ok {
		return ""
	}
	return `# Sized from the RAM, so it fits beside mvd: its piece cache a 64th of it,
# and a peer for every 3MB (each connection has its own buffers).
uci set $T.cache_size_mb='` + strconv.Itoa(cache) + `'
uci set $T.peer_limit_global='` + strconv.Itoa(peers) + `'
# Optional: as many for each torrent (up to its default, 60), so one torrent
# can't take them all.
#uci set $T.peer_limit_per_torrent='` + strconv.Itoa(min(60, peers)) + `'
`
}

// memSizes is Transmission's piece cache in MB and its global peer limit,
// sized from the RAM in a /proc/meminfo-style table (see memory). ok is
// false if it can't tell.
func memSizes(table string) (cache, peers int, ok bool) {
	b, err := os.ReadFile(table)
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "MemTotal:" {
			continue
		}
		kb, err := strconv.Atoi(f[1])
		if err != nil {
			return 0, 0, false
		}
		return max(1, (kb+32<<10)/(64<<10)), min(200, max(20, kb/1024/3)), true
	}
	return 0, 0, false
}

// runAs is the setup's user lines. Transmission runs as its own user, but on
// a drive with no file ownership (exFAT, FAT, NTFS: the usual USB drive) the
// mount decides who can write, normally root alone, and its start-up script's
// chowns fail. Running it as root is the simple way round on a router.
func runAs(dir string) string {
	fs := mountFS("/proc/mounts", dir)
	switch fs {
	case "exfat", "vfat", "msdos", "ntfs", "ntfs3", "fuseblk":
		return `# The drive is ` + fs + `, which has no file owners: only the user the mount
# names (normally root) can write to it, so Transmission runs as root.
uci set $T.user='root'
uci set $T.group='root'
`
	}
	return `# Transmission runs as user 'transmission' (uid 224). If it can't write to the
# drive (permission errors in 'logread -e transmission'), run it as root:
#uci set $T.user='root'
#uci set $T.group='root'
`
}

// mountFS is the filesystem type of the mount holding dir, from a
// /proc/mounts-style table, or "" if it can't tell.
func mountFS(table, dir string) string {
	b, err := os.ReadFile(table)
	if err != nil {
		return ""
	}
	best, fs := -1, ""
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		mp := strings.ReplaceAll(f[1], `\040`, " ")
		if (mp == "/" || within(mp, dir)) && len(mp) >= best { // later mounts sit on top
			best, fs = len(mp), f[2]
		}
	}
	return fs
}

// Discard takes torrents out of Transmission. Finished ones keep their files
// (in the import folder, the next import takes them); unfinished ones lose
// their partial data, unless keepPartial.
func Discard(tr *Client, ts []Torrent, keepPartial bool) error {
	var done, partial []int
	for _, t := range ts {
		if t.PercentDone >= 1 {
			done = append(done, t.ID)
		} else {
			partial = append(partial, t.ID)
		}
	}
	if err := tr.RemoveTorrents(done, false); err != nil {
		return err
	}
	return tr.RemoveTorrents(partial, !keepPartial)
}

// InQueueOrder sorts torrents as Transmission's queue has them: the first
// to start (usually the oldest) first.
func InQueueOrder(ts []Torrent) {
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].QueuePosition != ts[j].QueuePosition {
			return ts[i].QueuePosition < ts[j].QueuePosition
		}
		return ts[i].Added < ts[j].Added
	})
}

// Pick chooses torrents by hash, ID or part of their name; with none
// given, those that match keep. A hash or a number that is some torrent's
// ID means that torrent alone, not every name with those digits in it.
func Pick(ts []Torrent, args []string, keep func(Torrent) bool) ([]Torrent, error) {
	if len(args) == 0 {
		var out []Torrent
		for _, t := range ts {
			if keep(t) {
				out = append(out, t)
			}
		}
		return out, nil
	}
	var out []Torrent
	seen := map[int]bool{}
	for _, a := range args {
		byID, byHash := false, false
		for _, t := range ts {
			if t.Hash != "" && strings.EqualFold(t.Hash, a) {
				byHash = true
			}
		}
		if id, err := strconv.Atoi(a); err == nil && !byHash {
			for _, t := range ts {
				if t.ID == id {
					byID = true
				}
			}
		}
		found := false
		for _, t := range ts {
			match := strconv.Itoa(t.ID) == a
			switch {
			case byHash:
				match = strings.EqualFold(t.Hash, a)
			case !byID:
				match = strings.Contains(strings.ToLower(t.Name), strings.ToLower(a))
			}
			if match {
				if !seen[t.ID] {
					seen[t.ID] = true
					out = append(out, t)
				}
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("no download matches %q ('mvd torrent list' shows them)", a)
		}
	}
	return out, nil
}
