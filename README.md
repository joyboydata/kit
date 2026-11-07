# kit

Shared Go building blocks for the joyboydata apps: HTMX-first web
primitives on top of Echo + templ, config loading, structured logging,
and an in-process sandbox for end-to-end tests.

```go
import "github.com/joyboydata/kit/hxkit/v2"
```

Requires Go 1.27+. Install a package, not the repo — there is no
binary here except `migrate`:

```bash
go get github.com/joyboydata/kit/hxkit/v2
```

## Packages

| Package | What it is |
|---|---|
| `hxkit/v2` | Resource-oriented HTMX framework: one URL, N `?view` representations, mutations (`SwapWith` / `RedirectTo` / `Hold`), and SSE streams with `Resolve` + debounce + terminal redirect — layout, errors, and framing applied as framework policy. |
| `hxkit` (v1) | Original page/fragment module layer. Stable; new code should use `hxkit/v2`. |
| `sandbox` | In-process harness that boots a real `v2.App` on one localhost port for E2E tests: throwaway `Dirs` per run, seed/scenario control plane, fake `External` services behind `/control/world`, no-DNS cookie-jar client. |
| `config` | Opinionated Viper loader: struct-tag defaults → `config.yaml` → `<PREFIX>_*` env → `validate` checks, plus XDG `ConfigSearchDirs` / `AddConfigPaths` helpers. |
| `echokit` | Declarative Echo route groups with OpenAPI/Swagger spec generation and a TypeScript client generator (`cmd/openapigen`, `cmd/piextgen`). |
| `logkit` | Structured slog helpers plus `@log.*` comment annotations consumed by `cmd/logdoc` to generate log-schema docs. |
| `mailer` | Minimal SMTP mail client (`Client` interface + `SMTPClient`). |
| `migrate` | One-shot Postgres → bbolt app-metadata migration (`go run ./migrate`). |

See `docs/20261101-config-loaders.md` for the config-loader ADR
(ServerLoader + AppLoader, fail-closed).

## Quick start: hxkit/v2

Declare a resource — representations selected by `?view`, layout applied
to direct visits only, errors mapped to the canonical HTMX wire shape:

```go
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
}

app := v2.App{Layout: shell, Resources: []v2.Resource{apps}}
app.Register(e) // *echo.Echo
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
Streams: []v2.Stream{{
    Path: "events", Replay: true,
    Events: []v2.Event{{
        Name:      "status", // must equal the layout's sse-swap value
        Subscribe: store.WatchApps(ctxOf(c)),
        Resolve:   currentFrames,
    }},
    Terminal: onShutdown, // optional: emits a `redirect` event, then closes
}},
```

## Quick start: sandbox

```go
env, err := sandbox.Start(sandbox.Config{
    Mount: sandbox.Mount{App: app},
    Seeds: map[string]func(ctx context.Context) error{
        "published": seedPublished,
    },
    Default:  "published",
    Describe: currentState,
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
