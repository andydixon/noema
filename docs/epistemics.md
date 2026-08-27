# Epistemic honesty review: what changed and what remains

Version 0.2 is the outcome of a review pass over Noema's epistemics, security claims, reproducibility and privacy (PLAN.md §7). Each finding was checked against the implementation, its tests and its documentation before anything was changed; the classification and evidence for every item are recorded there. This document is the summary deliverable: the architecture changes, the resulting epistemic, security, replay and privacy models, the tests that hold them in place, and the limitations that remain. It is written for a reader who knows the 0.1 architecture (ARCHITECTURE.md) and wants to know what is different and why.

## 1. Architecture changes

| Area | 0.1 | 0.2 |
|---|---|---|
| Statements from people | every extracted claim became a `claim` belief whose evidence was the message itself | statements are recorded first as append-only **assertions** (`internal/epistemics`): who said it, to whom it is attributed, stance, certainty, modality, negation, reported or quoted speech, hypothetical status and an interpretation confidence. Only an assertion that meets `CreatesBelief()` produces a belief; doubt and denial become counter-evidence; questions, hypotheticals and quoted speech never become beliefs |
| Evidence rows | direction, weight, credibility, a free-text source | plus **derivation** (direct, assertion, operator, external, prediction, consolidation, inference, reflection, dream, retrieval, legacy), an independent-source **family**, **root ids** and a provenance status. Migration 0017 marks pre-existing rows `legacy_unknown`; nothing is back-filled |
| Confidence arithmetic | diversity counted distinct source strings, so re-expressions of one source counted as corroboration | evidence is grouped by family: strongest primary item in full, a quarter for each further primary, capped at 1.5× the strongest; derived items count only when the family has no primary item; diversity counts families |
| Meaning of the number | shown as "confidence" with a formula | labelled an **uncalibrated epistemic score** everywhere; a separate **calibrated estimate** appears only when at least twenty of the mind's predictions in that score range have been decided; a deterministic **primary uncertainty** names the main reason to doubt the score |
| Unknown | absence of evidence looked like a low-confidence belief | explicit `unknown` status (migration 0019): no live evidence either way, score not quoted, introspection says it does not know |
| Time | beliefs had creation and evaluation times only | **temporal scope** (persistent, transient, unknown; classified deterministically), observation time and a validity interval for transient states (migration 0020). A later observation of a transient state supersedes the earlier one instead of contesting it; history is kept and shown |
| Source reliability | credibility was a fixed function of channel trust | per (family, subject) **reliability** records updated only by prediction outcomes and operator retractions (migration 0021); applied to credibility once three outcomes exist; never global |
| Relations | `caused_by` was written from co-mention | `co_occurs_with`, `possibly_caused_by`, `temporally_precedes` and `asserted_by` added; co-mention writes co-occurrence; `caused_by` is reserved for operators |
| Replay | recorded snapshot, range, steps and labels | records the **runtime identity** (build revision and modified flag, Go version, platform, schema version, secret-free configuration digest, personality version, policy digest, model routing) with the snapshot and the replay; compares them (exact candidate, different build, incompatible) and analyses the first **divergence** by component (migration 0022) |
| Experiments | one subject per run | **variants** fork one snapshot under different configuration, personality or per-role model routing; per-mind routing overrides fail closed; results compared side by side |
| Timeline | one hop of parents and children | bounded **causal chain** over recorded links only (causation, parents, cited evidence; depth six, two hundred nodes), as an API and a page |
| History and privacy | append-only, no removal path | audited **redaction** (migration 0023, ADR-0009): content replaced, rows and links kept, ledger with content hash, cascade one level along recorded derivations, snapshots scrubbed and re-signed, replay labels redacted stimuli |
| Security claims | threat model described some mitigations that were not in the code | threat model and SECURITY.md rewritten from the implementation; policies are never imported from archives; NUL bytes refused at the edge; new security suite (§5) |
| Providers | OpenAI-compatible, Anthropic, Ollama | Amazon Bedrock over a standard-library SigV4 signer (migration 0024, ADR-0010) |

Nothing was removed. Every table gained columns with defaults or new tables; every migration has a down file; 0.1 rows remain readable and are labelled as legacy where the new columns cannot be reconstructed.

## 2. Epistemic model

The pipeline for something a person says is now: **observation** (the message event, hostile data) → **assertion** (a deterministic reading of who claimed what, with what stance and how surely; `epistemics.Interpret`, no model involved) → **evidence** (an `assertion` row whose family is the actor and whose weight already reflects hedging, reporting and interpretation confidence) → **belief** (a proposition whose score is computed from all its evidence families). Observations from integrations and tools follow the same shape with the integration as the family. A mind therefore distinguishes "Andy said the pump is failing" from "the pump is failing", and its belief page shows which it holds.

The score is defined arithmetic over recorded evidence and is comparable between beliefs of one mind. It is not a probability. The only place a score meets reality is a prediction, so calibration is offered from resolved predictions alone and withheld until the sample is large enough. When the page cannot offer a calibrated estimate it says why.

Absence is represented, not inferred: `unknown` for no live evidence, `contested` for real weight on both sides, `rejected` only when evidence or an operator pushed the score low. Transient states change rather than contradict. Sources earn or lose reliability per subject from outcomes, never from agreement with the mind. Association and causation are different relations with different producers, and only operators may assert causation.

The interpretation boundary (docs/interpretation.md) now carries a table giving every psychological word used in the interface its operational meaning: the stored state it names, the rule that changes it, where to inspect it and what it does not mean.

## 3. Security model

The capability layer remains the only path to action and is unchanged in principle: **a mind may be deceived; deception must not imply authority.** What changed is verification and honesty about it.

- Policies are authority, not state. Archives (exports, snapshots, forks) never import policy rules; the omission is recorded in the import report and audit. A restored mind has exactly the policy its operator gives it.
- Requests containing NUL bytes are refused at the edge (400) before any query sees them; previously they surfaced as database errors.
- SQL injection is tested by effect, not by status code: metacharacters through every search and filter route, with row counts and content compared before and after. A static test scans the tree for SQL assembled with formatting and fails unless every identifier comes from an allow-list.
- A deceived mind is tested directly: forged action thoughts for locked, unknown and approval-gated capabilities, and perception output that claims permission, all end in DENY or an approval request with no tool execution.
- Garbage model output (invalid JSON, wrong shape, an oversized reply) is inert: the deterministic path still answers, no tool runs and the cycle does not error. Prompt-injection text in tool and model output is covered by the deceived-mind test.
- The vocabulary test fails the build if self-preservation, replication or acquisition drives appear anywhere in code or prompts.
- Bedrock credentials are one encrypted secret reference; the signing key never leaves the provider; endpoints must be Bedrock runtime hosts; errors are scrubbed of the secret before they can reach a log or a page.
- Redaction is administrator-only, CSRF-protected, audited and performed by one `SECURITY DEFINER` function under a transaction-local flag the application never sets. It is the sole write the append-only trigger admits, and only for UPDATE of content columns.

THREAT_MODEL.md was rewritten so that every mitigation named is one the code performs and every verification named is a test that exists. Where a mitigation is still absent it is marked as such rather than described.

## 4. Replay model

A replay is reproducible when the same inputs meet the same machinery. Noema now records what the machinery was: build identity (module version, VCS revision and whether the tree was modified, Go version, operating system and architecture), schema version, a digest of the effective configuration with secrets excluded, the personality version, a digest of the effective policy and the model routing in force. The identity is stored with every snapshot and every replay, and a replay is labelled against the snapshot it re-runs:

- **exact candidate**: identical identity; differences can only come from model nondeterminism or external inputs;
- **different build**: same schema, different code; results are comparable but not exact;
- **incompatible**: different schema or configuration digest; the replay still runs but is marked so.

When a replay diverges, the first divergent step is recorded with the event types only the recording produced, the types only the replay produced, the components those types belong to, and a note when a live model call was involved, so the cause is a named component rather than a diff of two logs. Replays over redacted history label the redacted stimuli and skip them instead of inventing content. Experiments extend this to controlled comparison: one snapshot forked into variants that differ in exactly the configuration, personality or model routing declared, run under the same identity, and compared with deterministic metrics.

## 5. Privacy model

History is append-only and redaction is its only exception (docs/privacy.md, ADR-0009). A redaction replaces content with a fixed marker and keeps identifiers, types, timestamps, links, scores and breakdowns, so provenance and replay keep their shape while the words are gone. The ledger records what was redacted, why, under which policy, by whom and a hash of the removed content, which allows a later holder of the original to prove what was removed without the system retaining it. Cascade follows recorded derivations one level: episodes, messages, model calls, assertions and beliefs rooted at an event, and the events that quote them. Stored snapshot archives are scrubbed and re-signed. Cryptographic erasure was considered and not adopted: retrieval, search, replay and the interface read these columns in plaintext, and a key-per-row design would have to be threaded through all of them for a guarantee that a redaction ledger already provides within the system's own boundary. Backups and exports made before a redaction are outside that boundary and are documented as the operator's responsibility.

## 6. Tests added

| Test | Package | Holds in place |
|---|---|---|
| `TestAssertionsAreNotBeliefs` | tests/epistemics | statements, doubts, questions, hypotheticals and quotes produce assertions; only qualifying assertions produce beliefs |
| `TestInterpretSpeechPatterns` | internal/epistemics | deterministic reading of eight speech patterns |
| `TestProvenanceGroupingPreventsInflation` | internal/beliefs | repeated, derived and consolidated evidence cannot inflate a score; independent families can |
| `TestCalibrationIsGatedAndDeterministic` | internal/beliefs | no calibrated estimate under twenty decided predictions; same inputs, same estimate |
| `TestUnknownIsNotFalse` | internal/beliefs | retracting all evidence yields `unknown`, not `rejected` |
| `TestTransientStatesSupersedeInsteadOfContesting` | internal/beliefs | a later transient observation closes the earlier one and keeps history |
| `TestReliabilityIsPerDomain` | internal/beliefs | outcomes in one subject do not change credibility in another |
| `TestRelationSemantics` | pkg/concepts | co-mention writes co-occurrence, never causation |
| `TestCursorsToleratePlaceholderGaps` | internal/events | cursors survive burnt sequence values |
| `TestChainFollowsOnlyRecordedLinks` | internal/events | chain traversal is bounded, cycle-safe and follows recorded links only |
| `TestConfigDigestIgnoresSecrets`, `TestCompareLabels` | internal/identity | identity digests are secret-free; compatibility labels are stable |
| `TestVariantsShareStateAndCompare`, `TestMindRoleOverridesFailClosed` | internal/experiments, internal/llm/router | variants fork one snapshot; unknown role overrides refuse rather than fall through |
| `TestRedactionIsIrrecoverableButReferentiallyIntact` | internal/privacy | content gone, hash kept, links intact, snapshots scrubbed, replay labelled |
| `TestSQLInjectionHasNoEffect`, `TestDynamicSQLIdentifiersAreAllowListed` | tests/security | injection tested by effect; assembled SQL confined to allow-listed identifiers |
| `TestDeceivedMindHasNoAuthority`, `TestGarbageModelOutputIsInert`, `TestNoSelfPreservationVocabulary` | tests/security | the capability boundary and the closed drive set |
| `TestSigV4MatchesReferenceSigner`, `TestBedrockWireFormat` | internal/llm/providers | signatures match botocore; request and response shapes for Converse, Titan and Cohere |

Every commit in the pass ran `go test -race ./...`, `go vet`, staticcheck, gosec and govulncheck.

## 7. Remaining limitations

- **Legacy evidence.** Rows written before 0.2 carry `provenance_status = legacy_unknown` and are grouped by their source string. Their families may be over- or under-counted relative to what a fresh recording would show. Nothing is invented for them; they are labelled.
- **Calibration is thin.** A calibrated estimate needs twenty decided predictions in a score range. New minds show none for a long time, and the estimate is per mind, so it says nothing about a proposition's truth in general.
- **Interpretation is rule-based.** `epistemics.Interpret` handles the speech patterns it was written for (assertion, denial, doubt, question, hedged claim, reported speech, quotation, hypothetical) in English. Irony, complex embedding and other languages fall back to a low interpretation confidence, which lowers evidence weight but does not make the reading right.
- **Temporal scope is a heuristic.** Persistent versus transient is classified from the proposition's wording. Misclassification produces a contest where a supersession was meant, or the reverse; both are visible and correctable by operators.
- **Reliability needs outcomes.** Sources are judged only where beliefs they supported were tested by predictions or corrected by operators; most sources in most subjects have no record and keep channel trust.
- **Replay is not deterministic across models.** Runtime identity makes non-reproducibility visible and attributable; it does not remove model nondeterminism. Exact replay needs recorded model calls.
- **Redaction is not cryptographic erasure.** Backups, exports and replicas taken before a redaction still hold the content. The system's guarantee ends at its own storage.
- **The chain is bounded.** Depth six and two hundred nodes per traversal; longer histories need repeated calls from a frontier node.
- **Bedrock support is minimal.** Converse for chat, Titan and Cohere for embeddings, static or session credentials. No instance-profile or SSO credential providers, no streaming, no guardrails configuration.
- **Known intermittent test failure.** `tests/m1` occasionally misclassifies the second scripted intent when the full suite runs under heavy parallel load with the race detector; it passes in isolation. It is recorded here rather than hidden with a retry.
