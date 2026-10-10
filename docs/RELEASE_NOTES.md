# Noema 0.3.0

Third release: volition, planning and cognitive research (PLAN.md §8), on top of the 0.2.0 epistemic review and Amazon Bedrock support.

## What you get

- A daemon (`noemad`) hosting many minds, each with identity, personality, configuration, working memory, workspace, memories, beliefs with evidence arithmetic, goals, drives, affect, predictions, self-model, narrative, notifications and an append-only cognitive history.
- An inspectable model of volition: **wants** with origin, strength, urgency and decay; **want conflicts** with both sides and the recorded reason one was preferred; **commitments** (do or refrain, optionally unless approved) with due dates, expiry, beneficiary and normative weight; **intentions** with sources, expected result, confidence, priority, blocked reasons and subordinate intentions; and an append-only ledger of why each want did or did not become an intention ([volition.md](volition.md)).
- Explicit **plans**: dependency graphs of typed steps with assumptions, expectations, preconditions, budgets, alternatives and completion criteria, validated by Go code against the registered capability vocabulary; a scheduler-owned executor that claims steps with conditional updates, acts only through the capability layer, verifies outcomes by observation, records prediction errors, replans into new revisions while keeping the old plan as history, recovers after restarts without repeating non-idempotent actions, and learns what worked as procedural memory ([planning.md](planning.md)).
- Capability negotiation: structured **capability requests** (scope, duration, operation cap, reason, evidence, risk, mitigation, fallback, alternatives) decided only by named operators into **scoped grants** that expire, count operations and restrict targets, parameters and access; a frozen registry with access, side-effect, idempotency, timeout and cost metadata; typed `service.status`, `service.restart`, `logs.query` (personal data masked), `host.disk.usage`, `host.memory.status`, `host.process.list` and read-only `browser.navigate`, `browser.read`, `browser.links`.
- Forensic **"why?"** explanations built only from recorded state, conservative **attribution** (caused, followed, independent, unknown), introspection intents for past beliefs, lineage and counterfactuals, and every introspective reply scored against provenance ([agency.md](agency.md)).
- A **cognitive research** framework: ablations and workspace overrides in forks only, self-model manipulations, counterfactual replays that omit recorded events, protocol metadata, operational measures with definitions, reproducibility status, agency metrics and metacognitive calibration ([cognitive-research.md](cognitive-research.md)). Noema does not claim that any of these measures establishes phenomenal consciousness or sentience.
- Deterministic attention and executive logic; language models only as replaceable services (OpenAI-compatible, Anthropic, Ollama, Amazon Bedrock) with recorded, replayable calls and encrypted secret references.
- Integrations, background cognition, study tools (snapshots, forks, comparison, replay, experiments), a complete web UI with wants, intentions, commitments, plans, agency timeline, why and research pages, a CLI (`noemactl`), an OpenAPI document, Prometheus metrics, audit log, roles, sessions, API tokens and TOTP.

## Security

Wants, intentions, commitments, plans and model output are data to the capability layer; nothing cognitive can set policy, approve, grant or revoke, and a mind cannot decide its own request. Forks no longer inherit policy rules unless the operator asks (`inherit_policies`), and work inherited mid-flight is quiesced. Dreams may want but cannot request authority, plan awake or act. Every invariant in PLAN.md §8.4 has a test in `tests/security`.

## Verification

`go vet`, `go test -race ./...`, staticcheck, gosec (G104 excluded) and govulncheck are clean with Go 1.26.8. govulncheck reports one module-level advisory (GO-2026-5932, `golang.org/x/crypto/openpgp`, no fix available) in a package Noema does not import; no reachable vulnerability is reported.

## Since 0.2.0

The 0.3.0 section of the changelog lists everything added, changed and fixed, including the defects found by the release review: a plan model consulted when a deterministic strategy applied, intention and plan transitions without compare-and-set, and map-order iteration in decisions and cycle output.

## Known limits

- Replays diverge honestly when retrieval scoring depends on wall-clock recency; a virtual clock for replay is future work.
- Introspection accuracy is scored lexically against recorded facts; a paraphrase with no shared terms scores as unsupported, never as supported.
- Browser write operations (click, fill, submit, download) are designed in ADR-0011 but not registered.
- No screenshots yet; the build environment has no browser.
- Embeddings need pgvector; without it retrieval is lexical and structural only.
