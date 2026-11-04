package engine

import (
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

// The diagnostic report is meant to be attached to public bug reports, so it
// must not reveal where the user or their friends are. Redaction keeps what
// helps debugging (address family, private or public, the network prefix,
// ports, NAT behaviour) and removes what identifies a household.

// cgnat is the shared address space of carrier-grade NAT (RFC 6598).
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// redactAddr shortens a public IP address to its first two IPv4 octets or
// first two IPv6 groups. Private, loopback, link-local, carrier NAT and
// unspecified addresses are kept: they reveal nothing about a household.
func redactAddr(a netip.Addr) string {
	if !a.IsValid() {
		return a.String()
	}
	a = a.Unmap()
	if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsMulticast() ||
		(a.Is4() && cgnat.Contains(a)) {
		return a.String()
	}
	if a.Is4() {
		b := a.As4()
		return strconv.Itoa(int(b[0])) + "." + strconv.Itoa(int(b[1])) + ".x.x"
	}
	b := a.As16()
	return strconv.FormatUint(uint64(b[0])<<8|uint64(b[1]), 16) + ":" +
		strconv.FormatUint(uint64(b[2])<<8|uint64(b[3]), 16) + ":x::x"
}

// redactAddrPort redacts the address of an address and port.
func redactAddrPort(ap netip.AddrPort) string {
	if !ap.IsValid() {
		return ""
	}
	return net.JoinHostPort(redactAddr(ap.Addr()), strconv.Itoa(int(ap.Port())))
}

// redactHostPort redacts "host:port" or "host" when host is an IP literal;
// DNS names are kept (relay names are not personal).
func redactHostPort(s string) string {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return redactAddrPort(ap)
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return redactAddr(a)
	}
	return s
}

// redactKey shortens a public key (hex) to its first 8 characters: enough
// to tell peers apart in one report, useless to add someone as a friend.
func redactKey(hex string) string {
	if len(hex) <= 8 {
		return hex
	}
	return hex[:8] + "…"
}

// redactText redacts every IP address token in free text such as warnings.
func redactText(s string) string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '(' || r == ')' || r == ';' })
	for _, f := range fields {
		if r := redactHostPort(f); r != f {
			s = strings.ReplaceAll(s, f, r)
		}
	}
	return s
}

// redactStatus redacts the parts of a status that identify the user or
// their location.
func redactStatus(st *ipc.Status) {
	st.PublicKey = redactKey(st.PublicKey)
	if st.FriendCode != "" {
		st.FriendCode = "(hidden)"
	}
	for i := range st.Relays {
		st.Relays[i].Name = redactHostPort(st.Relays[i].Name)
	}
	for i, w := range st.Warnings {
		st.Warnings[i] = redactText(w)
	}
	st.AdapterError = redactText(st.AdapterError)
}
