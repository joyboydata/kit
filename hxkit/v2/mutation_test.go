package v2

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

func cmdApp() App {
	return App{
		Layout: shell,
		Resources: []Resource{{
			Path: "/files",
			Representations: []Representation{{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
				return text("files"), nil
			}}},
			Mutations: []Mutation{
				{Path: "go", Handler: func(c echo.Context) (templ.Component, error) {
					return RedirectTo("/files?view=done"), nil
				}},
				{Path: "swap", Handler: func(c echo.Context) (templ.Component, error) {
					return SwapWith(text("<li>new</li>")), nil
				}},
				{Path: "del", Handler: func(c echo.Context) (templ.Component, error) {
					return Hold(), nil
				}},
				{Path: "nil", Handler: func(c echo.Context) (templ.Component, error) {
					return nil, nil
				}},
			},
		}},
	}
}

func doReq(e *echo.Echo, method, target string, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestRedirectToHTMX(t *testing.T) {
	e := echo.New()
	cmdApp().Register(e)
	rec := doReq(e, http.MethodPost, "/files/go", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/files?view=done" {
		t.Fatalf("HX-Redirect = %q", got)
	}
	if rec.Body.String() != "" {
		t.Fatalf("redirect body should be empty, got %q", rec.Body.String())
	}
}

func TestRedirectToPlain(t *testing.T) {
	e := echo.New()
	cmdApp().Register(e)
	rec := doReq(e, http.MethodPost, "/files/go", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/files?view=done" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestSwapWithBothKinds(t *testing.T) {
	e := echo.New()
	cmdApp().Register(e)
	for _, htmx := range []bool{true, false} {
		rec := doReq(e, http.MethodPost, "/files/swap", htmx)
		if rec.Code != http.StatusOK {
			t.Fatalf("htmx=%v status = %d, want 200", htmx, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<li>new</li>") || strings.Contains(body, "<html>") {
			t.Fatalf("htmx=%v swap should be bare fragment, got %q", htmx, body)
		}
	}
}

func TestHoldBothKinds(t *testing.T) {
	e := echo.New()
	cmdApp().Register(e)

	rec := doReq(e, http.MethodPost, "/files/del", true)
	if rec.Code != http.StatusOK || rec.Body.String() != "" {
		t.Fatalf("htmx hold = %d %q, want 200 empty", rec.Code, rec.Body.String())
	}
	rec = doReq(e, http.MethodPost, "/files/del", false)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("plain hold status = %d, want 204", rec.Code)
	}
}

func TestNilMapsToHoldAndWarns(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	e := echo.New()
	cmdApp().Register(e, WithLogger(logger))

	rec := doReq(e, http.MethodPost, "/files/nil", true)
	if rec.Code != http.StatusOK || rec.Body.String() != "" {
		t.Fatalf("nil = %d %q, want 200 empty", rec.Code, rec.Body.String())
	}
	if !strings.Contains(buf.String(), "Hold") {
		t.Fatalf("nil mapping should warn, log got %q", buf.String())
	}
}
