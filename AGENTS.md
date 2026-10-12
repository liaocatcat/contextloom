# AGENTS.md — AI development gate for ContextLoom

This file constrains AI coding agents (vibe coding) working on ContextLoom.

## 1. Self-gate

Any code/doc change must be self-reviewed before commit:

| Dimension   | Criterion                                             | Max |
|-------------|--------------------------------------------------------|-----|
| Correctness | logic sound, boundaries handled                        | 30  |
| Tests       | new behavior covered by tests                          | 25  |
| Compatibility | existing configs keep working (zero-migration rule)  | 20  |
| Consistency | code / README / config.example.yaml agree              | 15  |
| Cleanliness | gofmt, no dead code, no debug leftovers                | 10  |

Total < 90 → do not commit; fix first.

## 2. Lever table

When trade-offs are unavoidable, list ≤3 levers explicitly (current value, options, quantified benefit, chosen option) in the PR description.

## 3. Evidence rule

"Done" claims need physical evidence: a file path, a diff, or a passing test run — never a verbal summary.

## 4. Style

- Conventional commits (`feat:`, `fix:`, `docs:`, ...).
- gofmt clean; `go vet ./...` clean.
- README (EN + zh-CN) and config.example.yaml must be updated in the same change when behavior changes.
