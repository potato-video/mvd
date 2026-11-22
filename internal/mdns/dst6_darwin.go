//go:build darwin

package mdns

import "syscall"

// The option that asks for a packet's in6_pktinfo, and the type of the
// control message it comes in. macOS has two sets of these, and the
// syscall package names the older (RFC 2292), which is one number for
// both.
const (
	wantPktinfo6 = syscall.IPV6_2292PKTINFO
	pktinfo6     = syscall.IPV6_2292PKTINFO
)
