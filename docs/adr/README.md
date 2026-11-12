# ADRs — index

Workflow: request → propose (`proposed` + Approaches) → iterate → approve
(`accepted`) → implement (`implemented` + commit links). Never delete;
superseded files point to their replacement.

Filenames: `YYYYMMDD-HHMM-<slug>.md` (prefix = first-commit timestamp, keeps
chronology sortable). New files take the current timestamp.

| File | Type | Status | One-liner |
|---|---|---|---|
| `20261112-2332-chat-protocol-core.md` | decision | implemented (9020b53, v0.3.0; core renamed kit/chat→kit/agui in 1c5af62) | Frozen AG-UI protocol core (`kit/agui`) + `<chat-thread>` widget (`kit/hxchat`) |
