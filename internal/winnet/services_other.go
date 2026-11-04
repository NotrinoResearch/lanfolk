//go:build !windows

package winnet

// NewServiceFilter returns nil on platforms without Windows services; the
// engine then lets members reach every port that is not on the denylist.
func NewServiceFilter() *ServiceFilter { return nil }
