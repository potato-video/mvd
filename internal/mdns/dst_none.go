//go:build unix && !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package mdns

import "net"

// No way known here to learn where a packet was addressed: every query is
// taken to be the group's.
func wantDestination(fd uintptr)            {}
func destination(oob []byte) net.IP         { return nil }
func wantDestination6(fd uintptr)           {}
func destination6(oob []byte) (net.IP, int) { return nil, 0 }
