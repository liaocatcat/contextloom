<div align="center">

# ContextLoom

**A context-aware coding agent harness — weaving exactly the right context for your LLM, in both terminal and desktop form.**

[English](README.md) | [中文](README.zh-CN.md)

Go 1.22+ · macOS / Linux / Windows · MIT License

</div>

## Repository Addresses

| Region | Repository |
|---|---|
| 🇨🇳 China (mainland) | **https://gitee.com/liao-yexian-my/ds-contextloom-harness** (Gitee) |
| 🌍 International | **https://github.com/liaocatcat/ds-contextloom-harness** (GitHub) |

> The two repositories are kept in sync. Users in mainland China should clone / download releases from **Gitee**; everyone else may use **GitHub**. All download/clone commands below use GitHub links by default — if you are in mainland China, simply replace `https://github.com/liaocatcat/...` with `https://gitee.com/liao-yexian-my/...`.

---

ContextLoom is a coding agent harness (cockpit) written in Go. It drives LLMs through a terminal (TUI), a native desktop window (GUI), or any MCP/ACP-compatible external tool, while a context engine guarantees information density on every request: long conversations are progressively compacted against a token budget, compacted-original content is never lost (it remains retrievable), project memory persists across sessions, and sub-agent outputs must pass a scoring gate review before they are ever shown to you.

Single binary, zero external dependencies (no database, no vector store, no message queue). All state is persisted as JSON/JSONL inside the project — self-contained and shareable with the repository.

## Why ContextLoom: The Fundamental Difference from a "Naked Agent"

Every common pain point of general-purpose agent frameworks has a mechanistic answer in ContextLoom:

| Common pain point | ContextLoom's answer |
|---|---|
| "Cliff-edge amnesia" when long conversations exceed the window | Token-budget-driven **progressive layered compaction** (prefix truncation → drop oldest → rolling summary), and **compacted-original content is 100% retrievable via `harness_search`** — compaction ≠ loss |
| Retrieval requires a bolted-on vector store; heavy infrastructure | Built-in **Harness hybrid retrieval engine**: inverted index + symbol index + lightweight semantic reranking (cross-language EN/ZH synonyms, FNV hash embeddings) — zero dependencies, pure in-memory, persistable |
| Sub-agent output quality is uncontrollable; "claims done" cannot be verified | **Overseer + quality gates**: evidence-chain verification, test execution escrow (TEE) with real-run comparison to prevent self-deception; failing outputs are rejected and reworked, never surfaced |
| All progress lost when an agent session crashes | **Multi-agent checkpoints** (JSONL-persisted durable state): the goal loop auto-saves every round and resumes after crashes |
| Prompt engineering by hand; agents get dumber the longer they run | **Automatic employee experience distillation**: after each subtask completes, the model automatically distills "golden rules / pitfalls / style" into the employee's ontology profile, injected automatically on the next assignment — employees get better with use |
| Model routing, rate limiting, and failover each hand-rolled separately | **Model gateway**: rule-based routing by role/task/budget, multi-key pooling with rotation, circuit-breaker auto-degradation |
| Tool execution cannot be rolled back; security risks | **Tool sandbox**: write operations redirected to a shadow filesystem, forced network isolation (proxy pointed at a dead port), two-phase commit/discard |

## Does ContextLoom Save Tokens?

**Short answer: yes — but the honest claim is "each request carries only what is needed", not a fixed discount.**

| Mechanism | Where | What it does |
|---|---|---|
| Token-budget progressive compaction | `internal/ctxmgr` | Long conversations are compacted against a token budget (70% / 85% / 92% thresholds) instead of being resent in full. |
| Compacted originals are **not** re-injected | `internal/executor/agent.go` (`contextBlocksForInject`) | Removed text goes into the retrieval index and is pulled back **only** when the model calls `harness_search`. This is the pitfall most systems hit — summarize, then quietly re-add the original; here the payload genuinely shrinks. |
| Structured minimal prompts + vocabulary | `internal/preprocessor` | Input is mapped to a compact intent/entity directive; project-specific terms can be shortened via confirmed vocabulary entries. |
| On-demand retrieval instead of resident context | `internal/harness` | Only relevant blocks are selected; low-scoring blocks are overflow-compressed (first 100 runes) and injected blocks are truncated (2000 runes). |
| Generation-parameter decoupling | `max_output_tokens` / `max_input_tokens` / `thinking_length` | Cap the input, cap the output, and turn thinking off for cheap tasks (`thinking_length: off`) — thinking tokens are a real cost. |
| De-duplication and small caches | `internal/tools/task.go`, `internal/executor/agent.go` | `singleflight` merges concurrent identical sub-task spawns; few-shot examples are trimmed on a prompt-cache hit; employee experience distillation is capacity-capped (≤ 1 KB growth) so the ontology never balloons into context. |
| Result-size caps | `internal/tools/exec.go`, executor | `web_fetch` defaults to 4096 bytes; tool results and verifier targets are truncated. |

**What we do *not* claim.** No fixed "saves N% tokens" figure is published: the savings depend on conversation length, task mix, and configuration. And the token estimator deliberately **over-estimates** (e.g. CJK at 0.9 vs a measured 0.7–0.8) so compaction fires early — that is a safety choice against API 400s, not a token saving. The defensible statement is: **per-request payload is bounded, de-duplicated, and retrieved on demand rather than kept resident.**

## Core Features

### 1. Fine-Grained Context Management (ctxmgr)

Context management is ContextLoom's first-class citizen, centered on the `TokenBudget` controller — the single decision-maker for all context policies (Runtime / Agent / Backend):

- **Three progressive thresholds** (configurable, monotonicity enforced automatically):
  - `compact_ratio: 0.70` — at 70% usage, begin prefix-truncating old messages;
  - `hard_ratio: 0.85` — at 85%, drop the oldest messages and accumulate a rolling summary;
  - `safety_ratio: 0.92` — a last-line red line before sending; over-limit requests are auto-compacted/force-wrapped up;
  - `keep_recent: 8` — the most recent N messages are never dropped.
- **Rolling summary**: key points from dropped messages are accumulated and injected as a system message, avoiding cliff-edge amnesia.
- **Compaction audit**: every compaction action (time + affected scope) is recorded; the `/ctx` command shows "exactly what was compacted" — the compaction process is auditable and explainable.
- **Compacted-original content is recoverable**: truncated/dropped original text is written via callback into the Harness retrieval index and persisted to `.contextloom/harness/index.json`; after a process restart it can still be retrieved via `harness_search` (tagged `[compacted-original]` / `[压缩原文]`).
- **Multimodal budgeting**: image inputs are included in the token budget; when the backend doesn't support vision, it automatically degrades to a path-based text prompt.
- **Thinking-message protection**: reasoning/thinking messages are handled separately during compaction under a "keep most recent" policy, without polluting history.

### 2. harness_search: Retrieval Recovery for Compacted Conversations (A Capability Unique to This Project)

`harness_search` is a retrieval-type tool registered in the local agent tool table (also exposed read-only via the MCP Server), built specifically to answer "where does information go after the context is compacted":

- **Data sources**: conversation originals flushed during compaction, plus project context blocks; the collection buffer is auto-flushed before every search, guaranteeing "if it was compacted, it's searchable."
- **Hybrid retrieval engine** (`internal/harness`):
  - **Inverted index**: keyword full-text search;
  - **Symbol index**: regex extraction of functions/types/constants/class names in Go/Python/JS/TS/Rust to build a symbol → block mapping, with weighting boosts when a query hits a symbol (symbol-awareness comparable to repo maps; a zero-dependency alternative to tree-sitter);
  - **Semantic reranking**: a 0.5/0.5 fusion of query coverage + cosine similarity over feature-hash embeddings (FNV-1a hashing trick); ships with a built-in EN/ZH coding-terminology synonym table ("登录"↔login, "超时"↔timeout, …), so a Chinese query can still hit English code blocks;
  - **Time decay**: unreferenced blocks decay by `decay_factor` and are actively reclaimed via compaction (`/compress`);
  - **Sliding-window anchor zone**: important blocks can be manually promoted (`/inject <id>`) to stay resident in context.
- **Session isolation**: search results contain only "project-level shared blocks + the current session's private blocks"; multiple concurrent sessions in the same project (task/goal) never cross-contaminate — the cross-contamination root cause is guarded by a regression test.
- **Result caching**: LRU + TTL; cache keys include the session ID, and deep copies are returned to prevent mutation.
- **Persistence**: index changes are tracked by changeSeq and written to `.contextloom/harness/index.json`; retrieval capability survives process restarts.

### 3. Three Working Modes (`/mode`)

| Mode | Behavior |
|---|---|
| `chat` | Single conversation, natural-language interaction (default) |
| `task` | Multi-task concurrency: minimal JSON prompts, sub-agent fan-out, ends after one execution |
| `goal` | Persistent goal loop: plan → execute → check → next round, auto-continuing until `[GOAL_DONE]` / token budget / manual abort |

**Goal loop details**: the state machine is isolated per conversation session (each session holds its own goal); each round generates a continuation (previous round's summary + next-step instructions) injected into the next round; a checkpoint is written at the end of every round, and after a crash/interruption `/goal resume <id>` resumes the run; consecutive no-progress rounds trigger an alert and force a strategy change (split into parallel / retrieve history / ask for clarification); step evidence-chain verification — if a done step's proofs fail, it is automatically demoted to pending and defects are appended.

### 4. Overseer and Quality Gates (Overseer + Gates)

- **Overseer scoring**: every sub-agent output is scored against a configurable threshold (default 90/100); below threshold it is rejected with critical feedback attached, and never surfaced.
- **The anti-cheating trio**:
  - **Test execution escrow (TEE)**: when a run claims "tests passed," the sandbox re-executes and compares; if similarity between claimed and actual pass/fail counts is < 0.8, the claim is judged untrustworthy;
  - **Evidence-chain verification**: outputs must cite physical evidence (file paths, test run results); verbal claims are invalid;
  - **Static verification**: every round of the goal loop ends with mandatory static verification + TEE real-run testing; failure means nothing is surfaced (`[GOAL_DONE]` writing is forbidden, and defects are injected into the next round's fix list).
- **Three quality levels** (`gate.yaml: level`): `standard` (all on) / `fast` (static checks only) / `none` (off).
- **Quality ledger**: every Fail verdict writes its defect signature into `quality_ledger.yaml` — a persistent record of distrust that survives restarts.

### 5. Employee System with Automatic Experience Distillation (Employees)

- An employee = a role-preset profile (YAML): persona, tool allow/block lists, model bindings, memory slices, approval policies, skills. Six employees ship built-in: `coder / tester / reviewer / product / writer / translator`.
- `spawn_task(directive=<employee-name>)` assembles a sub-agent per the profile; in the TUI, `/emp` handles management and assignment.
- **Custom model selection with multi-source combination**: an employee's `model.preset` binds a model preset directly (no gateway rules needed), while `model.fallbacks` declares a cross-source fallback chain (local + remote mixed). `spawn_task(model=<preset>)` lets the model pick a model per task. Priority: spawn_task model param > employee model.preset+fallbacks (combined into a chained backend that auto-switches to backups when the primary is throttled/broken) > gateway rules > default model.
- **Automatic experience distillation** (a signature feature of this project): when a subtask completes and matches an employee, the model automatically distills the output into experience entries (golden rules / pitfalls / style), deduplicated and appended to that employee's `ONTOLOGY.md`; the next assignment injects them into context automatically — **employees get better with use, and distillation is done by the agent itself, not as homework for the user**. Non-blocking, idempotent (hash dedup), capacity-limited (≤1KB per run), and silently degrades on failure.

### 6. Everything Is a Plugin: Scenes / Toolchains / Gates / Manifest Plugins

- **Scenes**: keyword hits automatically take over the conversation (e.g. "write my weekly report" → the weekly scene), bundling team orchestration/models/toolchains/gates; with no hit, behavior is identical to bare chat.
- **Toolchains**: declarative DAG pipelines — three node types: `tool` (tool steps) / `llm` (model steps) / `ref` (artifact references), with `depends_on` dependency orchestration, the `$ref` artifact protocol, `{{param.*}}` parameter injection, and failure retry. Built-in: `bug-fix-chain`, `code-review-chain`, `code-test-review-chain`, `pr-review-chain`, `weekly-report-chain`.
- **Gates**: enabled whenever the `.contextloom/gates/` directory exists.
- **Manifest plugins**: `stages` (pre/during/post) + `hooks` (message/route) + custom script tools (with timeouts and sandboxing). See [`examples/plugins/`](examples/plugins/) for examples — e.g. `minimal-anchor`, a prompt-engineering plugin that anchors with a minimal prompt in the first round and releases it in the next.
- Project-level (`.contextloom/`) and user-level (`~/.contextloom/plugins/`) plugins merge automatically; project-level wins on name conflicts.

### 7. Multi-Provider Model Gateway

- Any [OI] (OpenAI-compatible) endpoint: llama.cpp, ollama, vLLM, local, or cloud; optional official `deepseek` channel (additionally sends `reasoning_effort`).
- **Rule-based routing**: `role=reviewer → strong-model`, `task=vision → vision-model`, `budget<=low → cheap-model`; comma-separated predicates are intersected.
- **Multi-key pooling**: rotation, per-pool concurrency caps, exponential backoff retry.
- **Circuit-breaker degradation**: consecutive failures trip the breaker and auto-switch pools; the switch is visible to you.
- Hot model-preset switching (`/model <name>`), automatic switching to `vision_model` for image input, and graceful degradation when `supports_vision` is false.

### 8. Persistent Memory (Memory)

- A session = an append-only JSONL event log (`sessions/<uuid>.jsonl`) + a metadata JSON; `--memory-load` / `/session` restores any session (including the compaction snapshot baseline + increments).
- **Long-term lessons** (`lessons.md`): `/teach` for manual capture, `/dream` for automatic organization — scans historical sessions to extract lessons/experience, idempotently deduplicated.
- No global lock file: multiple instances on the same project are safe.

### 9. Tool Sandbox (Sandbox)

- **Shadow filesystem**: all write operations are redirected to `.contextloom/sandbox/` (read-mirror first, listing merged); `/sandbox commit` performs a two-phase merged write-back, `/sandbox discard` throws everything away.
- **Forced network isolation**: within a sandboxed session, HTTP(S)_PROXY/ALL_PROXY for commands are forced to a dead port (connections fail immediately), and NO_PROXY is deleted to prevent bypass — a zero-dependency landing of the container `--network=none` model.
- Resource limits: per-run memory cap, CPU limit, exec timeout.
- Approval presets: `standard` (confirm sensitive operations) / `permissive` (fully automatic) / `strict` (confirm everything); task/goal automation modes auto-approve.
- File-operation audit: JSONL rotated daily (`internal/audit`), an audit trail for enterprise governance.

### 10. Observability and Evaluation

- **JSONL trace**: rotated daily under `.contextloom/trace/`, recording every iteration/tool call/duration/error; view via TUI `/trace` or CLI `trace --tail N`.
- **OTLP export**: a zero-dependency, hand-written OpenTelemetry LogsData protocol; pluggable into Collector / Langfuse / Jaeger.
- **Offline evaluation**: `contextloom eval` (agent-level, `--dry-run`/`--real`), `contextloom eval-retrieval` (retrieval quality, Recall@K / MRR).

### 11. Ecosystem Integration

- **MCP, both directions**: an MCP Client consumes external tools (dynamically registered as `mcp.<server>.<tool>`); an MCP Server exposes Harness retrieval/memory/skills read-only over stdio JSON-RPC, so any MCP-capable client can drive it.
- **ACP service**: `--serve` headless mode (the GUI launcher starts it for you).
- **External trigger channels**: webhook (HTTP POST + token auth, random port), cron (5-field expressions triggering goal/task/chat on a schedule, 30s precision).
- **Skill marketplace**: `/market` searches built-in skills, `/market install <name>` installs them, and `/skills` enables them via interactive multi-select.

## Building on All Platforms (Full TUI / GUI Command Matrix)

### TUI Core (Pure Go — Zero CGO on Any Platform)

The TUI is pure Go and cross-compiles natively; `CGO_ENABLED=0` produces binaries for every platform:

```bash
# macOS (Apple Silicon / Intel)
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o contextloom-darwin-arm64 ./cmd/contextloom
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-darwin-amd64 ./cmd/contextloom

# Linux (amd64 / arm64 / 386 / arm)
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-linux-amd64 ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w" -o contextloom-linux-arm64 ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=386   go build -ldflags="-s -w" -o contextloom-linux-386 ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=arm   GOARM=7 go build -ldflags="-s -w" -o contextloom-linux-armv7 ./cmd/contextloom

# Windows (amd64 / arm64)
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-windows-amd64.exe ./cmd/contextloom
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags="-s -w" -o contextloom-windows-arm64.exe ./cmd/contextloom

# FreeBSD
CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-freebsd-amd64 ./cmd/contextloom
```

> ⚠️ Do **not** add `-H windowsgui` to Windows TUI builds: the TUI is a console program, and that flag would break terminal interaction and output.

<details>
<summary><strong>Windows native (PowerShell / CMD)</strong> — the bash env prefixes above do not work as-is</summary>

PowerShell (`$env:` assignment; do **not** use `VAR=value go build`):

```powershell
$env:CGO_ENABLED="0"; $env:GOOS="windows"; $env:GOARCH="amd64"
go build -ldflags="-s -w" -o contextloom-windows-amd64.exe ./cmd/contextloom
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

CMD (`set VAR=value&&` — no space before `&&` and no trailing space after the value, or the
space becomes part of the variable value and Go fails with `unknown GOOS`):

```cmd
set CGO_ENABLED=0&& set GOOS=windows&& set GOARCH=amd64&& go build -ldflags="-s -w" -o contextloom-windows-amd64.exe .\cmd\contextloom
```

The arm64/386 variants follow the same pattern (just swap `GOARCH`). Alternatively run the bash commands verbatim inside WSL / MSYS2 / Git Bash.

</details>

Or use the Makefile (which wraps the same commands):

```bash
make build     # TUI binary for the current platform
make test      # -race + coverage
make lint      # golangci-lint
```

### GUI Desktop Shell (Wails/WebView — Requires CGO)

The GUI shell launches the `contextloom` backend from its own directory (or use the `CONTEXTLOOM_BIN` environment variable to point at a path) — **keep both binaries in the same directory**.

#### macOS (native build)

```bash
# GUI binary (current platform)
CGO_ENABLED=1 go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui

# Package a .app (double-clickable in Finder as a GUI app, so LaunchServices won't open the bare binary with Terminal)
make build-gui-macos        # runs scripts/build-macos-app.sh internally
```

> ⚠️ On macOS 15+ the build prints a Wails deprecation warning (`'setShowsBaselineSeparator:' is deprecated … first deprecated in macOS 15.0`) — it comes from the third-party Wails dependency, **is a warning, not an error**, and the build still exits 0 and produces a working binary (upgrading Wails does not remove it). Silence it with:
>
> ```bash
> CGO_ENABLED=1 CGO_CFLAGS="-Wno-deprecated-declarations" go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui
> ```

#### Windows (no C toolchain needed — pure-Go COM binding)

Wails v2's Windows backend is **pure Go** (`wailsapp/go-webview2`, COM binding); the module's
`import "C"` files are all `*_darwin.go`. So the Windows GUI cross-compiles with
`CGO_ENABLED=0` from **any** host — **mingw-w64 is not required** (earlier docs claimed it was;
that was wrong).

```bash
# Windows amd64 — from any host, no mingw needed
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui

# Windows on ARM64
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o contextloom-gui-windows-arm64.exe ./cmd/contextloom-gui
```

<details>
<summary><strong>Windows native (PowerShell)</strong> — equivalent command for amd64</summary>

The bash commands above use POSIX env prefixes (`VAR=value cmd`), which PowerShell cannot run
as-is. PowerShell equivalent (single line, `$env:` assignment — **do not** use
`VAR=value go build`):

```powershell
$env:CGO_ENABLED="0"; $env:GOOS="windows"; $env:GOARCH="amd64"
go build -tags production -ldflags="-s -w -H windowsgui" -o contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

CMD (single line; `set VAR=value&&` with no trailing space after the value):

```cmd
set CGO_ENABLED=0&& set GOOS=windows&& set GOARCH=amd64&& go build -tags production -ldflags="-s -w -H windowsgui" -o contextloom-gui-windows-amd64.exe .\cmd\contextloom-gui
```

The arm64 variant follows the same pattern (swap `GOARCH=arm64` and the output name).

</details>

> ⚠️ **`-H windowsgui` is a make-or-break flag**: omitting it from a Windows GUI build produces a console-subsystem exe (Subsystem=3) that always pops up a black command-line window on double-click. The flag takes effect based on the **target GOOS=windows**, independent of the build host — cross-compiling from Linux/macOS requires it just the same.
>
> The PE subsystem flag is verified automatically after building (regression guard):
>
> ```bash
> go run ./scripts/pecheck contextloom-gui-windows-amd64.exe   # should print Subsystem=2 (GUI)
> ```
>
> Makefile shortcuts (pecheck runs automatically):
>
> ```bash
> make build-gui-win      # cross-compile the Windows GUI + PE verification
> make verify-gui         # verify the PE subsystem flag of an already-built binary
> ```

#### Linux (native build — requires the WebKitGTK dev libraries)

```bash
# Prerequisites (Debian/Ubuntu)
sudo apt install libgtk-3-dev libwebkit2gtk-4.0-dev gcc pkg-config
# Prerequisites (Fedora)
sudo dnf install gtk3-devel webkit2gtk4.0-devel gcc pkgconf-pkg-config

# Build
CGO_ENABLED=1 go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui
```

> On headless Linux servers without a WebView, the GUI shell automatically falls back to browser mode.

### Build Matrix Cheat Sheet

| Target | CGO | Extra dependencies | Key flags |
|---|---|---|---|
| TUI, any platform | `CGO_ENABLED=0` | none | `-ldflags="-s -w"` |
| GUI macOS | `CGO_ENABLED=1` | Xcode CLT | `-tags production` |
| GUI Windows amd64 | `CGO_ENABLED=0` | **none** | `-tags production -ldflags="… -H windowsgui"` |
| GUI Windows arm64 | `CGO_ENABLED=0` | **none** | same as above |
| GUI Linux | `CGO_ENABLED=1` | WebKitGTK dev libs + gcc | `-tags production` |

## Download & Install (Prebuilt Binaries)

Prebuilt binaries for every supported platform are attached to each
[release](https://github.com/liaocatcat/ds-contextloom-harness/releases) (also mirrored on
[Gitee](https://gitee.com/liao-yexian-my/ds-contextloom-harness/releases)). No Go toolchain required.

**Pick your file:** `contextloom-<os>-<arch>[.exe]` is the **TUI** (terminal) build;
`contextloom-gui-<os>-<arch>[.exe]` is the **GUI** (desktop window) build.

| Platform | TUI | GUI |
|---|---|---|
| macOS (Apple Silicon) | `contextloom-darwin-arm64` | `contextloom-gui-darwin-arm64` |
| macOS (Intel) | `contextloom-darwin-amd64` | `contextloom-gui-darwin-amd64` |
| Windows x64 | `contextloom-windows-amd64.exe` | `contextloom-gui-windows-amd64.exe` |
| Windows arm64 | `contextloom-windows-arm64.exe` | `contextloom-gui-windows-arm64.exe` |
| Windows 32-bit | `contextloom-windows-386.exe` | — |
| Linux x86-64 | `contextloom-linux-amd64` | build from source (WebKitGTK) |
| Linux arm64 / 386 / armv5-7 | `contextloom-linux-arm64` … | — |
| Linux ppc64le / s390x / riscv64 / loong64 / mips* | `contextloom-linux-<arch>` | — |
| FreeBSD / NetBSD / OpenBSD / DragonFly | `contextloom-<os>-amd64` | — |
| Solaris / AIX | `contextloom-solaris-amd64`, `contextloom-aix-ppc64` | — |

> **GUI needs its backend next to it.** Put `contextloom-gui` and `contextloom` in the **same
> directory** (the GUI locates the backend via `os.Executable()`), or point at it with
> `CONTEXTLOOM_BIN=/path/to/contextloom`. The Windows GUI is a GUI-subsystem exe — double-click
> it and no console window appears.

```bash
# macOS / Linux (TUI): download, make executable, run
chmod +x contextloom-linux-amd64 && ./contextloom-linux-amd64 --version
```

> **Linux GUI** is the one target we do not prebuild: Wails binds to the system WebView, so a
> portable Linux GUI binary would have to assume GTK3/WebKitGTK versions. Build it from source
> with `-tags production` (see §GUI Desktop Shell) — on a headless host it automatically falls
> back to browser mode.

### ⭐ Put the Binary on Your PATH (Run `contextloom` from Anywhere)

After downloading (or building), move the binary into a directory that is already on your `PATH`
— or add its directory to `PATH` — so you can type `contextloom` instead of `./contextloom`.
Both the TUI (`contextloom`) and the GUI (`contextloom-gui`) work this way; keep them in the
**same directory** if you use the GUI.

**Option A — drop it into a directory already on `PATH` (recommended):**

```bash
# macOS (Apple Silicon) — /opt/homebrew/bin is on PATH if Homebrew is installed
sudo install -m 0755 contextloom-darwin-arm64 /opt/homebrew/bin/contextloom

# macOS (Intel)
sudo install -m 0755 contextloom-darwin-amd64 /usr/local/bin/contextloom

# Linux
sudo install -m 0755 contextloom-linux-amd64 /usr/local/bin/contextloom
```

```powershell
# Windows (PowerShell, user scope — no admin needed)
New-Item -ItemType Directory -Force "$env:USERPROFILE\bin" | Out-Null
Copy-Item contextloom-windows-amd64.exe "$env:USERPROFILE\bin\contextloom.exe"
```

(For Windows Option A to work, `%USERPROFILE%\bin` must already be on `PATH`; if not, use
Option B below once and it will be.)

**Option B — add the binary's own directory to `PATH` (wherever you keep it):**

```bash
# macOS (zsh, Apple Silicon example)
echo 'export PATH="/opt/homebrew/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc

# Linux (bash)
echo 'export PATH="$HOME/bin:$PATH"' >> ~/.bashrc && source ~/.bashrc
```

```powershell
# Windows (PowerShell — persists for the current user)
[Environment]::SetEnvironmentVariable("Path",
  [Environment]::GetEnvironmentVariable("Path","User") + ";$env:USERPROFILE\bin", "User")
```

```cmd
:: Windows (CMD — alternative GUI way)
:: Run:  rundll32 sysdm.cpl,EditEnvironmentVariables
:: Edit "Path" → New → append the binary's directory → OK, then reopen the terminal.
```

**Verify — open a NEW terminal window, then:**

```bash
contextloom --version    # works in any directory
which contextloom        # macOS/Linux: shows the resolved path
where contextloom        # Windows CMD/PowerShell
```

> **Notes**
> - `~/.local/bin` (Linux) and `/opt/homebrew/bin` (macOS) are commonly on `PATH` already —
>   `echo $PATH` to check what you have.
> - Windows: changes made via the System Properties dialog or the PowerShell snippet above only
>   take effect in terminals opened **after** the change.
> - The GUI (`contextloom-gui`) still needs the `contextloom` backend next to it (or
>   `CONTEXTLOOM_BIN` set) — see the note above; PATH registration does not remove that
>   requirement.

## Quick Start

```bash
git clone https://github.com/liaocatcat/ds-contextloom-harness.git
cd contextloom

# 1. Configure
cp config.example.yaml config.yaml   # fill in your endpoint / api_key

# 2. Run (TUI)
make build && ./contextloom

# Or run straight from source
go run ./cmd/contextloom
```

Point `models[0].endpoint` at any [OI]-compatible service, e.g. local llama.cpp at `http://127.0.0.1:8080/v1`. The API key can be supplied via `OPENAI_API_KEY` (or `DEEPSEEK_API_KEY` when using the `deepseek` channel).

## CLI Usage

```bash
contextloom                          # interactive TUI
contextloom --model <id>             # start with a specific model preset
contextloom --resume                 # continue the most recent session
contextloom --memory-load <uuid>     # load a specific historical session
contextloom --memory-list            # list summaries of all historical sessions
contextloom --memory-delete <uuid>   # delete a specific session
contextloom --serve [--serve-port N] # headless service mode (started by the GUI launcher)
contextloom --acp [--acp-port 20250] # ACP service mode
contextloom --mcp-server             # MCP Server (stdio JSON-RPC; read-only exposure of retrieval/memory/skills)
contextloom --config <path>          # specify a config file
contextloom trace --tail 50          # view the JSONL execution trace
contextloom eval --file eval.yaml [--dry-run|--real] [--json out.json]   # offline evaluation
contextloom eval-retrieval --file retrieval.yaml [--json out.json]       # retrieval-quality evaluation
contextloom --version
```

### TUI Command Cheat Sheet (`/` enters command mode)

| Command | Description |
|---|---|
| `/mode chat\|task\|goal` | Switch working mode |
| `/goal [status\|abort\|list\|resume <id>]` | Goal loop management |
| `/model <name>` | Switch model preset (lists when given no argument) |
| `/effort <low\|medium\|high>` | Thinking depth (default max) |
| `/top_p` / `/temperature` | Sampling parameters |
| `/ctx` | Context usage/thresholds/compaction audit (`/ctx reset` resets the summary) |
| `/compress` | Manually compact the context (decays unreferenced blocks) |
| `/inject <id>` | Manually promote a context block into the anchor zone |
| `/threshold <n>` | Harness relevance-score threshold |
| `/overseer_threshold <n>` | Overseer pass threshold |
| `/emp [name [task]]` | Employee center: list employees / view profiles / assign tasks |
| `/split <task1>, <task2>` | Force parallel sub-agent dispatch |
| `/scene` | Scene selection |
| `/skills` / `/market [install <name>]` | Skill selection / skill marketplace |
| `/memory` / `/dream` / `/teach <content>` | Memory stats / auto-organize lessons / manual capture |
| `/sessions` / `/session <id>` | List / load historical sessions |
| `/checkpoints` | List multi-agent run checkpoints |
| `/sandbox [commit\|discard]` | Sandbox status / merged write-back / discard |
| `/vocab [list\|accept\|reject]` | Vocabulary-mapping proposal management |
| `/plugin list` | Plugin inventory |
| `/trace [N]` | The most recent N trace entries |
| `/config` | View the effective configuration |
| `/acp` | ACP service status |
| `/quit` | Graceful exit with persistence |

## Configuration

`config.yaml` (fully commented in [`config.example.yaml`](config.example.yaml)) defines:

- `models` — model presets (name/backend/endpoint/api_key/max_context/vision_model/supports_vision/send_effort, etc.).
- `gateway` — routing rules and key pools (unconfigured = behavior identical to a single model).
- `context` — the three compaction thresholds and keep_recent.
- `harness` — relevance threshold / decay factor / anchor-zone share.
- `overseer` — pass threshold / auto-plan nodes / test TEE / scratch-doc policy.
- `sandbox` / `tools` — shadow filesystem, network isolation, approval presets.
- `memory` / `project` / `plugins` — session storage, project root, plugin directories.
- `trace` — local JSONL + OTLP export endpoint.
- `channels` — webhook / cron triggers.
- `mcp` — external MCP server integration.
- `preview` — the PlantUML rendering service used by GUI preview.

Project-level data lives under the `.contextloom/` directory:

```
.contextloom/
├── settings.json     Runtime settings (model/effort/mode)
├── memory/sessions/  <uuid>.json + <uuid>.jsonl event logs
├── employees/        Employee profiles (including ONTOLOGY.md experience distillation)
├── scenes/           Scene presets
├── chains/           Toolchain definitions
├── gates/            Quality gates (enabled when the directory exists)
├── goals/            Goal loop state and checkpoints
├── checkpoints/      Multi-agent run checkpoints
├── harness/          Retrieval index persistence (index.json)
├── plugins/          Project-level plugins (merged with user-level)
├── skills/           Installed skills
├── trace/            JSONL trace (rotated daily)
└── sandbox/          Sandbox shadow filesystem
```

## Architecture

```
┌────────────┐  ┌──────────────┐  ┌─────────────┐
│  TUI       │  │  GUI (Wails) │  │ MCP / ACP   │
└─────┬──────┘  └──────┬───────┘  └──────┬──────┘
      └──────────┬─────┴──────────────────┘
          ┌──────▼───────┐
          │   runtime    │  orchestration, overseer, goal state machine, checkpoints
          └──────┬───────┘
   ┌─────────────┼──────────────┐
┌──▼───┐   ┌────▼─────┐   ┌────▼─────┐
│ ctxmgr│   │ executor │   │  memory  │
│ budget│   │ sub-agents│  │ JSONL logs│
└──┬────┘   └────┬─────┘   └──────────┘
   │        ┌────▼─────┐
   │        │  model   │  Provider gateway ([OI]-compatible / DeepSeek)
   └────────┤ gateway  │
            └──────────┘
         ┌──────────┐
         │ harness  │  hybrid retrieval (inverted + symbol + semantic), compacted-original recovery
         └──────────┘
```

## Documentation

- **This README** — features, architecture, install, build, configuration and usage.
- **[README.zh-CN.md](README.zh-CN.md)** — Chinese edition.
- **[docs/release/](docs/release/)** — release notes per version.
- **[AGENTS.md](AGENTS.md)** — constraints for AI-assisted development · **[CONTRIBUTING.md](CONTRIBUTING.md)** — contribution workflow.

For implementation-level detail, read the source under `internal/` (`runtime`, `ctxmgr`, `executor`, `gateway`, `memory`, `sandbox`, `observability`); each package carries its own doc comments.

## Development

```bash
gofmt -w .          # CI enforces the format check
go vet ./...
go test -race ./...
```

CI runs gofmt checks, `go vet`, builds, and tests on Linux/macOS/Windows (Go 1.22). For AI-collaboration development constraints see [AGENTS.md](AGENTS.md); for the contribution process see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
