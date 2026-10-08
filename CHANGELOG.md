# Changelog

All notable changes to Noema are recorded here. Format follows Keep a Changelog; versions follow SemVer.

## [Unreleased]

Volition, planning and cognitive research (PLAN.md §8; docs/volition.md, docs/planning.md, docs/agency.md, docs/cognitive-research.md).

### Added
- Wants, want conflicts, commitments, intentions and an append-only volition decision ledger (migration 0026, ADR-0012): deterministic want derivation with thresholds, reinforcement, decay and suppression; explicit conflict resolution without a fixed hierarchy; value-derived tensions (caution, privacy); formation weighing strength, urgency, goals, commitments, values, risk, cost, capability availability and capacity, recording why a want did not become an intention; commitment checks for due, broken, expired, conflicting, impossible and superseded; reconsideration on failures, denials, expiries, capability and commitment changes, weakened assumptions, budgets and goal outcomes; subordinate intentions while waiting for authority.
- Plans and plan steps (migration 0027, ADR-0013): validation of structure, vocabulary and parameters; deterministic strategies and validated model proposals (a `plan` model role); a scheduler-owned executor with conditional claims, verification by observation through predictions, replanning into revisions with a limit, budgets, preconditions, wait and monitor steps, recovery after interruptions without repeating non-idempotent actions, and procedural memory of what worked.
- Cognition: a volition hook, an `intend` process competing for attention, explicit requests become wants with proposed means, operator goals are raised for attention, intentions link supporting beliefs, volition checks in metacognition, self-model mismatch correction.
- Capabilities (migration 0025, ADR-0011): access, side effects, idempotency, timeout, estimates, parameter-derived targets and output trust; a frozen registry; `RequireApproval`; scoped grants with target, parameter, access, expiry and operation limits; structured capability requests with reason, evidence, risk, mitigation, fallback and alternatives, capped and batched per mind, decided only by named people.
- Typed capabilities: `service.status`, `service.restart`, `logs.query` (PII masked), `host.disk.usage`, `host.memory.status`, `host.process.list` (no command lines) and read-only `browser.navigate`, `browser.read`, `browser.links`.
- Agency: forensic "why" explanations from records, conservative attribution (caused, followed, independent, unknown), introspection intents for why an action happened, past beliefs, lineage, counterfactuals, wants, intentions and commitments; every reply scored for introspection accuracy and unsupported action claims (migration 0028).
- Research: ablations and workspace overrides in forks only, self-model overrides, counterfactual replays that omit recorded events, protocol metadata, per-variant volition, plan, downstream and introspection measures, architecture share, reproducibility status, agency metrics and metacognitive calibration, a registry of operational definitions.
- API routes for wants, conflicts, commitments, intentions, decisions, plans, why, attribution, the agency timeline, research measures and metrics, capability requests and scoped grants; UI pages for each, a research dashboard and approval-centre sections for requests and grants.

### Changed
- Forks no longer copy policy rules unless `inherit_policies` is set; work inherited mid-flight is quiesced.
- Conversational requests no longer invoke capabilities directly; they become wants and go through formation and planning.
- `http.fetch` output is low trust. `service.status` is allowed by default.
- Usernames that are internal component names are refused.
- `make test-race` and `make test-db` allow 30 minutes.

### Fixed
- The approval centre's Approve and Deny and the policy form called unwired UI callbacks and returned 500.
- API belief events were published before their transaction committed.
- Snapshot column discovery read the `public` schema instead of the current one.
- The planner consulted the `plan` model even when a deterministic strategy applied, spending a live call on an alternative that could not be selected.
- Intention and plan status transitions were not compare-and-set, so two reconsiderations racing on one intention could both apply and a stale copy could relabel a closed plan.
- Formation summed its terms, and the volition hook, metacognition and forensic explanations iterated, in map order; each now runs in sorted order so identical state gives identical output.

## [0.2.0] - 2026-09-15

Epistemic honesty review pass (PLAN.md §7, summarised in docs/epistemics.md) and Amazon Bedrock support.

### Security
- Archives never import policy rules; forks keep them. The omission is audited.
- NUL bytes in any request path, query or body are refused with a 400 at the edge instead of surfacing as a database error.
- Security suite: SQL-injection effect test, static allow-list scan for assembled SQL, deceived-mind authority test, garbage model output test, self-preservation vocabulary test; threat model rewritten so every verification cites a test that asserts the claim.

### Added
- Amazon Bedrock provider: Converse API for chat roles and Titan/Cohere embeddings, signed with a standard-library SigV4 implementation pinned to botocore reference signatures; credentials as one encrypted reference; non-Bedrock endpoints refused.
- Privacy redaction (ADR-0009): the audited path that makes an event, memory, message, model call, belief or assertion irrecoverable while keeping the row, its links, timestamps and scores; a ledger with a hash of the removed content; cascade to derived rows and the events that quote them; stored snapshot archives scrubbed and re-signed; replay labels redacted stimuli. `noema_redact()` is the only write the append-only trigger admits. Migration 0023. `docs/privacy.md` defines retained, redacted, irrecoverable and deleted.
- Causal timeline: `GET /api/v1/events/{id}/chain` and `/events/{id}/timeline` walk the links events actually record (causation, parents, cited evidence) backwards and forwards within bounded depth and size, label edge kinds, group by cycle as correlation only, and infer nothing.
- Cognitive forks per model: minds may override model routing per role (`llm_roles`, fail-closed); experiments may declare variants that fork one snapshot under different configuration, personality or routing, receive identical stimuli, and are compared deterministically (first divergent stimulus, belief and attention overlap, score differences, memory, goal, cost and capability-request ranges).
- Runtime identity: snapshots, replays and experiments record build (commit, modified tree, Go version, platform), schema version, a secret-free configuration digest, personality version, policy digest, model routing, prompt versions, deterministic subsystem versions and timezone. Replays compare identities and label themselves exact-candidate, different-build or incompatible (the last is never run), and analyse the first divergence by component. Migration 0022.
- Contextual source reliability: a per-(source, domain) record updated only by prediction outcomes and operator corrections, adjusting evidence credibility in that domain once three outcomes exist; never global. Shown on belief pages. Migration 0021.
- Temporal beliefs: every belief has a temporal scope (persistent, transient, unknown, classified deterministically), an observation time and, for transient states, a validity interval. A later observation that a transient state changed closes the earlier belief (valid_until, superseded, score unchanged) instead of contesting it; simultaneous conflicts and persistent propositions still contradict. Introspection reports what was true and when. Migration 0020.
- Confidence semantics: every breakdown states that the number is an uncalibrated epistemic score; belief detail adds a calibrated estimate from the mind's resolved predictions when at least twenty were decided in the same range (otherwise says why not) and names the primary uncertainty deterministically. `docs/beliefs.md` defines every epistemic number.
- Explicit `unknown` belief status: no live evidence either way is neither tentative belief nor falsity. Migration 0019.
- Assertions: every statement made to a mind is recorded first as an append-only assertion (attributed actor, stance, certainty, modality, negation, reported or quoted speech, hypothetical status, interpretation confidence and notes) before, and separately from, any belief about the proposition. Reported speech is attributed to the person reported and weighed as second-hand; hypotheticals, questions and imperatives create no belief; expressed doubt counts against. API `GET /api/v1/minds/{mind}/assertions`, belief pages show who said so. Migration 0018.

### Changed
- Evidence provenance: every evidence row records its derivation, independent-source family and root ids; confidence groups evidence by family so derived material (consolidation, inference, reflection, dreams, retrieval) never counts as corroboration of its own roots and repetition within one source is capped. Existing rows are marked `legacy_unknown` rather than given invented ancestry. Migration 0017.

### Fixed
- ADR-0002 no longer describes the event sequence as gap-free; cursors tolerate gaps by construction and a regression test proves it.

## [0.1.0] - 2026-09-14

First complete pass through all sixteen phases.

### Added
- Phase 16 hardening: TOTP second factor with recovery codes and two-step login, administrator 2FA reset, cross-cutting security test suite, OpenAPI 3.1 document served by the daemon and kept complete by a test, gosec and govulncheck clean with the toolchain pinned, cookie and redirect hardening, release notes.
- Phase 15 study tools: mind snapshots (checksummed JSON archives of full state), forks that share history by reference with remapped ids, deterministic mind comparison, export and hostile-input import, cognitive replay in disabled sandbox minds with exact/approximate/diverged labelling and recorded model responses, and an experiment framework (overrides, stimulus scripts, expectations, exportable results with the subject archive).
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
