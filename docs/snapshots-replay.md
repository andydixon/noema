# Snapshots, forks, export/import, replay and experiments

## Snapshots and forks (spec §42)

A **snapshot** is a JSON archive of a mind's state at a point in its history:
identity, personality, configuration, workspace and working memory, drives and
affect, goals, beliefs (with evidence, versions, alternatives, links), memories
(with relationships), predictions, entities, corrections, policy rules (kept by forks, never imported from archives) and
conversations. It records the event sequence number at capture and a SHA-256
checksum. Cognitive events are **not** copied.

A **fork** restores a snapshot into a new mind. Every row id is remapped, so
the fork never shares mutable rows with its source; references to events keep
pointing at the source's immutable history, which is how the past before the
fork is shared by reference (`forked_from_mind`, `forked_from_snapshot`,
snapshot `event_seq`). After the fork the two minds diverge freely.

**Compare** (`/minds/{a}/compare?with={b}`, `noemactl compare`) is
deterministic: counts, mean confidence, belief-by-belief differences keyed on
the normalised proposition, personality and configuration field diffs, and
the event-type mix.

## Export / import (spec §43)

Export produces the same archive (`kind: noema.mind-archive`, `version: 1`,
checksum). Nothing secret is included: provider keys, integration secret
references, API tokens and sessions are not mind state and are never in an
archive. Import treats the archive as hostile: size cap (32 MiB), unknown
top-level fields rejected, allow-listed tables only, row caps, column-name
validation, checksum must match, every id remapped, `mind_id` forced. Text
inside an archive is data; a prompt-injection string in a memory is imported
as a memory, never executed.

## Replay (spec §41)

`Replay` rebuilds a mind from a snapshot into a **disabled sandbox mind**,
then re-injects the source's recorded stimuli (perception and operator events
after the snapshot) one at a time, stepping the real cycle code synchronously
until the mind settles. Each step compares the events the source produced
after that stimulus with the events the sandbox produced, ignoring
bookkeeping types and normalising ids and numbers:

| Label | Meaning |
|---|---|
| **exact** | same events, and every model call was served from a recording |
| **approximate** | a live model call was needed; the recorded response did not match the request hash |
| **diverged** | no live model call, yet the deterministic code produced different events |

Divergence is reported, not hidden. Its usual causes are honest ones: the
working memory changed between snapshot and stimulus, retrieval scores depend
on wall-clock recency, or the code changed since the recording.

### Runtime identity and compatibility

Every snapshot, replay and experiment records the **runtime identity** it ran under: build (module version, VCS commit, whether the tree was modified, commit time, Go version, OS and architecture), schema version (highest applied migration), a digest of the non-secret configuration (secrets and credentials never enter it; it changes only when behaviour-affecting settings change), the mind's personality version, a digest of the policies in force, the LLM role routing (including per-mind overrides), the current version of every prompt, the versions of the deterministic subsystems whose output is compared, and the timezone.

A replay compares its identity with the snapshot's and labels itself:

| Compatibility | Meaning |
|---|---|
| **exact-candidate** | same build, schema, configuration, routing, prompts, policy and subsystem versions: differences can only come from state, timing or a model |
| **different-build** | the schema matches but something else differs; the differences are listed. Exact steps are still exact; a diverged step may be a code change |
| **incompatible** | the schema differs. The sandbox could not be a faithful reconstruction, so the replay stops before running rather than produce a misleading comparison |

Snapshots taken before identities were recorded are labelled as such.

### Divergence analysis

The first non-exact step is analysed: which event digests appear only in the recording, which only in the replay, and which components those events belong to (retrieval, attention, encoding, beliefs, association, predictions, goals, response, narrative, motivation, metacognition, capabilities). Components are inferred from event types and labelled as inferred: they point at where to look, not at a proven cause. Live model calls are named as a component of their own, because provider nondeterminism is a real and measurable source of difference, not one to be hidden.

The replaying router serves recorded responses by request hash
(`llm_calls.request_hash`) and records the replayed call with `replayed=true`.
Replays run under a forbidden capability context; no action can execute.

## Experiments (spec §57)

An experiment is a snapshot, optional configuration and personality
overrides, a stimulus script, expectations, a seed and a duration cap. Running
it forks the snapshot into a disabled subject mind, applies overrides, feeds
the stimuli and records: events by type, produced-event digests per stimulus,
subject beliefs, memory and goal counts, expectation outcomes, model-call
counts (live/replayed), the LLM role routing in force, duration and cycles.
Results are stored and exportable (`/api/v1/experiments/{id}/export` includes
an archive of the subject). The subject stays for comparison with the
baseline.

Different models are compared by changing role routing between runs; the
routing in force is recorded in each result. The seed is recorded with every
stimulus for provenance; deterministic code needs no seed, and model
providers do not honour one reliably.

## Operating

- UI: mind → **Snapshots** (take, fork, download, compare, import on the minds page), mind → **Replay**, top-level **Experiments**.
- API: `/api/v1/minds/{mind}/snapshots`, `/api/v1/snapshots/{id}[/archive|/fork]`, `/api/v1/minds/{mind}/export`, `/api/v1/minds/import-archive`, `/api/v1/minds/{mind}/compare/{other}`, `/api/v1/minds/{mind}/replays`, `/api/v1/replays/{id}`, `/api/v1/experiments[...]`.
- CLI: `noemactl snapshots`, `noemactl compare`, `noemactl replay`, `noemactl experiments`.
