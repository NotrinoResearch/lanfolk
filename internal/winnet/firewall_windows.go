//go:build windows

package winnet

import "context"

// EnsureFirewallRules creates the Lanfolk Windows Defender Firewall rules
// (group "Lanfolk"). It is idempotent: existing rules of the group are
// replaced. It needs administrator rights.
func EnsureFirewallRules(serviceExe string) error {
	cmd, err := ensureFirewallCommand(serviceExe)
	if err != nil {
		return err
	}
	return run(context.Background(), cmd)
}

// RemoveFirewallRules deletes all rules of the Lanfolk group. Removing
// nothing is not an error.
func RemoveFirewallRules() error {
	return run(context.Background(), removeFirewallCommand())
}
