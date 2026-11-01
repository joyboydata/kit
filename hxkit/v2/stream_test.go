package v2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

type streamState struct {
	mu       sync.Mutex
	content  string
	unsubbed bool
}

func (s *streamState) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.content
}

func (s *streamState) set(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.content = v
}

func (s *streamState) markUnsubbed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsubbed = true
}

func (s *streamState) wasUnsubbed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unsubbed
}

func streamApp(st *streamState, fire chan struct{}, tick time.Duration) App {
	return App{
		Resources: []Resource{
			{
				Path: "/things",
				Representations: []Representation{
					{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
						return text("things"), nil
					}},
				},
				Streams: []Stream{
					{
						Path:   "events",
						Replay: true,
						Tick:   tick,
						Events: []Event{
							{
								Name: "things",
								Subscribe: func(c echo.Context) (<-chan struct{}, func()) {
									return fire, st.markUnsubbed
								},
								Render: func(c echo.Context) (templ.Component, error) {
									return text(st.get()), nil
								},
							},
						},
					},
				},
			},
		},
	}
}

func TestStreamReplayAndHeaders(t *testing.T) {
	st := &streamState{content: "<li>one</li>"}
	fire := make(chan struct{}, 4)

	e := echo.New()
	streamApp(st, fire, 10*time.Millisecond).Register(e)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	req := httptest.NewRequest(http.MethodGet, "/things/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: things\n") {
		t.Fatalf("missing event line, got %q", body)
	}
	if !strings.Contains(body, "data: <li>one</li>\n") {
		t.Fatalf("missing data line, got %q", body)
	}
	if !st.wasUnsubbed() {
		t.Fatal("unsubscribe func should run when the connection ends")
	}
}

func TestStreamDedupsIdenticalRenders(t *testing.T) {
	st := &streamState{content: "v1"}
	fire := make(chan struct{}, 4)

	e := echo.New()
	streamApp(st, fire, 10*time.Millisecond).Register(e)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		fire <- struct{}{}
		time.Sleep(50 * time.Millisecond)
		st.set("v2")
		fire <- struct{}{}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	req := httptest.NewRequest(http.MethodGet, "/things/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if n := strings.Count(rec.Body.String(), "event: things\n"); n != 2 {
		t.Fatalf("event frames = %d, want 2 (replay + change); body %q", n, rec.Body.String())
	}
}

func TestStreamSplitsMultilineData(t *testing.T) {
	st := &streamState{content: "a\nb"}
	fire := make(chan struct{}, 4)

	e := echo.New()
	streamApp(st, fire, 10*time.Millisecond).Register(e)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	req := httptest.NewRequest(http.MethodGet, "/things/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "data: a\ndata: b\n") {
		t.Fatalf("multiline data must split per line, got %q", body)
	}
}

func TestStreamWithoutEvents404s(t *testing.T) {
	app := App{Resources: []Resource{{
		Path: "/empty",
		Representations: []Representation{{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
			return text("x"), nil
		}}},
		Streams: []Stream{{Path: "events"}},
	}}}
	rec := serve(app, http.MethodGet, "/empty/events", false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

type dynState struct {
	streamState
	tick     time.Duration
	redirect string
}

func dynApp(st *dynState, fire chan struct{}) App {
	return App{
		Resources: []Resource{{
			Path: "/rev",
			Representations: []Representation{{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
				return text("rev"), nil
			}}},
			Streams: []Stream{{
				Path:   "events",
				Replay: true,
				Tick:   st.tick,
				Events: []Event{{
					Name: "rev",
					Subscribe: func(c echo.Context) (<-chan struct{}, func()) {
						return fire, st.markUnsubbed
					},
					Resolve: func(c echo.Context) []Fired {
						return []Fired{
							{Name: "rev", Component: text(st.get())},
							{Name: "side", Component: text("side:" + st.get())},
						}
					},
				}},
				Terminal: func(c echo.Context) (string, bool) {
					if st.redirect != "" {
						return st.redirect, true
					}
					return "", false
				},
			}},
		}},
	}
}

func TestResolveServesNamedFired(t *testing.T) {
	st := &dynState{streamState: streamState{content: "r1"}, tick: 10 * time.Millisecond}
	fire := make(chan struct{}, 4)

	e := echo.New()
	dynApp(st, fire).Register(e)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		st.set("r2")
		fire <- struct{}{}
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	req := httptest.NewRequest(http.MethodGet, "/rev/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: rev\n") || !strings.Contains(body, "data: r2\n") {
		t.Fatalf("resolve should push updated frames, got %q", body)
	}
	if !strings.Contains(body, "event: side\n") || !strings.Contains(body, "data: side:r2\n") {
		t.Fatalf("one resolve may feed several regions, got %q", body)
	}
}

func TestDebounceCoalescesBursts(t *testing.T) {
	st := &dynState{streamState: streamState{content: "v1"}, tick: 30 * time.Millisecond}
	fire := make(chan struct{}, 4)

	e := echo.New()
	dynApp(st, fire).Register(e)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		fire <- struct{}{}
		time.Sleep(5 * time.Millisecond)
		fire <- struct{}{}
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	req := httptest.NewRequest(http.MethodGet, "/rev/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if n := strings.Count(rec.Body.String(), "event: rev\n"); n != 1 {
		t.Fatalf("burst should coalesce into replay only, frames = %d: %q", n, rec.Body.String())
	}
}

func TestTerminalRedirectCloses(t *testing.T) {
	st := &dynState{streamState: streamState{content: "v1"}, redirect: "/done"}
	fire := make(chan struct{}, 4)

	e := echo.New()
	dynApp(st, fire).Register(e)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(time.Second, cancel)
	req := httptest.NewRequest(http.MethodGet, "/rev/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: redirect\n") || !strings.Contains(body, "data: /done\n") {
		t.Fatalf("terminal should emit redirect frame, got %q", body)
	}
	if !st.wasUnsubbed() {
		t.Fatal("terminal close should still unsubscribe")
	}
}
