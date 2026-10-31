package v2

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

var errBoom = errors.New("plain failure")

func errApp() App {
	return App{
		Layout: shell,
		Resources: []Resource{
			{
				Path: "/r",
				Representations: []Representation{
					{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
						switch c.QueryParam("fail") {
						case "pay":
							return nil, E(http.StatusPaymentRequired, "login again")
						case "echo":
							return nil, echo.NewHTTPError(http.StatusForbidden, "no entry")
						case "plain":
							return nil, errBoom
						case "zero":
							return nil, E(200, "not an error code")
						}
						return text("ok"), nil
					}},
				},
			},
		},
	}
}

func TestPaymentRequiredHTMXRedirectsWithNext(t *testing.T) {
	rec := serve(errApp(), http.MethodGet, "/r?fail=pay", true)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", rec.Code)
	}
	got := rec.Header().Get("HX-Redirect")
	if !strings.HasPrefix(got, "/login?next=") || !strings.Contains(got, "%2Fr%3Ffail%3Dpay") {
		t.Fatalf("HX-Redirect = %q, want login with encoded next", got)
	}
	if rec.Body.String() != "" {
		t.Fatalf("402 body should be empty, got %q", rec.Body.String())
	}
}

func TestPaymentRequiredDirectRedirects(t *testing.T) {
	rec := serve(errApp(), http.MethodGet, "/r?fail=pay", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login?next=") {
		t.Fatalf("Location = %q, want login with next", loc)
	}
}

func TestServerErrorHTMXIsEmptyWithToast(t *testing.T) {
	rec := serve(errApp(), http.MethodGet, "/r?fail=plain", true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if rec.Body.String() != "" {
		t.Fatalf("500 body should be empty, got %q", rec.Body.String())
	}
	if trig := rec.Header().Get("HX-Trigger"); !strings.Contains(trig, "toast") {
		t.Fatalf("HX-Trigger = %q, want toast payload", trig)
	}
}

func TestServerErrorDirectHidesDetails(t *testing.T) {
	rec := serve(errApp(), http.MethodGet, "/r?fail=plain", false)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "plain failure") {
		t.Fatalf("direct 500 must not leak internals, got %q", body)
	}
	if !strings.Contains(body, "<html>") {
		t.Fatalf("direct 500 should be layout-wrapped, got %q", body)
	}
}

func TestEchoHTTPErrorAccepted(t *testing.T) {
	rec := serve(errApp(), http.MethodGet, "/r?fail=echo", true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `role="alert"`) {
		t.Fatalf("403 should be inline alert, got %q", body)
	}
}

func TestOutOfRangeCodeBecomes500(t *testing.T) {
	rec := serve(errApp(), http.MethodGet, "/r?fail=zero", true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
