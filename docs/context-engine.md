# ContextLoom Context Engine — A Deep Dive

> Audience: developers who want to understand **how** ContextLoom manages context and **why** it is designed this way. Every numeric constant below is quoted from the current source tree; file paths are given for each mechanism.
>
> Related: [README.zh-CN.md](../README.zh-CN.md) · [README.md](../README.md) · [docs/architecture.md](architecture.md)

---

## Overview: one request's lifecycle

The two subsystems form a closed loop: compression (Part A) keeps requests under budget; retrieval (Part B) guarantees that anything compression removes remains findable — **compression ≠ permanent loss**.

```
 user input
     │
     ▼
 conversation history ──► TokenBudget.Decide()
     │                     usage > 0.70 ─► ActionCompact     (prefix truncation)
     │                     usage > 0.85 ─► ActionHardCompact  (drop oldest +
     │                                                    rolling summary)
     ▼
 CompactToBudgetWithEvict()   ← progressive 5-step compression
     │
     ├── evict(original) ─────► evictPending ─► flush ─► Harness index
     │                                          (.contextloom/harness/index.json)
     ├── [历史摘要] message injected (≤ 1200 runes cumulative)
     ▼
 messages sent to model      ← SafetyRatio 0.92 red line enforced
     │
     ▼
 model response ──► AddToHistory() ──► compression check runs again
     │
     ▼
 model calls harness_search(query, limit)
     │
     ├─► flushEvictPending()        # "compressed → searchable" invariant
     ├─► HybridIndex.Query          # inverted index, session-filtered
     ├─► SymbolIndex boost          # +30 for symbol hits
     ├─► RelevanceScorer            # base keyword score
     ├─► semanticRerank             # coverage 0.5 + cosine 0.5 (zh↔en)
     ├─► overflow compression       # >50 blocks → truncate low scorers
     ├─► sliding window             # anchor / core / reference regions
     └─► LRU cache (256 cap, 2s TTL, session-scoped key)
     ▼
 results: id / score / content (≤ 2000 runes)
```

---

## Part A — Context Fine-Grained Management (`internal/ctxmgr`)

### A.1 TokenBudget — the single decision maker

**Motivation.** Earlier versions truncated by message count, which either threw away context too early or overflowed the model window (API 400). The fix: one `TokenBudget` struct (`internal/ctxmgr/budget.go`) owns every context decision, and Runtime / Agent / Backend paths all consult it.

**Behavior.** All thresholds are usage fractions of the model window:

| Field | Default | Meaning |
|---|---|---|
| `MaxTokens` | model preset `max_context` | ≤ 0 disables budgeting entirely |
| `CompactRatio` | **0.70** | usage above this → prefix truncation |
| `HardRatio` | **0.85** | usage above this → drop oldest + summary |
| `SafetyRatio` | **0.92** | pre-send red line (auto-compress / forced finish) |
| `KeepRecent` | **8** | the last N messages are never *dropped* |

`Decide(current, msgCount)` returns one of three actions:

- `ActionNone` — usage ≤ CompactRatio, or `msgCount ≤ KeepRecent`;
- `ActionCompact` — usage > 0.70;
- `ActionHardCompact` — usage > 0.85.

**Monotonicity is automatic.** `ApplyConfig()` guarantees `compact < hard < safety`; contradictory user input is repaired to the midpoint of the remaining range:

```
if compact >= hard   { hard   = compact + (1-compact)/2 }
if hard   >= safety  { safety = hard   + (1-hard)/2   }
```

**Key constants (token estimation, `MessageTokens()`).** The estimator covers the *full* message structure — undercounting here is the root cause of "ctx% frozen / API 400" bug classes:

- 8 tokens per message envelope; 6 per tool call;
- tool-call `arguments` JSON counted (a `write_file` call can be thousands of tokens);
- `ReasoningContent` (thinking traces) counted — otherwise the ctx indicator never moves for thinking models;
- images: fixed **85 + 384 = 469** tokens each (see A.6);
- `Name` / `ToolCallID` counted.

**Configuration.** `context.compact_ratio / hard_ratio / safety_ratio / keep_recent` (see A.7). Hot-reloadable: GUI/settings changes rebuild the budget via `applyLiveConfig` while preserving the rolling summary.

---

### A.2 The progressive degradation path

**Motivation.** A cliff ("drop half the history") loses structure and trust. Each degradation stage triggers only when the previous one was insufficient.

**Behavior.** `CompactToBudgetWithEvict()` runs up to five steps. `role=system` messages and `[历史摘要]` messages are **protected** in every step:

1. **Prefix truncation** — messages older than the last `KeepRecent` are cut to `prefixLimit()`: `len/3` clamped to **[60, 400] runes**. Recent messages stay intact. Any truncation fires the evict callback with the complete original (A.5).
2. **Drop oldest** — while usage > HardRatio and `len > KeepRecent`, drop the oldest non-protected message; its head **80 runes** feeds the rolling summary (A.3). Eviction is recorded **once per original message** (an `origIdx → evicted` map prevents duplicate index entries).
3. **Second-pass truncation** — if usage is still > SafetyRatio, all but the newest message are re-cut to **300 runes**.
4. **Halving fallback** — repeatedly halve the largest compressible field until usage ≤ SafetyRatio: message content, a tool-call's `arguments` (via `truncateArgumentsJSON`, which truncates the longest string value and re-serializes — JSON stays parseable, `ID`/`name` preserved so tool pairing never breaks), or `ReasoningContent`. Floors: **100 runes** non-system, **200 runes** for system (last resort only, after all non-system messages hit their floor).
5. **Orphan cleanup** — `StripOrphanToolMessages()` removes `tool` messages whose assistant call was dropped, and strips dangling `tool_calls` entries whose tool response was dropped — both would trigger an OpenAI-compatible API 400.

**Key constants.**

| Constant | Value | Where |
|---|---|---|
| prefix limit | `len/3`, min 60, max 400 runes | `prefixLimit()` |
| second-pass limit | 300 runes | step 3 |
| halving floor (non-system / system) | 100 / 200 runes | step 4 |
| JSON args floor | 32 runes, ×0.9 safety factor | `truncateArgumentsJSON()` |
| audit ring | last 20 entries | `addAudit()` |

**Configuration.** Same `context:` block as A.1.

---

### A.3 Rolling summary (`[历史摘要]`)

**Motivation.** Hard compaction would cause cliff amnesia — the model forgets early decisions and constraints. The dropped content is condensed into a running summary that is re-injected, so the gist survives even when the text does not.

**Behavior.**

- Each dropped message contributes its first **80 runes**, prefixed with a role label (`[用户]` / `[助手]` / `[工具]`), joined with `；`.
- The accumulated string is capped at **1200 runes**.
- `SummaryMessage()` wraps it as a **user-role** message whose content starts with `[历史摘要]`; the runtime prepends it to the history (never duplicated — an existing first-message prefix check guards this). The `[历史摘要]` prefix is also what the protection logic keys on: summary messages are never dropped (step 2 skips them) and never re-summarized (`addToSummary` ignores them — no feedback loop).
- The summary is **session-scoped**: cleared on `NewSession` (`ResetSummary`), rebuilt per session on `LoadSession`, preserved across hot config reloads (`applyLiveConfig` copies `old.Summary` into the rebuilt budget).
- Persisted with the compaction snapshot (`CompactedSummary`), so reloading a session restores the same summary instead of regenerating it.

**Key constants.** 80 runes per entry; 1200 runes total cap.

**Configuration.** None — always active when compression runs.

---

### A.4 Compaction audit (`AuditLog`, `/ctx`)

**Motivation.** Silent compression destroys trust ("what did it throw away?"). Every compaction action is logged and user-visible.

**Behavior.** Each step appends a line via `addAudit()`, e.g. `前缀截断 N 条旧消息（保留原长 1/3；system 消息受保护）`, `丢弃 N 条最旧消息并累积滚动摘要`, `折半截断 N 次最大消息兜底`. The buffer keeps the **last 20 entries**. `Runtime.ContextStats()` — served by the `/ctx` command — prints message count, token usage, thresholds, keep-recent, the rolling-summary preview (first 200 runes), and the audit lines.

**Key constants.** 20-entry ring.

**Configuration.** None; visible via `/ctx` in the TUI.

---

### A.5 `EvictFunc` — compression ≠ permanent loss

**Motivation.** Truncated or dropped text vanishes from the model's window but may still matter ("what was that file path I pasted an hour ago?"). ContextLoom closes the loop: everything compression removes is written into the retrieval index, where `harness_search` can find it again.

**Behavior.** `CompactToBudgetWithEvict` accepts an `EvictFunc func(m types.Message)`. The runtime passes `indexCompressedEvictedFor`, which wraps each original message as a `ContextBlock`:

- `ID`: `compressed:<unixnano>:<seq>` — unique via timestamp + monotonic counter;
- `Content`: `[压缩原文 <role>] ` + the **complete, untruncated** original text;
- `Score`: **50** — a neutral starting weight, subject to decay (B.5);
- `SessionID`: the owning session (isolation, B.3).

The block lands in the harness index via the `evictPending` buffer (B.6), is persisted to `.contextloom/harness/index.json` (B.4), and is searchable through `harness_search` — the same tool the model can call. Eviction fires **at most once per original message**, no matter how many times or in which steps it was truncated.

**Configuration.** None; always wired in Runtime.

---

### A.6 Images and thinking/reasoning messages

**Motivation.** Two protocol features skew naive token counters badly:

- **Images**: a base64 data URI can be megabytes. Counting it as text (÷4) estimated a 5 MB image at ~1.67 M tokens — instantly "exhausting" the budget, triggering compression, and degrading the very image the user just sent.
- **Thinking traces** (`ReasoningContent`): for thinking models the reasoning body dominates the message. Ignoring it means the ctx indicator freezes and compression never triggers, while the API bill (and the real window) does.

**Behavior — images.**

- Accounting: each image part costs a fixed **85 + 384 = 469 tokens**, matching DeepSeek's documented vision scaling rule (~800×800 total pixels → ≤ 384 tokens per image, independent of file size or base64 length); `+85` covers JSON structure overhead.
- Degradation: in steps 1 and 3, when an old (non-recent) message carries image parts, `degradeImageParts()` replaces image parts with a text placeholder (`ImageOmittedPlaceholder`), keeping any text parts; the evict callback fires with the original.
- In step 4, image degradation is a **last resort**: the target-picking function ranks multimodal messages by their *text* length, so images are stripped only after all text and tool-call arguments have reached their floors.
- Snapshots: `StripImageDataURI` strips data URIs before persisting compaction snapshots, so `session.json` never balloons to tens of MB; images survive in the JSONL event log as `[图片: <path>]` anchors and are rebuilt on reload.

**Behavior — thinking traces.**

- `ReasoningContent` is fully counted by `MessageTokens()`, so the ctx indicator reflects reality.
- `keepRecent` protects messages from being **dropped**, never from being **truncated**: a single giant message (e.g. a 20k-token tool result) within the last 8 still gets truncated in steps 1/3/4 — the gate for that path is `ExceedsCompactRatio()` (compact threshold), not `SafetyExceeded()`. This distinction is exactly what `thinking_keeprecent_test.go` and `thinking_compact_test.go` pin down.
- Truncation applies to the reasoning field too: step 1 cuts content *and* reasoning to the prefix limit if either exceeds it; step 3 cuts reasoning > 300 runes; step 4 lets the reasoning field compete in halving (internally flagged `tcIdx == -2`), preserving the body preferentially since it carries more value for later turns.

**Configuration.** None — correctness behavior, always active.

---

### A.7 Configuration example (`context:` block)

From `config.example.yaml`:

```yaml
context:
  compact_ratio: 0.70   # prefix-truncate old messages at 70% usage
  hard_ratio: 0.85      # drop oldest + rolling summary at 85%
  safety_ratio: 0.92    # pre-send red line
  keep_recent: 8        # last 8 messages are never dropped
```

`MaxTokens` is not set here — it comes from the active model preset's `max_context` (e.g. `1000000`). Changing `context.*` or `model.max_context` hot-reloads the budget while preserving the rolling summary.

---

## Part B — `harness_search` and the Hybrid Retrieval Engine (`internal/harness`)

### B.1 Tool surface — Agent tool + MCP read-only exposure

**Motivation.** The model itself must be able to recall compressed content, and external MCP clients should get the same data without any write capability.

**Behavior.**

- The runtime registers `harness_search` in the agent tool table (`internal/runtime/runtime.go`), described as read-only and "does not consume conversation token budget". Parameters:
  - `query` (string, **required**) — search keywords;
  - `limit` (integer, optional, **default 5**) — max results.
- The MCP adapter (`internal/runtime/mcp_adapter.go`, `Runtime.HarnessSearch`) exposes the same capability **read-only**. Results are maps with:
  - `id` — block ID;
  - `score` — float64 relevance;
  - `content` — truncated to **2000 runes**.
- `limit <= 0` falls back to 5. Empty queries are rejected at the tool layer.
- Session resolution priority (B.3): ctx-bound session ID (`tools.SessionIDFromContext`) → runtime global current session → none.

**Configuration.** None; both surfaces are always registered.

---

### B.2 The hybrid retrieval pipeline

**Motivation.** A single inverted index either misses paraphrases or drowns in candidates. ContextLoom chains several stages, each narrowing or re-scoring the previous one.

**Behavior.** `Engine.Search()` (`internal/harness/harness.go`):

```
query + metadata
   │
   ▼
[1] LRU cache lookup        key = "sid:<sid>|<query>" (or "<query>")
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
   │                        new = kw + (sem×0.5 + cos×0.5) × 0.5
   │                        only raises scores; capped at 100
   ▼
[6] overflow compression    if > 50 blocks: blocks scoring < 60 truncated to
   │                        first 100 runes + "...(压缩)"
   ▼
[7] sliding window          sort by score → anchor / core / reference
   │
   ▼
[8] cache.set               final windowed layout cached
```

**Stage 3 — symbol boost.** `symbolScoreBoost = 30` is added to blocks the `SymbolIndex` matches for the query (`internal/harness/symbols.go`). The extractor pulls identifier symbols (Go / Python / JavaScript / TypeScript / Rust patterns) from block content at index time — a lightweight, zero-dependency take on "repo map" symbol awareness.

**Stage 5 — semantic rerank** (`internal/harness/semantic.go`). Two complementary measures:

- **Query coverage** = |query features ∩ text features| / |query features|. Tokenization: CJK characters become 2-grams; ASCII words are lowercased with 3-gram prefixes; a built-in Chinese↔English coding-term synonym table (hundreds of entries, e.g. `登录→login/signin`, `超时→timeout`, `缓存→cache`) is applied **bidirectionally**, so a Chinese query like "修登录超时" reaches blocks containing `login`/`timeout`. Feature weights are all 1 (dedup, no TF weighting), so long texts are not diluted.
- **Hash-embedding cosine** (`internal/harness/embedding.go`): query and text are embedded into **256-dim** L2-normalized vectors via the FNV-1a hashing trick; cosine similarity is robust to feature distribution and length, complementing coverage.

Blend formula: `final = keyword + (coverage×100×0.5 + cosine×100×0.5) × 0.5`, applied only when it raises the keyword score, capped at 100 — strong keyword hits are never diluted.

**Stage 7 — sliding window** (`internal/harness/window.go`). Blocks are sorted by score and partitioned:

- **anchor**: `max(1, total × anchor_ratio)` top blocks;
- **core**: the bulk;
- **reference**: `max(1, (1 − anchor_ratio) × 0.2)` lowest blocks.

First-and-last repetition for long windows: blocks scoring ≥ 95 get a `KEY_REMIND:` copy prepended to the reference region; and when the result set exceeds 10 blocks, the top anchor block (score ≥ 85) is appended once more at the very end.

**Key parameters.**

| Parameter | Default | Source |
|---|---|---|
| `symbolScoreBoost` | 30 | constant, harness.go |
| `SemanticRerankTopN` | 30 | `harness.Config` |
| `OverflowCompressBlocks` | 50 | `harness.Config` |
| `OverflowCompressScore` | 60 | `harness.Config` |
| `OverflowCompressKeep` | 100 runes | `harness.Config` |
| embedding dims | 256 | constant, embedding.go |
| cache cap / TTL | 256 / 2s | `harness.Config` |

**Configuration.** `harness:` block (B.7); all knobs hot-apply via `Engine.ApplyConfig()`.

---

### B.3 Session isolation model

**Motivation.** Multiple sessions (chat / task / goal) share one project index. Without isolation, session A's compressed dialogue raw text would leak into session B's `harness_search` results.

**Behavior.** Every `ContextBlock` carries an optional `SessionID`. At query time (`internal/harness/index.go`, the `session_id` metadata key), results are the union of:

- **project-level shared blocks** — no `SessionID`;
- **session-private blocks** — `SessionID` equals the requesting session.

All compressed-history blocks written by A.5 carry the session ID, so they are private by construction. Ownership priority for *which* session a search belongs to:

1. **ctx-bound session** — precise under GUI concurrent multi-session;
2. **runtime global current session** — fallback for unbound tool calls;
3. **empty** — only project-level shared blocks are visible.

The retrieval cache key embeds the session (`"sid:<sid>|<query>"`), so two sessions issuing the same query cannot hit each other's cached results.

**Configuration.** None; enforced unconditionally.

---

### B.4 Persistence — `changeSeq` / `savedSeq` accounting and atomic writes

**Motivation.** Compressed originals must survive process restarts, but writing the index on every mutation would hammer the disk.

**Behavior.**

- `Engine` maintains `changeSeq` (incremented by `Index`, `Invalidate`, `Rerank`) and `savedSeq` (aligned after a successful `SaveIndex`). `NeedsSave()` is simply `changeSeq != savedSeq`.
- A background goroutine polls every **30 seconds** (`startHarnessAutoSave`); when `NeedsSave()` is true it saves. Process exit runs `CloseHarness()` — stop the goroutine, final save.
- `SaveIndex(path)` writes `.contextloom/harness/index.json` **atomically** (write-then-rename in `index.go`): a crash never destroys the previous index. A corrupted or version-mismatched file yields a warning and an empty index — retrieval degrades, the main flow never blocks.
- `LoadIndex` rebuilds the inverted and symbol indexes from the loaded blocks; a missing file is silently tolerated (first run). Only the block list is serialized — symbol/inverted structures are rebuildable, keeping the on-disk format stable.

**Configuration.** Path is fixed: `<project.root>/.contextloom/harness/index.json`.

---

### B.5 Time decay and manual anchoring (`/compress`, `/inject`)

**Motivation.** Raw content's relevance fades over time, but users sometimes need to force importance. Both need a lever.

**Behavior.**

- **Decay**: `Engine.ApplyDecay()` multiplies every block's score by `decay_factor` (default **0.9**). The runtime applies it after each request cycle and again inside `/compress`, so untouched blocks gradually sink while refreshed ones stay afloat.
- **Manual `/compress`**: `ForceCompactWithEvict` temporarily sets `MaxTokens = used × 6 / 10` (usage ≈ 1.67, so all three thresholds trigger), drops `KeepRecent` to `len/2` (min 1; system messages remain protected), and zeroes all three ratios to bypass short-circuits. It then runs the same five-step compression, injects the summary, runs a second compression pass, persists the snapshot, and flushes pending evictions. Short-circuit: only when `len(msgs) ≤ 1` and usage ≤ SafetyRatio — repeated invocations each roughly halve the message count, down to `system + 1` messages (deliberate "compression is unlimited" semantics, distinct from auto-compression which always protects `KeepRecent`).
- **Manual `/inject <blockID>`**: calls `Engine.Rerank(blockID)`, which marks the block dirty and bumps `changeSeq`; the command reports the block as promoted to the anchor region.

**Configuration.** `harness.decay_factor` (default 0.9).

---

### B.6 `evictPending` flush semantics — "compressed → searchable"

**Motivation.** Eviction callbacks fire *while the history lock (`historyMu`) is held*. Calling `Harness.Index()` there would take the harness global lock and rebuild inverted entries — for a large compaction that is tens to hundreds of milliseconds of hold time, during which every GUI websocket poll (status bar, session list, clicks) queues on `historyMu`. A real user-reported freeze ("UI unresponsive, status bar stuck") had exactly this root cause.

**Behavior.**

- Under the `historyMu` write lock, `indexCompressedEvictedFor` only **appends** to `rt.evictPending` — O(1), no harness lock taken.
- After unlocking, `flushEvictPending()` swaps out the buffer and indexes it in bulk.
- `Runtime.HarnessSearch` calls `flushEvictPending()` **before searching** — even on code paths with no other flush point (direct tool invocation, tests), a block that was just compressed is guaranteed to be in the index before the query runs. This is the "compressed → searchable" invariant.

**Configuration.** None; always on.

---

### B.7 Configuration and tunable parameters

From `config.example.yaml`:

```yaml
harness:
  score_threshold: 50   # minimum relevance score for returned blocks
  decay_factor: 0.9     # multiplicative score decay per cycle
  anchor_ratio: 0.2     # fraction of the window reserved for anchors
```

Engine-level tunables set programmatically via `harness.Config` (all have defaults):

| Field | Default | Effect |
|---|---|---|
| `SearchCacheCap` | 256 | LRU entry cap; ≤ 0 → default |
| `SearchCacheTTL` | 2s | cache entry lifetime |
| `SemanticRerankTopN` | 30 | candidates re-ranked semantically |
| `OverflowCompressBlocks` | 50 | block count triggering overflow compression |
| `OverflowCompressScore` | 60 | score floor below which blocks are truncated |
| `OverflowCompressKeep` | 100 | runes kept when truncating |

All `harness.*` values hot-apply through `Engine.ApplyConfig()` (the cache is rebuilt on cap/TTL change; the window is built per query, so `anchor_ratio` needs no rebuild). Context-budget tunables live in `context:` (A.7).

---

### B.8 Usage examples

**Agent-side tool call** — the model decides to recall compressed content:

```json
{
  "tool": "harness_search",
  "args": { "query": "登录超时 retry 配置", "limit": 8 }
}
```

Typical result:

```json
[
  {
    "id": "compressed:1717000000123456789:42",
    "score": 72.5,
    "content": "[压缩原文 user] 我把 retry 配置放在 config.yaml 的 harness 节……"
  }
]
```

**MCP-side call** — external client, read-only:

```json
{
  "method": "tools/call",
  "params": {
    "name": "harness_search",
    "arguments": { "query": "budget decay strategy", "limit": 5 }
  }
}
```

Same data source, same session isolation, same 2000-rune content truncation — but no write operations are ever exposed over MCP.

---

## Key takeaways

- **One decision maker, three thresholds, five steps.** `TokenBudget` (0.70 / 0.85 / 0.92, keep 8) drives a progressive degradation ladder whose monotonicity is repaired automatically.
- **Compression always leaves three traces:** the rolling summary (`[历史摘要]`, ≤ 1200 runes), the audit log (last 20 entries via `/ctx`), and a session-private harness index block (score 50) holding the complete original.
- **Retrieval is a staged pipeline** whose semantic layer (coverage 0.5 + cosine 0.5, bidirectional zh↔en synonyms, 256-dim FNV hashing) bridges Chinese queries to English code without any external embedding service.
- **Two invariants make unattended multi-session operation safe:** session isolation (shared + private blocks, cache keys included) and the `evictPending` flush-before-search guarantee.

## Related documents

- [README.zh-CN.md](../README.zh-CN.md) — 项目总览（中文）
- [README.md](../README.md) — Project overview (English)
- [docs/architecture.md](architecture.md) — overall system architecture
- [config.example.yaml](../config.example.yaml) — full configuration reference
