package controller

import (
	"errors"
	"fmt"
)

// ErrorCode is a stable, machine-readable controller failure code.
type ErrorCode string

const (
	ErrorCodeUnauthorized       ErrorCode = "UNAUTHORIZED"
	ErrorCodeUnsupportedVersion ErrorCode = "UNSUPPORTED_VERSION"
	ErrorCodeInvalidArgument    ErrorCode = "INVALID_ARGUMENT"
	ErrorCodeDeadlineExceeded   ErrorCode = "DEADLINE_EXCEEDED"
	ErrorCodeConflict           ErrorCode = "CONFLICT"
	ErrorCodeServiceUnavailable ErrorCode = "SERVICE_UNAVAILABLE"
	ErrorCodeDegraded           ErrorCode = "DEGRADED"
	ErrorCodeReauthRequired     ErrorCode = "REAUTH_REQUIRED"
	ErrorCodeResyncRequired     ErrorCode = "RESYNC_REQUIRED"
)

// Error is a structured controller failure. Detail must already be redacted and
// is intended for diagnostics rather than user-facing localization.
type Error struct {
	Code   ErrorCode
	Detail string
}

// Validate rejects unknown or missing stable codes.
func (e Error) Validate() error {
	if !e.Code.valid() {
		return fmt.Errorf("unknown controller error code %q", e.Code)
	}
	return nil
}

// Error implements the built-in error interface without manufacturing detail.
// A pointer receiver keeps one canonical structured-error representation.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Detail == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func (c ErrorCode) valid() bool {
	switch c {
	case ErrorCodeUnauthorized,
		ErrorCodeUnsupportedVersion,
		ErrorCodeInvalidArgument,
		ErrorCodeDeadlineExceeded,
		ErrorCodeConflict,
		ErrorCodeServiceUnavailable,
		ErrorCodeDegraded,
		ErrorCodeReauthRequired,
		ErrorCodeResyncRequired:
		return true
	default:
		return false
	}
}

// AsError returns a structured controller error when err carries one.
func AsError(err error) (*Error, bool) {
	var structured *Error
	if !errors.As(err, &structured) {
		return nil, false
	}
	return structured, true
}
