package v2

// Error-mapping edge coverage for the reliability track: every
// writeError/normalizeCode branch the host/app+hq migration exercises.
// Framework-402 is login-only; the paywall case is a handler-set
// HX-Redirect preserved through Hold (locked in repro_test.go).

import (
	"net/http"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

func TestNormalizeCodeBoundaries(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{-1, 500},
		{0, 500},
		{200, 500},
		{399, 500},
		{400, 400},
		{401, 401},
		{429, 429},
		{500, 500},
		{599, 599},
		{600, 500},
	}
	for _, tc := range cases {
		if got := normalizeCode(tc.in); got != tc.want {
			t.Fatalf("normalizeCode(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func edgeApp(code int, msg string) App {
	return App{
		Layout: shell,
		Resources: []Resource{{
			Path: "/r",
			Representations: []Representation{{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
				return nil, E(code, msg)
			}}},
		}},
	}
}

// Custom LoginPath must win over the /login default on both wires.
func TestCustomLoginPath(t *testing.T) {
	app := edgeApp(http.StatusPaymentRequired, "login again")
	app.Errors = Errors{LoginPath: "/subscriptions"}
	for _, htmx := range []bool{true, false} {
		rec := serve(app, http.MethodGet, "/r", htmx)
		if htmx {
			if rec.Code != http.StatusPaymentRequired {
				t.Fatalf("htmx status = %d, want 402", rec.Code)
			}
			if got := rec.Header().Get("HX-Redirect"); !strings.HasPrefix(got, "/subscriptions?next=") {
				t.Fatalf("HX-Redirect = %q, want custom login path", got)
			}
		} else {
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("direct status = %d, want 303", rec.Code)
			}
			if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/subscriptions?next=") {
				t.Fatalf("Location = %q, want custom login path", loc)
			}
		}
	}
}

// 4xx over HTMX: status code plus inline alert titled by StatusText,
// carrying the handler message, never layout-wrapped.
func TestClientErrorHTMXFragmentShape(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409, 429} {
		rec := serve(edgeApp(code, "edge-detail"), http.MethodGet, "/r", true)
		if rec.Code != code {
			t.Fatalf("code %d: status = %d", code, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `role="alert"`) {
			t.Fatalf("code %d: missing alert fragment: %q", code, body)
		}
		if !strings.Contains(body, http.StatusText(code)) {
			t.Fatalf("code %d: missing StatusText title: %q", code, body)
		}
		if !strings.Contains(body, "edge-detail") {
			t.Fatalf("code %d: handler message lost: %q", code, body)
		}
		if strings.Contains(body, "<html>") {
			t.Fatalf("code %d: htmx fragment must skip layout: %q", code, body)
		}
	}
}

// 4xx direct: same semantics, layout-wrapped.
func TestClientErrorDirectLayoutWrapped(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409, 429} {
		rec := serve(edgeApp(code, "edge-detail"), http.MethodGet, "/r", false)
		if rec.Code != code {
			t.Fatalf("code %d: status = %d", code, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<html>") || !strings.Contains(body, "edge-detail") {
			t.Fatalf("code %d: direct should be layout-wrapped with message: %q", code, body)
		}
	}
}

// Layout-less apps (the dashboard App has Layout nil): direct errors go
// out bare, never wrapped, never leaking internals on 5xx.
func TestLayoutNilDirectErrorsBare(t *testing.T) {
	app := edgeApp(http.StatusForbidden, "no entry")
	app.Layout = nil
	rec := serve(app, http.MethodGet, "/r", false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `role="alert"`) || strings.Contains(body, "<html>") {
		t.Fatalf("layout-less direct 4xx should be bare alert, got %q", body)
	}

	app = edgeApp(http.StatusBadGateway, "backend exploded")
	app.Layout = nil
	rec = serve(app, http.MethodGet, "/r", false)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	body = rec.Body.String()
	if strings.Contains(body, "backend exploded") {
		t.Fatalf("direct 5xx must not leak internals, got %q", body)
	}
	if strings.Contains(body, "<html>") {
		t.Fatalf("layout-less direct 5xx should stay bare, got %q", body)
	}
}

// Every 5xx: empty body plus toast trigger over HTMX, generic
// layout-wrapped page on direct visits.
func TestServerErrorVariants(t *testing.T) {
	for _, code := range []int{500, 502, 503} {
		rec := serve(edgeApp(code, "secret-internals"), http.MethodGet, "/r", true)
		if rec.Code != code {
			t.Fatalf("code %d: htmx status = %d", code, rec.Code)
		}
		if rec.Body.String() != "" {
			t.Fatalf("code %d: htmx 5xx body should be empty, got %q", code, rec.Body.String())
		}
		if trig := rec.Header().Get("HX-Trigger"); !strings.Contains(trig, "toast") {
			t.Fatalf("code %d: HX-Trigger = %q, want toast", code, rec.Header().Get("HX-Trigger"))
		}

		rec = serve(edgeApp(code, "secret-internals"), http.MethodGet, "/r", false)
		if rec.Code != code {
			t.Fatalf("code %d: direct status = %d", code, rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "secret-internals") {
			t.Fatalf("code %d: direct 5xx must not leak internals, got %q", code, body)
		}
		if !strings.Contains(body, "<html>") {
			t.Fatalf("code %d: direct 5xx should be layout-wrapped, got %q", code, body)
		}
	}
}

// echo.HTTPError passthrough: string messages render, non-4xx codes
// normalize to 500.
func TestEchoHTTPErrorEdges(t *testing.T) {
	app := App{
		Layout: shell,
		Resources: []Resource{{
			Path: "/r",
			Representations: []Representation{{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
				switch c.QueryParam("fail") {
				case "nf":
					return nil, echo.NewHTTPError(http.StatusNotFound, "gone-thing")
				case "teapot":
					return nil, echo.NewHTTPError(http.StatusTeapot, "short stout")
				}
				return text("ok"), nil
			}}},
		}},
	}

	rec := serve(app, http.MethodGet, "/r?fail=nf", true)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "gone-thing") {
		t.Fatalf("echo 404 = %d %q, want 404 with message", rec.Code, rec.Body.String())
	}
	rec = serve(app, http.MethodGet, "/r?fail=nf", false)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<html>") {
		t.Fatalf("echo 404 direct = %d %q, want layout-wrapped", rec.Code, rec.Body.String())
	}

	// 418 is inside 4xx: passes through as its own alert, not normalized.
	rec = serve(app, http.MethodGet, "/r?fail=teapot", true)
	if rec.Code != http.StatusTeapot || !strings.Contains(rec.Body.String(), "short stout") {
		t.Fatalf("echo 418 = %d %q, want passthrough", rec.Code, rec.Body.String())
	}
}
