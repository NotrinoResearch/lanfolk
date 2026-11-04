package lanprobe

import (
	"net/netip"
	"strings"
	"testing"
)

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

// WriteResultsTo logs the results through t.
func WriteResultsTo(t *testing.T, rs []Result) {
	t.Helper()
	var sb strings.Builder
	WriteResults(&sb, rs)
	t.Log("\n" + sb.String())
}
