package agui

import (
	"encoding/json"
	"fmt"
)

// EventType names one of the 20 frozen event types (the AG-UI 1.0 minimal
// core: runs + steps + text + tool + reasoning). Unknown values decode fine
// but are rejected by Validate, so the freeze holds while the spec grows.
type EventType string

// Frozen event types.
const (
	EventRunStarted  EventType = "RUN_STARTED"
	EventRunFinished EventType = "RUN_FINISHED"
	EventRunError    EventType = "RUN_ERROR"

	EventStepStarted  EventType = "STEP_STARTED"
	EventStepFinished EventType = "STEP_FINISHED"

	EventTextMessageStart   EventType = "TEXT_MESSAGE_START"
	EventTextMessageContent EventType = "TEXT_MESSAGE_CONTENT"
	EventTextMessageEnd     EventType = "TEXT_MESSAGE_END"
	EventTextMessageChunk   EventType = "TEXT_MESSAGE_CHUNK"

	EventToolCallStart  EventType = "TOOL_CALL_START"
	EventToolCallArgs   EventType = "TOOL_CALL_ARGS"
	EventToolCallEnd    EventType = "TOOL_CALL_END"
	EventToolCallChunk  EventType = "TOOL_CALL_CHUNK"
	EventToolCallResult EventType = "TOOL_CALL_RESULT"

	EventReasoningStart EventType = "REASONING_START"
	EventReasoningEnd   EventType = "REASONING_END"

	EventReasoningMessageStart   EventType = "REASONING_MESSAGE_START"
	EventReasoningMessageContent EventType = "REASONING_MESSAGE_CONTENT"
	EventReasoningMessageEnd     EventType = "REASONING_MESSAGE_END"
	EventReasoningMessageChunk   EventType = "REASONING_MESSAGE_CHUNK"
)

// knownTypes is the frozen set Validate enforces.
var knownTypes = map[EventType]bool{
	EventRunStarted: true, EventRunFinished: true, EventRunError: true,
	EventStepStarted: true, EventStepFinished: true,
	EventTextMessageStart: true, EventTextMessageContent: true,
	EventTextMessageEnd: true, EventTextMessageChunk: true,
	EventToolCallStart: true, EventToolCallArgs: true,
	EventToolCallEnd: true, EventToolCallChunk: true,
	EventToolCallResult: true,
	EventReasoningStart: true, EventReasoningEnd: true,
	EventReasoningMessageStart: true, EventReasoningMessageContent: true,
	EventReasoningMessageEnd: true, EventReasoningMessageChunk: true,
}

// Event is the flat wire envelope. Only Type is required on the wire; every
// other field is omitempty so absent stays absent (never null).
//
// Field roles: Delta carries streaming fragments (text, args JSON, reasoning)
// and the tool name on TOOL_CALL_START; Result carries a tool outcome;
// Message/Code carry a run failure; Usage/Model ride RUN_FINISHED; Seq is
// server-assigned by Log.Append (incoming Seq is ignored).
type Event struct {
	Type        EventType         `json:"type"`
	Timestamp   int64             `json:"timestamp,omitempty"`
	ThreadID    string            `json:"threadId,omitempty"`
	RunID       string            `json:"runId,omitempty"`
	ParentRunID string            `json:"parentRunId,omitempty"`
	MessageID   string            `json:"messageId,omitempty"`
	ToolCallID  string            `json:"toolCallId,omitempty"`
	StepName    string            `json:"stepName,omitempty"`
	Role        string            `json:"role,omitempty"`
	Delta       string            `json:"delta,omitempty"`
	Result      any               `json:"result,omitempty"`
	Message     string            `json:"message,omitempty"`
	Code        string            `json:"code,omitempty"`
	Usage       *Usage            `json:"usage,omitempty"`
	Model       string            `json:"model,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Seq         int64             `json:"seq,omitempty"`
}

// RunInput opens one execution of a thread. Message is the triggering turn
// (kept for the runner; Fold derives messages from events, not from input).
type RunInput struct {
	ThreadID    string            `json:"threadId,omitempty"`
	RunID       string            `json:"runId,omitempty"`
	ParentRunID string            `json:"parentRunId,omitempty"`
	Message     Message           `json:"message,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Message is one folded user-visible message.
type Message struct {
	ID        string     `json:"id,omitempty"`
	Role      string     `json:"role,omitempty"`
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
}

// ToolCall is one folded tool invocation.
type ToolCall struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Args   string `json:"args,omitempty"`
	Result any    `json:"result,omitempty"`
}

// Usage is token accounting; it rides the terminal RUN_FINISHED event.
type Usage struct {
	Input     int `json:"input,omitempty"`
	Output    int `json:"output,omitempty"`
	Total     int `json:"total,omitempty"`
	Cached    int `json:"cached,omitempty"`
	Reasoning int `json:"reasoning,omitempty"`
}

// Validate checks the stateless shape: known type plus the fields that type
// requires. Run/triad state (open runs, START-before-content) is enforced by
// Log.Append, not here, so hand-built events fail fast without a Log.
func (e Event) Validate() error {
	if !knownTypes[e.Type] {
		return fmt.Errorf("agui: unknown event type %q", e.Type)
	}
	req := func(ok bool, what string) error {
		if !ok {
			return fmt.Errorf("agui: %s requires %s", e.Type, what)
		}
		return nil
	}
	switch e.Type {
	case EventRunStarted:
		if err := req(e.ThreadID != "", "threadId"); err != nil {
			return err
		}
		return req(e.RunID != "", "runId")
	case EventRunFinished:
		return req(e.RunID != "", "runId")
	case EventRunError:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		return req(e.Message != "", "message")
	case EventStepStarted, EventStepFinished:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		return req(e.StepName != "", "stepName")
	case EventTextMessageStart:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		if err := req(e.MessageID != "", "messageId"); err != nil {
			return err
		}
		return req(e.Role != "", "role")
	case EventTextMessageContent, EventTextMessageChunk, EventTextMessageEnd:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		return req(e.MessageID != "", "messageId")
	case EventToolCallStart:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		if err := req(e.MessageID != "", "messageId"); err != nil {
			return err
		}
		if err := req(e.ToolCallID != "", "toolCallId"); err != nil {
			return err
		}
		return req(e.Delta != "", "delta (tool name)")
	case EventToolCallArgs, EventToolCallChunk, EventToolCallEnd:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		return req(e.ToolCallID != "", "toolCallId")
	case EventToolCallResult:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		if err := req(e.MessageID != "", "messageId"); err != nil {
			return err
		}
		return req(e.ToolCallID != "", "toolCallId")
	case EventReasoningStart, EventReasoningEnd,
		EventReasoningMessageStart,
		EventReasoningMessageContent, EventReasoningMessageChunk,
		EventReasoningMessageEnd:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		return req(e.MessageID != "", "messageId")
	}
	return nil
}

// Encode serializes one event. Total: no validation, so new types encode on
// old binaries.
func Encode(e Event) ([]byte, error) {
	return json.Marshal(e)
}

// Decode parses one event. Total: unknown types are accepted (forward-compat);
// Validate on Append is what rejects them for writes.
func Decode(data []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(data, &e); err != nil {
		return Event{}, err
	}
	return e, nil
}
