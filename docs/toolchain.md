# Toolchains, Employees, Scenes & Gates

This page explains how the four building blocks compose into a supervised workbench. See [plugins.md](plugins.md) for file locations.

## The model

```
          ┌──────────── goal/task input ────────────┐
          ▼                                          │
   ┌────────────┐  fan-out   ┌────────────┐          │
   │ orchestrator│──────────▶│ sub-agents │          │
   └──────┬─────┘            │ (employees)│          │
          │                  └──────┬─────┘          │
          │                deliverables              │
          │                         ▼                │
          │                  ┌────────────┐   reject │
          │                  │  overseer  │──────────┘
          │                  └──────┬─────┘
          │                   score ≥ threshold
          ▼                         ▼
   ┌─────────────────────────────────────┐
   │              result                  │
   └─────────────────────────────────────┘
```

- **Employees** define *who* does the work (prompt, tools, model).
- **Scenes** define *which set* of employees/toolchains is active.
- **Toolchains** define *how* a multi-step job is executed.
- **Gates** define *when output is good enough* to be shown.

## Quality gates in practice

With `.contextloom/gates/` present, every sub-agent deliverable passes through the overseer:

1. The overseer receives the deliverable plus its claimed evidence (files touched, tests run).
2. It scores against the gate config (default pass threshold 90/100).
3. Below threshold → bounced back with actionable critique; above → forwarded.

Evidence must be physical (a file path, a test result), not a verbal claim. This is what makes "tests passed" verifiable rather than aspirational.

## Toolchain example

`.contextloom/chains/release-check/toolchain.yaml`:

```yaml
name: release-check
steps:
  - tool: run_tests          # full suite first
  - tool: exec               # then lint
    params: { command: "golangci-lint run ./..." }
  - tool: git_diff           # collect the diff for review context
```

Run it from the TUI with the toolchain command; steps stream their outputs into the conversation.

## Employees example

`.contextloom/employees/coder/employee.yaml`:

```yaml
name: coder
description: Implementation agent
system_prompt: |
  Implement the requested change. Prefer minimal diffs.
  Every claim must reference a file path or test output.
tools: [read_file, write_file, replace_in_file, exec_command, run_tests]
```

## Scenes example

`.contextloom/scenes/hardening/scene.yaml`:

```yaml
name: hardening
description: Security-focus mode
employees: [coder, reviewer]
chains: [release-check]
gate:
  pass_threshold: 95
```

## Tips

- Keep gate thresholds strict (≥90); the cost of a bounce-back round is lower than the cost of reviewing bad output yourself.
- Commit project-level plugins so teammates get identical behavior.
- Use `role=` gateway rules to route reviewer employees to a stronger model.
