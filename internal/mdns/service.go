package mdns

import "strings"

// Service is what's advertised: one instance of one service type, on a host
// name of its own. The host name isn't the machine's: that's the system's
// responder's, if it has one, and two answering for it would fight.
type Service struct {
	Instance string // what browsers show: "GL-AR750S"
	Type     string // "_mvd._tcp"
	Host     string // the host name's one label, in .local: "mvd-gl-ar750s"
	Port     int
	TXT      []string // "key=value"
}

// TTLs, from RFC 6762 section 10: records naming a host go stale sooner.
const (
	hostTTL   = 120  // SRV, A, AAAA, NSEC
	otherTTL  = 4500 // PTR, TXT
	legacyTTL = 10   // in a reply to a one-shot resolver
)

var servicesName = name{"_services", "_dns-sd", "_udp", "local"}

func (s *Service) typeName() name     { return append(strings.Split(s.Type, "."), "local") }
func (s *Service) instanceName() name { return append(name{label(s.Instance)}, s.typeName()...) }
func (s *Service) hostName() name     { return name{label(s.Host), "local"} }

// The records a message holds, as bits.
type records uint8

const (
	recPTR      records = 1 << iota // the type's: _mvd._tcp.local -> the instance
	recServices                     // _services._dns-sd._udp.local -> _mvd._tcp.local
	recSRV
	recTXT
	recA
	recAAAA         // one for each of the interface's IPv6 addresses
	recNSEC         // the host has its addresses and nothing else: with no AAAA, there's none to wait for
	recNSECInstance // the instance has SRV and TXT and nothing else

	numRecords = iota
)

// ttl is the TTL of the one record r is.
func (r records) ttl() uint32 {
	if r&(recPTR|recServices|recTXT) != 0 {
		return otherTTL
	}
	return hostTTL
}
