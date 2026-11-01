// Package sandbox runs one app shape in isolation for end-to-end tests.
//
// Transport plus lifecycle only: it binds the app to one localhost port,
// serves fakes on a second mux, and owns a throwaway Dirs root per run.
// It never imports app code; per-run state enters through Config seeds
// and leaves through Describe, with the lifecycle handle as io.Closer.
//
// Kit starts only what the app hands over: the mount, the fakes mux,
// and Mount.Background services. There is no extractor or hidden runner.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"

	v2 "github.com/joyboydata/kit/hxkit/v2"
)

// Dirs is the throwaway root for one run. The harness creates it on
// Start, wipes the three buckets on reseed, and removes it on Close,
// so seeds and Open never manage temp lifetime themselves.
type Dirs struct {
	Root  string
	Data  string
	Apps  string
	Forks string
}

type dirsKey struct{}

// DirsFrom recovers the run root inside a seed func. Seeds are declared
// before the root exists, so the harness threads it via context.
func DirsFrom(ctx context.Context) (Dirs, bool) {
	d, ok := ctx.Value(dirsKey{}).(Dirs)
	return d, ok
}

// RouteFunc builds the app-facing URL for a subdomain label and path:
// "" is the bare root, "app" the app host, "<id>.app" a live app.
// Shape knowledge lives app-side; kit only dials what it returns.
type RouteFunc func(sub, path string) string

// Webhook names the app endpoint fakes fire into. Kit resolves it to a
// dialable URL plus the Host header the subdomain router expects; the
// app fire path must set req.Host explicitly (see Env).
type Webhook struct {
	Sub  string
	Path string
}

// Mount is one app shape on one port. Extra adds app action tweaks as
// POSTs beside the control plane; nil Extra leaves the pure flow.
type Mount struct {
	App   v2.App
	Extra func(e *echo.Echo)

	// Background are app-owned services (runner, server, fake agent)
	// started after the default seed and stopped on Close. A non-nil
	// return cancels the run; error reporting itself stays app-side.
	// Reseed wipes buckets under running services, so hand over ones
	// tolerant of that.
	Background []func(ctx context.Context) error

	// Routes maps subdomain shapes onto URLs once the mount port is
	// bound; nil means single-port (every label serves BaseURL).
	Routes func(baseURL string) RouteFunc

	// Webhook is the fire target; zero means no webhook slot.
	Webhook Webhook
}

// Control is the handle fakes receive: drive app state without
// importing it. Seed wipes and reseeds; State reports Describe.
type Control struct {
	h *harness
}

// Seed wipes the buckets, reopens, and runs the named scenario.
func (c Control) Seed(scenario string) error {
	if c.h == nil {
		return fmt.Errorf("sandbox: nil control")
	}
	return c.h.reseed(scenario)
}

// State reports Describe bare, mirroring GET /_control/state.
func (c Control) State() any {
	if c.h == nil {
		return nil
	}
	return c.h.describe()
}

// Config wires the run. Seeds write marker files under DirsFrom(ctx)
// or no-op; Open builds the lifecycle handle over Dirs (nil = none);
// Describe snapshots state for GET /_control/state (nil = null).
// Fakes registers behavior endpoints on its own mux with no allowlist:
// fake identity, payment activation, webhook firing, and seed UI live
// app-side beside the built-in state resource.
type Config struct {
	Mount    Mount
	Fakes    func(mux *http.ServeMux, ctl Control)
	Seeds    map[string]func(ctx context.Context) error
	Default  string
	Open     func(Dirs) (io.Closer, error)
	Describe func() any
}

// Env is the live run: two bases plus the client that drives them.
// WebClient carries a cookie jar and needs no DNS; Seed hits the
// control plane over HTTP like any out-of-process driver would.
type Env struct {
	// BaseURL is the mount (app + control plane), one localhost port.
	BaseURL string
	// FakeBase is the fakes mux on its own port.
	FakeBase string

	// WebhookURL is the dialable fire target (mount host + path).
	WebhookURL string
	// WebhookHost is the Host header the subdomain router dispatches
	// on — the fire path must set req.Host to this. It carries the full
	// subdomain host including port (the ServeDomain shape); a bare
	// host silently serves the wrong shape, which is the domain trap.
	WebhookHost string

	h         *harness
	mountPort string
	route     RouteFunc
	dirs      Dirs
	closeOnce sync.Once
}

// Dirs exposes the run root so tests can assert on seed markers.
func (e *Env) Dirs() Dirs { return e.dirs }

// Route builds the app-facing URL for sub+path; drive it with
// WebClient, which preserves Host while dialing the mount port.
func (e *Env) Route(sub, path string) string {
	if e.route == nil {
		return e.BaseURL + path
	}
	return e.route(sub, path)
}

// WebClient dials any hostname at the mount port, so tests skip DNS
// while the real HTTP stack (redirects, cookies, Host) still runs.
func (e *Env) WebClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	port := e.mountPort
	return &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return (&net.Dialer{}).DialContext(ctx, network, addr)
				}
				if ip := net.ParseIP(host); ip != nil {
					return (&net.Dialer{}).DialContext(ctx, network, addr)
				}
				return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
			},
		},
	}
}

// Seed reseeds through the control plane over HTTP.
func (e *Env) Seed(scenario string) error {
	body, _ := json.Marshal(map[string]string{"scenario": scenario})
	req, err := http.NewRequest(http.MethodPut, e.BaseURL+"/_control/state", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.WebClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var msg map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&msg)
		return fmt.Errorf("sandbox: seed %q: %s", scenario, msg["error"])
	}
	return nil
}

// Close stops background services and both servers, closes the handle,
// and removes the root.
func (e *Env) Close() {
	e.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e.h != nil {
			e.h.shutdown(ctx)
		}
	})
}

// Start boots the mount plus fakes in-process on ephemeral ports.
func Start(cfg Config) (*Env, error) {
	root, err := os.MkdirTemp("", "sandbox-*")
	if err != nil {
		return nil, err
	}
	dirs := Dirs{
		Root:  root,
		Data:  filepath.Join(root, "data"),
		Apps:  filepath.Join(root, "apps"),
		Forks: filepath.Join(root, "forks"),
	}
	for _, d := range []string{dirs.Data, dirs.Apps, dirs.Forks} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
	}

	h := &harness{cfg: cfg, dirs: dirs}

	e := echo.New()
	e.HideBanner = true
	cfg.Mount.App.Register(e)
	e.GET("/_control/state", h.serveGetState)
	e.PUT("/_control/state", h.servePutState)
	e.POST("/_control/seed", h.servePostSeed)
	e.GET("/_control/scenarios", h.serveScenarios)
	if cfg.Mount.Extra != nil {
		cfg.Mount.Extra(e)
	}
	mountLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	h.mountSrv = &http.Server{Handler: e}

	fakeMux := http.NewServeMux()
	if cfg.Fakes != nil {
		cfg.Fakes(fakeMux, Control{h: h})
	}
	fakeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = mountLn.Close()
		_ = os.RemoveAll(root)
		return nil, err
	}
	h.fakeSrv = &http.Server{Handler: fakeMux}

	if err := h.open(); err != nil {
		_ = mountLn.Close()
		_ = fakeLn.Close()
		_ = os.RemoveAll(root)
		return nil, err
	}
	if cfg.Default != "" {
		if err := h.reseed(cfg.Default); err != nil {
			h.shutdown(context.Background())
			return nil, err
		}
	}

	_, mountPort, _ := net.SplitHostPort(mountLn.Addr().String())
	baseURL := "http://127.0.0.1:" + mountPort
	route := RouteFunc(func(sub, path string) string { return baseURL + path })
	if cfg.Mount.Routes != nil {
		if r := cfg.Mount.Routes(baseURL); r != nil {
			route = r
		}
	}
	var webhookURL, webhookHost string
	if wh := cfg.Mount.Webhook; wh.Path != "" {
		webhookURL = baseURL + wh.Path
		if u, err := url.Parse(route(wh.Sub, wh.Path)); err == nil {
			webhookHost = u.Host
		}
	}

	runCtx, stop := context.WithCancel(context.Background())
	h.stop = stop
	for _, bg := range cfg.Mount.Background {
		if bg == nil {
			continue
		}
		h.bg.Add(1)
		go func(fn func(context.Context) error) {
			defer h.bg.Done()
			if err := fn(runCtx); err != nil {
				stop()
			}
		}(bg)
	}

	go h.mountSrv.Serve(mountLn) //nolint:errcheck // closed by Close
	go h.fakeSrv.Serve(fakeLn)   //nolint:errcheck // closed by Close

	env := &Env{
		BaseURL:     baseURL,
		FakeBase:    "http://" + fakeLn.Addr().String(),
		WebhookURL:  webhookURL,
		WebhookHost: webhookHost,
		h:           h,
		mountPort:   mountPort,
		route:       route,
		dirs:        dirs,
	}
	return env, nil
}

// Run boots the run and blocks until a signal tears it down.
func Run(cfg Config) error {
	env, err := Start(cfg)
	if err != nil {
		return err
	}
	defer env.Close()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	return nil
}

// harness owns the mutable run state; only reseed mutates it.
type harness struct {
	mu       sync.Mutex
	cfg      Config
	dirs     Dirs
	handle   io.Closer
	scenario string

	mountSrv *http.Server
	fakeSrv  *http.Server

	stop context.CancelFunc
	bg   sync.WaitGroup
}

func (h *harness) open() error {
	if h.cfg.Open == nil {
		return nil
	}
	handle, err := h.cfg.Open(h.dirs)
	if err != nil {
		return err
	}
	h.handle = handle
	return nil
}

// reseed wipes the buckets, reopens the handle, and runs the scenario.
// A fresh handle per scenario keeps runs hermetic without importing
// whatever the handle wraps.
func (h *harness) reseed(scenario string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	seed, ok := h.cfg.Seeds[scenario]
	if scenario != "" && !ok {
		return fmt.Errorf("sandbox: unknown scenario %q", scenario)
	}
	if h.handle != nil {
		_ = h.handle.Close()
		h.handle = nil
	}
	for _, d := range []string{h.dirs.Data, h.dirs.Apps, h.dirs.Forks} {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := h.open(); err != nil {
		return err
	}
	if seed != nil {
		ctx := context.WithValue(context.Background(), dirsKey{}, h.dirs)
		if err := seed(ctx); err != nil {
			return err
		}
	}
	h.scenario = scenario
	return nil
}

func (h *harness) describe() any {
	if h.cfg.Describe == nil {
		return nil
	}
	return h.cfg.Describe()
}

func (h *harness) report() map[string]any {
	return map[string]any{"scenario": h.scenario, "state": h.describe()}
}

func (h *harness) serveGetState(c echo.Context) error {
	return c.JSON(http.StatusOK, h.describe())
}

func (h *harness) servePutState(c echo.Context) error {
	var req struct {
		Scenario string `json:"scenario"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if err := h.reseed(req.Scenario); err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, h.report())
}

// servePostSeed is the compat alias: same wipe+reseed+report over a
// query param instead of a PUT body.
func (h *harness) servePostSeed(c echo.Context) error {
	scenario := c.QueryParam("scenario")
	if scenario == "" {
		var req struct {
			Scenario string `json:"scenario"`
		}
		if err := c.Bind(&req); err == nil {
			scenario = req.Scenario
		}
	}
	if err := h.reseed(scenario); err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, h.report())
}

func (h *harness) serveScenarios(c echo.Context) error {
	out := []string{}
	for name := range h.cfg.Seeds {
		out = append(out, name)
	}
	sort.Strings(out)
	return c.JSON(http.StatusOK, map[string]any{"scenarios": out})
}

func (h *harness) shutdown(ctx context.Context) {
	if h.stop != nil {
		h.stop()
	}
	wait := make(chan struct{})
	go func() { h.bg.Wait(); close(wait) }()
	select {
	case <-wait:
	case <-ctx.Done():
	}
	if h.mountSrv != nil {
		_ = h.mountSrv.Shutdown(ctx)
	}
	if h.fakeSrv != nil {
		_ = h.fakeSrv.Shutdown(ctx)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handle != nil {
		_ = h.handle.Close()
		h.handle = nil
	}
	_ = os.RemoveAll(h.dirs.Root)
}
