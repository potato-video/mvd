package main

import (
	"strings"
	"testing"
)

// Each service file gets this mvd's path (and, for launchd, the home folder)
// in place of the example's.
func TestServiceFile(t *testing.T) {
	for kind, want := range map[string][]string{
		"procd":   {"PROG=/mnt/Maxtor/bin/mvd "},
		"openrc":  {"command=\"/mnt/Maxtor/bin/mvd\" "},
		"systemd": {"ExecStart=/mnt/Maxtor/bin/mvd server\n"},
		"launchd": {"<string>/mnt/Maxtor/bin/mvd</string>", "<string>/Users/warren/Library/Logs/mvd.log</string>"},
	} {
		s, err := serviceFile(kind, "/mnt/Maxtor/bin/mvd", "/Users/warren")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		for _, w := range want {
			if !strings.Contains(s, w) {
				t.Errorf("%s: no %q in\n%s", kind, w, s)
			}
		}
		if strings.Contains(s, "/mnt/drive/bin") || strings.Contains(s, "%h/.local/bin") || strings.Contains(s, "/Users/YOU") {
			t.Errorf("%s: the example's paths are left in\n%s", kind, s)
		}
	}
	if _, err := serviceFile("upstart", "/bin/mvd", "/root"); err == nil {
		t.Error("upstart: no error")
	}
}

// The memory script turns watermark boosting off, at boot and now, and says
// how to undo it.
func TestMemorySetup(t *testing.T) {
	for _, want := range []string{
		"cat > /etc/sysctl.d/30-mvd-memory.conf <<'EOF'\nvm.watermark_boost_factor = 0\nEOF\n",
		"sysctl -p /etc/sysctl.d/30-mvd-memory.conf\n",
		"rm /etc/sysctl.d/30-mvd-memory.conf",
	} {
		if !strings.Contains(memorySetup, want) {
			t.Errorf("no %q in\n%s", want, memorySetup)
		}
	}
	if !strings.Contains(setupUsage, "\n  memory ") {
		t.Errorf("memory isn't in the usage:\n%s", setupUsage)
	}
}

// A token is 26 lower-case base32 characters, new each time, in a block to
// paste; a name makes a whole entry, and a bad name is refused.
func TestTokenSetup(t *testing.T) {
	a, b := newToken(), newToken()
	if len(a) != 26 || a == b || strings.ToLower(a) != a || strings.ContainsAny(a, "=") {
		t.Errorf("tokens %q %q", a, b)
	}
	s, err := tokenSetup("")
	if err != nil || !strings.HasPrefix(s, "# For config.toml, under [server]:\ntoken = \"") {
		t.Errorf("%v\n%s", err, s)
	}
	s, err = tokenSetup("living room")
	if err != nil || !strings.Contains(s, "[[server.tokens]]\nname    = \"living room\"\ntoken   = \"") {
		t.Errorf("%v\n%s", err, s)
	}
	for _, bad := range []string{"admin", "no/slashes", strings.Repeat("x", 33)} {
		if _, err := tokenSetup(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	if !strings.Contains(setupUsage, "\n  token [NAME] ") {
		t.Errorf("token isn't in the usage:\n%s", setupUsage)
	}
}
