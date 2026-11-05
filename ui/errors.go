package main

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

// Error codes the UI adds to the service's codes.
const (
	CodeNotRunning = "not_running" // no connection to the service
	CodeTimeout    = "timeout"     // the service did not answer in time
	CodeLocal      = "local"       // a problem on this computer (file, clipboard)
)

// UserError is an error whose text can be shown to the user as is. Wails
// sends err.Error() to the frontend, where it rejects the promise.
type UserError struct {
	Code    string
	Message string
}

func (e *UserError) Error() string { return e.Message }

var (
	errNotRunning     = &UserError{Code: CodeNotRunning, Message: "The Lanfolk service is not running. Open Lanfolk or choose Try again to start it; it also starts with Windows."}
	errWindowNotReady = &UserError{Code: CodeLocal, Message: "The window is not ready yet. Try again in a moment."}
)

// userError converts any error from the IPC layer into a UserError.
func userError(err error) error {
	if err == nil {
		return nil
	}
	var ue *UserError
	if errors.As(err, &ue) {
		return err
	}
	var ie *ipc.Error
	if errors.As(err, &ie) {
		return &UserError{Code: ie.Code, Message: userMessage(ie)}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &UserError{Code: CodeTimeout, Message: "The Lanfolk service did not answer in time. Try again."}
	}
	return &UserError{Code: CodeNotRunning, Message: "Lost the connection to the Lanfolk service. Reconnecting…"}
}

// userMessage returns readable text for an error returned by the service.
func userMessage(err error) string {
	var ie *ipc.Error
	if !errors.As(err, &ie) {
		return capitalize(err.Error())
	}
	m := strings.TrimSpace(ie.Message)
	switch ie.Code {
	case ipc.CodeInvalid:
		if m == "" {
			return "The service did not accept that input."
		}
		return capitalize(m)
	case ipc.CodeNotFound:
		return "Not found: " + orDefault(m, "the item no longer exists")
	case ipc.CodeForbidden:
		return "Not allowed: " + orDefault(m, "you do not have permission for this")
	case ipc.CodeUnauthorized:
		return "Lanfolk can only be controlled by the user who is signed in on this computer."
	case ipc.CodeUnavailable:
		return "Not available right now: " + orDefault(m, "the service cannot do this yet")
	case ipc.CodeInternal:
		return "The Lanfolk service reported an internal error: " + orDefault(m, "no details")
	}
	return capitalize(orDefault(m, "The service reported an unknown error."))
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
