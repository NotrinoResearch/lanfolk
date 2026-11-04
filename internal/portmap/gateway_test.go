package portmap

import (
	"strings"
	"testing"
)

func TestDefaultGateway(t *testing.T) {
	gw, err := DefaultGateway()
	if err != nil {
		t.Skipf("no default gateway on this machine: %v", err)
	}
	if !gw.IsValid() || !gw.Is4() || gw.IsUnspecified() {
		t.Fatalf("DefaultGateway() = %v, want a valid IPv4 address", gw)
	}
	t.Logf("default gateway %v", gw)
}

func TestParseProcNetRoute(t *testing.T) {
	const table = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0100A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"wlan0\t00000000\t0101A8C0\t0003\t0\t0\t50\t00000000\t0\t0\t0\n" +
		"tun0\t00000000\t0100000A\t0001\t0\t0\t10\t00000000\t0\t0\t0\n" + // no RTF_GATEWAY
		"eth0\t0000A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"
	gw, err := parseProcNetRoute(strings.NewReader(table))
	if err != nil {
		t.Fatal(err)
	}
	if gw.String() != "192.168.1.1" {
		t.Errorf("gateway %v, want 192.168.1.1 (lowest metric among usable default routes)", gw)
	}
	if _, err := parseProcNetRoute(strings.NewReader("Iface\tDestination\n")); err == nil {
		t.Error("expected an error without default route")
	}
}
