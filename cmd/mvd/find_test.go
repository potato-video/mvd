package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mvd/internal/config"
	"mvd/internal/mdns"
)

func TestFindServer(t *testing.T) {
	defer func(b func() ([]mdns.Found, error), l func(string) bool) { browseServers, listening = b, l }(browseServers, listening)
	router := mdns.Found{Name: "GL-AR750S", Host: "mvd-gl-ar750s.local", IP: net.IPv4(192, 168, 8, 1), Port: 6837}
	mac := mdns.Found{Name: "MBP14", Host: "mvd-mbp14.local", IP: net.IPv4(192, 168, 8, 219), Port: 6838}
	for _, c := range []struct {
		name   string
		cfg    config.Config
		local  bool // a server's listening on this machine
		found  []mdns.Found
		want   string
		says   string
		browse bool // whether it looks on the network at all
	}{
		{"set in the config", config.Config{Port: "6837", Server: "http://nas:6837"}, false, []mdns.Found{router}, "http://nas:6837", "", false},
		{"one on this machine", config.Config{Port: "6837"}, true, []mdns.Found{router}, "http://127.0.0.1:6837", "", false},
		{"one on the network", config.Config{Port: "6837"}, false, []mdns.Found{router}, "http://192.168.8.1:6837",
			"mvd: using GL-AR750S at http://192.168.8.1:6837, found on the network\n", true},
		{"none anywhere", config.Config{Port: "6837"}, false, nil, "http://127.0.0.1:6837", "", true},
		{"several", config.Config{Port: "6837"}, false, []mdns.Found{router, mac}, "",
			"mvd: there are several servers on the network. Pick one with -server, or server under [client] in config.toml:\n" +
				"  GL-AR750S                http://192.168.8.1:6837\n  MBP14                    http://192.168.8.219:6838\n", true},
		{"one that never said where", config.Config{Port: "6837"}, false, []mdns.Found{{Name: "Half"}, router, {Name: "No port", IP: net.IPv4(10, 0, 0, 1)}},
			"http://192.168.8.1:6837", "mvd: using GL-AR750S at http://192.168.8.1:6837, found on the network\n", true},
	} {
		browsed := false
		browseServers = func() ([]mdns.Found, error) { browsed = true; return append([]mdns.Found(nil), c.found...), nil }
		listening = func(string) bool { return c.local }
		var says bytes.Buffer
		got, ok := findServer(c.cfg, &says)
		if got != c.want || ok != (c.want != "") || says.String() != c.says || browsed != c.browse {
			t.Errorf("%s: %q %v, browsed %v, said %q", c.name, got, ok, browsed, says.String())
		}
	}
}

func TestListening(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	if !listening(srv.URL) {
		t.Error("a server that's there: not listening")
	}
	srv.Close()
	if listening(srv.URL) || listening("http://[bad") || listening(strings.Repeat("x", 10)) {
		t.Error("nothing there: listening")
	}
}
