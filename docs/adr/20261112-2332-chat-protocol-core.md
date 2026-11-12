# Frozen chat protocol core + `<chat-thread>` widget
Type: decision
Status: implemented (kit 9020b53, tag v0.3.0)
Date: 2026-11-12
Based on: apliq `20261105-0542-run-event-log.md`, apliq `20261105-1414-agui-bridge.md`
Supersedes: apliq `20261112-2320-chat-protocol-core.md` (moved to owning repo)

## Decision

1. Extract the AG-UI 1.0 minimal core (runs + steps + text + tool + reasoning, 20 of 31 event types) into a stdlib-only, frozen `kit/agui` package: envelope + `Log` + `Emitter` + `Encode`/`Decode`/`Fold`.
2. Ship one higher-level adapter, `kit/hxchat`, over hxkit v2 with exactly two seams (`Store`, `Runner`) and per-region `Render*` slots defaulting to public `Default*View` renderers.
3. Make the mount a custom element `<chat-thread>` (attributes `thread`/`src`/`stream`/`post`, auto-minted isolation, `theme` hook) with **HTML frames** as the wire format — the element swaps server-rendered fragments, never templates JSON.
4. Freeze the forward-compat rule: `Event` fields additive-optional only; unknown types decode-and-ignore; `Fold` gains per-family rules without touching message derivation.
5. Adopt in apliq by pointing `Options.Path` at the existing `/apps/:id/revisions/:rid` resource (reuse `POST messages` + `stream`, add one `?view=thread` representation); no `/chat` routes in apliq.

## Context

The revision builder already speaks half of AG-UI 1.0: the persisted run log is an AG-UI-shaped envelope (`RunEvent` with `Seq` ordering), and the ADK bridge maps parts to `TEXT_*` / `TOOL_CALL_*` / `STEP_*` / `REASONING_*` rows with terminal usage on `RUN_FINISHED`:

```go
type RunEvent struct {
	ID         string            `json:"id"         validate:"required"`
	RevisionID string            `json:"revisionId" validate:"required"`
	Seq        int64             `json:"seq"        validate:"gte=1"`
	Type       RunEventType      `json:"type"       validate:"required"`
	Timestamp  time.Time         `json:"timestamp"`
	Payload    map[string]any    `json:"payload,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}
```

Missing: `CHUNK` spellings, `STATE_*` / `MESSAGES_SNAPSHOT`, `ACTIVITY_*`, `SUBAGENT_*`, `RAW`/`CUSTOM`, interrupt/resume, capabilities, a transport codec. The hard part worth sharing across projects is not the events but the reliable UI loop (idempotent writes → notify → replay/dedup → terminal → retry), which today is hand-wired per page. This decision freezes the protocol core once and puts the loop behind a one-call widget.

Nomenclature (AG-UI): `threadId` = stable conversation (apliq: the revision), `runId` = one ephemeral execution (apliq: roughly a turn; the bridge may open several hop-runs per turn chained by `parentRunId`).

## Approaches

### A. Custom element + server HTML (recommended)

`<chat-thread>` shell (EventSource, region swap by id, composer POST, locked/error state) + server-rendered HTML frames. Mount is one tag, server wiring one `Resource()`, style via `theme` → CSS vars with `::part` override.

```html
<chat-thread thread="abc" src="/chat/abc?view=thread" stream="/chat/abc/stream" post="/chat/abc/messages">
</chat-thread>
```

Tradeoffs: + one-call mount, no global HTMX required, isolation per instance, no client/server render skew; − a JS artifact to freeze and browser-test; some duplication of hxkit's SSE client logic.

### B. Pure hxkit fragments, no custom element

Widget = `?view=thread` fragment + `sse-connect` wrapper div, parent page provides HTMX + SSE extension.

Tradeoffs: + zero JS, fully inside the frozen-HTML discipline; − parent layout must load scripts/CSS, region prefixes and stream URLs hand-plumbed per mount (the sprawl that motivated A).

### C. JSON events + client rendering

Server streams AG-UI JSON, element templates messages client-side.

Tradeoffs: + smallest wire, full client control; − duplicates `Fold`/dedup/terminal logic in JS, two implementations to freeze and keep in sync, stale clients render stale UI. Rejected: violates single-source-of-truth rendering.

## Alternatives

- Full 31-event core now (state, activity, subagents, passthrough): deferred, not rejected — the surface is additive (see Consequences) and the cut 11 are listed in Open items.
- Generic `Event` payload (`map[string]any`, today's `RunEvent` shape) instead of flat typed fields: rejected — flat `omitempty` fields enforce absent-means-absent at compile time and keep `Encode`/`Decode` total.

## Consequences

Positive:

- `agui` has no Echo/templ/ADK/bbolt imports: importable by any future project; apliq migrates its `RunEvent`/`RunBridge` onto it.
- UI updates ship with the Go binary: slot override → next stream frame carries new HTML; `Replay` reconverges late joiners; no client bundle skew.
- Extension without forking: unset slot = default; set slot = decorator around `Default*View`, whose HTML is a golden-tested contract.
- Reliability policy frozen once: idempotent append on `runId`/`messageId`, mutation returns `Hold` (stream pushes truth), `Tick` debounce + identical-frame dedup, `Terminal` close, retry as new run with `ParentRunId`.

Tradeoffs / risks:

- JS + CSS artifacts (`element.js`/`element.css`, `go:embed`, served by `Resource()`) need browser-level contract tests (sandbox `browser/cases.yaml`).
- `Resource()` must return `[]v2.Resource` (widget + asset sibling), not one — single-`Resource` shape does not fit.
- Giant threads re-read `Fold` per tick; windowing/`MESSAGES_SNAPSHOT` reserved for later (not v1).

Forward-compat mechanism (the freeze holds while the spec grows):

- New event types decode-and-ignore; `Fold` adds per-family rules; new regions are new `Render*` slots; interrupts are additive `outcome` + resume-by-`ParentRunId`; `CHUNK` spellings already included.

## Implementation

Order:

1. `kit/agui`: `Event` (20 types), `RunInput`, `Message`, `ToolCall`, `Usage`, `Log`, `Emitter`, `Encode`/`Decode`/`Fold` + contract tests (ordering, triad discipline, absent-means-absent, golden fold).

```go
func NewLog(threadID string) *Log
func NewEmitter(l *Log) *Emitter
func Encode(e Event) ([]byte, error)
func Decode(data []byte) (Event, error)
func Fold(events []Event) []Message
```

2. `kit/hxchat`: `Store` / `Runner` seams, `Options` (slots), `Chat`, `Resource() []v2.Resource`, `Tag(m Mount)`, `Head()`, `Default*View` + goldens, `element.js`/`element.css`.

```go
type Store interface {
  Append(threadID string, e agui.Event) (agui.Event, error)
  List(threadID string) []agui.Event
  Subscribe(threadID string) (<-chan struct{}, func())
}

type Runner interface {
  Run(ctx context.Context, in agui.RunInput, em *agui.Emitter) error
}

func (c *Chat) Tag(m Mount) templ.Component
func (c *Chat) Head() templ.Component
```

3. apliq adoption: `Options.Path = "/apps/:id/revisions/:rid"`, add `?view=thread` representation delegating to existing transcript renderers, wrap dialog in `<chat-thread>`; `Head()` once in dashboard layout.
4. Sandbox coverage: stub-model run → frames → `Fold` assertions + browser case (send, stream, lock, retry).

Package layout: `kit/agui/*` (stdlib only), `kit/hxchat/*` (`element.js`, `element.css`, `chat.go`, `defaults.go`, `*_test.go`).

### Surface: `kit/agui`

```go
package agui // github.com/joyboydata/kit/agui

type EventType string // 20 values: RUN_STARTED/FINISHED/ERROR, STEP_STARTED/FINISHED,
                       // TEXT_MESSAGE_{START,CONTENT,END,CHUNK}, TOOL_CALL_{START,ARGS,END,CHUNK,RESULT},
                       // REASONING_START/END, REASONING_MESSAGE_{START,CONTENT,END,CHUNK}

type Event struct {
  Type string; Timestamp int64; ThreadID, RunID, ParentRunID string
  MessageID, ToolCallID, StepName, Role, Delta string
  Result any; Message, Code, Model string
  Usage *Usage; Metadata map[string]string; Seq int64
}
type RunInput struct { ThreadID, RunID, ParentRunID string; Message Message; Metadata map[string]string }
type Message struct { ID, Role, Text string; ToolCalls []ToolCall }
type ToolCall struct { ID, Name, Args string; Result any }
type Usage struct { Input, Output, Total, Cached, Reasoning int }
type Log struct { /* append-only, Seq-ordered */ }
type Emitter struct { /* triad discipline */ }

func (e Event) Validate() error
func (l *Log) Append(e Event) (Event, error)
func (l *Log) List() []Event
func (l *Log) Fold() []Message
func (m *Emitter) StartRun(in RunInput) error
func (m *Emitter) FinishRun(usage *Usage, model string) error
func (m *Emitter) FailRun(message, code string) error
func (m *Emitter) StartStep/EndStep(stepName string) error
func (m *Emitter) StartText/WriteText/EndText(messageID, ... string) error
func (m *Emitter) StartTool/WriteArgs/EndTool(toolCallID, ... string) error
func (m *Emitter) ToolResult(messageID, toolCallID string, result any) error
func (m *Emitter) StartReasoning/WriteReasoning/EndReasoning(messageID, ... string) error

func NewLog(threadID string) *Log
func NewEmitter(l *Log) *Emitter
func Encode(e Event) ([]byte, error)
func Decode(data []byte) (Event, error)
func Fold(events []Event) []Message
```

### Surface: `kit/hxchat`

```go
package hxchat // github.com/joyboydata/kit/hxchat

type Store interface {
  Append(threadID string, e agui.Event) (agui.Event, error)
  List(threadID string) []agui.Event
  Subscribe(threadID string) (<-chan struct{}, func())
}
type Runner interface {
  Run(ctx context.Context, in agui.RunInput, em *agui.Emitter) error
}
type RunState struct { Phase, RunID string }
type Mount struct { ThreadID, Prefix, Theme string }
type Options struct {
  Path string; Tick time.Duration
  ThreadID func(c echo.Context) string
  Prefix func(c echo.Context) string; Theme string
  RenderThread func(m Mount, msgs []agui.Message) templ.Component
  RenderMessage func(m Mount, msg agui.Message) templ.Component
  RenderComposer func(m Mount, locked bool) templ.Component
  RenderStatus func(m Mount, s RunState) templ.Component
  RenderError func(m Mount, err error, runID string) templ.Component
}
type Chat struct { /* unexported */ }

func (c *Chat) Resource() []v2.Resource
func (c *Chat) Tag(m Mount) templ.Component
func (c *Chat) Head() templ.Component
func (c *Chat) StreamURL(m Mount) string

func New(store Store, runner Runner, opts Options) *Chat
func DefaultThreadView(m Mount, msgs []agui.Message) templ.Component
func DefaultMessageView(m Mount, msg agui.Message) templ.Component
func DefaultComposerView(m Mount, locked bool) templ.Component
func DefaultStatusView(m Mount, s RunState) templ.Component
func DefaultErrorView(m Mount, err error, runID string) templ.Component
```

### Examples

Fresh project (all defaults, one call each side):

```go
h := hxchat.New(store, runner, hxchat.Options{Path: "/chat/:thread"})
app := v2.App{Layout: shell, Resources: h.Resource()}
app.Register(e)
<head>{ h.Head() }</head>
<body>{ h.Tag(hxchat.Mount{ThreadID: "abc"}) }</body>
```

apliq adoption (existing resource, no `/chat` routes):

```go
h := hxchat.New(revStore, adkRunner, hxchat.Options{
  Path: "/apps/:id/revisions/:rid", // post → messages, stream → stream
  ThreadID: func(c echo.Context) string { return c.Param("rid") },
  Theme: "comic",
})
// + one EmbedOnly ?view=thread representation delegating to transcript renderers
```

Slot override (paywall composer, stock underneath):

```go
RenderComposer: func(m hxchat.Mount, locked bool) templ.Component {
  if !currentUser.HasAIAccess() {
    return paywallNudge(m)
  }
  return hxchat.DefaultComposerView(m, locked)
},
```

Element stack (decided): vanilla custom elements, no build step — one hand-authored `element.js`, `go:embed`-ed, version-locked by the Go module. `node --check` smoke in CI is test-only, never a build.

Freeze gate for `element.js` (decided): fragment goldens (Go) + serve contract (content-type, immutable cache, URL resolution) + `node --check` smoke + sandbox `browser/cases.yaml` against a stub `Runner` (send → lock → stream → unlock; error → retry; reconnect → `Replay`; two mounts isolated via auto `?hxchatns=`). No JS unit framework in v1.

## Open items

- Cut 11 event types to add later: `REASONING_ENCRYPTED_VALUE`, `STATE_SNAPSHOT`, `STATE_DELTA`, `MESSAGES_SNAPSHOT`, `ACTIVITY_SNAPSHOT`, `ACTIVITY_DELTA`, `SUBAGENT_{STARTED,FINISHED,ERROR}`, `RAW`, `CUSTOM`.
- Shadow DOM vs light DOM for the element shell (styling override is `::part`/vars vs vars/BEM respectively).
- Retry shape: client-button re-POST (recommended) vs runner auto-retry; failure surface inline vs terminal redirect.
- Idempotency key transport for double-submit across reconnects (composer lock covers v1).

## References

- https://docs.ag-ui.com/spec/1.0/basic/index.md (event model, envelope, identifiers)
- https://docs.ag-ui.com/spec/1.0/events/index.md (eight families, 31 types)
- `internal/domain/resource/run_event.go` (`RunEvent` envelope, current 16-type coverage)
- `internal/agent/runlog.go` (`RunBridge`: ADK → AG-UI rows, per-hop runs)
- `internal/httpserver/internal/host/app/revisions/templates.templ` (`V2RevisionResource`: path, `messages` mutation, `stream`)
- `kit/hxkit/v2/stream.go` (`Replay`, `Tick` debounce, frame dedup, `Terminal`)
- `kit/hxkit/v2/mutation.go` (`SwapWith` / `RedirectTo` / `Hold` commands)
