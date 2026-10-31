package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

// Errors is the canonical per-code HTMX wire map, configured once per
// App instead of re-implemented per handler:
//
//	402 → HX-Redirect to LoginPath with ?next= so the composer can
//	re-enable itself after login.
//	4xx → the status code plus an inline role=alert fragment.
//	5xx → empty body plus a global toast trigger; details stay server-side.
type Errors struct {
	// LoginPath is where 402 redirects land. Defaults to "/login".
	LoginPath string
}

func (e Errors) loginPath() string {
	if e.LoginPath == "" {
		return "/login"
	}
	return e.LoginPath
}

// writeError maps a Handler error onto the wire shape above. Direct
// (non-HTMX) visits get the same semantics without HTMX headers: a 303
// to login for 402, a layout-wrapped alert for 4xx, a generic message
// for 5xx.
func (a App) writeError(c echo.Context, err error) error {
	code, msg := statusFromErr(err)
	htmx := IsHTMX(c)

	if code == http.StatusPaymentRequired {
		to := a.Errors.loginPath() + "?next=" + url.QueryEscape(c.Request().URL.RequestURI())
		if htmx {
			c.Response().Header().Set("HX-Redirect", to)
			return c.NoContent(code)
		}
		return c.Redirect(http.StatusSeeOther, to)
	}

	if code >= 500 {
		if htmx {
			payload, _ := json.Marshal(map[string]string{"toast": msg})
			c.Response().Header().Set("HX-Trigger", string(payload))
			return c.NoContent(code)
		}
		return renderComponent(c, code, withLayout(a.Layout,
			alertFragment(http.StatusText(code), "Try refreshing the page.")))
	}

	frag := alertFragment(http.StatusText(code), msg)
	return renderComponent(c, code, withLayout(a.Layout, frag, htmx))
}

// withLayout wraps body in layout for direct visits; HTMX requests and
// nil layouts pass the body through untouched.
func withLayout(layout LayoutFunc, body templ.Component, htmx ...bool) templ.Component {
	for _, h := range htmx {
		if h {
			return body
		}
	}
	if layout == nil {
		return body
	}
	return layout(body)
}

// alertFragment is the shared inline error shape for 4xx responses.
func alertFragment(title, message string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w,
			`<div role="alert" data-variant="error"><div><h6 style="margin:0">`+
				html.EscapeString(title)+
				`</h6><p class="text-light" style="margin-block-end:0">`+
				html.EscapeString(message)+
				`</p></div></div>`)
		return err
	})
}

func renderComponent(c echo.Context, status int, t templ.Component) error {
	var buf bytes.Buffer
	if err := t.Render(c.Request().Context(), &buf); err != nil {
		return c.String(http.StatusInternalServerError, "failed to render template")
	}
	return c.HTML(status, buf.String())
}
