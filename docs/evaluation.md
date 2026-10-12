# Observability, Evaluation & Audit

**Audience:** developers and operators who run ContextLoom in CI or production and need to *see* what the agent did, *measure* whether a change helped, and *prove* an output is real.

**Goal:** turn ContextLoom's "observable + evaluable + auditable" capabilities into an actionable guide — every field name and command below is taken verbatim from the source under `internal/` and `cmd/`.

---

## 1. Overview

ContextLoom layers its trust story into **three observability layers** and **two evaluation layers**. They share one property: all of them are zero-dependency, pure-Go, and persisted as plain JSON/JSONL inside the project, so they survive process restarts and can be committed next to the code.

```
        ┌──────────────────────────── Observability (what happened) ────────────────────────────┐
        │                                                                                        │
        │   JSONL Trace            File-op Audit              Quality Ledger                     │
        │   internal/trace         internal/audit             internal/selfcheck                 │
        │   .contextloom/trace/    .contextloom/audit/        quality_ledger.yaml                │
        │   every round / tool     every write / exec         every Fail defect signature        │
        │   call / duration / err  (rotated daily JSONL)      (persistent record of distrust)    │
        │                                                                                        │
        └───────────────────────────────────────────┬───────────────────────────────────────────┘
                                                     │  raw data feeds
        ┌────────────────────────────────────────────▼──────────────────────────────────────────┐
        │                             Evaluation (did it get better)                             │
        │                                                                                        │
        │   Agent-level eval                     Retrieval-quality eval                          │
        │   contextloom eval                     contextloom eval-retrieval                      │
        │   internal/eval (eval.go)              internal/eval (retrieval.go)                    │
        │   pass-rate over a task set            Recall@K / MRR over a query set                 │
        │                                                                                        │
        └────────────────────────────────────────────────────────────────────────────────────────┘
```

| Layer | Package | Artifact on disk | Answers |
|---|---|---|---|
| JSONL trace | `internal/trace` | `.contextloom/trace/trace-YYYYMMDD.jsonl` | What did each iteration/tool call do, how long, did it fail? |
| OTLP export | `internal/trace` (`exporter.go`) | HTTP POST to your collector | Can I ship the same records to Collector / Langfuse / Jaeger? |
| File-op audit | `internal/audit` | `.contextloom/audit/audit-YYYYMMDD.jsonl` | Which files were written / replaced / deleted, by which tool? |
| Quality ledger | `internal/selfcheck` (`ledger.go`) | `quality_ledger.yaml` | Which defects keep recurring across rounds? |
| Agent-level eval | `internal/eval` (`eval.go`) | your `eval.yaml` → JSON report | Did the agent pass my task set? |
| Retrieval eval | `internal/eval` (`retrieval.go`) | your `retrieval.yaml` → JSON report | Did retrieval quality (Recall@K / MRR) regress? |

---

## 2. JSONL Trace

### What it is

A lightweight, append-only JSONL log written by `internal/trace`. One record per agent iteration, tool call, goal round, or error. It is the raw data source for failure attribution across rounds and for detecting capability regressions after a change.

### Where it lives

```
.contextloom/trace/trace-YYYYMMDD.jsonl
```

- **Rotated daily** — the file name embeds the date (`trace-20060102.jsonl`, Go layout `20060102`). A new day opens a new file; writes always append.
- **Retention** — the recorder keeps the most recent **30** rotated files (`maxKeep = 30`, same as audit). Pruning runs at rotation time, so a long-running project never grows the trace directory unbounded.
- **Disabled = no side effects** — with `trace.enabled: false`, no directory or file is created and `Recent` returns `nil`.

### Record fields (`trace.Entry`)

| JSON field | Type | Notes |
|---|---|---|
| `ts` | RFC3339 timestamp | Record time. Auto-filled if zero. |
| `session` | string | Session ID, **first 8 chars** only. |
| `mode` | string | `chat` / `task` / `goal`. |
| `round` | int | Agent iteration index. |
| `event` | string | One of `agent_round` / `tool_call` / `goal_round` / `error`. |
| `tool` | string | Tool name (for `tool_call`). |
| `ms` | int64 | Duration in milliseconds. |
| `tokens` | int | Best-effort token estimate. |
| `err` | string | Error text, if any. |
| `detail` | string | Extra note (e.g. goal evaluation score). |

Empty fields are omitted (`omitempty`). Example line:

```json
{"ts":"2026-01-15T09:30:12.345Z","session":"a1b2c3d4","mode":"goal","round":3,"event":"tool_call","tool":"exec_command","ms":345}
{"ts":"2026-01-15T09:30:13.001Z","session":"a1b2c3d4","mode":"goal","round":3,"event":"error","err":"boom"}
```

### How to read it

**From the TUI:**

```
/trace        # most recent N (default) trace entries
/trace 100    # most recent 100
```

**From the CLI:**

```bash
contextloom trace --tail 50
contextloom trace --tail 50 --dir /path/to/project   # when not run from the project root
```

`--dir` defaults to the current working directory; the command reads `.contextloom/trace/` under it. Output is newest-first, one line per entry:

```
最近 3 条 trace（/path/to/project/.contextloom/trace/）:
  2026-01-15 09:30:13 [goal] error 轮#3 错误=boom
  2026-01-15 09:30:12 [goal] tool_call 轮#3 工具=exec_command 345ms
  ...
```

### Configuration

```yaml
trace:
  enabled: true
```

### Why it matters

When a multi-round run fails, the trace tells you *which* round, *which* tool, *how long*, and *whether it errored* — without a debugger. It is also the dataset you diff before/after a change to see if a "fix" caused a regression.

---

## 3. OTLP Export

### What it is

An optional sink on top of the JSONL recorder: every `trace.Entry` is also shipped, in batches, to any **OTLP/HTTP + JSON** receiver (OpenTelemetry Collector `/v1/logs`, Langfuse, Jaeger, …). The implementation (`internal/trace/exporter.go`) is a **zero-dependency, hand-written** OpenTelemetry `LogsData` mapping — `net/http` + stdlib only, so it cross-compiles to every platform.

### Semantics

- **Non-blocking** — `Enqueue` never blocks the agent. If the internal channel (capacity 128) is full, the entry is **dropped and counted**, never retried inline.
- **Batch + timer dual trigger** — a batch is flushed when it reaches `batchSize` records **or** when `interval` elapses, whichever comes first.
- **Failures are silent** — a non-2xx response or network error increments the `dropped` counter; the recorder is unaffected.
- **Flush on shutdown** — `Recorder.Close` flushes the exporter's remaining buffer so records are not lost on exit.

### Payload mapping

Records are wrapped as OTLP `LogsData` → `resourceLogs` → `scopeLogs` → `logRecords`:

| OTLP field | Value |
|---|---|
| resource attribute `service.name` | `contextloom` |
| scope `name` | `contextloom.trace` |
| `body` | the `event` value |
| `severityText` | `INFO` |
| `timeUnixNano` | `ts` as an int64 string (proto3 JSON) |
| attributes | `session`, `mode`, `round`, `tool`, `ms`, `tokens`, `err`, `detail` (only non-empty ones) |

### Configuration

```yaml
trace:
  enabled: true
  # Empty = local JSONL only. Set to your collector's /v1/logs endpoint to enable export.
  export_endpoint: "http://127.0.0.1:4318/v1/logs"
  # Extra HTTP headers (e.g. auth). YAML map.
  export_headers:
    Authorization: "Bearer <token>"
  # Batch size (flush when full) and timed-flush interval in seconds.
  export_batch: 32
  export_interval: 5
```

| Key | Default | Meaning |
|---|---|---|
| `export_endpoint` | `""` | OTLP/HTTP+JSON target; empty disables export. |
| `export_headers` | `{}` | Extra headers attached to every POST. |
| `export_batch` | `32` | Flush when this many records are buffered. |
| `export_interval` | `5` | Timed flush interval, seconds. |

Leave `export_endpoint` empty to keep local-only JSONL — behavior is then identical to running without an exporter.

---

## 4. Agent-Level Offline Evaluation — `contextloom eval`

### What it is

A zero-dependency, reproducible evaluation harness (`internal/eval/eval.go`). You declare a task set in `eval.yaml` (prompt + file/gold/command/regex checkers); the harness runs each task in a fresh temp workspace and reports a **pass rate**. It is designed to be a first-class citizen in CI, in the spirit of OpenHands/Aider "make evaluation reproducible".

### `eval.yaml` structure (`eval.Spec`)

```yaml
name: <string>            # report name; defaults to the file's base name
tasks:                    # required, non-empty
  - id: <string>          # optional; auto-filled as task-1, task-2, … when missing
    prompt: <string>      # the task given to the agent
    files: [<path>, ...]  # files copied from the spec's directory into the temp workspace
    gold_contains: [...]  # pass if the agent output contains ANY substring (case-insensitive)
    file_contains:        # pass if the named workspace file contains the substring (case-insensitive)
      - file: <path>
        substr: <string>
    check_regex: [...]    # ALL regexes must match the agent output (skipped in --dry-run)
    check: <shell cmd>    # run in the workspace; exit 0 = pass (skipped in --dry-run)
```

A task is valid when it has a `prompt` **or** at least one checker (`file_contains` / `check` / `check_regex`). Every `check_regex` must compile at load time — an invalid regex aborts with an error instead of panicking at run time.

**Pass logic** (real mode): `(gold hit or gold empty) AND (all file checks hit) AND (all regexes match)`, and only then, if `check` is non-empty, the command must exit 0.

### `--dry-run` vs `--real`

| Mode | Flag | Calls the model? | Runs `check` / `check_regex`? | Runs `file_contains`? |
|---|---|---|---|---|
| Dry-run (**default**) | `--dry-run` (default true) | No | No | Yes |
| Real | `--real` | Yes | Yes | Yes |

- `--dry-run` is the default: it validates parsing, file copying, and file checkers with **zero side effects** — no model calls, no shell commands, no regex execution. Safe to re-run in CI.
- `--real` calls the configured model backend for each task's prompt. Requires a working endpoint/API key.
- `--direct` uses a lightweight direct-call path (loads only config + model backend, skipping the full runtime/TUI/plugin chain) — useful for large benchmark sets (GSM8K / HumanEval / Terminal-Bench).
- `--model <preset>` overrides which model preset is used (defaults to the active model).

### Command

```bash
contextloom eval --file eval.yaml --dry-run
contextloom eval --file eval.yaml --real --json report.json
contextloom eval --file tasks.jsonl --direct --model local-model   # large benchmark, direct mode
```

### Report JSON (`eval.Report`)

```json
{
  "name": "go-basics",
  "tasks": [
    {"id": "add-func", "pass": true, "ms": 12, "output": "..."}
  ],
  "passed": 1,
  "total": 1,
  "pass_rate": 1.0,
  "dry_run": true
}
```

| Field | Meaning |
|---|---|
| `tasks[].id` / `pass` | Task id and verdict. |
| `tasks[].err` | Failure reason (`omitempty`). |
| `tasks[].ms` | Wall-clock duration. |
| `tasks[].output` | Agent output, truncated to 300 chars (`omitempty`). |
| `passed` / `total` / `pass_rate` | Aggregate. |
| `dry_run` | Whether the run was dry. |

### Complete runnable example

`eval.yaml`:

```yaml
name: go-basics
tasks:
  - id: add-func
    prompt: "Write a Go function add(a, b int) int that returns the sum."
    gold_contains: ["func add"]

  - id: create-main
    prompt: "Create main.go containing a main function."
    files: ["main.go"]          # copied from this directory into the temp workspace
    file_contains:
      - file: "main.go"
        substr: "func main"
    check: "test -f main.go"    # exit 0 = pass (real mode only)

  - id: reply-done
    prompt: "Reply with the single word DONE."
    check_regex: ["(?i)done"]   # every regex must match the output
```

```bash
# CI-safe parse + file-checker validation, no model:
contextloom eval --file eval.yaml --dry-run

# Real run against the configured model, emit a JSON report:
contextloom eval --file eval.yaml --real --json report.json
```

---

## 5. Retrieval-Quality Evaluation — `contextloom eval-retrieval`

### What it is

A pure-offline baseline for the Harness retrieval engine (`internal/eval/retrieval.go`). It indexes a fixed corpus, runs each query, and reports **Recall@K** and **MRR** — so retrieval improvements are driven by data, not guesswork. Zero API dependency; re-runnable in CI.

### `retrieval.yaml` structure (`eval.RetrievalSpec`)

```yaml
name: <string>
corpus:                    # the blocks to index
  - id: <string>           # block id (referenced by gold)
    content: <string>
    meta: {k: v}           # optional; reserved for future metadata-filtered eval
queries:                   # required, non-empty
  - query: <string>
    gold: [<id>, ...]      # the full set of relevant block ids
ks: [1, 3, 5]              # Recall@K list; defaults to [1, 3, 5, 10] when omitted
```

### How Recall@K and MRR are computed

For each query, the engine returns a ranked list of blocks. Let `gold` be the relevant-id set.

- **Ranks** — for each gold id, its 1-based position in the result (0 = not found).
- **Recall@K** — `(number of gold ids ranked in [1..K]) / len(gold)`, averaged over all queries. Reported for every `K` in `ks` as `"R@<k>"`.
- **MRR** — for each query, `1 / rank` of the **first** (best, i.e. smallest-rank) gold hit; 0 if none. Averaged over all queries.
- The per-query `recall_at_k` field in the details uses the **largest K**.

### Command

```bash
contextloom eval-retrieval --file retrieval.yaml
contextloom eval-retrieval --file retrieval.yaml --json retrieval.json
```

### Report JSON (`eval.RetrievalReport`)

```json
{
  "name": "retrieval-smoke",
  "queries": 3,
  "corpus": 4,
  "ks": [1, 3, 5],
  "recall_at_k": {"R@1": 1.0, "R@3": 1.0, "R@5": 1.0},
  "mrr": 1.0,
  "details": [
    {"query": "search cache LRU capacity", "ranks": [1], "recall_at_k": 1.0, "mrr": 1.0}
  ]
}
```

Text output (from `RenderText` plus the per-query detail lines the CLI appends):

```
检索评测 retrieval-smoke：语料 4 块 / 查询 3 条
  Recall@1 = 1.00
  Recall@3 = 1.00
  Recall@5 = 1.00
  MRR = 1.000
  search cache LRU …  R=1.00 MRR=1.00 gold排名=[1]
  检索缓存容量淘汰        R=1.00 MRR=1.00 gold排名=[1]
  倒排索引删除           R=1.00 MRR=1.00 gold排名=[1]
```

### Complete example — mixed EN/ZH corpus, cross-language synonym hit

This mirrors the engine's bidirectional EN↔ZH synonym table: a **Chinese query can hit an English block** and vice versa.

```yaml
name: cross-language-smoke
corpus:
  - id: zh-cache
    content: "搜索缓存使用 LRU 淘汰策略，容量上限 256 条，过期时间 2 秒"
  - id: en-lru
    content: "searchCache implements LRU eviction with capacity limit 256"
  - id: zh-index
    content: "倒排索引维护 token 到块的映射，支持快速检索与删除"
  - id: en-idf
    content: "IDF weighting downgrades common tokens like the and err"
queries:
  - query: "检索缓存容量淘汰"        # ZH query → zh-cache
    gold: [zh-cache]
  - query: "search cache LRU capacity"   # EN query → en-lru
    gold: [en-lru]
  - query: "倒排索引删除"            # ZH query → zh-index
    gold: [zh-index]
ks: [1, 3, 5]
```

```bash
contextloom eval-retrieval --file retrieval.yaml --json retrieval.json
```

A healthy baseline reports `R@1 = 1.00` and `MRR = 1.000` on this set. Drop either metric and you have a retrieval regression to chase.

---

## 6. File-Operation Audit & Quality Ledger

### 6.1 File-operation audit (`internal/audit`)

An append-only JSONL audit trail of **sensitive file/tool operations**, rotated daily — a lightweight landing of enterprise "audit trail" governance (authn is out of scope; auditing is not).

**Location:** `.contextloom/audit/audit-YYYYMMDD.jsonl` (daily rotation, keep the most recent **30** files).

**Recorded actions** (`audit.Action`): `write_file`, `replace_in_file`, `delete`, `exec_command`, `read_file`, `list_files`, and `tool_call` (catch-all).

**Record fields** (`audit.Entry`):

| JSON field | Type | Notes |
|---|---|---|
| `ts` | RFC3339 | Record time. |
| `session` | string | Session id. |
| `action` | string | One of the actions above. |
| `tool` | string | Tool name. |
| `path` | string | Target path. |
| `ok` | bool | Whether the operation succeeded. |
| `ms` | int64 | Duration. |
| `err` | string | Error text. |
| `detail` | string | Extra note. |

The auditor is injected at the tools layer; `nil` is safe (no audit). Records are written best-effort and never block the main flow.

### 6.2 Quality ledger (`quality_ledger.yaml`, `internal/selfcheck/ledger.go`)

Every time the quality gate returns **Fail**, the defect signature is recorded in `quality_ledger.yaml` — a **persistent record of distrust** that survives restarts. Accumulated signatures make the reviewer prioritize recurring problem classes on the next round.

```yaml
defect_history:
  - signature: "改完不跑测试"       # human-readable defect signature
    count: 3                        # times seen
    last_seen: "2026-01-15T09:30:00Z"   # RFC3339, may be empty
```

| Field | Meaning |
|---|---|
| `signature` | The defect signature string. |
| `count` | Cumulative occurrences. |
| `last_seen` | Last time it was seen (RFC3339). |

The ledger persists at goal scope alongside checkpoints. `TopSuspects(n)` returns the n most frequent signatures (ties broken by signature for determinism).

---

## 7. Evidence Chain (`internal/goalmgr/evidence.go`)

### Why

Verbal claims ("tests pass", "done") are **not** evidence. `VerifyGoalProofs` accepts only three **physical** channels, all verified by deterministic processes (filesystem / git / `go test`) — zero model involvement:

| `kind` | `target` | Verified by |
|---|---|---|
| `file` | relative path | `os.Stat` — the file exists and is **not** a directory |
| `diff` | relative path | appears in `git diff --name-only` (a real working-tree change) |
| `test` | package path (e.g. `./internal/x`) | `go test <target>` exits 0 (real run, with timeout; default 120s) |

### Proof structure (`types.GoalProof`)

```json
{
  "kind": "test",
  "target": "./internal/eval",
  "note": "retrieval regression suite"
}
```

| Field | Meaning |
|---|---|
| `kind` | `file` / `diff` / `test` (any other value → verification failure; unknown kinds are rejected, not waved through). |
| `target` | file=relative path; diff=relative path; test=package path. |
| `note` | free-text note; **not** used in the verdict. |

### Semantics

- A step is verified only when its `Status == done` and it carries `Proofs`.
- Each proof is checked independently; failures come back as readable `ProofFailure{StepID, Kind, Target, Reason}` entries.
- A `test` proof must actually run `go test` and exit 0 — the model saying "tests pass" does not count.
- Failures are converted into defect entries (`EvidenceDefects`) that enter the next round's fix list; a previously-done step whose proofs fail is demoted to pending.

---

## 8. Self-Check (`internal/selfcheck`)

A three-piece quality self-evolution system: **questioning has an owner** (static checker), **evidence must be measured** (test TEE), **nothing surfaces until it passes** (mandatory gate). All three are built on the standard library + `gopkg.in/yaml.v3` (no new `go.mod` deps) and are decoupled from runtime/goalmgr/executor/overseer.

### 8.1 Quality levels (`QualityLevel`)

| Level | String | Static check | Test TEE | Gate |
|---|---|---|---|---|
| `standard` (default) | `standard` | on | on | on |
| `fast` | `fast` | on | off | on |
| `none` | `none` | off | off | off (legacy behavior) |

Configured via `gate.yaml: level`. Empty/invalid strings fall back to `standard` (fail-safe: quality defaults to max).

### 8.2 Static checker (`static.go`)

Scans changed files and reports issues. `StaticReport.Pass` is true only when there are **zero high/medium** issues.

| Issue `kind` | Meaning |
|---|---|
| `residue` | leftover markers: `TODO` / `FIXME` / `XXX` / `HACK` / `panic("TODO")` / temp-file markers / debug prints |
| `unresolved` | unresolved marker `[?]` (severity **high**) |
| `unused` | a symbol declared this change but never seen used project-wide |

`Issue.Severity` ∈ `high` / `medium` / `low`. Fields: `file`, `line`, `kind`, `hint`, `severity`.

### 8.3 Test TEE (`tee.go`)

Re-runs tests to prevent self-deception. `Verify(TestTEEResult)` decides by artifact type:

| Artifact type | String | Pass condition |
|---|---|---|
| Go code | `go_code` | `compiled` **and** `test_passed` |
| Non-Go code | `non_go_code` | `test_passed` |
| Doc | `doc` | TEE not applicable → `needs_logic_check` |
| unknown/empty | — | conservatively **failed** |

`TestTEEResult` fields: `artifact_type`, `compiled`, `test_passed`, `test_count`, `output`, `sha`. Verdicts: `passed` / `failed` / `needs_logic_check`.

### 8.4 Gate (`gate.go`)

The gate has teeth. `Admit(QualityReport)` returns one of:

- **`pass`** — static check + TEE + score all pass.
- **`fail`** — any check fails; the decision forbids writing `[GOAL_DONE]` and injects a defect list (`DefectsFromReport`) into the next round.
- **`human_review`** — **3** consecutive non-converging rounds (`maxNonConverge = 3`) escalate to a human / a different reviewer.

Pass score defaults to **90**. Doc artifacts skip TEE and are judged by static check + score.

---

## 9. Related Documents

- [README.md](../README.md) — project overview, CLI usage, TUI command cheat sheet (English)
- [README.zh-CN.md](../README.zh-CN.md) — 项目总览（中文）
- [docs/architecture.md](architecture.md) — overall system architecture
- [docs/context-engine.md](context-engine.md) — context management and the harness_search retrieval engine (the thing `eval-retrieval` measures)
- [docs/toolchain.md](toolchain.md) — employees / scenes / toolchains / gates
- [docs/comparison.md](comparison.md) — feature comparison with other agent harnesses
- [config.example.yaml](../config.example.yaml) — full configuration reference (`trace`, `overseer`, `harness` sections)

---

## Key Takeaways

1. **Three observability layers, one format.** Trace, audit, and the quality ledger are all plain JSON/JSONL under `.contextloom/` — inspectable with `cat`/`jq`, committable, and restart-surviving.
2. **Two evaluation layers, both offline.** `contextloom eval` scores an agent task set; `contextloom eval-retrieval` scores retrieval with Recall@K / MRR. Neither needs a network for `--dry-run`.
3. **`--dry-run` is the CI default.** It validates parsing and file checkers with zero side effects; `--real` is the opt-in that calls the model.
4. **Evidence beats claims.** A `done` step is only trustworthy when its `file`/`diff`/`test` proofs pass a deterministic check — verbal "tests pass" is rejected.
5. **Nothing surfaces until it passes.** The gate forbids `[GOAL_DONE]` on failure, and every Fail signature lands in `quality_ledger.yaml` so recurring defects are visible across rounds.
