package v2

// NotFound story (reliability track decision): DOCUMENTED RAW-ECHO
// ESCAPE HATCH — v2 deliberately registers no catch-all.
//
// Rationale: branding is app-owned. The www host and the dashboard host
// ship different 404 pages; a framework page would be wrong-brand for
// at least one of them, and a framework page wrapped in App.Layout would
// be wrong again for layout-less apps (the dashboard App has no Layout).
// Echo's RouteNotFound + HTTPErrorHandler is the correct seam, so v2
// stays out of it and documents the pattern instead. These tests lock
// both halves: v2 adds no catch-all of its own, and an app-set
// RouteNotFound coexists with registered resources.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestRegisterAddsNoCatchAll(t *testing.T) {
	e := echo.New()
	testApp().Register(e)
	for _, r := range e.Routes() {
		if strings.Contains(r.Path, "/*") {
			t.Fatalf("v2 must not register a catch-all; found %s %s", r.Method, r.Path)
		}
	}
}

// The documented escape hatch: app sets RouteNotFound last, after Register.
func TestRouteNotFoundEscapeHatchCoexists(t *testing.T) {
	e := echo.New()
	testApp().Register(e)
	e.RouteNotFound("/*", func(c echo.Context) error {
		return c.HTML(http.StatusNotFound, "<main>branded 404</main>")
	})

	get := func(target string, htmx bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	// Unknown path → branded page.
	rec := get("/no-such-page", true)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "branded 404") {
		t.Fatalf("catch-all = %d %q, want branded 404", rec.Code, rec.Body.String())
	}

	// Known resource still serves.
	if rec := get("/apps", true); rec.Code != http.StatusOK {
		t.Fatalf("resource behind catch-all = %d, want 200", rec.Code)
	}
}

// Unknown views and embed-only direct visits stay framework 404s rendered
// through writeError (alert fragment), independent of any app catch-all.
func TestResource404sUseErrorPolicy(t *testing.T) {
	rec := serve(testApp(), http.MethodGet, "/apps?view=bogus", false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "<html>") {
		t.Fatalf("direct unknown view should be layout-wrapped alert, got %q", body)
	}
}
