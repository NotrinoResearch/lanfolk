package winnet

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIsBlocked(t *testing.T) {
	tests := []struct {
		proto uint8
		port  uint16
		want  bool
	}{
		{6, 135, true}, {6, 139, true}, {6, 445, true}, {6, 3389, true},
		{6, 5985, true}, {6, 5986, true},
		{6, 134, false}, {6, 136, false}, {6, 138, false}, {6, 140, false},
		{6, 444, false}, {6, 446, false}, {6, 3388, false}, {6, 3390, false},
		{6, 5984, false}, {6, 5987, false}, {6, 0, false}, {6, 80, false},
		{6, 25565, false}, {6, 65535, false},
		{17, 137, true}, {17, 138, true},
		{17, 136, false}, {17, 139, false}, {17, 445, false}, {17, 3389, false},
		{17, 27015, false},
		{1, 135, false}, {0, 445, false}, {47, 137, false},
	}
	for _, tc := range tests {
		if got := IsBlocked(tc.proto, tc.port); got != tc.want {
			t.Errorf("IsBlocked(%d, %d) = %v, want %v", tc.proto, tc.port, got, tc.want)
		}
	}
}

func TestPortRangeString(t *testing.T) {
	if got := (PortRange{135, 135}).String(); got != "135" {
		t.Errorf("got %q", got)
	}
	if got := (PortRange{5985, 5986}).String(); got != "5985-5986" {
		t.Errorf("got %q", got)
	}
}

func TestMetricConstants(t *testing.T) {
	// Physical adapters get automatic metrics in the low tens; the virtual
	// adapter must stay far above them normally and below them in compat mode.
	if NormalMetric < 1000 {
		t.Errorf("NormalMetric %d too low to keep the physical adapter preferred", NormalMetric)
	}
	if CompatMetric > 5 || CompatMetric == 0 {
		t.Errorf("CompatMetric %d should be a very low non-zero metric", CompatMetric)
	}
	if metricFor(true) != CompatMetric || metricFor(false) != NormalMetric {
		t.Error("metricFor mismatch")
	}
}

func TestValidateConfigure(t *testing.T) {
	good := netip.MustParsePrefix("10.77.0.3/24")
	if err := validateConfigure(good, 1380); err != nil {
		t.Errorf("good: %v", err)
	}
	if validateConfigure(netip.Prefix{}, 1380) == nil {
		t.Error("invalid prefix accepted")
	}
	if validateConfigure(netip.MustParsePrefix("fd00::1/64"), 1380) == nil {
		t.Error("IPv6 accepted")
	}
	if validateConfigure(good, 100) == nil || validateConfigure(good, 70000) == nil {
		t.Error("bad MTU accepted")
	}
}

func decodeScript(t *testing.T, c Command) string {
	t.Helper()
	if len(c.Args) == 0 || c.Args[len(c.Args)-2] != "-Command" {
		t.Fatalf("expected -Command as second to last arg: %v", c.Args)
	}
	script := c.Args[len(c.Args)-1]
	if strings.ContainsRune(script, '"') {
		t.Errorf("script contains a double quote: %s", script)
	}
	return script
}

func TestEnsureFirewallCommand(t *testing.T) {
	exe := `C:\Program Files\Lanfolk\lanfolk-service.exe`
	c, err := ensureFirewallCommand(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"-NoProfile", "-NonInteractive"} {
		found := false
		for _, x := range c.Args {
			found = found || x == a
		}
		if !found {
			t.Errorf("missing %s in %v", a, c.Args)
		}
	}
	// The executable path must travel through the environment only.
	if len(c.Env) != 1 || c.Env[0] != "LANFOLK_EXE="+exe {
		t.Errorf("env = %v", c.Env)
	}
	script := decodeScript(t, c)
	if strings.Contains(script, "Program Files") {
		t.Error("service path leaked into the script text")
	}
	for _, want := range []string{
		"$ErrorActionPreference = 'Stop'",
		// Idempotence: the group is cleared first.
		"Get-NetFirewallRule -Group 'Lanfolk'",
		"Remove-NetFirewallRule",
		// Rule 1.
		"-Name 'Lanfolk-In-Service-UDP'",
		"-Direction Inbound -Action Allow -Protocol UDP -Program $env:LANFOLK_EXE",
		// Rule 2.
		"-Direction Inbound -Action Allow -Protocol Any -LocalAddress @('10.77.0.0/24','255.255.255.255','224.0.0.0/4') -RemoteAddress '10.77.0.0/24'",
		// Rule 3.
		"-Direction Inbound -Action Block -Protocol TCP -LocalPort @('135','139','445','3389','5985-5986') -RemoteAddress '10.77.0.0/24'",
		"-Direction Inbound -Action Block -Protocol UDP -LocalPort @('137-138') -RemoteAddress '10.77.0.0/24'",
		// Rule 4.
		"-Direction Outbound -Action Block -Protocol TCP -RemotePort @('135','139','445','3389','5985-5986') -RemoteAddress '10.77.0.0/24'",
		"-Direction Outbound -Action Block -Protocol UDP -RemotePort @('137-138') -RemoteAddress '10.77.0.0/24'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q\n%s", want, script)
		}
	}
	if n := strings.Count(script, "New-NetFirewallRule"); n != 6 {
		t.Errorf("New-NetFirewallRule count = %d, want 6", n)
	}
	if n := strings.Count(script, "-Group 'Lanfolk'"); n != 7 { // 6 rules + lookup
		t.Errorf("group count = %d, want 7", n)
	}
	if n := strings.Count(script, "-Profile Any"); n != 6 {
		t.Errorf("profile count = %d, want 6", n)
	}
	// No rule may open the whole machine to the internet.
	if strings.Contains(script, "0.0.0.0/0") || strings.Contains(script, "-RemoteAddress Any") {
		t.Error("rule is not scoped to the virtual network")
	}
}

func TestEnsureFirewallCommandRejectsBadPath(t *testing.T) {
	for _, p := range []string{"", "lanfolk.exe", `.\lanfolk.exe`, `\x`, "C:", "/usr/bin/x", "C:\\a\x00b"} {
		if _, err := ensureFirewallCommand(p); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
	for _, p := range []string{`C:\x.exe`, `c:/x.exe`, `\\server\share\x.exe`} {
		if _, err := ensureFirewallCommand(p); err != nil {
			t.Errorf("path %q rejected: %v", p, err)
		}
	}
}

func TestPathWithQuoteStaysOutOfScript(t *testing.T) {
	c, err := ensureFirewallCommand(`C:\a'; Remove-Item -Recurse C:\; '\x.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(decodeScript(t, c), "Remove-Item") {
		t.Error("path text reached the script")
	}
}

func TestRemoveFirewallCommand(t *testing.T) {
	script := decodeScript(t, removeFirewallCommand())
	if !strings.Contains(script, "Get-NetFirewallRule -Group 'Lanfolk'") ||
		!strings.Contains(script, "Remove-NetFirewallRule") ||
		strings.Contains(script, "New-NetFirewallRule") {
		t.Errorf("unexpected script: %s", script)
	}
}

func TestPSQuote(t *testing.T) {
	if got := psQuote("a'b"); got != "'a''b'" {
		t.Errorf("got %s", got)
	}
}

func TestSetPrivateCommand(t *testing.T) {
	script := decodeScript(t, setPrivateCommand(17))
	if !strings.Contains(script, "Set-NetConnectionProfile -InterfaceIndex 17 -NetworkCategory Private") {
		t.Errorf("script: %s", script)
	}
}

func TestRetryUntil(t *testing.T) {
	calls := 0
	err := retryUntil(context.Background(), time.Second, time.Millisecond, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("err=%v calls=%d", err, calls)
	}

	boom := errors.New("boom")
	err = retryUntil(context.Background(), 30*time.Millisecond, 5*time.Millisecond, func(context.Context) error { return boom })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want wrapped boom", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err = retryUntil(ctx, time.Minute, time.Minute, func(context.Context) error { return boom })
	if err == nil || time.Since(start) > time.Second {
		t.Errorf("cancelled retry: err=%v after %v", err, time.Since(start))
	}
}

func TestLinuxConfigureCommands(t *testing.T) {
	got := linuxConfigureCommands("Lanfolk", netip.MustParsePrefix("10.77.0.3/24"), 1380)
	want := []Command{
		{Name: "ip", Args: []string{"-4", "addr", "replace", "10.77.0.3/24", "dev", "Lanfolk"}},
		{Name: "ip", Args: []string{"link", "set", "dev", "Lanfolk", "mtu", "1380", "up"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	for _, c := range got {
		for _, a := range c.Args {
			if a == "default" || strings.Contains(a, "0.0.0.0/0") {
				t.Errorf("default route in %v", c)
			}
		}
	}
	if p := linuxIPv6DisablePath("Lanfolk"); p != "/proc/sys/net/ipv6/conf/Lanfolk/disable_ipv6" {
		t.Errorf("path %s", p)
	}
}

func TestValidLinuxIfName(t *testing.T) {
	for _, n := range []string{"Lanfolk", "tun0", "a.b"} {
		if err := validLinuxIfName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	for _, n := range []string{"", "-evil", "a/b", "a b", "0123456789abcdef", "a\nb"} {
		if validLinuxIfName(n) == nil {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestRunnerFake(t *testing.T) {
	old := runner
	defer func() { runner = old }()
	var got []Command
	runner = func(_ context.Context, c Command) ([]byte, error) {
		got = append(got, c)
		return nil, nil
	}
	if err := run(context.Background(), Command{Name: "x"}); err != nil || len(got) != 1 {
		t.Fatal("runner not used")
	}
}
