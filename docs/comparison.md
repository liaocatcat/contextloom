# ContextLoom in Comparison

> Audience: developers evaluating ContextLoom against "calling an LLM directly" and against mainstream agent frameworks / coding agents.
> Goal: state **structural, checkable differences** — not marketing claims. Every ContextLoom capability below is backed by a module path in this repository; competitor columns describe general, publicly documented characteristics and may change over time (consult each project's own docs for current specifics).
>
> Related: [README](../README.md) · [README.zh-CN](../README.zh-CN.md) · [architecture.md](architecture.md) · [context-engine.md](context-engine.md) · [toolchain.md](toolchain.md) · [plugins.md](plugins.md)

---

## 1. ContextLoom vs. a "naked" LLM call

A naked LLM call is: paste a prompt, get text back. It is stateless between requests, has no memory of your project, and cannot verify its own claims. Every row below maps a common pain point to the **mechanism** ContextLoom adds (and where it lives in the source).

| Pain point (naked LLM) | ContextLoom's mechanistic answer | Where |
|---|---|---|
| **Cliff-edge amnesia** — once the conversation exceeds the window, early content is silently lost | Token-budget-driven **progressive layered compaction**: prefix truncation (>70%) → drop oldest + rolling summary (>85%) → halving fallback (>92%). A rolling summary (`[历史摘要]`, ≤1200 runes) is re-injected so the gist survives | `internal/ctxmgr` (`TokenBudget`, `CompactToBudgetWithEvict`) |
| **Compaction = permanent loss** in naive truncation schemes | **Compacted originals are 100% retrievable.** Every truncated/dropped original is written to the retrieval index and persisted; the model (or an MCP client) can recall it via `harness_search`, tagged `[compacted-original]` | `internal/ctxmgr` (`EvictFunc`) → `internal/harness` (`index.json`) |
| **Retrieval needs a bolted-on vector store**, embedding service, and infra | **Built-in hybrid retrieval, zero dependencies**: inverted index + symbol index (+30 boost) + lightweight semantic reranking (query coverage + FNV-1a hash-embedding cosine, bidirectional EN↔ZH synonyms). Pure in-memory, persistable, no external embedding server | `internal/harness` (`harness.go`, `semantic.go`, `embedding.go`, `symbols.go`) |
| **Sub-agent output is unverifiable** — "done" cannot be trusted | **Overseer + quality gates**: evidence-chain verification (physical evidence required), **test execution escrow (TEE)** that re-runs claimed tests in a sandbox and compares pass/fail counts, and static verification each goal round. Below-threshold output is bounced, never surfaced | `internal/overseer` (`anti_cheat.go`, `scoring.go`), `internal/toolchain` gates |
| **Session crash loses all progress** | **JSONL multi-agent checkpoints**: one `run-<id>.jsonl` per run, a full snapshot appended every round, `LoadLatest` resumes after a crash (comparable to LangGraph's checkpointer, delivered as a zero-dependency file) | `internal/checkpoint`, `internal/goalmgr` |
| **Hand-rolled prompt tuning; the agent "gets dumber" over a long session** | **Automatic employee experience distillation**: after a subtask completes, the model distills golden rules / pitfalls / style into the employee's `ONTOLOGY.md`, injected automatically on the next assignment (idempotent, hash-deduped, ≤1KB per run) | `internal/orchestrator`, `internal/memory` |
| **Model routing, rate limiting, failover each written separately** | **Model gateway**: rule-based routing (`role=`, `task=`, `budget<=`), multi-key pools with rotation, exponential-backoff retries, circuit-breaker auto-degradation | `internal/modelgateway` |
| **Tool execution cannot be rolled back; network/security risk** | **Tool sandbox**: writes redirected to a shadow filesystem, forced network isolation (proxy pointed at a dead port), two-phase `commit`/`discard`, memory/CPU/time limits | `internal/sandbox` (`sandbox.go`, `network.go`, `limits_*.go`) |
| **No observability; debugging an agent session is guesswork** | **JSONL trace** (daily rotation) + **OTLP export** (hand-written LogsData protocol, no OTel SDK) + **offline eval** (`eval`, `eval-retrieval` with Recall@K / MRR) | `internal/trace`, `internal/eval` |

**One-line summary of the difference:** a naked LLM gives you *text*; ContextLoom wraps it in an engineering harness that makes output **verifiable, resumable, and auditable**.

---

## 2. ContextLoom vs. general agent frameworks

Frameworks like **LangChain**, **LangGraph**, **AutoGPT**, and **CrewAI** are powerful, widely adopted, and ecosystem-rich. ContextLoom makes a different structural choice: a **single self-contained binary, zero external services**. The table states factual differences only; where a framework is stronger, it is noted plainly.

| Dimension | ContextLoom | LangChain / LangGraph | AutoGPT | CrewAI |
|---|---|---|---|---|
| **Primary language** | Go (1.22+) | Python (LangChain.js also exists) | Python | Python |
| **Distribution** | Single static binary; TUI builds are `CGO_ENABLED=0` and cross-compile to all majors | Library installed into a Python app | App / library (Python env) | Library (Python env) |
| **Context compaction** | First-class: token-budget ladder (0.70/0.85/0.92) + rolling summary, auditable via `/ctx` | Varies by app; typically app-composed (e.g. summarization chains) | App-composed | App-composed |
| **Compacted-original recovery** | Built in — `harness_search` recalls compacted text after restart | Not a framework primitive (would be custom) | Not a framework primitive | Not a framework primitive |
| **Retrieval** | Embedded hybrid (inverted + symbol + hash-embedding rerank); **no external vector DB / embedding service required** | Rich integrations; typically pairs with a vector store (FAISS, Pinecone, pgvector, …) and an embedding provider | Vector-store integrations | Via LangChain / tools |
| **Quality gate / anti-cheat** | Overseer scoring + evidence chain + test-TEE re-run (sandbox) | Not a built-in gate; composed via custom callbacks | Not built in | Not built in |
| **Crash-recovery checkpoints** | JSONL checkpoints per run (file-based, zero-dep) | **LangGraph has a first-class checkpointer** (in-memory/DB backends) | Limited | Limited |
| **Persistent memory** | Append-only JSONL sessions + `lessons.md`; no global lock, multi-instance safe | Memory modules + external stores | Vector memory | Memory modules |
| **Dependency footprint** | Zero external services; no DB, no message queue, no vector store, no OTel SDK | Large dependency tree; vector store / LLM provider SDKs | Similar, Python ecosystem | Depends on LangChain stack |
| **Observability** | Built-in JSONL trace + OTLP export | LangSmith (hosted) / integrations | Third-party | Third-party |
| **Ecosystem & language breadth** | Narrow: Go, self-contained | **Much broader**: hundreds of integrations, many languages (via LangChain.js), huge community | Broad plugin ecosystem | Multi-agent focused |

**Honest framing.** If you need dozens of third-party integrations, a Python data/ML ecosystem, or a hosted tracing/observability platform, a Python framework will fit better. ContextLoom's bet is the opposite: **the entire context, quality, and memory machinery ships inside one binary you can copy to any machine and run offline** — a trade of ecosystem breadth for zero-infrastructure self-containment.

---

## 3. ContextLoom vs. coding agents

Coding agents like **Claude Code**, **Cursor**, **OpenHands**, and **aider** are polished, capable tools with their own strengths. ContextLoom differs mainly in **form factor, extensibility model, and license/dependency posture**. This table is factual, not evaluative.

| Dimension | ContextLoom | Claude Code | Cursor | OpenHands | aider |
|---|---|---|---|---|---|
| **Form factor** | **Dual form**: TUI (pure Go) + native desktop GUI (Wails/WebView), sharing one runtime | Terminal CLI | Desktop IDE (editor) | Web/CLI app | Terminal CLI |
| **Local vs cloud** | Runs entirely locally; drives any OpenAI-compatible endpoint (llama.cpp, ollama, vLLM, cloud) — model-agnostic | Cloud (Anthropic models) | Cloud models (some local) | Local or cloud; Docker sandbox | Local; many model providers |
| **Extensibility model** | Scenes / toolchains (DAG) / employees / gates / manifest plugins — declarative YAML, project + user level merged | MCP tools, hooks | Extensions, MCP | Plugins, MCP | Scripts / config, git-centric |
| **Sandbox** | Shadow filesystem (write redirection) + forced network isolation; `/sandbox commit|discard` | Permission prompts | IDE-level | Docker container sandbox | Git-based rollback |
| **Multi-agent & quality gate** | Sub-agent fan-out + Overseer scoring + evidence chain + test-TEE; goal loop with checkpoints | Subagents | Limited | Multi-agent | — |
| **Observability** | Built-in JSONL trace + OTLP export + offline eval | Provider-side | IDE-level | Logs | Logs |
| **MCP** | **Both directions**: MCP client (consume external tools) + MCP server (expose retrieval/memory/skills read-only) | MCP client | MCP | MCP client | MCP |
| **License** | MIT (open source) | Proprietary | Proprietary | Open source (MIT) | Open source (Apache-2.0) |
| **Language of implementation** | Go (single binary, zero external deps) | — | — | Python | Python |

**Honest framing.** Claude Code and Cursor are highly integrated products with first-party model tuning and mature UX; ContextLoom does not compete on model quality or editor polish. OpenHands and aider share ContextLoom's open-source, model-agnostic stance. ContextLoom's distinct offering is **the context engine + quality gate + dual TUI/GUI form packaged as one dependency-free Go binary**.

---

## 4. Capabilities that are unique or rare

These are the mechanisms that are either unique to ContextLoom or uncommon as **built-in, dependency-free** features. Each is checkable in the source tree.

| Capability | Why it is unusual | Source |
|---|---|---|
| **`harness_search`: compacted-original recovery** | Most systems treat compaction as loss; ContextLoom makes "compressed ≠ lost" an invariant (flush-before-search guarantees a just-compacted block is already indexed) | `internal/ctxmgr` + `internal/harness` |
| **Employee experience auto-distillation** | The agent itself distills lessons into its own ontology profile — not user homework; idempotent and capacity-limited | `internal/orchestrator`, `internal/memory` |
| **Test execution escrow (TEE)** | Claimed test results are re-executed in a sandbox and compared; similarity < 0.8 ⇒ untrustworthy | `internal/overseer/anti_cheat.go` |
| **Zero-dependency hybrid retrieval** | Inverted + symbol index + semantic rerank with a hand-rolled FNV hash embedding and a built-in EN↔ZH synonym table — no vector DB, no embedding service | `internal/harness` |
| **JSONL multi-agent checkpoints** | File-based per-run snapshots (comparable to LangGraph's checkpointer), resumable, with rolling rewrite to bound file size | `internal/checkpoint` |
| **MCP both directions** | Consumes external MCP tools **and** exposes its own retrieval/memory/skills read-only over stdio JSON-RPC | `internal/mcpclient`, `internal/mcpserver` |
| **OTLP export, hand-written** | Emits OpenTelemetry LogsData without importing the OTel SDK — collectable by Langfuse / Jaeger / any Collector | `internal/trace/exporter.go` |

---

## 5. When NOT to use ContextLoom

Honest boundaries — ContextLoom is the wrong tool in these cases:

- **You need a huge library of third-party integrations** (dozens of vector stores, SaaS connectors, language-specific ML tooling). A Python framework ecosystem will serve you better.
- **You want a hosted/cloud service** with managed scaling, multi-tenant auth, and a web dashboard. ContextLoom is a local binary; it is not a hosted platform.
- **You plan to extend the core in a non-Go language.** The core is Go; extension points are YAML plugins and MCP/ACP, not in-process bindings to other runtimes.
- **You need a full IDE experience** (rich editor, language servers, debugger UI). ContextLoom ships a TUI and a lightweight GUI, not an IDE.
- **You only need a one-shot LLM call** with no persistence, retrieval, or verification — the harness is overhead you don't need.

---

## 6. Further reading

- [README](../README.md) / [README.zh-CN](../README.zh-CN.md) — project overview and full feature list
- [docs/architecture.md](architecture.md) — layered architecture
- [docs/context-engine.md](context-engine.md) — deep dive on compaction and `harness_search`
- [docs/toolchain.md](toolchain.md) — employees / scenes / toolchains / gates
- [docs/plugins.md](plugins.md) — plugin model and discovery order

---

## Key takeaways

1. Against a **naked LLM**, ContextLoom's difference is **mechanism, not model**: verifiable output (TEE + evidence chain), resumable sessions (checkpoints), and non-lossy compaction (retrieval recovery).
2. Against **general agent frameworks**, the difference is **packaging**: one dependency-free Go binary versus a Python library ecosystem — a deliberate breadth-for-self-containment trade.
3. Against **coding agents**, the difference is **form and extensibility**: TUI + GUI from one runtime, declarative scenes/chains/employees/gates, dual-direction MCP, MIT-licensed Go core.
4. ContextLoom is **not** a fit when you need massive integrations, a hosted service, a non-Go core, or a full IDE.
