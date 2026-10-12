<div align="center">

# ContextLoom

**A context-aware coding agent harness — weave the right context for your LLM, in terminal or desktop.**

[English](README.md) | [中文](README.zh-CN.md)

Go 1.22+ · macOS / Linux / Windows · MIT License

</div>

---

ContextLoom is a coding-agent harness written in Go. It drives an LLM through your terminal (TUI), a native desktop window (GUI), or any MCP/ACP-compatible external tool, while its context engine keeps the information density of every request high: long conversations are compressed on a budget, project memory persists across sessions, and sub-agents are supervised by a scoring gate before their output reaches you.

## Highlights

- **Context engine** — token budgeting, rolling summaries, and automatic compaction keep long tasks inside the context window without losing thread.
- **JSON-structured state for long tasks** — task/goal state is managed as structured JSON instead of free-form prose, which survives compaction and multi-agent hand-offs.
- **Supervised sub-agents** — a built-in overseer scores every sub-agent deliverable against configurable quality gates; below-threshold output is sent back, never shown.
- **Multi-provider model gateway** — any OpenAI-compatible endpoint (llama.cpp, ollama, vLLM, local, cloud) plus an optional DeepSeek channel; rule-based routing (`role`, `task`, `budget`), multi-key pooling, circuit-breaker failover.
- **Everything is a plugin** — employees (role presets), scenes, toolchains, and quality gates are declarative YAML files; project-level and user-level plugin directories merge automatically.
- **MCP client & server** — consume external MCP tools, or expose ContextLoom itself as an MCP server for Claude Code, Cursor, etc.
- **Persistent memory** — sessions stored as append-only JSONL event logs; resume any conversation with `--memory-load`, compacted snapshots included.
- **Tool sandbox** — network/file-write policies, shadow filesystem for writes, per-run memory limits.
- **Reproducible evals** — offline agent-level evaluation harness (`eval.yaml`) with trace-based scoring.
- **Two UIs, one core** — a keyboard-first TUI and a desktop GUI shell (Wails/WebView) with browser fallback on headless servers.

## Quick start

```bash
git clone https://github.com/contextloom/contextloom-agent.git
cd contextloom-agent

# 1. configure
cp config.example.yaml config.yaml   # set your endpoint / api_key

# 2. run (TUI)
make build && ./contextloom

# or straight from source
go run ./cmd/contextloom
```

Point `models[0].endpoint` at any OpenAI-compatible service, e.g. a local llama.cpp server at `http://127.0.0.1:8080/v1`. API keys can be provided via `OPENAI_API_KEY` (or `DEEPSEEK_API_KEY` when using the `deepseek` backend channel).

## Building

Prefer raw `go` commands? Everything works without Make:

```bash
go build -ldflags="-s -w" -o contextloom ./cmd/contextloom   # current platform (TUI)
go test -race -cover ./...                                    # tests
```

Or use the Makefile, which wraps the same commands plus project-specific guards:

```bash
make build    # current platform binary (TUI)
make test     # tests with -race and coverage
make lint     # golangci-lint
```

Cross-compilation is native Go — set `GOOS`/`GOARCH`, e.g. `GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o contextloom-linux-arm64 ./cmd/contextloom`.

### Desktop GUI (optional)

```bash
make build-gui          # macOS / Linux (WebKitGTK on Linux)
make build-gui-win      # cross-compile Windows GUI (needs mingw-w64; PE subsystem verified automatically)
make build-gui-macos    # package a macOS .app bundle
make verify-gui         # verify the GUI PE subsystem flag
```

Raw equivalent (why `make` is recommended here): the GUI build needs `CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 go build -tags production -ldflags="-s -w -H windowsgui"` — omitting `-H windowsgui` yields a console-subsystem exe that flashes a terminal window on launch. The Makefile also auto-runs `scripts/pecheck` to verify the PE subsystem flag, guarding against regressions.

The GUI shell launches the `contextloom` backend from its own directory (or via `CONTEXTLOOM_BIN`), so keep both binaries side by side.

## Usage

```bash
contextloom                    # interactive TUI
contextloom --resume           # continue the most recent session
contextloom --memory-load <id> # load a specific session
contextloom trace --tail 50    # inspect JSONL execution trace
contextloom --serve            # ACP service mode
contextloom --mcp              # run as an MCP server (stdio JSON-RPC)
```

In the TUI, press `/` for commands: `/model`, `/mode` (`chat` / `task` / `goal`), `/trace`, `/resume`, and more. `?` shows all shortcuts.

## Configuration

`config.yaml` (see the fully commented [`config.example.yaml`](config.example.yaml)) defines:

- `models` — presets (name/backend/endpoint/api_key/max_context/...); `/model <name>` hot-switches between them.
- `gateway` — optional routing rules and key pools: `role=reviewer → strong-model`, `task=vision → vision-model`, `budget<=low → cheap-model`.
- `sandbox`, `memory`, `plugins`, `project` — tool isolation, session storage, plugin directories, project root.

Per-project data lives in a `.contextloom/` directory (sessions, plugins, toolchains, gates, trace), so projects are self-contained and shareable.

## Architecture

```
┌────────────┐  ┌──────────────┐  ┌─────────────┐
│  TUI       │  │  GUI (Wails) │  │ MCP / ACP   │
└─────┬──────┘  └──────┬───────┘  └──────┬──────┘
      └──────────┬─────┴──────────────────┘
          ┌──────▼───────┐
          │   runtime    │  orchestration, overseer, goal manager
          └──────┬───────┘
   ┌─────────────┼──────────────┐
┌──▼───┐   ┌────▼─────┐   ┌────▼─────┐
│ ctxmgr│   │ executor │   │  memory  │
│budget │   │ sub-agents│  │ JSONL log│
└──┬────┘   └────┬─────┘   └──────────┘
   │        ┌────▼─────┐
   │        │  model   │  provider gateway (OpenAI-compatible / DeepSeek)
   └────────┤ gateway  │
            └──────────┘
```

See [docs/architecture.md](docs/architecture.md) for the full tour, [docs/plugins.md](docs/plugins.md) for the plugin system, and [docs/toolchain.md](docs/toolchain.md) for employees/scenes/toolchains/gates.

## Development

```bash
gofmt -w .          # formatting is CI-enforced
go vet ./...
go test -race ./...
```

CI runs gofmt check, `go vet`, build, and the test suite on Linux/macOS/Windows with Go 1.22. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
