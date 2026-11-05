//go:build windows

package main

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// Signed-in users may start the service but not stop or change it.
func TestServiceSDDL(t *testing.T) {
	sd, err := windows.SecurityDescriptorFromString(serviceSDDL)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sd.DACL(); err != nil {
		t.Fatal(err)
	}
	for _, ace := range strings.SplitAfter(serviceSDDL[2:], ")") {
		if !strings.HasSuffix(ace, ";;;IU)") {
			continue
		}
		rights := strings.Split(ace, ";")[2]
		if !strings.Contains(rights, "RP") {
			t.Errorf("interactive users cannot start the service: %s", ace)
		}
		for _, r := range []string{"WP", "DC", "WD", "WO", "SD"} {
			if strings.Contains(rights, r) {
				t.Errorf("interactive users get %s: %s", r, ace)
			}
		}
		return
	}
	t.Fatal("no entry for interactive users")
}
