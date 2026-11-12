package chat

import (
	"fmt"
	"reflect"
	"sync"
	"time"
)

// toolState tracks one invocation: which parent message it belongs to and
// whether args fragments are still accepted (between START and END).
type toolState struct {
	messageID string
	argsOpen  bool
}

// runState tracks one run's open segments so Append can enforce triad
// discipline and single-terminal lifecycle.
type runState struct {
	open       bool // started, no terminal yet
	texts      map[string]bool
	tools      map[string]*toolState
	reasoning  map[string]bool // block open per messageId
	reasonMsgs map[string]bool // message triad open per messageId
	steps      map[string]bool
}

// Log is an append-only, Seq-ordered event log for one thread. Append assigns
// Seq (1-based, contiguous), dedupes exact duplicates, and enforces triad +
// lifecycle rules. Safe for concurrent use.
type Log struct {
	mu       sync.Mutex
	threadID string
	events   []Event
	nextSeq  int64 // next Seq to assign; starts at 1
	runs     map[string]*runState
}

// NewLog returns an empty Log for the given thread.
func NewLog(threadID string) *Log {
	return &Log{threadID: threadID, nextSeq: 1, runs: map[string]*runState{}}
}

// Append validates, dedupes, and stores one event, returning the stored copy
// with Seq assigned. An exact duplicate (all fields equal modulo Seq) returns
// the original Seq without consuming a new one. Incoming Seq is ignored;
// empty ThreadID is filled from the Log; zero Timestamp is filled with now.
func (l *Log) Append(e Event) (Event, error) {
	if err := e.Validate(); err != nil {
		return Event{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	e.Seq = 0
	if e.ThreadID == "" {
		e.ThreadID = l.threadID
	}
	if e.ThreadID != l.threadID {
		return Event{}, fmt.Errorf("chat: thread mismatch: event %q, log %q", e.ThreadID, l.threadID)
	}
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}
	for _, st := range l.events {
		dup := st
		dup.Seq = 0
		if reflect.DeepEqual(dup, e) {
			return st, nil // idempotent: same content, existing Seq, no gap
		}
	}
	if err := l.check(e); err != nil {
		return Event{}, err
	}
	e.Seq = l.nextSeq
	l.nextSeq++
	l.events = append(l.events, e)
	return e, nil
}

// List returns a copy of stored events in Seq order.
func (l *Log) List() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.events...)
}

// Fold derives the message list from the stored events.
func (l *Log) Fold() []Message {
	return Fold(l.List())
}

// check enforces run lifecycle and triad discipline. Caller holds l.mu.
func (l *Log) check(e Event) error {
	if e.Type == EventRunStarted {
		if _, dup := l.runs[e.RunID]; dup {
			return fmt.Errorf("chat: run %q already started", e.RunID)
		}
		l.runs[e.RunID] = &runState{
			open: true, texts: map[string]bool{},
			tools: map[string]*toolState{}, reasoning: map[string]bool{},
			reasonMsgs: map[string]bool{}, steps: map[string]bool{},
		}
		return nil
	}
	st, ok := l.runs[e.RunID]
	if !ok || !st.open {
		return fmt.Errorf("chat: %s for run %q without open run", e.Type, e.RunID)
	}
	switch e.Type {
	case EventRunFinished, EventRunError:
		st.open = false // single terminal: second one finds !open above
		return nil
	case EventStepStarted:
		if st.steps[e.StepName] {
			return fmt.Errorf("chat: step %q already started in run %q", e.StepName, e.RunID)
		}
		st.steps[e.StepName] = true
		return nil
	case EventStepFinished:
		if !st.steps[e.StepName] {
			return fmt.Errorf("chat: step %q not open in run %q", e.StepName, e.RunID)
		}
		st.steps[e.StepName] = false
		return nil
	case EventTextMessageStart:
		if _, dup := st.texts[e.MessageID]; dup {
			return fmt.Errorf("chat: text message %q already started in run %q", e.MessageID, e.RunID)
		}
		st.texts[e.MessageID] = true
		return nil
	case EventTextMessageContent, EventTextMessageChunk:
		if !st.texts[e.MessageID] {
			return fmt.Errorf("chat: %s for message %q before START or after END", e.Type, e.MessageID)
		}
		return nil
	case EventTextMessageEnd:
		if !st.texts[e.MessageID] {
			return fmt.Errorf("chat: TEXT_MESSAGE_END for message %q without open segment", e.MessageID)
		}
		st.texts[e.MessageID] = false
		return nil
	case EventToolCallStart:
		if _, dup := st.tools[e.ToolCallID]; dup {
			return fmt.Errorf("chat: tool call %q already started in run %q", e.ToolCallID, e.RunID)
		}
		st.tools[e.ToolCallID] = &toolState{messageID: e.MessageID, argsOpen: true}
		return nil
	case EventToolCallArgs, EventToolCallChunk:
		t, ok := st.tools[e.ToolCallID]
		if !ok || !t.argsOpen {
			return fmt.Errorf("chat: %s for tool call %q before START or after END", e.Type, e.ToolCallID)
		}
		return nil
	case EventToolCallEnd:
		t, ok := st.tools[e.ToolCallID]
		if !ok || !t.argsOpen {
			return fmt.Errorf("chat: TOOL_CALL_END for tool call %q without open segment", e.ToolCallID)
		}
		t.argsOpen = false
		return nil
	case EventToolCallResult:
		if _, ok := st.tools[e.ToolCallID]; !ok {
			return fmt.Errorf("chat: TOOL_CALL_RESULT for tool call %q before START", e.ToolCallID)
		}
		return nil // RESULT may land before or after END
	case EventReasoningStart:
		if st.reasoning[e.MessageID] {
			return fmt.Errorf("chat: reasoning block %q already started in run %q", e.MessageID, e.RunID)
		}
		st.reasoning[e.MessageID] = true
		return nil
	case EventReasoningEnd:
		if !st.reasoning[e.MessageID] {
			return fmt.Errorf("chat: REASONING_END for message %q without open block", e.MessageID)
		}
		if st.reasonMsgs[e.MessageID] {
			return fmt.Errorf("chat: REASONING_END for message %q with open reasoning message", e.MessageID)
		}
		st.reasoning[e.MessageID] = false
		return nil
	case EventReasoningMessageStart:
		if !st.reasoning[e.MessageID] {
			return fmt.Errorf("chat: REASONING_MESSAGE_START for message %q without open block", e.MessageID)
		}
		if st.reasonMsgs[e.MessageID] {
			return fmt.Errorf("chat: reasoning message %q already started in run %q", e.MessageID, e.RunID)
		}
		st.reasonMsgs[e.MessageID] = true
		return nil
	case EventReasoningMessageContent, EventReasoningMessageChunk:
		if !st.reasonMsgs[e.MessageID] {
			return fmt.Errorf("chat: %s for message %q before START or after END", e.Type, e.MessageID)
		}
		return nil
	case EventReasoningMessageEnd:
		if !st.reasonMsgs[e.MessageID] {
			return fmt.Errorf("chat: REASONING_MESSAGE_END for message %q without open segment", e.MessageID)
		}
		st.reasonMsgs[e.MessageID] = false
		return nil
	}
	return nil
}
