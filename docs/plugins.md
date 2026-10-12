# Plugins

Everything in ContextLoom is a declarative YAML plugin. Four kinds exist: **employees** (role presets), **scenes**, **toolchains**, and **gates** (quality gates).

## Discovery & merge order

Plugin directories are scanned and merged:

1. User-level: `~/.contextloom/plugins/` (personal, shared across projects)
2. Project-level: `.contextloom/plugins/` (team-shared, committed with the project)

Same-name plugins: project level wins. Ready-made examples ship in [`examples/plugins/`](../examples/plugins/) — copy them into either directory to install.

## Employees (`employees/<name>/employee.yaml`)

An employee is a role preset for sub-agents: system prompt, bound tools, and an optional model route.

```yaml
name: reviewer
description: Strict code reviewer
system_prompt: |
  You are a meticulous code reviewer. Judge correctness first, style last.
tools: [read_file, search_content, git_diff]
model:
  route: role=reviewer     # optional gateway routing hint
```

## Scenes (`scenes/<name>/scene.yaml`)

A scene bundles prompt + toolset + mode into a switchable workspace ("code review mode", "docs writing mode", ...). Switch scenes from the TUI to change how the agent behaves for the whole session.

## Toolchains (`chains/<name>/toolchain.yaml`)

A toolchain is a named, ordered pipeline executed by the agent on demand:

```yaml
name: code-review-chain
steps:
  - tool: search_content
    params: { pattern: "TODO|FIXME" }
  - tool: run_tests
  - tool: propose_vocab
```

Steps run sequentially; each step's output feeds the next prompt.

## Gates (`gates/gate.yaml`)

Creating `.contextloom/gates/` activates quality gating. Gates define score thresholds and evidence requirements for sub-agent deliverables:

```yaml
pass_threshold: 90          # overseer score below this = rejected
require_evidence: true      # deliverable must cite file/test evidence
```

Rejected outputs are sent back to the producing sub-agent with the critique attached — they never reach the user.

## Writing your own

1. Create a directory under `.contextloom/plugins/<your-plugin>/`.
2. Add a `manifest.yaml` (name, version, stages, tools, hooks).
3. Reload (`/reload` in the TUI, or restart).

See [`examples/plugins/`](../examples/plugins/) for minimal and full-featured templates.
