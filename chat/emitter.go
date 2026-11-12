package chat

import (
	"fmt"
	"sync"
	"time"
)

// Emitter is the write side of a Log: it tracks the current open run and
// emits well-formed triads (START → content* → END) so callers cannot. One
// Emitter drives one run at a time; share a Log across Emitters for
// concurrent runs. Safe for concurrent use.
type Emitter struct {
	mu       sync.Mutex
	log      *Log
	threadID string
	runID    string // "" when no run is open
}

// NewEmitter binds an Emitter to a Log.
func NewEmitter(l *Log) *Emitter {
	return &Emitter{log: l}
}

// StartRun opens a run; exactly one run may be open per Emitter. The input
// MessageID/Role (the triggering turn) are recorded on RUN_STARTED.
func (m *Emitter) StartRun(in RunInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runID != "" {
		return fmt.Errorf("chat: run %q already open", m.runID)
	}
	if in.ThreadID == "" || in.RunID == "" {
		return fmt.Errorf("chat: StartRun requires threadId and runId")
	}
	_, err := m.log.Append(Event{
		Type: EventRunStarted, Timestamp: time.Now().UnixMilli(),
		ThreadID: in.ThreadID, RunID: in.RunID, ParentRunID: in.ParentRunID,
		MessageID: in.Message.ID, Role: in.Message.Role, Metadata: in.Metadata,
	})
	if err != nil {
		return err
	}
	m.threadID, m.runID = in.ThreadID, in.RunID
	return nil
}

// FinishRun closes the open run with terminal usage. Usage survives the
// Encode/Decode round-trip on the RUN_FINISHED event.
func (m *Emitter) FinishRun(usage *Usage, model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireRun(); err != nil {
		return err
	}
	if _, err := m.log.Append(Event{
		Type: EventRunFinished, Timestamp: time.Now().UnixMilli(),
		ThreadID: m.threadID, RunID: m.runID, Usage: usage, Model: model,
	}); err != nil {
		return err
	}
	m.runID = ""
	return nil
}

// FailRun closes the open run as failed with a message and optional code.
func (m *Emitter) FailRun(message, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireRun(); err != nil {
		return err
	}
	if message == "" {
		return fmt.Errorf("chat: FailRun requires a message")
	}
	if _, err := m.log.Append(Event{
		Type: EventRunError, Timestamp: time.Now().UnixMilli(),
		ThreadID: m.threadID, RunID: m.runID, Message: message, Code: code,
	}); err != nil {
		return err
	}
	m.runID = ""
	return nil
}

// StartStep opens a named step in the open run; step names are unique per run.
func (m *Emitter) StartStep(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventStepStarted, StepName: name})
}

// EndStep closes a named step.
func (m *Emitter) EndStep(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventStepFinished, StepName: name})
}

// StartText opens an assistant message segment.
func (m *Emitter) StartText(messageID, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventTextMessageStart, MessageID: messageID, Role: role})
}

// WriteText appends one CONTENT delta to an open message segment. (The CHUNK
// spelling covers the same slot via Log.Append for transports that need it.)
func (m *Emitter) WriteText(messageID, delta string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventTextMessageContent, MessageID: messageID, Delta: delta})
}

// EndText seals a message segment.
func (m *Emitter) EndText(messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventTextMessageEnd, MessageID: messageID})
}

// StartTool opens a tool invocation under a parent message; name rides Delta.
func (m *Emitter) StartTool(messageID, toolCallID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventToolCallStart, MessageID: messageID, ToolCallID: toolCallID, Delta: name})
}

// WriteArgs appends one ARGS fragment to an open tool invocation.
func (m *Emitter) WriteArgs(toolCallID, delta string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventToolCallArgs, ToolCallID: toolCallID, Delta: delta})
}

// EndTool seals a tool invocation's args.
func (m *Emitter) EndTool(toolCallID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventToolCallEnd, ToolCallID: toolCallID})
}

// ToolResult records a tool outcome; it may land before or after EndTool.
func (m *Emitter) ToolResult(messageID, toolCallID string, result any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventToolCallResult, MessageID: messageID, ToolCallID: toolCallID, Result: result})
}

// StartReasoning opens a reasoning block plus its message triad for one id.
func (m *Emitter) StartReasoning(messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.emit(Event{Type: EventReasoningStart, MessageID: messageID}); err != nil {
		return err
	}
	return m.emit(Event{Type: EventReasoningMessageStart, MessageID: messageID})
}

// WriteReasoning appends one delta to an open reasoning block.
func (m *Emitter) WriteReasoning(messageID, delta string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventReasoningMessageContent, MessageID: messageID, Delta: delta})
}

// EndReasoning seals the message triad and the block together.
func (m *Emitter) EndReasoning(messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.emit(Event{Type: EventReasoningMessageEnd, MessageID: messageID}); err != nil {
		return err
	}
	return m.emit(Event{Type: EventReasoningEnd, MessageID: messageID})
}

// emit stamps run/thread/time and appends. Caller holds m.mu.
func (m *Emitter) emit(e Event) error {
	if err := m.requireRun(); err != nil {
		return err
	}
	e.ThreadID, e.RunID, e.Timestamp = m.threadID, m.runID, time.Now().UnixMilli()
	_, err := m.log.Append(e)
	return err
}

// requireRun fails writes with no open run. Caller holds m.mu.
func (m *Emitter) requireRun() error {
	if m.runID == "" {
		return fmt.Errorf("chat: no open run (call StartRun first)")
	}
	return nil
}
