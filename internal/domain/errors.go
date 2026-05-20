package domain

import "errors"

var (
	ErrNotFound       = errors.New("not found")
	ErrNotImplemented = errors.New("not implemented for this backend")
	ErrUnauthorized   = errors.New("unauthorized")
	ErrConflict       = errors.New("conflict")
)
