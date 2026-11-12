package hxchat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"

	"github.com/joyboydata/kit/chat"
	v2 "github.com/joyboydata/kit/hxkit/v2"
)

// Store is the persistence seam. Implementations keep one event log per
// thread and signal every Subscribe channel on each Append, so streams
// wake without polling. Append assigns ordering; List returns Seq order.
type Store interface {
	Append(threadID string, e chat.Event) (chat.Event, error)
	List(threadID string) []chat.Event
	Subscribe(threadID string) (<-chan struct{}, func())
}

// Runner is the execution seam. It streams one run through em and closes
// it with FinishRun or FailRun; a returned error is backstopped into
// FailRun so every run ends terminal. ctx is detached from the POST.
type Runner interface {
	Run(ctx context.Context, in chat.RunInput, em *chat.Emitter) error
}

// RunState is the folded lifecycle of a thread: the latest run is open
// ("running"), the latest terminal was a failure ("error", RunID names
// the failed run for retry), or nothing is in flight ("idle").
type RunState struct {
	Phase string
	RunID string
}

// Phases of RunState.
const (
	PhaseIdle    = "idle"
	PhaseRunning = "running"
	PhaseError   = "error"
)

// Mount names one widget placement. Prefix defaults to the sanitized
// ThreadID; set it when two mounts share a thread on one page.
type Mount struct {
	ThreadID string
	Prefix   string
	Theme    string
}

// Options wires Chat to one resource. Path is the resource URL, params
// included ("/chat/:thread", "/apps/:id/revisions/:rid"). Tag must be
// rendered on a page served from Path: src/stream/post are relative so
// they resolve wherever Path is mounted. Render* slots default to the
// Default*View renderers; set one to decorate, never to reimplement.
type Options struct {
	Path     string
	Tick     time.Duration
	ThreadID func(c echo.Context) string
	Prefix   func(c echo.Context) string
	Theme    string

	RenderThread   func(m Mount, msgs []chat.Message) templ.Component
	RenderMessage  func(m Mount, msg chat.Message) templ.Component
	RenderComposer func(m Mount, locked bool) templ.Component
	RenderStatus   func(m Mount, s RunState) templ.Component
	RenderError    func(m Mount, err error, runID string) templ.Component
}

// Chat is one widget instance: one Store, one Runner, one Options.
type Chat struct {
	store  Store
	runner Runner
	opts   Options
}

// New binds the seams. It panics on a nil store or runner: a widget
// without persistence or execution is a wiring bug, not a runtime one.
// An empty Path defaults to "/chat/:thread".
func New(store Store, runner Runner, opts Options) *Chat {
	if store == nil {
		panic("hxchat: New requires a non-nil Store")
	}
	if runner == nil {
		panic("hxchat: New requires a non-nil Runner")
	}
	if opts.Path == "" {
		opts.Path = "/chat/:thread"
	}
	if opts.RenderThread == nil {
		opts.RenderThread = DefaultThreadView
	}
	if opts.RenderMessage == nil {
		opts.RenderMessage = DefaultMessageView
	}
	if opts.RenderComposer == nil {
		opts.RenderComposer = DefaultComposerView
	}
	if opts.RenderStatus == nil {
		opts.RenderStatus = DefaultStatusView
	}
	if opts.RenderError == nil {
		opts.RenderError = DefaultErrorView
	}
	return &Chat{store: store, runner: runner, opts: opts}
}

// Resource returns the widget resource (thread fragment, messages
// mutation returning Hold, Replay/Tick/dedup/terminal stream) plus the
// asset sibling serving element.js and element.css. Register both on an
// App; the asset resource needs a nil Layout (it serves bytes, and
// layouts wrap direct visits in HTML).
func (c *Chat) Resource() []v2.Resource {
	return []v2.Resource{
		{
			Path: c.opts.Path,
			Representations: []v2.Representation{
				{Name: "thread", EmbedOnly: true, Handler: c.serveThread},
			},
			Mutations: []v2.Mutation{
				{Path: "messages", Handler: c.serveMessages},
			},
			Streams: []v2.Stream{
				{
					Path:   "stream",
					Replay: true,
					Tick:   c.opts.Tick,
					Events: []v2.Event{
						{
							Name:      "thread",
							Subscribe: c.watch,
							Resolve:   c.resolve,
						},
					},
					Terminal: c.terminal,
				},
			},
		},
		{
			Path: assetPath,
			Representations: []v2.Representation{
				{Name: "", Handler: serveAsset},
			},
		},
	}
}

// threadID resolves the thread for a request: the option, else the
// :thread param, else :id, else ?thread=.
func (c *Chat) threadID(ctx echo.Context) string {
	if c.opts.ThreadID != nil {
		return c.opts.ThreadID(ctx)
	}
	if v := ctx.Param("thread"); v != "" {
		return v
	}
	if v := ctx.Param("id"); v != "" {
		return v
	}
	return ctx.QueryParam("thread")
}

// prefix resolves the region prefix: the connection's ?prefix= (the
// element reports the prefix attribute Tag rendered, so each mount's
// frames match its own markup), else the option, else the sanitized
// thread ID — the same default Tag applies.
func (c *Chat) prefix(ctx echo.Context, threadID string) string {
	if v := ctx.QueryParam("prefix"); v != "" {
		return v
	}
	if c.opts.Prefix != nil {
		if v := c.opts.Prefix(ctx); v != "" {
			return v
		}
	}
	return SanitizePrefix(threadID)
}

// SanitizePrefix maps a thread ID to a region prefix: alphanumerics,
// dash, and underscore survive; anything else becomes a dash. Empty
// input falls back to "chat" so region IDs are never bare suffixes.
func SanitizePrefix(threadID string) string {
	if threadID == "" {
		return "chat"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, threadID)
}

// mountFor builds the Mount for a request thread.
func (c *Chat) mountFor(ctx echo.Context, threadID string) Mount {
	return Mount{ThreadID: threadID, Prefix: c.prefix(ctx, threadID), Theme: c.opts.Theme}
}

// newID mints a run or message ID. Callers supply all chat IDs, so
// retries can reuse them and Append can dedupe exact duplicates.
func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("hxchat: rand: " + err.Error())
	}
	return prefix + hex.EncodeToString(b[:])
}

// serveThread renders the ?view=thread fragment: the four regions with
// the lock derived from the current log. EmbedOnly keeps direct visits
// a 404; the page embeds this HTML inside Tag instead.
func (c *Chat) serveThread(ctx echo.Context) (templ.Component, error) {
	threadID := c.threadID(ctx)
	m := c.mountFor(ctx, threadID)
	events := c.store.List(threadID)
	locked, st, rerr := foldRun(events)
	return c.renderThread(m, chat.Fold(events), locked, st, rerr), nil
}

// serveMessages appends the user turn, opens the run synchronously so
// the stream sees it before Hold returns, then executes async. It
// returns Hold: the stream pushes truth, the POST carries none.
func (c *Chat) serveMessages(ctx echo.Context) (templ.Component, error) {
	threadID := c.threadID(ctx)
	text := strings.TrimSpace(ctx.FormValue("text"))
	if text == "" {
		return nil, v2.E(400, "text is required")
	}

	runLog := chat.NewLog(threadID)
	em := chat.NewEmitter(runLog)
	msgID, runID := newID("m"), newID("r")
	in := chat.RunInput{
		ThreadID:    threadID,
		RunID:       runID,
		ParentRunID: ctx.FormValue("parent_run"),
		Message:     chat.Message{ID: msgID, Role: "user", Text: text},
	}
	if err := em.StartRun(in); err != nil {
		return nil, v2.E(500, "could not open run")
	}
	if err := em.StartText(msgID, "user"); err != nil {
		return nil, v2.E(500, "could not open run")
	}
	if err := em.WriteText(msgID, text); err != nil {
		return nil, v2.E(500, "could not open run")
	}
	if err := em.EndText(msgID); err != nil {
		return nil, v2.E(500, "could not open run")
	}

	// Drain synchronously: the run is open in the store before Hold
	// returns, so a stream connecting on the response sees the lock.
	sent := c.drain(threadID, runLog, 0)
	go c.execute(context.WithoutCancel(ctx.Request().Context()), threadID, in, em, runLog, sent)
	return v2.Hold(), nil
}

// execute runs the runner while forwarding its log into the store, then
// drains. A returned error becomes FailRun when the runner left the run
// open, so every run ends terminal and the stream closes.
func (c *Chat) execute(ctx context.Context, threadID string, in chat.RunInput, em *chat.Emitter, runLog *chat.Log, sent int) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.forward(threadID, runLog, done, sent)
	}()
	err := c.runner.Run(ctx, in, em)
	if err != nil {
		_ = em.FailRun(err.Error(), "E_RUNNER")
	}
	close(done)
	wg.Wait()
}

// forward polls a live run log into the store until done closes, then
// drains. Polling (not hooks): Emitter owns its appends, so the only
// total observer is a diff over List. Store dedupes exact duplicates,
// and Seq tracking keeps each event forwarded exactly once.
// drain forwards every event the run log holds past sent, returning the
// new offset. Store dedupes exact duplicates; offsets keep each event
// forwarded exactly once per log.
func (c *Chat) drain(threadID string, runLog *chat.Log, sent int) int {
	evs := runLog.List()
	if sent > len(evs) {
		sent = len(evs)
	}
	for _, e := range evs[sent:] {
		_, _ = c.store.Append(threadID, e)
		sent++
	}
	return sent
}

func (c *Chat) forward(threadID string, runLog *chat.Log, done <-chan struct{}, sent int) {
	flush := func() {
		sent = c.drain(threadID, runLog, sent)
	}
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-done:
			flush()
			return
		case <-t.C:
			flush()
		}
	}
}

// watch subscribes the stream to the thread. Store.Append signals on
// every write (including forwarded runner events), so one subscription
// wakes both user turns and streamed chunks.
func (c *Chat) watch(ctx echo.Context) (<-chan struct{}, func()) {
	return c.store.Subscribe(c.threadID(ctx))
}

// resolve folds the thread into one frame per region. Frame names carry
// the connection ns (?hxchatns=, minted per element instance), so two
// mounts on one page swap only their own regions.
func (c *Chat) resolve(ctx echo.Context) []v2.Fired {
	threadID := c.threadID(ctx)
	m := c.mountFor(ctx, threadID)
	ns := frameNS(m.Prefix, ctx.QueryParam("hxchatns"))
	events := c.store.List(threadID)
	msgs := chat.Fold(events)
	locked, st, rerr := foldRun(events)
	var errComp templ.Component
	if st.Phase == PhaseError && rerr != nil {
		errComp = c.opts.RenderError(m, rerr.err, rerr.runID)
	}
	return []v2.Fired{
		{Name: ns.messages(), Component: messagesRegion(ns.messages(), m, msgs, c.opts.RenderMessage)},
		{Name: ns.composer(), Component: composerRegion(ns.composer(), m, locked, c.opts.RenderComposer)},
		{Name: ns.status(), Component: statusRegion(ns.status(), m, st, c.opts.RenderStatus)},
		{Name: ns.error(), Component: errorRegion(ns.error(), m, errComp)},
	}
}

// terminal closes the stream once the thread's latest event is a run
// terminal: after the final frames of a finished run. Streams are
// otherwise long-lived, so passive mounts converge without sending.
// A level (no open run) would close every quiet connection and force
// a reconnect loop; the edge (latest event terminal) closes exactly
// once per run. The redirect frame carries no URL; the element treats
// close with an unlocked composer as rest, with a locked one as a
// drop to redial.
func (c *Chat) terminal(ctx echo.Context) (string, bool) {
	events := c.store.List(c.threadID(ctx))
	if len(events) == 0 {
		return "", false
	}
	last := events[len(events)-1].Type
	return "", last == chat.EventRunFinished || last == chat.EventRunError
}

// runFailure carries a RUN_ERROR terminal for the error region.
type runFailure struct {
	err   error
	runID string
}

// foldRun derives the lock, status, and error from the event log: any
// run without a terminal keeps the composer locked; otherwise the
// latest terminal decides idle vs error.
func foldRun(events []chat.Event) (locked bool, st RunState, rerr *runFailure) {
	open := map[string]bool{}
	var lastType chat.EventType
	var last chat.Event
	for _, e := range events {
		switch e.Type {
		case chat.EventRunStarted:
			open[e.RunID] = true
		case chat.EventRunFinished, chat.EventRunError:
			delete(open, e.RunID)
			lastType, last = e.Type, e
		}
	}
	if len(open) > 0 {
		for id := range open {
			return true, RunState{Phase: PhaseRunning, RunID: id}, nil
		}
	}
	if lastType == chat.EventRunError {
		msg := last.Message
		if msg == "" {
			msg = "the run failed"
		}
		if last.Code != "" {
			msg = fmt.Sprintf("%s (%s)", msg, last.Code)
		}
		return false, RunState{Phase: PhaseError, RunID: last.RunID},
			&runFailure{err: errors.New(msg), runID: last.RunID}
	}
	return false, RunState{Phase: PhaseIdle}, nil
}

// frameNS prefixes region names per connection. An empty ns (plain
// EventSource without the element) falls back to bare prefix names.
func frameNS(prefix, ns string) regionNames {
	if ns == "" {
		return regionNames{base: prefix}
	}
	return regionNames{base: prefix + "-" + ns}
}

// regionNames renders the four frame/element IDs for one scope.
type regionNames struct{ base string }

func (n regionNames) messages() string { return n.base + "-messages" }
func (n regionNames) composer() string { return n.base + "-composer" }
func (n regionNames) status() string   { return n.base + "-status" }
func (n regionNames) error() string    { return n.base + "-error" }
