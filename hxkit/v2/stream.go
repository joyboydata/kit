package v2

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

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
// binding the pushed frame to the element it swaps.
type Event struct {
	Name      string
	Subscribe Watch
	Render    Handler
}

// Stream is an SSE endpoint owned by a resource. Replay pushes current
// state on connect so late joiners converge without waiting for the
// next change.
type Stream struct {
	Path   string
	Replay bool
	Events []Event
}

// serveStream owns replay-on-connect, framing, flush, and dedup.
// Identical re-renders are skipped per event; render errors skip the
// frame and keep the connection alive. The loop ends when the client
// goes away.
func (a App) serveStream(s Stream) echo.HandlerFunc {
	return func(c echo.Context) error {
		if len(s.Events) == 0 {
			return echo.NewHTTPError(http.StatusNotFound, "stream has no events")
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

		last := make([]string, len(s.Events))
		send := func(i int) {
			comp, err := s.Events[i].Render(c)
			if err != nil || comp == nil {
				return
			}
			html, err := renderString(ctx, comp)
			if err != nil || html == last[i] {
				return
			}
			last[i] = html
			if err := writeFrame(res.Writer, s.Events[i].Name, html); err != nil {
				return
			}
			fl.Flush()
		}

		if s.Replay {
			for i := range s.Events {
				send(i)
			}
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

		for {
			select {
			case <-ctx.Done():
				return nil
			case i := <-notify:
				send(i)
			}
		}
	}
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
