//go:build unix

package mdns

import (
	"net"
	"testing"
	"time"
)

// The system says where a packet was addressed, which is how a query sent
// to this machine alone is told from one sent to the group.
func TestDestination(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip(err)
	}
	defer conn.Close()
	rc, _ := conn.SyscallConn()
	rc.Control(wantDestination)
	if _, err := conn.WriteToUDP([]byte("hello"), conn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Skip(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf, oob := make([]byte, 16), make([]byte, 128)
	_, oobn, _, _, err := conn.ReadMsgUDP(buf, oob)
	if err != nil {
		t.Fatal(err)
	}
	dst := destination(oob[:oobn])
	if dst == nil && oobn == 0 {
		t.Skip("this system doesn't say")
	}
	if !dst.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("destination %v, from %d bytes of control messages", dst, oobn)
	}
}

// And over IPv6, with the interface it came in on.
func TestDestination6(t *testing.T) {
	conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Skip(err) // no IPv6 here
	}
	defer conn.Close()
	rc, _ := conn.SyscallConn()
	rc.Control(wantDestination6)
	if _, err := conn.WriteToUDP([]byte("hello"), conn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Skip(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf, oob := make([]byte, 16), make([]byte, 128)
	_, oobn, _, _, err := conn.ReadMsgUDP(buf, oob)
	if err != nil {
		t.Fatal(err)
	}
	dst, ifindex := destination6(oob[:oobn])
	if dst == nil && oobn == 0 {
		t.Skip("this system doesn't say")
	}
	lo, err := net.InterfaceByIndex(ifindex)
	if !dst.Equal(net.IPv6loopback) || err != nil || lo.Flags&net.FlagLoopback == 0 {
		t.Errorf("destination %v on interface %d (%v), from %d bytes of control messages", dst, ifindex, err, oobn)
	}
}
