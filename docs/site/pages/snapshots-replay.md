These four tools share one idea: a mind's state is data that can be captured, branched, re-run and compared. They are what make Noema a place to *study* cognition rather than merely run it. Since 0.2 every capture and every re-run also records the **runtime identity** it happened under, so a comparison says up front whether it could have been exact; and an experiment can fork one snapshot into **variants** that differ in exactly one declared thing, including which model serves each role.

{{include docs/snapshots-replay.md}}

## Reading a replay

Each step of a replay shows the stimulus, the events the source mind produced after it, and the events the sandbox produced. Digests normalise identifiers and numbers so that two minds' outputs compare. Read the labels as follows:

- **exact**: the deterministic code did the same thing and any model calls were served from recordings. This is the common case for perception, retrieval, belief updates and workspace admission.
- **approximate**: a model was called live because no recording matched the request hash (a prompt version changed, or the sandbox's state produced a different request). The step is still valid; it is simply not a reproduction.
- **diverged**: no live model call, yet the outcome differed. The usual causes are honest: retrieval scoring depends on wall-clock recency, working memory carried something into the original that the snapshot did not, or the code changed since the recording. Divergence is a finding, not a failure.
- **redacted**: the stimulus was redacted after the recording; it is skipped and labelled rather than reconstructed.

Above the steps, the replay states its **compatibility** with the snapshot: an **exact candidate** ran under the same build, schema, configuration, routing, prompts and policy, so differences can only come from state, timing or a model; a **different build** lists what differs; an **incompatible** replay has a different schema and stops before running rather than produce a misleading comparison. The first non-exact step is analysed into the event digests only the recording produced, those only the replay produced, and the components they belong to, with live model calls named as a component of their own.

## Designing an experiment

An experiment definition is JSON with five parts: `config` (cognitive configuration overrides by field name), `personality` (section-by-section overrides), `stimuli` (a list of observations with text, optional source and salience), `expected` (event types that should or should not appear, optionally containing a phrase) and a `seed` and `duration_seconds` for provenance and bounding. The run forks the baseline snapshot into a disabled subject mind, applies overrides, feeds the stimuli one at a time until the mind settles after each, and records events by type, produced-event digests per stimulus, the subject's beliefs, memory and goal counts, expectation outcomes, model-call counts and the LLM role routing in force. Results are stored, shown in the interface, exportable with an archive of the subject, and the subject stays for comparison with the baseline.

An experiment may instead declare **variants**: named override sets, each becoming its own fork of the same snapshot and receiving the same stimuli. A variant can change configuration, personality and, through `llm_roles`, the model used for each role for that fork only; a missing or disabled model fails the variant rather than falling back silently. The results hold one entry per variant and a deterministic comparison: the first stimulus at which any two diverged, belief overlap and mean score difference on shared propositions, attention overlap, and the ranges of memory, goal, cost and capability-request counts.

Typical experiments: memory decay on and off, dreaming on and off, two personalities on the same stimuli, different attention weights, and the same mind under two providers.
