// Package demo builds the hxchat sandbox mount: one page route over the
// widget resource (single + double mount), a scripted runner (fail /
// slow / echo by message text), and reseedable seeds. The serve binary
// and the sandbox test share it so the browser rows and the Go gate
// can never drift apart.
package demo

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"

	"github.com/joyboydata/kit/agui"
	"github.com/joyboydata/kit/hxchat"
	v2 "github.com/joyboydata/kit/hxkit/v2"
	sandbox "github.com/joyboydata/kit/sandbox"
)

// MemStore is an in-memory Store with Reset for reseed.
type MemStore struct {
	mu     sync.Mutex
	events map[string][]agui.Event
	subs   map[string]map[chan struct{}]struct{}
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{events: map[string][]agui.Event{}, subs: map[string]map[chan struct{}]struct{}{}}
}

// Reset wipes all threads. The Chat keeps the pointer, so seeds reset
// without rebuilding the mount.
func (s *MemStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = map[string][]agui.Event{}
}

// Append stores the event and signals subscribers non-blockingly.
func (s *MemStore) Append(threadID string, e agui.Event) (agui.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ThreadID = threadID
	e.Seq = int64(len(s.events[threadID])) + 1
	s.events[threadID] = append(s.events[threadID], e)
	for ch := range s.subs[threadID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return e, nil
}

// List returns the thread log in Seq order.
func (s *MemStore) List(threadID string) []agui.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agui.Event(nil), s.events[threadID]...)
}

// Subscribe returns a channel signaled on every Append.
func (s *MemStore) Subscribe(threadID string) (<-chan struct{}, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{}, 8)
	if s.subs[threadID] == nil {
		s.subs[threadID] = map[chan struct{}]struct{}{}
	}
	s.subs[threadID][ch] = struct{}{}
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subs[threadID], ch)
	}
}

// ScriptRunner picks behavior by message text: "fail" fails the run,
// "slow ..." streams three chunks over ~1s, anything else echoes fast.
type ScriptRunner struct{}

// Run executes one scripted turn.
func (ScriptRunner) Run(ctx context.Context, in agui.RunInput, em *agui.Emitter) error {
	mid := "m-" + in.RunID
	if in.Message.Text == "fail" || strings.HasPrefix(in.Message.Text, "fail ") {
		return em.FailRun("model blew up", "E_MODEL")
	}
	if err := em.StartText(mid, "assistant"); err != nil {
		return err
	}
	if strings.HasPrefix(in.Message.Text, "slow") {
		for _, chunk := range []string{"one ", "two ", "three"} {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
			if err := em.WriteText(mid, chunk); err != nil {
				return err
			}
		}
	} else if err := em.WriteText(mid, "echo: "+in.Message.Text); err != nil {
		return err
	}
	if err := em.EndText(mid); err != nil {
		return err
	}
	return em.FinishRun(nil, "stub")
}

// SeedTurn appends one finished user/assistant turn to the thread.
func SeedTurn(s *MemStore, thread, user, assistant string) {
	l := agui.NewLog(thread)
	em := agui.NewEmitter(l)
	_ = em.StartRun(agui.RunInput{ThreadID: thread, RunID: "r-seed", Message: agui.Message{ID: "m-seed-u", Role: "user", Text: user}})
	_ = em.StartText("m-seed-u", "user")
	_ = em.WriteText("m-seed-u", user)
	_ = em.EndText("m-seed-u")
	_ = em.StartText("m-seed-a", "assistant")
	_ = em.WriteText("m-seed-a", assistant)
	_ = em.EndText("m-seed-a")
	_ = em.FinishRun(nil, "stub")
	for _, e := range l.List() {
		_, _ = s.Append(thread, e)
	}
}

// SeedFailed appends history plus a RUN_ERROR terminal.
func SeedFailed(s *MemStore, thread string) {
	SeedTurn(s, thread, "hey", "hello back")
	l := agui.NewLog(thread)
	em := agui.NewEmitter(l)
	_ = em.StartRun(agui.RunInput{ThreadID: thread, RunID: "r-bad", Message: agui.Message{ID: "m-bad", Role: "user", Text: "boom"}})
	_ = em.FailRun("model blew up", "E_MODEL")
	for _, e := range l.List() {
		if e.Type == agui.EventRunStarted || e.Type == agui.EventRunError {
			_, _ = s.Append(thread, e)
		}
	}
}

// Build returns the sandbox mount plus seeds. The page representation
// rides the widget resource, so relative Tag URLs resolve; the asset
// sibling mounts beside it with a nil layout.
func Build(store *MemStore) (sandbox.Mount, map[string]func(ctx context.Context) error) {
	h := hxchat.New(store, ScriptRunner{}, hxchat.Options{Path: "/chat/:thread"})

	page := v2.Representation{Name: "", Handler: func(c echo.Context) (templ.Component, error) {
		tid := c.Param("thread")
		tags := []hxchat.Mount{{ThreadID: tid}}
		if c.QueryParam("two") != "" {
			tags = append(tags, hxchat.Mount{ThreadID: tid, Prefix: tid + "-b"})
		}
		return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			if _, err := io.WriteString(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>hxchat demo</title>`); err != nil {
				return err
			}
			if err := h.Head().Render(ctx, w); err != nil {
				return err
			}
			if _, err := io.WriteString(w, `</head><body>`); err != nil {
				return err
			}
			for _, m := range tags {
				if err := h.Tag(m).Render(ctx, w); err != nil {
					return err
				}
			}
			_, err := io.WriteString(w, `</body></html>`)
			return err
		}), nil
	}}

	widget := h.Resource()[0]
	res := v2.Resource{
		Path:            widget.Path,
		Representations: append([]v2.Representation{page}, widget.Representations...),
		Mutations:       widget.Mutations,
		Streams:         widget.Streams,
	}

	seeds := map[string]func(ctx context.Context) error{
		"empty": func(context.Context) error {
			store.Reset()
			return nil
		},
		"history": func(context.Context) error {
			store.Reset()
			SeedTurn(store, "t1", "hey", "hello back")
			return nil
		},
		"failed": func(context.Context) error {
			store.Reset()
			SeedFailed(store, "t1")
			return nil
		},
	}
	return sandbox.Mount{App: v2.App{Resources: []v2.Resource{res, h.Resource()[1]}}}, seeds
}
