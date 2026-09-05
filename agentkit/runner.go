package agentkit

import "context"

// AgentRunner executes the AI agent and returns a stream of events.
// T is the expected output type parsed from the agent's response.
// Infrastructure concern: how the agent runs (subprocess, API call, etc.).
type AgentRunner[T any] interface {
	// Run spawns the agent with the given prompt and systemPrompt and returns a channel
	// of progress events. The channel is closed when generation completes.
	// systemPrompt is delivered to the AI model as a system-level instruction.
	// Callers must drain the channel.
	Run(ctx context.Context, prompt string, systemPrompt string) (<-chan ProgressEvent[T], error)
}
