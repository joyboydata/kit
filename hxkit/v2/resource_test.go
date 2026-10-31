package v2

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

func text(s string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	})
}

func shell(body templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, "<html><body>"); err != nil {
			return err
		}
		if err := body.Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, "</body></html>")
		return err
	})
}

func testApp() App {
	return App{
		Layout: shell,
		Resources: []Resource{
			{
				Path: "/apps",
				Representations: []Representation{
					{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
						return text("<ul>list</ul>"), nil
					}},
					{Name: "cards", Handler: func(c echo.Context) (templ.Component, error) {
						return text("<div>cards</div>"), nil
					}},
					{Name: "dialog", EmbedOnly: true, Handler: func(c echo.Context) (templ.Component, error) {
						return text("<dialog>hi</dialog>"), nil
					}},
					{Name: "boom", Handler: func(c echo.Context) (templ.Component, error) {
						return nil, E(http.StatusNotFound, "no such app")
					}},
				},
				Mutations: []Mutation{
					{Path: "save", Handler: func(c echo.Context) (templ.Component, error) {
						return text("<li>saved</li>"), nil
					}},
					{Path: "gone", Handler: func(c echo.Context) (templ.Component, error) {
						return nil, nil
					}},
					{Path: "/absolute", Handler: func(c echo.Context) (templ.Component, error) {
						return text("abs"), nil
					}},
				},
			},
		},
	}
}

func serve(app App, method, target string, htmx bool) *httptest.ResponseRecorder {
	e := echo.New()
	app.Register(e)
	req := httptest.NewRequest(method, target, nil)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestDefaultRepresentationDirectGetsLayout(t *testing.T) {
	rec := serve(testApp(), http.MethodGet, "/apps", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html>") || !strings.Contains(body, "<ul>list</ul>") {
		t.Fatalf("direct visit should be layout-wrapped, got %q", body)
	}
}

func TestDefaultRepresentationHTMXSkipsLayout(t *testing.T) {
	rec := serve(testApp(), http.MethodGet, "/apps", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html>") || !strings.Contains(body, "<ul>list</ul>") {
		t.Fatalf("htmx swap should be bare, got %q", body)
	}
}

func TestViewSelectsRepresentation(t *testing.T) {
	rec := serve(testApp(), http.MethodGet, "/apps?view=cards", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<div>cards</div>") {
		t.Fatalf("wrong representation, got %q", rec.Body.String())
	}
}

func TestUnknownView404s(t *testing.T) {
	rec := serve(testApp(), http.MethodGet, "/apps?view=bogus", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestEmbedOnly404sDirectVisits(t *testing.T) {
	rec := serve(testApp(), http.MethodGet, "/apps?view=dialog", false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	rec = serve(testApp(), http.MethodGet, "/apps?view=dialog", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<dialog>hi</dialog>") {
		t.Fatalf("wrong body, got %q", rec.Body.String())
	}
}

func TestNilLayoutServesBareBody(t *testing.T) {
	app := testApp()
	app.Layout = nil
	rec := serve(app, http.MethodGet, "/apps", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "<ul>list</ul>" {
		t.Fatalf("layout-less app should serve bare body, got %q", body)
	}
}

func TestHandlerErrorMapsToAlertFragment(t *testing.T) {
	rec := serve(testApp(), http.MethodGet, "/apps?view=boom", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "no such app") {
		t.Fatalf("expected inline alert with message, got %q", body)
	}
}

func TestMutationRendersFragment(t *testing.T) {
	rec := serve(testApp(), http.MethodPost, "/apps/save", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<li>saved</li>") {
		t.Fatalf("wrong body, got %q", rec.Body.String())
	}
}

func TestMutationNilComponentMapsToHold(t *testing.T) {
	rec := serve(testApp(), http.MethodPost, "/apps/gone", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "" {
		t.Fatalf("nil should hold with empty body, got %q", rec.Body.String())
	}

	rec = serve(testApp(), http.MethodPost, "/apps/gone", false)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestMutationPathJoining(t *testing.T) {
	rec := serve(testApp(), http.MethodPost, "/absolute", true)
	if rec.Code != http.StatusOK || rec.Body.String() != "abs" {
		t.Fatalf("absolute path should bypass resource prefix, got %d %q", rec.Code, rec.Body.String())
	}
}

func TestOtherParamsAreNotSelectors(t *testing.T) {
	rec := serve(testApp(), http.MethodGet, "/apps?q=foo&page=2", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<ul>list</ul>") {
		t.Fatalf("filter params must not change representation, got %q", rec.Body.String())
	}
}
