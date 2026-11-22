//go:build unix && !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package mdns

const soReusePort = 0
