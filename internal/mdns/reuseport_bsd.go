//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package mdns

import "syscall"

// On the BSDs and macOS, sharing a multicast port takes SO_REUSEPORT too:
// mDNSResponder sets it.
const soReusePort = syscall.SO_REUSEPORT
