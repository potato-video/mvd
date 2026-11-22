package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"mvd/internal/config"
)

// pingDelays are the waits between tries at a rescan: a server busy with
// the import's writes to the drive can be slow to answer.
var pingDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

// pingServer asks a server running on this machine to rescan, so an import
// shows up straight away, trying again if that fails. Nothing listening is
// fine: there's no server to tell.
func pingServer(cfg config.Config) bool {
	c := &http.Client{Timeout: 30 * time.Second}
	for try := 0; ; try++ {
		req, err := http.NewRequest("POST", localServer(cfg)+"/api/rescan", nil)
		if err != nil {
			return false
		}
		if cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				fmt.Println("server rescanned")
				return true
			}
		} else if errors.Is(err, syscall.ECONNREFUSED) {
			return false
		}
		if try == len(pingDelays) {
			return false
		}
		time.Sleep(pingDelays[try])
	}
}

// serverVersion says which build the server on this machine is running,
// and whether it's this one (after installing a new binary, the old server
// keeps running until it's restarted).
func serverVersion(cfg config.Config) string {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(localServer(cfg) + "/api/")
	if err != nil {
		return "server: not running"
	}
	resp.Body.Close()
	v := resp.Header.Get("X-Mvd-Version")
	switch {
	case v == "":
		return "server: running an older build (restart it to run this one)"
	case v != version:
		return "server: running " + v + " (restart it to run this one)"
	}
	return "server: running this build"
}

// localServer is the address of the mvd server on this machine, from
// [server] address and port, unless [client] server says where it is (on a
// laptop, say, the router). A server listening on one interface is only
// reachable there; one listening on all of them is reached at 127.0.0.1.
func localServer(cfg config.Config) string {
	if cfg.Server != "" {
		return cfg.Server
	}
	host := cfg.Address
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, cfg.Port)
}
