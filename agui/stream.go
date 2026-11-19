package agui

import (
	"fmt"
	"reflect"
	"time"
)

// IsStreamOpener reports whether an event may open a stream: RUN_STARTED for
// a fresh run, or a fail-before-begin RUN_ERROR when transport fails first.
func IsStreamOpener(e Event) bool {
	return e.Type == EventRunStarted || e.Type == EventRunError
}

// AppendStream appends with the full spec-1.0 stream lifecycle: the first
// event must open with RUN_STARTED|RUN_ERROR, a RUN_ERROR may arrive before
// any run begins, and a late RUN_ERROR after close is recorded (the run then
// reads as failed). Append stays frozen for the 20-type core, so existing
// callers keep the strict single-terminal behavior; new streams use this.
func (l *Log) AppendStream(e Event) (Event, error) {
	if err := e.Validate(); err != nil {
		return Event{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.insert(e, true)
}

// insert fills, dedupes, checks, and stores one event. Caller holds l.mu.
func (l *Log) insert(e Event, stream bool) (Event, error) {
	e.Seq = 0
	if e.ThreadID == "" {
		e.ThreadID = l.threadID
	}
	if e.ThreadID != l.threadID {
		return Event{}, fmt.Errorf("agui: thread mismatch: event %q, log %q", e.ThreadID, l.threadID)
	}
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}
	for _, stored := range l.events {
		dup := stored
		dup.Seq = 0
		if reflect.DeepEqual(dup, e) {
			return stored, nil // idempotent: same content, existing Seq
		}
	}
	if stream {
		if len(l.events) == 0 && !IsStreamOpener(e) {
			return Event{}, fmt.Errorf("agui: stream must open with RUN_STARTED|RUN_ERROR, got %s", e.Type)
		}
		if e.Type == EventRunError {
			st, ok := l.runs[e.RunID]
			if !ok || !st.open {
				// Fail-before-begin or late error: record the closed run.
				l.runs[e.RunID] = &runState{
					open: false, texts: map[string]bool{},
					tools: map[string]*toolState{}, reasoning: map[string]bool{},
					reasonMsgs: map[string]bool{}, steps: map[string]bool{},
					activities: map[string]*activityState{}, subagents: map[string]*subagentState{},
					msgOwner: map[string]string{}, toolOwner: map[string]string{},
					reasonOwner: map[string]string{},
				}
				e.Seq = l.nextSeq
				l.nextSeq++
				l.events = append(l.events, e)
				return e, nil
			}
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

// StateOf returns the run-scoped state tracked by STATE_SNAPSHOT/STATE_DELTA,
// seeded by the RUN_STARTED input echo. The clone keeps callers from mutating
// the store.
func (l *Log) StateOf(runID string) (any, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.runs[runID]
	if !ok || !st.hasState {
		return nil, false
	}
	return cloneAny(st.state), true
}

// ActivityOf returns one activity message's live type and content.
func (l *Log) ActivityOf(runID, messageID string) (string, map[string]any, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.runs[runID]
	if !ok {
		return "", nil, false
	}
	act, ok := st.activities[messageID]
	if !ok {
		return "", nil, false
	}
	out := make(map[string]any, len(act.content))
	for k, v := range act.content {
		out[k] = cloneAny(v)
	}
	return act.typ, out, true
}

// SubagentOpen reports whether an invocation is live in the run.
func (l *Log) SubagentOpen(runID, subagentRunID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.runs[runID]
	if !ok {
		return false
	}
	sub, ok := st.subagents[subagentRunID]
	return ok && sub.open
}
