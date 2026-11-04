// Package version holds build metadata set with -ldflags.
package version

import "strconv"

// Version is the release version, for example "1.0.0". Set at build time:
//
//	-ldflags "-X github.com/NotrinoResearch/lanfolk/internal/version.Version=1.0.0"
var Version = "0.0.0-dev"

// Default relay, injected at build time from the local, untracked build
// configuration (docs/DECISIONS.md D-012). The repository never contains a
// real relay address:
//
//	-X github.com/NotrinoResearch/lanfolk/internal/version.DefaultRelayHost=<host>
//	-X github.com/NotrinoResearch/lanfolk/internal/version.DefaultRelayUDPPort=3478
//	-X github.com/NotrinoResearch/lanfolk/internal/version.DefaultRelayTCPPort=443
var (
	DefaultRelayHost    = ""
	DefaultRelayUDPPort = "3478"
	DefaultRelayTCPPort = "443"
)

// ProtocolVersion is the peer protocol version announced in Hello messages.
const ProtocolVersion = 1

// DefaultRelay returns the build-time default relay; ok is false when the
// build has none.
func DefaultRelay() (host string, udpPort, tcpPort int, ok bool) {
	udpPort, err := strconv.Atoi(DefaultRelayUDPPort)
	if err != nil || udpPort <= 0 || udpPort > 65535 {
		udpPort = 3478
	}
	tcpPort, err = strconv.Atoi(DefaultRelayTCPPort)
	if err != nil || tcpPort < 0 || tcpPort > 65535 {
		tcpPort = 443
	}
	return DefaultRelayHost, udpPort, tcpPort, DefaultRelayHost != ""
}
