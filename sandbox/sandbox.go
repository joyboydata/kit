// Package sandbox runs one app shape in isolation for end-to-end tests.
//
// Single surface: the app, the state resource, and the world resource
// share one localhost port. There is no second mux and no subdomain
// routing; FakeBase aliases BaseURL for callers that still drive fakes
// over HTTP.
//
// Fakes are in-process Externals behind GET /_control/world. Per-run
// state enters through Config seeds and leaves through Describe, with
// the lifecycle handle as io.Closer. The harness creates a throwaway
// root per run, wipes the buckets on reseed, and removes it on Close,
// so seeds never manage temp lifetime themselves.
//
// Order is open -> resolve -> Register -> Default seed. Start makes the
// throwaway Dirs, calls Source.open once (Static returns nil, so Mount.App
// stays as-is with no builder), threads the handle plus Dirs into
// Source.resolve to derive the Mount (capture-style Dynamic builders close
// over the concrete open-created stores via kit Dirs directly), then
// registers Mount.App (+ Extra) and runs the Default seed. Open owns the
// handle lifetime: it closes once on Close, never across reseed, so runner
// subscriptions and handler captures stay stable; reseed hermeticity comes
// from wiping the buckets plus External.Reset, not from replacing objects.
//
// Domain trap: apps that dispatch on Host (subdomain routers) serve the
// wrong shape on a bare 127.0.0.1 URL. A webhook fire must dial the
// mount address yet set req.Host explicitly to the app-facing host;
// External.Fire receives that host verbatim for exactly this reason,
// and Env.WebClient preserves Host while dialing the mount port.
//
// Raw escape hatch: Mount.Extra runs on the same echo instance as the
// app, so anything Resources cannot declare — e.Any("/*") file serving,
// Host-dispatched subdomains, extra methods — lives there. Extra IS the
// hatch on purpose: a second raw slot would duplicate it with no extra
// power. Resources stay the typed home for app URLs; Extra is the raw
// one beside them.
//
// Host mapping: the mount is single-port, so a fire cannot infer the
// app-facing host from the dial address (the trap above). Mount.Host
// names the host the app would see in production; an empty fire host
// falls back to it, and an explicit per-fire host overrides it.
//
// Behavior control: there is no Fakes mux. World behavior goes through
// External methods (Set/Fire/Reset over the world routes) and app pages
// plus action endpoints through Extra — e.g. a checkout GET plus an
// activate POST that drives Set+Fire. A behavior neither can express is
// a deliberate contract gap: add the minimal slot and say so loudly.
//
// Identity: one fixture identity per run. Reseed swaps the identity
// through seeds the way fixtures do, and a fresh harness is a cheap full
// reset. There is no As scoping because the stores are shared
// in-process — a scoped handle would fake an isolation the harness does
// not enforce.
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

// Dirs are the throwaway buckets for one run.
type Dirs struct {
	Data  string
	Apps  string
	Forks string
}

// Mount is one app shape on one port. Extra is the raw-handler escape
// hatch beside Resources: it runs on the mount instance, so it can add
// e.Any routes, Host-dispatched subdomains, or methods Resources cannot
// declare. Nil means the pure flow.
type Mount struct {
	App v2.App
	// Host is the app-facing host synthesized for fires that omit one
	// (e.g. "app.<rootDomain>"). The app overrides the default by
	// setting it; an explicit per-fire host always wins.
	Host  string
	Extra func(e *echo.Echo)
}

// External is one fake service behind the world resource. State reports
// for GET, Set applies a PUT body, Fire dials the app with req.Host set
// to the given host (never the dial address), Reset restores defaults
// so reseed wipes under running services.
type External interface {
	Name() string
	State() any
	Set(state map[string]any) error
	Fire(host string) error
	Reset()
}

// Built is the resolve-derived app surface. Mount handlers and seed
// funcs close over the same open-created stores, so reseed never
// replaces them out from under the running mount.
type Built struct {
	Mount    Mount
	Seeds    map[string]func(ctx context.Context) error
	Describe func() any
}

// Source is the polymorphic mount behind Start. Methods are unexported so
// only kit constructs Sources: Static for the pure flow, Dynamic for the
// open/build flow.
type Source interface {
	open(d Dirs) (io.Closer, error)
	resolve(d Dirs, h io.Closer) (Built, error)
}

// Static is today's pure flow (nil-Open behavior): open returns a nil
// handle, resolve returns the Mount/Seeds/Describe as Built.
type Static struct {
	Mount    Mount
	Seeds    map[string]func(ctx context.Context) error
	Describe func() any
}

func (s Static) open(_ Dirs) (io.Closer, error) { return nil, nil }

func (s Static) resolve(_ Dirs, _ io.Closer) (Built, error) {
	return Built(s), nil
}

// Dynamic is today's Build path: open calls Open, resolve calls Build
// with the Dirs plus the open handle.
type Dynamic struct {
	Open  func(d Dirs) (io.Closer, error)
	Build func(d Dirs, handle io.Closer) (Built, error)
}

func (d Dynamic) open(dirs Dirs) (io.Closer, error) {
	if d.Open == nil {
		return nil, nil
	}
	return d.Open(dirs)
}

func (d Dynamic) resolve(dirs Dirs, h io.Closer) (Built, error) {
	if d.Build == nil {
		return Built{}, nil
	}
	return d.Build(dirs, h)
}

// legacySource preserves the deprecated Config fields with the old merge
// semantics: base Mount/Seeds/Describe, replaced by Build's non-nil
// results. Pure-legacy configs are equivalent to Static (no Open/Build)
// or Dynamic (no Mount/Seeds/Describe); only mixed configs need this
// adapter.
type legacySource struct {
	mount    Mount
	seeds    map[string]func(ctx context.Context) error
	describe func() any
	openFn   func(d Dirs) (io.Closer, error)
	buildFn  func(d Dirs, handle io.Closer) (Built, error)
}

func (l legacySource) open(dirs Dirs) (io.Closer, error) {
	if l.openFn == nil {
		return nil, nil
	}
	return l.openFn(dirs)
}

func (l legacySource) resolve(dirs Dirs, h io.Closer) (Built, error) {
	b := Built{Mount: l.mount, Seeds: l.seeds, Describe: l.describe}
	if l.buildFn == nil {
		return b, nil
	}
	built, err := l.buildFn(dirs, h)
	if err != nil {
		return Built{}, err
	}
	b.Mount = built.Mount
	if built.Seeds != nil {
		b.Seeds = built.Seeds
	}
	if built.Describe != nil {
		b.Describe = built.Describe
	}
	return b, nil
}

// Config wires the run. Source is the mount: Static for the pure flow,
// Dynamic for the open/build flow. Describe snapshots state for GET
// /_control/state (nil renders null). Background starters run after the
// default seed and stop on Close; they must tolerate reseed wiping the
// buckets underneath them.
//
// Deprecated: Mount, Seeds, Describe, Open, Build are still accepted and
// mapped internally (pure Mount/Seeds/Describe to Static, pure Open/Build
// to Dynamic, mixed to the old merge). New code must set Source and leave
// them unset; setting both returns an error from Start/Run.
type Config struct {
	Source     Source
	Externals  []External
	Default    string
	Background []func(ctx context.Context) error

	// Deprecated: use Source (Static/Dynamic) instead.
	Mount    Mount
	Seeds    map[string]func(ctx context.Context) error
	Describe func() any
	Open     func(d Dirs) (io.Closer, error)
	Build    func(d Dirs, handle io.Closer) (Built, error)
}

// effectiveSource maps Config to the single polymorphic Source. Pure
// deprecated configs become Static/Dynamic directly; mixed ones keep the
// old merge via legacySource.
func (c Config) effectiveSource() (Source, error) {
	if c.Source != nil {
		if c.Mount.App.Resources != nil || c.Mount.Host != "" || c.Mount.Extra != nil ||
			c.Seeds != nil || c.Describe != nil || c.Open != nil || c.Build != nil {
			return nil, fmt.Errorf("sandbox: Config.Source set with deprecated Mount/Seeds/Describe/Open/Build; use Static/Dynamic only")
		}
		return c.Source, nil
	}
	if c.Open == nil && c.Build == nil {
		return Static{Mount: c.Mount, Seeds: c.Seeds, Describe: c.Describe}, nil
	}
	if c.Mount.App.Resources == nil && c.Mount.Host == "" && c.Mount.Extra == nil &&
		c.Seeds == nil && c.Describe == nil {
		return Dynamic{Open: c.Open, Build: c.Build}, nil
	}
	return legacySource{
		mount:    c.Mount,
		seeds:    c.Seeds,
		describe: c.Describe,
		openFn:   c.Open,
		buildFn:  c.Build,
	}, nil
}

// Env is the live run. WebClient carries a jar and dials 127.0.0.1 for
// any hostname, preserving Host so tests skip DNS while the real stack
// still runs. Seed hits the control plane over HTTP like any
// out-of-process driver would.
type Env struct {
	BaseURL  string
	FakeBase string

	WebClient func() *http.Client
	Seed      func(scenario string) error
	Close     func()
}

// Start boots the mount in-process on an ephemeral port.
func Start(cfg Config) (*Env, error) {
	root, err := os.MkdirTemp("", "sandbox-*")
	if err != nil {
		return nil, err
	}
	dirs := Dirs{
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

	h := &harness{externals: cfg.Externals, dirs: dirs, root: root}

	src, err := cfg.effectiveSource()
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}

	// Open-first: stores exist before the mount registers, so builders
	// close over kit Dirs directly instead of a caller-owned mirror.
	handle, err := src.open(dirs)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	h.handle = handle
	built, err := src.resolve(dirs, handle)
	if err != nil {
		if h.handle != nil {
			_ = h.handle.Close()
			h.handle = nil
		}
		_ = os.RemoveAll(root)
		return nil, err
	}
	h.mount = built.Mount
	h.seeds = built.Seeds
	h.describeFn = built.Describe

	e := echo.New()
	e.HideBanner = true
	h.mount.App.Register(e)
	e.GET("/_control/state", h.serveGetState)
	e.PUT("/_control/state", h.servePutState)
	e.POST("/_control/seed", h.servePostSeed)
	e.GET("/_control/scenarios", h.serveScenarios)
	e.GET("/_control/world", h.serveGetWorld)
	e.GET("/_control/world/:name", h.serveGetWorldOne)
	e.PUT("/_control/world/:name", h.servePutWorldOne)
	e.POST("/_control/world/:name/fire", h.servePostFireOne)
	if h.mount.Extra != nil {
		h.mount.Extra(e)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if h.handle != nil {
			_ = h.handle.Close()
			h.handle = nil
		}
		_ = os.RemoveAll(root)
		return nil, err
	}
	h.srv = &http.Server{Handler: e}

	if cfg.Default != "" {
		if err := h.reseed(cfg.Default); err != nil {
			_ = ln.Close()
			h.shutdown(context.Background())
			return nil, err
		}
	}

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	baseURL := "http://127.0.0.1:" + port

	runCtx, stop := context.WithCancel(context.Background())
	h.stop = stop
	for _, bg := range cfg.Background {
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

	go func() { _ = h.srv.Serve(ln) }()

	var once sync.Once
	env := &Env{BaseURL: baseURL, FakeBase: baseURL}
	env.WebClient = func() *http.Client {
		jar, _ := cookiejar.New(nil)
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
					// Host survives; only the dial target is rewritten.
					return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
				},
			},
		}
	}
	env.Seed = func(scenario string) error {
		body, _ := json.Marshal(map[string]string{"scenario": scenario})
		req, err := http.NewRequest(http.MethodPut, baseURL+"/_control/state", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		client := env.WebClient()
		resp, err := client.Do(req)
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
	env.Close = func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			h.shutdown(ctx)
		})
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

// harness owns the mutable run state; only reseed mutates scenario.
// mount/seeds/describeFn are the effective values from Source.resolve
// before Register. The handle opens once and closes once; reseed never
// touches it.
type harness struct {
	mu         sync.Mutex
	externals  []External
	dirs       Dirs
	root       string
	handle     io.Closer
	mount      Mount
	seeds      map[string]func(ctx context.Context) error
	describeFn func() any
	scenario   string

	srv  *http.Server
	stop context.CancelFunc
	bg   sync.WaitGroup
}

// reseed wipes the buckets, resets externals, and runs the scenario.
// Empty means wipe-only. The Open handle stays stable across reseed so
// Mount captures and runner subscriptions survive; hermeticity comes
// from the wipe plus Reset, not from reopening.
func (h *harness) reseed(scenario string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	seed, ok := h.seeds[scenario]
	if scenario != "" && !ok {
		return fmt.Errorf("sandbox: unknown scenario %q", scenario)
	}
	for _, d := range []string{h.dirs.Data, h.dirs.Apps, h.dirs.Forks} {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	for _, ext := range h.externals {
		if ext != nil {
			ext.Reset()
		}
	}
	if seed != nil {
		if err := seed(context.Background()); err != nil {
			return err
		}
	}
	h.scenario = scenario
	return nil
}

func (h *harness) describe() any {
	if h.describeFn == nil {
		return nil
	}
	return h.describeFn()
}

func (h *harness) report() map[string]any {
	return map[string]any{"scenario": h.scenario, "state": h.describe()}
}

func (h *harness) lookup(name string) External {
	for _, ext := range h.externals {
		if ext != nil && ext.Name() == name {
			return ext
		}
	}
	return nil
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

// servePostSeed is the compat alias for PUT /_control/state.
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
	for name := range h.seeds {
		out = append(out, name)
	}
	sort.Strings(out)
	return c.JSON(http.StatusOK, map[string]any{"scenarios": out})
}

func (h *harness) serveGetWorld(c echo.Context) error {
	out := map[string]any{}
	for _, ext := range h.externals {
		if ext != nil {
			out[ext.Name()] = ext.State()
		}
	}
	return c.JSON(http.StatusOK, out)
}

func (h *harness) serveGetWorldOne(c echo.Context) error {
	ext := h.lookup(c.Param("name"))
	if ext == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "sandbox: unknown external"})
	}
	return c.JSON(http.StatusOK, ext.State())
}

func (h *harness) servePutWorldOne(c echo.Context) error {
	ext := h.lookup(c.Param("name"))
	if ext == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "sandbox: unknown external"})
	}
	var state map[string]any
	if err := c.Bind(&state); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if err := ext.Set(state); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, ext.State())
}

func (h *harness) servePostFireOne(c echo.Context) error {
	ext := h.lookup(c.Param("name"))
	if ext == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "sandbox: unknown external"})
	}
	host := c.QueryParam("host")
	if host == "" {
		var req struct {
			Host string `json:"host"`
		}
		if err := c.Bind(&req); err == nil {
			host = req.Host
		}
	}
	// Single-port mount, so the dial address never names the app-facing
	// host (domain trap): fall back to the Mount derivation before the
	// raw request host.
	if host == "" {
		host = h.mount.Host
	}
	if host == "" {
		host = c.Request().Host
	}
	if err := ext.Fire(host); err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]string{"fired": ext.Name(), "host": host})
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
	if h.srv != nil {
		_ = h.srv.Shutdown(ctx)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handle != nil {
		_ = h.handle.Close()
		h.handle = nil
	}
	_ = os.RemoveAll(h.root)
}
