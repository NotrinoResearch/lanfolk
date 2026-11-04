//go:build nomock

package main

import "errors"

// startMock is not available in builds made with -tags nomock.
func startMock() (func(), string, error) {
	return nil, "", errors.New("this build does not include the -mock service")
}
