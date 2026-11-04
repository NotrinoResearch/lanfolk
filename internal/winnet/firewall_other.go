//go:build !windows

package winnet

// EnsureFirewallRules is a no-op outside Windows. On Linux test clients the
// network namespaces have no firewall, and the userspace packet filter
// enforces the same denylist.
func EnsureFirewallRules(serviceExe string) error { return nil }

// RemoveFirewallRules is a no-op outside Windows.
func RemoveFirewallRules() error { return nil }
