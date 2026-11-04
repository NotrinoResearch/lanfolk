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
//
//   - RPC (TCP 135, RPC over HTTP 593), NetBIOS (UDP 137-138, TCP 139), SMB
//     (TCP 445), remote desktop (TCP and UDP 3389), WinRM (TCP 5985-5986);
//   - name resolution and device discovery that a member could answer or
//     abuse: LLMNR (UDP and TCP 5355), SSDP (UDP 1900), the UPnP device host
//     (TCP 2869), WS-Discovery (UDP 3702, TCP 5357-5358) and Windows Media
//     sharing (TCP 10243).
//
// No game uses these ports. Game ports cannot be known in advance, so
// everything else stays open. mDNS (UDP 5353) is not listed: games may use
// it, and Windows' own responder is a service, which ServiceFilter covers. The
// Windows Firewall rules and the userspace packet filter share these lists;
// the filter also applies them to broadcast and multicast, so LLMNR, SSDP
// and WS-Discovery queries never reach other members.
var (
	BlockedTCPPorts = []PortRange{{135, 135}, {139, 139}, {445, 445}, {593, 593}, {2869, 2869},
		{3389, 3389}, {5355, 5355}, {5357, 5358}, {5985, 5986}, {10243, 10243}}
	BlockedUDPPorts = []PortRange{{137, 138}, {1900, 1900}, {3389, 3389}, {3702, 3702}, {5355, 5355}}
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
