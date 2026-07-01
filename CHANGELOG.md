# Changelog

All notable changes to Noema are recorded here. Format follows Keep a Changelog; versions follow SemVer.

## [Unreleased]

### Added
- Phase 14 dreaming: opt-in idle mode with allowed hours, interval and budgets; deterministic memory replay sample, vocabulary-based association of unlinked memories, speculative hypotheses as low-prior beliefs with optional model wording; promotion/rejection review in UI, API and CLI; dreams run in a forbidden capability context so they can never act.
- Phase 13 integrations: research folder watcher with safe extraction and fingerprints, shallow bare git mirrors with co-change stats, Prometheus/VictoriaMetrics threshold and change detection, HMAC-signed webhooks with replay protection and rate limiting, per-integration health, runs and document ledger in UI/API/CLI, `read_metrics` and `repository.read` capabilities.
- Phase 12 capabilities: single policy-governed action path with ALLOW/ASK/DENY rules, locked capabilities, request-bound approvals (once, session, modified), tool output as tool-trust observations, self-model synchronisation, approval centre and policy matrix.
- Phase 11 relationship graph: mind-scoped entities with aliases, evidenced typed edges, links to memories/beliefs/events, deterministic extraction from conversation and observations, merge/distinct corrections, bounded canvas explorer, API and CLI.
- Phase 10 background cognition: metacognitive self-checks, scheduled reflection with lessons, episode consolidation into evidenced generalisations with operator review, internal narrative, thresholded notifications with aggregation.
- Phase 9 motivation: goal tree with attention relevance, closed drive set and rule-driven affect state with decay and weight modulation, explicit predictions with deterministic resolution, prediction errors feeding beliefs, calibration dashboard, curiosity questions becoming investigation goals.
- Phase 8 conversation (milestone M1): messages become observations, deterministic perception with optional model enrichment, claims become evidenced beliefs, grounded introspective answers from real state, model or deterministic wording with full provenance, chat UI, `noemactl ask`, sample mind Dani with a strict seed import, and an end-to-end acceptance test.
- Phase 7 LLM layer: provider-neutral router with roles and fallbacks, OpenAI-compatible/Anthropic/Ollama providers, encrypted secret references, SSRF-safe HTTP client, budgets, full call recording with replay, versioned prompts with untrusted data blocks, configuration and prompt manager UI, CLI.
- Phase 6 beliefs: evidence-derived confidence with inspectable arithmetic, statuses, alternatives, append-only history, deterministic contradiction detection, temporal-association hypotheses, operator corrections (wrong, do-not-infer, inaccurate memory), belief explorer and CLI.
- Phase 5 memory: episodic/semantic/procedural/autobiographical memory with epistemic status, fused retrieval with inspectable component scores, optional pgvector embeddings, decay without deletion, attention-gated encoding, retrieval process, memory explorer and CLI.
- Phase 4 cognition core: deterministic attention scoring with inspectable breakdowns, global workspace and working memory with decay, per-mind cognitive runtime with bounded concurrent processes, loop detection, operator controls, live workspace page and CLI.
- Phase 3 events: append-only cognitive event store with provenance links, filtered queries, durable cursors, live SSE streams with resume, forensic event explorer UI, thought stream page, operator observations and CLI.
- Phase 2 minds: structured personalities with append-only version history, diff, restore, templates with preserved overrides, self-model, mind lifecycle (create, clone, disable, archive), API, UI editor and CLI.
- Phase 1 foundation: configuration, structured logging, metrics, migrations, local authentication (Argon2id, sessions, CSRF, lockout, roles, API tokens), audit log, JSON API skeleton, server-rendered admin UI with light/dark theme, `noemad` and `noemactl`.
- Repository foundation: plan, architecture, security model, threat model, ADRs, Makefile, container files.
