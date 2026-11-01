package sandbox_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"

	v2 "github.com/joyboydata/kit/hxkit/v2"
	sandbox "github.com/joyboydata/kit/sandbox"
)

func text(s string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	})
}

func stubApp() v2.App {
	return v2.App{
		Resources: []v2.Resource{
			{
				Path: "/",
				Representations: []v2.Representation{
					{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
						return text("hello"), nil
					}},
				},
			},
		},
	}
}

func markerSeed(name string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		dirs, ok := sandbox.DirsFrom(ctx)
		if !ok {
			return nil
		}
		return os.WriteFile(filepath.Join(dirs.Data, name+".marker"), []byte(name), 0o644)
	}
}

// fakeUser is app-side fake-service state on the fakes mux, beside the
// built-in state resource: kit sets no allowlist, the app registers
// whatever behavior endpoints its clients call.
type fakeUser struct {
	mu    sync.Mutex
	email string
}

func (f *fakeUser) get() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.email
}

func (f *fakeUser) set(email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.email = email
}

func baseConfig(user *fakeUser) sandbox.Config {
	return sandbox.Config{
		Mount: sandbox.Mount{
			App: stubApp(),
			Extra: func(e *echo.Echo) {
				e.POST("/_control/touch", func(c echo.Context) error {
					return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
				})
			},
		},
		Fakes: func(mux *http.ServeMux, ctl sandbox.Control) {
			mux.HandleFunc("/fake/user", func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					_ = json.NewEncoder(w).Encode(map[string]string{"email": user.get()})
				case http.MethodPost:
					var req struct {
						Email string `json:"email"`
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						http.Error(w, "invalid body", http.StatusBadRequest)
						return
					}
					user.set(req.Email)
					_ = json.NewEncoder(w).Encode(map[string]string{"email": req.Email})
				default:
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			})
			mux.HandleFunc("/fake/reset", func(w http.ResponseWriter, r *http.Request) {
				user.set("fake@example.com")
				w.WriteHeader(http.StatusOK)
			})
		},
		Seeds: map[string]func(ctx context.Context) error{
			"alpha": markerSeed("alpha"),
			"beta":  markerSeed("beta"),
		},
		Open: func(d sandbox.Dirs) (io.Closer, error) {
			return io.NopCloser(nil), nil
		},
		Describe: func() any {
			return map[string]string{"mode": "stub"}
		},
	}
}

func TestTransport(t *testing.T) {
	user := &fakeUser{email: "fake@example.com"}
	env, err := sandbox.Start(baseConfig(user))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	client := env.WebClient()

	get := func(url string) (int, string) {
		t.Helper()
		resp, err := client.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if code, body := get(env.BaseURL + "/"); code != http.StatusOK || body != "hello" {
		t.Fatalf("GET / = %d %q, want 200 hello", code, body)
	}
	if code, body := get(env.BaseURL + "/_control/state"); code != http.StatusOK || !strings.Contains(body, "stub") {
		t.Fatalf("GET /_control/state = %d %q, want Describe bare", code, body)
	}
	if code, body := get(env.BaseURL + "/_control/scenarios"); code != http.StatusOK ||
		!strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Fatalf("GET /_control/scenarios = %d %q, want alpha+beta", code, body)
	}
	resp, err := client.Post(env.BaseURL+"/_control/touch", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /_control/touch = %d, want 200", resp.StatusCode)
	}

	if err := env.Seed("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(env.Dirs().Data, "alpha.marker")); err != nil {
		t.Fatalf("seed marker missing: %v", err)
	}
	// Compat alias wipes before reseeding: beta in, alpha gone.
	alias, err := client.Post(env.BaseURL+"/_control/seed?scenario=beta", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = alias.Body.Close()
	if alias.StatusCode != http.StatusOK {
		t.Fatalf("POST /_control/seed = %d, want 200", alias.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(env.Dirs().Data, "beta.marker")); err != nil {
		t.Fatalf("alias seed marker missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.Dirs().Data, "alpha.marker")); !os.IsNotExist(err) {
		t.Fatal("alpha.marker survived reseed, want wiped")
	}
	if err := env.Seed("nope"); err == nil {
		t.Fatal("Seed(nope) = nil, want unknown-scenario error")
	}

	// App behavior endpoints beside the state resource.
	if code, body := get(env.FakeBase + "/fake/user"); code != http.StatusOK || !strings.Contains(body, "fake@example.com") {
		t.Fatalf("GET /fake/user = %d %q, want default identity", code, body)
	}
	setReq, _ := http.NewRequest(http.MethodPost, env.FakeBase+"/fake/user", strings.NewReader(`{"email":"new@example.com"}`))
	setResp, err := http.DefaultClient.Do(setReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = setResp.Body.Close()
	if user.get() != "new@example.com" {
		t.Fatalf("fake identity = %q, want new@example.com", user.get())
	}
	if _, err := http.DefaultClient.Post(env.FakeBase+"/fake/reset", "", nil); err != nil {
		t.Fatal(err)
	}
	if user.get() != "fake@example.com" {
		t.Fatalf("after reset identity = %q, want default", user.get())
	}
}

func TestRoutingWebhookBackground(t *testing.T) {
	var mu sync.Mutex
	var hookHost string
	started := make(chan struct{})
	stopped := make(chan struct{})
	targets := struct {
		sync.Mutex
		url  string
		host string
	}{}

	cfg := baseConfig(&fakeUser{email: "fake@example.com"})
	cfg.Mount.Routes = func(baseURL string) sandbox.RouteFunc {
		u, _ := url.Parse(baseURL)
		port := u.Port()
		return func(sub, path string) string {
			if sub == "" {
				return baseURL + path
			}
			return "http://" + sub + ".example.test:" + port + path
		}
	}
	cfg.Mount.Webhook = sandbox.Webhook{Sub: "app", Path: "/hook"}
	cfg.Mount.Extra = func(e *echo.Echo) {
		e.POST("/hook", func(c echo.Context) error {
			mu.Lock()
			hookHost = c.Request().Host
			mu.Unlock()
			return c.NoContent(http.StatusOK)
		})
	}
	cfg.Mount.Background = []func(ctx context.Context) error{
		func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		},
	}
	cfg.Fakes = func(mux *http.ServeMux, ctl sandbox.Control) {
		mux.HandleFunc("/fake/pay", func(w http.ResponseWriter, r *http.Request) {
			targets.Lock()
			fireURL, fireHost := targets.url, targets.host
			targets.Unlock()
			req, err := http.NewRequest(http.MethodPost, fireURL, nil)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			req.Host = fireHost
			fireResp, err := http.DefaultClient.Do(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			_ = fireResp.Body.Close()
			w.WriteHeader(http.StatusOK)
		})
	}

	env, err := sandbox.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	targets.Lock()
	targets.url, targets.host = env.WebhookURL, env.WebhookHost
	targets.Unlock()

	u, _ := url.Parse(env.BaseURL)
	port := u.Port()
	if got := env.Route("app", "/x"); got != "http://app.example.test:"+port+"/x" {
		t.Fatalf("Route(app, /x) = %q, want subdomain shape", got)
	}
	if env.WebhookURL != env.BaseURL+"/hook" {
		t.Fatalf("WebhookURL = %q, want dialable mount URL", env.WebhookURL)
	}
	if env.WebhookHost != "app.example.test:"+port {
		t.Fatalf("WebhookHost = %q, want subdomain host with port", env.WebhookHost)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background service did not start")
	}

	// App-facing URL drives the mount through the no-DNS client.
	resp, err := env.WebClient().Get(env.Route("app", "/"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "hello" {
		t.Fatalf("GET app shape = %q, want hello", body)
	}

	// Fake fires the webhook with the Host override.
	fire, err := http.DefaultClient.Post(env.FakeBase+"/fake/pay", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fire.Body.Close()
	if fire.StatusCode != http.StatusOK {
		t.Fatalf("POST /fake/pay = %d, want 200", fire.StatusCode)
	}
	mu.Lock()
	gotHost := hookHost
	mu.Unlock()
	if gotHost != env.WebhookHost {
		t.Fatalf("hook Host = %q, want %q", gotHost, env.WebhookHost)
	}

	env.Close()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("background service did not stop on Close")
	}
}
