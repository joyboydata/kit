package agentkit

// EventType classifies a ProgressEvent emitted by the agent runner.
type EventType string

const (
	// EventProgress is a human-readable status update (e.g. "Building prompt...").
	EventProgress EventType = "progress"
	// EventThinking carries real-time AI thinking/reasoning text.
	EventThinking EventType = "thinking"
	// EventResult carries the final output from the agent.
	EventResult EventType = "result"
	// EventError carries an error message.
	EventError EventType = "error"
)

// Status represents the current state of a generation session.
type Status string

const (
	// StatusIdle indicates no generation is in progress.
	StatusIdle Status = "idle"
	// StatusGenerating indicates the agent is actively producing output.
	StatusGenerating Status = "generating"
	// StatusDone indicates the agent completed successfully and a result is available.
	StatusDone Status = "done"
	// StatusError indicates the agent encountered an error and no result is available.
	StatusError Status = "error"
)

// ProgressEvent is emitted by AgentRunner.Run through the returned channel.
// T is the concrete type of the Result field, defined by the caller at the call site.
type ProgressEvent[T any] struct {
	// Type classifies this event.
	Type EventType
	// Message is a human-readable progress update (set for EventProgress).
	Message string
	// Thinking is the current AI thinking/reasoning text (set for EventThinking).
	Thinking string
	// Result is set for EventResult. Its type T is defined by the caller.
	Result T
	// Error is set for EventError.
	Error string
}
