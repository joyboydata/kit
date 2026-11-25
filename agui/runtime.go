package agui

import (
	"context"
	"errors"
	"sync"
)

// errNoBackend is the contract stub speaking: kit ships the shape, the
// backend (persistence, execution) lives outside kit, so every I/O method
// fails until one is wired in.
var errNoBackend = errors.New("agui: no runtime backend (kit ships the contract; implementation lives outside kit)")

// Runtime is the app-facing handle for the thread lifecycle. Contract only:
// no store, no runner, no policy. Apps compile against kit alone; the
// implementation arrives from outside kit.
type Runtime struct{}

// NewRuntime returns the contract handle. No backend is attached here.
func NewRuntime() *Runtime {
	return &Runtime{}
}

// CreateThread opens one stable conversation. Fails until a backend exists.
func (r *Runtime) CreateThread(ctx context.Context) (Thread, error) {
	return Thread{}, errNoBackend
}

// OpenThread resolves the handle for a known conversation. Pure resolution,
// no I/O, hence no ctx; the I/O methods on Thread carry one.
func (r *Runtime) OpenThread(threadID string) (Thread, error) {
	return Thread{}, errNoBackend
}

// Resume recovers interrupted threads after a restart. Fails until a backend
// exists; the backend returns exactly the threads needing continuation.
func (r *Runtime) Resume(ctx context.Context) ([]Thread, error) {
	return nil, errNoBackend
}

// ListActiveRuns reports the live runs for one owner. Fails until a backend
// exists; the backend defines how owners partition across restarts.
func (r *Runtime) ListActiveRuns(ctx context.Context, owner string) ([]Event, error) {
	return nil, errNoBackend
}

// Thread is one stable conversation. The id is opaque; ID exposes it for
// logging and wiring, never for persistence (that is the backend's job).
type Thread struct {
	id string
}

// ID returns the conversation identifier.
func (t Thread) ID() string {
	return t.id
}

// SendInput opens one execution of this thread. Fails until a backend
// exists; the backend mints the run and returns its run ID.
func (t Thread) SendInput(ctx context.Context, in RunInput) (string, error) {
	return "", errNoBackend
}

// Subscribe streams this thread's events from fromSeq. Binds to ctx:
// request-scoped, never Background. The channel always closes on
// termination; the caller owns cancel (returned func or ctx). The contract
// stub has no events to deliver, so termination is all it can prove.
func (t Thread) Subscribe(ctx context.Context, fromSeq int64) (<-chan []Event, func()) {
	ch := make(chan []Event)
	var once sync.Once
	finish := func() {
		once.Do(func() { close(ch) })
	}
	go func() {
		<-ctx.Done()
		finish()
	}()
	return ch, finish
}
