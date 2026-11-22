package main

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	"mvd/internal/config"
	"mvd/internal/mdns"
)

// What findServer uses to look, for tests to replace.
var (
	browseServers = func() ([]mdns.Found, error) { return mdns.Browse("_mvd._tcp", time.Second) }
	listening     = func(base string) bool {
		u, err := url.Parse(base)
		if err != nil {
			return false
		}
		c, err := net.DialTimeout("tcp", u.Host, 500*time.Millisecond)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
)

// findServer is where a client command (upload, progress, status, search)
// finds the server when it isn't told with -server: [client] server if
// that's set; else this machine's, if one's listening; else the one that
// answers on the local network (Bonjour), saying so on stderr. If nothing
// answers it's this machine's after all, for the command to say it can't
// be reached. Not ok: several answered, and they've been listed for the
// person to pick from, as guessing could send an upload to the wrong one.
func findServer(cfg config.Config, stderr io.Writer) (base string, ok bool) {
	local := localServer(cfg)
	if cfg.Server != "" || listening(local) {
		return local, true
	}
	found, _ := browseServers()
	var at []string
	for i := 0; i < len(found); i++ {
		if found[i].IP == nil || found[i].Port == 0 {
			found = append(found[:i], found[i+1:]...)
			i--
			continue
		}
		at = append(at, "http://"+net.JoinHostPort(found[i].IP.String(), strconv.Itoa(found[i].Port)))
	}
	switch len(found) {
	case 0:
		return local, true
	case 1:
		fmt.Fprintf(stderr, "mvd: using %s at %s, found on the network\n", found[0].Name, at[0])
		return at[0], true
	}
	fmt.Fprintln(stderr, "mvd: there are several servers on the network. Pick one with -server, or server under [client] in config.toml:")
	for i, f := range found {
		fmt.Fprintf(stderr, "  %-24s %s\n", f.Name, at[i])
	}
	return "", false
}

// clientAPI is localAPI for the commands that also work from another
// machine: it talks to the server findServer finds.
func clientAPI(cfg config.Config) (api *apiClient, ok bool) {
	base, ok := findServer(cfg, os.Stderr)
	if !ok {
		return nil, false
	}
	api = localAPI(cfg)
	api.base = base
	return api, true
}
