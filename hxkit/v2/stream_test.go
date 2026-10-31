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

func streamApp(st *streamState, fire chan struct{}) App {
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
						Path:   "/things/events",
						Replay: true,
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
	streamApp(st, fire).Register(e)

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
	streamApp(st, fire).Register(e)

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
	streamApp(st, fire).Register(e)

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
		Streams: []Stream{{Path: "/empty/events"}},
	}}}
	rec := serve(app, http.MethodGet, "/empty/events", false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
