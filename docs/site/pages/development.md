Noema was built in sixteen explicit phases, each committed with tests and documentation before the next began. This page collects the contributing guide, the testing story, the phase plan, the changelog and the release notes.

## How it was built

| Phase | Delivered |
|---|---|
| 0 | Repository, plan, architecture, threat model, ADRs |
| 1 | Daemon skeleton, configuration, embedded migrations, authentication, roles, sessions, audit, metrics |
| 2 | Minds, identity, versioned personality, templates, self-model |
| 3 | Append-only cognitive events, provenance, live streams |
| 4 | The cognitive cycle: processes, attention, workspace, executive, modes |
| 5 | Memory: kinds, fused retrieval, decay, relationships |
| 6 | Beliefs: evidence arithmetic, statuses, contradictions, corrections |
| 7 | Language models: roles, providers, routing, budgets, recording, prompts |
| 8 | Conversation and the first milestone: a mind you can talk to and inspect |
| 9 | Goals, drives, affect, predictions, calibration, curiosity |
| 10 | Metacognition, reflection, consolidation, narrative, notifications |
| 11 | Relationship graph |
| 12 | Capabilities, policy, approvals |
| 13 | Integrations: research folder, git, Prometheus, webhooks |
| 14 | Dreaming |
| 15 | Snapshots, forks, comparison, export/import, replay, experiments |
| 16 | Two-factor authentication, security suite, OpenAPI, scanners, performance pass, screenshots, release |

## Testing

`go test -race ./...` runs unit and integration tests; anything that needs PostgreSQL uses a fresh schema per test and is enabled by `NOEMA_TEST_DATABASE_URL`. `tests/m1` is the end-to-end acceptance test for the first milestone with a fake model. `tests/security` is the cross-cutting security suite. `tests/perf` is the opt-in load test. `make lint` runs staticcheck; `make security` runs gosec and govulncheck. All were clean at release.

{{include CONTRIBUTING.md}}

## Release notes

{{include docs/RELEASE_NOTES.md demote=1}}

## Changelog

{{include CHANGELOG.md demote=1}}
