# Contributing to Noema

## Ground rules

- Read `PLAN.md`, `ARCHITECTURE.md` and `THREAT_MODEL.md` before changing cognitive or security code.
- Cognitive history is append-only. Never add code that updates or deletes rows in `cognitive_events`, `belief_versions`, `audit_log` or `personality_versions`.
- Every external action goes through `internal/capabilities`. No exceptions, no "internal" shortcuts.
- Every goroutine has an owner, a bound and a context. Use `internal/scheduler` worker pools; do not `go func()` in handlers.
- Deterministic first. If ordinary code can do it (scoring, policy, budgets, state transitions), the LLM does not.
- Treat all ingested data and all model output as hostile input (see `THREAT_MODEL.md`).

## Workflow

1. Small commits, imperative messages using the prefixes `feat:`, `fix:`, `docs:`, `test:`, `chore:`, `security:`, `refactor:`.
2. `make fmt lint test-race` must pass before committing. `make test-db` before finishing a phase.
3. Schema changes are numbered SQL files in `migrations/` with matching `.down.sql`. Never edit an applied migration.
4. Architectural decisions get an ADR in `docs/adr/` (copy `0000-template.md`).
5. Update `CHANGELOG.md` under Unreleased.
6. Never commit `.env`, keys or database dumps. `make security` runs gosec and govulncheck.

## Code style

Idiomatic Go, `context.Context` first, wrapped errors (`fmt.Errorf("...: %w", err)`), `log/slog`, no global mutable state, no panics for expected failures. Comments explain why, not what. Exported identifiers are documented.

## Tests

Unit tests live beside code. Tests needing PostgreSQL read `NOEMA_TEST_DATABASE_URL` and skip when it is unset. Scenario tests live in `tests/`.
