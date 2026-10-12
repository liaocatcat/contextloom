# Contributing to ContextLoom

Thanks for your interest in contributing! This document covers the ground rules.

## Development workflow

```bash
git clone https://github.com/contextloom/contextloom-agent.git
cd contextloom-agent
go build ./...
go test ./...
```

## Before you open a PR

1. **Format**: `gofmt -w .` — CI enforces this on all tracked `.go` files.
2. **Vet**: `go vet ./...` must be clean.
3. **Tests**: `go test -race ./...` must pass. New behavior needs new tests.
4. **Docs sync**: if you change behavior, update the README (both languages), `config.example.yaml`, and the relevant `docs/` page in the same PR.

## Self-review gate

Before submitting, score your own change honestly on:

| Dimension   | What to check                                            | Weight |
|-------------|----------------------------------------------------------|--------|
| Correctness | logic sound, edge cases handled                          | 30    |
| Tests       | new behavior covered by unit/integration tests           | 25    |
| Compatibility | existing configs & workflows unaffected (zero migration) | 20  |
| Consistency | code / README / config.example.yaml all tell the same story | 15 |
| Cleanliness | gofmt clean, no dead code, no debug leftovers            | 10    |

A total below 90 means the change is not ready — iterate before opening the PR.

## Commit messages

Use conventional commits: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`. Reference the issue number when applicable.

## Reporting bugs

Include: Go version, OS, minimal reproduction steps, and the relevant `.contextloom/trace/*.jsonl` snippet (redact secrets).

## License

By contributing you agree that your contributions are licensed under the [MIT License](LICENSE).
