//go:build windows

package identity

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Protector encrypts the identity seed at rest.
type Protector interface {
	Protect(secret []byte) ([]byte, error)
	Unprotect(blob []byte) ([]byte, error)
}

// dpapiProtector uses Windows DPAPI bound to the account running the
// service (LocalSystem for the installed service).
type dpapiProtector struct{}

// DefaultProtector returns the platform protector: DPAPI on Windows.
func DefaultProtector() Protector { return dpapiProtector{} }

var dpapiEntropy = []byte("lanfolk-identity-v1")

func blob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func (dpapiProtector) Protect(secret []byte) ([]byte, error) {
	var out windows.DataBlob
	name, _ := windows.UTF16PtrFromString("Lanfolk identity")
	err := windows.CryptProtectData(blob(secret), name, blob(dpapiEntropy), 0, nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte("dpapi:"), unsafe.Slice(out.Data, out.Size)...), nil
}

func (dpapiProtector) Unprotect(b []byte) ([]byte, error) {
	const prefix = "dpapi:"
	if len(b) <= len(prefix) || string(b[:len(prefix)]) != prefix {
		return nil, errors.New("identity blob is not DPAPI protected")
	}
	b = b[len(prefix):]
	var out windows.DataBlob
	err := windows.CryptUnprotectData(blob(b), nil, blob(dpapiEntropy), 0, nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
