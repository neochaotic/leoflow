package domain

import "errors"

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("resource not found")

// ErrConflict is returned when a write conflicts with an existing resource (e.g.
// a duplicate dag run for the same logical date). The API maps it to 409.
var ErrConflict = errors.New("resource already exists")

// ErrValidation is returned when input fails a business-rule check that the
// caller can fix (e.g. creating a user with a role that does not exist). The API
// maps it to 400.
var ErrValidation = errors.New("invalid input")
