# Background cognition: metacognition, reflection, consolidation, narrative, notifications

## Causal timeline

Every event records the links it was written with: `causation_id` (the event that directly led to it), `parent_ids` (the events it was assembled from) and evidence references (the events, memories, beliefs and model calls it rests on). The causal timeline (`/events/{id}/timeline`, `GET /api/v1/events/{id}/chain`) walks those links backwards towards causes and forwards towards consequences, up to six hops and two hundred events, and draws each edge with its kind: `caused`, `parent` or `cited`. Events are grouped by the cognitive cycle they belong to, and that grouping is labelled as correlation, not causation. Nothing on the page is inferred; where the system does not know why something happened, no arrow is drawn. Questions the walk answers directly: why a belief exists (walk back to the observations and assertions it cites), what changed it (the `belief_updated` events downstream of an outcome), which memories were retrieved and which thoughts won attention in the cycle, which model call contributed (an artefact on the node), and which later decisions depended on it (walk forward).

These subsystems run beside the cognitive cycle. Each is deterministic in what it concludes; a language model may only word things, and only within a configured budget.

## Metacognition (per cycle)

The `metacognition` process audits the mind's own reasoning with explicit checks and proposes warnings that compete for attention like any thought:

| Check | Fires when | Effect when admitted |
|---|---|---|
| insufficient_evidence | a belief is at ≥ 0.7 with a single supporting item | counter-evidence (weight ≈ 0.25) from `metacognition` lowers it |
| single_source | all support for a ≥ 0.6 belief comes from one source | counter-evidence |
| fluent_without_evidence | a belief rests only on model output or free notes | counter-evidence |
| repeated_failure | three or more hypotheses about one subject rejected in a month | warning |
| stale_contradiction | a contested belief unresolved for a day | an investigation goal |
| overconfidence | calibration shows stated confidence running ahead of outcomes | warning, notification |

Each warning is a `metacognition` event with `check` and `ref` metadata; the same check on the same target is not repeated within a day. Warnings appear on the mind overview.

## Reflection (scheduled, per mind)

`reflection.Service.Run` reviews the period since the last reflection: prediction errors, rejected beliefs, operator corrections, belief status changes, completed goals, contradictions and calibration. Repeated failures on one subject become a **pattern**, and each new pattern is written once as a procedural memory ("Lesson: … seek an independent source before raising confidence"). The findings are stored structurally with a plain rendering; the `reflect` prompt may rewrite the prose when the `narrate` role has a model and the mind's reflection budget allows. A `reflection` event carries everything. Operators can run it now from the Background tab or `noemactl reflect`.

## Consolidation (scheduled, per mind)

`consolidation.Service.Run` takes the last 30 days of episodes not yet consolidated, clusters them deterministically by shared stemmed terms (speaker prefixes stripped), and for each cluster of at least `consolidation_min_episodes` episodes creates:

- a **belief** (epistemic kind inference) whose confidence is computed from the episodes as evidence (weight 0.3 each, credibility from trust, single-source discount), and
- a **semantic memory** with the same proposition, linked to every episode by `derived_from`.

The proposition is a plain deterministic sentence unless the `consolidate` prompt (summarise role) returns a supported generalisation within the job's model budget; the job records which. Episodes are never modified. Jobs record inputs, outputs and errors; operators can run now, pause scheduled runs globally, and reject a generalisation (memory retracted, belief pinned rejected, episodes untouched). Demo scenario 3 ("user repeatedly selects Go") produces a generalisation this way.

## Narrative (after cycles)

The narrative hook builds a five-part structure from state: doing (top workspace thoughts), why (recent stimuli), recently (notable emitted events), resolving (contested beliefs, active goals), next (open predictions). It is rendered as short first-person prose and stored with a `narrative_updated` event when it changes. With `narrative_llm` enabled and a `narrate` model, the prompt may rephrase it; it cannot add content.

## Notifications (after cycles)

`notifications.Rules` maps events to candidates with explicit thresholds: confident prediction misses (≥ 0.7), clear contradictions, strong beliefs (only for minds whose personality says notify proactively, above `notify_min_confidence` and `notify_min_salience`), completed goals, overconfidence or loop warnings, denied actions, and very salient observations. Repeats sharing an aggregate key within `notify_cooldown` increment the existing notification instead of creating another, and are recorded as `notification_suppressed` events. Every notification stores the rule and the values that fired it.

## Modes

While reflection or consolidation runs for a mind, its runtime mode is switched to `reflecting` or `consolidating` and back, with `mode_changed` events, so the dashboard shows what the mind is doing.
