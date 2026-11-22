//go:build linux || dragonfly || freebsd || netbsd || openbsd

package mdns

import "syscall"

// The option that asks for a packet's in6_pktinfo, and the type of the
// control message it comes in.
const (
	wantPktinfo6 = syscall.IPV6_RECVPKTINFO
	pktinfo6     = syscall.IPV6_PKTINFO
)
