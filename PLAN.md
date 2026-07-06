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
- Bedrock deferred until a SigV4 client is justified; interface supports it.
- Remote repository: none configured. Development continues locally; pushing will be documented when a remote exists.
