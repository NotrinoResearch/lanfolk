package winnet

import (
	"errors"
	"net/netip"
	"strings"
)

// VirtualNetwork is the address range of every Lanfolk private network. The
// firewall rules are scoped by address, so they work before the adapter
// exists and survive adapter recreation.
var VirtualNetwork = netip.MustParsePrefix("10.77.0.0/24")

// envServiceExe carries the service executable path into the PowerShell
// script. The path never becomes part of the script text.
const envServiceExe = "LANFOLK_EXE"

// Firewall rule names (unique IDs inside the Lanfolk group).
const (
	ruleInService = "Lanfolk-In-Service-UDP"
	ruleInAllow   = "Lanfolk-In-Allow-VirtualLAN"
	ruleInBlockT  = "Lanfolk-In-Block-TCP"
	ruleInBlockU  = "Lanfolk-In-Block-UDP"
	ruleOutBlockT = "Lanfolk-Out-Block-TCP"
	ruleOutBlockU = "Lanfolk-Out-Block-UDP"
)

// portList renders ranges as a PowerShell string array: @('135','5985-5986').
func portList(ranges []PortRange) string {
	parts := make([]string, len(ranges))
	for i, r := range ranges {
		parts[i] = psQuote(r.String())
	}
	return "@(" + strings.Join(parts, ",") + ")"
}

func isWindowsAbs(p string) bool {
	if strings.ContainsRune(p, 0) {
		return false
	}
	if strings.HasPrefix(p, `\\`) {
		return len(p) > 2
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z')
}

// ensureFirewallCommand builds the command that (re)creates all Lanfolk
// firewall rules. It first removes every rule of the group, which makes the
// operation idempotent.
//
// Rules:
//  1. Allow inbound UDP to the service executable on all profiles (hole
//     punching, manual port).
//  2. Allow inbound from the virtual network to the virtual network for any
//     program. The local address list also covers the broadcast and multicast
//     destinations, because injected broadcast datagrams are addressed to
//     255.255.255.255 or the group address, not to the member's own address.
//     The remote address still restricts the traffic to members.
//  3. Block inbound from the virtual network to the denylisted ports.
//  4. Block outbound to the virtual network on the same ports.
//
// Windows Defender Firewall evaluates block rules before allow rules, so 3 and
// 4 win over 2.
func ensureFirewallCommand(serviceExe string) (Command, error) {
	if !isWindowsAbs(serviceExe) {
		return Command{}, errors.New("winnet: service executable path must be absolute")
	}
	net := psQuote(VirtualNetwork.String())
	g := psQuote(FirewallGroup)
	newRule := func(name, display string, params ...string) string {
		return "New-NetFirewallRule -Name " + psQuote(name) + " -DisplayName " + psQuote(display) +
			" -Group " + g + " -Profile Any -Enabled True " + strings.Join(params, " ") + " | Out-Null"
	}
	steps := []string{
		removeGroupScript(),
		newRule(ruleInService, "Lanfolk service (UDP in)",
			"-Direction Inbound -Action Allow -Protocol UDP -Program $env:"+envServiceExe),
		newRule(ruleInAllow, "Lanfolk virtual LAN (in)",
			"-Direction Inbound -Action Allow -Protocol Any -LocalAddress @("+net+",'255.255.255.255','224.0.0.0/4') -RemoteAddress "+net),
		newRule(ruleInBlockT, "Lanfolk block system services TCP (in)",
			"-Direction Inbound -Action Block -Protocol TCP -LocalPort "+portList(BlockedTCPPorts)+" -RemoteAddress "+net),
		newRule(ruleInBlockU, "Lanfolk block system services UDP (in)",
			"-Direction Inbound -Action Block -Protocol UDP -LocalPort "+portList(BlockedUDPPorts)+" -RemoteAddress "+net),
		newRule(ruleOutBlockT, "Lanfolk block system services TCP (out)",
			"-Direction Outbound -Action Block -Protocol TCP -RemotePort "+portList(BlockedTCPPorts)+" -RemoteAddress "+net),
		newRule(ruleOutBlockU, "Lanfolk block system services UDP (out)",
			"-Direction Outbound -Action Block -Protocol UDP -RemotePort "+portList(BlockedUDPPorts)+" -RemoteAddress "+net),
	}
	return powershellCommand(strings.Join(steps, "; "), envServiceExe+"="+serviceExe), nil
}

// removeGroupScript deletes all rules of the Lanfolk group, if there are any.
func removeGroupScript() string {
	return "$old = Get-NetFirewallRule -Group " + psQuote(FirewallGroup) +
		" -ErrorAction SilentlyContinue; if ($old) { $old | Remove-NetFirewallRule }"
}

func removeFirewallCommand() Command {
	return powershellCommand(removeGroupScript())
}
