#!/bin/sh
# Installs mvd from its latest release, or updates it: fetches the build for
# this machine, asks where the videos are and for a TMDB key, writes the
# config, and sets mvd up as a service (OpenWRT, Alpine and other OpenRC
# systems, and Linux with systemd).
#
#   wget -O install.sh https://potato.video/install.sh && sh install.sh
#
# Run again later, it updates mvd and leaves the config alone. It's the
# setup guides' steps (docs/SETUP_OPENWRT.md, docs/SETUP_ALPINE.md), so read
# those to do any of it by hand.
#
# To answer ahead of time (nothing is asked when all three are set):
#   MVD_VIDEO_DIR   the library folder
#   MVD_TMDB_KEY    the TMDB key; empty to add it later
#   MVD_YES=1       don't ask before going ahead
# And MVD_RELEASE_URL: where the builds are, instead of the latest release.

set -eu

RELEASE=${MVD_RELEASE_URL:-https://github.com/potato-video/mvd/releases/latest/download}

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# Questions are read from the terminal, so "wget -O- ... | sh" works too.
if [ -t 0 ]; then
	exec 3<&0
elif (: </dev/tty) 2>/dev/null; then
	exec 3</dev/tty
else
	exec 3</dev/null
fi

# ask VAR QUESTION DEFAULT
ask() {
	if [ -n "$3" ]; then printf '%s [%s]: ' "$2" "$3"; else printf '%s: ' "$2"; fi
	answer=
	read -r answer <&3 || { say; die "no terminal to ask at: see the top of this script for answering ahead"; }
	[ -n "$answer" ] || answer=$3
	eval "$1=\$answer"
}

fetch() {
	if have curl; then
		curl -fsSL -o "$2" "$1"
	elif have wget; then
		wget -q -O "$2" "$1"
	else
		die "needs curl or wget"
	fi
}

# The first byte past an ELF header's class: 1 little-endian, 2 big.
elf_endian() {
	if have hexdump; then
		dd if=/bin/sh bs=1 skip=5 count=1 2>/dev/null | hexdump -e '1/1 "%d"'
	else
		dd if=/bin/sh bs=1 skip=5 count=1 2>/dev/null | od -An -tu1 | tr -d ' \n'
	fi
}

# Which build: mvd-linux-ARCH.
[ "$(uname -s)" = Linux ] || die "the release builds are for Linux. Elsewhere, build mvd yourself: see the README"
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
armv6* | armv7* | armv8l) arch=arm ;;
mips)
	if [ "$(elf_endian)" = 1 ]; then arch=mipsle; else arch=mips; fi
	;;
*) die "no release build for $(uname -m). Build mvd yourself: see the README" ;;
esac

# Which service, as mvd setup service decides it.
if [ -f /etc/openwrt_release ]; then
	kind=procd
elif [ -x /sbin/openrc-run ]; then
	kind=openrc
elif [ -d /run/systemd/system ]; then
	kind=systemd
else
	kind=none
fi

# Who runs mvd, and so whose ~/.config/mvd/config.toml it reads.
uid=$(id -u)
case $kind in
procd)
	[ "$uid" = 0 ] || die "run this as root"
	home=/root
	;;
openrc)
	[ "$uid" = 0 ] || die "run this as root"
	home=/home/mvd
	;;
systemd)
	[ "$uid" != 0 ] || die "run this as the user mvd should run as, not root: it becomes a systemd user service"
	home=$HOME
	;;
none) home=$HOME ;;
esac
config=$home/.config/mvd/config.toml

# A path or key that can go in the config as it is.
plain() {
	case $1 in
	*'"'* | *'\'* | *'|'* | *'&'*) return 1 ;;
	esac
}

# The first drive that's mounted, for a default.
drive=
if [ "$kind" = procd ] || [ "$kind" = openrc ]; then
	drive=$(awk '$2 ~ "^/(mnt|media|tmp/mountd)/" && $3 != "tmpfs" { print $2; exit }' /proc/mounts)
fi

if [ -f "$config" ]; then
	update=1
	say "mvd is set up here already ($config): updating it, and leaving the config alone."
	bin=$(command -v mvd || true)
	[ -n "$bin" ] || die "there's a config but no mvd on the PATH. Put the new build where the old one was by hand"
	bin=$(readlink -f "$bin")
else
	update=
	say "This sets up mvd, a small server for your own movie and TV collection."
	say

	video=${MVD_VIDEO_DIR-}
	if [ -n "$drive" ]; then
		default=$drive/video
	elif [ "$kind" = systemd ] || [ "$kind" = none ]; then
		default=$HOME/video
	else
		default=
	fi
	if [ -z "$video" ]; then
		say "Where is your video library? mvd keeps movies/, tv/ and import/ in it,"
		say "and makes the folder if it isn't there."
	fi
	while :; do
		[ -n "$video" ] || ask video "Library folder" "$default"
		case $video in
		/*) if plain "$video"; then break; fi; say "That path has a character the config can't hold." ;;
		*) say "Give the whole path, starting with /." ;;
		esac
		[ -z "${MVD_VIDEO_DIR-}" ] || die "MVD_VIDEO_DIR isn't a path mvd can use"
		video=
	done
	video=${video%/}

	if [ "${MVD_TMDB_KEY+set}" = set ]; then
		key=$MVD_TMDB_KEY
	else
		say
		say "mvd looks titles up on TMDB for artwork and descriptions. A key is free:"
		say "https://www.themoviedb.org/settings/api (leave it empty to add it later)."
		ask key "TMDB API key" ""
	fi
	plain "$key" || die "that key has a character the config can't hold"

	# On a router or a Pi, mvd and its files go on the drive, beside the
	# library: flash is small and wears. Elsewhere, in the usual places.
	if [ "$kind" = procd ] || [ "$kind" = openrc ]; then
		bin=$(dirname "$video")/bin/mvd
		data=$(dirname "$video")/.mvd
	elif [ "$uid" = 0 ]; then
		bin=/usr/local/bin/mvd
		data=
	else
		bin=$HOME/.local/bin/mvd
		data=
	fi

	say
	say "  library   $video"
	say "  mvd       $bin"
	[ -z "$data" ] || say "  its files $data"
	say "  config    $config"
	case $kind in
	procd) say "  service   /etc/init.d/mvd (procd)" ;;
	openrc) say "  service   /etc/init.d/mvd (OpenRC), running as the user mvd" ;;
	systemd) say "  service   a systemd user service, running as $(id -un)" ;;
	none) say "  service   none: no init system this script knows" ;;
	esac
	if [ -z "${MVD_YES-}" ]; then
		ask go "Go ahead?" "y"
		case $go in y* | Y*) ;; *) die "stopped, with nothing changed" ;; esac
	fi
fi

# Fetch the build beside where it goes, check it, and move it into place: a
# running mvd keeps the old file until it restarts.
say
say "Fetching mvd-linux-$arch..."
mkdir -p "$(dirname "$bin")"
new=$bin.new
trap 'rm -f "$new" "$new.sums"' EXIT
fetch "$RELEASE/mvd-linux-$arch" "$new"
if have sha256sum; then
	fetch "$RELEASE/SHA256SUMS" "$new.sums"
	want=$(awk -v f="mvd-linux-$arch" '$2 == f || $2 == "*" f { print $1 }' "$new.sums")
	got=$(sha256sum "$new" | awk '{ print $1 }')
	[ -n "$want" ] && [ "$want" = "$got" ] || die "the download doesn't match its checksum"
fi
chmod +x "$new"
"$new" version >/dev/null || die "the build doesn't run on this machine"
mv "$new" "$bin"
say "Installed: $("$bin" version | head -n 1)"

restart() {
	case $kind in
	procd) /etc/init.d/mvd restart ;;
	openrc) rc-service mvd restart ;;
	systemd) systemctl --user restart mvd ;;
	none) say "Restart mvd server to run the new build." ;;
	esac
}

if [ -n "$update" ]; then
	restart
	say "Updated."
	exit 0
fi

# On the PATH, where the drive's bin isn't.
case $bin in
/usr/local/bin/mvd | "$HOME/.local/bin/mvd") ;;
*)
	if [ "$kind" = procd ]; then link=/usr/bin/mvd; else link=/usr/local/bin/mvd; fi
	ln -sf "$bin" "$link"
	;;
esac

if [ "$kind" = openrc ] && ! id mvd >/dev/null 2>&1; then
	adduser -D mvd
fi

# The config: mvd's own example, with the answers in it.
if [ -n "$data" ]; then dataline="data_dir = \"$data\""; else dataline="#data_dir = \"~/.config/mvd\""; fi
[ -n "$key" ] || key=your-key-here
mkdir -p "$(dirname "$config")" "$video/import"
[ -z "$data" ] || mkdir -p "$data"
(
	umask 077
	"$bin" setup config | sed \
		-e "s|^video_dir = .*|video_dir = \"$video\"|" \
		-e "s|^data_dir = .*|$dataline|" \
		-e "s|^api_key = \"[^\"]*\"|api_key = \"$key\"|" >"$config"
)
if [ "$kind" = openrc ]; then
	# What mvd writes must be the user mvd's. (exFAT has no owners: there the
	# drive's mount options decide, and chown changes nothing.)
	chown -R mvd:mvd "$home/.config" 2>/dev/null || true
	chown mvd:mvd "$video" "$video/import" "$data" 2>/dev/null || true
fi

case $kind in
procd)
	"$bin" setup service >/etc/init.d/mvd
	chmod +x /etc/init.d/mvd
	# Keep the service across firmware upgrades.
	grep -qx /etc/init.d/mvd /etc/sysupgrade.conf 2>/dev/null || echo /etc/init.d/mvd >>/etc/sysupgrade.conf
	# With little memory and no swap, the kernel setting from mvd setup memory.
	if [ "$(awk '/^MemTotal/ { print $2 }' /proc/meminfo)" -lt 262144 ]; then
		"$bin" setup memory | sh
	fi
	/etc/init.d/mvd enable
	/etc/init.d/mvd restart
	;;
openrc)
	"$bin" setup service >/etc/init.d/mvd
	chmod +x /etc/init.d/mvd
	rc-update add mvd default
	rc-service mvd restart
	;;
systemd)
	mkdir -p "$HOME/.config/systemd/user"
	"$bin" setup service >"$HOME/.config/systemd/user/mvd.service"
	systemctl --user daemon-reload
	systemctl --user enable --now mvd
	# So it starts at boot and stays up with nobody logged in (a headless Pi).
	# Where this needs root, the closing notes say how.
	lingering=
	if loginctl enable-linger "$(id -un)" 2>/dev/null; then lingering=1; fi
	;;
esac

# Where it answers.
addr=
if [ "$kind" = procd ]; then addr=$(uci -q get network.lan.ipaddr 2>/dev/null || true); fi
if [ -z "$addr" ] && have ip; then
	addr=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
fi
[ -n "$addr" ] || addr=this-machine

say
if [ "$kind" = none ]; then
	say "mvd is installed. Start it with: mvd server"
else
	say "mvd is running."
fi
say "  Open http://$addr:6837/ in a browser, or the mvd app on Apple TV, iPhone"
say "  or iPad, which finds the server by itself."
say "  Put videos in $video/import and mvd files them into the library."
if [ "$key" = your-key-here ]; then
	say "  It can't look anything up until the TMDB key is in $config."
fi
case $kind in
openrc)
	say "  Run mvd's commands as the user mvd: su mvd -c 'mvd status'. A library"
	say "  that's there already must be mvd's too: chown -R mvd:mvd $video"
	say "  On a diskless Alpine, save all this with: lbu commit"
	;;
systemd)
	if [ -z "$lingering" ]; then
		say "  It runs while you're logged in. To start it at boot and keep it running:"
		say "    sudo loginctl enable-linger $(id -un)"
	fi
	;;
esac
say "  Anyone on your network can use it. To ask for a token, see mvd setup token."
