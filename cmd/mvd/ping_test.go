package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"mvd/internal/config"
)

// A rescan that fails (a server busy with the import's writes, say) is
// tried again, until it works.
func TestPingServerRetries(t *testing.T) {
	pingDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/rescan" || r.Header.Get("Authorization") != "Bearer tok" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
	}))
	defer srv.Close()

	if !pingServer(config.Config{Server: srv.URL, Token: "tok"}) {
		t.Error("pingServer gave up")
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("%d calls, want 3", n)
	}
}

// Nothing listening means no server to tell: no retries.
func TestPingServerNothingListening(t *testing.T) {
	pingDelays = []time.Duration{time.Hour}
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if pingServer(config.Config{Server: url}) {
		t.Error("pingServer says it rescanned")
	}
}

// The local commands find the server where it listens: [server] address if
// it's one interface, 127.0.0.1 if it's all of them, and [client] server
// over either.
func TestLocalServer(t *testing.T) {
	for _, c := range []struct {
		cfg  config.Config
		want string
	}{
		{config.Config{Port: "6837"}, "http://127.0.0.1:6837"},
		{config.Config{Address: "0.0.0.0", Port: "6837"}, "http://127.0.0.1:6837"},
		{config.Config{Address: "::", Port: "6837"}, "http://127.0.0.1:6837"},
		{config.Config{Address: "192.168.8.1", Port: "6837"}, "http://192.168.8.1:6837"},
		{config.Config{Address: "fd00::1", Port: "6837"}, "http://[fd00::1]:6837"},
		{config.Config{Address: "router.lan", Port: "6837"}, "http://router.lan:6837"},
		{config.Config{Address: "192.168.8.1", Port: "6837", Server: "http://nas:6837"}, "http://nas:6837"},
	} {
		if got := localServer(c.cfg); got != c.want {
			t.Errorf("localServer(address %q, server %q) = %q, want %q", c.cfg.Address, c.cfg.Server, got, c.want)
		}
	}
}
