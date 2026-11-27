# kit

Shared Go building blocks for the joyboydata apps: HTMX-first web
primitives on top of Echo + templ, a frozen chat protocol core with a
server-rendered widget, config loading, structured logging, and an
in-process sandbox for end-to-end tests.

Requires Go 1.27+. Install a package, not the repo — the mains are
code generators (`echokit/cmd/openapigen`, `echokit/cmd/piextgen`,
`logkit/cmd/logdoc`) plus the hxchat demo server
(`hxchat/sandbox/serve`):

```bash
go get github.com/joyboydata/kit/hxkit/v2
```

## Packages

| Package | What it is |
|---|---|
| `hxkit/v2` | Resource-oriented HTMX framework: one URL, N `?view` representations, mutations (`SwapWith` / `RedirectTo` / `Hold`), and SSE streams with `Resolve` + debounce + terminal redirect — layout, errors, and framing applied as framework policy. Non-resource routes (marketing pages, logout) render through the `Render` escape hatch. |
| `agui` | Frozen, stdlib-only chat protocol core: envelope, `Log`, `Emitter`, codec, `Fold`, plus the contract-only `Runtime`/`Thread` surface. |
| `hxchat` | Server-rendered `<chat-thread>` widget over `hxkit/v2`: `New(store, runner, opts)` → `Resource()` (`[]v2.Resource`) + `Tag`/`Head`. Widget goes on the main `App`; the asset sibling serves bytes so it needs a nil-`Layout` `App`. |
| `sandbox` | In-process harness that boots a real `v2.App` on one localhost port for E2E tests: throwaway `Dirs` per run, seed/scenario control plane, fake `External` services behind `/_control/world`, no-DNS cookie-jar client. |
| `config` | Opinionated Viper loader: struct-tag defaults → `config.yaml` → `<PREFIX>_*` env → `validate` checks, plus XDG `ConfigSearchDirs` / `AddConfigPaths` helpers. |
| `echokit` | Declarative Echo route groups with OpenAPI/Swagger spec generation, a TypeScript client generator, and PI extension tool definitions via `Tool` (`echokit/cmd/openapigen`, `echokit/cmd/piextgen`). |
| `logkit` | Structured slog helpers plus `@log.*` comment annotations consumed by `logkit/cmd/logdoc` to generate log-schema docs. |

See `docs/20261101-config-loaders.md` for the config-loader ADR
(ServerLoader + AppLoader, fail-closed).

## Quick start: hxkit/v2

Declare a resource — representations selected by `?view`, layout applied
to direct visits only, errors mapped to the canonical HTMX wire shape:

```go
// shell is your LayoutFunc: it wraps direct visits only, HTMX swaps skip it.
var apps = v2.Resource{
    Path: "/apps",
    Representations: []v2.Representation{
        {Name: "", Handler: listApps},          // GET /apps
        {Name: "cards", Handler: cardsApps},    // GET /apps?view=cards
        {Name: "dialog", EmbedOnly: true, Handler: appDialog},
    },
    Mutations: []v2.Mutation{
        {Path: "publish", Handler: publishApp}, // POST /apps/publish
    },
    Streams: []v2.Stream{appsEvents}, // Path joins under /apps, e.g. "events"
}

app := v2.App{Layout: shell, Resources: []v2.Resource{apps}}
app.Register(e) // *echo.Echo; dump app.Routes() in tests to audit the table
```

Handlers share one signature and return components or coded errors —
the framework owns 303/fragment/`HX-Redirect` conventions:

```go
func publishApp(c echo.Context) (templ.Component, error) {
    if err := doPublish(c); err != nil {
        return nil, v2.E(http.StatusForbidden, "plan does not allow publishing")
    }
    return v2.RedirectTo("/apps?view=cards"), nil // plain→303, HTMX→HX-Redirect
}
```

Streams push live regions over SSE with replay, debounce, and dedup:

```go
// Watch: func(c echo.Context) (<-chan struct{}, func())
// Resolve: func(c echo.Context) []v2.Fired
var appsEvents = v2.Stream{
    Path: "events", Replay: true,
    Events: []v2.Event{{
        Name:      "status", // must equal the layout's sse-swap value
        Subscribe: store.WatchApps,
        Resolve:   currentFrames,
    }},
    Terminal: onShutdown, // optional: func(c echo.Context) (string, bool) — emits `redirect`, then closes
}
```

## Quick start: sandbox

```go
env, err := sandbox.Start(sandbox.Config{
    Source: sandbox.Static{
        Mount: sandbox.Mount{App: app},
        Seeds: map[string]func(ctx context.Context) error{
            "published": seedPublished,
        },
        Describe: currentState,
    },
    Default: "published",
})
if err != nil { t.Fatal(err) }
defer env.Close()

client := env.WebClient() // cookie jar, no DNS needed
_ = env.Seed("published") // wipe + reseed over HTTP
```

## Quick start: config

```go
type Config struct {
    Server ServerConfig `mapstructure:"server"`
}
type ServerConfig struct {
    Port int `mapstructure:"port" default:"8080" validate:"gte=1,lte=65535"`
}

cfg := &Config{}
if err := config.Load(cfg, "APLIQ"); err != nil {
    log.Fatal(err)
}
```

## Developing

```bash
go build ./...
go test ./...
```

`templ` components need regeneration after `.templ` edits:

```bash
go generate ./...
```
