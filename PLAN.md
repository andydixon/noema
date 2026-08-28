# Noema implementation plan

Living document. Each phase lists tasks with status (☐ todo, ◐ in progress, ☑ done) and is committed as it completes. Milestone M1 (section 3) is the first usable vertical slice and is the priority; later phases deepen the system.

## 1. Architectural risks (identified before implementation)

| # | Risk | Mitigation in design |
|---|---|---|
| R1 | Cognitive cycle becomes an LLM loop in disguise | Processes decide model use within explicit budgets; attention, confidence, policy are pure Go; tests assert cycles complete with no LLM configured |
| R2 | Event volume overwhelms storage and UI | UUIDv7 + `seq` indexes, per-type partial indexes, paginated cursors, SSE fan-out with bounded buffers, salience-filtered stream default |
| R3 | Non-determinism defeats replay | Seeds, recorded LLM responses, deterministic ordering of candidates (score, then id), single cycle transaction, explicit exact/approximate labels |
| R4 | Poisoned data shapes beliefs | Trust levels, credibility-weighted confidence, source diversity requirements, corrections as counter-evidence |
| R5 | Goroutine sprawl | One scheduler owns all pools/tickers; `go` statements only inside `internal/scheduler`; leak tests |
| R6 | Provider lock-in | Roles not vendors in cognitive code; three providers in the first provider phase |
| R7 | Schema churn as cognition evolves | JSON payload columns for typed-but-evolving content; strict Go types with versioned decoders; migrations never edited |
| R8 | UI grows into a CRUD panel | Every page answers an operator question ("what is it thinking, why, based on what"); provenance links everywhere; ADR-0007 |
| R9 | Cost blow-up in idle modes | Dreaming/reflection/consolidation each have budgets, hours and kill switches; global daily token budget |
| R10 | Cross-mind leakage via shared caches or queries | Mind-scoped repositories, static SQL scan test, isolation scenario |

## 2. Security boundaries (identified before implementation)

See `ARCHITECTURE.md` §13 and `THREAT_MODEL.md`. Enforced in code at: `internal/api` middleware (auth, CSRF, roles, limits), `internal/perception` (trust tagging, size/type limits), `internal/llm` (data blocks, schema validation, budgets), `internal/capabilities` (policy, approvals), storage repositories (mind scoping), `internal/snapshots` (import validation), `internal/security/httpclient` (SSRF guard).

## 3. Milestone M1 – first usable vertical slice

An administrator can: start Noema → log in → create a mind → configure personality → configure an LLM → chat → watch events live → inspect working memory, workspace, episodic memories, basic beliefs, provenance → restart → continue the same mind. Phases 0–8 deliver M1. Nothing in later phases starts until M1 is robust.

## 4. Phases

### Phase 0 – Repository and architecture ☑
- ☑ Inspect environment, record in `docs/ENVIRONMENT.md`
- ☑ `git init`, `.gitignore`, `.env.example`, `Makefile`
- ☑ README, PLAN, ARCHITECTURE, SECURITY, THREAT_MODEL, CONTRIBUTING, CHANGELOG
- ☑ ADRs 0001–0008
- ☑ `go.mod`, compose and Dockerfile skeletons
- ☑ First commit

### Phase 1 – Foundation: config, logging, storage, migrations, auth, base UI ☑
- ☑ `internal/config`: defaults → JSON file → env; validation; redaction
- ☑ `internal/observability`: slog with redacting handler, Prometheus registry, `/metrics` (token-protected)
- ☑ `pkg/ids`: UUIDv7
- ☑ `internal/storage`: pgxpool with statement timeout, migration runner (advisory lock, checksums, up/down/status), per-test schema helper
- ☑ `migrations/0001_foundation`: append-only guard trigger, users, sessions, api_tokens, recovery_codes, audit_log, settings
- ☑ `internal/auth`: Argon2id, opaque hashed sessions with idle/absolute expiry, CSRF (token + Origin check), per-IP limiter, per-account lockout, roles, middleware, API tokens
- ☑ `internal/audit`: audit writer and listing
- ☑ `internal/api`: server, middleware chain (request id, recover, CSP/security headers, body limits, logging/metrics, auth), JSON error envelope, health, status, users, sessions, tokens, audit
- ☑ `web/` + `internal/ui`: layout, light/dark theme, navigation, login, first-run setup, dashboard, system, users, account (sessions, tokens, password), audit, about; `app.js` (theme, CSRF fetch, SSE helper, keyboard shortcuts)
- ☑ `cmd/noemad` (graceful shutdown, bootstrap admin, supervised background tasks), `cmd/noemactl` (`status`, `migrate up|down|status`, `user create`, exit codes)
- ☑ `deploy/`: compose (pgvector db + app), multi-stage Dockerfile
- ☑ Tests: config precedence, migration up/down + append-only trigger, hashing, session lifecycle, lockout, CSRF, roles, API envelope
- ☑ TOTP + recovery codes (delivered in Phase 16)

### Phase 2 – Mind model, identity, personality, self-model ☑
- ☑ `migrations/0002_minds`: minds, personality_templates, personality_versions (append-only), self_models
- ☑ `internal/minds`: structured personality (identity, values, traits, communication, behaviour, free text) with normalisation, validation, deterministic brief, field diff, template overrides; cognitive config with defaults; self-model with structural limitations
- ☑ Store: create/clone/update status/list/resolve by slug or id, versions with restore, templates CRUD with in-use protection, apply/detach template preserving overrides
- ☑ API: minds, personality (get/put/brief/versions/diff/restore/template), templates, self-model
- ☑ UI: minds list + create, mind overview (identity, brief, self-model, config, lifecycle, clone), personality editor (sliders, preview, history, diff, restore, template, import/export JSON), template editor
- ☑ `noemactl mind list|create|inspect|clone|personality`, `noemactl template list`
- ☑ Audit events for every change; docs/personality.md
- ☑ Tests: brief determinism, diff/override round trip, normalisation, store lifecycle including append-only history

### Phase 3 – Immutable cognitive event store ☑
- ☑ `pkg/events`: closed type vocabulary with categories, evidence refs, observation payload with trust levels and credibility
- ☑ `migrations/0003_events`: cognitive_events (append-only trigger, seq, indexes on mind/type/correlation/causation/parents/evidence), mind_cursors
- ☑ `internal/events`: validating append (single tx or caller tx + publish after commit), filtered queries with seq cursors, causal navigation (cause, parents, children, cited, derived, correlated), bounded fan-out bus, durable cursors, stats, operator-action helper
- ☑ API: list/filter, stats, get, forensic context, SSE stream (per mind and global) with Last-Event-ID replay, filter matching and heartbeats, operator observation injection
- ☑ UI: live thought stream with filters/paging, forensic event page (causal chain, evidence, derived, correlation, payload), dashboard important events, mind overview activity + observe form; operator changes recorded as `operator_action` events
- ☑ `noemactl event inspect|list|tail`, `noemactl observe`
- ☑ Tests: bus bounds and filtering, append validation, immutability, filters, cursors, causal lookups, SSE end to end

### Phase 4 – Working memory, global workspace, attention ☑
- ☑ `internal/attention`: ten named features, per-mind weights, normalised weighted sum with per-term breakdown, total ordering by (score, id)
- ☑ `internal/workspace`: capacity-limited workspace with refresh-by-subject, decayed competition, explained outcomes; working memory with activation decay and least-active eviction; token overlap for context relevance
- ☑ `internal/brain`: `Process` interface and registry, per-mind runtime with debounced wake-ups, idle timer, settle window, cursor advance only on read events, bounded concurrent Observe with isolation and timeouts, deterministic scoring, loop detector with metacognition warning, executive dispatch by thought kind, single transaction per cycle with publish after commit, persisted snapshot, operator commands (cycle, pause, resume, reset, reload), cycle rate budget; manager reconciling active minds and waking on bus events (own output never re-triggers)
- ☑ `internal/cognition/attend`: deterministic attention candidates from external events (urgency lexicon, novelty vs context, trust-based operator priority)
- ☑ `migrations/0004_workspace`
- ☑ API: workspace view, cycle now, mode, reset; config changes reload runtimes
- ☑ UI: workspace page (contents with score breakdown bars, working memory, last cycle candidates with fate and reason, controls, attention weight editor) refreshing live on broadcasts
- ☑ `noemactl workspace show|cycle|pause|resume|reset`
- ☑ Tests: scoring determinism and bounds, ranking order, admission/refresh/capacity/decay/prune, provenance chain across a cycle, restart continuity, no self-triggered cycles, loop suppression, pause/reset/cancellation

### Phase 5 – Episodic and semantic memory ☑
- ☑ `pkg/concepts`: epistemic kinds, memory kinds, relation vocabulary
- ☑ `migrations/0005_memory`: memories (kinds, epistemic status, trust/credibility, importance, activation, tsvector), memory_relationships, conditional pgvector memory_embeddings
- ☑ `internal/embeddings`: interface, None fallback, pgvector formatting
- ☑ `internal/memory`: mind-scoped store, fused retrieval with recorded components (text any-term/strict, vector, recency, activation, importance, credibility), reinforcement, decay without deletion, relationships, statuses, stats, embed-missing job
- ☑ Processes and handlers: `retrieve` proposes recalls as candidates; `attend` handler encodes attended events as episodic/autobiographical memories; `retrieve` handler reinforces and records `memory_retrieved`
- ☑ API: list/search, create (operator knowledge), detail with relationships and citing events, retract, relate, stats
- ☑ UI: memory explorer (search with component scores, timeline, filters, add knowledge), memory detail (provenance, strength, relationships, correction)
- ☑ `noemactl memory search|list|inspect|add`; decay and embedding background jobs; docs/memory.md
- ☑ Tests: lifecycle, fused retrieval, cross-mind isolation (retrieve, get, relate), reinforcement, decay, retraction, stats

### Phase 6 – Beliefs, evidence, confidence, contradictions ☑
- ☑ `migrations/0006_beliefs`: beliefs, belief_evidence, belief_alternatives, belief_links, append-only belief_versions and corrections
- ☑ `internal/beliefs`: deterministic confidence (prior by epistemic kind, weight×credibility, single-source diversity discount, bounded sigmoid) with stored breakdown; derived statuses; create/add evidence/re-evaluate/retract/alternatives/links; deterministic contradiction detection (negation + Jaccard) with mutual counter-evidence; operator corrections including do-not-infer blocking; retracted-evidence propagation
- ☑ Process `associate` (temporal co-occurrence hypotheses) and `hypothesis` handler creating beliefs with automatic alternatives; cycle `EmitNow` for atomic provenance
- ☑ API: list, assert, detail (breakdown, evidence, versions, alternatives, links, events), evidence, alternatives, retract, corrections; memory retraction re-evaluates beliefs
- ☑ UI: belief explorer with statuses and corrections, belief detail with arithmetic, evidence for/against, alternatives, contradictions, history, correction forms; overview shows important beliefs
- ☑ `noemactl belief list|inspect|assert|wrong`; docs/beliefs.md
- ☑ Tests: monotonic bounded confidence, diversity discount, status derivation, contradiction detection, lifecycle with provenance, append-only history, do-not-infer enforcement, isolation

### Phase 7 – LLM provider abstraction and configuration UI ☑
- ☑ `internal/security/secrets`: env:/file:/enc: references, AES-256-GCM under the master key, description without disclosure
- ☑ `internal/security/httpclient`: dial-time private-range refusal, bounded redirects, body caps
- ☑ `internal/llm`: roles, provider-neutral Request/Response, LanguageModel, provider kinds, scripted Fake
- ☑ `internal/llm/providers`: OpenAI-compatible, Anthropic, Ollama (chat + embeddings), scrubbed errors, fenced-JSON parsing
- ☑ `internal/llm/router`: providers/models/roles/calls store, role routing with fallbacks, role and model parameter defaults, global and per-mind daily budgets, concurrency pool, call recording with request hash, replay from recordings, connectivity test, embedder for the embed role
- ☑ `internal/llm/prompts`: versioned prompts (append-only), data blocks, rendering to requests with schema, seeded defaults (perceive, respond, hypothesise, summarise, judge, narrate), clone/restore
- ☑ `migrations/0007_llm`
- ☑ API: providers (create/update/delete/test), models, roles, calls, usage, prompts (save/versions/restore/clone/test)
- ☑ UI: LLM page (providers, models, role matrix, usage, errors, budgets, add provider with encrypted key), provider page, prompt list, prompt editor with test runner, history and line diff
- ☑ `noemactl provider list|test`, `noemactl prompt list|show`; embedder wired into memory; docs/llm-providers.md
- ☑ Tests: secret references, SSRF dialer, wire formats for all three providers, error scrubbing, private endpoint refusal, router routing/budget/recording/replay, prompt data-block escaping, defaults, versioning

### Phase 8 – Conversation (completes M1) ☑
- ☑ `migrations/0008_conversations`: conversations (person, project, channel), messages linked to events, intents and model calls
- ☑ `internal/conversation`: mind-scoped store; deterministic analysis (intent, introspection kind, entities, claims with certainty, urgency, sentiment, topic)
- ☑ `cognition/perceive` process: conversational observations become high-priority candidates, optionally enriched by the perceive prompt within the per-cycle model cap
- ☑ `cognition/respond` handler: perception event, episodic memory with participants, claims → beliefs with the message as evidence, response intent built from beliefs/memories/self-model, grounded introspection (thinking, beliefs, why, uncertain, contradictions, learned, changed; honest declines otherwise), wording by the respond prompt or deterministic rendering, `response_generated` citing all evidence, reply row
- ☑ Per-cycle model allowance shared by processes and handlers; attend handler defers conversational episodes to perceive
- ☑ API: conversations, messages with optional wait, one-shot `ask`; mind creation starts its runtime immediately
- ☑ UI: conversation list, chat page with live refresh, per-reply “why this reply” intent and event links
- ☑ Sample mind `configs/minds/dani.json` (schema noema.mind-seed/1) with strict import endpoint and `noemactl mind import`; `noemactl ask`
- ☑ `tests/m1`: acceptance test covering all 14 milestone steps including restart continuity; docs/conversation.md

**Milestone M1 reached.** Remaining phases deepen the system; the vertical slice above is exercised end to end by `go test ./tests/m1/`.

### Phase 9 – Goals, drives, affect, predictions, curiosity ☑
- ☑ `migrations/0009_motivation`: goals, drives, affect_states, predictions, append-only prediction_results
- ☑ `internal/goals`: tree with kinds/status/priority/progress/blockers, stalled detection, deterministic goal relevance
- ☑ `internal/motivation`: closed drive set (no self-preservation), personality-derived baselines, explicit event rule table, decay, attention weight modulation, significance thresholds
- ☑ `internal/predictions`: criteria (event terms, belief confidence, operator), deterministic judging, append-only outcomes, calibration (accuracy, Brier, buckets, overconfidence flag), resolver job with prediction_error events and belief feedback
- ☑ Brain: after-cycle hooks, goals and motivation in cycle state, effective weights recorded per cycle
- ☑ Cognition: motivation hook, goal monitor, curiosity (questions from misses, contradictions, unexplained observations) with question → investigation goal handler, hypothesis-derived predictions, goal relevance in attend
- ☑ API: goals, predictions (create/resolve/calibration), state (get/reset/drive ranges); UI: goals page, prediction dashboard with calibration, cognitive state panel on overview, effective weights on workspace; CLI goal/prediction/state
- ☑ Docs: predictions.md, motivation.md. Demo scenario 4 (prediction error) verified live

### Phase 10 – Metacognition, reflection, consolidation, narrative, notifications ☑
- ☑ `migrations/0010_cognition`: consolidation_jobs, reflections, narratives, notifications; per-mind settings for schedules and model budgets
- ☑ `cognition/metacognition`: deterministic self-checks (insufficient evidence, single source, fluent-without-evidence, repeated failure, stale contradiction, overconfidence) competing for attention; admitted warnings discount beliefs or open investigations
- ☑ `internal/reflection`: computed findings over the period, patterns → procedural lessons, optional model prose, reflection events
- ☑ `internal/consolidation`: deterministic clustering of episodes, evidence-derived belief + linked semantic memory per cluster, optional model wording within budget, jobs with inputs/outputs, pause, reject
- ☑ `internal/narrative` + hook: structured five-part narrative rendered deterministically, optional rephrasing
- ☑ `internal/notifications` + hook: explicit thresholded rules, cooldown aggregation, suppression events, unread badge
- ☑ Background job: scheduled consolidation and reflection per mind with mode switching; API, UI (Background tab, notifications page, overview narrative and warnings), CLI (narrative, consolidate, reflect, notifications); docs/cognition.md
- ☑ Demo scenario 3 verified live; tests for notifications, consolidation, reflection

### Phase 11 – Relationship graph ☑
- ☑ `migrations/0011_graph`: entities (aliases, merged_into), entity_relationships (relation, confidence, source, event, count), entity_links
- ☑ `internal/graph`: canonical matching, deterministic kind guessing, upsert with alias resolution, reinforced edges cited to events, links, bounded subgraph/neighbourhood, merge preserving old ids, kind/description correction, stats
- ☑ Cognition: perceive and attend handlers link extracted entities, edges (discussed_with, related_to) and references to memories/beliefs/events
- ☑ API: graph, entities search/detail, merge (entities_same correction), distinct (entities_different correction), update, relate; UI: canvas force explorer with filters, focus and side panel, entity page with corrections; `noemactl entity`; docs/graph.md
- ☑ Tests: kind guessing, canonicalisation, upsert dedupe, edge reinforcement, cross-mind refusal, merge with alias/link migration, bounded subgraph, stats

### Phase 12 – Capability and action policy system ☑
- ☑ `migrations/0012_capabilities`: policies, approval_requests, append-only approval_decisions, session_grants, tool_executions
- ☑ `internal/capabilities`: code-defined registry with param schemas and risk classes, locked capabilities, deterministic policy evaluation (specificity, strict tie-break, time windows), request hashing, single `Invoke` path with forbidden-context refusal, execution isolation, tool output as tool-trust observations, approvals (once, session, deny, modified params), grants, expiry, inventory; built-ins
- ☑ Cognition: `act` process (fetch requests → http.fetch), `action` handler through the policy layer, replies report capabilities and action status, self-model synced from effective policy with self_model_updated events
- ☑ API: capabilities, policies, per-mind inventory, invoke, approvals, grants, executions; UI: policy matrix and rules, approval centre with modify/approve/session/deny, dashboard count; notification rule for requests; `noemactl approvals|policy|invoke`; docs/capabilities.md
- ☑ Tests: specificity and ties, locks, windows, parameter validation, hashing, allow/ask/deny/session/forbidden/panic paths, append-only decisions, inventory; live flow verified

### Phase 13 – Integrations
- ☑ research folder watcher (fingerprints, safe extraction, provenance), git repositories, Prometheus/VictoriaMetrics, generic webhooks (HMAC); integration health UI, `read_metrics` and `repository.read` capabilities

### Phase 14 – Dreaming and associative cognition
- ☑ dream mode, hours, budgets, association process, promotion/rejection UI (forbidden capability context; hypotheses start ≤0.35)

### Phase 15 – Snapshots, forking, replay, experiments, export/import
- ☑ snapshots, fork with shared history, comparison tools, replay engine and UI (exact / approximate / diverged), experiment framework with exportable results, mind export/import archives (hostile-input validation)

### Phase 16 – Hardening and release
- ☑ TOTP + recovery codes, security test suite (`tests/security`), OpenAPI document (`docs/openapi.json`, served at `/api/v1/openapi.json`, coverage-tested), docs set, `go test -race`, `go vet`, staticcheck, gosec, govulncheck clean, release notes
- ☑ screenshots captured with headless Chromium into `docs/screenshots/` (19 pages)
- ☑ performance pass: `tests/perf` seeds 60k events / 6k memories / 1.5k beliefs, times hot reads against a ceiling and rejects sequential-scan plans; migration 0016 adds the indexes it found missing (see `docs/performance.md`)

## 5. Deterministic vs LLM decisions
See `ARCHITECTURE.md` §14. Rule: if a unit test can assert the exact output, it is code.

## 6. Assumptions and defaults chosen
- Dev database is the compose container on port 5433 (local 5432 lacks pgvector).
- pgvector optional; detected at migrate time.
- Default branch `master` (git default on this machine).
- JSON config file format (stdlib) rather than YAML.
- No npm toolchain; UI assets hand-written and embedded.
- Bedrock added in 0.2 (ADR-0010) with a standard-library SigV4 signer.
- Remote repository: none configured. Development continues locally; pushing will be documented when a remote exists.

---

## 7. Review pass: epistemic honesty, security reconciliation, reproducibility, privacy (v0.2, started 2026-09-15)

Baseline recorded before any change: `go vet ./...` clean; `go test -race -count=1 ./...` passes (36 packages with tests, exit 0) at commit `4693dcb`, tagged `v0.1`. Working tree carried one untracked, user-created file (`docs.tgz`), left untouched.

### 7.1 Findings and evidence

Each finding was checked against the implementation, its tests and the documentation. Classification: **D** implementation defect, **DD** documentation defect, **AW** architectural weakness, **P** partially implemented, **OK** already correct, **X** incorrect assumption.

| # | Topic | Class | Evidence |
|---|---|---|---|
| 1 | Assertions vs beliefs | **AW** | `internal/cognition/respond.go:95-111`: every claim extracted from a message becomes a `claim` belief whose only evidence is the message event (`Source: "said-by:"+actor`, weight 0.6, 0.8 for corrections). No structure records *who asserted what, with what stance, modality or certainty*; reported speech, hypotheticals, quotation, disagreement and sarcasm all collapse into the same path. `docs/conversation.md` states the message is the evidence. No test covers reported/hypothetical/negated speech. |
| 2 | Evidence provenance | **AW** | `internal/beliefs/confidence.go:91-117`: diversity counts distinct `Source` strings; `belief_evidence` has no ancestry columns (`migrations/0006`). Consolidation (`consolidation.go:310`) cites episodes as `memory` evidence with source `conversation/user` while the same episodes' claims already cite the message event as `said-by:actor`; a belief holding both counts two "independent" sources for one utterance. Hypotheses (`handlers.go:75`) key each event as its own source (`co-occurrence:`+id), so two events from one actor count twice. Replay preserves event ids, so provenance can be reconstructed once recorded. |
| 3 | Meaning of confidence | **DD/P** | `docs/beliefs.md` and the UI present `confidence` as a bounded score with a formula, never as calibrated probability, but the word "confidence" and the `conf` helper (percent-like display) invite that reading. `predictions.Calibrate` (`predictions.go:379-425`) already computes buckets and a Brier score per mind; nothing links belief scores to it. No documented meaning for `credibility`, `weight`, `diversity`. |
| 4 | "Gap-free" sequences | **DD** | `docs/adr/0002-event-immutability.md:12` says "bigserial seq for gap-free cursors". Code assumes only monotonic order: cursor reads `seq > cursor` (`brain/cycle.go:387`, `events/store.go:388-403`), SSE resumes with `seq > Last-Event-ID` (`api/events.go:283-303`). No `+1` assumption found (grep). No regression test for gaps. |
| 5 | Security docs vs code | **DD** (one **D**) | T3 says archives are "extracted with path normalisation…" and, in the same paragraph, "Archives are never extracted" (`THREAT_MODEL.md:26-27`); code rejects archives (`integrations/research.go`). T16 says "JSON in a zip with per-file SHA-256 manifest" then contradicts itself; code is a single JSON document with a whole-archive SHA-256 (`snapshots/archive.go`). T16 also says "policies are not imported" then "policies are copied as state": code copies `policies` on **import** as well as fork (`snapshots.Tables`). Importing policies from an untrusted archive is an authority-bearing import: **defect**; fork (same operator, same instance) may keep them. `SECURITY.md` says the research folder "treats archive members as hostile paths": archives are rejected outright. |
| 6 | Security test claims | **DD/P** | T9 (SQL injection) cites `TestStoredXSSIsEscaped`, which only sends metacharacters through search routes and checks status 200. No test asserts that injected SQL had no effect on other rows or that identifiers are allow-listed. Dynamic SQL exists in `snapshots/archive.go` (table/column names from `Tables` and `information_schema`) and in filter builders (`events/store.go`, `memory/memory.go`, `beliefs/store.go`) that use positional parameters. T17–T25 are marked ☐ although T19 (`router` budget tests), T20 (`brain.TestLoopDetectorSuppressesRepeats`), T21 (`secrets` tests), T22 (`integrations` signature/replay tests), T23 (git hooks disabled, tested) have tests; T17, T18, T24, T25 are partially covered. |
| 7 | Capability boundary | **OK/P** | Authority is enforced in `capabilities.Service.Invoke` (`service.go:258-`) outside cognition: forbidden contexts, registry lookup, parameter validation, policy `Decide`, approval or execution. `cognition.Act` only proposes `http.fetch` from conversation requests (`action.go:31-57`); model output never reaches `Invoke` directly. Missing: a test that a *forged or malicious cognitive output* (an `action` thought naming `sudo_shell`, or a perception JSON containing a tool call) cannot execute, and an explicit statement of the principle in `SECURITY.md`. |
| 8 | Immutable history vs privacy | **AW** | No redaction path exists. `cognitive_events` payloads, `messages.content`, `memories.content`, `llm_calls.request/response`, `audit_log.details`, snapshot archives and exports all hold plaintext; ADR-0002 mentions a "dedicated, audited maintenance path" that was never built. Logs do not write payload content (checked `slog` call sites). |
| 9 | Replay reproducibility | **P** | `replays` record snapshot, range, steps and labels (`replay.go`) but no build identity: `internal/version.Version` exists (ldflags, default `dev`) and nothing else is captured. `experiments` record LLM routing but not build, schema or prompt versions. Labels are exact/approximate/diverged; no "incompatible" and no first-divergence component analysis. |
| 10 | Cognitive forks per model | **P** | Snapshots, forks and experiments exist; experiments run one subject. Model routing is daemon-wide (`router.routing`, `llm_roles`), so two forks cannot use different providers. `llm.Request.MindID` is already carried, so per-mind role overrides can be resolved in the router without touching callers. |
| 11 | Causal timeline | **P** | Event page shows one hop (`api/events.go:151-190`: parents, children ≤50, evidence, derived, correlated). No multi-hop, bounded, labelled traversal; no distinction between recorded causation (`causation_id`, `parent_ids`, evidence refs) and inferred (same cycle). |
| 12 | Temporal beliefs | **AW** | `beliefs` has `first_created`, `last_evaluated`, `superseded_by` only; no validity interval. Contradiction detection (`store.go:~500-530`) marks both contested. "door open at 10:00" then "door closed at 10:05" becomes a contradiction. |
| 13 | Explicit unknowns | **P** | Statuses: hypothesis, tentative, likely, accepted, contested, rejected, superseded (`confidence.go:18-24`). A belief with no evidence sits at its prior as `tentative`; there is no `unknown`. Introspection answers "I have no belief about X" when nothing matches (`respond.go` intent), which is correct but not represented as state. |
| 14 | Contextual reliability | **AW** | Credibility is a fixed function of trust level (`pkg/events/events.go:221-236`); no per-source or per-domain adjustment, and no place an operator correction could lower it. Nothing lowers global credibility either (no defect, but no mechanism). |
| 15 | Association vs causation | **P** | `concepts.Relation` has `related_to`, `caused_by`, `supports`, `contradicts`, `derived_from`, `similar_to`, `follows`; entity co-mention writes `related_to` (`entities.go:51`); association hypotheses say "may have caused" and are `hypothesis` beliefs with alternatives (`handlers.go`), which is correct; no `co_occurs_with`, `possibly_caused_by`, `temporally_precedes`, `asserted_by` relations; nothing writes `caused_by` today. |
| 16 | Interpretation language | **OK/P** | `docs/interpretation.md` states the boundary and gives two operational examples. Missing: a table of operational meanings for the major terms. |
| 17 | Docs consistency | **X/DD** | README's "all sixteen phases" is true (PLAN §4). "T17–T25 unchecked" refers to `THREAT_MODEL.md` threats, not phases: they are verification items, several of which have tests today (see #6). |
| 18 | AWS Bedrock (user request) | feature | ADR-0004 anticipated Bedrock "behind the same interface later"; `llm.ProviderKind` has openai, anthropic, ollama. Bedrock needs SigV4 signing (stdlib crypto) over the Converse API, credentials as secret references. |

### 7.2 Design decisions (smallest coherent change per finding)

1. **Assertions.** New package `internal/epistemics`: an `Assertion` record (actor, actor kind, proposition, subject, stance `asserts|denies|doubts|questions|supposes`, speaker certainty `asserted|hedged|uncertain`, modality `factual|hypothetical|reported|quoted|imperative|question`, negated, reported actor, interpretation confidence with notes, source event, context, trust) and a deterministic interpreter over the perception output. Conversation creates an assertion for every claim; a proposition belief is created **only** for factual or reported stances, with evidence of kind `assertion`, family rooted at the source event, weight scaled by certainty, modality and interpretation confidence; hypotheticals and questions create no belief; `doubts` adds counter-evidence from the doubter. "Andy believes X" is queryable from assertions with the assertion's own confidence (interpretation confidence), separate from the proposition's score. Migration `0017_assertions`.
2. **Provenance.** `belief_evidence` gains `derivation` (direct, assertion, consolidation, inference, reflection, dream, retrieval, prediction, operator, external), `family` (independent-source key) and `root_ids uuid[]` with `provenance_status` (`recorded` | `legacy_unknown`, default legacy for existing rows). `Compute` groups by family: a family contributes its strongest member plus a diminishing share of the rest (documented), so three derived copies of one source ≈ one, and diversity counts families. Producers (conversation, consolidation, association, predictions, operator, dreaming) supply ancestry; a bounded helper derives roots for memories through `derived_from` edges (depth ≤ 8). Migration `0017` (shared with assertions, or `0018`).
3. **Confidence semantics.** Rename in documentation and UI labels: *epistemic score* (raw, uncalibrated) everywhere the number appears; add `beliefs.Calibration` interface backed by `predictions.Calibrate` per mind (overall and per prediction category), returning a calibrated estimate only when the bucket has ≥ 20 decided predictions and "insufficient data" otherwise; `Breakdown` gains explicit component meanings. No stored precision beyond what exists.
4. **Sequences.** Correct ADR-0002 and comments; add `TestCursorsToleratePlaceholderGaps` that burns sequence values in a rolled-back transaction and verifies cursor, query and SSE resume unchanged.
5. **Security docs.** Rewrite T3, T7, T16 and `SECURITY.md` to describe the code; **stop importing policies from archives** (fork keeps them), record the omission in the import result and audit; T17–T25 each rewritten to name a real test or stay ☐ with an honest note.
6. **Security tests.** Add `TestSQLInjectionHasNoEffect` (metacharacters through every search and filter route with row-count and content assertions before/after) and a static test that dynamic SQL identifiers come only from allow-lists; add tests for T25 (drive and goal vocabularies exclude self-preservation; minds cannot reach admin routes) and T24 (schema-invalid model output rejected). Rewrite each threat's V line to the test that actually proves it.
7. **Capability boundary.** Add `TestDeceivedMindHasNoAuthority`: forged action thoughts for locked, unknown and ASK capabilities and a perception JSON containing a tool call all end in deny/approval, never execution; document "A mind may be deceived. Deception must not imply authority." with the boundary's location.
8. **Privacy.** Cryptographic erasure is not adopted now: search, retrieval, replay and the UI read plaintext from these columns, and key-per-row envelopes would touch every read path; recorded as future work with the threat model. Implement the audited maintenance path ADR-0002 promised: a `redactions` ledger and `noema_redact_*` SQL functions (SECURITY DEFINER, callable only through `internal/privacy`) that replace the sensitive columns of an event, memory, message or model call with a fixed marker, keep a SHA-256 of the removed content, set `redacted_at/reason/policy`, write an audit row and a `redaction` event; cascade helpers find dependants (memories by origin event, messages by event, model calls by correlation); stored snapshot archives are scrubbed of the same ids; export and replay skip redacted content and label it. Backups are documented as outside the system's reach. Migration `0019_redaction`.
9. **Replay identity.** `internal/version` gains `Info()` from `debug.ReadBuildInfo` (module version, VCS revision, modified flag, time, Go version, OS/arch) plus schema version (max applied migration), canonical non-secret config digest, timezone, and per-run prompt versions, personality version, policy digest and LLM routing. Snapshots store the identity at capture; replays store it at run and compare: same commit and schema → candidate for exact; different schema → **incompatible**; different build → approximate at best with the differing fields listed. Divergence analysis records the first diverging step and the event categories that differed. Migration `0020_runtime_identity`.
10. **Forks per model.** `CognitiveConfig.LLMRoles map[role]model-id` honoured by the router when `Request.MindID` is set; experiments gain `variants` (name + config/personality/LLM overrides) run from one snapshot against the same stimuli, with a comparison report: first divergent stimulus, belief-set overlap, mean confidence difference, attention overlap, memory and goal counts, tokens and cost, capability requests. Existing single-subject experiments keep working.
11. **Causal timeline.** API `GET /api/v1/events/{id}/chain?direction=back|forward&depth≤6` and UI `/events/{id}/timeline`: bounded BFS over `parent_ids`, `causation_id`, evidence references and reverse references, ≤ 200 nodes, edge kinds `caused`, `parent`, `cited`, `derived`, and `same-cycle (inferred)` clearly labelled.
12. **Temporal beliefs.** `beliefs` gains `valid_from`, `valid_until`, `observed_at`, `temporal_scope` (`persistent|transient|unknown`); a deterministic scope classifier for state propositions; a newer contradicting observation of a *transient* proposition closes the older belief (`valid_until`, status `superseded`, reason "state changed") instead of contesting it; persistent contradictions behave as today. Migration `0018_temporal_beliefs`.
13. **Unknowns.** New status `unknown` for beliefs with no live evidence on either side (except hypotheses); introspection reports it as "I do not know"; CHECK constraint widened by migration `0018`.
14. **Source reliability.** `source_reliability(mind_id, source, domain, correct, wrong, score)` updated only by prediction outcomes and operator corrections **in the domain of the belief concerned**; evidence credibility = trust credibility adjusted by the (source, domain) score when it has ≥ 3 observations. Exposed on the belief page. Migration `0018`.
15. **Association vs causation.** Add relations `co_occurs_with`, `possibly_caused_by`, `temporally_precedes`, `asserted_by`; co-mention writes `co_occurs_with`; association hypotheses record `possibly_caused_by` between the two memories/entities; `caused_by` is reserved for explicit operator statements. Tests.
16. **Interpretation.** Add an operational-meaning table to `docs/interpretation.md`.
17. **Docs pass** after implementation; README status line clarified; THREAT_MODEL statuses verified.
18. **Bedrock.** `internal/llm/providers/bedrock`: SigV4 (HMAC-SHA256, stdlib) over the Converse API for chat and Titan/Cohere embeddings via InvokeModel; the credential as one secret reference (`ACCESS_KEY_ID:SECRET_ACCESS_KEY[:SESSION_TOKEN]`) and the region read from a Bedrock runtime endpoint; same SSRF-safe client; migration 0024 widens the kind constraint; signing pinned to botocore reference vectors plus wire-format tests.

### 7.3 Affected packages and files

`internal/beliefs` (confidence, store, migrations), new `internal/epistemics`, `internal/cognition` (respond, handlers, entities, associate, action tests), `internal/consolidation`, `internal/dreaming`, `internal/predictions`, `internal/events` (gap test), `internal/snapshots` (policy exclusion, identity, scrub), `internal/replay`, `internal/experiments`, `internal/llm` (+`router`, new `providers/bedrock`), `internal/version`, `internal/config` (digest), new `internal/privacy`, `internal/api` (evidence/assertion/chain/redaction/variant routes, openapi.json), `internal/ui` (belief detail, timeline, experiment comparison, redaction admin), `pkg/concepts`, `migrations/0017–0020`, `tests/security`, `docs/*`, `THREAT_MODEL.md`, `SECURITY.md`, `README.md`, ADRs 0002 and new 0009–0012.

### 7.4 Database migrations

All additive: new tables (`assertions`, `source_reliability`, `redactions`, `experiment_variants` if needed), new nullable or defaulted columns (`belief_evidence.derivation/family/root_ids/provenance_status`, `beliefs.valid_from/valid_until/observed_at/temporal_scope`, `replays.identity`, `snapshots.identity`, `cognitive_events.redacted_at/redaction_id` plus `redacted_hash`), a widened CHECK on `beliefs.status`, and SQL functions for redaction. Existing evidence rows are marked `provenance_status='legacy_unknown'` with `family = source`; no ancestry is invented. Down migrations drop only what they added. Redaction functions are the only path that modifies append-only rows and run inside one transaction with an audit row.

### 7.5 Security implications

Policies are no longer imported from archives (fail closed; forks unaffected). Redaction is administrator-only, audited, CSRF-protected and irreversible by design. New endpoints are read-only except redaction and experiment variants (operator). Bedrock credentials are secret references; the signing key never leaves the process; the region is derived from an endpoint that must be a Bedrock runtime host. No new attack surface accepts file uploads or shell arguments.

### 7.6 Backwards compatibility

APIs are extended, not changed; existing fields keep their names (`confidence` stays as the JSON field, documented as the epistemic score, with `calibrated` added where available). Existing beliefs keep their statuses and scores until re-evaluated; `Compute` on legacy rows behaves exactly as before because legacy families equal legacy sources. Archives from 0.1 import unchanged except that policies are skipped and reported. Replays without identity are shown as "identity not recorded".

### 7.7 Tests required

Per change: unit tests for the interpreter (eight speech patterns), provenance grouping (independent vs derived vs repeated vs consolidated), calibration gating, gap tolerance, policy import exclusion, SQL injection effect test, static identifier scan, deceived-mind authority test, temporal supersession, unknown status, per-domain reliability isolation, relation semantics, identity capture and incompatibility, variant comparison metrics, chain traversal bounds and cycle safety, redaction (content gone, hash kept, references intact, snapshots scrubbed, replay labelled), Bedrock signing vectors and wire formats. Full `go test -race ./...`, `go vet`, staticcheck, gosec, govulncheck before each commit that touches code.

### 7.8 Documentation affected

`docs/beliefs.md`, `docs/conversation.md`, new `docs/epistemics.md` (final deliverable: architecture changes, epistemic, security, replay and privacy models, tests, limitations), `docs/snapshots-replay.md`, `docs/graph.md`, `docs/interpretation.md`, `docs/llm-providers.md`, `docs/predictions.md`, `SECURITY.md`, `THREAT_MODEL.md`, `README.md`, ADR-0002 correction, new ADRs (assertions and provenance; redaction; replay identity; Bedrock signing), `CHANGELOG.md`, the OpenAPI document, and the published site.

### 7.9 Implementation order

1. Sequence terminology + gap regression test (docs, tiny code).
2. Security reconciliation: policy import exclusion, SQL-injection and identifier tests, deceived-mind test, T17–T25 verification, `SECURITY.md`/`THREAT_MODEL.md` rewrite.
3. Evidence provenance schema and grouped `Compute`.
4. Assertions and the conversation pipeline.
5. Confidence semantics and calibration interface; unknown status; temporal beliefs; contextual reliability; relation semantics (four small commits).
6. Replay identity and divergence analysis; per-mind LLM routing and multi-variant experiments.
7. Causal timeline.
8. Privacy redaction.
9. Bedrock provider.
10. Interpretation table, documentation pass, final deliverable, site rebuild.

### 7.10 Rollback considerations

Each commit is independently revertible; migrations are additive with down scripts, and no migration rewrites existing values other than defaulting new columns. Reverting provenance leaves extra columns unread. Reverting redaction leaves the ledger table; redacted content is gone by design and cannot be rolled back, which is documented on the redaction screen. Reverting per-mind LLM routing leaves the config key ignored.

### 7.11 Status (2026-09-15)

| # | Topic | Status | Commit |
|---|---|---|---|
| 4 | Sequence semantics | done | 920c2c0 |
| 5 | Security docs vs code; policy import | done | 6a0dd74, c684e87 |
| 6 | Security test claims | done | 0577be3, 93a082d |
| 7 | Capability boundary test | done | 93a082d |
| 2 | Evidence provenance | done | 27c7a42 |
| 1 | Assertions | done | 2020ac5 |
| 13 | Explicit unknown | done | dcd4c5e |
| 3 | Confidence semantics, calibration, primary uncertainty | done | da5e7ce |
| 12 | Temporal beliefs | done | 5291942 |
| 14 | Contextual reliability | done | 10a0314, 10c2eb6 |
| 15 | Association vs causation | done | 831d476 |
| 9 | Replay identity and divergence | done | 767f765 |
| 10 | Forks per model | done | 1fca8fe |
| 11 | Causal timeline | done | 14a77bf |
| 8 | Redaction | done | 761e00f |
| 18 | Bedrock | done | 0a46012 |
| 16 | Interpretation table | done | c9c2cc9 |
| 17 | Documentation consistency | done | 7c69791 and the site rebuild |
| 25 | Final deliverable | done | f43495c (`docs/epistemics.md`) |

Every code commit passed `go test -race ./...`, `go vet`, staticcheck, gosec (G104 excluded) and govulncheck. Remaining limitations are listed in `docs/epistemics.md` §7.
