# Belief system

A **belief** is a proposition the mind holds with an explicit, evidence-derived confidence and a status. Beliefs are never edited in place: every change appends a `belief_versions` row (append-only trigger) and an event.

## Structure

| Table | Contents |
|---|---|
| `beliefs` | proposition, optional subject, epistemic kind, status, confidence, prior, version, source, origin event, operator-pinned status |
| `belief_evidence` | typed references (event, memory, belief, prediction, operator, note) with direction for/against, weight (how much it bears on the proposition), credibility (trust in the source), retracted flag |
| `belief_alternatives` | competing explanations kept alongside (open, dismissed, promoted) |
| `belief_links` | contradicts / supports / alternative_to / derived_from between beliefs |
| `belief_versions` | append-only history with the confidence breakdown and the event that caused each change |
| `corrections` | operator statements: belief wrong, memory inaccurate, do not infer, entity same/different |

## Confidence arithmetic

Confidence is never typed in by a person or produced by a model. It is:

```
value(e)      = weight × credibility                       for each live evidence row e
family(e)     = the independent source e ultimately rests on (an actor, an integration, an operator, a prediction)
contribution(family) = strongest primary item + ¼ × each further primary item, capped at 1.5 × the strongest
                       (if the family has no primary item: its strongest derived item alone)
for           = Σ contribution over supporting families
against       = Σ contribution over opposing families
diversity     = 0.6 + 0.4 × min(1, (supporting families − 1) / 2)
confidence    = sigmoid( logit(prior) + 1.5 × (for × diversity − against) )   clamped to [0.02, 0.98]
```

- The **prior** depends on the epistemic kind: hypothesis 0.25, assumption/opinion 0.4, claim 0.5, observation/memory 0.6, fact 0.8.
- **Families** are the unit of independence. Five messages from one person, the episodes they became, the semantic memory consolidated from them and an inference drawn from that memory are all one family: they carry one person's information however many times it has been re-expressed.
- **Derivations** say how a row relates to its roots. *Primary* derivations bring information in from outside the belief system: `direct`, `assertion`, `operator`, `external`, `prediction`. *Derived* ones re-express it: `consolidation`, `inference`, `reflection`, `dream`, `retrieval`. Derived rows never add weight to a family that already has a primary row, which is what stops recursive cognition inflating confidence.
- **Repetition** within a family is worth a quarter per extra item and never more than half again in total, so a single feed cannot manufacture certainty (threat T4), while three genuinely independent families can.
- **Diversity** counts families, not rows.
- Absolute certainty is unreachable; the bounds are deliberate (spec §10).

Every evidence row records its `derivation`, `family`, `root_ids` (the originating events or memories) and a `provenance_status`. Rows written before provenance existed are marked `legacy_unknown` with `family = source`; no ancestry is invented for them. The breakdown (prior, per-family contributions, for, against, families, diversity, log-odds, formula, notes) is stored with every version and shown on the belief page under "Independent sources".

## Assertions as evidence

Statements people make reach beliefs only through assertions (`docs/conversation.md`). An `assertion` evidence row carries the attributed actor as its family, the source message as its root, and a weight that already reflects hedging, reporting and interpretation confidence. Nothing is silently upgraded: a claim stays a claim until evidence from another family arrives.

## Status

Derived from the breakdown unless an operator pinned it:

- `contested` when both sides carry real weight (against ≥ half of the discounted support) and confidence is mid-range;
- `rejected` below 0.15; `hypothesis` for hypotheses without support or below 0.5; `tentative` below 0.7; `likely` below 0.85; `accepted` above;
- `superseded` when replaced by a newer proposition.

## Contradictions

On creation and on gaining support, a belief is compared with the mind's live beliefs. The deterministic pass detects the same content with flipped polarity (negation words plus token Jaccard ≥ 0.6). A hit emits `contradiction_detected`, links both beliefs, and makes each counter-evidence for the other weighted by the other's confidence, so both move towards `contested` until more evidence arrives. Paraphrase-level judgement is added by an LLM-assisted process once providers exist; it can only propose, and the same arithmetic applies.

## Hypothesis generation

The `associate` process proposes causal hypotheses when two notable observations occur within a window ("deployment … may have caused: latency alert"). Each observation is weak evidence (weight 0.15) so the belief starts as a hypothesis around 0.3–0.4; independent support is needed to move it. Alternatives ("coincidence", "a third factor") are recorded automatically.

## Corrections

- **This belief is wrong** pins the status to rejected, adds operator counter-evidence, records a `belief_wrong` correction and a `belief_retracted` event. History is kept.
- **Do not infer this again** stores a `do_not_infer` correction; future creations of a matching proposition (token Jaccard ≥ 0.7) are refused and logged as metacognition.
- **This memory is inaccurate** retracts the memory and marks belief evidence citing it as retracted; affected beliefs are re-evaluated with a `belief_updated` event.
