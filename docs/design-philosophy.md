# ContextLoom Design Philosophy

> Audience: engineers, integrators, and reviewers who want to understand **why** ContextLoom is built the way it is — not just which files do what. Every numeric constant and file path below is quoted from the current source tree.
>
> Related: [README.md](../README.md) · [README.zh-CN.md](../README.zh-CN.md) · [docs/architecture.md](architecture.md) · [docs/context-engine.md](context-engine.md) · [docs/plugins.md](plugins.md) · [docs/toolchain.md](toolchain.md)

---

## 0. What ContextLoom is, in one sentence

ContextLoom is a **coding-agent harness (a cockpit)** written in Go: it drives an LLM through a terminal (TUI), a native desktop window (GUI), or any MCP/ACP-compatible client, while a context engine keeps every request at the right information density.

The core bet is simple: **do not rely on the model's own intelligence to stay on task.** Treat the model's inherent limitations as *engineering* problems, and answer each one with a mechanism that is verifiable in code.

---

## 1. The five inherent limitations of the model

A large language model used directly for long, unattended coding work hits five problems. ContextLoom answers all five mechanically:

| # | Limitation | Plain-language description | ContextLoom's mechanistic answer |
|---|---|---|---|
| 1 | **Finite context window** | The model can only hold so much; long conversations push early content out. | Token-budget-driven **progressive layered compaction**; the dropped originals are **never lost** — they remain searchable via `harness_search`. |
| 2 | **Attention dilution** | Too much irrelevant material scatters attention; key facts get missed. | **Hybrid retrieval** selects only relevant blocks, and the **three-region sliding window + `KEY_REMIND`** places the most important material where the model attends best. |
| 3 | **Token cost** | Requests are billed by token; more context means more cost. | **Minimal-JSON prompts**, vocabulary compression, `singleflight` de-duplication, oversized-output spillover — "frugality" is a default, not an optional optimization. |
| 4 | **Output ≠ execution** | The model may claim "tests pass" when nothing ran. | **Overseer + the anti-cheating trio**: test-execution escrow (TEE), evidence-chain verification, and static verification. Failing output is never surfaced. |
| 5 | **Repetition / non-termination** | The same tool is called with the same arguments, forever. | **Engineering guards**: `detectLoop` (same tool + args 3×), no-tool-round detection, token red-line cutoff, and manual abort. |

Everything else in this document is a consequence of taking those five answers seriously.

---

## 2. Three core design principles

### 2.1 Principle 1 — "Frugality" is a design principle, not an afterthought

Token reduction is a first-class goal, applied at every layer:

- **Minimal-JSON prompts** (`internal/preprocessor`): user input passes through a semantic mapper with 60+ keyword rules (`fix/bug/crash → BUG_FIX`, `test/verify → TEST`, …) plus a vocabulary table (`authentication → AUTH`, `null_pointer → NPE`), turning a sentence into a compact directive.
- **Vocabulary learning** (`propose_vocab`, `.contextloom/vocab.json`): the model proposes abbreviations for project-specific terms; **the proposal only takes effect after the user confirms it** (`/vocab accept`). Model proposes, human approves.
- **`singleflight`** (`internal/tools/task.go`): concurrent identical sub-task spawns execute once and share the result.
- **Oversized-output spillover**: tool output beyond the threshold is written to a temp file, leaving only a preview + reference in the message (the model can `read_file` the full text).
- **Rolling summary** instead of raw history, and **overflow compression** of low-scoring blocks in retrieval.

### 2.2 Principle 2 — "Backstop" rather than "prevent"

ContextLoom does **not** try to stop the model from making mistakes — that would break unattended automation. Instead it guarantees that mistakes are **discoverable** and **auditable**:

- Discoverable: the **Overseer** scores every deliverable; the **TEE** re-runs claimed tests; the **evidence chain** requires physical proof.
- Auditable: **compaction audit** (`/ctx`, last 20 entries), **checkpoints** (per-round goal state), **JSONL trace** (daily rotation), and the **quality ledger** (`quality_ledger.yaml`).
- Recoverable: anything compaction removes is written into the retrieval index — **compaction ≠ permanent loss**.

This is engineering-grade trust management: trade a safety net for automation.

### 2.3 Principle 3 — "The model is the executor; the system is the organizer"

ContextLoom borrows from organizational management, not from prompt engineering:

- **Harness** is the reference library (retrieval).
- **ctxmgr** is the budget office (compaction).
- **Overseer** is the quality inspector (scoring).
- **`spawn_task`** is task delegation (sub-agents).
- **The message bus** is the intercom.
- **Goal mode** is the objective-management loop.
- **Employees** are staffed roles with stable identities.

The model supplies intelligence; ContextLoom supplies **reliability**.

---

## 3. Mechanism by mechanism

Each subsection follows the same shape: **what it does (plain language) → why it matters (design point) → key parameters → how to configure.**

### 3.1 Minimal-JSON prompts and vocabulary learning

**What it does.** Instead of a multi-thousand-word natural-language system prompt, ContextLoom communicates with the model in compact JSON. A semantic mapper compresses the request into a short directive; a vocabulary table replaces project terms with abbreviations.

**Design point.** This is not "write a shorter prompt" — it is **switching the protocol** from chat to structured communication. Combined with a vocabulary that the model proposes and the user approves, the compression adapts to the project over time.

**Key parameters.** 60+ intent keyword rules; built-in vocabulary entries (NPE / AUTH / REFACTOR, …) seeded on first run; proposals written to `.contextloom/vocab.json` only after confirmation.

**How to configure.** Manage proposals via `/vocab [list|accept|reject]` in the TUI; the tool `propose_vocab` is how the model submits them.

---

### 3.2 Harness — the context engine (`internal/harness`)

**What it does.** The Harness is the model's reference library. It selects only the most relevant blocks, scores them, and arranges them in the window where attention is strongest. It is also the destination for everything compaction removes, so nothing is truly lost.

**Design point — a staged pipeline, not a single index.** A lone inverted index either misses paraphrases or drowns in candidates. ContextLoom chains several stages, each narrowing or re-scoring the previous one (`Engine.Search` in `internal/harness/harness.go`):

```
query + metadata
   │
   ▼
[1] LRU cache lookup        key = "sid:<sid>|<query>"   (256 cap, 2s TTL)
   │ miss
   ▼
[2] HybridIndex.Query       inverted-index candidates, session-filtered
   │
   ▼
[3] SymbolIndex boost       +30 for blocks whose indexed symbols appear in query
   │
   ▼
[4] RelevanceScorer         base keyword score (threshold from score_threshold)
   │
   ▼
[5] semanticRerank          top-N (30) candidates:
   │                        sem = queryCoverage × 100
   │                        cos = cosine(FNV-1a embedding) × 100
   │                        new = kw + (sem×0.5 + cos×0.5) × 0.5   (only raises)
   ▼
[6] overflow compression    if > 50 blocks: blocks scoring < 60 truncated to
   │                        first 100 runes + "...(压缩)"
   ▼
[7] sliding window          sort by score → anchor / core / reference
   │
   ▼
[8] cache.set               final windowed layout cached
```

**The four retrieval signals.**

- **Inverted index** — keyword full-text search.
- **Symbol index** (`internal/harness/symbols.go`) — regex extraction of function/type/constant/class names across Go/Python/JS/TS/Rust builds a symbol → block map; a query hitting a symbol gets `symbolScoreBoost = 30`. This is a lightweight, zero-dependency stand-in for "repo map" symbol awareness (no tree-sitter).
- **Semantic rerank** (`internal/harness/semantic.go`) — a 0.5/0.5 blend of **query coverage** and **hash-embedding cosine**:
  - *Query coverage* = |query features ∩ text features| / |query features|. CJK becomes 2-grams; ASCII words are lowercased with 3-gram prefixes; a built-in **Chinese↔English coding-term synonym table** (hundreds of entries, e.g. `登录→login/signin`, `超时→timeout`, `缓存→cache`) is applied **bidirectionally**, so a Chinese query like "修登录超时" reaches blocks containing `login`/`timeout`.
  - *Hash-embedding cosine* (`internal/harness/embedding.go`) — query and text are embedded into **256-dim** L2-normalized vectors via the **FNV-1a hashing trick**; cosine similarity is robust to feature distribution and length, complementing coverage.
  - Blend formula: `final = keyword + (coverage×100×0.5 + cosine×100×0.5) × 0.5`, applied only when it raises the keyword score, capped at 100 — strong keyword hits are never diluted.
  - ⚠️ **No ONNX, no FAISS, no external embedding model.** The semantic layer is entirely zero-dependency and pure-stdlib.
- **Time decay** — `Engine.ApplyDecay()` multiplies every block's score by `decay_factor` (default **0.9**) each cycle, so untouched blocks sink and refreshed ones stay afloat.

**The three-region window and `KEY_REMIND`** (`internal/harness/window.go`). Blocks are sorted by score and partitioned into:

- **anchor** — `max(1, total × anchor_ratio)` top blocks;
- **core** — the bulk;
- **reference** — `max(1, (1 − anchor_ratio) × 0.2)` lowest blocks.

Then two first-and-last repetitions fight attention decay:

- Blocks scoring **≥ 95** get a `KEY_REMIND:` copy (first 100 runes) prepended to the **reference** region — the "write the key formula twice" trick.
- When the result set exceeds **10** blocks, the top anchor block (score **≥ 85**) is appended once more at the very **end**.

**Session isolation.** Every block carries an optional `SessionID`. A query returns the union of *project-level shared blocks* (no `SessionID`) and *the requesting session's private blocks*. Compressed-history blocks are session-private by construction, and the cache key embeds the session (`"sid:<sid>|<query>"`), so concurrent sessions never cross-contaminate. Ownership resolution priority: ctx-bound session → runtime global session → none.

**Persistence and the "compressed → searchable" invariant.** Index changes are tracked by `changeSeq` vs `savedSeq`; a background goroutine polls every **30 seconds** and saves when needed. `SaveIndex` writes `.contextloom/harness/index.json` **atomically** (write-then-rename). Crucially, eviction callbacks fire **while the history lock is held**, so they only *append* to an `evictPending` buffer (O(1)); the actual indexing happens in a bulk `flushEvictPending()` — and `Runtime.HarnessSearch` flushes **before searching**, guaranteeing a just-compressed block is always searchable.

**Key parameters.**

| Parameter | Default | Source |
|---|---|---|
| `symbolScoreBoost` | 30 | `harness.go` |
| `SemanticRerankTopN` | 30 | `harness.Config` |
| `OverflowCompressBlocks` / `Score` / `Keep` | 50 / 60 / 100 runes | `harness.Config` |
| embedding dims | 256 | `embedding.go` |
| cache cap / TTL | 256 / 2s | `harness.Config` |
| `decay_factor` | 0.9 | `config.example.yaml` |
| `anchor_ratio` | 0.2 | `config.example.yaml` |
| auto-save interval | 30s | `startHarnessAutoSave` |

**How to configure.** `harness:` block (`score_threshold`, `decay_factor`, `anchor_ratio`); engine-level tunables hot-apply via `Engine.ApplyConfig()`. Manual control: `/compress` (decay + force compaction) and `/inject <id>` (promote a block to the anchor region).

---

### 3.3 ctxmgr — progressive, auditable compaction (`internal/ctxmgr`)

**What it does.** Keeps every request inside the model's window without cliff-edge amnesia.

**Design point — one decision maker, three thresholds, five steps.** Earlier designs truncated by message count, which either cut context too early or overflowed the window (API 400). The fix: a single `TokenBudget` (`internal/ctxmgr/budget.go`) owns every decision, and Runtime / Agent / Backend paths all consult it.

| Field | Default | Meaning |
|---|---|---|
| `CompactRatio` | **0.70** | usage above this → prefix-truncate old messages |
| `HardRatio` | **0.85** | usage above this → drop oldest + rolling summary |
| `SafetyRatio` | **0.92** | pre-send red line (auto-compact / forced finish) |
| `KeepRecent` | **8** | the last N messages are never *dropped* |

`Decide(current, msgCount)` returns `ActionNone` / `ActionCompact` / `ActionHardCompact`. `ApplyConfig()` enforces monotonicity automatically (`compact < hard < safety`), repairing contradictory input to the midpoint of the remaining range.

**The five-step degradation ladder** (`CompactToBudgetWithEvict`). `role=system` and `[历史摘要]` messages are **protected** at every step:

1. **Prefix truncation** — old messages cut to `len/3` runes, clamped to **[60, 400]**.
2. **Drop oldest** — while over `HardRatio` and `len > KeepRecent`, drop the oldest non-protected message; its head **80 runes** feed the rolling summary. Eviction is recorded **once per original message**.
3. **Second-pass truncation** — if still over `SafetyRatio`, all but the newest message are re-cut to **300 runes**.
4. **Halving fallback** — repeatedly halve the largest compressible field (content, a tool-call's `arguments` via `truncateArgumentsJSON` which keeps JSON valid, or `ReasoningContent`). Floors: **100 runes** non-system, **200 runes** system (last resort).
5. **Orphan cleanup** — `StripOrphanToolMessages()` removes `tool` messages whose assistant call was dropped, and strips dangling `tool_calls` entries whose tool response was dropped — both would trigger an OpenAI-compatible API 400.

**Three traces of every compaction.**

- **Rolling summary** — each dropped message contributes its first 80 runes (role-labelled), joined with `；`, capped at **1200 runes**, wrapped as a `[历史摘要]` message. It is session-scoped (cleared on new session, restored on load, preserved across hot config reloads).
- **Audit log** — every action appends a line (last **20** entries); `/ctx` shows usage, thresholds, keep-recent, the summary preview (first 200 runes), and the audit lines.
- **Retrieval-index block** — the `EvictFunc` wraps each original as a `ContextBlock`: `ID = compressed:<unixnano>:<seq>`, `Content = [压缩原文 <role>] ` + the complete original, `Score = 50`, `SessionID = owning session`. It is searchable via `harness_search`.

**Multimodal and thinking-message correctness.**

- **Images**: each image part costs a fixed **85 + 384 = 469 tokens** (matching DeepSeek's documented ~800×800 scaling → ≤ 384 tokens per image, independent of base64 length). Counting the base64 as text would estimate a 5 MB image at ~1.67 M tokens and instantly "exhaust" the budget. In compaction, images degrade to a placeholder (`ImageOmittedPlaceholder`); snapshots strip data URIs (`StripImageDataURI`) so `session.json` never balloons.
- **Thinking traces** (`ReasoningContent`): fully counted by `MessageTokens()`, so the ctx indicator reflects reality. `keepRecent` protects messages from being *dropped*, never from being *truncated* — a single giant message inside the last 8 still gets truncated.

**Token estimation** (`MessageTokens`) covers the **full** structure: 8 tokens per message envelope, 6 per tool call, tool-call `arguments` JSON, `ReasoningContent`, `Name` / `ToolCallID`, and images. Undercounting here is the root cause of "ctx% frozen / API 400" bug classes.

**How to configure.** `context:` block:

```yaml
context:
  compact_ratio: 0.70
  hard_ratio: 0.85
  safety_ratio: 0.92
  keep_recent: 8
```

`MaxTokens` comes from the active model preset's `max_context`. Changing `context.*` or `model.max_context` hot-reloads the budget while preserving the rolling summary.

---

### 3.4 Overseer and the anti-cheating trio (`internal/overseer`)

**What it does.** In `task` mode the system spawns an independent **Overseer** agent that holds the original goal and scores each sub-agent's output.

**Design point — verify, don't trust.**

- **Four-dimension weighted scoring** (`scoring.go`): completeness **40%** + test coverage **30%** + code quality **20%** + no-side-effect **10%**; pass threshold default **90/100**. Non-code tasks (deleting a file, editing config) are not required to have tests — otherwise they'd be unfairly failed.
- **The anti-cheating trio**:
  - **Test-execution escrow (TEE)** (`anti_cheat.go`): when a run claims "tests passed", the sandbox re-executes and compares claimed vs actual pass/fail counts. Similarity **< 0.8** → judged untrustworthy.
  - **Evidence-chain verification**: outputs must cite physical evidence (file paths, test-run results); verbal claims are invalid.
  - **Static verification**: every round of the goal loop ends with mandatory static verification + TEE; failure means nothing is surfaced.
- **Node tree**: each sub-task becomes a node (`pending → running → passed/failed`) recording attempt counts.
- **Purge on cheat detection** (`overseer.go`): when cheating is detected, the agent's previous checkpoints are purged to prevent process contamination.
- **Three quality levels** (`gate.yaml: level`): `standard` (all on) / `fast` (static checks only) / `none` (off).
- **Quality ledger** (`quality_ledger.yaml`): every Fail verdict writes its defect signature — a persistent record of distrust that survives restarts.

**How to configure.** `overseer:` block (`pass_threshold`, `node_auto_plan`, `test_tee`, `temp_doc_policy`); `/overseer_threshold <n>` in the TUI.

---

### 3.5 Multi-agent collaboration

**What it does.** The main agent may call `spawn_task` to split work into sub-agents; the user may force a split with `/split`.

**Design point — the model decides *whether* to split; the system provides the *rules*.** ContextLoom supplies interfaces and norms, not a hard-wired pipeline:

| Mechanism | Plain language | Implementation |
|---|---|---|
| **Entry contract** | Each sub-agent receives an onboarding manual: working directory + project tree + harness results + parent context. | `ContextManifest` / `buildMessagesWithManifest` |
| **`singleflight`** | Concurrent identical requests execute once, sharing the result. | `internal/tools/task.go` |
| **Anti-recursion** | Sub-agents' tool tables have `spawn_task` removed. | `CloneWithout("spawn_task", …)` |
| **Message bus** | Agents talk via `send_message(to: main/agentID/broadcast)`; each loop drains its inbox. | `internal/executor/messagebus.go` |
| **Activity-based timeout** | A sub-agent is never cut off while it keeps producing output; only 10 minutes of silence terminates it. | `spawn_task` activity monitor |
| **Multi-batch** | After one batch finishes, the main agent may spawn a fresh batch (dynamic batch nudge). | `batchNo / batchNudge` |
| **Tool execution policy** | Read-only tools run in parallel; write tools run serially. | `executeToolsConcurrent` |

**Design point.** Sub-tasks must produce **verifiable artifacts** (files/tests/reports); queries, analyses, or evaluations with no artifact are executed by the main agent and cannot be delegated.

---

### 3.6 Goal mode — a self-improving loop (`internal/goalmgr`)

**What it does.** `task` mode runs once; `goal` mode loops across rounds until the goal is met, with no user intervention required. Since v2.4 it is a **self-improvement loop** — not "running in circles" but "climbing".

**Design point — six mechanisms** (state machine in `goalmgr.go`):

```
you state a goal
  → [PLAN round]     model emits a step list + acceptance criteria ([GOAL_PLAN])
  → [EXECUTE round]  model executes → overseer scores (protocol tags stripped first)
  → failed? auto-generate a 【defect list】 → fix one by one, tick off each
  → 2 rounds with no real progress? force a 【strategy change】
  → all steps done + criteria met? → append a 【polish round】 (quality hardening)
  → polish done → [GOAL_POLISH_DONE] finalize
  → each round's lessons written to the 【experience archive】
```

1. **Reflect every round** — `[GOAL_REFLECT]` (what was done / problems / next), read at the start of the next round.
2. **Rework with a list** — the overseer produces a concrete defect list, not a vague score; the next round fixes each and reports `[GOAL_DEFECTS]`.
3. **Definition of Done** — a goal starts by generating hard acceptance criteria; "all steps done + all criteria pass" is required.
4. **Change strategy on stall** — the same defect unfixed after 2 rounds forces a strategy change (split / retrieve / ask for clarification).
5. **Polish round** — after passing, one more hardening round (boundaries / tests / dead code / docs / side effects).
6. **Lesson capture** — `[GOAL_LESSON]` is reused across rounds.

**Supporting design.** Session isolation (`SessionID`) so multiple sessions hold independent goals; **anti self-report** (`stripGoalProtocol` strips all protocol tags before scoring, so writing "added tests" in the reflection doesn't count); **checkpoints** written every round (`.contextloom/goals/`), resumable via `/goal resume <id>`; **dual budget** (round cap + token budget, default `MaxContext × 2`); **step evidence-chain verification** (a `done` step whose proofs fail is demoted to `pending` and a defect appended).

**How to configure.** `/goal [status|abort|list|resume <id>]`, `/mode goal`.

---

### 3.7 Memory — plain files, zero locks (`internal/memory`)

**What it does.** A session is an **append-only JSONL event log** (`sessions/<uuid>.jsonl`) plus a small JSON metadata file; compaction snapshots live inside the metadata. Loading a session replays events (or restores the snapshot + increments).

**Design point — why not a database.** An earlier design used an embedded key-value store that depended on a **file lock**, which meant a second instance on the same project directory would time out. Switching to plain-file scanning removed the lock: **multiple instances on the same project are safe**. Session titles auto-derive from the first user message (first 50 chars); the list is sorted by recent activity with previews; the oldest sessions are evicted past the cap.

**Key parameters.** `memory.directory` (`.contextloom/memory`), `auto_title`, `max_sessions` (1000). Long-term lessons live in `lessons.md` (`/teach` manual, `/dream` auto-organize).

---

### 3.8 Employees and automatic experience distillation (`internal/orchestrator`)

**What it does.** An employee is a **persistent role profile** (`.contextloom/employees/<name>/employee.yaml`): persona, expertise, workflow, constraints, experience, output format, model binding, tool allow/deny lists, approval preset, skills. Six ship built-in: `coder / tester / reviewer / product / writer / translator`.

**Design point — from "temp worker" to "staffed role".** A model that self-declares a role forgets it next turn. An employee is written into a profile: identity, model, and tool permissions are stable. `spawn_task(directive=<employee-name>)` assembles a sub-agent per the profile.

**Deep ontology and distillation.** Beyond a few prompt words, a profile carries a **distillable capability library**: `expertise`, `workflow`, `constraints`, `experience`, `output_format`, plus an `ontology` text / `ontology_file` (`workspace/ONTOLOGY.md`) and a `workspace` corpus directory. `DeepProfile()` compiles these into one injectable capability statement.

**Automatic experience distillation** (a signature feature, `internal/runtime/distill.go`). When a subtask completes and matches an employee, the model distills the output into experience entries (golden rules / pitfalls / style), deduplicated and appended to that employee's `workspace/ONTOLOGY.md` under `## 经验蒸馏（自动沉淀）`. The next assignment injects them automatically — **employees get better with use, and distillation is the agent's job, not the user's homework**. Non-blocking (background goroutine), idempotent (SHA-256 content hash), capacity-limited (**≤ 1 KB growth**, ≤ 4 lines, ≤ 300 bytes/line), and silently degrades on failure.

**Custom model selection with multi-source combination.** `model.preset` binds a model preset directly; `model.fallbacks` declares a cross-source fallback chain (local + remote mixed). Priority: `spawn_task model` param > employee `model.preset`+`fallbacks` > gateway rules > default model.

**How to configure.** `.contextloom/employees/<name>/employee.yaml`; `/emp` in the TUI.

---

### 3.9 Plugins — scenes, toolchains, gates, manifest plugins (`internal/plugin`, `internal/scenes`, `internal/toolchain`)

**What it does.** Everything is a declarative YAML plugin, discovered and merged from two levels: user-level (`~/.contextloom/plugins/`) and project-level (`.contextloom/plugins/`); project-level wins on name conflicts.

- **Scenes** (`internal/scenes`) — keyword hits automatically take over the conversation (e.g. "write my weekly report" → the weekly scene), bundling team orchestration/models/toolchains/gates; with no hit, behavior is identical to bare chat.
- **Toolchains** (`internal/toolchain`) — declarative **DAG** pipelines with three node types: `tool` / `llm` / `ref`, plus `depends_on` dependency orchestration, the `$ref` artifact protocol, `{{param.*}}` parameter injection, and per-step retry. Static validation covers cycle detection, missing dependencies, unknown step types, duplicate ids, and unknown tool names. Built-in: `bug-fix-chain`, `code-review-chain`, `code-test-review-chain`, `pr-review-chain`, `weekly-report-chain`.
- **Gates** — enabled whenever `.contextloom/gates/` exists; define pass thresholds and evidence requirements.
- **Manifest plugins** — `stages` (pre/during/post) + `hooks` (message/route) + custom script tools (with timeouts and sandboxing). See [`examples/plugins/`](../examples/plugins/).

**Design point.** The model can invoke a whole toolchain by name (`run_toolchain`) as easily as a single tool; the user gets the full standard process as output.

---

### 3.10 Tool sandbox (`internal/sandbox`, `internal/tools`)

**What it does.** Isolation for risky operations.

**Design point — a zero-dependency landing of container semantics.**

- **Shadow filesystem**: all writes are redirected to `.contextloom/sandbox/` (read-mirror first, listings merged); `/sandbox commit` performs a two-phase merged write-back, `/sandbox discard` throws everything away.
- **Forced network isolation** (`internal/sandbox/network.go`): within a sandboxed session, `HTTP(S)_PROXY` / `ALL_PROXY` are forced to a **dead port** `http://127.0.0.1:9` (connections fail immediately), `NO_PROXY` is deleted to prevent bypass, and `CONTEXTLOOM_SANDBOX_NET=deny` is set — a zero-dependency `--network=none`.
- **Resource limits**: per-run memory cap, CPU limit, exec timeout.
- **Approval presets**: `standard` (confirm sensitive operations) / `permissive` (fully automatic) / `strict` (confirm everything); task/goal automation modes auto-approve.
- **File-operation audit** (`internal/audit`): JSONL rotated daily.

---

### 3.11 Model gateway (`internal/model`, `internal/modelgateway`)

**What it does.** Routes requests across multiple model providers.

**Design point — from a single pot to a power grid.** A single model is a single pot: when it breaks, everything stops, and every task costs the same.

- **Rule-based routing**: `role=reviewer → strong-model`, `task=vision → vision-model`, `budget<=low → cheap-model`; comma-separated predicates are intersected.
- **Multi-key pooling** (`pool.go`): key rotation, per-pool concurrency caps, exponential backoff retry.
- **Circuit-breaker degradation**: consecutive failures trip the breaker and auto-switch pools; the switch is visible to you.
- **Provider backends**: any OpenAI-compatible endpoint (llama.cpp, ollama, vLLM, local, cloud); the optional `deepseek` channel additionally sends `reasoning_effort`.
- **Hot model-preset switching** (`/model <name>`), automatic switching to `vision_model` for image input.

**How to configure.** `gateway:` block (rules + pools); unconfigured = behavior identical to a single model.

---

### 3.12 ACP + MCP — both directions

**What it does.** Opens the system to the outside world.

- **MCP Client** (`internal/mcpclient`): consumes external MCP servers; their tools register dynamically as `mcp.<server>.<tool>`.
- **MCP Server** (`internal/mcpserver`): exposes Harness retrieval / memory / skills **read-only** over stdio JSON-RPC, so tools like Claude Code or Cursor can drive ContextLoom.
- **ACP service** (`internal/acp`): `--serve` headless mode (the GUI launcher starts it for you).

**Design point.** "Server first, client second" — open the core assets via a standard protocol, then plug into the external ecosystem. Hand-written JSON-RPC 2.0, no MCP SDK dependency.

---

### 3.13 TUI cockpit and GUI desktop

**What it does.** Two shells over one runtime.

- **TUI** (`internal/tui`, Bubbletea, three-pane): collapsible thinking blocks (Space to expand; the chain-of-thought is not written back to history), a live sub-task panel, a status bar showing `ctx 42% (12k/131k)`. `Ctrl+J` is the only distinguishable newline key in the terminal protocol — which is why the README documents it.
- **GUI** (`cmd/contextloom-gui`, Wails/WebView): a project manager, multi-terminal (xterm.js + PTY), a dual-pane Markdown editor with PlantUML rendering, a schema-driven config panel, drag-resizable layout, and observability (folded thinking blocks, tool-call cards, ctx usage, agent activity, goal defect list). It launches the `contextloom` backend from its own directory and shares the same `.contextloom` data.

**Design point.** GUI and TUI have **zero feature divergence** — the GUI is a thin WS wrapper over the same runtime.

---

### 3.14 Vision mode and the three-layer capability gate (`internal/vision`)

**What it does.** Write an image path (e.g. `./设计图.png`) in the conversation; ContextLoom detects the extension, reads the file, base64-encodes it, and sends an OpenAI-compatible `image_url` multimodal message — **no file upload**.

**Design point — a three-layer gate** to avoid 400 errors against text-only APIs:

1. **Config layer**: `model.supports_vision` (default false — DeepSeek's official API is text-only today).
2. **Runtime layer**: when unsupported, history stays text-only, the agent clears images and emits a degradation notice with the reason and config guidance.
3. **Model layer**: before sending, all `image_url` parts are stripped; the 400-retry path strips them too — **no multimodal message escapes on any path**.

**Key details.** `vision_model` switches to a vision model for image rounds, then restores the main model. The JSONL event log stores only a `[图片: <path>]` marker (never base64) and rebuilds images on reload.

---

### 3.15 Tool cautiousness — warn, don't forbid (`internal/tools`)

**What it does.** A `CautiousAnalyzer` warns before sensitive operations; it does not forbid them.

**Design point — the safety decision stays with the user.** 40+ sensitive-path rules and 50+ dangerous-command rules, graded from notify / standard confirm / strong confirm / **hard refuse**. Command matching uses word-boundary handling (a trailing-space pattern like `"sudo "` won't match `sudoku`). Three approval presets: `standard` / `permissive` / `strict`. "Always allow" memory means repeated operations on a path stop prompting.

---

### 3.16 Engineering guards — failure-driven (`internal/executor`, `internal/runtime`)

Every guard exists because a real problem was observed:

| Guard | Problem it answers |
|---|---|
| **Loop detection** (`detectLoop`) | Same tool + same args 3× consecutively → treated as a loop, model nudged to change approach. |
| **Iteration soft nudge** | At a round threshold, a gentle "summarize progress" hint (not a forced wrap-up); termination is bounded by the token red line + user interrupt. |
| **Orphan tool-message cleanup** | After compaction, `tool` messages whose call was dropped cause API 400 — cleaned by a pending set. |
| **Message self-repair** | Empty assistant messages deleted; missing tool responses get a placeholder. |
| **400 self-repair** | On API 400, clean illegal messages + compact length, then retry once. |
| **Retry backoff + jitter** | Network errors retried with exponential backoff `1s × 2ⁿ` plus 0–1s jitter. |
| **`normalizeSystemFirst`** | Backends like llama.cpp require system messages at the front; all are normalized before sending. |
| **Oversized-output spillover** | Tool output > 128 KB spilled to a temp file, preview + reference kept. |
| **Chinese bigram tokenization** | Single-character CJK matching is too diffuse; the index uses unigram + bigram. |
| **effort remapping** | `low/medium/high → high`; `xhigh/max → max` (avoid unsupported tiers). |
| **Config lookup chain** | `--config` > `CONTEXTLOOM_CONFIG` > binary's real directory (symlink-resolved) > exe dir > CWD > `~/.contextloom/config.yaml` > built-in default. |

---

## 4. Design-features cheat sheet

| Feature | One-line plain language | Design point |
|---|---|---|
| Minimal-JSON prompts | Talk to the AI in "telegram code" | Save 70–90% tokens; semantic mapper + vocabulary |
| Harness context engine | The AI's librarian | Inverted + symbol + semantic + three-region window + `KEY_REMIND` |
| ctxmgr budget management | The AI's memory bank | 0.70/0.85/0.92, keep 8, five-step degradation, rolling summary, retrievable originals, auditable |
| Overseer | The quality inspector | 4-dim scoring (40/30/20/10) + TEE anti-cheat + node tree |
| Multi-agent collaboration | Task splitting + intercom | Model-decided orchestration + message bus + `singleflight` + anti-recursion + activity timeout |
| Goal mode | Loop across rounds until done | State machine + continuation + checkpoints + dual budget + session isolation + self-improvement |
| Vocabulary learning | Abbreviations proposed by the model | Model proposes + human approves |
| Plain-file memory | Storage without a database | Zero locks, zero dependencies, multi-instance safe |
| Plugin sandbox | Extensions in a fenced playground | Dual-level dirs + lifecycle + subprocess isolation |
| ACP + MCP | Full-duplex inward / open outward | Standard protocols, hand-written JSON-RPC |
| TUI cockpit | A visual AI work-state panel | Folded thinking + sub-task panel + ctx usage |
| GUI desktop | Native window + webview | Wails shell + project manager + xterm.js + dual-pane editor |
| Vision mode | Path recognition, on-demand multimodal | Extension detection + `image_url` + three-layer gate |
| Cautiousness warnings | Warn, don't forbid | Three approval presets + sensitive-rule library |
| Engineering guards | Failure-driven safety nets | Loop detection / message self-repair / 400 self-repair / retry backoff / config chain |

---

## 5. Related documents

- [README.md](../README.md) — project overview (English)
- [README.zh-CN.md](../README.zh-CN.md) — 项目总览（中文）
- [docs/architecture.md](architecture.md) — overall system architecture
- [docs/context-engine.md](context-engine.md) — deep dive on compaction and `harness_search`
- [docs/plugins.md](plugins.md) — plugin system
- [docs/toolchain.md](toolchain.md) — employees, scenes, toolchains, gates
- [config.example.yaml](../config.example.yaml) — full configuration reference

---

## Key takeaways

- **One decision maker, three thresholds, five steps.** `TokenBudget` (0.70 / 0.85 / 0.92, keep 8) drives a progressive degradation ladder whose monotonicity is repaired automatically; every compaction leaves three traces (rolling summary, audit log, retrievable original).
- **Retrieval is a staged pipeline with zero heavy dependencies.** The semantic layer (coverage 0.5 + cosine 0.5, bidirectional zh↔en synonyms, 256-dim FNV hashing) bridges Chinese queries to English code without any external embedding service — no ONNX, no FAISS.
- **Reliability is engineered, not assumed.** The Overseer + anti-cheating trio, session isolation, and the "compressed → searchable" invariant make unattended multi-session operation safe; plain-file memory with no global lock makes multi-instance operation safe.
