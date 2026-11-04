//go:build !windows

package secdir

import "os"

func harden(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return nil, os.Chmod(dir, 0o700)
}
