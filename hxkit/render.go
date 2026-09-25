package hxkit

import (
	"bytes"
	"net/http"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

// RenderPage renders a templ component, wrapping it with the provided layout
// function for direct (non-HTMX) requests. For HTMX requests (via hx-boost or
// explicit hx-get attributes), only the body is rendered — the layout is skipped.
//
// This enables SPA-style navigation where the server returns a full page for
// initial loads and just the body for in-app navigation, without duplicating
// handler logic or maintaining separate page/fragment endpoints.
//
// Usage:
//
//	func myHandler(c echo.Context) error {
//	    data := buildData(c)
//	    return hxkit.RenderPage(c, http.StatusOK, myBody(data), func(body templ.Component) templ.Component {
//	        return myLayout("Page Title", body)
//	    })
//	}
func RenderPage(c echo.Context, status int, body templ.Component, layoutFn func(templ.Component) templ.Component) error {
	if IsHTMX(c) {
		return Render(c, status, body)
	}
	return Render(c, status, layoutFn(body))
}

// Render buffers a templ.Component, renders it with the request context,
// and writes the output as HTML with the given status code.
//
// On render failure, Render behaves differently depending on the request type:
//   - HTMX requests receive an error fragment (suitable for swapping into a
//     notification area).
//   - Full-page requests receive a plain text error message.
//
// Usage:
//
//	func myHandler(c echo.Context) error {
//	    return hxkit.Render(c, http.StatusOK, myPage(data))
//	}
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
