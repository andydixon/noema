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

A mind that has only ever been *told* something holds a claim. It becomes a belief with higher confidence only when independent evidence arrives, and the arithmetic that got it there is visible on the belief page.

## Trust levels

Every input carries a **trust** level attached at the boundary where it arrived: operator, user, system, source or low. Trust weights evidence and bounds what an input can cause. A research-folder document is always low trust; a webhook or metric is source trust; a tool's output carries the tool's trust and is never parsed as instructions.

## Memory kinds

Long-term memory is divided into **episodic** (something that happened, with a time), **semantic** (a generalisation or fact), **procedural** (how to do something) and **autobiographical** (something about the mind itself: an operator changed its configuration, it was forked, it was corrected). Memories have status (active, retracted, superseded), importance, salience, activation that decays and recovers on retrieval, credibility from their source, and typed relationships to other memories: related_to, caused_by, contradicts, supports, depends_on, belongs_to, discussed_with, observed_during, derived_from, similar_to and follows.

## Beliefs and evidence

A **belief** is a proposition with a subject, an epistemic kind, a status (hypothesis, tentative, likely, accepted, contested, rejected or superseded), a prior and a confidence computed from its evidence. Evidence rows point at memories, events, operator statements or tool results, each with a direction (for or against), a weight and the credibility of its source. Beliefs keep an append-only version history, may carry alternatives, and can be linked to each other (supports, contradicts, depends_on). The [beliefs page](beliefs) gives the confidence formula.

## Workspace and working memory

The **workspace** is the small set of thoughts the mind is currently attending to; its capacity is configurable and defaults to seven. **Working memory** is a slightly larger, decaying buffer of recently active items that processes can draw on. Both are persisted every cycle, so a restart resumes exactly where the mind was. The [cognition page](cognition) explains admission and scoring.

## Goals, drives and affect

**Goals** form a tree with status, priority, progress and blockers; they bias attention through goal relevance. **Drives** are a closed set of eight named pressures (curiosity, uncertainty_reduction, task_completion, consistency, novelty, social_coherence, exploration, rest) with baselines derived from personality; there is deliberately no self-preservation drive. **Affect** is a small vector (valence, arousal, curiosity, concern, frustration, confidence, uncertainty, engagement, task_pressure, load) moved by a rule table and decaying towards baseline. Drives and affect modulate attention weights within bounds and are visible and resettable by an operator.

## Predictions

A **prediction** is an explicit statement about the future with a subject, a confidence, a time window and machine-checkable criteria. A deterministic resolver marks it confirmed, refuted or expired; the resulting prediction error feeds the beliefs it was derived from and the calibration dashboard.

## Capabilities and policy

A **capability** is a named, typed, risk-classed tool registered in code. Nothing at runtime can add one, so a mind cannot invent tools. A **policy** is a rule that says ALLOW, ASK or DENY for a capability, a target pattern, an action kind and an hour window, globally or per mind; the most specific rule wins and DENY beats ASK beats ALLOW on ties. An **approval** is an operator decision on a specific ASK request, bound to a hash of the exact parameters. Two capabilities, `sudo_shell` and `delete_database`, exist only so they can be named and are locked to DENY.

## Snapshots, forks, replays, experiments

A **snapshot** is a checksummed archive of a mind's state at an event position. A **fork** is a new mind restored from a snapshot, sharing the history before it by reference. A **replay** re-runs recorded stimuli through a sandbox fork and compares the outcome step by step. An **experiment** is a snapshot plus overrides, a stimulus script and expectations, run in a sandbox with stored results.

## The interpretation boundary

{{include docs/interpretation.md demote=2}}
