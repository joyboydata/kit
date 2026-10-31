package v2

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// viewParam is the only representation selector. Handler input such as
// ?q= or pagination params is never a variant selector.
const viewParam = "view"

// Resource replaces v1's Module: one URL with N declared representations
// instead of a page/fragment split. Mutations and streams belonging to
// the same URL live on the same declaration.
type Resource struct {
	// Path is the GET route serving every representation.
	Path string

	// Representations are selected by ?view=<Name>. Exactly one should
	// have an empty Name: the default served when ?view is absent.
	Representations []Representation

	// Mutations are write endpoints owned by this resource.
	Mutations []Mutation

	// Streams are SSE endpoints owned by this resource.
	Streams []Stream
}

// Representation is one view of a resource. Name is the ?view value;
// EmbedOnly marks dialog, drawer, and in-place representations that
// have no standalone meaning, so direct visits 404 by declaration.
type Representation struct {
	Name      string
	EmbedOnly bool
	Handler   Handler
}

// Mutation is a write endpoint owned by a resource. Method defaults to
// POST; Path defaults to the resource path, joins it when relative,
// and overrides it when absolute. The handler returns a result command
// (SwapWith, RedirectTo, Hold) or a plain component; see mutation.go.
type Mutation struct {
	Method  string
	Path    string
	Handler Handler
}

func selectRep(reps []Representation, view string) (Representation, bool) {
	for _, r := range reps {
		if r.Name == view {
			return r, true
		}
	}
	return Representation{}, false
}

// serveResource renders the representation named by ?view. Unknown views
// 404; EmbedOnly representations 404 direct visits; layout wrapping and
// errors follow App policy so handlers never branch on request kind.
func (a App) serveResource(res Resource) echo.HandlerFunc {
	return func(c echo.Context) error {
		rep, ok := selectRep(res.Representations, c.QueryParam(viewParam))
		if !ok {
			return echo.NewHTTPError(http.StatusNotFound, "unknown view")
		}
		if rep.EmbedOnly && !IsHTMX(c) {
			return echo.NewHTTPError(http.StatusNotFound, "not found")
		}
		if rep.Handler == nil {
			return a.writeError(c, E(http.StatusInternalServerError, "missing handler"))
		}
		comp, err := rep.Handler(c)
		if err != nil {
			return a.writeError(c, err)
		}
		if comp == nil {
			return a.writeError(c, E(http.StatusInternalServerError, "nil component"))
		}
		if !IsHTMX(c) && a.Layout != nil {
			comp = a.Layout(comp)
		}
		return renderComponent(c, http.StatusOK, comp)
	}
}
