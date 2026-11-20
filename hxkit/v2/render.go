package v2

import (
	"bytes"
	"net/http"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

// Render buffers a templ.Component, renders it with the request context,
// and writes the output as HTML with the given status code.
//
// This is the escape hatch for routes that are not resources: marketing
// pages, branded 404s, logout pages. Resource routes render through App;
// everything else renders through here with plain Echo registration.
//
// On render failure, Render behaves differently depending on the request type:
//   - HTMX requests receive an error fragment (suitable for swapping into a
//     notification area).
//   - Full-page requests receive a plain text error message.
func Render(c echo.Context, status int, t templ.Component) error {
	var buf bytes.Buffer
	if err := t.Render(c.Request().Context(), &buf); err != nil {
		if IsHTMX(c) {
			return c.HTML(http.StatusInternalServerError, `<div role="alert" data-variant="error"><div><h6 style="margin:0">Something went wrong</h6><p class="text-light" style="margin-block-end:0">Try refreshing the page.</p></div></div>`)
		}
		return c.String(http.StatusInternalServerError, "failed to render template")
	}
	return c.HTML(status, buf.String())
}
