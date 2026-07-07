These four tools share one idea: a mind's state is data that can be captured, branched, re-run and compared. They are what make Noema a place to *study* cognition rather than merely run it.

{{include docs/snapshots-replay.md}}

## Reading a replay

Each step of a replay shows the stimulus, the events the source mind produced after it, and the events the sandbox produced. Digests normalise identifiers and numbers so that two minds' outputs compare. Read the labels as follows:

- **exact**: the deterministic code did the same thing and any model calls were served from recordings. This is the common case for perception, retrieval, belief updates and workspace admission.
- **approximate**: a model was called live because no recording matched the request hash (a prompt version changed, or the sandbox's state produced a different request). The step is still valid; it is simply not a reproduction.
- **diverged**: no live model call, yet the outcome differed. The usual causes are honest: retrieval scoring depends on wall-clock recency, working memory carried something into the original that the snapshot did not, or the code changed since the recording. Divergence is a finding, not a failure.

## Designing an experiment

An experiment definition is JSON with five parts: `config` (cognitive configuration overrides by field name), `personality` (section-by-section overrides), `stimuli` (a list of observations with text, optional source and salience), `expected` (event types that should or should not appear, optionally containing a phrase) and a `seed` and `duration_seconds` for provenance and bounding. The run forks the baseline snapshot into a disabled subject mind, applies overrides, feeds the stimuli one at a time until the mind settles after each, and records events by type, produced-event digests per stimulus, the subject's beliefs, memory and goal counts, expectation outcomes, model-call counts and the LLM role routing in force. Results are stored, shown in the interface, exportable with an archive of the subject, and the subject stays for comparison with the baseline.

Typical experiments: memory decay on and off, dreaming on and off, two personalities on the same stimuli, different attention weights, and model comparisons made by changing role routing between runs.
