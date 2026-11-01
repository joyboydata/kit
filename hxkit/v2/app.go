package v2

import (
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

// LayoutFunc wraps a body component in the app shell. It runs only for
// direct (non-HTMX) visits; HTMX swaps receive the body alone. A nil
// layout means the app has no shell (e.g. layout-less file serving).
type LayoutFunc func(templ.Component) templ.Component

// App is the unit of configuration for one app shape: the layout every
// resource shares plus the error policy every handler shares. Subdomain
// routing stays outside kit; an app needing a different shell or error
// map is a different App.
type App struct {
	Layout LayoutFunc
	Errors Errors

	Resources []Resource

	logger *slog.Logger
}

// log returns the Register-configured logger, falling back to the
// process default so framework deprecations stay visible.

// Option configures Register behavior.
type Option func(*options)

type options struct {
	logger *slog.Logger
}

// WithLogger enables structured request logging for all routes Register
// adds, with the same record shape as hxkit v1: HTMX requests log the
// HX-Target/HX-Trigger request headers and the HX-Retarget/HX-Reswap/
// HX-Trigger/HX-Redirect response headers; every record carries the
// path, status, duration, and content length at INFO below 400, WARN
// for 4xx, ERROR for 5xx.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		o.logger = logger
	}
}

// Route is one method+path pair Register adds to Echo. Dump it in tests
// and deploy checks to audit the final table — a misrouted PATCH /:id
// sitting at root is what this dump is for.
type Route struct {
	Method string
	Path   string
}

// Routes returns the method+path table Register will add, with mutation
// and stream sub-paths already joined to their resource. It shares the
// computation with Register so the audit can never drift from reality.
func (a App) Routes() []Route {
	var out []Route
	for _, r := range a.Resources {
		// A representation-less resource (a stream owner like the
		// status endpoint) serves no GET of its own; registering one
		// would only add a guaranteed-404 route — and collide with an
		// empty-path stream joined to the same path.
		if len(r.Representations) > 0 {
			out = append(out, Route{Method: http.MethodGet, Path: r.Path})
		}
		for _, m := range r.Mutations {
			method := m.Method
			if method == "" {
				method = http.MethodPost
			}
			out = append(out, Route{Method: method, Path: joinPaths(r.Path, m.Path)})
		}
		for _, s := range r.Streams {
			out = append(out, Route{Method: http.MethodGet, Path: joinPaths(r.Path, s.Path)})
		}
	}
	return out
}

// Register adds every resource, mutation, and stream route to e.
func (a App) Register(e *echo.Echo, opts ...Option) {
	if e == nil {
		return
	}

	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}
	a.logger = cfg.logger

	for _, r := range a.Resources {
		if len(r.Representations) > 0 {
			h := a.serveResource(r)
			if cfg.logger != nil {
				h = wrapHandler(h, cfg.logger)
			}
			e.GET(r.Path, h)
		}

		for _, m := range r.Mutations {
			method := m.Method
			if method == "" {
				method = http.MethodPost
			}
			mh := a.serveMutation(r, m)
			if cfg.logger != nil {
				mh = wrapHandler(mh, cfg.logger)
			}
			e.Add(method, joinPaths(r.Path, m.Path), mh)
		}

		for _, s := range r.Streams {
			sh := a.serveStream(s)
			if cfg.logger != nil {
				sh = wrapHandler(sh, cfg.logger)
			}
			e.GET(joinPaths(r.Path, s.Path), sh)
		}
	}
}

// wrapHandler decorates a handler with structured request logging.
//
// @log.desc: Logged for every v2 resource, mutation, or stream request.
// @log.desc: For HTMX requests, includes request and response HTMX headers
// @log.desc: to debug client-side behavior. Level is INFO (<400), WARN (4xx),
// @log.desc: ERROR (>=500).
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

		attrs := []slog.Attr{
			slog.String("type", "v2.request"),               // @log.field: Event type identifier
			slog.String("path", c.Request().URL.Path),       // @log.field: Request URL path
			slog.Bool("htmx", isHTMX),                       // @log.field: Whether the request carries HX-Request
			slog.Int("status", status),                      // @log.field: HTTP status code
			slog.Duration("duration", time.Since(start)),    // @log.field: Request duration
			slog.Int64("content_length", c.Response().Size), // @log.field: Response body size in bytes
		}
		if isHTMX {
			attrs = append(attrs,
				slog.String("target", c.Request().Header.Get("HX-Target")),               // @log.field: HX-Target header — element to replace
				slog.String("trigger", c.Request().Header.Get("HX-Trigger")),             // @log.field: HX-Trigger header — element that initiated request
				slog.String("retarget", c.Response().Header().Get("HX-Retarget")),        // @log.field: HX-Retarget response header — overrides target
				slog.String("reswap", c.Response().Header().Get("HX-Reswap")),            // @log.field: HX-Reswap response header — swap strategy override
				slog.String("response_trigger", c.Response().Header().Get("HX-Trigger")), // @log.field: HX-Trigger response header — events to fire client-side
				slog.String("redirect", c.Response().Header().Get("HX-Redirect")),        // @log.field: HX-Redirect response header — client-side redirect URL
			)
		}
		logger.LogAttrs(c.Request().Context(), level, "v2 request", attrs...)

		return err
	}
}

// IsHTMX reports whether the request carries the HX-Request header.
func IsHTMX(c echo.Context) bool {
	return strings.EqualFold(c.Request().Header.Get("HX-Request"), "true")
}

func (a App) log() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

// joinPaths joins a resource path and a sub-path. The sub-path is always
// relative: a leading "/" is stripped, so "/:id" and ":id" both land
// under the resource, and "" means the resource path itself. There is
// deliberately no absolute override — a leading-/ typo used to register
// PATCH /:id at root and 404 the real route with no signal, so the
// override class was removed instead of warned about. Off-prefix
// endpoints are declared as their own Resource.
func joinPaths(base string, p string) string {
	p = strings.TrimLeft(p, "/")
	if p == "" {
		if base == "" {
			return "/"
		}
		return base
	}
	if base == "" {
		return "/" + p
	}
	return path.Clean(base + "/" + p)
}
