This page defines the vocabulary the rest of the documentation uses. The words are borrowed from cognitive science because they name the *role* each component plays; the interpretation boundary at the end of this page says what that borrowing does and does not imply.

## Minds

A **mind** is the unit of identity and isolation. Each mind has a slug, a display name, a versioned personality, a cognitive configuration, a self-model, a status (active, disabled or archived) and its own timeline. Every state table in the database is scoped by mind, and no query on a mind-owned table runs without that scope; two minds hosted by the same daemon cannot see each other's memories, beliefs, events or archives. Minds can be created from templates, cloned, forked from snapshots, exported and imported.

A mind has a **mode** that changes what runs and how fast: `awake`, `focused`, `idle`, `reflecting`, `consolidating`, `dreaming` or `paused`. Mode changes are deterministic and recorded as events.

## Cognitive events

Everything of consequence is a **cognitive event**: an immutable row with a type, a category, a source, a summary, salience, confidence, optional valence and arousal, structured metadata and payload, evidence references, parent and causation links, and a monotonic sequence number per daemon. Categories are perception, cognition, memory, belief, prediction, goal, affect, action, llm, operator and system. The event table is append-only and a database trigger refuses updates and deletes, so history cannot be rewritten by application code, an administrator's click or a bug.

Events are the backbone of provenance: memories, beliefs and entities cite events; the mind's runtime reads events by sequence cursor so restarts lose nothing; replay re-injects recorded events; the interface shows every event with its causes and consequences.

## Epistemic kinds

Every proposition Noema stores says what kind of knowledge it is:

| Kind | Meaning | Starting confidence when it becomes a belief |
|---|---|---|
| observation | directly perceived through a channel | 0.60 |
| fact | verified against an authoritative source | 0.80 |
| memory | a recalled episode, subject to distortion | 0.60 |
| claim | asserted by someone else, unverified | 0.50 |
| hypothesis | a proposed explanation under evaluation | 0.25 |
| belief | held with confidence derived from evidence | 0.50 |
| prediction | an expectation about the future | 0.50 |
| assumption | taken as given without evidence | 0.40 |
| inference | derived by reasoning from other items | 0.25 |
| opinion | a preference or judgement, not truth-apt | 0.40 |

A mind that has only ever been *told* something holds a claim. It becomes a belief with a higher score only when independent evidence arrives, and the arithmetic that got it there is visible on the belief page.

## Observations, assertions and beliefs

Three things are kept apart that most systems blur. An **observation** is what arrived: a message, a document, a metric, a tool's output, with its channel and trust. An **assertion** is a deterministic reading of a statement inside an observation: who is attributed (the speaker, or someone they quote or report), their stance (asserts, denies, doubts, questions, supposes), how firmly they put it, whether it is negated, hypothetical, reported or quoted, and how confident the reading itself is. Assertions are append-only and recorded whatever happens next. A **belief** is the mind's own proposition, and an assertion reaches it only as one piece of evidence attributed to the person behind it; questions, hypotheticals and quoted speech create no belief, and expressed doubt counts against. "Andy said the pump is failing" and "the pump is failing" are therefore different rows, and the mind can hold the first with certainty while holding the second tentatively.

## Trust levels

Every input carries a **trust** level attached at the boundary where it arrived: operator, user, system, source or low. Trust weights evidence and bounds what an input can cause. A research-folder document is always low trust; a webhook or metric is source trust; a tool's output carries the tool's trust and is never parsed as instructions. Trust is the starting point for a source's credibility; a source also earns or loses **reliability** per subject from how the beliefs it supported fared against predictions and operator corrections, never from agreeing with the mind.

## Memory kinds

Long-term memory is divided into **episodic** (something that happened, with a time), **semantic** (a generalisation or fact), **procedural** (how to do something) and **autobiographical** (something about the mind itself: an operator changed its configuration, it was forked, it was corrected). Memories have status (active, retracted, superseded), importance, salience, activation that decays and recovers on retrieval, credibility from their source, and typed relationships to other memories and entities drawn from one shared vocabulary: `co_occurs_with` and `temporally_precedes` for what was seen together or in order, `possibly_caused_by` for a causal hypothesis the association process proposes, `caused_by` for an established cause that only an operator may assert, `asserted_by` for who said a thing, and `related_to`, `contradicts`, `supports`, `depends_on`, `belongs_to`, `discussed_with`, `observed_during`, `derived_from`, `similar_to` and `follows`. Association is never written as causation.

## Beliefs and evidence

A **belief** is a proposition with a subject, an epistemic kind, a status (unknown, hypothesis, tentative, likely, accepted, contested, rejected or superseded), a prior and an **epistemic score** computed from its evidence. The score is a deterministic ranking of support given what the mind has seen, not a probability that the proposition is true; a calibrated estimate is shown separately, and only when enough of the mind's own predictions have been decided to justify one. Evidence rows point at memories, events, assertions, operator statements, tool results or predictions, each with a direction (for or against), a weight, the credibility of its source, and its **provenance**: a derivation (direct, assertion, operator, external, prediction, consolidation, inference, reflection, dream, retrieval) and a **family**, the independent source it ultimately rests on. Support is counted per family, so one source repeating itself or a consolidation re-expressing it cannot inflate a score. **Unknown** is a real status: a belief with no live evidence either way is neither tentatively true nor false, and the mind says it does not know. Every belief has a **temporal scope** (persistent, transient or unknown) and, for transient states, a validity interval: a later observation of a changed state supersedes the earlier one instead of contesting it, and the history of states is kept. Beliefs keep an append-only version history, may carry alternatives, and link to each other (supports, contradicts, depends_on). The [beliefs page](beliefs) gives the arithmetic and the meaning of every number.

## Workspace and working memory

The **workspace** is the small set of thoughts the mind is currently attending to; its capacity is configurable and defaults to seven. **Working memory** is a slightly larger, decaying buffer of recently active items that processes can draw on. Both are persisted every cycle, so a restart resumes exactly where the mind was. The [cognition page](cognition) explains admission and scoring.

## Goals, drives and affect

**Goals** form a tree with status, priority, progress and blockers; they bias attention through goal relevance. **Drives** are a closed set of eight named pressures (curiosity, uncertainty_reduction, task_completion, consistency, novelty, social_coherence, exploration, rest) with baselines derived from personality; there is deliberately no self-preservation drive. **Affect** is a small vector (valence, arousal, curiosity, concern, frustration, confidence, uncertainty, engagement, task_pressure, load) moved by a rule table and decaying towards baseline. Drives and affect modulate attention weights within bounds and are visible and resettable by an operator.

## Predictions

A **prediction** is an explicit statement about the future with a subject, a confidence, a time window and machine-checkable criteria. A deterministic resolver marks it confirmed, refuted or expired; the resulting prediction error feeds the beliefs it was derived from and the calibration dashboard.

## Capabilities and policy

A **capability** is a named, typed, risk-classed tool registered in code. Nothing at runtime can add one, so a mind cannot invent tools. A **policy** is a rule that says ALLOW, ASK or DENY for a capability, a target pattern, an action kind and an hour window, globally or per mind; the most specific rule wins and DENY beats ASK beats ALLOW on ties. An **approval** is an operator decision on a specific ASK request, bound to a hash of the exact parameters. Two capabilities, `sudo_shell` and `delete_database`, exist only so they can be named and are locked to DENY.

## Snapshots, forks, replays, experiments

A **snapshot** is a checksummed archive of a mind's state at an event position, recorded with the **runtime identity** that produced it: build, schema version, a secret-free configuration digest, personality version, policy digest, model routing and prompt versions. A **fork** is a new mind restored from a snapshot, sharing the history before it by reference; policies are never carried across, because authority is not state. A **replay** re-runs recorded stimuli through a sandbox fork, compares the outcome step by step, and labels itself an exact candidate, a different build or incompatible according to how its identity compares with the snapshot's. An **experiment** is a snapshot plus overrides, a stimulus script and expectations, run in a sandbox with stored results; its **variants** fork the same snapshot under different configuration, personality or per-role model routing and are compared deterministically.

## Redaction

History is append-only and content can still be removed. A **redaction** replaces a row's content with a marker while keeping its identifiers, links, scores and timestamps, writes an append-only ledger entry with a hash of what was removed, cascades along recorded derivations, scrubs stored snapshots and appends an event so that the removal is itself part of history. The [privacy page](privacy) sets out what it does and does not guarantee.

## The interpretation boundary

{{include docs/interpretation.md demote=2}}
