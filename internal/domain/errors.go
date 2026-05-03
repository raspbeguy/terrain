package domain

import "errors"

// Sentinel errors returned by Backend implementations. Callers should compare
// with errors.Is to allow wrapping.
var (
	ErrNotFound       = errors.New("not found")
	ErrNotImplemented = errors.New("not implemented for this backend")
	ErrUnauthorized   = errors.New("unauthorized")
	ErrConflict       = errors.New("conflict")
)
