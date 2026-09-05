package hxkit

import (
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// Module organizes a group of related HTMX routes under a common BasePath.
//
// Pages are full-page routes (e.g., GET /). Fragments are HTMX partials
// registered under {BasePath}/hx/{Name} that require the HX-Request header
// (non-HTMX requests get a 404).
//
// A module corresponds to one screen or feature area in your application.
type Module struct {
	// BasePath is the URL prefix for all routes in this module.
	// Examples: "", "/app", "/early-access".
	BasePath string

	// Pages is an optional list of full-page routes.
	Pages []Page

	// Fragments is an optional list of HTMX partial endpoints.
	Fragments []Fragment
}

// Page is a full-page route, typically rendered on initial navigation.
type Page struct {
	// Method is the HTTP method. Defaults to GET if empty.
	Method string

	// Pattern is the URL path relative to Module.BasePath.
	// Example: "/" becomes "/" with BasePath="" or "/app" with BasePath="/app".
	Pattern string

	// Handler is the Echo handler. It receives echo.Context directly.
	Handler echo.HandlerFunc
}

// Fragment is an HTMX endpoint that returns partial HTML to replace or
// modify a portion of the page. Non-HTMX requests receive HTTP 404.
type Fragment struct {
	// Name is the last path segment: {BasePath}/hx/{Name}.
	Name string

	// Method is the HTTP method. Defaults to POST if empty.
	Method string

	// Handler is the Echo handler. It receives echo.Context directly.
	Handler echo.HandlerFunc

	// Aliases are additional absolute paths that route to the same handler.
	// Useful for backwards compatibility when renaming fragments.
	Aliases []string
}

// Option configures Register behavior.
type Option func(*options)

type options struct {
	logger *slog.Logger
}

// WithLogger enables structured request logging for all handlers registered
// via this module.
//
// HTMX fragment requests log:
//   - type=htmx.fragment — event type identifier
//   - path — request URL path
//   - target, trigger — HTMX request headers
//   - retarget, reswap, response_trigger, redirect — HTMX response headers
//   - status, duration, content_length — standard request metadata
//
// Full page requests log:
//   - type=page.request — event type identifier
//   - path — request URL path
//   - status, duration, content_length — standard request metadata
//
// All fields are documented with @log.desc and @log.field annotations.
// Run go generate ./... to regenerate docs/logging.md.
//
// Example:
//
//	m := hxkit.Module{...}
//	m.Register(e, hxkit.WithLogger(slog.Default()))
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		o.logger = logger
	}
}

// Register adds all pages and fragments to an Echo instance.
//
// Pages are registered as-is. Fragments are wrapped with requireHTMX so
// they respond only to requests with the HX-Request header.
//
// If WithLogger was provided, all handlers are wrapped with structured
// request logging before registration.
func (m Module) Register(e *echo.Echo, opts ...Option) {
	if e == nil {
		return
	}

	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}

	for _, p := range m.Pages {
		if p.Method == "" {
			p.Method = http.MethodGet
		}
		handler := p.Handler
		if cfg.logger != nil {
			handler = wrapHandler(handler, cfg.logger)
		}
		e.Add(p.Method, joinPaths(m.BasePath, p.Pattern), handler)
	}

	for _, f := range m.Fragments {
		if f.Method == "" {
			f.Method = http.MethodPost
		}
		handler := f.Handler
		if cfg.logger != nil {
			handler = wrapHandler(handler, cfg.logger)
		}
		handler = requireHTMX(handler)
		e.Add(f.Method, joinPaths(m.BasePath, "/hx/"+f.Name), handler)
		for _, alias := range f.Aliases {
			e.Add(f.Method, alias, handler)
		}
	}
}

// wrapHandler decorates a handler with structured request logging.
//
// @log.desc: Logged for every HTMX fragment or page request served through hxkit.
// @log.desc: For HTMX fragments, includes request and response HTMX headers.
// @log.desc: For page requests, includes basic request metadata.
// @log.desc: Level is dynamic: INFO (<400), WARN (4xx), ERROR (>=500).
func wrapHandler(h echo.HandlerFunc, logger *slog.Logger) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		isHTMX := IsHTMX(c)
		err := h(c)

		status := c.Response().Status
		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		} else if status >= 400 {
			level = slog.LevelWarn
		}

		if isHTMX {
			// @log.desc: Logged when an HTMX fragment request is served.
			// @log.desc: Contains request headers (target, trigger) and response
			// @log.desc: headers (retarget, reswap, response_trigger, redirect)
			// @log.desc: to debug client-side behavior.
			logger.LogAttrs(c.Request().Context(), level, "htmx fragment",
				slog.String("type", "htmx.fragment"),                       // @log.field: Event type identifier
				slog.String("path", c.Request().URL.Path),                 // @log.field: Request URL path
				slog.String("target", c.Request().Header.Get("HX-Target")),        // @log.field: HX-Target header — element to replace
				slog.String("trigger", c.Request().Header.Get("HX-Trigger")),       // @log.field: HX-Trigger header — element that initiated request
				slog.String("retarget", c.Response().Header().Get("HX-Retarget")),     // @log.field: HX-Retarget response header — overrides target
				slog.String("reswap", c.Response().Header().Get("HX-Reswap")),        // @log.field: HX-Reswap response header — swap strategy override
				slog.String("response_trigger", c.Response().Header().Get("HX-Trigger")), // @log.field: HX-Trigger response header — events to fire client-side
				slog.String("redirect", c.Response().Header().Get("HX-Redirect")),      // @log.field: HX-Redirect response header — client-side redirect URL
				slog.Int("status", status),                                              // @log.field: HTTP status code
				slog.Duration("duration", time.Since(start)),                            // @log.field: Request duration
				slog.Int64("content_length", c.Response().Size),                            // @log.field: Response body size in bytes
			)
		} else {
			// @log.desc: Logged when a full page request is served.
			logger.LogAttrs(c.Request().Context(), level, "page request",
				slog.String("type", "page.request"),                       // @log.field: Event type identifier
				slog.String("path", c.Request().URL.Path),                 // @log.field: Request URL path
				slog.Int("status", status),                                 // @log.field: HTTP status code
				slog.Duration("duration", time.Since(start)),               // @log.field: Request duration
				slog.Int64("content_length", c.Response().Size),              // @log.field: Response body size in bytes
			)
		}

		return err
	}
}

// requireHTMX returns a handler that rejects non-HTMX requests with 404.
func requireHTMX(next echo.HandlerFunc) echo.HandlerFunc {
	if next == nil {
		return func(c echo.Context) error {
			return echo.NewHTTPError(http.StatusNotFound)
		}
	}
	return func(c echo.Context) error {
		if !IsHTMX(c) {
			return echo.NewHTTPError(http.StatusNotFound)
		}
		return next(c)
	}
}

// IsHTMX reports whether the request carries the HX-Request header.
func IsHTMX(c echo.Context) bool {
	return strings.EqualFold(c.Request().Header.Get("HX-Request"), "true")
}

// joinPaths joins a base path and a relative path, normalizing the result.
func joinPaths(base string, p string) string {
	if base == "" {
		if p == "" {
			return "/"
		}
		if strings.HasPrefix(p, "/") {
			return p
		}
		return "/" + p
	}
	if p == "" {
		return base
	}
	return path.Clean(base + "/" + strings.TrimPrefix(p, "/"))
}
