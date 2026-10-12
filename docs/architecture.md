# Architecture

ContextLoom is a single Go binary with a layered architecture. All UIs are thin shells over one `runtime` core.

## Layer overview

### 1. Presentation layer

| Shell  | Location                  | Notes                                                        |
|--------|---------------------------|--------------------------------------------------------------|
| TUI    | `internal/tui`            | Bubbletea-based; keyboard-first; chat/task/goal modes        |
| GUI    | `cmd/contextloom-gui`     | Wails/WebView desktop shell; launches the backend binary; browser fallback on headless hosts |
| MCP    | `internal/mcpserver`      | stdio JSON-RPC server so external agents (Claude Code, Cursor) can drive ContextLoom |
| ACP    | `internal/acp`            | HTTP service mode (`--serve`)                                |

Shells never talk to providers directly. They submit user input to `runtime.ProcessInput` and render an event stream back.

### 2. Runtime (`internal/runtime`)

The orchestrator. Responsibilities:

- Session lifecycle (`NewSession`, `LoadSession`, resume).
- Conversation history ownership; deciding when compaction runs.
- Mode dispatch: `chat` (single conversation), `task` (sub-agent fan-out with an overseer), `goal` (long-running goal loop with checkpoints).
- Live settings (`model`, `effort`, `top_p`, ...) persisted to `.contextloom/settings.json`.
- Harness scoring: every assistant deliverable is scored; below-threshold results are regenerated, not surfaced.

### 3. Context manager (`internal/ctxmgr`)

Keeps every request inside the model's context window while preserving information density:

- Token budgeting per message class (system, history, tools, current turn).
- Rolling summary: older turns collapse into a compact rolling summary.
- Compaction: when history exceeds the budget, messages are compressed into a persisted snapshot (`CompactedMessages` + `CompactedSeq`), so a later `LoadSession` can restore the compacted baseline plus post-compaction increments.
- Image budget handling for multimodal inputs.

Long-task state is JSON-structured (goals, checklists, verdicts), not prose — structured state survives compaction and sub-agent hand-offs.

### 4. Executor & sub-agents (`internal/executor`, `internal/orchestrator`)

- Fan-out: a task is split into sub-tasks with typed contracts.
- Employees: role presets (YAML) that bind a system prompt, tool set, and model route.
- Overseer (`internal/overseer`): an independent reviewer pass scores each sub-agent output against quality gates (`internal/toolchain` gates). Failures bounce back with critique.

### 5. Model gateway (`internal/model`, `internal/modelgateway`)

- `internal/model` — provider backends. Default: any OpenAI-compatible `/v1/chat/completions` endpoint (llama.cpp, ollama, vLLM, cloud). Optional: `deepseek` channel which additionally sends `reasoning_effort`.
- `internal/modelgateway` — routing rules (`role=`, `task=`, `budget<=`), multi-key pools with round-robin, per-pool concurrency caps, retries with exponential backoff, and circuit-breaker failover.

### 6. Memory (`internal/memory`)

- Sessions are append-only JSONL event logs (`sessions/<uuid>.jsonl`) + a small JSON metadata file. Compaction snapshots live inside the metadata.
- Loading a session replays events (or restores the snapshot + increments) and rebuilds conversation history.
- Long-term lessons (`lessons.md`) survive across sessions.
- No global lock file: multiple instances on the same project are safe.

### 7. Tools & sandbox (`internal/tools`, `internal/sandbox`)

- Tool implementations (exec, file edit, search, ...) with approval modes.
- Sandbox: network policy, write redirection into a shadow filesystem (`.contextloom/sandbox/`), memory limits.

### 8. Observability (`internal/trace`, `internal/eval`)

- JSONL trace with daily rotation (`.contextloom/trace/`), inspectable via `/trace` or `contextloom trace --tail N`.
- Offline eval harness: replay recorded sessions against scoring configs for reproducible agent-level evaluation.

## Project layout

```
cmd/contextloom        TUI/CLI entrypoint
cmd/contextloom-gui    desktop shell (Wails)
internal/runtime       orchestration core
internal/ctxmgr        context budgeting & compaction
internal/executor      sub-agent execution
internal/orchestrator  employees & task decomposition
internal/overseer      deliverable scoring
internal/model         provider backends
internal/modelgateway  routing, pools, failover
internal/memory        session & event-log storage
internal/tools         tool implementations
internal/sandbox       tool isolation
internal/plugin        plugin loader
internal/toolchain     toolchains & quality gates
internal/trace         JSONL observability
internal/eval          offline evaluation
pkg/types              shared types
```

## Data on disk

Everything a project generates lives under `.contextloom/`:

```
.contextloom/
├── settings.json     runtime settings (model, effort, mode, ...)
├── memory/sessions/  <uuid>.json + <uuid>.jsonl event logs
├── plugins/          project-level plugins (merged with user-level)
├── employees/        role presets
├── scenes/           scene presets
├── chains/           toolchain definitions
├── gates/            quality gate configs (presence activates gating)
├── trace/            JSONL traces (daily rotation)
└── sandbox/          shadow filesystem for sandboxed writes
```
