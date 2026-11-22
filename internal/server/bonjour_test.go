package server

import (
	"net"
	"strings"
	"testing"

	"mvd/internal/config"
)

func TestBonjourService(t *testing.T) {
	Version = "v1.2"
	defer func() { Version = "dev" }()
	cfg := config.Config{Port: "6837", MDNS: true}
	svc, allow, ok := bonjourService(cfg, "GL-AR750S.lan")
	if !ok {
		t.Fatal("not advertised")
	}
	if svc.Instance != "GL-AR750S" || svc.Host != "mvd-gl-ar750s" || svc.Type != "_mvd._tcp" || svc.Port != 6837 {
		t.Errorf("%+v", svc)
	}
	if strings.Join(svc.TXT, " ") != "version=v1.2" {
		t.Errorf("TXT %q", svc.TXT)
	}
	if allow != nil {
		t.Error("restricted, with nothing set")
	}

	cfg.Token = "secret"
	cfg.MDNSName = "Living room"
	svc, _, _ = bonjourService(cfg, "GL-AR750S")
	if svc.Instance != "Living room" || svc.Host != "mvd-gl-ar750s" || strings.Join(svc.TXT, " ") != "version=v1.2 auth=token" {
		t.Errorf("%+v", svc)
	}
}

func TestBonjourHostName(t *testing.T) {
	for in, want := range map[string]string{"Warrens-MacBook-Pro.local": "mvd-warrens-macbook-pro",
		"nas_box": "mvd-nas-box", "": "mvd", "--": "mvd", "émile": "mvd-mile"} {
		svc, _, _ := bonjourService(config.Config{Port: "6837", MDNS: true}, in)
		if svc.Host != want {
			t.Errorf("%q: %q, want %q", in, svc.Host, want)
		}
	}
	if svc, _, _ := bonjourService(config.Config{Port: "6837", MDNS: true}, ""); svc.Instance != "mvd" {
		t.Errorf("no host name: instance %q", svc.Instance)
	}
}

func TestBonjourOff(t *testing.T) {
	for _, cfg := range []config.Config{
		{Port: "6837"}, // mdns = false
		{Address: "127.0.0.1", Port: "6837", MDNS: true},
		{Address: "localhost", Port: "6837", MDNS: true},
		{Address: "::1", Port: "6837", MDNS: true},
	} {
		if _, _, ok := bonjourService(cfg, "router"); ok {
			t.Errorf("%+v: advertised", cfg)
		}
	}
}

func TestBonjourWhere(t *testing.T) {
	_, allow, _ := bonjourService(config.Config{Address: "192.168.8.1", Port: "6837", MDNS: true}, "router")
	if !allow("br-lan", net.IPv4(192, 168, 8, 1)) || allow("br-guest", net.IPv4(192, 168, 9, 1)) {
		t.Error("listening on one address: advertised elsewhere, or not there")
	}
	_, allow, _ = bonjourService(config.Config{Port: "6837", MDNS: true, MDNSInterfaces: "br-lan, eth1"}, "router")
	if !allow("br-lan", net.IPv4(192, 168, 8, 1)) || !allow("eth1", net.IPv4(10, 0, 0, 1)) ||
		allow("wan", net.IPv4(192, 168, 1, 20)) {
		t.Error("interfaces: wrong ones")
	}
	// IPv6 addresses only where the server can be reached over IPv6.
	six := net.ParseIP("fe80::1")
	if !allow("br-lan", six) {
		t.Error("listening on everything: no IPv6 address")
	}
	_, allow, _ = bonjourService(config.Config{Address: "0.0.0.0", Port: "6837", MDNS: true}, "router")
	if !allow("br-lan", net.IPv4(192, 168, 8, 1)) || allow("br-lan", six) {
		t.Error("listening on IPv4 alone: an IPv6 address, or no IPv4 one")
	}
	_, allow, _ = bonjourService(config.Config{Address: "192.168.8.1", Port: "6837", MDNS: true}, "router")
	if allow("br-lan", six) {
		t.Error("listening on one IPv4 address: an IPv6 address")
	}
	if _, allow, _ = bonjourService(config.Config{Address: "::", Port: "6837", MDNS: true}, "router"); allow != nil {
		t.Error("listening on every address of both kinds: restricted")
	}
}
