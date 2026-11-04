package winnet

import "strconv"

// PortRange is an inclusive range of ports.
type PortRange struct{ From, To uint16 }

// Contains reports whether port is in the range.
func (r PortRange) Contains(port uint16) bool { return port >= r.From && port <= r.To }

// String formats the range the way Windows Firewall expects it: "135" or
// "5985-5986".
func (r PortRange) String() string {
	if r.From == r.To {
		return strconv.Itoa(int(r.From))
	}
	return strconv.Itoa(int(r.From)) + "-" + strconv.Itoa(int(r.To))
}

// IP protocol numbers.
const (
	ProtoTCP uint8 = 6
	ProtoUDP uint8 = 17
)

// The denylist of system services that members must not reach on each other:
// RPC, NetBIOS, SMB, remote desktop and WinRM. Game ports cannot be known in
// advance, so everything else stays open. The Windows Firewall rules and the
// userspace packet filter share these lists.
var (
	BlockedTCPPorts = []PortRange{{135, 135}, {139, 139}, {445, 445}, {3389, 3389}, {5985, 5986}}
	BlockedUDPPorts = []PortRange{{137, 138}}
)

// IsBlocked reports whether traffic of the given IP protocol (6 TCP, 17 UDP)
// to the given destination port is on the denylist.
func IsBlocked(proto uint8, port uint16) bool {
	var list []PortRange
	switch proto {
	case ProtoTCP:
		list = BlockedTCPPorts
	case ProtoUDP:
		list = BlockedUDPPorts
	default:
		return false
	}
	for _, r := range list {
		if r.Contains(port) {
			return true
		}
	}
	return false
}
