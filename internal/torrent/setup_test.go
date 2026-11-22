package torrent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"mvd/internal/config"
	"mvd/internal/testutil"
)

// Transmission's cache is a 64th of the RAM, rounded, at least 1MB, and its
// peers a third of the MB, 20 to 200; each torrent's the same, at most 60,
// commented out. With no /proc/meminfo they're left alone.
func TestSetupMemory(t *testing.T) {
	dir := t.TempDir()
	for kb, want := range map[int][3]string{
		121184:   {"2", "39", "39"},
		250000:   {"4", "81", "60"},
		30000:    {"1", "20", "20"},
		67108864: {"1024", "200", "60"},
	} {
		f := dir + "/meminfo"
		testutil.Write(t, f, "MemTotal:       "+strconv.Itoa(kb)+" kB\nMemFree:          9208 kB\n")
		got := "\n" + memory(f)
		if !strings.Contains(got, "\nuci set $T.cache_size_mb='"+want[0]+"'\n") ||
			!strings.Contains(got, "\nuci set $T.peer_limit_global='"+want[1]+"'\n") ||
			!strings.Contains(got, "\n#uci set $T.peer_limit_per_torrent='"+want[2]+"'\n") {
			t.Errorf("%d kB:\n%s", kb, got)
		}
	}
	if got := memory(dir + "/none"); strings.Contains(got, "uci set") {
		t.Errorf("no meminfo:\n%s", got)
	}
}

// max_downloads becomes Transmission's download queue; 0 leaves its own.
func TestSetupQueue(t *testing.T) {
	s := Setup(config.Config{ImportDir: "/mnt/drive/video/import", SeedRatio: 1, MaxDownloads: 2})
	if !strings.Contains(s, "\nuci set $T.download_queue_enabled='1'\nuci set $T.download_queue_size='2'\n") {
		t.Errorf("max_downloads 2:\n%s", s)
	}
	s = Setup(config.Config{ImportDir: "/mnt/drive/video/import", SeedRatio: 1})
	if strings.Contains(s, "download_queue") {
		t.Errorf("max_downloads 0:\n%s", s)
	}
	if !strings.Contains(s, "mvd setup transmission") || !strings.Contains(s, "[downloads]\n#   transmission") {
		t.Errorf("instructions:\n%s", s)
	}
}

// On Alpine, Transmission runs as mvd's user with its state beside the
// library, and its settings.json is valid JSON with mvd's folders, queue and
// memory sizes in it.
func TestSetupAlpine(t *testing.T) {
	dir := t.TempDir()
	testutil.Write(t, dir+"/meminfo", "MemTotal:       3884108 kB\n")
	s := setupAlpine(config.Config{ImportDir: "/mnt/media/video/import", VideoDir: "/mnt/media/video", SeedRatio: 1, MaxDownloads: 2}, dir+"/meminfo")
	for _, want := range []string{
		"apk add transmission-daemon\n",
		"runas_user=mvd:mvd\n",
		"config_dir='/mnt/media/.transmission'\n",
		"logfile=syslog\n",
		"rc-update add transmission-daemon default\n",
		"[downloads]\n#   transmission",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "uci ") || strings.Contains(s, "opkg") {
		t.Errorf("OpenWRT commands in the Alpine setup:\n%s", s)
	}
	_, rest, ok := strings.Cut(s, "settings.json' <<'EOF'\n")
	body, _, ok2 := strings.Cut(rest, "\nEOF\n")
	if !ok || !ok2 {
		t.Fatalf("no settings.json heredoc in\n%s", s)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("settings.json isn't JSON: %v\n%s", err, body)
	}
	for k, v := range map[string]any{
		"download-dir":           "/mnt/media/video/import",
		"incomplete-dir":         "/mnt/media/video/import/.downloading",
		"incomplete-dir-enabled": true,
		"rpc-bind-address":       "127.0.0.1",
		"download-queue-enabled": true,
		"download-queue-size":    2.0,
		"cache-size-mb":          59.0,
		"peer-limit-global":      200.0,
	} {
		if got[k] != v {
			t.Errorf("%s: %v, want %v", k, got[k], v)
		}
	}

	// No queue and no meminfo: those keys are left to Transmission, and the
	// JSON is still valid.
	s = setupAlpine(config.Config{ImportDir: "/mnt/media/video/import"}, dir+"/none")
	_, rest, _ = strings.Cut(s, "settings.json' <<'EOF'\n")
	body, _, _ = strings.Cut(rest, "\nEOF\n")
	got = nil
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("settings.json isn't JSON: %v\n%s", err, body)
	}
	for _, k := range []string{"download-queue-enabled", "cache-size-mb", "peer-limit-global"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s set with no queue or meminfo:\n%s", k, body)
		}
	}
}
