package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Root gets a warning only where the service runs as another user, and not
// for the commands the setup guides run as root on purpose.
func TestRootWarning(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		euid int
		kind string
		want string // "" for no warning
	}{
		{"refresh", 0, "openrc", "su mvd -c 'mvd refresh'"},
		{"import", 0, "openrc", "su mvd -c 'mvd import'"},
		{"server", 0, "systemd", "without sudo"},
		{"status", 0, "launchd", "without sudo"},
		{"refresh", 0, "procd", ""},
		{"refresh", 1000, "openrc", ""},
		{"refresh", 1000, "systemd", ""},
		{"setup", 0, "openrc", ""},
		{"version", 0, "systemd", ""},
		{"--version", 0, "openrc", ""},
	} {
		got := rootWarning(c.cmd, c.euid, c.kind)
		switch {
		case c.want == "" && got != "":
			t.Errorf("%s as %d on %s: warned %q", c.cmd, c.euid, c.kind, got)
		case c.want != "" && !strings.Contains(got, c.want):
			t.Errorf("%s as %d on %s: %q, want it to say %q", c.cmd, c.euid, c.kind, got, c.want)
		}
	}
}

// The missing-key message names the config actually read, so reading root's
// by mistake shows.
func TestNoAPIKeyNamesTheConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got, want := noAPIKey(), filepath.Join(dir, "mvd", "config.toml"); !strings.Contains(got, want) {
		t.Errorf("%q doesn't name %s", got, want)
	}
}
