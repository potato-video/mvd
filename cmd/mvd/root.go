package main

import (
	"fmt"
	"strings"

	"mvd/internal/config"
)

// rootWarning is what to say when mvd runs as root (euid 0) where the
// service doesn't: anywhere but OpenWRT (procd), whose service runs as root.
// As root, mvd reads root's config rather than the server's, and leaves
// files the server can't move or trash. setup and version are left alone:
// the guides run `mvd setup service > /etc/init.d/mvd` as root on purpose.
func rootWarning(cmd string, euid int, kind string) string {
	if euid != 0 || kind == "procd" || cmd == "setup" || strings.TrimLeft(cmd, "-") == "version" {
		return ""
	}
	const why = "root's config isn't the server's, and files made as root are ones the server can't move or trash"
	if kind == "openrc" {
		return fmt.Sprintf("mvd: running as root, but the service runs as the user mvd: %s. Run it as mvd: su mvd -c 'mvd %s'", why, cmd)
	}
	return fmt.Sprintf("mvd: running as root, but the service runs as your own user: %s. Run it without sudo", why)
}

// noAPIKey is the message for a missing TMDB key, naming the config read.
func noAPIKey() string {
	return "mvd: no TMDB API key: set TMDB_API_KEY, or api_key under [tmdb] in " + config.Path()
}
