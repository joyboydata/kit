# Config loaders: ServerLoader + AppLoader (one binary, fail-closed)

Type: decision
Status: accepted
Date: 2026-11-01

## Context

`kit/config` today is two helpers with no agreed contract. Both files are
embedded verbatim in the Appendix (base `b12830c`); the load-bearing
excerpts:

`Load(cfg, envPrefix)` applies tag defaults, reads `config.yaml` from
implicit cwd paths, injects `<PREFIX>_*` env vars, unmarshals, validates:

```go
v := viper.New()
v.SetConfigName("config")
v.AddConfigPath(".")
v.AddConfigPath("./config")
if err := v.ReadInConfig(); err != nil {
	if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
		return fmt.Errorf("read config file: %w", err)
	}
	// config file not found is okay — fall through to env
}
```

```go
prefix := envPrefix + delimiter
for _, e := range os.Environ() {
	if !strings.HasPrefix(e, prefix) {
		continue
	}
	pair := strings.SplitN(e, "=", 2)
	if len(pair) != 2 {
		continue
	}
	envKey := strings.TrimPrefix(pair[0], prefix)
	// APLIQ_AUTH_CLIENT_ID → auth.client_id
	// Replace only the first delimiter to separate section from field.
	parts := strings.SplitN(envKey, delimiter, 2)
	viperKey := strings.ToLower(parts[0]) + "." + parts[1]
	v.Set(viperKey, pair[1])
}
```

`AddConfigPaths(v, app)` registers the XDG user dir, then system dirs,
then legacy `/etc/<app>` (full source in Appendix):

```go
func AddConfigPaths(v *viper.Viper, app string) {
	for _, d := range ConfigSearchDirs(app) {
		v.AddConfigPath(d)
	}
}
```

Actual callers diverge three ways (excerpts verbatim, same base):

apliq wraps `Load` with no file flag; both entry points share it:

```go
func Load() (*Config, error) {
	cfg := &Config{}
	if err := xcfg.Load(cfg, "APLIQ"); err != nil {
		return nil, err
	}
	return cfg, nil
}
```

```go
// main.go — serve path
cfg, err := config.Load()
if err != nil {
	log.Fatalf("failed to load configuration: %v\n", err)
}
```

```go
// main.go — add-credit path
cfg, err := config.Load()
if err != nil {
	fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
	os.Exit(1)
}
```

Gets implicit cwd reads; no XDG, no explicit file flag.

librarian does **not** use `xcfg.Load`: it hand-rolls viper and splits cwd
behavior across build tags (`config_dev.go` adds project-local paths,
`config_nofile.go` searches XDG/global only) — two binaries, two contracts:

```go
// config_nofile.go — release setupViper
func setupViper(v *viper.Viper) {
	// Global XDG + system paths via shared kit helper.
	kitconfig.AddConfigPaths(v, "klaboterman")

	// Read config file (non-fatal if absent)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			// Log but don't fail — env vars may provide everything needed
		}
	}
}
```

```go
// config_dev.go — dev setupViper
func setupViper(v *viper.Viper) {
	// Project-local paths (dev override)
	for _, p := range configPaths {
		v.AddConfigPath(p)
	}

	// Global XDG + system paths via shared kit helper.
	kitconfig.AddConfigPaths(v, "klaboterman")

	// Read config file (non-fatal if absent)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			// Log but don't fail — env vars may provide everything needed
		}
	}
}
```

workspace hand-rolls viper on config name `workspace` with
`AddConfigPaths` plus its own env loop — closest to the desired
AppLoader, but bespoke:

```go
v := viper.New()
v.SetConfigName("workspace")
v.SetConfigType("yaml")
config.AddConfigPaths(v, "klaboterman")

// Step 2: read config file (non-fatal when absent).
if err := v.ReadInConfig(); err != nil {
	if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
		return loaded, fmt.Errorf("read config: %w", err)
	}
} else {
	configFileUsed = v.ConfigFileUsed()
}

// Step 3: WORKSPACE_* env overrides (highest priority).
v.SetEnvPrefix("WORKSPACE")
v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
v.AutomaticEnv()
for _, key := range []string{"workspaces_dir", "repos_root", "default_repo", "default_branch"} {
	if val, ok := os.LookupEnv("WORKSPACE_" + strings.ToUpper(key)); ok {
		v.Set(key, val)
	}
}
```

Human-approved direction (this ADR specifies, not renegotiates): **ServerLoader**
(env-default) + **AppLoader** (XDG-default); struct-tag shape declarations;
`FlagPath` accepted by both; **no implicit cwd reads**; **one binary**;
**fail-closed** (missing required = hard error at startup, never silent
defaults).

## Decision

1. Provide two loaders in `kit/config` (names: `ServerLoader`, `AppLoader`):
   - `ServerLoader`: defaults (tags) < optional file (`--config` flag path only)
     < `<PREFIX>_*` env (highest). No XDG, no cwd.
   - `AppLoader`: defaults (tags) < XDG search
     (`ConfigSearchDirs(app)`) < `--config` flag path override (highest-priority
     file when given) < `<PREFIX>_*` env (highest overall).
2. Shape declared via struct tags already in use: `mapstructure:"…"`,
   `default:"…"`, `validate:"…"` (fail-closed = `validate:"required"` errors at
   startup). Secrets marked per § Pretty-print rule.
3. Both loaders accept `FlagPath string` (empty = no explicit file). No loader
   reads `./config.yaml` or `./config/config.yaml` unless that path arrives via
   `FlagPath`. Viper `ConfigFileNotFound` stays non-fatal only when no file was
   requested; a requested-but-unreadable file is a hard error.
4. One binary: delete the `dev`/`!dev` (`config_dev.go` / `config_nofile.go`)
   split; dev override becomes `--config ./config.yaml` (explicit).
5. Fail-closed: after unmarshal, run validator; any `required` violation or
   unreadable explicit file returns an error the caller must treat as fatal
   (`log.Fatal` / non-zero exit before serving). No silent zero-value start.
6. Pretty-print resolved config at startup per § Pretty-print (redacted,
   per-key source).

## Approaches (v2 vs modify)

**Option A — new `kit/config/v2` API, keep current symbols frozen.**
Callers opt in one by one; old `Load`/`AddConfigPaths` behavior (incl. cwd
reads) stays green during migration. Cost: two loader contracts live side by
side; the cwd-read footgun stays importable; librarian's build-tag split must
still be unpicked inside v2; three bespoke env-injection loops persist until
every caller moves. Payoff only matters with external/unknown consumers.

**Option B — evolve in place (modify `Load` / `AddConfigPaths` surface).**
Change the contract where it stands: remove implicit cwd reads, add the two
loaders (+ `FlagPath`), unify env injection, keep `ConfigSearchDirs` semantics.
Cost: flag-day for the three known callers (all first-party, all listed above);
any out-of-tree importer of `xcfg.Load` sees a behavior break. Benefit: one
contract, no drift period, the footgun disappears instead of being deprecated.

**Recommendation: Option B — modify in place.** Caller surface is exactly three
first-party sites (apliq wrapper + librarian + workspace); no external
consumers exist in the workspace. A v2 package buys parallel-run safety nobody
needs while doubling the API to document and test. The breaking change (killing
implicit cwd reads) *is* the fix — versioning around it preserves the bug.

## Consequences

- Positive: single loader contract; cwd-poisoning class eliminated; dev/prod
  parity via explicit `--config`; fail-closed uniform across server and app
  paths; pretty-print gives every incident a "what did it start with" line.
- Tradeoffs: flag-day — all three callers migrate together; `xcfg.Load(cfg,
  prefix)` callers must pass a `FlagPath` (even if `""`).
- Risks: operators relying on accidental cwd `config.yaml` (apliq serve) get a
  startup error instead of silent pickup — intended, but announce it; XDG-only
  AppLoader means a misplaced file is silently ignored unless `--config` is
  passed (mitigated by pretty-print `source: default` visibility + `file:`
  field showing which file won).

## Migration sketch (under recommended Option B)

1. `kit/config`: add `ServerLoader`/`AppLoader` (+ `FlagPath` option), remove
   `.`/`./config` from default search, unify `<PREFIX>_*` injection, add
   pretty-printer; keep `ConfigSearchDirs`/`AddConfigPaths` (AppLoader uses
   them internally).
2. `apliq/internal/config/config.go`: the `xcfg.Load` call in `Load()` →
   `ServerLoader` with `FlagPath` wired from a `--config` flag; the serve
   and `add-credit` entry points in `apliq/main.go` parse the flag once
   and pass it to `config.Load()`.
   Remove reliance on cwd pickup; ops passes `--config` explicitly.
3. `klaboterman/cmd/librarian/`: delete `config_nofile.go` + `config_dev.go`
   build-tag split; `Load()` in `cmd/librarian/config.go` delegates to `AppLoader`
   (`app="klaboterman"`, prefix `LIBRARIAN`, `FlagPath` from `--config`).
   Keep `ConfigFileUsed` semantics via the loader's returned file path.
4. `klaboterman/internal/workspace/config.go`: replace the bespoke viper in
   `Load()` +
   `AddConfigPaths` + env loop with `AppLoader` (`app="klaboterman"`, config
   name `workspace`, prefix `WORKSPACE`, `FlagPath` from flag); keep `~`
   expansion and `ConfigFileUsed()` accessor on the loader result.
5. Gate: `go build/vet/test ./config/` green; then `go build ./...` in apliq +
   klaboterman; manual check: start each binary with no file + no env and
   confirm hard error (fail-closed), then with `--config` pointing at a cwd
   file and confirm it loads (explicit, not implicit).

## Pretty-print (resolved-config log)

- **What:** one log block at program start showing every top-level key, its
  effective value, and its source (`env` | `file` | `default`).
- **Output shape** (one line per leaf, `section.key = value  (source)` plus a
  header with the winning file):
  `config file=<path|none> prefix=<PREFIX>` then lines like
  `server.port = 8080  (env APLIQ_SERVER_PORT)` /
  `auth.client_secret = [REDACTED]  (env)` / `server.scheme = http  (default)`.
- **Redaction rule:** a leaf is `[REDACTED]` if its field carries
  `secret:"true"` **or** its `mapstructure` name contains `secret`, `token`,
  `api_key`, or `password` (case-insensitive), **or** it carries
  `validate:"required"` with an empty default and no file/env value present
  (shown as `[MISSING]` + startup aborts). Required-but-present secrets still
  print as `[REDACTED]` — never the value. Non-secret values print verbatim.
- **New tag:** `secret:"true"` is the explicit marker; teams add it to any
  credential the substring heuristic misses (e.g. apliq `AuthConfig.ClientID`
  at `apliq/internal/config/config.go`, librarian `LLM.APIKey` /
  `Context7.APIKey` at `klaboterman/cmd/librarian/config.go`, mayar/api tokens).
- **Hook:** explicit call, not loader side-effect — loader returns
  `{ Config, FileUsed, Sources map[key]source }`; caller invokes
  `PrintResolved(cfg, meta)` (or `LogResolved`) immediately after successful
  load, before serving. Keeps libraries quiet and gives each `main.go` entry
  call site each; tests assert on the returned `Sources` map without capturing
  logs.

## Open items

- Final Go symbol names/signatures (`Options` struct vs variadic) at
  implementation time; this ADR binds behavior, not identifiers.
- Whether `secret:"true"` also triggers `validate:"required"` implicitly —
  currently no; redaction and requirement stay orthogonal.

## References

- Appendix A: `kit/config/config.go` (verbatim, base `b12830c`)
- Appendix B: `kit/config/xdg.go` (verbatim, base `b12830c`)
- Caller excerpts: embedded in Context above (same base)
- `apliq/internal/config/config.go`, `apliq/main.go`
- `klaboterman/cmd/librarian/config.go`,
  `config_nofile.go`, `config_dev.go`
- `klaboterman/internal/workspace/config.go`

## Appendix A: `kit/config/config.go` (verbatim, base `b12830c`)

```go
// Package config provides an opinionated, reusable configuration loader.
//
// Usage:
//
//	type MyConfig struct {
//		Server ServerConfig `mapstructure:"server"`
//	}
//
//	type ServerConfig struct {
//		Port int `mapstructure:"port" default:"8080" validate:"gte=1,lte=65535"`
//	}
//
//	cfg := &MyConfig{}
//	if err := config.Load(cfg, "APLIQ"); err != nil {
//	    log.Fatal(err)
//	}
//
// Load reads configuration in the following priority order:
//
//	1. Default values from `default:"..."` struct tags (lowest)
//	2. config.yaml (from . or ./config/, non-fatal if absent)
//	3. Environment variables prefixed with the given prefix (highest)
//
// After loading, it validates the config using `validate:"..."` struct tags.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/creasty/defaults"
	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"
)

// Load reads configuration into cfg using three-tier merge.
//
// Priority (lowest to highest):
//  1. Struct tag defaults (`default:"..."`)
//  2. config.yaml (from . or ./config/)
//  3. Environment variables prefixed with envPrefix
//
// After loading, the struct is validated via `validate:"..."` struct tags.
// cfg must be a non-nil pointer to a struct.
func Load(cfg any, envPrefix string) error {
	// Step 1: Apply defaults from struct tags
	if err := defaults.Set(cfg); err != nil {
		return fmt.Errorf("set defaults: %w", err)
	}

	// Step 2: Read config.yaml (if it exists)
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return fmt.Errorf("read config file: %w", err)
		}
		// config file not found is okay — fall through to env
	}

	// Step 3: Inject environment variables into Viper's store.
	//
	// Viper's AutomaticEnv only resolves env vars during per-key Get() calls,
	// not during Unmarshal (which uses AllSettings() internally).
	// To ensure nested env vars (e.g., APLIQ_AUTH_CLIENT_ID) are picked up
	// during Unmarshal, we explicitly inject them.
	const delimiter = "_"
	prefix := envPrefix + delimiter
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, prefix) {
			continue
		}
		pair := strings.SplitN(e, "=", 2)
		if len(pair) != 2 {
			continue
		}
		envKey := strings.TrimPrefix(pair[0], prefix)
		// APLIQ_AUTH_CLIENT_ID → auth.client_id
		// Replace only the first delimiter to separate section from field.
		parts := strings.SplitN(envKey, delimiter, 2)
		viperKey := strings.ToLower(parts[0]) + "." + parts[1]
		v.Set(viperKey, pair[1])
	}

	// Step 4: Unmarshal viper values into config (overrides defaults)
	if err := v.Unmarshal(cfg); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}

	// Step 5: Validate all required fields
	validate := validator.New()
	if err := validate.Struct(cfg); err != nil {
		return fmt.Errorf("config validation: %w", err)
	}

	return nil
}
```

## Appendix B: `kit/config/xdg.go` (verbatim, base `b12830c`)

```go
package config

import (
	"path/filepath"

	"github.com/adrg/xdg"
	"github.com/spf13/viper"
)

// ConfigSearchDirs returns the ordered directories to search for an app's
// config files: the user dir (xdg.ConfigHome/app) first, then each system
// dir (xdg.ConfigDirs/app) in order, then the legacy /etc/<app> last.
func ConfigSearchDirs(app string) []string {
	dirs := make([]string, 0, len(xdg.ConfigDirs)+2)
	dirs = append(dirs, filepath.Join(xdg.ConfigHome, app))
	for _, d := range xdg.ConfigDirs {
		dirs = append(dirs, filepath.Join(d, app))
	}
	legacy := filepath.Join("/etc", app)
	if len(dirs) == 0 || dirs[len(dirs)-1] != legacy {
		dirs = append(dirs, legacy)
	}
	return dirs
}

// AddConfigPaths registers each ConfigSearchDirs(app) dir on v in order.
// Viper gives higher priority to earlier-added paths, so the user dir wins
// over system dirs and /etc/<app>.
func AddConfigPaths(v *viper.Viper, app string) {
	for _, d := range ConfigSearchDirs(app) {
		v.AddConfigPath(d)
	}
}
```
