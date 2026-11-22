//go:build !(linux || darwin || dragonfly || freebsd || netbsd || openbsd)

package mdns

// changes: this system isn't asked what changes, so the networks are
// looked at every minute.
func changes() (events <-chan struct{}, end func()) { return nil, func() {} }
