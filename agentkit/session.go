package agentkit

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Session represents a single generation run with an event stream.
// T is the concrete type of the expected result.
//
// The session is created by Agent.CreateSession, which spawns an internal
// goroutine that drains the raw channel from AgentRunner, updates status
// fields, and forwards events to the public Events channel.
// The caller only needs to read from Events — no manual Update() calls needed.
type Session[T any] struct {
	// ID is the unique session identifier.
	ID string
	// Events carries the stream of ProgressEvents.
	Events <-chan ProgressEvent[T]
	// CreatedAt is when the session was created.
	CreatedAt time.Time

	mu     sync.RWMutex
	status Status
	result T
	err    string
	logger *slog.Logger
}

// Status returns the current session status.
func (s *Session[T]) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// Result returns the completed output, if any.
// Zero value of T if no result has been produced yet.
func (s *Session[T]) Result() T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.result
}

// Error returns the error message, if any.
func (s *Session[T]) Error() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

// newSession creates a Session and spawns an internal goroutine that drains
// the raw channel from the runner, updates session state, and forwards events
// to the public Events channel. The public channel is closed when the raw
// channel is exhausted.
//
// onResult, if non-nil, is called when a result is received (before forwarding
// to the public channel). The agent uses this to track LastResult.
func newSession[T any](id string, raw <-chan ProgressEvent[T], logger *slog.Logger, onResult func(T)) *Session[T] {
	// Buffered to avoid blocking the drain goroutine on slow readers.
	// 512 is generous — the opportunity generator can emit many rapid events.
	forward := make(chan ProgressEvent[T], 512)

	sess := &Session[T]{
		ID:        id,
		Events:    forward,
		status:    StatusGenerating,
		CreatedAt: time.Now(),
		logger:    logger,
	}

	go sess.drain(raw, forward, onResult)
	return sess
}

// drain reads from the raw channel, updates session state, and forwards
// events to the public channel. Runs in a dedicated goroutine.
//
// Consecutive EventThinking events are batched and flushed either on a
// 100ms tick or when a non-thinking event arrives. This prevents the
// forward channel from filling up with many tiny thinking deltas.
func (s *Session[T]) drain(raw <-chan ProgressEvent[T], forward chan<- ProgressEvent[T], onResult func(T)) {
	defer close(forward)

	var thinkingBuf []string
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	drainDone := make(chan struct{})
	go func() {
		for evt := range raw {
			s.apply(evt, onResult)

			if evt.Type == EventThinking {
				thinkingBuf = append(thinkingBuf, evt.Thinking)
				continue
			}

			// Non-thinking event: flush pending thinking first, then forward.
			s.flushThinking(forward, &thinkingBuf)

			select {
			case forward <- evt:
			default:
				s.logger.Warn("session event dropped (channel full)",
					slog.String("session_id", s.ID),
					slog.String("event_type", string(evt.Type)),
				)
			}

			if evt.Type == EventResult || evt.Type == EventError {
				close(drainDone)
				return
			}
		}

		// Raw channel closed without a terminal event — mark as error.
		s.mu.Lock()
		s.status = StatusError
		s.err = "agent exited without producing output"
		s.mu.Unlock()
		close(drainDone)
	}()

	// Tick-driven flush for batching thinking events.
	for {
		select {
		case <-drainDone:
			s.flushThinking(forward, &thinkingBuf)
			return
		case <-ticker.C:
			s.flushThinking(forward, &thinkingBuf)
		}
	}
}

// flushThinking sends accumulated thinking text as a single EventThinking.
// If the forward channel is full, the text is kept in the buffer for retry.
func (s *Session[T]) flushThinking(forward chan<- ProgressEvent[T], buf *[]string) {
	if len(*buf) == 0 {
		return
	}
	merged := strings.Join(*buf, "")
	*buf = nil
	select {
	case forward <- ProgressEvent[T]{Type: EventThinking, Thinking: merged}:
	default:
		// Still full — put text back for next attempt.
		*buf = []string{merged}
	}
}

// apply updates session state from a progress event.
// Must only be called from the drain goroutine.
func (s *Session[T]) apply(evt ProgressEvent[T], onResult func(T)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch evt.Type {
	case EventResult:
		s.status = StatusDone
		s.result = evt.Result
		if onResult != nil {
			onResult(evt.Result)
		}
	case EventError:
		s.status = StatusError
		s.err = evt.Error
	}
}
