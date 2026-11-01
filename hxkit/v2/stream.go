package v2

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

// Watch is the subscription seam for streams. It receives the request's
// echo.Context so the app can read request state (e.g. auth.GetUser(c))
// to build per-user store subscriptions, and returns a channel signaled
// on every state change plus an unsubscribe func the framework calls
// when the SSE connection ends. Kit takes echo already; it still never
// imports a datastore, operation log, or view package.
type Watch func(c echo.Context) (<-chan struct{}, func())

// Event is one named live region on a stream. Name must equal the
// layout's sse-swap value: that string is the htmx-ext-sse contract
// binding a pushed frame to the element it swaps. Resolve computes the
// current frames on demand; when it is nil the static Render path is
// used as a degenerate constant Resolve of one Fired.
type Event struct {
	Name      string
	Subscribe Watch
	Render    Handler
	Resolve   func(c echo.Context) []Fired
}

// Fired is one resolved frame: the region to swap and what to put in
// it. Resolve may return frames for regions beyond its own event name,
// so one subscription can update several layout slots at once.
type Fired struct {
	Name      string
	Component templ.Component
}

// DefaultTick is the debounce window coalescing notification bursts
// into one resolve when Stream.Tick is unset.
const DefaultTick = 80 * time.Millisecond

// redirectEvent is the terminal frame name. The app's client script
// listens for it and navigates to the frame URL.
const redirectEvent = "redirect"

// Stream is an SSE endpoint owned by a resource. Path is always relative
// to the resource path like a mutation (a leading "/" is stripped, ""
// means the resource path itself): "stream" under "/apps/:id" serves
// /apps/:id/stream. There is no absolute override; off-prefix endpoints
// are their own Resource. Replay pushes current
// state on connect so late joiners converge without waiting for the
// next change. Tick bounds the resolve rate: notification bursts
// coalesce into one resolve per quiet window (DefaultTick when unset).
// Terminal, when non-nil, ends the stream: returning (url, true) emits
// a redirect frame with the URL, flushes, and closes. A nil Terminal
// lives until the client disconnects.
type Stream struct {
	Path     string
	Replay   bool
	Tick     time.Duration
	Events   []Event
	Terminal func(c echo.Context) (string, bool)
}

// serveStream owns replay-on-connect, framing, flush, debounce, and
// dedup. Each flush resolves every event, skips frames identical to
// the last sent for that name, and skips render errors while keeping
// the connection alive. Notifications arriving inside one tick window
// coalesce into a single flush. The loop ends when Terminal fires or
// the client goes away.
func (a App) serveStream(s Stream) echo.HandlerFunc {
	return func(c echo.Context) error {
		if len(s.Events) == 0 {
			return a.writeError(c, E(http.StatusNotFound, "stream has no events"))
		}

		res := c.Response()
		h := res.Header()
		h.Set(echo.HeaderContentType, "text/event-stream")
		h.Set(echo.HeaderCacheControl, "no-cache")
		h.Set(echo.HeaderConnection, "keep-alive")
		h.Set("X-Accel-Buffering", "no")

		fl, ok := res.Writer.(http.Flusher)
		if !ok {
			return a.writeError(c, E(http.StatusInternalServerError, "streaming unsupported"))
		}

		ctx := c.Request().Context()
		res.WriteHeader(http.StatusOK)
		fl.Flush()

		tick := s.Tick
		if tick <= 0 {
			tick = DefaultTick
		}

		last := make(map[string]string)
		flush := func() bool {
			for i := range s.Events {
				for _, f := range resolveEvent(c, &s.Events[i]) {
					if f.Name == "" || f.Component == nil {
						continue
					}
					html, err := renderString(ctx, f.Component)
					if err != nil || html == last[f.Name] {
						continue
					}
					last[f.Name] = html
					if err := writeFrame(res.Writer, f.Name, html); err != nil {
						return true
					}
					fl.Flush()
				}
			}
			if s.Terminal != nil {
				if url, ok := s.Terminal(c); ok {
					if err := writeFrame(res.Writer, redirectEvent, url); err != nil {
						return true
					}
					fl.Flush()
					return true
				}
			}
			return false
		}

		notify := make(chan int)
		var unsubs []func()
		for i := range s.Events {
			sub := s.Events[i].Subscribe
			if sub == nil {
				continue
			}
			ch, unsub := sub(c)
			if ch == nil {
				if unsub != nil {
					unsub()
				}
				continue
			}
			if unsub != nil {
				unsubs = append(unsubs, unsub)
			}
			go func(i int, ch <-chan struct{}) {
				for {
					select {
					case <-ctx.Done():
						return
					case _, ok := <-ch:
						if !ok {
							return
						}
						select {
						case notify <- i:
						case <-ctx.Done():
							return
						}
					}
				}
			}(i, ch)
		}
		defer func() {
			for _, u := range unsubs {
				u()
			}
		}()

		if s.Replay {
			if flush() {
				return nil
			}
		}

		var timer *time.Timer
		var timerCh <-chan time.Time
		schedule := func() {
			if timer == nil {
				timer = time.NewTimer(tick)
				timerCh = timer.C
				return
			}
			if !timer.Stop() {
				select {
				case <-timerCh:
				default:
				}
			}
			timer.Reset(tick)
		}

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-notify:
				schedule()
			case <-timerCh:
				timer = nil
				timerCh = nil
				if flush() {
					return nil
				}
			}
		}
	}
}

// resolveEvent computes one event's current frames. A set Resolve wins;
// otherwise the static Render path degrades to a constant single frame.
func resolveEvent(c echo.Context, ev *Event) []Fired {
	if ev.Resolve != nil {
		return ev.Resolve(c)
	}
	if ev.Render == nil {
		return nil
	}
	comp, err := ev.Render(c)
	if err != nil || comp == nil {
		return nil
	}
	return []Fired{{Name: ev.Name, Component: comp}}
}

// writeFrame emits one SSE frame. Data is split so every line carries
// its own data: prefix; a payload containing blank lines still parses
// as a single frame.
func writeFrame(w io.Writer, event, data string) error {
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	for _, line := range strings.Split(data, "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", strings.TrimSuffix(line, "\r")); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func renderString(ctx context.Context, t templ.Component) (string, error) {
	var buf bytes.Buffer
	if err := t.Render(ctx, &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}
