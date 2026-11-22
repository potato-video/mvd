//go:build !unix

package mdns

import (
	"errors"
	"net"
	"syscall"
)

var errUnsupported = errors.New("not supported on this system")

func shareControl(network, address string, c syscall.RawConn) error { return errUnsupported }
func setup(fd uintptr) error                                        { return errUnsupported }
func join(fd uintptr, ifaddr net.IP) error                          { return errUnsupported }
func multicastInterface(fd uintptr, ifaddr net.IP) error            { return errUnsupported }
func destination(oob []byte) net.IP                                 { return nil }
func setup6(fd uintptr) error                                       { return errUnsupported }
func join6(fd uintptr, ifindex int) error                           { return errUnsupported }
func multicastInterface6(fd uintptr, ifindex int) error             { return errUnsupported }
func destination6(oob []byte) (net.IP, int)                         { return nil, 0 }
