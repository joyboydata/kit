// Package agentkit provides a streaming, type-safe abstraction for running AI agents
// that produce structured JSON output.
//
// # Architecture
//
// The package has three core types that form a pipeline:
//
//	AgentRunner[T]    — Infrastructure: how the agent runs (subprocess, API call, etc.)
//	     │
//	     ▼
//	Agent[T]          — Session management: creates, tracks, and stores results
//	     │
//	     ▼
//	Session[T]        — Streaming: a single generation run with typed events
//
// The generic type parameter T is the expected JSON output type. It flows through
// every layer, so callers get compile-time type safety from agent start to result.
//
// # Schema Injection
//
// Before sending the prompt to the runner, Agent appends a JSON schema derived
// from T via reflection. The caller's prompt only needs domain logic — the output
// contract (field names, types, optionality, descriptions) is derived automatically
// from Go struct tags on T. See the `description` struct tag for field-level
// documentation that gets passed to the AI.
//
// # Usage
//
// Define your output type, create an AgentRunner, build an Agent, and stream events:
//
//	type Analysis struct {
//	    Summary string   `json:"summary"`
//	    Score   float64  `json:"score"`
//	}
//
//	runner := agentkit.NewJSONAgentRunner[Analysis](agentkit.WithLogger(logger))
//	agent  := agentkit.NewAgent[Analysis](runner, "You are a data analyst.", logger)
//
//	sess, err := agent.CreateSession(ctx, "Analyze this data...")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	for evt := range sess.Events {
//	    switch evt.Type {
//	    case agentkit.EventProgress:
//	        fmt.Println("→", evt.Message)
//	    case agentkit.EventThinking:
//	        fmt.Println("💭", evt.Thinking)
//	    case agentkit.EventResult:
//	        fmt.Printf("Result: %+v\n", evt.Result)
//	    case agentkit.EventError:
//	        fmt.Println("✗", evt.Error)
//	    }
//	}
//
// # Event Stream Lifecycle
//
// Each Session[T] owns a forward channel (buffer 64) that carries ProgressEvent[T]
// values. The session is self-updating — a drain goroutine reads from the raw
// runner channel, updates internal status/result/error fields, and forwards
// events to the public Events channel. The caller only reads.
//
// Terminal events (EventResult, EventError) close the Events channel immediately.
// If the runner closes its channel without a terminal event, Session marks itself
// as StatusError.
//
// # Extensibility
//
// The AgentRunner[T] interface is the extension point:
//
//	type AgentRunner[T any] interface {
//	    Run(ctx context.Context, prompt string, systemPrompt string) (<-chan ProgressEvent[T], error)
//	}
//
// Implementations included:
//   - NewJSONAgentRunner — spawns a pi subprocess with --mode json
//
// Custom implementations you can build:
//
//	type HTTPRunner[T any] struct {
//	    endpoint string
//	    client   *http.Client
//	}
//
//	func (r *HTTPRunner[T]) Run(ctx context.Context, prompt string, systemPrompt string) (<-chan ProgressEvent[T], error) {
//	    // Call any OpenAI-compatible API with structured output,
//	    // use systemPrompt as the 'system' role message,
//	    // parse SSE stream, emit ProgressEvents into the channel.
//	}
//
// # Thread Safety
//
// Agent[T] and Session[T] are safe for concurrent use:
//   - Agent uses sync.RWMutex for the sessions map and lastResult cache
//   - Session uses sync.RWMutex for Status(), Result(), and Error() reads
//   - The Events channel carries progress events; read and write sides are separate
package agentkit
