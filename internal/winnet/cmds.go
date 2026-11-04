package winnet

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Network profile retry policy: the Network Location Awareness service needs
// a few seconds before it knows a freshly created adapter.
const (
	privateRetryTotal    = 15 * time.Second
	privateRetryInterval = time.Second
)

// setPrivateCommand builds the command that marks the network of the interface
// with the given index as Private.
func setPrivateCommand(ifIndex int) Command {
	return powershellCommand(fmt.Sprintf(
		"Set-NetConnectionProfile -InterfaceIndex %d -NetworkCategory Private", ifIndex))
}

// retryUntil calls attempt until it succeeds, the total time is used up or ctx
// ends. It returns the last error.
func retryUntil(ctx context.Context, total, interval time.Duration, attempt func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	var last error
	for {
		last = attempt(ctx)
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("giving up after %v: %w", total, last)
		case <-time.After(interval):
		}
	}
}

// validLinuxIfName accepts what the kernel accepts (at most 15 bytes, no
// slash or white space) and nothing that ip could take for an option.
func validLinuxIfName(name string) error {
	if name == "" || len(name) > 15 || strings.HasPrefix(name, "-") ||
		strings.ContainsAny(name, "/ \t\r\n\x00") {
		return errors.New("winnet: invalid interface name " + strconv.Quote(name))
	}
	return nil
}

// linuxConfigureCommands returns the ip commands that give the interface its
// address and MTU and bring it up. "addr replace" is idempotent and the kernel
// adds the connected route itself; no default route is created.
func linuxConfigureCommands(name string, addr netip.Prefix, mtu int) []Command {
	return []Command{
		{Name: "ip", Args: []string{"-4", "addr", "replace", addr.String(), "dev", name}},
		{Name: "ip", Args: []string{"link", "set", "dev", name, "mtu", strconv.Itoa(mtu), "up"}},
	}
}

// linuxIPv6DisablePath is the sysctl file that disables IPv6 on the interface.
func linuxIPv6DisablePath(name string) string {
	return "/proc/sys/net/ipv6/conf/" + name + "/disable_ipv6"
}
