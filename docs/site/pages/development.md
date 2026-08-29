Noema was built in sixteen explicit phases, each committed with tests and documentation before the next began, and each verifiable on its own. This page records what each phase delivered, how the result was measured, and the release notes and changelog.

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

## The 0.2 review

After the 0.1 release the project was reviewed against its own claims: is what the documentation says about evidence, security, reproducibility and privacy true of the code? Each finding was classified with evidence before anything changed, recorded in the plan, and fixed with the smallest coherent change, its own tests and its own commit. The outcome is summarised under [epistemic honesty](epistemics): assertions kept apart from beliefs, evidence provenance and per-family counting, honest score labels with gated calibration, explicit unknowns, temporal scope, source reliability per subject, association distinguished from causation, runtime identity on snapshots and replays, forks per model, a bounded causal timeline, audited redaction, a threat model rewritten from the implementation, and Amazon Bedrock. Eight migrations were added; none discarded data, and every one has a down file.

## How it is verified

Every subsystem has unit tests that run against a fresh database schema; an end-to-end acceptance test drives the whole daemon with a fake model through the first milestone (start, log in, create a mind, converse, inspect memory and beliefs, restart and continue); a security suite exercises prompt injection, roles, cross-mind isolation, stored scripts, secret disclosure and hostile archives; and an opt-in load test seeds a realistic volume and rejects any hot query that drifts off its index. The race detector, staticcheck, gosec and govulncheck were clean at release and at every commit of the 0.2 review, which added tests for provenance grouping, calibration gating, unknown status, temporal supersession, per-subject reliability, relation semantics, cursor gaps, chain traversal bounds, secret-free identity digests, variant comparison, fail-closed routing, redaction, injection by effect, assembled-SQL scanning, the deceived mind, garbage model output, drive vocabulary and Bedrock signing against reference vectors.

## Measured performance

{{include docs/performance.md}}

## Release notes

{{include docs/RELEASE_NOTES.md demote=1}}

## Changelog

{{include CHANGELOG.md demote=1}}
