The cognitive cycle is the heart of a mind. It is deterministic code: given the same state, the same new events and the same recorded model responses, it produces the same thoughts in the same order. This page describes each stage, the processes that feed it, the modes that change it, and the metacognitive checks that watch it.

## One cycle, end to end

1. **Load state.** The runtime holds the mind, its self-model, mode, cycle counter, workspace and working memory, active goals, drives, affect and the effective attention weights. State is persisted after every cycle and reloaded on start, so restarts are seamless.
2. **Read new events.** Events after the mind's cursor are read in sequence order. An event must have been recorded for at least a short settle window before it is seen, so backdated or in-flight events are not consumed half-written. A cycle triggered by a stimulus only proceeds if at least one of the new events came from outside the brain; a mind's own output never re-triggers it.
3. **Observe.** Every registered cognitive **process** looks at the state and the new events and returns **thought candidates**: a kind, a subject, a summary, a salience and confidence, evidence references and a feature vector. Processes run concurrently in a bounded pool with per-process timeouts and panic isolation; a slow or broken process cannot stall the cycle.
4. **Score.** Attention scoring computes ten features for each candidate, multiplies by the effective weights, and orders candidates by score with a stable tie-break on identifier. Every candidate is recorded as a `thought_candidate` event with its breakdown.
5. **Admit.** The top candidates enter the workspace up to capacity, displacing the weakest existing thoughts. Admissions are `thought_selected` events; the new workspace is a `workspace_broadcast` event; working memory decays and refreshes.
6. **Dispatch.** The executive runs the handler registered for each admitted thought's kind: encode a memory, create or update a belief, create a goal, register a prediction, generate a response, request an action. Handlers share the cycle's model-call cap.
7. **Hooks and commit.** After-cycle hooks update motivation, narrative and notifications. The whole cycle runs in one database transaction; events are published to live subscribers only after commit, so nobody ever sees a thought that was rolled back.
8. **Advance.** The cursor moves past the events that were read, a `cycle_completed` event records timing and counts, and the loop detector records the fingerprints of what was admitted.

## The processes

| Process | Produces | Deterministic? |
|---|---|---|
| Attend | candidates from new perception events, weighted by their salience, trust and urgency | yes |
| Retrieve | recalled memories relevant to what is in the workspace, with score breakdowns | yes (semantic component needs an embedding model) |
| Associate | low-confidence causal hypotheses when notable observations arrive close together | yes |
| Perceive | structured perception of messages and documents (entities, claims, questions, intent) | deterministic analysis, optionally enriched by a model |
| Goal monitor | progress, blockers and stalls on active goals | yes |
| Curiosity | questions about uncertain or contested beliefs, which can become investigation goals | yes |
| Metacognition | warnings about loops, contradictions, overconfidence and budget pressure | yes |
| Act | action requests when a thought calls for a capability | yes; execution goes through policy |

Each process is a small Go type registered in code; there is no plugin loading and nothing a mind can do to add or change them.

## Attention features

- **salience** from the event or memory that produced the candidate;
- **urgency** from time words, alert-like vocabulary and deadlines;
- **novelty** from how unlike recent workspace contents the candidate is;
- **affect** from the mind's current arousal and concern;
- **goal_relevance** from lexical overlap with active goals, weighted by priority;
- **consequence** from the candidate's kind (an action request weighs more than a recollection);
- **confidence** from the candidate's own confidence;
- **contradiction** when the candidate conflicts with a held belief;
- **operator_priority** when an operator injected or pinned it;
- **context_relevance** from overlap with the current conversation.

The weights are per mind (see [configuration](configuration)), modulated each cycle by drives and affect within bounds, and the effective values are stored on every selection event.

## Modes

`awake` is the normal state. `focused` narrows admission to a conversation's context. `idle` lengthens the timer. `reflecting`, `consolidating` and `dreaming` are entered by the scheduler around the corresponding background job and left afterwards. `paused` stops cycles entirely until an operator resumes; events still accumulate and are processed on resume. All transitions are `mode_changed` events with a reason.

## Metacognition

The metacognition process watches for the failure modes of a system that thinks in a loop: the same candidate fingerprint winning repeatedly (loop detector), a belief whose confidence moved far on little evidence, a contradiction left unresolved for too long, and model or cycle budgets running low. Each produces a `metacognition` event, a workspace warning and, when thresholds are crossed, a notification to the operator. Loops also suppress the repeating candidate for a while.

## The causal timeline

Every event records the links it was written with: the event that directly caused it, the events it was assembled from, and the evidence it cites. The timeline page for any event walks those links backwards to causes and forwards to consequences, up to six hops and two hundred events, and draws each edge as caused, parent or cited. Events in the same cycle are grouped and the grouping is labelled correlation, because sharing a cycle is not a cause. Nothing on the page is inferred: where the system did not record why something happened, no arrow is drawn. It answers "why does this belief exist", "what changed it", "which model call contributed" and "what later depended on it" from the record alone.

## Operator controls

From the interface or `noemactl workspace`, an operator can trigger a cycle, pause and resume, reset the workspace or the motivation state, and reload configuration. Every control is an `operator_action` event in the mind's own history and an entry in the audit log, and the mind encodes it as an autobiographical memory: it knows it was changed.

{{include docs/cognition.md}}
