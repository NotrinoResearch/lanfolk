package portmap

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

// errNoGateway is returned by DefaultGateway when no IPv4 default route exists.
var errNoGateway = errors.New("portmap: no IPv4 default gateway")

// parseProcNetRoute returns the gateway of the best default route in the
// text of Linux's /proc/net/route. Among usable default routes (destination
// 0.0.0.0, mask 0.0.0.0, flags UP and GATEWAY, non-zero gateway) the one with
// the lowest metric wins.
func parseProcNetRoute(r io.Reader) (netip.Addr, error) {
	const (
		rtfUp      = 0x1
		rtfGateway = 0x2
	)
	var best netip.Addr
	bestMetric := int64(-1)
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&rtfUp == 0 || flags&rtfGateway == 0 {
			continue
		}
		gw, err := strconv.ParseUint(f[2], 16, 32)
		if err != nil || gw == 0 {
			continue
		}
		metric, err := strconv.ParseInt(f[6], 10, 64)
		if err != nil {
			continue
		}
		// The kernel prints the address as a host-endian hex word of
		// the network-order bytes, which is little-endian on the
		// platforms Lanfolk supports.
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(gw))
		if bestMetric < 0 || metric < bestMetric {
			best, bestMetric = netip.AddrFrom4(b), metric
		}
	}
	if err := sc.Err(); err != nil {
		return netip.Addr{}, err
	}
	if !best.IsValid() {
		return netip.Addr{}, errNoGateway
	}
	return best, nil
}
