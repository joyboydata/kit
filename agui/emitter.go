package agui

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
		return fmt.Errorf("agui: run %q already open", m.runID)
	}
	if in.ThreadID == "" || in.RunID == "" {
		return fmt.Errorf("agui: StartRun requires threadId and runId")
	}
	_, err := m.log.Append(Event{
		Type: EventRunStarted, Timestamp: time.Now().UnixMilli(),
		ThreadID: in.ThreadID, RunID: in.RunID, ParentRunID: in.ParentRunID,
		MessageID: in.Message.ID, Role: in.Message.Role, Metadata: in.Metadata,
		State: in.State, ProtocolVersion: in.ProtocolVersion,
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
	return m.FinishRunEx(RunEnd{Usage: usage, Model: model})
}

// FinishRunEx closes the open run with the full terminal shape: absent
// Outcome reads as success; interrupt waits on outside input.
func (m *Emitter) FinishRunEx(end RunEnd) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireRun(); err != nil {
		return err
	}
	if _, err := m.log.Append(Event{
		Type: EventRunFinished, Timestamp: time.Now().UnixMilli(),
		ThreadID: m.threadID, RunID: m.runID,
		Outcome: end.Outcome, Result: end.Result,
		Usage: end.Usage, Usages: end.Usages, Model: end.Model,
		Interrupts: end.Interrupts, PendingToolCallIDs: end.PendingToolCallIDs,
	}); err != nil {
		return err
	}
	m.runID = ""
	return nil
}

// InterruptRun closes the run waiting on outside input; the next run's
// resume must cover every interrupt entry (see RunInput.ValidateResume).
func (m *Emitter) InterruptRun(interrupts []Interrupt) error {
	return m.FinishRunEx(RunEnd{Outcome: OutcomeInterrupt, Interrupts: interrupts})
}

// CancelRun closes the run stopped on purpose: not failed, not waiting.
func (m *Emitter) CancelRun() error {
	return m.FinishRunEx(RunEnd{Outcome: OutcomeCancelled})
}

// FailRun closes the open run as failed with a message and optional code.
func (m *Emitter) FailRun(message, code string) error {
	return m.FailRunEx(message, code, nil, nil)
}

// FailRunEx fails the run carrying terminal usage (RUN_ERROR owns usage too).
func (m *Emitter) FailRunEx(message, code string, usage *Usage, usages []ProviderUsage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireRun(); err != nil {
		return err
	}
	if message == "" {
		return fmt.Errorf("agui: FailRun requires a message")
	}
	if _, err := m.log.Append(Event{
		Type: EventRunError, Timestamp: time.Now().UnixMilli(),
		ThreadID: m.threadID, RunID: m.runID, Message: message, Code: code,
		Usage: usage, Usages: usages,
	}); err != nil {
		return err
	}
	m.runID = ""
	return nil
}

// SnapshotState replaces the run state wholesale; later deltas build on it.
func (m *Emitter) SnapshotState(snapshot any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventStateSnapshot, Snapshot: snapshot})
}

// PatchState amends the run state with an RFC 6902 patch, applied atomically.
func (m *Emitter) PatchState(patch any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventStateDelta, Patch: patch})
}

// SnapshotMessages declares the producer's full message set; Fold reconciles
// it in place by id (activity/reasoning the consumer alone holds survive).
func (m *Emitter) SnapshotMessages(msgs []Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventMessagesSnapshot, Messages: msgs})
}

// SnapshotActivity creates or replaces one activity message; replace=false
// keeps an existing one instead of overwriting it.
func (m *Emitter) SnapshotActivity(messageID, activityType string, content any, replace ...bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := Event{Type: EventActivitySnapshot, MessageID: messageID, ActivityType: activityType, Content: content}
	if len(replace) > 0 {
		r := replace[0]
		e.Replace = &r
	}
	return m.emit(e)
}

// PatchActivity amends one activity's content with an RFC 6902 patch.
func (m *Emitter) PatchActivity(messageID, activityType string, patch any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventActivityDelta, MessageID: messageID, ActivityType: activityType, Patch: patch})
}

// StartSubagent announces one invocation; the id names the invocation, not
// the agent, and never repeats within the run.
func (m *Emitter) StartSubagent(subagentRunID string) error {
	return m.StartSubagentNested(subagentRunID, "")
}

// StartSubagentNested announces a nested invocation under its parent.
func (m *Emitter) StartSubagentNested(subagentRunID, parentSubagentRunID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventSubagentStarted, SubagentRunID: subagentRunID, ParentSubagentRun: parentSubagentRunID})
}

// FinishSubagent closes an invocation; outcome is success or suspended (the
// latter may continue in a later run under the same id).
func (m *Emitter) FinishSubagent(subagentRunID, outcome string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventSubagentFinished, SubagentRunID: subagentRunID, Outcome: outcome})
}

// FailSubagent ends one invocation as failed; the parent run may continue.
func (m *Emitter) FailSubagent(subagentRunID, message, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventSubagentError, SubagentRunID: subagentRunID, Message: message, Code: code})
}

// EmitRaw passes a provider-native event through untranslated; consumers
// derive no protocol behavior from it.
func (m *Emitter) EmitRaw(event any, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventRaw, RawEvent: event, RawSource: source})
}

// EmitCustom records an application extension event; unknown names are
// ignored by consumers that do not recognise them, never stripped.
func (m *Emitter) EmitCustom(name string, value any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventCustom, CustomName: name, CustomValue: value})
}

// StoreEncryptedValue keeps an opaque provider artefact against its message
// or tool call; consumers return it unmodified in the next run's input.
func (m *Emitter) StoreEncryptedValue(subtype, entityID, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventReasoningEncryptedValue, Subtype: subtype, EntityID: entityID, EncryptedValue: value})
}

// ForSubagent binds an emitter to one announced invocation; every event it
// writes carries that attribution, so ownership checks hold by construction.
func (m *Emitter) ForSubagent(subagentRunID string) *SubEmitter {
	return &SubEmitter{parent: m, sub: subagentRunID}
}

// SubEmitter is one invocation's write handle; see Emitter.ForSubagent.
type SubEmitter struct {
	parent *Emitter
	sub    string
}

// SubagentRunID reports the invocation this handle attributes to.
func (s *SubEmitter) SubagentRunID() string { return s.sub }

func (s *SubEmitter) StartText(messageID, role string) error {
	return s.parent.emitAs(s.sub, Event{Type: EventTextMessageStart, MessageID: messageID, Role: role})
}

func (s *SubEmitter) WriteText(messageID, delta string) error {
	return s.parent.emitAs(s.sub, Event{Type: EventTextMessageContent, MessageID: messageID, Delta: delta})
}

func (s *SubEmitter) EndText(messageID string) error {
	return s.parent.emitAs(s.sub, Event{Type: EventTextMessageEnd, MessageID: messageID})
}

func (s *SubEmitter) StartTool(messageID, toolCallID, name string) error {
	return s.parent.emitAs(s.sub, Event{Type: EventToolCallStart, MessageID: messageID, ToolCallID: toolCallID, Delta: name, ToolCallName: name})
}

func (s *SubEmitter) WriteArgs(toolCallID, delta string) error {
	return s.parent.emitAs(s.sub, Event{Type: EventToolCallArgs, ToolCallID: toolCallID, Delta: delta})
}

func (s *SubEmitter) EndTool(toolCallID string) error {
	return s.parent.emitAs(s.sub, Event{Type: EventToolCallEnd, ToolCallID: toolCallID})
}

func (s *SubEmitter) ToolResult(messageID, toolCallID string, result any) error {
	return s.parent.emitAs(s.sub, Event{Type: EventToolCallResult, MessageID: messageID, ToolCallID: toolCallID, Result: result})
}

func (s *SubEmitter) StartReasoning(messageID string) error {
	if err := s.parent.emitAs(s.sub, Event{Type: EventReasoningStart, MessageID: messageID}); err != nil {
		return err
	}
	return s.parent.emitAs(s.sub, Event{Type: EventReasoningMessageStart, MessageID: messageID})
}

func (s *SubEmitter) WriteReasoning(messageID, delta string) error {
	return s.parent.emitAs(s.sub, Event{Type: EventReasoningMessageContent, MessageID: messageID, Delta: delta})
}

func (s *SubEmitter) EndReasoning(messageID string) error {
	if err := s.parent.emitAs(s.sub, Event{Type: EventReasoningMessageEnd, MessageID: messageID}); err != nil {
		return err
	}
	return s.parent.emitAs(s.sub, Event{Type: EventReasoningEnd, MessageID: messageID})
}

func (s *SubEmitter) SnapshotActivity(messageID, activityType string, content any, replace ...bool) error {
	e := Event{Type: EventActivitySnapshot, MessageID: messageID, ActivityType: activityType, Content: content}
	if len(replace) > 0 {
		r := replace[0]
		e.Replace = &r
	}
	return s.parent.emitAs(s.sub, e)
}

func (s *SubEmitter) PatchActivity(messageID, activityType string, patch any) error {
	return s.parent.emitAs(s.sub, Event{Type: EventActivityDelta, MessageID: messageID, ActivityType: activityType, Patch: patch})
}

func (s *SubEmitter) EmitCustom(name string, value any) error {
	return s.parent.emitAs(s.sub, Event{Type: EventCustom, CustomName: name, CustomValue: value})
}

func (s *SubEmitter) EmitRaw(event any, source string) error {
	return s.parent.emitAs(s.sub, Event{Type: EventRaw, RawEvent: event, RawSource: source})
}

func (s *SubEmitter) SnapshotState(snapshot any) error {
	return s.parent.emitAs(s.sub, Event{Type: EventStateSnapshot, Snapshot: snapshot})
}

func (s *SubEmitter) PatchState(patch any) error {
	return s.parent.emitAs(s.sub, Event{Type: EventStateDelta, Patch: patch})
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

// StartTool opens a tool invocation under a parent message; name rides Delta
// with ToolCallName as the long spelling.
func (m *Emitter) StartTool(messageID, toolCallID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit(Event{Type: EventToolCallStart, MessageID: messageID, ToolCallID: toolCallID, Delta: name, ToolCallName: name})
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
	return m.emitAs("", e)
}

// emitAs stamps one subagent attribution on top of emit. Caller holds m.mu.
func (m *Emitter) emitAs(sub string, e Event) error {
	if err := m.requireRun(); err != nil {
		return err
	}
	e.ThreadID, e.RunID, e.Timestamp = m.threadID, m.runID, time.Now().UnixMilli()
	if sub != "" {
		e.SubagentRunID = sub // invocation handle tags by construction
	}
	_, err := m.log.Append(e)
	return err
}

// requireRun fails writes with no open run. Caller holds m.mu.
func (m *Emitter) requireRun() error {
	if m.runID == "" {
		return fmt.Errorf("agui: no open run (call StartRun first)")
	}
	return nil
}
