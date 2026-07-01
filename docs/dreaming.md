# Dreaming

Dreaming is an optional idle mode (spec §19). It is **off by default**. When
enabled, and only inside the allowed hours, a mind periodically:

1. **Replays** a deterministic sample of recent episodic and semantic memories
   (the sample is a function of the dream id, so a dream can be replayed).
2. **Associates** pairs that share vocabulary (Jaccard within a band, excluding
   near-duplicates and same-episode pairs) but were never linked. Each new link
   is a `similar_to` relationship with low confidence and a `dream_association`
   event citing both memories.
3. **Proposes** speculative hypotheses for the strongest pairs. Wording is
   deterministic unless a budgeted model call (role `hypothesise`, prompt
   `dream`) offers a better one. The model never decides truth.

## Guarantees

- **Dreams cannot act.** `Run` marks its context forbidden for the capability
  layer before doing anything; any `Invoke` reached from a dream returns
  `ErrForbiddenContext`. There is no code path from dreaming to an executor.
- **Low confidence by construction.** Every hypothesis is a belief with
  epistemic kind `hypothesis` (prior 0.25) and two memory citations at weight
  0.1, giving roughly 0.30. The test suite asserts ≤ 0.35. Confidence can only
  rise through ordinary evidence.
- **Nothing is rewritten.** Sampled memories are untouched. Rejecting a
  hypothesis retracts its belief; promoting adds an operator endorsement as
  evidence, so the arithmetic stays inspectable on the belief page.
- **Budgeted.** Per-mind model-call, memory and hypothesis caps; a global pause
  switch; scheduled dreams need enabled + hours window + interval elapsed.

## Operating

- UI: mind → **Dreams**. Review proposed hypotheses (promote / reject), see
  dreams with counts, change settings (administrator).
- API: `GET /api/v1/minds/{mind}/dreams`, `POST …/dreams/run`,
  `POST …/dreams/settings`, `POST …/dreams/hypotheses/{id}/promote|reject`,
  `POST /api/v1/dreams/pause`.
- CLI: `noemactl dreams <mind> list|run|promote <id> [note]|reject <id> [reason]`.

Settings (`CognitiveConfig`): `dreaming_enabled`, `dream_hours` (`HH-HH` local,
may wrap midnight, blank = any), `dream_interval`, `dream_llm_calls`,
`dream_max_memories`, `dream_max_hypotheses`.
