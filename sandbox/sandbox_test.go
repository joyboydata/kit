package sandbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// stubExternal is an in-process fake: Set mutates memory, Fire dials the
// app webhook with req.Host set explicitly (the domain trap).
type stubExternal struct {
	mu        sync.Mutex
	email     string
	firedHost string
	hookURL   string
	hookSeen  *string
	hookMu    *sync.Mutex
}

func (s *stubExternal) Name() string { return "mail" }

func (s *stubExternal) State() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]string{"email": s.email}
}

func (s *stubExternal) Set(state map[string]any) error {
	email, ok := state["email"].(string)
	if !ok || email == "" {
		return fmt.Errorf("sandbox: email required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.email = email
	return nil
}

// Fire dials the app address but dispatches on host.
func (s *stubExternal) Fire(host string) error {
	s.mu.Lock()
	s.firedHost = host
	url := s.hookURL
	s.mu.Unlock()
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sandbox: hook %d", resp.StatusCode)
	}
	return nil
}

func (s *stubExternal) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.email = "fake@example.com"
	s.firedHost = ""
}

func (s *stubExternal) fired() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firedHost
}

func testConfig(ext *stubExternal, hookSeen *string, hookMu *sync.Mutex) sandbox.Config {
	return sandbox.Config{
		Mount: sandbox.Mount{
			App: stubApp(),
			Extra: func(e *echo.Echo) {
				e.POST("/hook", func(c echo.Context) error {
					hookMu.Lock()
					*hookSeen = c.Request().Host
					hookMu.Unlock()
					return c.NoContent(http.StatusOK)
				})
			},
		},
		Externals: []sandbox.External{ext},
		Seeds: map[string]func(ctx context.Context) error{
			"alpha": func(_ context.Context) error {
				return ext.Set(map[string]any{"email": "alpha@example.com"})
			},
			"beta": func(_ context.Context) error {
				return ext.Set(map[string]any{"email": "beta@example.com"})
			},
		},
		Default: "alpha",
		Open: func(_ sandbox.Dirs) (io.Closer, error) {
			return io.NopCloser(nil), nil
		},
		Describe: func() any {
			return ext.State()
		},
	}
}

func get(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestStateRoutes(t *testing.T) {
	var hookSeen string
	var hookMu sync.Mutex
	ext := &stubExternal{email: "fake@example.com", hookSeen: &hookSeen, hookMu: &hookMu}
	env, err := sandbox.Start(testConfig(ext, &hookSeen, &hookMu))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	if env.FakeBase != env.BaseURL {
		t.Fatalf("FakeBase = %q, want alias of BaseURL", env.FakeBase)
	}
	client := env.WebClient()

	if code, body := get(t, client, env.BaseURL+"/"); code != http.StatusOK || body != "hello" {
		t.Fatalf("GET / = %d %q, want 200 hello", code, body)
	}
	// Default seed applied at Start.
	if code, body := get(t, client, env.BaseURL+"/_control/state"); code != http.StatusOK || !strings.Contains(body, "alpha@example.com") {
		t.Fatalf("GET /_control/state = %d %q, want default Describe", code, body)
	}
	if code, body := get(t, client, env.BaseURL+"/_control/scenarios"); code != http.StatusOK ||
		!strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Fatalf("GET /_control/scenarios = %d %q, want alpha+beta", code, body)
	}
	if err := env.Seed("beta"); err != nil {
		t.Fatal(err)
	}
	if code, body := get(t, client, env.BaseURL+"/_control/state"); code != http.StatusOK || !strings.Contains(body, "beta@example.com") {
		t.Fatalf("after Seed(beta) state = %d %q", code, body)
	}
	// Unknown scenario is a 404 JSON, not a wipe.
	if err := env.Seed("nope"); err == nil {
		t.Fatal("Seed(nope) = nil, want error")
	}
	// Empty scenario wipes only: externals reset to defaults.
	if err := env.Seed(""); err != nil {
		t.Fatal(err)
	}
	if code, body := get(t, client, env.BaseURL+"/_control/state"); code != http.StatusOK || !strings.Contains(body, "fake@example.com") {
		t.Fatalf("after Seed(\"\") state = %d %q, want reset defaults", code, body)
	}
	// Compat alias over query param.
	resp, err := client.Post(env.BaseURL+"/_control/seed?scenario=alpha", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /_control/seed = %d, want 200", resp.StatusCode)
	}
	if code, body := get(t, client, env.BaseURL+"/_control/state"); code != http.StatusOK || !strings.Contains(body, "alpha@example.com") {
		t.Fatalf("after compat seed state = %d %q", code, body)
	}
}

func TestWorldFireBackground(t *testing.T) {
	var hookSeen string
	var hookMu sync.Mutex
	ext := &stubExternal{email: "fake@example.com", hookSeen: &hookSeen, hookMu: &hookMu}
	cfg := testConfig(ext, &hookSeen, &hookMu)
	started := make(chan struct{})
	stopped := make(chan struct{})
	cfg.Background = []func(ctx context.Context) error{
		func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		},
	}
	env, err := sandbox.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	ext.mu.Lock()
	ext.hookURL = env.BaseURL + "/hook"
	ext.mu.Unlock()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background did not start")
	}
	client := env.WebClient()
	if code, body := get(t, client, env.BaseURL+"/_control/world"); code != http.StatusOK || !strings.Contains(body, "mail") {
		t.Fatalf("GET /_control/world = %d %q, want mail entry", code, body)
	}
	if code, body := get(t, client, env.BaseURL+"/_control/world/mail"); code != http.StatusOK || !strings.Contains(body, "alpha@example.com") {
		t.Fatalf("GET /_control/world/mail = %d %q", code, body)
	}
	// Set per-external state.
	setReq, _ := http.NewRequest(http.MethodPut, env.BaseURL+"/_control/world/mail", strings.NewReader(`{"email":"new@example.com"}`))
	setReq.Header.Set("Content-Type", "application/json")
	setResp, err := client.Do(setReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = setResp.Body.Close()
	if setResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /_control/world/mail = %d, want 200", setResp.StatusCode)
	}
	if code, body := get(t, client, env.BaseURL+"/_control/world/mail"); code != http.StatusOK || !strings.Contains(body, "new@example.com") {
		t.Fatalf("after Set state = %d %q", code, body)
	}
	if code, _ := get(t, client, env.BaseURL+"/_control/world/unknown"); code != http.StatusNotFound {
		t.Fatalf("GET unknown external = %d, want 404", code)
	}

	// Fire forwards host verbatim; the app dispatches on req.Host.
	fireBody := strings.NewReader(`{"host":"billing.app.example"}`)
	fireResp, err := client.Post(env.BaseURL+"/_control/world/mail/fire", "application/json", fireBody)
	if err != nil {
		t.Fatal(err)
	}
	var fired map[string]string
	_ = json.NewDecoder(fireResp.Body).Decode(&fired)
	_ = fireResp.Body.Close()
	if fireResp.StatusCode != http.StatusOK || fired["host"] != "billing.app.example" {
		t.Fatalf("POST fire = %d %v, want host echo", fireResp.StatusCode, fired)
	}
	if got := ext.fired(); got != "billing.app.example" {
		t.Fatalf("external fired host = %q, want explicit host", got)
	}
	hookMu.Lock()
	seen := hookSeen
	hookMu.Unlock()
	if seen != "billing.app.example" {
		t.Fatalf("hook Host = %q, want explicit host", seen)
	}

	env.Close()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("background did not stop on Close")
	}
}

// captureStore is Open-created state shared by Mount handlers and seeds.
// Close marks shutdown; reseed must never close it.
type captureStore struct {
	mu     sync.Mutex
	msg    string
	dirs   sandbox.Dirs
	closed bool
}

func (s *captureStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *captureStore) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.msg
}

func (s *captureStore) set(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msg = msg
}

// TestOpenFirstCapture proves the Open-first order: Open builds state
// from kit Dirs, Build threads it into the Mount and seeds, and reseed
// keeps the same handle instead of reopening.
func TestOpenFirstCapture(t *testing.T) {
	openCalls := 0
	var opened *captureStore
	var buildDirs sandbox.Dirs

	cfg := sandbox.Config{
		Open: func(d sandbox.Dirs) (io.Closer, error) {
			openCalls++
			// kit Dirs already exist; capture-style apps use them directly.
			for _, dir := range []string{d.Data, d.Apps, d.Forks} {
				if st, err := os.Stat(dir); err != nil || !st.IsDir() {
					return nil, fmt.Errorf("sandbox: missing dir %s", dir)
				}
			}
			if err := os.WriteFile(filepath.Join(d.Data, "marker.txt"), []byte("kit-dirs"), 0o644); err != nil {
				return nil, err
			}
			opened = &captureStore{msg: "open-created", dirs: d}
			return opened, nil
		},
		Build: func(d sandbox.Dirs, handle io.Closer) (sandbox.Built, error) {
			st, ok := handle.(*captureStore)
			if !ok {
				return sandbox.Built{}, fmt.Errorf("sandbox: handle is %T, want *captureStore", handle)
			}
			buildDirs = d
			app := v2.App{
				Resources: []v2.Resource{
					{
						Path: "/",
						Representations: []v2.Representation{
							{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
								raw, err := os.ReadFile(filepath.Join(st.dirs.Data, "marker.txt"))
								marker := "missing"
								if err == nil {
									marker = string(raw)
								}
								return text(st.get() + "|" + marker), nil
							}},
						},
					},
				},
			}
			return sandbox.Built{
				Mount: sandbox.Mount{App: app},
				Seeds: map[string]func(ctx context.Context) error{
					"greet": func(_ context.Context) error {
						st.set("seeded-hi")
						return nil
					},
				},
				Describe: func() any { return st.get() },
			}, nil
		},
	}
	env, err := sandbox.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	if openCalls != 1 {
		t.Fatalf("openCalls = %d, want 1 before reseed", openCalls)
	}
	if buildDirs.Data != opened.dirs.Data {
		t.Fatalf("Build dirs = %q, want Open dirs %q", buildDirs.Data, opened.dirs.Data)
	}
	first := opened
	client := env.WebClient()

	// Handler sees Open-created state plus the kit-Dirs marker file.
	if code, body := get(t, client, env.BaseURL+"/"); code != http.StatusOK || body != "open-created|kit-dirs" {
		t.Fatalf("GET / = %d %q, want Open-created state", code, body)
	}

	// Reseed runs the seed on the same store; the handle is stable.
	if err := env.Seed("greet"); err != nil {
		t.Fatal(err)
	}
	if openCalls != 1 {
		t.Fatalf("openCalls = %d after reseed, want stable handle (1)", openCalls)
	}
	if opened != first {
		t.Fatal("handle replaced across reseed, want stable identity")
	}
	opened.mu.Lock()
	closed := opened.closed
	opened.mu.Unlock()
	if closed {
		t.Fatal("handle closed across reseed, want close-once on Close")
	}
	if code, body := get(t, client, env.BaseURL+"/"); code != http.StatusOK || !strings.HasPrefix(body, "seeded-hi|") {
		t.Fatalf("GET / after reseed = %d %q, want seeded state on same handle", code, body)
	}
	if code, body := get(t, client, env.BaseURL+"/_control/state"); code != http.StatusOK || !strings.Contains(body, "seeded-hi") {
		t.Fatalf("GET /_control/state = %d %q, want seeded Describe", code, body)
	}
}

func getHost(t *testing.T, client *http.Client, url, host string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestExtraRawHatch locks Extra as the raw-handler escape hatch beside
// Resources: it runs on the mount instance, so e.Any file-style routes
// and Host-dispatched subdomains coexist with the typed app routes.
func TestExtraRawHatch(t *testing.T) {
	var hookSeen string
	var hookMu sync.Mutex
	ext := &stubExternal{email: "fake@example.com", hookSeen: &hookSeen, hookMu: &hookMu}
	cfg := testConfig(ext, &hookSeen, &hookMu)
	keep := cfg.Mount.Extra
	cfg.Mount.Extra = func(e *echo.Echo) {
		keep(e)
		e.Any("/files/*", func(c echo.Context) error {
			return c.String(http.StatusOK, "file:"+c.Param("*"))
		})
		e.GET("/whoami", func(c echo.Context) error {
			return c.String(http.StatusOK, c.Request().Host)
		})
	}
	env, err := sandbox.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	client := env.WebClient()

	// The typed resource route is untouched.
	if code, body := get(t, client, env.BaseURL+"/"); code != http.StatusOK || body != "hello" {
		t.Fatalf("GET / = %d %q, want 200 hello", code, body)
	}
	// e.Any passthrough: methods Resources never declare.
	if code, body := get(t, client, env.BaseURL+"/files/a.txt"); code != http.StatusOK || body != "file:a.txt" {
		t.Fatalf("GET /files/a.txt = %d %q, want file body", code, body)
	}
	resp, err := client.Post(env.BaseURL+"/files/a.txt", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(raw) != "file:a.txt" {
		t.Fatalf("POST /files/a.txt = %d %q, want Any passthrough", resp.StatusCode, raw)
	}
	// Subdomain-ish routing: same path, Host decides the shape.
	if code, body := getHost(t, client, env.BaseURL+"/whoami", "live.app.example"); code != http.StatusOK || body != "live.app.example" {
		t.Fatalf("GET /whoami Host=live = %d %q, want host echo", code, body)
	}
	if code, body := get(t, client, env.BaseURL+"/whoami"); code != http.StatusOK || !strings.Contains(body, "127.0.0.1") {
		t.Fatalf("GET /whoami dial host = %d %q, want loopback host", code, body)
	}
}

func payConfig(ext *stubExternal, hookSeen *string, hookMu *sync.Mutex) sandbox.Config {
	cfg := testConfig(ext, hookSeen, hookMu)
	mount := cfg.Mount
	mount.Host = "billing.app.example"
	keep := mount.Extra
	mount.Extra = func(e *echo.Echo) {
		keep(e)
		// Pay-page-like GET: app page served beside the control plane.
		e.GET("/pay/:id", func(c echo.Context) error {
			return c.HTML(http.StatusOK, "<h1>checkout for "+c.Param("id")+"</h1>")
		})
		// Activate-like POST: world behavior through External methods
		// only — Set marks active, Fire delivers the webhook.
		e.POST("/pay/activate", func(c echo.Context) error {
			var req struct {
				Email string `json:"email"`
				Host  string `json:"host"`
			}
			if err := c.Bind(&req); err != nil || req.Email == "" {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "email required"})
			}
			if err := ext.Set(map[string]any{"email": req.Email}); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			host := req.Host
			if host == "" {
				host = mount.Host
			}
			if err := ext.Fire(host); err != nil {
				return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
			}
			return c.JSON(http.StatusOK, map[string]string{"activated": req.Email, "host": host})
		})
	}
	cfg.Mount = mount
	return cfg
}

// TestBehavioralPayFlow reconciles the Fakes-mux ask against the FINAL
// contract: app pages via Extra, world behavior via External methods —
// a pay-page-like GET plus an activate-like POST driving Set+Fire, with
// no mux allowlist resurrected.
func TestBehavioralPayFlow(t *testing.T) {
	var hookSeen string
	var hookMu sync.Mutex
	ext := &stubExternal{email: "fake@example.com", hookSeen: &hookSeen, hookMu: &hookMu}
	env, err := sandbox.Start(payConfig(ext, &hookSeen, &hookMu))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	ext.mu.Lock()
	ext.hookURL = env.BaseURL + "/hook"
	ext.mu.Unlock()
	client := env.WebClient()

	if code, body := get(t, client, env.BaseURL+"/pay/member-1"); code != http.StatusOK || !strings.Contains(body, "member-1") {
		t.Fatalf("GET /pay/member-1 = %d %q, want checkout page", code, body)
	}

	// Activate without a host: the Mount derivation fills it in.
	activate := strings.NewReader(`{"email":"buyer@example.com"}`)
	resp, err := client.Post(env.BaseURL+"/pay/activate", "application/json", activate)
	if err != nil {
		t.Fatal(err)
	}
	var done map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&done)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || done["activated"] != "buyer@example.com" || done["host"] != "billing.app.example" {
		t.Fatalf("POST /pay/activate = %d %v, want buyer via Mount host", resp.StatusCode, done)
	}
	hookMu.Lock()
	seen := hookSeen
	hookMu.Unlock()
	if seen != "billing.app.example" {
		t.Fatalf("hook Host = %q, want Mount host", seen)
	}
	if code, body := get(t, client, env.BaseURL+"/_control/world/mail"); code != http.StatusOK || !strings.Contains(body, "buyer@example.com") {
		t.Fatalf("world after activate = %d %q, want buyer state", code, body)
	}

	// An explicit host overrides the Mount default.
	activate = strings.NewReader(`{"email":"other@example.com","host":"pay.example"}`)
	resp, err = client.Post(env.BaseURL+"/pay/activate", "application/json", activate)
	if err != nil {
		t.Fatal(err)
	}
	done = map[string]string{}
	_ = json.NewDecoder(resp.Body).Decode(&done)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || done["host"] != "pay.example" {
		t.Fatalf("POST /pay/activate explicit = %d %v, want override", resp.StatusCode, done)
	}
}

// TestMountHostDefault locks the host-mapping rule: a fire that omits the
// host synthesizes Mount.Host instead of leaking the dial address, and an
// explicit host still wins.
func TestMountHostDefault(t *testing.T) {
	var hookSeen string
	var hookMu sync.Mutex
	ext := &stubExternal{email: "fake@example.com", hookSeen: &hookSeen, hookMu: &hookMu}
	cfg := testConfig(ext, &hookSeen, &hookMu)
	cfg.Mount.Host = "billing.app.example"
	env, err := sandbox.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	ext.mu.Lock()
	ext.hookURL = env.BaseURL + "/hook"
	ext.mu.Unlock()
	client := env.WebClient()

	fire := func(body string) map[string]string {
		t.Helper()
		resp, err := client.Post(env.BaseURL+"/_control/world/mail/fire", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&out)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST fire %q = %d %v", body, resp.StatusCode, out)
		}
		return out
	}

	if out := fire(`{}`); out["host"] != "billing.app.example" {
		t.Fatalf("fire without host = %v, want Mount host", out)
	}
	hookMu.Lock()
	seen := hookSeen
	hookMu.Unlock()
	if seen != "billing.app.example" {
		t.Fatalf("hook Host = %q, want synthesized Mount host", seen)
	}
	if out := fire(`{"host":"other.example"}`); out["host"] != "other.example" {
		t.Fatalf("fire explicit host = %v, want override", out)
	}
}

// TestSingleIdentityScope locks the deferred As decision: cookie jars do
// not partition state. Identity swaps through reseed, not concurrent
// scopes, because the stores are shared in-process.
func TestSingleIdentityScope(t *testing.T) {
	var hookSeen string
	var hookMu sync.Mutex
	ext := &stubExternal{email: "fake@example.com", hookSeen: &hookSeen, hookMu: &hookMu}
	env, err := sandbox.Start(testConfig(ext, &hookSeen, &hookMu))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	a, b := env.WebClient(), env.WebClient()

	if _, body := get(t, a, env.BaseURL+"/_control/state"); !strings.Contains(body, "alpha@example.com") {
		t.Fatalf("jar A state = %q, want default Describe", body)
	}
	if _, body := get(t, b, env.BaseURL+"/_control/state"); !strings.Contains(body, "alpha@example.com") {
		t.Fatalf("jar B state = %q, want same shared state", body)
	}
	if err := env.Seed("beta"); err != nil {
		t.Fatal(err)
	}
	// Both jars observe the reseed: no per-scope isolation exists.
	for name, c := range map[string]*http.Client{"A": a, "B": b} {
		if _, body := get(t, c, env.BaseURL+"/_control/state"); !strings.Contains(body, "beta@example.com") {
			t.Fatalf("jar %s after reseed = %q, want shared beta state", name, body)
		}
	}
}
