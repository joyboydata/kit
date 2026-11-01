package v2

// Repro harness for the hxkit-v2 reliability track (apliq migration traps).
//
// Each TestRepro* locks a gap the host/app+hq migration exposed. Tests
// asserting the HARDENED behavior fail against the current code — that is
// the repro. The fix must flip them green without breaking the rest.
//
// Gap 1 — joinPaths trap: a leading-/ mutation sub-path silently becomes an
// absolute override (PATCH /:id lands at root, /apps/:id 404s in prod).
// Gap 2 — stream paths ignore the resource prefix entirely (registered
// verbatim), so relative stream paths misroute with no signal.
// Gap 3 — NotFound story: v2 registers no catch-all, leaving Echo's default.
// Gap 4 — error-mapping edges: framework-402-login vs handler-set
// paywall HX-Redirect+Hold, plus 4xx/5xx branch coverage.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

// joinPaths with a leading-/ sub-path must stay under the resource.
// Today it escapes to root — the prod 404.
func TestReproJoinPathsLeadingSlashStaysUnderResource(t *testing.T) {
	if got := joinPaths("/apps", "/:id"); got != "/apps/:id" {
		t.Fatalf("joinPaths(/apps, /:id) = %q, want /apps/:id (silent root override)", got)
	}
}

// End-to-end version of the trap: PATCH /apps/123 must hit the mutation.
func TestReproMutationLeadingSlashServesUnderResource(t *testing.T) {
	app := App{
		Resources: []Resource{{
			Path: "/apps",
			Representations: []Representation{{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
				return text("apps"), nil
			}}},
			Mutations: []Mutation{
				{Method: http.MethodPatch, Path: "/:id", Handler: func(c echo.Context) (templ.Component, error) {
					return text("patched:" + c.Param("id")), nil
				}},
			},
		}},
	}
	e := echo.New()
	app.Register(e)

	req := httptest.NewRequest(http.MethodPatch, "/apps/123", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "patched:123") {
		t.Fatalf("PATCH /apps/123 = %d %q, want 200 patched:123 (misrouted to root?)", rec.Code, rec.Body.String())
	}

	// And the escaped root route must NOT exist.
	req = httptest.NewRequest(http.MethodPatch, "/123", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PATCH /123 = %d, want 404 (absolute escape must be gone)", rec.Code)
	}
}

// Streams declared relative to their resource must serve under it.
// Today Stream.Path registers verbatim, so "stream" lands at /stream.
func TestReproStreamRelativePathJoinsResource(t *testing.T) {
	app := App{
		Resources: []Resource{{
			Path: "/apps/:id/revisions/:rid",
			Representations: []Representation{{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
				return text("rev"), nil
			}}},
			Streams: []Stream{{
				Path:   "stream",
				Replay: true,
				Events: []Event{{
					Name:    "rev",
					Render: func(c echo.Context) (templ.Component, error) { return text("rev"), nil },
				}},
			}},
		}},
	}
	e := echo.New()
	app.Register(e)

	found := false
	for _, r := range e.Routes() {
		if r.Path == "/apps/:id/revisions/:rid/stream" && r.Method == http.MethodGet {
			found = true
		}
	}
	if !found {
		paths := make([]string, 0)
		for _, r := range e.Routes() {
			paths = append(paths, r.Method+" "+r.Path)
		}
		t.Fatalf("no GET /apps/:id/revisions/:rid/stream route; routes=%v", paths)
	}
}

// v2 leaves unknown paths to Echo (no framework catch-all). This locks the
// seam the NotFound decision must document or replace.
func TestReproNotFoundLeftToEcho(t *testing.T) {
	rec := serve(testApp(), http.MethodGet, "/no-such-page", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404 from Echo default", rec.Code)
	}
}

// Paywall edge (apliq revisions composer): the handler sets HX-Redirect
// itself and returns Hold(). The framework must preserve that header.
func TestReproPaywallRedirectSurvivesHold(t *testing.T) {
	app := App{
		Resources: []Resource{{
			Path: "/apps/:id/revisions/:rid",
			Representations: []Representation{{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
				return text("rev"), nil
			}}},
			Mutations: []Mutation{
				{Path: "messages", Handler: func(c echo.Context) (templ.Component, error) {
					c.Response().Header().Set("HX-Redirect", "/subscriptions?next=%2Fapps%2F1%2Frevisions%2F2")
					return Hold(), nil
				}},
			},
		}},
	}
	e := echo.New()
	app.Register(e)

	req := httptest.NewRequest(http.MethodPost, "/apps/1/revisions/2/messages", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("hold status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); !strings.HasPrefix(got, "/subscriptions?next=") {
		t.Fatalf("HX-Redirect = %q, want paywall URL preserved", got)
	}
	if rec.Body.String() != "" {
		t.Fatalf("hold body = %q, want empty", rec.Body.String())
	}
}

// 429/409 must render like every other 4xx: inline alert over HTMX,
// layout-wrapped on direct visits.
func TestReproRateLimitAndConflictFragments(t *testing.T) {
	for _, code := range []int{http.StatusConflict, http.StatusTooManyRequests} {
		app := App{
			Layout: shell,
			Resources: []Resource{{
				Path: "/r",
				Representations: []Representation{{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
					return nil, E(code, "busy")
				}}},
			}},
		}

		rec := serve(app, http.MethodGet, "/r", true)
		if rec.Code != code {
			t.Fatalf("code %d htmx status = %d, want %d", code, rec.Code, code)
		}
		if body := rec.Body.String(); !strings.Contains(body, `role="alert"`) {
			t.Fatalf("code %d htmx body should be alert fragment, got %q", code, body)
		}

		rec = serve(app, http.MethodGet, "/r", false)
		if rec.Code != code {
			t.Fatalf("code %d direct status = %d, want %d", code, rec.Code, code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "<html>") {
			t.Fatalf("code %d direct body should be layout-wrapped, got %q", code, body)
		}
	}
}
