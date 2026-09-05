package agentkit

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Agent creates and tracks generation sessions.
// T is the expected output type for all sessions created by this agent.
//
// Usage:
//
//	agent := agentkit.NewAgent[Output](runner, systemPrompt, logger)
//	sess := agent.CreateSession(ctx, "analyze the data...")
//	for evt := range sess.Events {
//	    // SSE write or progress update
//	}
type Agent[T any] struct {
	runner       AgentRunner[T]
	systemPrompt string

	mu         sync.RWMutex
	sessions   map[string]*Session[T]
	lastResult T
	logger     *slog.Logger
}

// NewAgent creates a new Agent with the given runner and system prompt.
// The systemPrompt is passed natively to the runner and delivered to the AI
// model as a system-level instruction for all sessions created by this agent.
// The logger is used for session lifecycle events (start, errors, dropped events)
// and is passed through to the runner's logger via With("sub", ...).
func NewAgent[T any](runner AgentRunner[T], systemPrompt string, logger *slog.Logger) *Agent[T] {
	return &Agent[T]{
		runner:       runner,
		systemPrompt: systemPrompt,
		sessions:     make(map[string]*Session[T]),
		logger:       logger,
	}
}

// CreateSession begins a new generation session with the given prompt.
// The prompt should contain the domain instructions (what to do, what context
// to use). The agent appends the JSON output schema derived from T before
// passing it to the runner.
//
// The returned Session is self-updating — the caller only needs to drain
// sess.Events. Session status/result/error are updated internally.
//
// The context is passed through to AgentRunner.Run. If cancelled or expired,
// the runner should abort and close its channel. Note that the session's
// drain goroutine does not select on ctx.Done() — it waits for the raw
// channel to close. A cancelled context will still terminate the underlying
// agent process (via exec.CommandContext for the JSON runner), but the
// goroutine may briefly outlive the caller. Callers should drain Events
// until close or use a separate goroutine.
func (a *Agent[T]) CreateSession(ctx context.Context, prompt string) (*Session[T], error) {
	// Inject the output format derived from T.
	prompt = injectOutputFormat[T](prompt)

	raw, err := a.runner.Run(ctx, prompt, a.systemPrompt)
	if err != nil {
		return nil, fmt.Errorf("run agent: %w", err)
	}

	id := generateSessionID()
	sess := newSession(id, raw, a.logger, a.setResult)

	a.mu.Lock()
	a.sessions[id] = sess
	a.mu.Unlock()

	a.logger.Info("generation session started",
		slog.String("session_id", id),
	)
	return sess, nil
}

// Session retrieves a session by ID. Returns nil, false if not found.
func (a *Agent[T]) Session(id string) (*Session[T], bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	sess, ok := a.sessions[id]
	return sess, ok
}

// LastResult returns the most recently completed output, if any.
// Useful for state recovery after a page reload or reconnection.
// Returns the zero value of T if no session has completed yet.
func (a *Agent[T]) LastResult() T {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastResult
}

// setResult is called by a session's drain goroutine when a result arrives.
func (a *Agent[T]) setResult(result T) {
	a.mu.Lock()
	a.lastResult = result
	a.mu.Unlock()
}

func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}
