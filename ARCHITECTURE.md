# Noema architecture

This document explains how Noema is built and why. Decisions with alternatives are recorded as ADRs in `docs/adr/`.

## 1. Runtime shape

`noemad` is a single long-running process containing:

- the HTTP API and web UI (server-rendered templates, SSE for live updates),
- the **mind manager**, which owns one *mind runtime* per enabled mind,
- the **scheduler**, which owns every background goroutine (bounded worker pools, tickers, integration pollers),
- the **integration manager** (research folder, git, metrics, webhooks),
- the LLM and embedding provider registries,
- the capability/policy layer,
- storage (PostgreSQL via pgx, explicit SQL, embedded migrations).

`noemactl` is a thin client of the HTTP API plus a few offline commands (`migrate`, `user create`).

There is no global mutable state. A `Runtime` struct is assembled in `cmd/noemad` and dependencies are passed explicitly.

## 2. Event system

Everything important is a **cognitive event** (`pkg/events`). The table `cognitive_events` is append-only, enforced by a database trigger that rejects UPDATE and DELETE (ADR-0002). Each event has:

- `id` UUIDv7 (time-ordered, globally unique), `seq` (monotonic per database, used for cursors and SSE resume),
- `mind_id`, `type`, `source` (process or integration name), `occurred_at`, `recorded_at`,
- `parent_ids[]`, `causation_id` (the event that directly caused this one), `correlation_id` (the cognitive cycle or conversation it belongs to),
- `salience`, `confidence`, optional `valence`, `arousal`,
- `evidence[]` – typed references `{kind, id}` to memories, beliefs, other events, entities, documents, LLM calls,
- `summary` – one human-readable sentence, `payload` – immutable typed JSON.

The in-process **event bus** is a wake-up and fan-out mechanism only. Durable consumers (the mind runtime) read from the table by `seq` cursor, so nothing is lost across restarts and every cycle is replayable. SSE clients receive events from the bus with per-client bounded buffers.

## 3. Mind runtime and the cognitive cycle

Each enabled mind has a runtime with a single **cycle loop** goroutine. A cycle is triggered by new events for that mind, a timer (mode-dependent), or an operator command. A cycle:

1. **Load state** – `BrainState`: identity/personality snapshot, working memory, workspace contents, active goals, drives, affect, self-model, mode, budgets, and the new events since the last cursor.
2. **Run cognitive processes** concurrently under a bounded pool. Each implements

   ```go
   type Process interface {
       Name() string
       Observe(ctx context.Context, s *BrainState) ([]ThoughtCandidate, error)
   }
   ```

   Processes are pure with respect to the state they are given; they may read storage and call LLM services through the state's service handles, and they return candidates. A failing or slow process is isolated: it cannot break the cycle or the daemon.
3. **Attention scoring** – deterministic weighted sum over candidate features (salience, urgency, novelty, affective significance, goal relevance, predicted consequence, confidence, unresolved contradiction, operator priority, context relevance). Weights are per-mind configuration. Every candidate and its component scores are written as a `thought_candidate` event.
4. **Workspace admission** – the top *K* candidates (capacity configurable, default 7) enter the workspace, replacing the weakest existing items when full. Admissions are `thought_selected` events, the new workspace is a `workspace_broadcast` event, and the workspace snapshot is persisted.
5. **Executive** – admitted thoughts are dispatched by kind to handlers: create/update belief, create hypothesis, create prediction, create goal, update narrative, respond to user, request action, raise notification, record reflection. Handlers emit events and update stores.
6. **Housekeeping** – working memory decay, drive and affect updates, budget accounting, loop detection, cursor advance. All within one transaction per cycle where possible; the cursor moves only after the cycle's events are committed.

Cognitive modes (`awake`, `focused`, `idle`, `reflecting`, `consolidating`, `dreaming`, `paused`) change which processes run, their budgets and the cycle timer. Transitions are deterministic and recorded.

**Runaway protection**: per-mind cycle rate limits, per-cycle LLM budgets, and a loop detector that fingerprints candidates (process + kind + subject). If the same fingerprint recurs *N* times within a window without a state change, further instances are suppressed and a `metacognition` warning event is emitted.

## 4. Memory

Distinct kinds share a storage table but are distinct Go types and distinct epistemic categories:

- **Working memory** – in-process, capacity-limited (default 9 items), activation decays each cycle, persisted as part of the workspace snapshot for restart continuity.
- **Episodic** – things that happened: time, participants, context, source, confidence, affective significance, links to originating events.
- **Semantic** – propositions with confidence, provenance, supporting and contradicting evidence, update history.
- **Procedural** – named procedures with steps, preconditions, success/failure counts.
- **Autobiographical** – significant self-history items, marked by importance and curated by reflection.

Every memory carries `importance`, `salience`, `activation` (accessibility), `retrieval_count`, `last_retrieved_at`, `source`, `source_credibility`, `epistemic_kind` and relationship edges. Decay reduces `activation`; nothing is deleted except by explicit operator retention policy.

**Retrieval** is a deterministic fusion: full-text rank (tsvector), optional vector similarity (pgvector, per-model tables), recency, activation, importance and graph proximity. Component scores are recorded on `memory_retrieved` events.

**Consolidation** runs on a schedule or on demand: clusters recent episodes by entity/predicate, asks the LLM to propose candidate generalisations *as structured JSON*, then computes confidence deterministically from the count, consistency and credibility of the supporting episodes. Results are new semantic memories linked to their sources; originals are untouched.

## 5. Beliefs, hypotheses, predictions

A **belief** is a proposition with `status` (hypothesis → tentative → likely → accepted, or contested/rejected/superseded), `confidence`, evidence for and against (typed links with weights), alternatives, and an append-only `belief_versions` history. Confidence updates are deterministic functions of evidence weights and source credibility; the LLM may *propose* a hypothesis or evidence relevance but never sets a confidence number directly.

**Contradiction detection** compares new propositions against existing beliefs (deterministic negation/entity-predicate matching first, LLM-assisted judgment for paraphrase) and emits `contradiction_detected` events which raise attention and lower confidence on both sides until resolved.

**Predictions** are explicit rows with a resolution window and machine-checkable criteria where possible (metric threshold, event occurrence) or operator/LLM-judged criteria otherwise. A scheduler resolves them, emits `prediction_resolved` / `prediction_error`, adjusts linked belief confidence, source credibility and the mind's calibration record.

## 6. Goals, drives, affect, self-model, narrative

- **Goals** form a tree with priority, status, progress, origin event and blockers. The goal monitor process emits candidates when goals stall or complete.
- **Drives** (curiosity, uncertainty reduction, task completion, consistency, novelty, social coherence, exploration, rest) are scalars within operator-configured min/max ranges. They bias attention weights and goal priority. There is no self-preservation or resource-acquisition drive and the code has no place to put one.
- **Affect** (valence, arousal, curiosity, concern, frustration, confidence, uncertainty, engagement, task pressure, load) is updated by deterministic rules from events and decays toward a personality-defined baseline. It biases attention, memory significance and communication style. It is inspectable and resettable.
- **Self-model** records capabilities actually granted, known limitations, current activities/goals and confidence by domain; the response generator is given it so the mind cannot truthfully claim abilities it lacks.
- **Narrative** is a short, regenerated summary ("doing / why / recently / resolving / next") stored as events, never hidden chain-of-thought.

## 7. Metacognition, reflection, dreaming

- **Metacognition** processes run deterministic checks (insufficient evidence, single-source reliance, overconfidence relative to calibration, repeated failed hypotheses, fluent-but-unevidenced LLM output) and emit warning candidates that reduce confidence or spawn investigation goals.
- **Reflection** is scheduled: it reviews recent mistakes, abandoned hypotheses, belief changes, prediction errors and operator corrections, producing explicit `reflection` events and possibly procedural memories.
- **Dreaming** is an optional idle mode with its own compute/LLM budget and allowed hours. It replays and associates memories and proposes low-confidence hypotheses. The dreaming context carries a flag that the capability layer refuses outright, so dreams can never act.

## 8. Language models and prompts

`internal/llm` defines `LanguageModel` and `Embedder` interfaces. Providers (OpenAI-compatible, Anthropic, Ollama/llama.cpp, Amazon Bedrock via a standard-library SigV4 signer) are configured in the database with secret *references*, not secrets. **Roles** (classify, extract, hypothesise, summarise, narrate, respond, embed…) map to a primary model and fallbacks. Every call is recorded (`llm_request`/`llm_response` events plus `llm_calls` with prompt version, parameters, seed, usage, latency, cost) for inspection and replay.

Prompts are named and versioned rows; templates receive structured data. Untrusted content is passed in clearly delimited data blocks with an explicit instruction that it is data, and model output is parsed into typed JSON and validated. Model output never becomes an action without passing through the capability layer, and never becomes a belief without an evidence link.

## 9. Capabilities and policy

All external effects go through `capabilities.Invoke(ctx, mind, capability, target, params)`. The policy engine evaluates rules scoped globally, per mind, per capability, per target pattern and per time window; the most specific rule wins and at equal specificity DENY > ASK > ALLOW. Results are `policy_decision` events. `ASK` creates an approval request with reason, parameters, evidence, risk and expiry; operators approve once, approve for a session, deny, deny with explanation, or modify parameters. Execution, denial and failure are all events.

## 10. Attention and workspace inspectability

The UI shows every candidate of the latest cycle with its component scores and the reason it won or lost. Weights are editable per mind, and changes are audited. Scoring is pure Go and unit-tested for determinism.

## 11. Snapshots, forking, replay, experiments

A **snapshot** captures a mind's full state (identity, personality version, configuration, working memory, workspace, drives, affect, goals, beliefs, memories) plus the event `seq` at that point. A **fork** creates a new mind from a snapshot with a `forked_from` link; history before the fork is shared by reference, everything after diverges. **Replay** rebuilds `BrainState` from a snapshot and re-runs the cycle code over the recorded event sequence with LLM calls served from the recorded responses. Each replayed step is labelled *exact* (deterministic, or recorded response used) or *approximate* (live model call). **Experiments** wrap snapshot + stimuli + configuration + seed + expected observations and export results.

## 12. Storage

PostgreSQL 16 with explicit SQL through pgx. Numbered migrations in `migrations/`, applied by an embedded runner under an advisory lock. Append-only tables are protected by triggers. pgvector is optional: the embeddings migration creates vector tables only when the extension is available, and the runtime falls back to text and graph retrieval when it is not.

## 13. Security boundaries

1. Browser ↔ API: authentication, sessions, CSRF, roles, rate limits.
2. Ingested data ↔ cognition: trust levels, provenance, content treated as data, size and type limits, no execution.
3. Cognition ↔ LLM: prompts carry only what is needed; outputs validated; budgets enforced.
4. Cognition ↔ world: capability layer, policy, approvals, audit.
5. Mind ↔ mind: separate rows keyed by `mind_id`, repositories always scoped, explicit channels only.
6. Import ↔ system: schema-versioned, size-limited, validated, secret-free archives.

Details in `THREAT_MODEL.md`.

## 14. Deterministic vs LLM-assisted

| Deterministic (plain Go/SQL) | LLM-assisted |
|---|---|
| timestamps, ids, ordering | language understanding of messages and documents |
| attention scoring, workspace admission | concept and entity extraction |
| confidence arithmetic, decay, calibration | hypothesis proposal and alternative generation |
| policy evaluation, budgets, rate limits | paraphrase-level contradiction judgment |
| prediction resolution against metrics/events | summarisation, narrative, response wording |
| mode transitions, scheduling, retention | relevance judgment where text matching fails |
| provenance links, event relationships | consolidation candidates (confidence still computed) |

The LLM augments the architecture. It is never asked "what should I think about?" with the whole memory attached.
