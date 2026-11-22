package torrent

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"mvd/internal/config"
)

// SetupAlpine is 'mvd setup transmission' for Alpine Linux: the commands to
// install transmission-daemon and point it at mvd's folders, filled in from
// cfg.
func SetupAlpine(cfg config.Config) string { return setupAlpine(cfg, "/proc/meminfo") }

// setupAlpine is SetupAlpine, sizing Transmission from the RAM in meminfo.
//
// Transmission runs as mvd's user (mvd), not its own: then what it downloads
// belongs to mvd, which moves it into the library and trashes what's left,
// with no shared group or umask to get right. Its settings go in a fresh
// settings.json written while it's stopped, since it rewrites the file when
// it stops; the daemon's flags can't set the queue or the cache.
func setupAlpine(cfg config.Config, meminfo string) string {
	imp := cfg.ImportDir
	if imp == "" {
		imp = "/mnt/drive/video/import"
	}
	base := filepath.Dir(imp)
	if cfg.VideoDir != "" {
		base = cfg.VideoDir
	}
	incomplete := filepath.Join(imp, ".downloading")
	state := filepath.Join(filepath.Dir(base), ".transmission")
	ratio := fmt.Sprintf("%g", cfg.SeedRatio)

	settings := []string{
		`"download-dir": "` + imp + `"`,
		`"incomplete-dir": "` + incomplete + `"`,
		`"incomplete-dir-enabled": true`,
		`"rpc-bind-address": "127.0.0.1"`,
	}
	notes := "# Finished downloads land in mvd's import folder; partial ones wait in a\n" +
		"# hidden folder inside it, which import skips. Only mvd (on this machine)\n" +
		"# talks to it, so its RPC listens on 127.0.0.1 alone.\n"
	if n := cfg.MaxDownloads; n > 0 {
		settings = append(settings, `"download-queue-enabled": true`, `"download-queue-size": `+strconv.Itoa(n))
		notes += "# " + strconv.Itoa(n) + " downloads at a time (max_downloads), the rest queued in turn.\n"
	}
	if cache, peers, ok := memSizes(meminfo); ok {
		settings = append(settings, `"cache-size-mb": `+strconv.Itoa(cache), `"peer-limit-global": `+strconv.Itoa(peers))
		notes += "# Sized from the RAM: its piece cache a 64th of it, and a peer for every 3MB.\n"
	}

	owners := ""
	switch fs := mountFS("/proc/mounts", imp); fs {
	case "exfat", "vfat", "msdos", "ntfs", "ntfs3", "fuseblk":
		owners = `
# The drive is ` + fs + `, which has no file owners: the mount decides who can
# write. Mount it as mvd, with uid= and gid= in /etc/fstab ('id mvd' gives
# them), or the chown below fails and neither mvd nor Transmission can write.
`
	}

	return `#!/bin/sh
# Transmission for mvd, on Alpine Linux. Read it through, then run it as root:
#   mvd setup transmission > setup.sh && sh setup.sh
# It runs Transmission as the user mvd runs as, mvd. On a diskless
# install, run 'lbu commit' afterwards to keep /etc/conf.d across reboots.
set -e
` + owners + `
apk add transmission-daemon
rc-service transmission-daemon stop 2>/dev/null || true

# Its own state, on the drive beside the library, and the folders it writes
# to, all mvd's, so mvd can move and trash what it downloads.
mkdir -p '` + state + `' '` + incomplete + `'
chown mvd:mvd '` + state + `' '` + imp + `' '` + incomplete + `'

cat > /etc/conf.d/transmission-daemon <<EOF
TRANSMISSION_OPTIONS="--encryption-preferred"
runas_user=mvd:mvd
config_dir='` + state + `'
logfile=syslog
EOF

` + notes + `# Transmission fills in the rest of its settings, and rewrites the file when
# it stops: change it only while it's stopped. An old one is kept as .old.
if [ -f '` + state + `/settings.json' ]; then mv '` + state + `/settings.json' '` + state + `/settings.json.old'; fi
cat > '` + state + `/settings.json' <<'EOF'
{
  ` + strings.Join(settings, ",\n  ") + `
}
EOF
chown mvd:mvd '` + state + `/settings.json'
# Optional: Transmission stops seeding by itself too (mvd sets ` + ratio + ` per
# torrent it adds): add "ratio-limit-enabled": true, "ratio-limit": ` + ratio + `.

rc-update add transmission-daemon default
rc-service transmission-daemon start

# Optional: let peers connect in (more peers, faster downloads). Transmission
# asks the router to forward its port (51413) by UPnP or NAT-PMP, if the
# router allows it; otherwise forward TCP and UDP 51413 to this machine.

# Then, in mvd's config.toml:
#
#   [downloads]
#   transmission = "http://127.0.0.1:9091/transmission/rpc"
#   seed_ratio   = ` + ratio + `
`
}
