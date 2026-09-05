// Package logkit provides structured logging utilities.
//
// It includes:
//   - LoggedError: wraps an error with structured slog.Attr metadata for
//     centralized error logging in HTTP middleware.
//
// # @log. Convention
//
// Every log call using slog can be annotated with @log. tags in comments.
// These tags are the single source of truth for the log schema documentation.
// A code generator (x/logkit/cmd/logdoc) reads them and produces
// docs/logging.md.
//
// ## @log.desc
//
// Describes what the log event represents. Place it in a comment line
// immediately above the log call. Multiline descriptions are supported by
// repeating the tag on consecutive lines.
//
//	// @log.desc: Logged for every HTTP request.
//	// @log.desc: Generates a unique request_id for correlation.
//	logger.LogAttrs(ctx, level, "request completed",
//	    ...
//	)
//
// ## @log.field
//
// Describes a single field in the log event. Place it as an inline or
// end-of-line comment on the same line as the slog.Xxx() call.
//
//	slog.String("request_id", reqID),  // @log.field: Unique request identifier
//	slog.String("method", c.Request().Method), // @log.field: HTTP method
//	slog.Int("status", status),         // @log.field: HTTP status code
//	slog.Duration("duration", duration), // @log.field: Request duration
//	slog.String("error", err.Error())   // @log.field: Error message (if any)
//
// ## Type index
//
// Every structured log event MUST include a "type" field with a unique,
// descriptive value (e.g. "app.deploy", "http.request"). The generator
// validates this and exits with an error if any event is missing a type.
//
// The type index documents all known type values and their source locations
// across the project.
package logkit