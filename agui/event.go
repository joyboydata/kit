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

	EventReasoningEncryptedValue EventType = "REASONING_ENCRYPTED_VALUE"

	EventStateSnapshot    EventType = "STATE_SNAPSHOT"
	EventStateDelta       EventType = "STATE_DELTA"
	EventMessagesSnapshot EventType = "MESSAGES_SNAPSHOT"

	EventActivitySnapshot EventType = "ACTIVITY_SNAPSHOT"
	EventActivityDelta    EventType = "ACTIVITY_DELTA"

	EventSubagentStarted  EventType = "SUBAGENT_STARTED"
	EventSubagentFinished EventType = "SUBAGENT_FINISHED"
	EventSubagentError    EventType = "SUBAGENT_ERROR"

	EventRaw    EventType = "RAW"
	EventCustom EventType = "CUSTOM"
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
	EventReasoningEncryptedValue: true,
	EventStateSnapshot:           true, EventStateDelta: true, EventMessagesSnapshot: true,
	EventActivitySnapshot: true, EventActivityDelta: true,
	EventSubagentStarted: true, EventSubagentFinished: true, EventSubagentError: true,
	EventRaw: true, EventCustom: true,
}

// Event is the flat wire envelope. Only Type is required on the wire; every
// other field is omitempty so absent stays absent (never null).
//
// Field roles: Delta carries streaming fragments (text, args JSON, reasoning)
// and the tool name on TOOL_CALL_START; Result carries a tool outcome;
// Message/Code carry a run failure; Usage/Model ride RUN_FINISHED; Seq is
// server-assigned by Log.Append (incoming Seq is ignored).
//
// Spec-1.0 fields (all omitempty, so old binaries keep decoding new streams):
// Outcome/Interrupts/PendingToolCallIDs/Usages close RUN_FINISHED;
// Snapshot/Patch carry STATE_SNAPSHOT/STATE_DELTA; Messages carries
// MESSAGES_SNAPSHOT; ActivityType/Content run with ACTIVITY_* (Replace
// defaults true); SubagentRunID attributes invocations (ParentSubagentRunID
// nests); RawEvent/RawSource carry RAW; CustomName/CustomValue carry CUSTOM;
// Subtype/EntityID/EncryptedValue carry REASONING_ENCRYPTED_VALUE;
// ToolCallName/ParentMessageID are the long spellings of TOOL_CALL_START;
// ProtocolVersion/State echo the run input on RUN_STARTED.
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

	Outcome            string          `json:"outcome,omitempty"`
	Interrupts         []Interrupt     `json:"interrupts,omitempty"`
	PendingToolCallIDs []string        `json:"pendingToolCallIds,omitempty"`
	Usages             []ProviderUsage `json:"usages,omitempty"`
	Snapshot           any             `json:"snapshot,omitempty"`
	Patch              any             `json:"patch,omitempty"`
	State              any             `json:"state,omitempty"`
	Messages           []Message       `json:"messages,omitempty"`
	ActivityType       string          `json:"activityType,omitempty"`
	Content            any             `json:"content,omitempty"`
	Replace            *bool           `json:"replace,omitempty"`
	SubagentRunID      string          `json:"subagentRunId,omitempty"`
	ParentSubagentRun  string          `json:"parentSubagentRunId,omitempty"`
	RawEvent           any             `json:"event,omitempty"`
	RawSource          string          `json:"source,omitempty"`
	CustomName         string          `json:"name,omitempty"`
	CustomValue        any             `json:"value,omitempty"`
	Subtype            string          `json:"subtype,omitempty"`
	EntityID           string          `json:"entityId,omitempty"`
	EncryptedValue     string          `json:"encryptedValue,omitempty"`
	ToolCallName       string          `json:"toolCallName,omitempty"`
	ParentMessageID    string          `json:"parentMessageId,omitempty"`
	ProtocolVersion    string          `json:"protocolVersion,omitempty"`
}

// RunInput opens one execution of a thread. Message is the triggering turn
// (kept for the runner; Fold derives messages from events, not from input).
// Messages is the full history (activity roles stripped before send);
// State seeds snapshot-delta tracking; Resume answers a prior interrupt and
// must cover every one of its entries; Tools are frontend tools on offer.
// Capabilities stay out of the envelope: informative-only, never rejected.
type RunInput struct {
	ThreadID        string            `json:"threadId,omitempty"`
	RunID           string            `json:"runId,omitempty"`
	ParentRunID     string            `json:"parentRunId,omitempty"`
	Message         Message           `json:"message,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	ProtocolVersion string            `json:"protocolVersion,omitempty"`
	Messages        []Message         `json:"messages,omitempty"`
	State           any               `json:"state,omitempty"`
	Resume          []ResumeEntry     `json:"resume,omitempty"`
	Tools           []FrontendTool    `json:"tools,omitempty"`
	Context         map[string]any    `json:"context,omitempty"`
	ForwardedProps  any               `json:"forwardedProps,omitempty"`
}

// Message is one folded user-visible message. Role "reasoning" holds
// reasoning blocks and "activity" holds activity snapshots; Parts carries
// multimodal history (unusable parts are stripped before send).
type Message struct {
	ID             string        `json:"id,omitempty"`
	Role           string        `json:"role,omitempty"`
	Text           string        `json:"text,omitempty"`
	ToolCalls      []ToolCall    `json:"toolCalls,omitempty"`
	Parts          []ContentPart `json:"parts,omitempty"`
	ActivityType   string        `json:"activityType,omitempty"`
	Content        any           `json:"content,omitempty"`
	EncryptedValue string        `json:"encryptedValue,omitempty"`
}

// ToolCall is one folded tool invocation.
type ToolCall struct {
	ID             string `json:"id,omitempty"`
	Name           string `json:"name,omitempty"`
	Args           string `json:"args,omitempty"`
	Result         any    `json:"result,omitempty"`
	EncryptedValue string `json:"encryptedValue,omitempty"`
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
// OutcomeOrSuccess reports the run outcome with the absent-means-success default.
func (e Event) OutcomeOrSuccess() string {
	if e.Outcome == "" {
		return OutcomeSuccess
	}
	return e.Outcome
}

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
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		return checkRunEnd(e.Outcome, e.Interrupts, e.PendingToolCallIDs, e.Usages)
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
		// Delta stays required for the frozen shape; ToolCallName is the
		// long spelling new producers set alongside it.
		return req(e.Delta != "" || e.ToolCallName != "", "delta/toolCallName (tool name)")
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
	case EventReasoningEncryptedValue:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		if err := req(e.Subtype == "message" || e.Subtype == "toolCall", "subtype message|toolCall"); err != nil {
			return err
		}
		if err := req(e.EntityID != "", "entityId"); err != nil {
			return err
		}
		return req(e.EncryptedValue != "", "encryptedValue")
	case EventStateSnapshot:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		return req(e.Snapshot != nil, "snapshot")
	case EventStateDelta:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		return req(e.Patch != nil, "patch")
	case EventMessagesSnapshot:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		if e.SubagentRunID != "" {
			return fmt.Errorf("agui: %s is conversation-wide, carries no subagentRunId", e.Type)
		}
		return req(e.Messages != nil, "messages")
	case EventActivitySnapshot:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		if err := req(e.MessageID != "", "messageId"); err != nil {
			return err
		}
		if err := req(e.ActivityType != "", "activityType"); err != nil {
			return err
		}
		if _, ok := asObject(e.Content); !ok {
			return fmt.Errorf("agui: %s requires object content", e.Type)
		}
		return nil
	case EventActivityDelta:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		if err := req(e.MessageID != "", "messageId"); err != nil {
			return err
		}
		if err := req(e.ActivityType != "", "activityType"); err != nil {
			return err
		}
		return req(e.Patch != nil, "patch")
	case EventSubagentStarted:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		return req(e.SubagentRunID != "", "subagentRunId")
	case EventSubagentFinished:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		if err := req(e.SubagentRunID != "", "subagentRunId"); err != nil {
			return err
		}
		if e.Usage != nil || len(e.Usages) > 0 {
			return fmt.Errorf("agui: %s carries no usage (parent RUN_FINISHED owns it)", e.Type)
		}
		switch e.Outcome {
		case "", OutcomeSuccess, OutcomeSuspended:
			return nil
		default:
			return fmt.Errorf("agui: %s outcome is success|suspended, got %q", e.Type, e.Outcome)
		}
	case EventSubagentError:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		if err := req(e.SubagentRunID != "", "subagentRunId"); err != nil {
			return err
		}
		if e.Usage != nil || len(e.Usages) > 0 {
			return fmt.Errorf("agui: %s carries no usage (parent RUN_FINISHED owns it)", e.Type)
		}
		return req(e.Message != "", "message")
	case EventRaw:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		return req(e.RawEvent != nil, "event")
	case EventCustom:
		if err := req(e.RunID != "", "runId"); err != nil {
			return err
		}
		if err := req(e.CustomName != "", "name"); err != nil {
			return err
		}
		return req(e.CustomValue != nil, "value")
	}
	return nil
}

// checkRunEnd enforces the RUN_FINISHED shape: absent outcome is success,
// interrupt needs its entries, pending calls ride success only.
func checkRunEnd(outcome string, interrupts []Interrupt, pending []string, usages []ProviderUsage) error {
	switch outcome {
	case "", OutcomeSuccess, OutcomeCancelled:
		if len(interrupts) > 0 {
			return fmt.Errorf("agui: RUN_FINISHED outcome %q carries no interrupts", outcome)
		}
	case OutcomeInterrupt:
		if len(interrupts) == 0 {
			return fmt.Errorf("agui: RUN_FINISHED outcome interrupt requires interrupts")
		}
		for _, it := range interrupts {
			if it.ID == "" {
				return fmt.Errorf("agui: RUN_FINISHED interrupt requires id")
			}
		}
		if len(pending) > 0 {
			return fmt.Errorf("agui: RUN_FINISHED outcome interrupt carries no pendingToolCallIds")
		}
	default:
		return fmt.Errorf("agui: RUN_FINISHED unknown outcome %q", outcome)
	}
	if len(pending) > 0 && outcome != "" && outcome != OutcomeSuccess {
		return fmt.Errorf("agui: pendingToolCallIds ride success only")
	}
	for _, u := range usages {
		if u.Provider == "" || u.Model == "" {
			return fmt.Errorf("agui: usage entry requires provider and model")
		}
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
