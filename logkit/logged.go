// Package logkit provides shared logging utilities for structured logging.
//
// It includes:
//   - LoggedError: wraps an error with structured slog.Attr metadata for
//     centralized error logging in HTTP middleware.
//   - LogDoc generator (subpackage logdoc): generates docs/logging.md from
//     @log.desc and @log.field annotations in source code.
package logkit

import (
	"errors"
	"log/slog"
)

// LoggedError wraps an error with structured log attributes.
// HTTP middleware can detect this type via errors.As and include the
// metadata in the request log event.
type LoggedError struct {
	Err   error
	Attrs []slog.Attr
}

func (e *LoggedError) Error() string { return e.Err.Error() }
func (e *LoggedError) Unwrap() error  { return e.Err }

// Logged wraps an error with structured log attributes for middleware logging.
// Use where you previously returned fmt.Errorf or a bare error — add relevant
// fields (appID, size, etc.) as metadata for centralized error logging.
//
//	// Before:
//	return fmt.Errorf("failed to get app: %w", err)
//
//	// After:
//	return logkit.Logged(fmt.Errorf("failed to get app: %w", err),
//	    slog.String("appID", appID),
//	)
//
// The HTTP RequestLoggerMiddleware will extract these attributes and include
// them in the http.request log event.
func Logged(err error, attrs ...slog.Attr) error {
	return &LoggedError{Err: err, Attrs: attrs}
}

// UnwrapAttrs extracts the structured attributes from a LoggedError, if any.
// Returns nil if err is not a *LoggedError.
func UnwrapAttrs(err error) []slog.Attr {
	var le *LoggedError
	if errors.As(err, &le) {
		return le.Attrs
	}
	return nil
}