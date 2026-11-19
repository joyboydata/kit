package agui

import (
	"fmt"
	"sync"
)

// toolState tracks one invocation: which parent message it belongs to and
// whether args fragments are still accepted (between START and END).
type toolState struct {
	messageID string
	argsOpen  bool
}

// activityState is one activity message: its open type plus structured content.
type activityState struct {
	typ     string
	content map[string]any
}

// subagentState tracks one invocation: announced once per run, open until
// its FINISHED/ERROR lands. A suspended finish may continue in a later run
// under the same id, so reuse is per-run, not global.
type subagentState struct {
	open   bool
	parent string
}

// runState tracks one run's open segments so Append can enforce triad
// discipline and single-terminal lifecycle.
type runState struct {
	open        bool // started, no terminal yet
	texts       map[string]bool
	tools       map[string]*toolState
	reasoning   map[string]bool // block open per messageId
	reasonMsgs  map[string]bool // message triad open per messageId
	steps       map[string]bool
	state       any // run-scoped state, seeded by RUN_STARTED
	hasState    bool
	activities  map[string]*activityState
	subagents   map[string]*subagentState
	msgOwner    map[string]string // messageId -> subagentRunId opener tag
	toolOwner   map[string]string // toolCallId -> subagentRunId opener tag
	reasonOwner map[string]string // reasoning messageId -> opener tag
}

// attribute enforces announce-before-attribute: a tagged event needs a live
// invocation of that id in this run.
func (st *runState) attribute(tag, runID string) error {
	if tag == "" {
		return nil
	}
	sub, ok := st.subagents[tag]
	if !ok {
		return fmt.Errorf("agui: subagent %q not announced in run %q", tag, runID)
	}
	if !sub.open {
		return fmt.Errorf("agui: subagent %q not open in run %q", tag, runID)
	}
	return nil
}

// agree enforces ownership: a continuation tag must match its opener, so a
// stray subagent cannot scribble on another invocation's segment.
func (st *runState) agree(owner map[string]string, id, tag, what, runID string) error {
	if owner[id] != tag {
		return fmt.Errorf("agui: %s %q tag %q disagrees with opener %q in run %q", what, id, tag, owner[id], runID)
	}
	return nil
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
// Append validates, dedupes, and stores one event, returning the stored copy
// with Seq assigned. Append keeps the frozen core lifecycle: runs open with
// RUN_STARTED and close exactly once, so fail-before-begin and late
// RUN_ERROR go through AppendStream instead.
func (l *Log) Append(e Event) (Event, error) {
	if err := e.Validate(); err != nil {
		return Event{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.insert(e, false)
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
			return fmt.Errorf("agui: run %q already started", e.RunID)
		}
		st := &runState{
			open: true, texts: map[string]bool{},
			tools: map[string]*toolState{}, reasoning: map[string]bool{},
			reasonMsgs: map[string]bool{}, steps: map[string]bool{},
			activities: map[string]*activityState{}, subagents: map[string]*subagentState{},
			msgOwner: map[string]string{}, toolOwner: map[string]string{},
			reasonOwner: map[string]string{},
		}
		if e.State != nil {
			st.state, st.hasState = cloneAny(e.State), true // input seeds deltas
		}
		l.runs[e.RunID] = st
		return nil
	}
	st, ok := l.runs[e.RunID]
	if !ok || !st.open {
		return fmt.Errorf("agui: %s for run %q without open run", e.Type, e.RunID)
	}
	switch e.Type {
	case EventRunFinished, EventRunError:
		if e.Type == EventRunFinished {
			for id, sub := range st.subagents {
				if sub.open {
					return fmt.Errorf("agui: run %q has open subagent %q", e.RunID, id)
				}
			}
		} else {
			for _, sub := range st.subagents {
				sub.open = false // RUN_ERROR abandons every open invocation
			}
		}
		st.open = false // single terminal: second one finds !open above
		return nil
	case EventStepStarted:
		if err := st.attribute(e.SubagentRunID, e.RunID); err != nil {
			return err
		}
		if st.steps[e.StepName] {
			return fmt.Errorf("agui: step %q already started in run %q", e.StepName, e.RunID)
		}
		st.steps[e.StepName] = true
		return nil
	case EventStepFinished:
		if err := st.attribute(e.SubagentRunID, e.RunID); err != nil {
			return err
		}
		if !st.steps[e.StepName] {
			return fmt.Errorf("agui: step %q not open in run %q", e.StepName, e.RunID)
		}
		st.steps[e.StepName] = false
		return nil
	case EventTextMessageStart:
		if err := st.attribute(e.SubagentRunID, e.RunID); err != nil {
			return err
		}
		if _, dup := st.texts[e.MessageID]; dup {
			return fmt.Errorf("agui: text message %q already started in run %q", e.MessageID, e.RunID)
		}
		st.texts[e.MessageID] = true
		st.msgOwner[e.MessageID] = e.SubagentRunID
		return nil
	case EventTextMessageContent, EventTextMessageChunk:
		if !st.texts[e.MessageID] {
			return fmt.Errorf("agui: %s for message %q before START or after END", e.Type, e.MessageID)
		}
		return st.agree(st.msgOwner, e.MessageID, e.SubagentRunID, "text message", e.RunID)
	case EventTextMessageEnd:
		if !st.texts[e.MessageID] {
			return fmt.Errorf("agui: TEXT_MESSAGE_END for message %q without open segment", e.MessageID)
		}
		if err := st.agree(st.msgOwner, e.MessageID, e.SubagentRunID, "text message", e.RunID); err != nil {
			return err
		}
		st.texts[e.MessageID] = false
		return nil
	case EventToolCallStart:
		if err := st.attribute(e.SubagentRunID, e.RunID); err != nil {
			return err
		}
		if _, dup := st.tools[e.ToolCallID]; dup {
			return fmt.Errorf("agui: tool call %q already started in run %q", e.ToolCallID, e.RunID)
		}
		st.tools[e.ToolCallID] = &toolState{messageID: e.MessageID, argsOpen: true}
		st.toolOwner[e.ToolCallID] = e.SubagentRunID
		return nil
	case EventToolCallArgs, EventToolCallChunk:
		t, ok := st.tools[e.ToolCallID]
		if !ok || !t.argsOpen {
			return fmt.Errorf("agui: %s for tool call %q before START or after END", e.Type, e.ToolCallID)
		}
		return st.agree(st.toolOwner, e.ToolCallID, e.SubagentRunID, "tool call", e.RunID)
	case EventToolCallEnd:
		t, ok := st.tools[e.ToolCallID]
		if !ok || !t.argsOpen {
			return fmt.Errorf("agui: TOOL_CALL_END for tool call %q without open segment", e.ToolCallID)
		}
		if err := st.agree(st.toolOwner, e.ToolCallID, e.SubagentRunID, "tool call", e.RunID); err != nil {
			return err
		}
		t.argsOpen = false
		return nil
	case EventToolCallResult:
		if _, ok := st.tools[e.ToolCallID]; !ok {
			return fmt.Errorf("agui: TOOL_CALL_RESULT for tool call %q before START", e.ToolCallID)
		}
		return st.agree(st.toolOwner, e.ToolCallID, e.SubagentRunID, "tool call", e.RunID)
	case EventReasoningStart:
		if err := st.attribute(e.SubagentRunID, e.RunID); err != nil {
			return err
		}
		if st.reasoning[e.MessageID] {
			return fmt.Errorf("agui: reasoning block %q already started in run %q", e.MessageID, e.RunID)
		}
		st.reasoning[e.MessageID] = true
		st.reasonOwner[e.MessageID] = e.SubagentRunID
		return nil
	case EventReasoningEnd:
		if !st.reasoning[e.MessageID] {
			return fmt.Errorf("agui: REASONING_END for message %q without open block", e.MessageID)
		}
		if st.reasonMsgs[e.MessageID] {
			return fmt.Errorf("agui: REASONING_END for message %q with open reasoning message", e.MessageID)
		}
		if err := st.agree(st.reasonOwner, e.MessageID, e.SubagentRunID, "reasoning block", e.RunID); err != nil {
			return err
		}
		st.reasoning[e.MessageID] = false
		return nil
	case EventReasoningMessageStart:
		if !st.reasoning[e.MessageID] {
			return fmt.Errorf("agui: REASONING_MESSAGE_START for message %q without open block", e.MessageID)
		}
		if st.reasonMsgs[e.MessageID] {
			return fmt.Errorf("agui: reasoning message %q already started in run %q", e.MessageID, e.RunID)
		}
		if err := st.agree(st.reasonOwner, e.MessageID, e.SubagentRunID, "reasoning block", e.RunID); err != nil {
			return err
		}
		st.reasonMsgs[e.MessageID] = true
		return nil
	case EventReasoningMessageContent, EventReasoningMessageChunk:
		if !st.reasonMsgs[e.MessageID] {
			return fmt.Errorf("agui: %s for message %q before START or after END", e.Type, e.MessageID)
		}
		return st.agree(st.reasonOwner, e.MessageID, e.SubagentRunID, "reasoning block", e.RunID)
	case EventReasoningMessageEnd:
		if !st.reasonMsgs[e.MessageID] {
			return fmt.Errorf("agui: REASONING_MESSAGE_END for message %q without open segment", e.MessageID)
		}
		if err := st.agree(st.reasonOwner, e.MessageID, e.SubagentRunID, "reasoning block", e.RunID); err != nil {
			return err
		}
		st.reasonMsgs[e.MessageID] = false
		return nil
	case EventReasoningEncryptedValue:
		return st.attribute(e.SubagentRunID, e.RunID) // standalone; Fold resolves the entity
	case EventStateSnapshot:
		if err := st.attribute(e.SubagentRunID, e.RunID); err != nil {
			return err
		}
		st.state, st.hasState = cloneAny(e.Snapshot), true // wholesale replace
		return nil
	case EventStateDelta:
		if err := st.attribute(e.SubagentRunID, e.RunID); err != nil {
			return err
		}
		next, _, err := ApplyJSONPatch(st.state, e.Patch)
		if err != nil {
			return err // failed patch applies nothing; resync by snapshot
		}
		st.state, st.hasState = next, true
		return nil
	case EventMessagesSnapshot:
		return nil // conversation-wide reconcile; Fold owns the semantics
	case EventActivitySnapshot:
		if err := st.attribute(e.SubagentRunID, e.RunID); err != nil {
			return err
		}
		content, ok := asObject(e.Content)
		if !ok {
			return fmt.Errorf("agui: ACTIVITY_SNAPSHOT for message %q requires object content", e.MessageID)
		}
		if _, exists := st.activities[e.MessageID]; exists && e.Replace != nil && !*e.Replace {
			return nil // replace=false: keep the existing message
		}
		st.activities[e.MessageID] = &activityState{typ: e.ActivityType, content: content}
		st.msgOwner[e.MessageID] = e.SubagentRunID
		return nil
	case EventActivityDelta:
		if err := st.attribute(e.SubagentRunID, e.RunID); err != nil {
			return err
		}
		act, ok := st.activities[e.MessageID]
		if !ok {
			return nil // delta for unknown activity: logged, state untouched
		}
		if err := st.agree(st.msgOwner, e.MessageID, e.SubagentRunID, "activity", e.RunID); err != nil {
			return err
		}
		next, _, err := ApplyJSONPatch(act.content, e.Patch)
		if err != nil {
			return err
		}
		obj, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("agui: ACTIVITY_DELTA for message %q must stay an object", e.MessageID)
		}
		act.typ, act.content = e.ActivityType, obj
		return nil
	case EventSubagentStarted:
		if _, dup := st.subagents[e.SubagentRunID]; dup {
			return fmt.Errorf("agui: subagent %q already announced in run %q", e.SubagentRunID, e.RunID)
		}
		st.subagents[e.SubagentRunID] = &subagentState{open: true, parent: e.ParentSubagentRun}
		return nil
	case EventSubagentFinished, EventSubagentError:
		sub, ok := st.subagents[e.SubagentRunID]
		if !ok {
			return fmt.Errorf("agui: subagent %q not announced in run %q", e.SubagentRunID, e.RunID)
		}
		if !sub.open {
			return fmt.Errorf("agui: subagent %q not open in run %q", e.SubagentRunID, e.RunID)
		}
		sub.open = false
		return nil
	case EventRaw, EventCustom:
		// Standalone anywhere in an open run: no lanes open or close, so a
		// CUSTOM never seals a content segment (chunk windows excepted).
		return st.attribute(e.SubagentRunID, e.RunID)
	}
	return nil
}
