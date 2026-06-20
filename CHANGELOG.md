# Changelog

All notable changes to Noema are recorded here. Format follows Keep a Changelog; versions follow SemVer.

## [Unreleased]

### Added
- Phase 5 memory: episodic/semantic/procedural/autobiographical memory with epistemic status, fused retrieval with inspectable component scores, optional pgvector embeddings, decay without deletion, attention-gated encoding, retrieval process, memory explorer and CLI.
- Phase 4 cognition core: deterministic attention scoring with inspectable breakdowns, global workspace and working memory with decay, per-mind cognitive runtime with bounded concurrent processes, loop detection, operator controls, live workspace page and CLI.
- Phase 3 events: append-only cognitive event store with provenance links, filtered queries, durable cursors, live SSE streams with resume, forensic event explorer UI, thought stream page, operator observations and CLI.
- Phase 2 minds: structured personalities with append-only version history, diff, restore, templates with preserved overrides, self-model, mind lifecycle (create, clone, disable, archive), API, UI editor and CLI.
- Phase 1 foundation: configuration, structured logging, metrics, migrations, local authentication (Argon2id, sessions, CSRF, lockout, roles, API tokens), audit log, JSON API skeleton, server-rendered admin UI with light/dark theme, `noemad` and `noemactl`.
- Repository foundation: plan, architecture, security model, threat model, ADRs, Makefile, container files.
