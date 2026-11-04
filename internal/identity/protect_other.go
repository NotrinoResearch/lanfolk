//go:build !windows

package identity

import "errors"

// Protector encrypts the identity seed at rest.
type Protector interface {
	Protect(secret []byte) ([]byte, error)
	Unprotect(blob []byte) ([]byte, error)
}

// fileProtector is used on non-Windows builds (relay hosts, Linux test
// clients). It relies on file permissions only; the data directory is
// created with mode 0700.
type fileProtector struct{}

// DefaultProtector returns the platform protector.
func DefaultProtector() Protector { return fileProtector{} }

func (fileProtector) Protect(secret []byte) ([]byte, error) {
	return append([]byte("plain:"), secret...), nil
}

func (fileProtector) Unprotect(b []byte) ([]byte, error) {
	const prefix = "plain:"
	if len(b) <= len(prefix) || string(b[:len(prefix)]) != prefix {
		return nil, errors.New("unsupported identity blob")
	}
	return append([]byte(nil), b[len(prefix):]...), nil
}
