package server

import (
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"mvd/internal/config"
	"mvd/internal/mdns"
)

// advertise announces the server on the local network over Bonjour
// (_mvd._tcp), so the Apple TV app finds it without anyone typing an
// address. It returns what stops it, saying goodbye. Not being able to is
// logged, not fatal: typing the address still works.
func advertise(cfg config.Config) (stop func()) {
	host, _ := os.Hostname()
	svc, allow, ok := bonjourService(cfg, host)
	if !ok {
		return func() {}
	}
	r, err := mdns.Start(svc, allow, log.Printf)
	if err != nil {
		log.Printf("mvd: Bonjour off: %v", err)
		return func() {}
	}
	return r.Close
}

// bonjourService says what to advertise, given the machine's host name,
// and where (allow; nil means every private network). Not ok: Bonjour is
// off, or the server only listens on this machine.
func bonjourService(cfg config.Config, hostname string) (svc mdns.Service, allow func(string, net.IP) bool, ok bool) {
	if !cfg.MDNS {
		return svc, nil, false
	}
	lhost, lport, err := net.SplitHostPort(cfg.Listen())
	if err != nil {
		return svc, nil, false
	}
	port, err := strconv.Atoi(lport)
	if err != nil {
		return svc, nil, false
	}
	listenIP := net.ParseIP(lhost)
	if lhost == "localhost" || (listenIP != nil && listenIP.IsLoopback()) {
		return svc, nil, false
	}

	short, _, _ := strings.Cut(hostname, ".") // "GL-AR750S.lan", "Warrens-Mac.local"
	svc = mdns.Service{Instance: cfg.MDNSName, Type: "_mvd._tcp", Host: hostLabel(short), Port: port,
		TXT: []string{"version=" + Version, "api=" + apiLevel}}
	if svc.Instance == "" {
		svc.Instance = short
	}
	if svc.Instance == "" {
		svc.Instance = "mvd"
	}
	if cfg.Token != "" {
		svc.TXT = append(svc.TXT, "auth=token") // so an app asks for it before trying
	}

	var only map[string]bool
	for _, n := range strings.Split(cfg.MDNSInterfaces, ",") {
		if n = strings.TrimSpace(n); n != "" {
			if only == nil {
				only = map[string]bool{}
			}
			only[n] = true
		}
	}
	// Only addresses the server can be reached at: its one address, or
	// IPv4 ones alone if it listens on "0.0.0.0".
	listenOne := listenIP != nil && !listenIP.IsUnspecified()
	only4 := listenIP != nil && listenIP.IsUnspecified() && listenIP.To4() != nil
	if only != nil || listenOne || only4 {
		allow = func(ifname string, ip net.IP) bool {
			return (only == nil || only[ifname]) && (!listenOne || ip.Equal(listenIP)) && (!only4 || ip.To4() != nil)
		}
	}
	return svc, allow, true
}

// hostLabel makes the server's own .local host name from the machine's:
// "GL-AR750S" -> "mvd-gl-ar750s". Its own, because the machine's belongs
// to the system's responder (umdns, avahi, mDNSResponder) if there is one.
func hostLabel(machine string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(machine) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	if s := strings.TrimSuffix(b.String(), "-"); s != "" {
		return "mvd-" + s
	}
	return "mvd"
}
