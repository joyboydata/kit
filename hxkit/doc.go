/*
Package hxkit provides a thin, opinionated layer over Echo for building
HTMX-driven web applications with Go.

It eliminates wiring boilerplate shared across projects: organizing
HTMX pages and fragments into declarative modules, rendering templ
components, and structured request logging.

hxkit does NOT replace Echo or hide it from you. echo.Context is passed
directly to your handlers. You configure Echo as you normally would
(middleware, static files, auth). hxkit just takes care of the patterns
that are identical in every HTMX project.

Usage
-----

Define a module:

	import (
	    "github.com/joyboydata/kit/hxkit"
	    "github.com/labstack/echo/v4"
	)

	var Landing = hxkit.Module{
	    BasePath: "",
	    Pages: []hxkit.Page{
	        {Method: http.MethodGet, Pattern: "/", Handler: landingPage},
	    },
	    Fragments: []hxkit.Fragment{
	        {Name: "apps-list", Method: http.MethodGet, Handler: appsList},
	        {Name: "upload", Handler: handleUpload},
	    },
	}

Register it:

	e := echo.New()
	e.Static("/static", "static")
	Landing.Register(e, hxkit.WithLogger(slog.Default()))

Fragments are automatically protected by requireHTMX — non-HTMX requests
receive a 404. Fragment method defaults to POST. Page method defaults to GET.

Render a templ component:

	func landingPage(c echo.Context) error {
	    return hxkit.Render(c, http.StatusOK, helloPage("World"))
	}

Logging
-------

When WithLogger is provided, every request is logged with:

	component=hxkit   — identifies the log source
	status            — HTTP response status code
	duration          — time to serve the request

HTMX requests also log the HX-Target and HX-Trigger request headers (at DEBUG
level before the handler runs).

The request context is passed to slog's LogAttrs. If you configure a
context-aware slog handler (e.g., one that extracts request_id from context),
those values will appear in hxkit log records automatically.

Example: request_id propagation

	// Your slog handler picks up request_id from context.
	type contextHandler struct {
	    inner slog.Handler
	}
	func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	    if id, ok := ctx.Value("request_id").(string); ok {
	        r.AddAttrs(slog.String("request_id", id))
	    }
	    return h.inner.Handle(ctx, r)
	}

	// Your middleware generates a request ID and stores it in context.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
	    return func(c echo.Context) error {
	        ctx := context.WithValue(c.Request().Context(), "request_id", uuid.New().String())
	        c.SetRequest(c.Request().WithContext(ctx))
	        return next(c)
	    }
	})

	hxkit's wrapHandler passes c.Request().Context() to LogAttrs. With a
context-aware handler, every hxkit log line will include the request ID
without hxkit needing to know about it directly.
*/
package hxkit
