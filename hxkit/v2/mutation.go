package v2

import (
	"context"
	"io"
	"net/http"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

// Mutation result commands. Handlers return these as components; the
// mutation framework interprets them instead of rendering them.
//
// Plain components keep working: they render as an in-place fragment,
// layout-wrapped on direct visits. A nil component is deprecated and
// maps to Hold with a warning; return Hold explicitly instead.
func SwapWith(comp templ.Component) templ.Component {
	return swapResult{comp}
}

// RedirectTo goes somewhere computed post-write: a plain request gets a
// 303 with Location, an HTMX request gets an empty 200 with HX-Redirect
// so htmx navigates client-side.
func RedirectTo(url string) templ.Component {
	return redirectResult{url}
}

// Hold stays put and swaps nothing: 204 for plain requests, an empty
// 200 for HTMX ones (the file-delete shape).
func Hold() templ.Component {
	return holdResult{}
}

type swapResult struct{ comp templ.Component }

func (s swapResult) Render(ctx context.Context, w io.Writer) error {
	if s.comp == nil {
		return nil
	}
	return s.comp.Render(ctx, w)
}

type redirectResult struct{ url string }

func (r redirectResult) Render(context.Context, io.Writer) error { return nil }

type holdResult struct{}

func (holdResult) Render(context.Context, io.Writer) error { return nil }

// serveMutation runs a write endpoint. Errors follow App policy; results
// follow the command each handler returned.
func (a App) serveMutation(res Resource, m Mutation) echo.HandlerFunc {
	return func(c echo.Context) error {
		if m.Handler == nil {
			return a.writeError(c, E(http.StatusInternalServerError, "missing handler"))
		}
		comp, err := m.Handler(c)
		if err != nil {
			return a.writeError(c, err)
		}
		htmx := IsHTMX(c)
		switch r := comp.(type) {
		case nil:
			a.log().Warn("v2: mutation returned nil component, mapped to Hold; return Hold() explicitly",
				"resource", res.Path, "mutation", m.Path)
			return hold(c, htmx)
		case holdResult:
			return hold(c, htmx)
		case redirectResult:
			if htmx {
				c.Response().Header().Set("HX-Redirect", r.url)
				return c.NoContent(http.StatusOK)
			}
			return c.Redirect(http.StatusSeeOther, r.url)
		case swapResult:
			if r.comp == nil {
				return hold(c, htmx)
			}
			return renderComponent(c, http.StatusOK, r.comp)
		default:
			if !htmx && a.Layout != nil {
				comp = a.Layout(comp)
			}
			return renderComponent(c, http.StatusOK, comp)
		}
	}
}

func hold(c echo.Context, htmx bool) error {
	if htmx {
		return c.NoContent(http.StatusOK)
	}
	return c.NoContent(http.StatusNoContent)
}
