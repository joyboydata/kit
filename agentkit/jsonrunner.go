package agentkit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// jsonAgentRunner implements AgentRunner by spawning a pi subprocess to produce JSON output.
type jsonAgentRunner[T any] struct {
	piPath         string
	provider       string
	model          string
	sessionDir     string            // directory for persisting pi sessions; empty means ephemeral (--no-session)
	skills         []string          // paths to .ts skill files loaded via --skill (requires --no-skill)
	toolExtensions []string          // paths to .ts extension files loaded via --extension
	env            map[string]string // additional environment variables injected into the pi subprocess
	logger         *slog.Logger
}

// NewJSONAgentRunner creates an AgentRunner that prompts an AI agent to produce JSON output of type T.
// The runner spawns a pi subprocess with --mode json, disables built-in tools and context files,
// and extracts the first JSON object from the model's response deserialized into T.
// The prompt must instruct the model to return a JSON object matching the schema of T.
//
// All configuration is via functional options. Calling with no options gives sensible defaults:
// pi binary from PATH, pi's default provider and model, ephemeral session, slog.Default() logger,
// no skills, no extensions, and no additional environment variables.
func NewJSONAgentRunner[T any](opts ...JSONAgentRunnerOption) AgentRunner[T] {
	cfg := &jsonAgentRunnerConfig{
		piPath: "pi",
		logger: slog.Default(),
		env:    make(map[string]string),
	}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.env == nil {
		cfg.env = make(map[string]string)
	}
	return &jsonAgentRunner[T]{
		piPath:         cfg.piPath,
		provider:       cfg.provider,
		model:          cfg.model,
		sessionDir:     cfg.sessionDir,
		logger:         cfg.logger,
		skills:         cfg.skills,
		toolExtensions: cfg.toolExtensions,
		env:            cfg.env,
	}
}

// JSONAgentRunnerOption configures a JSONAgentRunner.
type JSONAgentRunnerOption func(*jsonAgentRunnerConfig)

// jsonAgentRunnerConfig holds the configurable fields for JSONAgentRunner.
// This non-generic struct avoids leaking the type parameter T into option functions.
type jsonAgentRunnerConfig struct {
	piPath         string
	provider       string
	model          string
	sessionDir     string
	logger         *slog.Logger
	skills         []string
	toolExtensions []string
	env            map[string]string
}

// WithPiBinary sets the path to the pi binary. Default is "pi" (looked up from PATH).
func WithPiBinary(path string) JSONAgentRunnerOption {
	return func(c *jsonAgentRunnerConfig) { c.piPath = path }
}

// WithProvider sets the LLM provider (e.g., "google", "anthropic", "openai").
// Empty uses pi's configured default provider.
func WithProvider(name string) JSONAgentRunnerOption {
	return func(c *jsonAgentRunnerConfig) { c.provider = name }
}

// WithModel sets the model pattern/ID. Empty uses pi's configured default model.
func WithModel(id string) JSONAgentRunnerOption {
	return func(c *jsonAgentRunnerConfig) { c.model = id }
}

// WithSessionDir enables session persistence in the given directory.
// Empty (default) uses ephemeral mode (--no-session).
func WithSessionDir(dir string) JSONAgentRunnerOption {
	return func(c *jsonAgentRunnerConfig) { c.sessionDir = dir }
}

// WithLogger sets the structured logger for the runner. Default is slog.Default().
// If nil is passed, it is replaced with slog.Default().
func WithLogger(logger *slog.Logger) JSONAgentRunnerOption {
	return func(c *jsonAgentRunnerConfig) {
		if logger == nil {
			c.logger = slog.Default()
		} else {
			c.logger = logger
		}
	}
}

// WithSkills loads .ts skill files. When provided, --no-skill is passed first
// to disable default skills, then each path is loaded via --skill.
func WithSkills(paths ...string) JSONAgentRunnerOption {
	return func(c *jsonAgentRunnerConfig) { c.skills = paths }
}

// WithEnv sets additional environment variables injected into the pi subprocess.
// Use this to pass configuration values (e.g., SLICER_API_URL) to extensions that read from process.env.
func WithEnv(env map[string]string) JSONAgentRunnerOption {
	return func(c *jsonAgentRunnerConfig) { c.env = env }
}

// WithToolExtensions loads .ts extension files via --extension.
// When provided, --no-extensions is passed first to disable default extensions.
func WithToolExtensions(paths ...string) JSONAgentRunnerOption {
	return func(c *jsonAgentRunnerConfig) { c.toolExtensions = paths }
}

// piEvent is the top-level envelope for pi --mode json output.
type piEvent struct {
	Type                  string           `json:"type"`
	AssistantMessageEvent *json.RawMessage `json:"assistantMessageEvent,omitempty"`
}

// assistantMessageEvent is the nested event inside piEvent for message updates.
type assistantMessageEvent struct {
	Type         string          `json:"type"`
	ContentIndex int             `json:"contentIndex"`
	Delta        string          `json:"delta,omitempty"`
	Content      string          `json:"content,omitempty"`
	Partial      json.RawMessage `json:"partial,omitempty"`
}

// partialContent is an element of the content array inside partial messages.
type partialContent struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking,omitempty"`
	Text     string `json:"text,omitempty"`
}

// Run spawns the PI agent with the given prompt and system prompt using --mode json
// and emits streaming progress events through the returned channel.
// The systemPrompt is delivered to the AI model as a system-level instruction via
// pi's --system-prompt flag.
// The channel is closed when generation completes.
func (r *jsonAgentRunner[T]) Run(ctx context.Context, prompt string, systemPrompt string) (<-chan ProgressEvent[T], error) {
	ch := make(chan ProgressEvent[T], 100)

	go r.run(ctx, prompt, systemPrompt, ch)

	return ch, nil
}

func (r *jsonAgentRunner[T]) run(ctx context.Context, prompt string, systemPrompt string, ch chan<- ProgressEvent[T]) {
	defer close(ch)

	r.logger.Info("starting agent generation")
	ch <- ProgressEvent[T]{Type: EventProgress, Message: "Building prompt..."}

	// Build pi command args including --mode json for structured streaming output
	args := []string{"--mode", "json", "-p", prompt}
	if r.provider != "" {
		args = append([]string{"--provider", r.provider}, args...)
	}
	if r.model != "" {
		args = append([]string{"--model", r.model}, args...)
	}
	// Disable built-in tools and context files for ephemeral generation runs.
	// Only tools registered via --extension (e.g. Slicer API tools) are available to the agent.
	args = append(args, "--no-builtin-tools", "--no-context-files")

	// Control session persistence: when a sessionDir is configured, point pi at it so
	// sessions survive container restarts. Otherwise run ephemerally (no save).
	if r.sessionDir != "" {
		args = append(args, "--session-dir", r.sessionDir)
	} else {
		args = append(args, "--no-session")
	}

	// Reset skills and extensions to baseline, then selectively load
	// only the ones we explicitly provide. This ensures the agent runs
	// in a clean, predictable environment.
	args = append(args, "--no-skills")
	for _, skill := range r.skills {
		absPath, err := filepath.Abs(skill)
		if err != nil {
			ch <- ProgressEvent[T]{Type: EventProgress, Message: fmt.Sprintf("Skipping skill %s: %s", skill, err)}
			continue
		}
		args = append(args, "--skill", absPath)
		r.logger.Debug("loaded skill", slog.String("path", absPath))
	}

	args = append(args, "--no-extensions")
	for _, ext := range r.toolExtensions {
		absPath, err := filepath.Abs(ext)
		if err != nil {
			ch <- ProgressEvent[T]{Type: EventProgress, Message: fmt.Sprintf("Skipping tool extension %s: %s", ext, err)}
			continue
		}
		args = append(args, "--extension", absPath)
		r.logger.Debug("loaded tool extension", slog.String("path", absPath))
	}

	// Add native system prompt support via pi's --system-prompt flag.
	// This is passed as-is to the model, making it a true system-level instruction
	// that the model prioritizes over user input.
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}

	// Spawn pi process with a timeout
	cmdCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, r.piPath, args...)

	// Inject additional environment variables into the pi subprocess.
	// This allows passing configuration values (e.g., SLICER_API_URL) to extensions
	// that read from process.env. We preserve the parent process's environment
	// and add/override with the runner's env map.
	if len(r.env) > 0 {
		cmd.Env = buildEnv(r.env)
	}

	r.logger.Info("spawning pi agent with json streaming",
		slog.String("cmd", r.piPath+" "+strings.Join(args, " ")),
		slog.String("binary", r.piPath),
		slog.String("provider", r.provider),
		slog.String("model", r.model),
		slog.String("env_vars", strings.Join(buildEnv(r.env), "-")),
	)

	// Pipe stdout line-by-line for real-time JSON event parsing
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		ch <- ProgressEvent[T]{Type: EventError, Error: fmt.Sprintf("failed to create stdout pipe: %s", err)}
		return
	}

	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		errMsg := fmt.Sprintf("pi agent failed to start: %s", err)
		r.logger.Error(errMsg)
		ch <- ProgressEvent[T]{Type: EventError, Error: errMsg}
		return
	}

	// Scan stdout line by line, parsing JSON events in real time
	var textBuf strings.Builder
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var evt piEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			// Skip lines that aren't valid JSON
			r.logger.Debug("skipping non-JSON line from pi stdout",
				slog.String("line", truncateString(line, 200)),
			)
			continue
		}

		switch evt.Type {
		case "message_update":
			if evt.AssistantMessageEvent == nil {
				continue
			}
			var ame assistantMessageEvent
			if err := json.Unmarshal(*evt.AssistantMessageEvent, &ame); err != nil {
				continue
			}

			switch ame.Type {
			case "thinking_start", "thinking_delta", "thinking_end":
				thinking := extractThinkingFromPartial(ame.Partial)
				if thinking != "" {
					select {
					case ch <- ProgressEvent[T]{Type: EventThinking, Thinking: thinking}:
					default:
						// Channel full, drop to avoid blocking
					}
				}

			case "text_start":
				// Start of a new text content block. If there are multiple text blocks
				// (e.g. text → tool call → more text), each starts fresh.
				textBuf.Reset()
				textBuf.WriteString(ame.Delta)

			case "text_delta":
				textBuf.WriteString(ame.Delta)

			case "text_end":
				// text_end.Content is the full accumulated text of this block.
				// Use it as authoritative to avoid duplication from deltas.
				if ame.Content != "" {
					textBuf.Reset()
					textBuf.WriteString(ame.Content)
				}
				// Try to parse as JSON of type T. If it fails, this text block was likely
				// preliminary output (e.g. planning before tool calls). Continue
				// scanning — the model may emit more text blocks with the final JSON.
				parsed, parseErr := parseOutputJSON[T](textBuf.String())
				if parseErr != nil {
					r.logger.Debug("text block is not JSON, waiting for more output",
						slog.String("text", truncateString(textBuf.String(), 200)),
					)
					continue
				}
				ch <- ProgressEvent[T]{Type: EventProgress, Message: "Done!"}
				ch <- ProgressEvent[T]{Type: EventResult, Result: parsed}
				r.logger.Info("agent output received",
					slog.Int("result_size", len(textBuf.String())),
				)
				return
			}
		}
	}

	// Wait for the process to finish
	waitErr := cmd.Wait()

	// Check for scanner errors (e.g., pipe closed prematurely)
	if scanErr := scanner.Err(); scanErr != nil {
		r.logger.Error("stdout scanner error", slog.String("error", scanErr.Error()))
	}

	if waitErr != nil {
		errMsg := fmt.Sprintf("pi agent failed: %s", waitErr)
		if stderrBuf.Len() > 0 {
			errMsg += ": " + stderrBuf.String()
		}
		r.logger.Error("pi agent execution failed",
			slog.String("error", errMsg),
			slog.String("partial_text", truncateString(textBuf.String(), 500)),
			slog.String("stderr", truncateString(stderrBuf.String(), 500)),
		)
		ch <- ProgressEvent[T]{Type: EventError, Error: errMsg}
		return
	}

	// If we get here, the process exited but we never got text_end
	errMsg := "pi agent exited without producing output"
	if textBuf.Len() > 0 {
		// Try to parse whatever text we accumulated
		parsed, parseErr := parseOutputJSON[T](textBuf.String())
		if parseErr == nil {
			ch <- ProgressEvent[T]{Type: EventProgress, Message: "Done!"}
			ch <- ProgressEvent[T]{Type: EventResult, Result: parsed}
			r.logger.Info("agent output recovered from partial text")
			return
		}
		r.logger.Error(errMsg, slog.String("text", truncateString(textBuf.String(), 1000)))
	}
	ch <- ProgressEvent[T]{Type: EventError, Error: errMsg}
}

// extractThinkingFromPartial parses the partial message and returns the thinking text
// from content[0] if it's a thinking-type block.
func extractThinkingFromPartial(partial json.RawMessage) string {
	if len(partial) == 0 {
		return ""
	}
	var msg struct {
		Role    string           `json:"role"`
		Content []partialContent `json:"content"`
	}
	if err := json.Unmarshal(partial, &msg); err != nil {
		return ""
	}
	if len(msg.Content) > 0 && msg.Content[0].Type == "thinking" {
		return msg.Content[0].Thinking
	}
	return ""
}

// parseOutputJSON attempts to parse the first JSON object from text into T.
// It uses json.Decoder to decode a single top-level value, which handles cases where
// the agent appends extra text or repeats the JSON multiple times.
func parseOutputJSON[T any](text string) (T, error) {
	text = strings.TrimSpace(text)

	// Find the first '{' to locate JSON start
	jsonStart := strings.Index(text, "{")
	if jsonStart < 0 {
		var zero T
		return zero, fmt.Errorf("no JSON found in agent output")
	}

	// Decode a single top-level JSON value starting at jsonStart
	decoder := json.NewDecoder(strings.NewReader(text[jsonStart:]))
	var result T
	if err := decoder.Decode(&result); err != nil {
		var zero T
		return zero, fmt.Errorf("failed to parse agent output as JSON: %s", err)
	}

	return result, nil
}

// buildEnv merges the parent process's environment with the extra env vars.
// Extra keys override any existing values in the parent environment.
func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		if k == "" {
			continue
		}
		prefix := k + "="
		found := false
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env[i] = prefix + v
				found = true
				break
			}
		}
		if !found {
			env = append(env, prefix+v)
		}
	}
	return env
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Ensure jsonAgentRunner satisfies AgentRunner at compile time.
var _ AgentRunner[struct{}] = (*jsonAgentRunner[struct{}])(nil)