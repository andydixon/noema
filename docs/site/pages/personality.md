Personality is the stable part of a mind's identity: who it says it is, what it values, how it tends to behave, and how it speaks. It is data, versioned append-only, and it influences cognition in specific, inspectable ways rather than being pasted into a prompt.

## What a personality contains

| Section | Fields | Used for |
|---|---|---|
| Identity | name, display name, description, role, pronouns, backstory, origin | the self-model, introspective answers, the response voice |
| Values (0–1) | privacy, evidence, kindness, curiosity, accuracy, autonomy, caution, creativity, persistence | drive baselines (accuracy raises uncertainty reduction; persistence raises task completion; evidence raises consistency) |
| Traits (0–1) | curiosity, scepticism, sociability, humour, patience, verbosity, confidence, risk_tolerance, novelty_preference | drive baselines (curiosity, novelty preference, sociability, risk tolerance), affect baselines, attention modulation |
| Communication | language, dialect, formality, verbosity, humour style, terminology | deterministic response rendering and the respond prompt |
| Behaviour (flags) | challenge assumptions, ask follow-ups, prefer evidence, cite uncertainty, offer alternatives, notify proactively | executive choices: whether to append alternatives, whether to ask before answering, whether to raise notifications |
| Free text | anything else | shown to the model as data inside a delimited block, never as instructions |

Values and traits are normalised on every write: unknown keys are dropped, missing keys filled from defaults, and every number clamped to [0, 1]. The default personality is a neutral, evidence-first starting point with evidence, accuracy and privacy raised and scepticism slightly above the middle.

## Versions, templates and history

Every save creates a new **personality version** with a change note and author; the table is append-only and the mind row points at the current version. The interface shows a diff between any two versions and can restore an older one (as a new version, so the history stays intact). **Templates** are named personalities you can start a mind from; a mind created from a template records the template and its overrides. Import and export of a personality is plain JSON.

## How personality reaches cognition

- **Drives.** Baselines are computed from values and traits when a mind is created and when personality changes (see [motivation](motivation)).
- **Affect.** The starting affect vector takes curiosity from the trait and concern from risk tolerance.
- **Attention.** Effective attention weights are the configured weights modulated by drives and affect; personality therefore shifts what wins the workspace, within bounds, without ever overriding operator priority.
- **Response.** Deterministic rendering picks formality, verbosity and whether to state uncertainty and alternatives from communication and behaviour settings; when a model words the answer it receives the same settings and the response intent, and the intent is what the answer must stay grounded in.
- **Self-model.** The mind's self-model (what it is, what it can and cannot do, which capabilities it currently has) is built from identity plus live policy state, so an introspective question is answered from data, not from a persona prompt.

{{include docs/personality.md}}
