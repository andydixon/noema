# Personality

A mind's personality is a **structured document**, not a system prompt. It lives in `personality_versions` (append-only) and the mind row points at the current version.

## Structure

| Section | Fields | Notes |
|---|---|---|
| Identity | name, display name, description, role, pronouns, backstory, origin | The only free-text part prompts always include |
| Values | privacy, evidence, kindness, curiosity, accuracy, autonomy, caution, creativity, persistence | 0–1, fixed vocabulary |
| Traits | curiosity, scepticism, sociability, humour, patience, verbosity, confidence, risk tolerance, novelty preference | 0–1, fixed vocabulary |
| Communication | language, dialect, formality, verbosity, humour style, terminology | |
| Behaviour | challenge assumptions, ask follow-ups, prefer evidence, cite uncertainty, offer alternatives, notify proactively | booleans |
| Free text | additional character notes | supplements, never replaces, the structured fields |

Unknown keys are dropped and numbers are clamped on every write (`Personality.Normalise`).

## Brief

`Personality.Brief()` renders the structured data into a short deterministic paragraph. Cognitive prompts receive the brief, not the JSON, and the editor's *Preview* shows exactly that text. Because it is deterministic it is unit-tested.

## Versions

Every save, import, restore or template application appends a version with a change note and author. Restoring an old version writes a *new* version copying it. The diff view compares any two versions field by field. Deleting history is impossible: the table has an append-only trigger.

## Templates and overrides

A template is a reusable personality. When a mind is based on a template, each version also records the mind's **overrides**: the fields that differ from the template. Re-applying the template (for example after editing it) recomputes `template + overrides`, so mind-specific changes survive. Editing a template never changes minds implicitly; an operator re-applies it and that action is audited.

## Traits and the runtime

The runtime consumes the personality as follows:

- attention weights are biased by traits such as curiosity and novelty preference;
- affect baselines derive from traits (patience, confidence);
- the response generator receives the brief and the behavioural preferences;
- notification thresholds honour *notify proactively*.

## Audit

`personality.update`, `personality.import`, `personality.restore`, `personality.template`, `template.create/update/delete` entries are written to the audit log with the field-level diff.
