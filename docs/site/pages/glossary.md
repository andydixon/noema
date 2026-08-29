Short definitions of the terms used across this documentation, in alphabetical order.

## A to D

Affect
:   A ten-dimensional vector (valence, arousal, curiosity, concern, frustration, confidence, uncertainty, engagement, task pressure, load) moved by a rule table, decaying to a personality-derived baseline, and modulating attention within bounds.

Assertion
:   The deterministic reading of a statement someone made: attributed actor, stance, certainty, modality, negation, reported or quoted speech, hypothetical status and an interpretation confidence. Append-only; becomes belief evidence only when it qualifies.

Approval
:   An operator decision on an ASK policy outcome, bound to a hash of the exact invocation. Once, for a session, with changes, or deny.

Archive
:   A checksummed JSON document of a mind's state used by snapshots, export and import. Contains no secrets and no event history.

Attention
:   Deterministic scoring of thought candidates over ten features with per-mind weights, producing the order of admission to the workspace.

Belief
:   A proposition with a subject, epistemic kind, status, prior and an epistemic score derived from evidence with a visible formula.

Calibrated estimate
:   The hit rate of a mind's resolved predictions whose stated confidence fell in the same range as a score; shown only when at least twenty have been decided.

Causal chain
:   The bounded walk (six hops, two hundred events) backwards and forwards along the links events actually record: caused, parent and cited.

Capability
:   A named, typed, risk-classed tool registered in code. The only way a mind can act.

Cognitive event
:   An immutable record of something that happened in or to a mind: the unit of history and provenance.

Consolidation
:   Scheduled generalisation of clusters of episodes into semantic memories and evidenced beliefs, reviewable by operators.

Contradiction
:   A deterministic finding that two propositions on one subject conflict. Marks both contested; resolved by evidence or an operator.

Cycle
:   One pass of the cognitive loop: read events, observe, score, admit, dispatch, commit.

Derivation
:   How an evidence row came to exist: direct, assertion, operator, external, prediction, consolidation, inference, reflection, dream or retrieval. Derived rows re-express information and do not corroborate it.

Drive
:   One of eight named pressures with a personality-derived baseline. No self-preservation drive exists.

## E to M

Epistemic kind
:   The label every stored proposition carries saying what kind of knowledge it is: observation, fact, memory, claim, hypothesis, belief, prediction, assumption, inference or opinion.

Epistemic score
:   The deterministic, uncalibrated output of the confidence arithmetic over recorded evidence, bounded to [0.02, 0.98]. A ranking of support, not a probability of truth.

Evidence
:   A row pointing at a memory, event, assertion, operator statement, tool result, prediction or belief, with direction, weight, source credibility, derivation, family and root ids. The only thing that moves a score.

Family
:   The independent source an evidence row ultimately rests on: a person, an integration, an operator, a prediction. Support is counted per family so repetition cannot inflate a score.

Experiment
:   A snapshot plus overrides, a stimulus script and expectations, run in a disabled subject mind with stored, exportable results.

Fork
:   A new mind restored from a snapshot with all row ids remapped; history before the snapshot is shared by reference.

Goal
:   A node in a mind's goal tree with status, priority, progress, blockers and an origin event.

Integration
:   An external source (research folder, git, Prometheus, webhook) polled or receiving deliveries, producing provenance-tagged observations.

Intent (response)
:   The structured, state-derived plan for a reply: what was asked, what answers it and how confidently, what is unknown. Rendered to language deterministically or by a model.

Locked capability
:   A capability whose effect is DENY regardless of policy and cannot be relaxed from any interface: `sudo_shell` and `delete_database`.

Master key
:   The 32-byte key (`NOEMA_MASTER_KEY`) under which provider keys, integration secrets and TOTP secrets are encrypted at rest.

Memory (long-term)
:   An episodic, semantic, procedural or autobiographical item with provenance, importance, decaying activation and typed relationships.

Metacognition
:   The process that watches for loops, overconfidence, unresolved contradictions and budget pressure, producing warnings and notifications.

Mind
:   The unit of identity and isolation: personality, configuration, state, history and runtime.

Mode
:   The runtime state of a mind: awake, focused, idle, reflecting, consolidating, dreaming or paused.

## N to Z

Narrative
:   A short, templated (optionally model-worded) statement of what the mind is attending to, working on and waiting for; updated after cycles.

Observation
:   A perception event carrying external data with its source, trust level and reference.

Policy
:   A rule producing ALLOW, ASK or DENY for a capability, target pattern, action kind and hour window, globally or per mind.

Prediction
:   An explicit expectation with criteria, a window and a confidence, resolved deterministically and feeding calibration.

Primary uncertainty
:   The deterministic reason a score should be doubted most: a single family, second-hand only, conflicting evidence, little evidence, or an unclear reading.

Prompt (versioned)
:   A stored template for a model role with system text, a user template, an optional JSON schema and an append-only version history.

Provenance
:   The chain from any memory, belief, entity or reply back to the events and sources that produced it.

Redaction
:   The audited replacement of a row's content with a marker, keeping identifiers, links, scores and timestamps, with a ledger entry carrying a hash of what was removed. The only write the append-only store admits.

Reflection
:   A scheduled review of a period's mistakes, resolved contradictions and lessons, recorded as an event and optionally worded by a model.

Reliability (source)
:   A per-source, per-subject record updated only by prediction outcomes and operator corrections, adjusting that source's credibility on that subject once three outcomes exist.

Replay
:   Re-running recorded stimuli through a sandbox fork from a snapshot, labelling each step exact, approximate, diverged or redacted, and the whole run exact-candidate, different-build or incompatible.

Runtime identity
:   The build, schema version, secret-free configuration digest, personality version, policy digest, model routing and prompt versions recorded with every snapshot, replay and experiment.

Role (model)
:   A cognitive task a model may perform: perceive, respond, hypothesise, summarise, judge, narrate, embed. Each routes to a primary model and fallbacks.

Role (user)
:   Observer, operator or administrator; the minimum required is stated for every route.

Self-model
:   The mind's data about itself: identity, what it can do, what needs asking and what is forbidden, kept in step with policy.

Snapshot
:   A stored archive of a mind's state at an event position.

Temporal scope
:   Whether a belief is about something persistent, transient or of unknown duration; transient states carry a validity interval and are superseded rather than contested by later observations.

Trust
:   The level attached to every input at its boundary: operator, user, system, source or low. Weights evidence and bounds effects.

Unknown
:   The belief status for a proposition recorded with no live evidence for or against. Not a low score; the mind says it does not know.

Variant
:   One named override set in an experiment, forked from the same snapshot as its siblings and fed the same stimuli; may change configuration, personality or the model behind each role.

Working memory
:   A decaying buffer of recently active items, slightly larger than the workspace, persisted every cycle.

Workspace
:   The capacity-limited set of thoughts a mind is attending to; admission is by attention score.
