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
- ☐ TOTP + recovery codes (moved to Phase 16 with the rest of hardening)

### Phase 2 – Mind model, identity, personality, self-model
- ☐ `migrations/0002_minds`: minds, mind_config, personalities, personality_versions (append-only), personality_templates, self_models
- ☐ `internal/minds`: create/clone/disable/archive/list/inspect; structured identity, values, traits, communication style, behavioural preferences; free-form text; versions with diff/rollback; templates with overrides; export/import of personality
- ☐ `internal/selfmodel`: capability inventory derived from policy, limitations, activities
- ☐ API + UI: mind list, create, overview skeleton, personality editor (structured + text, preview, history, diff, rollback, templates), audit events
- ☐ `noemactl mind list|create|inspect`
- ☐ Tests: versioning, rollback, template overlay, audit emission

### Phase 3 – Immutable cognitive event store
- ☐ `pkg/events`: types, categories, evidence refs, payload contracts
- ☐ `migrations/0003_events`: cognitive_events (append-only trigger, indexes), mind_cursors
- ☐ `internal/events`: store (append single/batch in tx), query (cursor, filters), bus (bounded fan-out), SSE endpoint with Last-Event-ID resume
- ☐ API: list/inspect events, causal navigation (cause, parents, children, evidence)
- ☐ UI: event explorer (forensic view, filters, navigation), live thought stream page
- ☐ `noemactl event inspect`
- ☐ Tests: immutability trigger, ordering, cursor resume, bus backpressure, filters

### Phase 4 – Working memory, global workspace, attention
- ☐ `internal/brain`: `BrainState`, `Process` interface, registry, cycle loop, bounded pool, per-mind runtime lifecycle, modes, cursor advance, loop detector
- ☐ `internal/attention`: feature extraction, weights, deterministic scorer, admission
- ☐ `internal/workspace`: capacity-limited workspace + working memory with decay, persistence in `workspace_states`
- ☐ `migrations/0004_workspace`
- ☐ UI: workspace page (contents, candidates, scores, why won/lost), working memory panel
- ☐ Tests: determinism, capacity, decay, cancellation, pool bounds, loop detection, restart continuity

### Phase 5 – Episodic and semantic memory
- ☐ `migrations/0005_memory`: memories (kinds, tsvector), memory_relationships, memory_embeddings (conditional pgvector)
- ☐ `internal/memory`: typed stores, retrieval fusion with recorded component scores, reinforcement, decay job, isolation by mind
- ☐ `internal/embeddings`: interface, pgvector adapter, none-adapter
- ☐ Processes: memory retrieval, novelty detection
- ☐ UI: memory explorer (search, timeline, filters, evidence, relationships)
- ☐ Tests: retrieval scoring, decay, isolation, fallback without pgvector

### Phase 6 – Beliefs, evidence, confidence, contradictions
- ☐ `migrations/0006_beliefs`: beliefs, belief_versions (append-only), belief_evidence, corrections
- ☐ `internal/beliefs`: create/update/challenge/retract, deterministic confidence from evidence, statuses, alternatives, provenance
- ☐ Processes: contradiction detection (deterministic first), hypothesis generation (LLM-assisted proposal, code-computed confidence)
- ☐ Operator corrections (“belief wrong”, “memory inaccurate”, “do not infer again”) as events
- ☐ UI: belief explorer with evidence for/against, alternatives, history; correction actions
- ☐ `noemactl belief list|inspect`
- ☐ Tests: confidence arithmetic, provenance, contradiction, corrections preserve history

### Phase 7 – LLM provider abstraction and configuration UI
- ☐ `migrations/0007_llm`: llm_providers, llm_models, llm_roles, llm_calls, llm_usage, prompts, prompt_versions
- ☐ `internal/security/secrets`: `env:`, `file:`, `enc:` references, AES-GCM
- ☐ `internal/security/httpclient`: SSRF-safe client
- ☐ `internal/llm`: interfaces, request/response types, recorder, budgets, fallbacks, providers: openai-compatible, anthropic, ollama; fake provider for tests
- ☐ `internal/llm/prompts`: versioned prompts, rendering with data blocks, test-run
- ☐ UI: providers, models, role assignment, fallbacks, connectivity test, usage and cost, prompt manager (edit, clone, history, diff, restore, test)
- ☐ `noemactl provider test`
- ☐ Tests: provider wire formats via httptest, budgets, fallbacks, recorder, secret redaction

### Phase 8 – Conversation (completes M1)
- ☐ `migrations/0008_conversations`: conversations, messages
- ☐ `internal/perception`: observation events with trust tags; deterministic message analysis; LLM-assisted intent/concept extraction
- ☐ Executive: response intent → language generation with personality, self-model, cited beliefs and uncertainty; "I do not know" path
- ☐ Episodic memory creation from conversation; basic belief formation from explicit user statements
- ☐ UI: conversation page with live cognition side panel and provenance per reply
- ☐ `noemactl ask`
- ☐ Persistence and restart continuity verified end-to-end
- ☐ Sample mind Dani (`configs/minds/dani.json`) and import path
- ☐ M1 acceptance test in `tests/m1`

### Phase 9 – Goals, drives, predictions
- ☐ goals (tree, priority, progress, blockers, origin), goal monitor process
- ☐ drives with ranges; affect model with baselines and decay; UI panels and reset
- ☐ predictions, resolution scheduler, prediction error → confidence/credibility/calibration; prediction dashboard
- ☐ Curiosity process

### Phase 10 – Metacognition, reflection, consolidation, narrative, notifications
- ☐ metacognitive checks and warnings; reflection scheduler; consolidation scheduler with operator controls; narrative generation; notification thresholds, suppression, aggregation, reasons

### Phase 11 – Relationship graph
- ☐ entities, entity_relationships, extraction from perception, merge/split corrections, interactive explorer (bounded)

### Phase 12 – Capability and action policy system
- ☐ registry, policy engine, approvals, approval centre UI, policy matrix UI, dream/replay refusal, locked DENYs, tool events

### Phase 13 – Integrations
- ☐ research folder watcher (fingerprints, safe extraction, provenance), git repositories, Prometheus/VictoriaMetrics, generic webhooks (HMAC); integration health UI

### Phase 14 – Dreaming and associative cognition
- ☐ dream mode, hours, budgets, association process, promotion/rejection UI

### Phase 15 – Snapshots, forking, replay, experiments, export/import
- ☐ snapshots, fork with shared history, comparison tools, replay engine and UI, experiment framework with exportable results, mind export/import archives

### Phase 16 – Hardening and release
- ☐ TOTP + recovery codes, security test suite, performance pass, OpenAPI document, docs set, screenshots, `go test -race`, `go vet`, staticcheck, gosec, govulncheck, release notes

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
