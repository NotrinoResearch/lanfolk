// Package version holds build metadata set with -ldflags.
package version

// Version is the release version, for example "1.0.0". Set at build time:
//
//	-ldflags "-X github.com/NotrinoResearch/lanfolk/internal/version.Version=1.0.0"
var Version = "0.0.0-dev"

// ProtocolVersion is the peer protocol version announced in Hello messages.
const ProtocolVersion = 1
