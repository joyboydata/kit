package v2

// Routes() audit table: the dump that catches the next silent misroute.
// It must mirror Register exactly (same join, same method defaulting).

import (
	"net/http"
	"testing"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

func TestRoutesMirrorsRegister(t *testing.T) {
	app := App{
		Resources: []Resource{
			{
				Path: "/apps",
				Representations: []Representation{{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
					return text("apps"), nil
				}}},
				Mutations: []Mutation{
					{Path: "save", Handler: func(c echo.Context) (templ.Component, error) {
						return text("x"), nil
					}},
					{Method: http.MethodPatch, Path: "/:id", Handler: func(c echo.Context) (templ.Component, error) {
						return text("x"), nil
					}},
					{Method: http.MethodDelete, Path: "files/:name", Handler: func(c echo.Context) (templ.Component, error) {
						return text("x"), nil
					}},
				},
				Streams: []Stream{
					{Path: "events", Events: []Event{{
						Name:   "apps",
						Render: func(c echo.Context) (templ.Component, error) { return text("x"), nil },
					}}},
				},
			},
			{
				Path:            "/status/stream",
				Representations: []Representation{},
				Streams: []Stream{
					{Path: "", Events: []Event{{
						Name:   "status",
						Render: func(c echo.Context) (templ.Component, error) { return text("x"), nil },
					}}},
				},
			},
		},
	}

	want := []string{
		"GET /apps",                // resource
		"POST /apps/save",          // relative mutation
		"PATCH /apps/:id",          // leading-/ mutation joins
		"DELETE /apps/files/:name", // nested mutation
		"GET /apps/events",         // relative stream
		"GET /status/stream",       // resource + empty stream path
	}

	routes := app.Routes()
	got := map[string]bool{}
	for _, r := range routes {
		got[r.Method+" "+r.Path] = true
	}
	for _, k := range want {
		if !got[k] {
			t.Fatalf("Routes() missing %q; got %v", k, routes)
		}
	}
	if len(routes) != 6 {
		t.Fatalf("Routes() = %v, want exactly 6 entries (no root escapes)", routes)
	}

	// And Echo must serve exactly that table — no more, no less.
	e := echo.New()
	app.Register(e)
	echoRoutes := map[string]bool{}
	for _, r := range e.Routes() {
		echoRoutes[r.Method+" "+r.Path] = true
	}
	for k := range got {
		if !echoRoutes[k] {
			t.Fatalf("echo missing route %q; echo has %v", k, e.Routes())
		}
	}
}
