package main

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mvd/deploy"
	"mvd/internal/config"
)

const setupUsage = `usage: mvd setup <what>

Each prints something to read through and save; mvd changes nothing itself.

  config         a commented config.toml, with the defaults:
                 mvd setup config > ~/.config/mvd/config.toml
  transmission [openwrt|alpine]
                 the commands to set Transmission up for mvd, for this kind of
                 machine (or the one named)
  service [procd|openrc|systemd|launchd]
                 a service that runs mvd server at start-up, for this kind of
                 machine (or the one named), with this mvd's path filled in
  memory         Linux kernel settings for a small machine with no swap (a
                 router), so memory running low doesn't kill mvd early
  token [NAME]   a new random token, for [server] token; with a NAME, a whole
                 [[server.tokens]] entry for a viewer's token
`

// memorySetup is 'mvd setup memory'. Linux raises its free-memory reserve for
// a while after memory gets fragmented (watermark boosting): on a 128MB
// router that took it from about 6MB to about 20MB, so the kernel killed mvd
// and Transmission with 17MB still free.
const memorySetup = `#!/bin/sh
# Kernel memory settings for mvd on a small Linux machine with no swap (a
# router, say). Read it through, then run it as root:
#   mvd setup memory > memory.sh && sh memory.sh
#
# After memory gets fragmented, Linux raises the amount it keeps free for a
# while ("watermark boosting"), to hurry reclaim along. With no swap there's
# little to reclaim, and on a 128MB router that took the reserve from about
# 6MB to about 20MB, so the kernel killed mvd and Transmission with 17MB still
# free. This turns boosting off; the usual reserve stays.
#
# To undo it: rm /etc/sysctl.d/30-mvd-memory.conf and reboot.
set -e

cat > /etc/sysctl.d/30-mvd-memory.conf <<'EOF'
vm.watermark_boost_factor = 0
EOF
sysctl -p /etc/sysctl.d/30-mvd-memory.conf
`

func cmdSetup(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(os.Stderr, setupUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch {
	case args[0] == "config" && len(args) == 1:
		fmt.Print(deploy.ConfigExample)
		return 0
	case args[0] == "transmission" && len(args) <= 2:
		kind := "openwrt"
		if serviceKind() == "openrc" {
			kind = "alpine"
		}
		if len(args) == 2 {
			kind = args[1]
		}
		return setupTransmission(kind)
	case args[0] == "token" && len(args) <= 2:
		name := ""
		if len(args) == 2 {
			name = args[1]
		}
		s, err := tokenSetup(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mvd:", err)
			return 2
		}
		fmt.Print(s)
		return 0
	case args[0] == "memory" && len(args) == 1:
		fmt.Print(memorySetup)
		return 0
	case args[0] == "service" && len(args) <= 2:
		kind := serviceKind()
		if len(args) == 2 {
			kind = args[1]
		}
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "mvd: can't tell where this mvd is:", err)
			return 1
		}
		home, _ := os.UserHomeDir()
		s, err := serviceFile(kind, exe, home)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mvd:", err)
			return 2
		}
		fmt.Print(s)
		return 0
	}
	fmt.Fprint(os.Stderr, setupUsage)
	return 2
}

// serviceKind is the service file this machine wants: a LaunchAgent on a
// Mac, procd on OpenWRT, OpenRC where it's the init system (Alpine), systemd
// on any other Linux.
func serviceKind() string {
	if runtime.GOOS == "darwin" {
		return "launchd"
	}
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return "procd"
	}
	if _, err := os.Stat("/sbin/openrc-run"); err == nil {
		return "openrc"
	}
	return "systemd"
}

// serviceFile is deploy's service file of that kind, with the example's
// paths replaced by exe (this mvd) and home.
func serviceFile(kind, exe, home string) (string, error) {
	var s string
	var r *strings.Replacer
	switch kind {
	case "procd":
		s, r = deploy.OpenWRT, strings.NewReplacer("PROG=/mnt/drive/bin/mvd", "PROG="+exe)
	case "openrc":
		s, r = deploy.OpenRC, strings.NewReplacer(`command="/mnt/drive/bin/mvd"`, `command="`+exe+`"`)
	case "systemd":
		s, r = deploy.Systemd, strings.NewReplacer("ExecStart=%h/.local/bin/mvd", "ExecStart="+exe)
	case "launchd":
		s, r = deploy.Launchd, strings.NewReplacer("/Users/YOU/.local/bin/mvd", exe, "/Users/YOU", home)
	default:
		return "", fmt.Errorf("no service file for %q: procd, openrc, systemd or launchd", kind)
	}
	return r.Replace(s), nil
}

// newToken is 16 random bytes as 26 lower-case base32 characters: long
// enough that guessing it is hopeless, short enough to paste anywhere.
func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// tokenSetup is 'mvd setup token [NAME]': a new token, ready to paste into
// config.toml.
func tokenSetup(name string) (string, error) {
	tok := newToken()
	if name == "" {
		return "# For config.toml, under [server]:\ntoken = \"" + tok + "\"\n", nil
	}
	// The server's own checks on a name, with a stand-in admin's token.
	c := config.Config{Token: newToken(), Tokens: []config.Token{{Name: name, Token: tok}}}
	if _, err := c.CheckTokens(); err != nil {
		return "", err
	}
	return fmt.Sprintf(`# For config.toml, at the end of [server] (it needs [server] token too):
[[server.tokens]]
name    = %q
token   = %q
#ratings = ["U", "PG"]   # only these certifications, as tmdb.region has them; leave out for all
#unrated = false         # with ratings: whether it sees things with no certification
`, name, tok), nil
}
